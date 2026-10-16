//go:build legacy_patched

// 本文件保留私有 observer 与读体前快照；当前名单/上限时点见 TestOfficialBeforeUsesCurrentConfiguration，回复前保存见 TestHTTPProtocolsBeforeReply。
package system

import (
	"encoding/json"
	"fmt"
	"maps"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestSnapshotBridgeFailureRemainsVisible 验证损坏合成配置导致prepare unavailable后，管理状态持续显示缺口且模型调用不变。
func TestSnapshotBridgeFailureRemainsVisible(t *testing.T) {
	upstream, received := snapshotUpstream()
	defer upstream.Close()
	h := startHost(t, 1<<30, true, upstream.URL)
	h.monitorAll(t)
	h.stop()
	stateDir := filepath.Join(h.dir, "audit")
	configPath := filepath.Join(stateDir, "monitors.json")
	if _, err := os.ReadFile(configPath); err != nil {
		t.Fatal("桥接故障合成配置读取失败")
	}
	// 只替换本测试生成的配置；公开HTTP触发正常生产解析失败，不增加故障后门。
	if err := os.WriteFile(configPath, []byte(`{"version":999,"capture_paused":false,"objects":{}}`), 0600); err != nil {
		t.Fatal("桥接故障合成配置写入失败")
	}
	h = startHost(t, 1<<30, true, upstream.URL, stateDir)
	_, _, initialGaps := snapshotCounters(t, h)
	if initialGaps["observer_unavailable"] != 0 {
		t.Fatal("尚未提交前已有桥接缺口")
	}
	marker := fmt.Sprintf("SNAPSHOT-BRIDGE-FAULT-%d", time.Now().UnixNano())
	for round := 1; round <= 2; round++ {
		input := fmt.Sprintf("%s-%d", marker, round)
		code, _ := h.request(t, "POST", "/v1/chat/completions", keyA, snapshotInput(t, input))
		if code != http.StatusOK {
			t.Fatal("桥接失败阻断模型调用")
		}
		verifySnapshotUpstream(t, received, input)
		_, _, gaps := snapshotCounters(t, h)
		if gaps["observer_unavailable"] != uint64(round) {
			t.Fatal("prepare不可用没有在管理员状态形成可见桥接缺口")
		}
		for query := 0; query < 2; query++ {
			_, _, refreshed := snapshotCounters(t, h)
			if refreshed["observer_unavailable"] != gaps["observer_unavailable"] {
				t.Fatal("查询状态重复累计或清除了历史桥接缺口")
			}
		}
		code, raw := h.request(t, "GET", api+"/status", adminKey, nil)
		var status struct {
			State struct {
				Reason string `json:"reason"`
			} `json:"state"`
		}
		if code != http.StatusOK || json.Unmarshal(raw, &status) != nil || status.State.Reason != "storage_error" {
			t.Fatal("历史桥接缺口遮蔽了仍活动的存储故障")
		}
	}
}

// TestSnapshotAdmissionIsFixedBeforeBodyRead 验证未监控提交不会因读体期间加入名单而被追溯采集。
func TestSnapshotAdmissionIsFixedBeforeBodyRead(t *testing.T) {
	upstream, received := snapshotUpstream()
	defer upstream.Close()
	h := startHost(t, 1<<30, true, upstream.URL)
	marker := fmt.Sprintf("SNAPSHOT-ADMISSION-%d", time.Now().UnixNano())
	complete := beginSnapshotSubmission(t, h, snapshotInput(t, marker+"-BEFORE"))
	h.monitorAll(t)
	complete()
	code, _ := h.request(t, "POST", "/v1/chat/completions", keyA, snapshotInput(t, marker+"-AFTER"))
	if code != http.StatusOK {
		t.Fatal("下一提交模型调用失败")
	}
	waitSnapshotRecord(t, h, marker+"-AFTER")
	for _, suffix := range []string{"-BEFORE", "-AFTER"} {
		select {
		case input := <-received:
			if input != marker+suffix {
				t.Fatal("快照操作改变了可控上游收到的本轮输入")
			}
		case <-time.After(5 * time.Second):
			t.Fatal("本轮输入没有到达可控上游")
		}
	}
	// 下一条已同步记录证明先前同步观察已排空；不能用旧历史条数或固定等待证明没有采集。
	for _, record := range snapshotRecords(t, h) {
		if strings.HasPrefix(record.Text, marker+"-BEFORE") {
			t.Fatal("未监控提交被读体期间的新名单追溯采集")
		}
	}
}

// TestSnapshotRemovalAndPausePreserveAcceptedSubmission 验证同实例两轮移除/主动暂停不取消已接收副本。
func TestSnapshotRemovalAndPausePreserveAcceptedSubmission(t *testing.T) {
	upstream, received := snapshotUpstream()
	defer upstream.Close()
	h := startHost(t, 1<<30, true, upstream.URL)
	h.monitorAll(t)
	seedMarker := fmt.Sprintf("SNAPSHOT-OBJECT-%d", time.Now().UnixNano())
	code, _ := h.request(t, "POST", "/v1/chat/completions", keyA, snapshotInput(t, seedMarker))
	if code != http.StatusOK {
		t.Fatal("对象定位提交失败")
	}
	object := waitSnapshotRecord(t, h, seedMarker).ObjectID
	verifySnapshotUpstream(t, received, seedMarker)
	for round := 1; round <= 2; round++ {
		for _, operation := range []string{"remove", "pause"} {
			t.Run(fmt.Sprintf("%s-%d", operation, round), func(t *testing.T) {
				snapshotMutation(t, h, "/pause", map[string]bool{"paused": false})
				snapshotMutation(t, h, "/monitor", map[string]string{"object_id": object, "name": "快照对象"})
				beforeSaved, beforeQueued, beforeGaps := snapshotCounters(t, h)
				if beforeQueued != 0 {
					t.Fatal("前轮快照未排空")
				}
				marker := fmt.Sprintf("SNAPSHOT-ACCEPTED-%s-%d-%d", operation, round, time.Now().UnixNano())
				complete := beginSnapshotSubmission(t, h, snapshotInput(t, marker+"-BEFORE"))
				if operation == "remove" {
					snapshotMutation(t, h, "/remove", map[string]string{"object_id": object})
				} else {
					snapshotMutation(t, h, "/pause", map[string]bool{"paused": true})
				}
				complete()
				code, _ := h.request(t, "POST", "/v1/chat/completions", keyA, snapshotInput(t, marker+"-AFTER"))
				if code != http.StatusOK {
					t.Fatal("移除或暂停阻断了下一模型调用")
				}
				accepted := waitSnapshotRecord(t, h, marker+"-BEFORE")
				if accepted.ObjectID != object {
					t.Fatal("接收快照被关联到其他对象")
				}
				verifySnapshotUpstream(t, received, marker+"-BEFORE", marker+"-AFTER")
				assertSnapshotSkipped(t, h, marker+"-AFTER")
				finalSaved, finalQueued, finalGaps := snapshotCounters(t, h)
				if finalSaved != beforeSaved+1 || finalQueued != 0 || !maps.Equal(finalGaps, beforeGaps) {
					t.Fatal("主动跳过丢失已接收副本、产生缺口或未排空")
				}
			})
		}
	}
}

// TestSnapshotReactivationOnlyAffectsNextSubmission 验证同实例两轮重新加入/恢复不会追溯采集慢体。
func TestSnapshotReactivationOnlyAffectsNextSubmission(t *testing.T) {
	upstream, received := snapshotUpstream()
	defer upstream.Close()
	h := startHost(t, 1<<30, true, upstream.URL)
	h.monitorAll(t)
	seedMarker := fmt.Sprintf("SNAPSHOT-REACTIVATE-OBJECT-%d", time.Now().UnixNano())
	code, _ := h.request(t, "POST", "/v1/chat/completions", keyA, snapshotInput(t, seedMarker))
	if code != http.StatusOK {
		t.Fatal("对象定位提交失败")
	}
	object := waitSnapshotRecord(t, h, seedMarker).ObjectID
	verifySnapshotUpstream(t, received, seedMarker)
	for round := 1; round <= 2; round++ {
		for _, operation := range []string{"rejoin", "resume"} {
			t.Run(fmt.Sprintf("%s-%d", operation, round), func(t *testing.T) {
				snapshotMutation(t, h, "/pause", map[string]bool{"paused": false})
				snapshotMutation(t, h, "/monitor", map[string]string{"object_id": object, "name": "快照对象"})
				beforeSaved, beforeQueued, beforeGaps := snapshotCounters(t, h)
				if beforeQueued != 0 {
					t.Fatal("前轮快照未排空")
				}
				if operation == "rejoin" {
					snapshotMutation(t, h, "/remove", map[string]string{"object_id": object})
				} else {
					snapshotMutation(t, h, "/pause", map[string]bool{"paused": true})
				}
				marker := fmt.Sprintf("SNAPSHOT-REACTIVATE-%s-%d-%d", operation, round, time.Now().UnixNano())
				complete := beginSnapshotSubmission(t, h, snapshotInput(t, marker+"-BEFORE"))
				if operation == "rejoin" {
					snapshotMutation(t, h, "/monitor", map[string]string{"object_id": object, "name": "快照对象"})
				} else {
					snapshotMutation(t, h, "/pause", map[string]bool{"paused": false})
				}
				complete()
				code, _ := h.request(t, "POST", "/v1/chat/completions", keyA, snapshotInput(t, marker+"-AFTER"))
				if code != http.StatusOK {
					t.Fatal("恢复后的模型调用失败")
				}
				accepted := waitSnapshotRecord(t, h, marker+"-AFTER")
				if accepted.ObjectID != object {
					t.Fatal("重新加入或恢复改变了对象关联")
				}
				verifySnapshotUpstream(t, received, marker+"-BEFORE", marker+"-AFTER")
				assertSnapshotSkipped(t, h, marker+"-BEFORE")
				finalSaved, finalQueued, finalGaps := snapshotCounters(t, h)
				if finalSaved != beforeSaved+1 || finalQueued != 0 || !maps.Equal(finalGaps, beforeGaps) {
					t.Fatal("恢复操作追溯采集、产生缺口或未排空")
				}
			})
		}
	}
}
