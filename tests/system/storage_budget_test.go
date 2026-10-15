// 本文件通过真实宿主、受保护管理接口及隔离持久输出验证总预算，不调用存储内部方法。
package system

import (
	"bytes"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

// budgetSettings 读取公开预算；返回字节数，接口或字段错误时终止测试。
func budgetSettings(t *testing.T, h *instance) int64 {
	t.Helper()
	code, raw := h.request(t, "GET", api+"/settings", adminKey, nil)
	var settings struct {
		Budget int64 `json:"budget_bytes"`
	}
	if code != http.StatusOK || json.Unmarshal(raw, &settings) != nil || settings.Budget == 0 {
		t.Fatal("设置必须返回当前总预算")
	}
	return settings.Budget
}

type budgetState struct {
	State                string            `json:"state"`
	Saved                int64             `json:"saved"`
	Queued               int64             `json:"queued"`
	Reason               string            `json:"reason"`
	CapturePaused        bool              `json:"capture_paused"`
	UsedBytes            int64             `json:"used_bytes"`
	BudgetBytes          int64             `json:"budget_bytes"`
	ReservedBytes        int64             `json:"reserved_bytes"`
	ResumeRequiredBytes  int64             `json:"resume_required_bytes"`
	OverBudgetBytes      int64             `json:"over_budget_bytes"`
	CapacityGap          bool              `json:"capacity_gap"`
	DiagnosticsPersisted bool              `json:"diagnostics_persisted"`
	Gaps                 map[string]uint64 `json:"gaps"`
}

// readBudgetState 读取受保护采集状态；返回独立快照，接口或解码失败终止测试。
func readBudgetState(t *testing.T, h *instance) budgetState {
	t.Helper()
	code, raw := h.request(t, "GET", api+"/status", adminKey, nil)
	var status struct {
		State budgetState `json:"state"`
	}
	if code != http.StatusOK || json.Unmarshal(raw, &status) != nil {
		t.Fatal("无法读取预算状态")
	}
	return status.State
}

// waitBudgetState 等待本轮状态满足条件；返回匹配快照，超时终止测试，不打印合成正文。
func waitBudgetState(t *testing.T, h *instance, matches func(budgetState) bool) budgetState {
	t.Helper()
	deadline := time.Now().Add(8 * time.Second)
	for {
		state := readBudgetState(t, h)
		if matches(state) {
			return state
		}
		if time.Now().After(deadline) {
			t.Fatalf("预算状态未满足条件：state=%s reason=%s used=%d budget=%d gaps=%v", state.State, state.Reason, state.UsedBytes, state.BudgetBytes, state.Gaps)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// budgetOutputUsage 独立统计持久输出的保守占用；返回总字节数，遍历失败终止测试，含目录和实际分配块。
func budgetOutputUsage(t *testing.T, root string) int64 {
	t.Helper()
	var total int64
	if err := filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		blocks := ((info.Size() + 4095) / 4096) * 4096
		if blocks == 0 {
			blocks = 4096
		}
		if stat, ok := info.Sys().(*syscall.Stat_t); ok && stat.Blocks*512 > blocks {
			blocks = stat.Blocks * 512
		}
		total += blocks
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	return total
}

// TestBudgetSettingsPersist 验证预算受严格校验，保存后优先于启动默认值，联合设置不部分生效。
func TestBudgetSettingsPersist(t *testing.T) {
	h := startHost(t, 1<<30, true, "")
	if got := budgetSettings(t, h); got != 1<<30 {
		t.Fatalf("默认预算应为1GiB，实际%d", got)
	}
	for _, input := range []string{
		`{"budget_bytes":0}`, `{"budget_bytes":4194303}`, `{"budget_bytes":1073741825}`,
		`{"budget_bytes":null}`, `{"budget_bytes":4.5}`, `{"budget_bytes":"4194304"}`,
		`{"Budget_Bytes":4194304}`, `{"budget_bytes":4194304,"budget_bytes":8388608}`,
		`{"budget_bytes":4194304,"max_text_bytes":0}`,
	} {
		code, _ := h.request(t, "POST", api+"/settings", adminKey, []byte(input))
		if code != http.StatusBadRequest || budgetSettings(t, h) != 1<<30 {
			t.Fatalf("非法预算应原子拒绝，案例=%q HTTP=%d", input, code)
		}
	}
	snapshotMutation(t, h, "/settings", map[string]int{"budget_bytes": 4 << 20, "max_text_bytes": 17})
	if budgetSettings(t, h) != 4<<20 || settingsLimit(t, h) != 17 {
		t.Fatal("联合设置没有完整生效")
	}
	stateDir := h.stateDir
	h.stop()
	h = startHost(t, 8<<20, true, "", stateDir)
	if budgetSettings(t, h) != 4<<20 || settingsLimit(t, h) != 17 {
		t.Fatal("重启默认值覆盖了已保存预算或正文上限")
	}
}

// TestBudgetExpiryResumesWithGap 验证容量拒收不误报桥接故障，清理后续采且跨重启保留基础容量告警。
func TestBudgetExpiryResumesWithGap(t *testing.T) {
	now := time.Date(2031, 1, 1, 0, 0, 0, 0, time.UTC)
	clock := newRetentionClock(t, now)
	h := startRetentionHost(t, 4<<20, "", clock)
	h.monitorAll(t)
	body, _ := json.Marshal(map[string]any{"model": "missing", "messages": []map[string]string{{"role": "user", "content": strings.Repeat("b", 65536)}}})
	for n := 0; n < 100; n++ {
		code, _ := h.request(t, "POST", "/v1/chat/completions", keyA, body)
		if code != http.StatusBadRequest {
			t.Fatal("预算不应改变模型路由错误")
		}
		if used := budgetOutputUsage(t, h.stateDir); used > 4<<20 {
			t.Fatalf("连续提交使正文或辅助持久化超出预算：%d", used)
		}
		if readBudgetState(t, h).Reason == "budget_exhausted" {
			break
		}
	}
	paused := waitBudgetState(t, h, func(s budgetState) bool { return s.Reason == "budget_exhausted" })
	before := searchRecords(t, h, nil).Total
	alerts := readGapAlerts(t, h)
	if !gapItem(t, alerts, "budget_exhausted").Active || acknowledgeGaps(t, h, alerts, adminKey) != http.StatusOK {
		t.Fatal("容量暂停未显示可确认的活动故障")
	}
	if after := readGapAlerts(t, h); after.Unread || !gapItem(t, after, "budget_exhausted").Active {
		t.Fatal("已阅未减少提醒或隐藏了活动容量故障")
	}
	h.stop()
	h = startRetentionHost(t, 4<<20, "", clock, h.stateDir)
	if after := readGapAlerts(t, h); after.Unread || !gapItem(t, after, "budget_exhausted").Active {
		t.Fatal("正常重启将同一持续容量故障误报成新提醒")
	}
	paused = readBudgetState(t, h)
	// 缺口晚于历史输入一天，清理原输入时缺口仍在自己的保留期内。
	retentionClock(t, clock, now.Add(24*time.Hour), 0)
	for n := 0; n < 3; n++ {
		h.request(t, "POST", "/v1/chat/completions", keyA, body)
	}
	blocked := readBudgetState(t, h)
	if !readGapAlerts(t, h).Unread {
		t.Fatal("容量拒收的新缺口没有重新提醒")
	}
	if blocked.Gaps["observer_unavailable"] != paused.Gaps["observer_unavailable"] || blocked.Gaps["budget_exhausted"] < paused.Gaps["budget_exhausted"]+3 {
		t.Fatal("容量拒收必须归入容量缺口，不能变成桥接故障")
	}
	if searchRecords(t, h, nil).Total != before {
		t.Fatal("满额挤掉未到期历史")
	}
	retentionClock(t, clock, now.Add(7*24*time.Hour), 1)
	resumed := waitBudgetState(t, h, func(s budgetState) bool {
		return s.State == "active" && s.UsedBytes < paused.UsedBytes && s.DiagnosticsPersisted
	})
	if !resumed.CapacityGap || !resumed.DiagnosticsPersisted || resumed.Gaps["budget_exhausted"] < blocked.Gaps["budget_exhausted"] || resumed.ReservedBytes == 0 || resumed.ResumeRequiredBytes <= resumed.ReservedBytes {
		t.Fatal("恢复应保留容量告警和计数，并说明预留/恢复空间")
	}
	if item := gapItem(t, readGapAlerts(t, h), "budget_exhausted"); item.Active || !item.HistoryRetained {
		t.Fatal("容量恢复删除了未到期历史或仍报告活动容量故障")
	}
	searchSubmit(t, h, keyA, "missing", "BUDGET-RESUMED")
	stateDir := h.stateDir
	h.stop()
	h = startRetentionHost(t, 4<<20, "", clock, stateDir)
	if state := readBudgetState(t, h); state.State != "active" || !state.CapacityGap || !state.DiagnosticsPersisted {
		t.Fatal("正常重启丢失此前容量告警或未恢复可写状态")
	}
}

// TestBudgetLoweredMaintenanceContinues 验证超预算不提前淘汰，混合段无空间时仍回收后续全到期段并保留主动暂停。
func TestBudgetLoweredMaintenanceContinues(t *testing.T) {
	now := time.Date(2031, 2, 1, 0, 0, 0, 0, time.UTC)
	clock := newRetentionClock(t, now)
	h := startRetentionHost(t, 16<<20, "", clock)
	h.monitorAll(t)
	searchSubmit(t, h, keyA, "missing", "BUDGET-MIXED-OLD")
	retentionClock(t, clock, now.Add(2*24*time.Hour), 0)
	live := searchSubmit(t, h, keyA, "missing", "BUDGET-MIXED-LIVE")
	retentionClock(t, clock, now, 0)
	body, _ := json.Marshal(map[string]any{"model": "missing", "messages": []map[string]string{{"role": "user", "content": strings.Repeat("c", 65536)}}})
	for n := 0; n < 85; n++ {
		h.request(t, "POST", "/v1/chat/completions", keyA, body)
	}
	waitBudgetState(t, h, func(s budgetState) bool { return s.Saved == 87 && s.Queued == 0 })
	before := retentionOutput(t, h.stateDir)
	snapshotMutation(t, h, "/pause", map[string]bool{"paused": true})
	snapshotMutation(t, h, "/settings", map[string]int{"budget_bytes": 4 << 20})
	state := readBudgetState(t, h)
	if state.Reason != "budget_exhausted" || state.OverBudgetBytes == 0 || !state.CapturePaused || !bytes.Equal(before, retentionOutput(t, h.stateDir)) {
		t.Fatal("降额必须明确超预算，保留主动暂停和全部未到期历史")
	}
	code, _ := h.request(t, "POST", api+"/settings", adminKey, []byte(`{"max_text_bytes":33}`))
	if code != http.StatusBadRequest || settingsLimit(t, h) != 65536 {
		t.Fatal("超额期间不应借临时配置继续扩大占用")
	}
	if used := budgetOutputUsage(t, h.stateDir); used > state.UsedBytes {
		t.Fatal("超额期间配置失败仍增加了持久化占用")
	}
	// 第一个分段含未到期对照；不能因其改写无空间就放弃之后无增长的整段回收。
	retentionClock(t, clock, now.Add(7*24*time.Hour), 1)
	waitBudgetState(t, h, func(s budgetState) bool { return s.UsedBytes < 4<<20 && s.Reason != "storage_error" })
	retentionClock(t, clock, now.Add(7*24*time.Hour), 2)
	waitRetentionOutput(t, h, func(raw []byte) bool { return !bytes.Contains(raw, []byte("BUDGET-MIXED-OLD")) })
	state = waitBudgetState(t, h, func(s budgetState) bool { return s.State == "active" })
	if !state.CapturePaused || searchRecords(t, h, nil).Total != 1 {
		t.Fatal("容量恢复解除主动暂停或损坏幸存历史")
	}
	// 同一持续容量故障的重复维护不是新缺口；恢复时原始证据恰满七天，不能靠维护刷新它的期限。
	if state.CapacityGap {
		t.Fatal("重复维护延长了已到期容量历史")
	}
	code, raw := h.request(t, "GET", api+"/detail?id="+live.ID, adminKey, nil)
	var detail savedRecord
	if code != http.StatusOK || json.Unmarshal(raw, &detail) != nil || detail.Text != "BUDGET-MIXED-LIVE" {
		t.Fatal("回收修改了未到期输入")
	}
	if got := budgetOutputUsage(t, h.stateDir); got > 4<<20 {
		t.Fatalf("维护后持久输出仍超预算：%d", got)
	}
}

// TestBudgetAuxiliaryGrowthHysteresis 验证辅助分配计入预算，恢复需要额外余量，正常满额的主动暂停能持久且不被空间恢复解除。
func TestBudgetAuxiliaryGrowthHysteresis(t *testing.T) {
	now := time.Date(2031, 3, 1, 0, 0, 0, 0, time.UTC)
	clock := newRetentionClock(t, now)
	text := "BUDGET-AUXILIARY-UPSTREAM-UNCHANGED"
	upstream, received := controlledDiskUpstream(t, text)
	defer upstream.Close()
	h := startRetentionHost(t, 4<<20, upstream.URL+"/v1", clock)
	h.monitorAll(t)
	body, _ := json.Marshal(map[string]any{"model": "audit-test-model", "messages": []map[string]string{{"role": "user", "content": text}}})
	sendDiskSubmission(t, h, body, received)
	h.waitRecords(t, 1)
	directory := filepath.Join(h.stateDir, "synthetic-database-auxiliary")
	if err := os.Mkdir(directory, 0700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(directory, "auxiliary.data")
	if err := os.WriteFile(path, bytes.Repeat([]byte{0x5a}, 2<<20), 0600); err != nil {
		t.Fatal(err)
	}
	retentionClock(t, clock, now, 1)
	state := waitBudgetState(t, h, func(s budgetState) bool { return s.Reason == "budget_exhausted" && s.DiagnosticsPersisted })
	if expected := budgetOutputUsage(t, h.stateDir); state.UsedBytes != expected || state.UsedBytes > 4<<20 {
		t.Fatal("辅助文件、目录或实际分配块未计入公开占用")
	}
	sendDiskSubmission(t, h, body, received)
	if readBudgetState(t, h).Saved != 1 {
		t.Fatal("辅助增长满额后仍保存正文")
	}
	snapshotMutation(t, h, "/pause", map[string]bool{"paused": true})
	stateDir := h.stateDir
	h.stop()
	h = startRetentionHost(t, 4<<20, upstream.URL+"/v1", clock, stateDir)
	if s := readBudgetState(t, h); !s.CapturePaused || !s.CapacityGap || s.Reason != "budget_exhausted" {
		t.Fatal("重启未保留主动暂停、容量暂停和历史告警")
	}
	// 释放至正文准入预留以上、恢复门槛以下，不能立即反复切换状态。
	if err := os.Truncate(path, 3<<19); err != nil {
		t.Fatal(err)
	}
	retentionClock(t, clock, now, 2)
	state = waitBudgetState(t, h, func(s budgetState) bool { return s.UsedBytes < 2<<20 })
	free := state.BudgetBytes - state.UsedBytes
	if state.State != "paused" || free <= state.ReservedBytes || free >= state.ResumeRequiredBytes {
		t.Fatal("容量恢复没有使用独立滞回余量")
	}
	if err := os.Truncate(path, 1<<20); err != nil {
		t.Fatal(err)
	}
	retentionClock(t, clock, now, 3)
	state = waitBudgetState(t, h, func(s budgetState) bool { return s.State == "active" })
	if !state.CapturePaused || !state.CapacityGap || !state.DiagnosticsPersisted {
		t.Fatal("空间恢复错误解除主动暂停或擦除了告警")
	}
	sendDiskSubmission(t, h, body, received)
	if readBudgetState(t, h).Saved != 1 {
		t.Fatal("主动暂停被空间恢复解除")
	}
	snapshotMutation(t, h, "/pause", map[string]bool{"paused": false})
	sendDiskSubmission(t, h, body, received)
	h.waitRecords(t, 2)
	if got := budgetOutputUsage(t, h.stateDir); got > 4<<20 {
		t.Fatal("恢复后的持久化超过总预算")
	}
}

// TestBudgetNeverClearsStorageFault 验证预算配置失败和提高预算不能把先前真实存储故障转成可自动解除的容量暂停。
func TestBudgetNeverClearsStorageFault(t *testing.T) {
	h := startHost(t, 16<<20, true, "")
	h.monitorAll(t)
	fault := filepath.Join(h.stateDir, "synthetic-storage-fault")
	if err := os.WriteFile(fault, []byte("synthetic"), 0400); err != nil {
		t.Fatal(err)
	}
	h.request(t, "POST", "/v1/chat/completions", keyA, snapshotInput(t, "BUDGET-STORAGE-FAULT"))
	waitBudgetState(t, h, func(s budgetState) bool { return s.Reason == "storage_error" })
	// 外部修复只让管理配置可以写入，不授权把未核实的数据完整性故障自动解除。
	if err := os.Chmod(fault, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(h.stateDir, "synthetic-auxiliary"), bytes.Repeat([]byte{0x5a}, 5<<20), 0600); err != nil {
		t.Fatal(err)
	}
	snapshotMutation(t, h, "/settings", map[string]int{"budget_bytes": 4 << 20})
	code, _ := h.request(t, "POST", api+"/settings", adminKey, []byte(`{"max_text_bytes":19}`))
	if code != http.StatusBadRequest || readBudgetState(t, h).Reason != "storage_error" {
		t.Fatal("预算不足覆盖了未恢复的真实存储故障")
	}
	snapshotMutation(t, h, "/settings", map[string]int{"budget_bytes": 16 << 20})
	if readBudgetState(t, h).Reason != "storage_error" {
		t.Fatal("提高预算自动解除了真实存储故障")
	}
}

// TestBudgetAlertRetention 验证已恢复容量告警遵守接收时保留规则，调长不能延长旧告警，活动容量故障不因到期隐藏。
func TestBudgetAlertRetention(t *testing.T) {
	now := time.Date(2031, 4, 1, 0, 0, 0, 0, time.UTC)
	clock := newRetentionClock(t, now)
	h := startRetentionHost(t, 4<<20, "", clock)
	h.monitorAll(t)
	path := filepath.Join(h.stateDir, "synthetic-auxiliary")
	if err := os.WriteFile(path, bytes.Repeat([]byte{0x5a}, 2<<20), 0600); err != nil {
		t.Fatal(err)
	}
	retentionClock(t, clock, now, 1)
	waitBudgetState(t, h, func(s budgetState) bool { return s.Reason == "budget_exhausted" && s.DiagnosticsPersisted })
	if err := os.Truncate(path, 1<<20); err != nil {
		t.Fatal(err)
	}
	retentionClock(t, clock, now.Add(24*time.Hour), 2)
	waitBudgetState(t, h, func(s budgetState) bool { return s.State == "active" && s.CapacityGap })
	snapshotMutation(t, h, "/settings", map[string]int{"retention_days": 30})
	retentionClock(t, clock, now.Add(7*24*time.Hour), 3)
	waitBudgetState(t, h, func(s budgetState) bool { return !s.CapacityGap && s.DiagnosticsPersisted })
	if err := os.WriteFile(path, bytes.Repeat([]byte{0x5a}, 2<<20), 0600); err != nil {
		t.Fatal(err)
	}
	retentionClock(t, clock, now.Add(8*24*time.Hour), 4)
	waitBudgetState(t, h, func(s budgetState) bool {
		return s.Reason == "budget_exhausted" && s.CapacityGap && s.DiagnosticsPersisted
	})
	if acknowledgeGaps(t, h, readGapAlerts(t, h), adminKey) != http.StatusOK {
		t.Fatal("不能确认持续容量故障")
	}
	retentionClock(t, clock, now.Add(40*24*time.Hour), 5)
	waitBudgetState(t, h, func(s budgetState) bool {
		return s.Reason == "budget_exhausted" && s.CapacityGap && s.DiagnosticsPersisted
	})
	h.stop()
	h = startRetentionHost(t, 4<<20, "", clock, h.stateDir)
	alerts := readGapAlerts(t, h)
	item := gapItem(t, alerts, "budget_exhausted")
	if !item.Active || item.HistoryRetained || item.AffectedCount != nil || item.CountAccuracy != "unknown" || alerts.Unread {
		t.Fatal("持续容量故障到期重启后丢失未知语义或重新提醒")
	}
}

// TestBudgetExpectedSkipsStaySkips 验证容量暂停不把工具续跑、非用户尾项和空输入误报为丢失的新用户输入。
func TestBudgetExpectedSkipsStaySkips(t *testing.T) {
	now := time.Date(2031, 5, 1, 0, 0, 0, 0, time.UTC)
	clock := newRetentionClock(t, now)
	h := startRetentionHost(t, 4<<20, "", clock)
	h.monitorAll(t)
	if err := os.WriteFile(filepath.Join(h.stateDir, "synthetic-auxiliary"), bytes.Repeat([]byte{0x5a}, 2<<20), 0600); err != nil {
		t.Fatal(err)
	}
	retentionClock(t, clock, now, 1)
	before := waitBudgetState(t, h, func(s budgetState) bool { return s.Reason == "budget_exhausted" && s.DiagnosticsPersisted })
	for _, entry := range []struct{ path, body string }{
		{"/v1/chat/completions", `{"model":"missing","messages":[{"role":"assistant","content":"synthetic continuation"}]}`},
		{"/v1/chat/completions", `{"model":"missing","messages":[{"role":"user","content":""}]}`},
		{"/v1/messages", `{"model":"missing","messages":[{"role":"user","content":[{"type":"tool_result","tool_use_id":"synthetic","content":"synthetic output"}]}]}`},
		{"/v1/responses", `{"model":"missing","input":[{"type":"function_call_output","call_id":"synthetic","output":"synthetic output"}]}`},
	} {
		h.request(t, "POST", entry.path, keyA, []byte(entry.body))
	}
	after := readBudgetState(t, h)
	if after.Gaps["budget_exhausted"] != before.Gaps["budget_exhausted"] || after.Gaps["observer_unavailable"] != before.Gaps["observer_unavailable"] {
		t.Fatal("容量暂停把正常不采集变成缺口")
	}
}

// TestBudgetRestartKeepsRecoveryThreshold 验证编码膨胀导致的容量暂停在重启后仍须满足恢复余量，不能通过默认active绕过滞回。
func TestBudgetRestartKeepsRecoveryThreshold(t *testing.T) {
	now := time.Date(2031, 6, 1, 0, 0, 0, 0, time.UTC)
	clock := newRetentionClock(t, now)
	h := startRetentionHost(t, 4<<20, "", clock)
	h.monitorAll(t)
	if err := os.WriteFile(filepath.Join(h.stateDir, "synthetic-auxiliary"), bytes.Repeat([]byte{0x5a}, 15<<17), 0600); err != nil {
		t.Fatal(err)
	}
	// 64KiB NUL 会按JSON编码为约384KiB，不能只按原文字节判断下一条写入空间。
	body, _ := json.Marshal(map[string]any{"model": "missing", "messages": []map[string]string{{"role": "user", "content": strings.Repeat("\x00", 65536)}}})
	h.request(t, "POST", "/v1/chat/completions", keyA, body)
	paused := waitBudgetState(t, h, func(s budgetState) bool { return s.Reason == "budget_exhausted" && s.DiagnosticsPersisted })
	free := paused.BudgetBytes - paused.UsedBytes
	if free <= paused.ReservedBytes || free >= paused.ResumeRequiredBytes {
		t.Fatal("夹具没有形成正文门槛与恢复门槛之间的空间")
	}
	stateDir := h.stateDir
	h.stop()
	h = startRetentionHost(t, 4<<20, "", clock, stateDir)
	restarted := readBudgetState(t, h)
	if restarted.State != "paused" || restarted.Reason != "budget_exhausted" || !restarted.CapacityGap {
		t.Fatal("重启在没有释放空间时绕过了容量恢复门槛")
	}
}
