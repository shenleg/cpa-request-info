// 本文件通过标准 HTTP 慢体屏障和受保护管理接口验证接收快照，不访问私有观察者或队列。
package system

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"maps"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// snapshotInput 构造只含本轮 marker 的合成提交；返回协议正文，编码失败终止测试。
func snapshotInput(t *testing.T, marker string) []byte {
	t.Helper()
	body, err := json.Marshal(map[string]any{"model": "audit-test-model", "messages": []map[string]string{{"role": "user", "content": marker}}})
	if err != nil {
		t.Fatal("合成提交编码失败")
	}
	return body
}

// beginSnapshotSubmission 发送已认证 chunked 请求头并等待100屏障；body 是稍后释放的合成正文。
// 返回完成提交的闭包；连接、屏障或最终响应失败终止测试，清理仅关闭本轮连接，不保存凭据。
func beginSnapshotSubmission(t *testing.T, h *instance, body []byte) func() {
	t.Helper()
	u, err := url.Parse(h.base)
	if err != nil {
		t.Fatal("合成入口无效")
	}
	conn, err := net.DialTimeout("tcp", u.Host, 5*time.Second)
	if err != nil {
		t.Fatal("慢体连接失败")
	}
	t.Cleanup(func() { _ = conn.Close() })
	if err := conn.SetDeadline(time.Now().Add(15 * time.Second)); err != nil {
		t.Fatal("慢体期限设置失败")
	}
	// 小 Content-Length 正文会被宿主日志中间件在鉴权前读取；chunked 避免把提前100误当快照屏障。
	if _, err := fmt.Fprintf(conn, "POST /v1/chat/completions HTTP/1.1\r\nHost: %s\r\nAuthorization: Bearer %s\r\nContent-Type: application/json\r\nTransfer-Encoding: chunked\r\nExpect: 100-continue\r\nConnection: close\r\n\r\n", u.Host, keyA); err != nil {
		t.Fatal("慢体请求头发送失败")
	}
	reader := bufio.NewReader(conn)
	request := &http.Request{Method: "POST"}
	response, err := http.ReadResponse(reader, request)
	if err != nil || response.StatusCode != http.StatusContinue {
		t.Fatal("未取得读体100屏障")
	}
	if err := response.Body.Close(); err != nil {
		t.Fatal("100响应关闭失败")
	}
	return func() {
		t.Helper()
		if _, err := fmt.Fprintf(conn, "%x\r\n%s\r\n0\r\n\r\n", len(body), body); err != nil {
			t.Fatal("慢体正文发送失败")
		}
		final, err := http.ReadResponse(reader, request)
		if err != nil {
			t.Fatal("慢体最终响应读取失败")
		}
		defer func() { _ = final.Body.Close() }()
		if _, err := io.Copy(io.Discard, final.Body); err != nil || final.StatusCode != http.StatusOK {
			t.Fatal("快照操作改变了实际模型调用")
		}
		if err := conn.Close(); err != nil {
			t.Fatal("慢体连接关闭失败")
		}
	}
}

// snapshotRecords 查询受保护的已保存列表；返回独立解码记录，HTTP或解码失败终止测试。
func snapshotRecords(t *testing.T, h *instance) []savedRecord {
	t.Helper()
	code, raw := h.request(t, "GET", api+"/records?limit=100", adminKey, nil)
	var list struct {
		Records []struct {
			ID       string `json:"id"`
			ObjectID string `json:"object_id"`
			Preview  string `json:"preview"`
		} `json:"records"`
	}
	if code != http.StatusOK || json.Unmarshal(raw, &list) != nil {
		t.Fatal("快照已保存列表不可用")
	}
	result := make([]savedRecord, 0, len(list.Records))
	for _, record := range list.Records {
		result = append(result, savedRecord{ID: record.ID, ObjectID: record.ObjectID, Text: record.Preview})
	}
	return result
}

// waitSnapshotRecord 等待本轮 marker 对应的唯一记录并核对详情；返回已保存记录，超时或不一致终止测试。
func waitSnapshotRecord(t *testing.T, h *instance, marker string) savedRecord {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		for _, record := range snapshotRecords(t, h) {
			if record.Text == marker {
				code, raw := h.request(t, "GET", api+"/detail?id="+url.QueryEscape(record.ID), adminKey, nil)
				var detail savedRecord
				if code != http.StatusOK || json.Unmarshal(raw, &detail) != nil || detail.ID != record.ID || detail.Text != marker {
					t.Fatal("快照记录详情与本轮提交不符")
				}
				return detail
			}
		}
		if time.Now().After(deadline) {
			t.Fatal("本轮快照记录未保存")
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// snapshotMutation 通过管理公开写入口提交payload；成功无返回值，编码或HTTP拒绝终止测试。
func snapshotMutation(t *testing.T, h *instance, path string, payload any) {
	t.Helper()
	body, err := json.Marshal(payload)
	if err != nil {
		t.Fatal("快照配置编码失败")
	}
	code, _ := h.request(t, "POST", api+path, adminKey, body)
	if code != http.StatusOK {
		t.Fatalf("快照配置操作失败: route=%s HTTP=%d", path, code)
	}
}

// snapshotCounters 查询公开保存/排队/缺口计数；返回独立状态，HTTP或解码失败终止测试。
func snapshotCounters(t *testing.T, h *instance) (int64, int64, map[string]uint64) {
	t.Helper()
	code, raw := h.request(t, "GET", api+"/status", adminKey, nil)
	var status struct {
		State struct {
			Saved  int64             `json:"saved"`
			Queued int64             `json:"queued"`
			Gaps   map[string]uint64 `json:"gaps"`
		} `json:"state"`
	}
	if code != http.StatusOK || json.Unmarshal(raw, &status) != nil || status.State.Gaps == nil {
		t.Fatal("快照运行状态不可用")
	}
	return status.State.Saved, status.State.Queued, status.State.Gaps
}

// snapshotUpstream 启动只接受合成文本的可控模型；返回入口和收到的内容，读取错误以空标记提供观察。
// 上游仅在本地运行，调用者负责Close；通道容量覆盖两轮测试，不输出或持久化正文。
func snapshotUpstream() (*httptest.Server, <-chan string) {
	received := make(chan string, 32)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request struct {
			Messages []struct {
				Role    string `json:"role"`
				Content string `json:"content"`
			} `json:"messages"`
		}
		input := ""
		if json.NewDecoder(r.Body).Decode(&request) == nil && len(request.Messages) == 1 && request.Messages[0].Role == "user" {
			input = request.Messages[0].Content
		}
		select {
		case received <- input:
		default:
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"id":"synthetic-snapshot","object":"chat.completion","choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}]}`)
	}))
	return upstream, received
}

// verifySnapshotUpstream 核对本轮顺序完整文本到达上游；成功无返回值，不一致或超时终止测试。
func verifySnapshotUpstream(t *testing.T, received <-chan string, markers ...string) {
	t.Helper()
	for _, marker := range markers {
		select {
		case input := <-received:
			if input != marker {
				t.Fatal("快照操作改变了可控上游收到的本轮输入")
			}
		case <-time.After(5 * time.Second):
			t.Fatal("本轮输入没有到达可控上游")
		}
	}
}

// assertSnapshotSkipped 在同步提交完成后检查marker没有入库；成功无返回值，发现记录终止测试。
func assertSnapshotSkipped(t *testing.T, h *instance, marker string) {
	t.Helper()
	for _, record := range snapshotRecords(t, h) {
		if record.Text == marker {
			t.Fatal("接收快照预期跳过的输入被保存")
		}
	}
}

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
