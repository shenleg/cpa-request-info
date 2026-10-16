// 本文件从真实模型入口与受保护管理状态验收异常退出、不确定损失及正常重启的区别。
package system

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

type recoverySnapshot struct {
	Uncertain  bool    `json:"uncertain"`
	DetectedAt string  `json:"detected_at"`
	Reason     string  `json:"reason"`
	LostCount  *uint64 `json:"lost_count"`
	LostFrom   *string `json:"lost_from"`
	LostUntil  *string `json:"lost_until"`
}

type restartSnapshot struct {
	State struct {
		State                string            `json:"state"`
		Reason               string            `json:"reason"`
		Saved                int64             `json:"saved"`
		Queued               int64             `json:"queued"`
		Gaps                 map[string]uint64 `json:"gaps"`
		GapHistory           map[string]string `json:"gap_history"`
		Recovery             *recoverySnapshot `json:"recovery"`
		DiagnosticsPersisted bool              `json:"diagnostics_persisted"`
	} `json:"state"`
}

// restartStatus 查询公开基础诊断；返回状态，HTTP、解码或缺少恢复语义时终止测试，不读取私有文件。
func restartStatus(t *testing.T, h *instance) restartSnapshot {
	t.Helper()
	code, raw := h.request(t, "GET", api+"/status", adminKey, nil)
	var status restartSnapshot
	if code != http.StatusOK || json.Unmarshal(raw, &status) != nil || status.State.Recovery == nil {
		t.Fatal("管理状态未提供重启损失是否确定的信息")
	}
	return status
}

// assertUncertainRecovery 验证未知损失没有被本次运行零计数或发现时间冒充；返回发现时刻，语义不符终止测试。
func assertUncertainRecovery(t *testing.T, h *instance, reason string) string {
	t.Helper()
	state := restartStatus(t, h).State
	recovery := state.Recovery
	if !recovery.Uncertain || recovery.Reason != reason || recovery.LostCount != nil || recovery.LostFrom != nil || recovery.LostUntil != nil {
		t.Fatal("不确定损失被报告为确定数量、时段或没有缺口")
	}
	if _, err := time.Parse(time.RFC3339Nano, recovery.DetectedAt); err != nil {
		t.Fatal("未知损失没有独立的有效发现时刻")
	}
	return recovery.DetectedAt
}

// TestRestartCrashUncertainty 验证强杀后即使文件完整也提示未知损失，正常重启不清掉这一信号或配置历史。
func TestRestartCrashUncertainty(t *testing.T) {
	upstream, received := snapshotUpstream()
	defer upstream.Close()
	h := startHost(t, 1<<30, true, upstream.URL+"/v1")
	h.monitorAll(t)
	marker := "restart-crash-" + time.Now().UTC().Format("150405.000000000")
	snapshotMutation(t, h, "/settings", map[string]any{"max_text_bytes": 2048})
	body, _ := json.Marshal(map[string]any{"model": "audit-test-model", "messages": []map[string]string{{"role": "user", "content": marker}}})
	code, _ := h.request(t, "POST", "/v1/chat/completions", keyA, body)
	if code != http.StatusOK {
		t.Fatal("异常退出前模型调用失败")
	}
	verifySnapshotUpstream(t, received, marker)
	before := waitSnapshotRecord(t, h, marker)
	// 已保存文件看起来完整也不能据此证明没有未落盘副本；只强杀本测试宿主，不调用正常关闭协议。
	if err := h.cmd.Process.Kill(); err != nil {
		t.Fatal("无法强杀本轮隔离宿主")
	}
	h.stop()
	restarted := startHost(t, 1<<30, true, upstream.URL+"/v1", h.stateDir)
	detected := assertUncertainRecovery(t, restarted, "unclean_shutdown")
	if after := waitSnapshotRecord(t, restarted, marker); after.ID != before.ID {
		t.Fatal("异常重启改变了已经保存的历史")
	}
	code, raw := restarted.request(t, "GET", api+"/settings", adminKey, nil)
	var settings struct {
		MaxTextBytes int `json:"max_text_bytes"`
	}
	if code != http.StatusOK || json.Unmarshal(raw, &settings) != nil || settings.MaxTextBytes != 2048 {
		t.Fatal("异常重启丢失持久配置")
	}
	restarted.stop()
	again := startHost(t, 1<<30, true, upstream.URL+"/v1", h.stateDir)
	if assertUncertainRecovery(t, again, "unclean_shutdown") != detected {
		t.Fatal("正常重启清除或重置了此前不确定损失")
	}
}

// TestRestartStorageGapSurvivesRepair 验证一次真实未保存提交有确定运行计数，修复和正常重启仍保留基本缺口。
func TestRestartStorageGapSurvivesRepair(t *testing.T) {
	upstream, received := snapshotUpstream()
	defer upstream.Close()
	h := startHost(t, 1<<30, true, upstream.URL+"/v1")
	h.monitorAll(t)
	marker := "restart-storage-" + time.Now().UTC().Format("150405.000000000")
	body, _ := json.Marshal(map[string]any{"model": "audit-test-model", "messages": []map[string]string{{"role": "user", "content": marker}}})
	if code, _ := h.request(t, "POST", "/v1/chat/completions", keyA, body); code != http.StatusOK {
		t.Fatal("故障前模型调用失败")
	}
	waitSnapshotRecord(t, h, marker)
	fault := filepath.Join(h.stateDir, "external-fault")
	if err := os.WriteFile(fault, []byte("synthetic fault"), 0400); err != nil {
		t.Fatal(err)
	}
	request, err := http.NewRequest("POST", h.base+"/v1/chat/completions", strings.NewReader(string(body)))
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Authorization", "Bearer "+keyA)
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("User-Agent", "FORBIDDEN_FAULT_UA")
	request.Header.Set("X-Session", "FORBIDDEN_FAULT_SESSION")
	response, err := h.client.Do(request)
	if err != nil {
		t.Fatal("故障请求未到达宿主")
	}
	response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatal("存储故障改变模型响应")
	}
	verifySnapshotUpstream(t, received, marker, marker)
	waitBudgetState(t, h, func(state budgetState) bool { return state.Reason == "storage_error" && state.Queued == 0 })
	state := restartStatus(t, h).State
	if state.Saved != 1 || state.Gaps["storage_error"] != 1 || state.GapHistory["storage_error"] == "" {
		t.Fatal("实际未保存提交没有独立运行计数及基本历史信号")
	}
	h.stop()
	assertChatPrivacy(t, h, "FORBIDDEN_FAULT_UA", "FORBIDDEN_FAULT_SESSION")
	if err := os.Chmod(fault, 0600); err != nil {
		t.Fatal(err)
	}
	repaired := startHost(t, 1<<30, true, upstream.URL+"/v1", h.stateDir)
	state = restartStatus(t, repaired).State
	if state.State != "active" || state.Gaps["storage_error"] != 0 || state.GapHistory["storage_error"] == "" {
		t.Fatal("修复重启抹掉了此前缺口或把历史数伪造成本次运行计数")
	}
	waitSnapshotRecord(t, repaired, marker)
	repaired.stop()
	again := startHost(t, 1<<30, true, upstream.URL+"/v1", h.stateDir)
	if restartStatus(t, again).State.GapHistory["storage_error"] == "" {
		t.Fatal("正常重启清掉了已恢复缺口")
	}
}

// TestRestartCleanSession 验证新目录和排空后的正常退出不被误报异常，且故障检测不是虚构丢失数量。
func TestRestartCleanSession(t *testing.T) {
	h := startHost(t, 1<<30, true, "")
	h.monitorAll(t)
	if state := restartStatus(t, h).State; state.Recovery.Uncertain || !state.DiagnosticsPersisted {
		t.Fatal("全新目录被报告为异常退出")
	}
	h.stop()
	restarted := startHost(t, 1<<30, true, "", h.stateDir)
	if restartStatus(t, restarted).State.Recovery.Uncertain {
		t.Fatal("正常排空并同步后的退出被报告为异常")
	}
	restarted.stop()
	fault := filepath.Join(h.stateDir, "startup-fault")
	if err := os.WriteFile(fault, []byte("synthetic fault"), 0400); err != nil {
		t.Fatal(err)
	}
	paused := startHost(t, 1<<30, true, "", h.stateDir)
	state := restartStatus(t, paused).State
	if state.State != "paused" || state.Reason != "storage_error" || state.Gaps["storage_error"] != 0 {
		t.Fatal("没有提交的启动故障被计为确定丢失输入")
	}
}

// TestRestartLegacyAndCorruptDiagnostics 验证旧标记明确迁移为未知，损坏诊断不被清零或覆盖。
func TestRestartLegacyAndCorruptDiagnostics(t *testing.T) {
	for _, kind := range []string{"legacy", "corrupt"} {
		t.Run(kind, func(t *testing.T) {
			h := startHost(t, 1<<30, true, "")
			h.monitorAll(t)
			h.stop()
			content := `{"version":1,"capacity_gap":false,"last_at":""}`
			if kind == "corrupt" {
				content = `{"version":2,"session_dirty":`
			}
			// 磁盘文件是系统外部边界；合成旧版/中断写入，不调用内部解码器或新增生产注入接口。
			data := []byte(content + strings.Repeat(" ", 4096-len(content)))
			path := filepath.Join(h.stateDir, "capacity.json")
			if err := os.WriteFile(path, data, 0600); err != nil {
				t.Fatal(err)
			}
			restarted := startHost(t, 1<<30, true, "", h.stateDir)
			reason := "legacy_unknown"
			if kind == "corrupt" {
				reason = "recovery_unavailable"
			}
			assertUncertainRecovery(t, restarted, reason)
			state := restartStatus(t, restarted).State
			if kind == "corrupt" {
				if state.State != "paused" || state.DiagnosticsPersisted {
					t.Fatal("损坏诊断被宣布可采集或已持久化")
				}
				restarted.stop()
				after, err := os.ReadFile(path)
				if err != nil || string(after) != string(data) {
					t.Fatal("损坏诊断被覆盖为另一份无损失数据")
				}
			}
		})
	}
}

// TestRestartDiagnosticExpiry 验证基础缺口及未知损失按发现时刻到期，调长不延长旧信号。
func TestRestartDiagnosticExpiry(t *testing.T) {
	now := time.Date(2026, 10, 15, 8, 0, 0, 0, time.UTC)
	clock := newRetentionClock(t, now)
	h := startRetentionHost(t, 1<<30, "", clock)
	h.monitorAll(t)
	h.request(t, "POST", "/v1/chat/completions", keyA, []byte(`{"model":"audit-test-model","messages":[{"role":"user","content":[{"type":"unknown_block","value":"FORBIDDEN_UNKNOWN_PAYLOAD"}]}]}`))
	if err := h.cmd.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	h.stop()
	restarted := startRetentionHost(t, 1<<30, "", clock, h.stateDir)
	assertUncertainRecovery(t, restarted, "unclean_shutdown")
	// 未知块仍为明确缺口；正文安全提取为空不意味着正常空输入。只等待重启后本轮新增的运行计数。
	restarted.request(t, "POST", "/v1/chat/completions", keyA, []byte(`{"model":"audit-test-model","messages":[{"role":"user","content":[{"type":"unknown_block"}]}]}`))
	waitBudgetState(t, restarted, func(state budgetState) bool {
		return state.Gaps["unsupported_content"] == 1 && state.DiagnosticsPersisted
	})
	retentionClock(t, clock, now.Add(24*time.Hour), 0)
	snapshotMutation(t, restarted, "/settings", map[string]any{"retention_days": 30})
	retentionClock(t, clock, now.Add(7*24*time.Hour), 0)
	state := restartStatus(t, restarted).State
	if state.Recovery.Uncertain || len(state.GapHistory) != 0 {
		t.Fatal("调长保留期延长了先前基础诊断或发现时间被误作新的损失时间")
	}
}

// TestRestartQueuedCrash 验证公开可见待保存副本时强杀会保留未知损失提示，不承诺崩溃计数精确。
func TestRestartQueuedCrash(t *testing.T) {
	h := startHostWithManagementAuth(t, 1<<30, true, "", true)
	h.monitorAll(t)
	// 与既有真实队列满验收采用相同128并发；负载有界，强杀后允许在途请求被进程退出中断。
	const requests = 1024
	body := []byte(fmt.Sprintf(`{"model":"audit-test-model","messages":[{"role":"user","content":%q}]}`, strings.Repeat("queued-crash-", 5400)))
	client := &http.Client{Timeout: 30 * time.Second}
	defer client.CloseIdleConnections()
	var workers sync.WaitGroup
	work := make(chan struct{})
	for range 128 {
		workers.Add(1)
		go func() {
			defer workers.Done()
			for range work {
				request, err := http.NewRequest("POST", h.base+"/v1/chat/completions", strings.NewReader(string(body)))
				if err != nil {
					return
				}
				request.Header.Set("Authorization", "Bearer "+keyA)
				request.Header.Set("Content-Type", "application/json")
				response, err := client.Do(request)
				if err == nil {
					response.Body.Close()
				}
			}
		}()
	}
	go func() {
		for range requests {
			work <- struct{}{}
		}
		close(work)
	}()
	deadline := time.Now().Add(20 * time.Second)
	var queued int64
	for time.Now().Before(deadline) {
		_, queued, _ = snapshotCounters(t, h)
		if queued > 0 {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	if err := h.cmd.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	h.stop()
	workers.Wait()
	if queued == 0 {
		t.Fatal("强杀前没有实际观察到未落盘队列")
	}
	t.Logf("强杀前公开待保存副本：%d（不是精确崩溃损失数）", queued)
	restarted := startHost(t, 1<<30, true, "", h.stateDir)
	assertUncertainRecovery(t, restarted, "unclean_shutdown")
}
