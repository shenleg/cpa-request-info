// 本文件经真实宿主管理 API 验证缺口历史、保留、已阅与再次提醒，不读取内部持久结构。
package system

import (
	"bytes"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"
)

type gapHistoryItem struct {
	Reason          string  `json:"reason"`
	FirstObservedAt *string `json:"first_observed_at"`
	LastObservedAt  string  `json:"last_observed_at"`
	AffectedCount   *uint64 `json:"affected_count"`
	CountAccuracy   string  `json:"count_accuracy"`
	CountUnit       string  `json:"count_unit"`
	Active          bool    `json:"active"`
	HistoryRetained bool    `json:"history_retained"`
	Unread          bool    `json:"unread"`
}

type gapAlerts struct {
	Generation           string           `json:"generation"`
	Revision             uint64           `json:"revision"`
	AcknowledgedRevision uint64           `json:"acknowledged_revision"`
	Unread               bool             `json:"unread"`
	Items                []gapHistoryItem `json:"items"`
}

// readGapAlerts 读取管理员可见历史，缺失新接口或解码失败时终止测试；不访问私有存储。
func readGapAlerts(t *testing.T, h *instance) gapAlerts {
	t.Helper()
	code, raw := h.request(t, "GET", api+"/status", adminKey, nil)
	var status struct {
		State struct {
			Alerts *gapAlerts `json:"gap_alerts"`
		} `json:"state"`
	}
	if code != http.StatusOK || json.Unmarshal(raw, &status) != nil || status.State.Alerts == nil {
		t.Fatal("管理状态缺少缺口历史与提醒快照")
	}
	return *status.State.Alerts
}

// gapItem 查找固定原因的公开摘要，不存在时终止测试。
func gapItem(t *testing.T, alerts gapAlerts, reason string) gapHistoryItem {
	t.Helper()
	for _, item := range alerts.Items {
		if item.Reason == reason {
			return item
		}
	}
	t.Fatalf("缺口历史未包含原因 %s", reason)
	return gapHistoryItem{}
}

// acknowledgeGaps 提交页面已看到的快照；返回HTTP状态，编码或网络失败由测试辅助边界终止测试。
func acknowledgeGaps(t *testing.T, h *instance, alerts gapAlerts, key string) int {
	t.Helper()
	body, err := json.Marshal(map[string]any{"generation": alerts.Generation, "revision": alerts.Revision})
	if err != nil {
		t.Fatal(err)
	}
	code, _ := h.request(t, "POST", api+"/gaps/acknowledge", key, body)
	return code
}

// submitGap 发送带未知块的真实提交，以安全文本落盘作为本轮异步处理屏障。
func submitGap(t *testing.T, h *instance, marker string) {
	t.Helper()
	body, err := json.Marshal(map[string]any{"model": "missing", "messages": []any{map[string]any{"role": "user", "content": []any{map[string]string{"type": "text", "text": marker}, map[string]string{"type": "future_block"}}}}})
	if err != nil {
		t.Fatal(err)
	}
	h.request(t, "POST", "/v1/chat/completions", keyA, body)
	waitSnapshotRecord(t, h, marker)
}

// TestGapHistoryAcknowledgement 验证确认受保护、不删除历史、新缺口重提醒且旧快照不能确认新缺口。
func TestGapHistoryAcknowledgement(t *testing.T) {
	h := startHost(t, 1<<30, true, "")
	h.monitorAll(t)
	marker := "gap-ack-" + time.Now().UTC().Format("150405.000000000")
	submitGap(t, h, marker)
	before := readGapAlerts(t, h)
	for _, key := range []string{"", keyA} {
		if code := acknowledgeGaps(t, h, before, key); code != http.StatusUnauthorized {
			t.Fatalf("非管理认证能确认缺口: %d", code)
		}
	}
	if code := acknowledgeGaps(t, h, before, adminKey); code != http.StatusOK {
		t.Fatalf("管理员不能确认已展示缺口: %d", code)
	}
	if after := readGapAlerts(t, h); after.Unread || gapItem(t, after, "unsupported_content").Unread || len(after.Items) == 0 {
		t.Fatal("确认未减少提醒或删除了历史")
	}
	submitGap(t, h, marker+"-new")
	if code := acknowledgeGaps(t, h, before, adminKey); code != http.StatusConflict {
		t.Fatalf("旧快照误确认了新缺口: %d", code)
	}
	after := readGapAlerts(t, h)
	if !after.Unread || after.Revision <= before.Revision {
		t.Fatal("新缺口没有重新提醒")
	}
	if code := acknowledgeGaps(t, h, after, adminKey); code != http.StatusOK {
		t.Fatal("最新缺口不能确认")
	}
	h.stop()
	restarted := startHost(t, 1<<30, true, "", h.stateDir)
	if saved := readGapAlerts(t, restarted); saved.Unread || gapItem(t, saved, "unsupported_content").AffectedCount == nil || *gapItem(t, saved, "unsupported_content").AffectedCount != 2 {
		t.Fatal("正常重启丢失历史次数或已阅状态")
	}
	if code := acknowledgeGaps(t, restarted, after, adminKey); code != http.StatusConflict {
		t.Fatal("重启前页面能确认新会话")
	}
	waitSnapshotRecord(t, restarted, marker)
}

// TestGapHistoryRetention 验证同原因聚合跨到期时丢弃旧精确次数，调长不延长旧事件。
func TestGapHistoryRetention(t *testing.T) {
	now := time.Date(2026, 10, 15, 8, 0, 0, 0, time.UTC)
	clock := newRetentionClock(t, now)
	h := startRetentionHost(t, 1<<30, "", clock)
	h.monitorAll(t)
	submitGap(t, h, "gap-retention-first")
	retentionClock(t, clock, now.Add(6*24*time.Hour), 0)
	submitGap(t, h, "gap-retention-second")
	if item := gapItem(t, readGapAlerts(t, h), "unsupported_content"); item.AffectedCount == nil || *item.AffectedCount != 2 {
		t.Fatal("保留期内的可确定次数不准确")
	}
	retentionClock(t, clock, now.Add(7*24*time.Hour), 0)
	item := gapItem(t, readGapAlerts(t, h), "unsupported_content")
	if item.FirstObservedAt != nil || item.AffectedCount != nil || item.CountAccuracy != "unknown" || !item.HistoryRetained {
		t.Fatal("聚合包含已过期事件后仍显示虚假的精确次数或起点")
	}
	snapshotMutation(t, h, "/settings", map[string]any{"retention_days": 30})
	retentionClock(t, clock, now.Add(13*24*time.Hour), 0)
	if alerts := readGapAlerts(t, h); len(alerts.Items) != 0 || alerts.Unread {
		t.Fatal("调长延长了旧缺口或到期历史仍留下空提醒")
	}
}

// TestGapHistoryCrashRemindsAgain 验证正常重启保留已阅，不同异常退出各自重新提醒，损失未知不填零。
func TestGapHistoryCrashRemindsAgain(t *testing.T) {
	h := startHost(t, 1<<30, true, "")
	h.monitorAll(t)
	if err := h.cmd.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	h.stop()
	restarted := startHost(t, 1<<30, true, "", h.stateDir)
	alerts := readGapAlerts(t, restarted)
	item := gapItem(t, alerts, "unclean_shutdown")
	if item.AffectedCount != nil || item.CountAccuracy != "unknown" || !alerts.Unread {
		t.Fatal("异常重启把未知损失报告成了确定数量或未提醒")
	}
	if acknowledgeGaps(t, restarted, alerts, adminKey) != http.StatusOK {
		t.Fatal("无法确认异常重启")
	}
	if err := restarted.cmd.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	restarted.stop()
	again := startHost(t, 1<<30, true, "", h.stateDir)
	if after := readGapAlerts(t, again); !after.Unread || after.Revision <= alerts.Revision {
		t.Fatal("再次异常退出没有重新提醒")
	}
}

// TestGapHistoryActiveFault 验证无提交的真实存储故障不虚报丢失次数，已阅和到期均不隐藏活动故障。
func TestGapHistoryActiveFault(t *testing.T) {
	now := time.Date(2026, 10, 15, 8, 0, 0, 0, time.UTC)
	clock := newRetentionClock(t, now)
	h := startRetentionHost(t, 1<<30, "", clock)
	h.monitorAll(t)
	h.stop()
	fault := filepath.Join(h.stateDir, "gap-startup-fault")
	if err := os.WriteFile(fault, []byte("synthetic fault"), 0400); err != nil {
		t.Fatal(err)
	}
	broken := startRetentionHost(t, 1<<30, "", clock, h.stateDir)
	before := readGapAlerts(t, broken)
	item := gapItem(t, before, "storage_error")
	if !item.Active || item.AffectedCount != nil {
		t.Fatal("启动故障未持续显示或虚构了丢失次数")
	}
	// 初始化失败可能未能打开诊断文件；确认可以明确报告未持久化，但活动故障必须保留。
	code := acknowledgeGaps(t, broken, before, adminKey)
	if code != http.StatusOK && code != http.StatusServiceUnavailable {
		t.Fatalf("确认故障返回错误状态 %d", code)
	}
	retentionClock(t, clock, now.Add(8*24*time.Hour), 0)
	item = gapItem(t, readGapAlerts(t, broken), "storage_error")
	if !item.Active || item.HistoryRetained {
		t.Fatal("历史到期隐藏活动故障或延长过期证据")
	}
	broken.stop()
	if err := os.Chmod(fault, 0600); err != nil {
		t.Fatal(err)
	}
	repaired := startRetentionHost(t, 1<<30, "", clock, h.stateDir)
	for _, item := range readGapAlerts(t, repaired).Items {
		if item.Reason == "storage_error" && item.Active {
			t.Fatal("修复后仍将旧故障报告为活动故障")
		}
	}
}

// TestGapHistoryCompatibility 验证无提交的不兼容也有历史，恢复和已阅互不改变健康，复发重新提醒。
func TestGapHistoryCompatibility(t *testing.T) {
	h, control := startCompatibilityHost(t, "extension-missing", "")
	before := readGapAlerts(t, h)
	if !gapItem(t, before, "observer_extension_missing").Active {
		t.Fatal("初始化接口缺失未显示活动故障")
	}
	if acknowledgeGaps(t, h, before, adminKey) != http.StatusOK {
		t.Fatal("无法确认活动不兼容")
	}
	if after := readGapAlerts(t, h); after.Unread || !gapItem(t, after, "observer_extension_missing").Active {
		t.Fatal("已阅隐藏活动故障或未减少提醒")
	}
	compatibilityControl(t, control, "")
	after := readGapAlerts(t, h)
	if gapItem(t, after, "observer_extension_missing").Active || after.Unread {
		t.Fatal("恢复丢失了已阅或仍显示活动不兼容")
	}
	compatibilityControl(t, control, "extension-missing")
	if again := readGapAlerts(t, h); !again.Unread || !gapItem(t, again, "observer_extension_missing").Active {
		t.Fatal("故障复发没有重新提醒")
	}
}

// TestGapHistoryLegacyMigration 验证v2最后时刻能迁移但不能编造次数与首时刻，诊断不复制正文或Key。
func TestGapHistoryLegacyMigration(t *testing.T) {
	h := startHost(t, 1<<30, true, "")
	h.monitorAll(t)
	h.stop()
	at := time.Now().UTC().Format(time.RFC3339Nano)
	fixture := map[string]any{"version": 2, "capacity_gap": false, "last_at": "", "session_dirty": false, "gap_history": map[string]string{"invalid_json": at}, "recovery": map[string]any{"uncertain": false, "detected_at": "", "reason": "", "lost_count": nil, "lost_from": nil, "lost_until": nil}}
	raw, err := json.Marshal(fixture)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(h.stateDir, "capacity.json"), append(raw, bytes.Repeat([]byte{' '}, 4096-len(raw))...), 0600); err != nil {
		t.Fatal(err)
	}
	restarted := startHost(t, 1<<30, true, "", h.stateDir)
	item := gapItem(t, readGapAlerts(t, restarted), "invalid_json")
	if item.AffectedCount != nil || item.FirstObservedAt != nil || item.LastObservedAt != at || !item.Unread {
		t.Fatal("旧诊断迁移伪造了缺失的精确信息或删除了历史")
	}
	marker := "FORBIDDEN_GAP_DIAGNOSTIC_BODY"
	submitGap(t, restarted, marker)
	waitBudgetState(t, restarted, func(state budgetState) bool { return state.DiagnosticsPersisted })
	_, status := restarted.request(t, "GET", api+"/status", adminKey, nil)
	diagnostic, err := os.ReadFile(filepath.Join(h.stateDir, "capacity.json"))
	if err != nil {
		t.Fatal(err)
	}
	for _, output := range [][]byte{status, diagnostic} {
		for _, forbidden := range []string{marker, keyA, keyB, adminKey} {
			if bytes.Contains(output, []byte(forbidden)) {
				t.Fatal("缺口输出复制了正文或认证Key")
			}
		}
	}
}

// TestGapHistoryPendingFaultRecovery 验证目录不能打开时的已阅仅在内存，可写后同进程移交历史且不补报完整。
func TestGapHistoryPendingFaultRecovery(t *testing.T) {
	h := startHost(t, 1<<30, true, "")
	h.monitorAll(t)
	h.stop()
	lock := filepath.Join(h.stateDir, ".audit.lock")
	if err := os.Chmod(lock, 0400); err != nil {
		t.Fatal(err)
	}
	broken := startHost(t, 1<<30, true, "", h.stateDir)
	alerts := readGapAlerts(t, broken)
	if len(alerts.Items) == 0 || !alerts.Items[0].Active {
		t.Fatal("无法打开目录时未显示活动运行故障")
	}
	if acknowledgeGaps(t, broken, alerts, adminKey) != http.StatusOK {
		t.Fatal("运行期故障不能确认已阅")
	}
	broken.request(t, "POST", "/v1/chat/completions", keyA, snapshotInput(t, "gap-pending-failure"))
	fresh := readGapAlerts(t, broken)
	if !fresh.Unread || fresh.Revision <= alerts.Revision || acknowledgeGaps(t, broken, alerts, adminKey) != http.StatusConflict {
		t.Fatal("无法打开存储时的新桥接缺口没有重新提醒")
	}
	if item := gapItem(t, fresh, "observer_unavailable"); item.AffectedCount != nil || item.CountAccuracy != "unknown" {
		t.Fatal("无法读取保留策略时伪造精确次数")
	}
	if acknowledgeGaps(t, broken, fresh, adminKey) != http.StatusOK {
		t.Fatal("不能确认运行期新缺口")
	}
	alerts = fresh
	if err := os.Chmod(lock, 0600); err != nil {
		t.Fatal(err)
	}
	after := readGapAlerts(t, broken)
	for _, previous := range alerts.Items {
		item := gapItem(t, after, previous.Reason)
		if item.Active || !item.HistoryRetained || item.AffectedCount != nil || item.Unread {
			t.Fatal("恢复打开目录丢失已阅历史、虚报精确损失或仍显示旧活动故障")
		}
	}
	waitBudgetState(t, broken, func(state budgetState) bool { return state.DiagnosticsPersisted })
	broken.stop()
	again := startHost(t, 1<<30, true, "", h.stateDir)
	for _, previous := range alerts.Items {
		if item := gapItem(t, readGapAlerts(t, again), previous.Reason); item.Unread || !item.HistoryRetained {
			t.Fatal("恢复写入后的正常重启丢失已阅历史")
		}
	}
}

// TestGapHistoryPartialInput 验证未知块有独立可确定次数，安全部分仍保存，正常空输入不制造缺口。
func TestGapHistoryPartialInput(t *testing.T) {
	h := startHost(t, 1<<30, true, "")
	h.monitorAll(t)
	body := []byte(`{"model":"missing","messages":[{"role":"user","content":[{"type":"text","text":"gap-history-partial-input"},{"type":"future_block","value":"FORBIDDEN_GAP_CONTENT"}]}]}`)
	h.request(t, "POST", "/v1/chat/completions", keyA, body)
	record := waitSnapshotRecord(t, h, "gap-history-partial-input")
	alerts := readGapAlerts(t, h)
	item := gapItem(t, alerts, "unsupported_content")
	if item.AffectedCount == nil || *item.AffectedCount != 1 || item.CountAccuracy != "exact" || item.CountUnit != "incomplete_submissions" || !item.HistoryRetained || !item.Unread || !alerts.Unread || item.Active {
		t.Fatal("不完整提取未报告独立次数、历史和新提醒，或被当作活动存储故障")
	}
	if item.FirstObservedAt == nil || item.LastObservedAt == "" || record.Text != "gap-history-partial-input" || !record.ExtractionIncomplete {
		t.Fatal("缺口缺少时间范围，或未知块妨碍了安全部分保存")
	}
	h.request(t, "POST", "/v1/chat/completions", keyA, []byte(`{"model":"missing","messages":[{"role":"user","content":""}]}`))
	if after := readGapAlerts(t, h); after.Revision != alerts.Revision {
		t.Fatal("正常空输入制造了新提醒")
	}
}
