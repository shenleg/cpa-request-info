//go:build legacy_patched

// 历史私有 observer 扩展与 compatibility overlay 契约；当前官方能力见 TestOfficialKnownCallerDirectory，存储故障见 TestHTTPProtocolsStorageFailure。
// 本文件通过替代宿主契约的隔离产物，验收兼容故障、快照保留及恢复；不添加生产故障 API。
package system

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// compatibilityControl 写入宿主 overlay 专用模式；path 位于合成数据目录旁，成功无返回值，I/O 失败终止测试。
func compatibilityControl(t *testing.T, path, mode string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(mode), 0600); err != nil {
		t.Fatal("兼容性夹具控制写入失败")
	}
}

// startCompatibilityHost 启动隔离真实宿主；mode 选择替代契约，upstream 是合成模型地址。
// 返回 HTTP 实例及本轮控制路径；目录、产物或启动失败终止测试，不修改共享构建产物。
func startCompatibilityHost(t *testing.T, mode, upstream string) (*instance, string) {
	t.Helper()
	root, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	parent := filepath.Join(root, ".test-data")
	if err := os.MkdirAll(parent, 0700); err != nil {
		t.Fatal(err)
	}
	dir, err := os.MkdirTemp(parent, "compatibility-")
	if err != nil {
		t.Fatal(err)
	}
	stateDir := filepath.Join(dir, "audit")
	control := stateDir + ".compatibility"
	compatibilityControl(t, control, mode)
	out := filepath.Join(root, ".build/compatibility-test")
	h := startHostWithRuntime(t, 1<<30, mode != "plugin-missing", upstream, false, "", filepath.Join(out, "plugins"), "", filepath.Join(out, "bin/cliproxyapi"), "", stateDir)
	return h, control
}

type compatibilityStatus struct {
	Available bool   `json:"available"`
	Reason    string `json:"reason"`
	State     struct {
		Saved int64             `json:"saved"`
		Gaps  map[string]uint64 `json:"gaps"`
	} `json:"state"`
}

// readCompatibilityStatus 读取受保护公开状态；返回独立解码副本，HTTP 或 JSON 错误终止测试。
func readCompatibilityStatus(t *testing.T, h *instance) compatibilityStatus {
	t.Helper()
	code, raw := h.request(t, "GET", api+"/status", adminKey, nil)
	var status compatibilityStatus
	if code != http.StatusOK || json.Unmarshal(raw, &status) != nil {
		t.Fatalf("兼容性状态不可用: HTTP %d", code)
	}
	return status
}

// TestCompatibilityStartupDiagnosis 验证管理注册或插件也缺失时，启动日志含固定安全原因，真实模型请求仍成功。
func TestCompatibilityStartupDiagnosis(t *testing.T) {
	for _, test := range []struct{ mode, reason string }{
		{"management-missing", "management_unavailable"},
		{"extension-missing-no-management", "observer_extension_missing"},
		{"plugin-missing", "observer_bridge_unavailable"},
	} {
		t.Run(test.mode, func(t *testing.T) {
			upstream, received := snapshotUpstream()
			defer upstream.Close()
			h, _ := startCompatibilityHost(t, test.mode, upstream.URL)
			if code, _ := h.request(t, "GET", api+"/status", adminKey, nil); code != http.StatusNotFound {
				t.Fatalf("夹具应缺少管理入口: %d", code)
			}
			marker := "compatibility-no-management-private-input"
			if code, _ := h.request(t, "POST", "/v1/chat/completions", keyA, snapshotInput(t, marker)); code != http.StatusOK {
				t.Fatalf("缺少管理入口改变模型调用: %d", code)
			}
			verifySnapshotUpstream(t, received, marker)
			raw, err := os.ReadFile(filepath.Join(h.dir, "host.log"))
			if err != nil {
				t.Fatal("启动日志不可读")
			}
			found := false
			for _, line := range strings.Split(string(raw), "\n") {
				if !strings.Contains(line, "input audit") {
					continue
				}
				if strings.Contains(line, test.reason) {
					found = true
				}
				for _, forbidden := range []string{marker, keyA, keyB, adminKey, h.stateDir} {
					if strings.Contains(line, forbidden) {
						t.Fatal("固定审计诊断包含秘密、正文或数据目录")
					}
				}
			}
			if !found {
				t.Fatalf("管理入口缺失时没有固定启动诊断: %s", test.reason)
			}
		})
	}
}

// TestCompatibilityFusedObserver 验证真实宿主熔断阻止新采集、保留缺口，解除后重新核验并恢复。
func TestCompatibilityFusedObserver(t *testing.T) {
	upstream, received := snapshotUpstream()
	defer upstream.Close()
	h, control := startCompatibilityHost(t, "", upstream.URL)
	h.monitorAll(t)
	compatibilityControl(t, control, "fused")
	if code, _ := h.request(t, "GET", api+"/status", adminKey, nil); code != http.StatusNotFound {
		t.Fatalf("整插件熔断不能绕过宿主调用管理回调: %d", code)
	}
	blocked := "compatibility-fused-blocked"
	if code, _ := h.request(t, "POST", "/v1/chat/completions", keyA, snapshotInput(t, blocked)); code != http.StatusOK {
		t.Fatalf("熔断改变模型调用: %d", code)
	}
	raw, err := os.ReadFile(filepath.Join(h.dir, "host.log"))
	if err != nil || !strings.Contains(string(raw), "input audit observer unavailable") || !strings.Contains(string(raw), "observer_bridge_unavailable") {
		t.Fatal("熔断同时失去管理能力时缺少固定宿主诊断")
	}
	compatibilityControl(t, control, "")
	if status := readCompatibilityStatus(t, h); !status.Available || status.State.Gaps["observer_unavailable"] != 1 {
		t.Fatal("熔断恢复清除了桥接缺口或未重新核验")
	}
	recovered := "compatibility-fused-recovered"
	if code, _ := h.request(t, "POST", "/v1/chat/completions", keyA, snapshotInput(t, recovered)); code != http.StatusOK {
		t.Fatalf("解除熔断改变模型调用: %d", code)
	}
	waitSnapshotRecord(t, h, recovered)
	assertSnapshotSkipped(t, h, blocked)
	verifySnapshotUpstream(t, received, blocked, recovered)
}

// TestCompatibilityFinalCallbackUncertainty 验证真正无法读取关闭累计时保留未知，不伪造确定丢失次数。
func TestCompatibilityFinalCallbackUncertainty(t *testing.T) {
	h, control := startCompatibilityHost(t, "", "")
	h.monitorAll(t)
	compatibilityControl(t, control, "final-callback-unavailable")
	h.stop()
	compatibilityControl(t, control, "")
	root, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	out := filepath.Join(root, ".build/compatibility-test")
	restarted := startHostWithRuntime(t, 1<<30, true, "", false, "", filepath.Join(out, "plugins"), "", filepath.Join(out, "bin/cliproxyapi"), "", h.stateDir)
	assertUncertainRecovery(t, restarted, "observer_unavailable")
	if state := restartStatus(t, restarted).State; state.Gaps["observer_unavailable"] != 0 || state.GapHistory["observer_unavailable"] == "" {
		t.Fatal("关闭累计不可读没有保留未知，或被冒充确定丢失次数")
	}
}

// TestCompatibilityLastBridgeFailureSurvivesClose 验证末次桥接失败后不刷新状态直接正常停机，重启仍可见基本历史缺口。
func TestCompatibilityLastBridgeFailureSurvivesClose(t *testing.T) {
	upstream, received := snapshotUpstream()
	defer upstream.Close()
	h, control := startCompatibilityHost(t, "", upstream.URL)
	h.monitorAll(t)
	compatibilityControl(t, control, "fused")
	marker := "compatibility-last-bridge-failure"
	if code, _ := h.request(t, "POST", "/v1/chat/completions", keyA, snapshotInput(t, marker)); code != http.StatusOK {
		t.Fatalf("末次桥接失败改变模型调用: %d", code)
	}
	verifySnapshotUpstream(t, received, marker)
	h.stop()
	compatibilityControl(t, control, "")
	root, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	out := filepath.Join(root, ".build/compatibility-test")
	restarted := startHostWithRuntime(t, 1<<30, true, upstream.URL, false, "", filepath.Join(out, "plugins"), "", filepath.Join(out, "bin/cliproxyapi"), "", h.stateDir)
	code, raw := restarted.request(t, "GET", api+"/status", adminKey, nil)
	var status struct {
		State struct {
			History map[string]string `json:"gap_history"`
		} `json:"state"`
	}
	if code != http.StatusOK || json.Unmarshal(raw, &status) != nil || status.State.History["observer_unavailable"] == "" {
		t.Fatal("未查询末次桥接故障直接停机后，历史缺口被抹去")
	}
}

// TestCompatibilityBlockedStatusDoesNotBlockModel 验证管理能力探测阻塞时，模型准备和观察不等待管理锁或恢复 I/O。
func TestCompatibilityBlockedStatusDoesNotBlockModel(t *testing.T) {
	upstream, received := snapshotUpstream()
	defer upstream.Close()
	h, control := startCompatibilityHost(t, "", upstream.URL)
	h.monitorAll(t)
	compatibilityControl(t, control, "callback-blocked")
	defer compatibilityControl(t, control, "")
	done := make(chan error, 1)
	go func() {
		req, err := http.NewRequest("GET", h.base+api+"/status", nil)
		if err != nil {
			done <- err
			return
		}
		req.Header.Set("Authorization", "Bearer "+adminKey)
		resp, err := h.client.Do(req)
		if err == nil {
			err = resp.Body.Close()
		}
		done <- err
	}()
	// 该文件只同步测试宿主替代契约，不用它断言生产状态；行为仍通过模型和受保护 HTTP 验收。
	deadline := time.Now().Add(5 * time.Second)
	for {
		if _, err := os.Stat(h.stateDir + ".capabilities-entered"); err == nil {
			break
		} else if !os.IsNotExist(err) || time.Now().After(deadline) {
			t.Fatal("能力阻塞夹具没有到达屏障")
		}
		time.Sleep(time.Millisecond)
	}
	marker := "compatibility-blocked-status-model"
	if code, _ := h.request(t, "POST", "/v1/chat/completions", keyA, snapshotInput(t, marker)); code != http.StatusOK {
		t.Fatalf("管理能力探测阻塞改变模型调用: %d", code)
	}
	verifySnapshotUpstream(t, received, marker)
	compatibilityControl(t, control, "")
	if err := <-done; err != nil {
		t.Fatal("释放能力屏障后管理请求失败")
	}
	waitSnapshotRecord(t, h, marker)
}

// TestCompatibilityPrepareUsesPublishedState 验证模型准备只读已发布准入，不因额外宿主能力回调失败丢弃输入。
func TestCompatibilityPrepareUsesPublishedState(t *testing.T) {
	upstream, received := snapshotUpstream()
	defer upstream.Close()
	h, control := startCompatibilityHost(t, "", upstream.URL)
	h.monitorAll(t)
	compatibilityControl(t, control, "callback-unavailable")
	marker := "compatibility-prepare-no-capabilities-io"
	if code, _ := h.request(t, "POST", "/v1/chat/completions", keyA, snapshotInput(t, marker)); code != http.StatusOK {
		t.Fatalf("能力查询故障改变模型调用: %d", code)
	}
	verifySnapshotUpstream(t, received, marker)
	waitSnapshotRecord(t, h, marker)
}

// TestCompatibilityInitialRecovery 验证初始化缺扩展不造成自锁，宿主恢复后能建立采集器并采集下一提交。
func TestCompatibilityInitialRecovery(t *testing.T) {
	upstream, received := snapshotUpstream()
	defer upstream.Close()
	h, control := startCompatibilityHost(t, "extension-missing", upstream.URL)
	if state := readCompatibilityStatus(t, h); state.Available || state.Reason != "observer_extension_missing" {
		t.Fatal("初始化缺扩展状态不准确")
	}
	compatibilityControl(t, control, "")
	if state := readCompatibilityStatus(t, h); !state.Available {
		t.Fatalf("初始化失败后的恢复自锁: %q", state.Reason)
	}
	h.monitorAll(t)
	marker := "compatibility-initial-recovery"
	if code, _ := h.request(t, "POST", "/v1/chat/completions", keyA, snapshotInput(t, marker)); code != http.StatusOK {
		t.Fatalf("恢复模型调用失败: %d", code)
	}
	waitSnapshotRecord(t, h, marker)
	verifySnapshotUpstream(t, received, marker)
}

// TestCompatibilityAdmissionRecovery 验证兼容状态停止下一提交，却保留同代慢体快照；恢复重新核验且不清历史缺口。
func TestCompatibilityAdmissionRecovery(t *testing.T) {
	upstream, received := snapshotUpstream()
	defer upstream.Close()
	h, control := startCompatibilityHost(t, "", upstream.URL)
	h.monitorAll(t)
	baseline := "compatibility-before-failure"
	if code, _ := h.request(t, "POST", "/v1/chat/completions", keyA, snapshotInput(t, baseline)); code != http.StatusOK {
		t.Fatalf("正常模型调用失败: %d", code)
	}
	waitSnapshotRecord(t, h, baseline)
	old := "compatibility-old-snapshot"
	finish := beginSnapshotSubmission(t, h, snapshotInput(t, old))
	compatibilityControl(t, control, "bridge-unavailable")
	if state := readCompatibilityStatus(t, h); state.Available || state.Reason != "observer_bridge_unavailable" {
		t.Fatal("运行时桥接失效没有停采")
	}
	blocked := "compatibility-new-must-not-save"
	if code, _ := h.request(t, "POST", "/v1/chat/completions", keyA, snapshotInput(t, blocked)); code != http.StatusOK {
		t.Fatalf("停采改变模型调用: %d", code)
	}
	finish()
	waitSnapshotRecord(t, h, old)
	assertSnapshotSkipped(t, h, blocked)
	before := readCompatibilityStatus(t, h)
	if before.State.Gaps["observer_unavailable"] != 1 {
		t.Fatalf("本次停止准入应留一次缺口: %d", before.State.Gaps["observer_unavailable"])
	}
	compatibilityControl(t, control, "")
	if state := readCompatibilityStatus(t, h); !state.Available {
		t.Fatalf("恢复后未重新核验: %q", state.Reason)
	}
	recovered := "compatibility-after-recovery"
	if code, _ := h.request(t, "POST", "/v1/chat/completions", keyA, snapshotInput(t, recovered)); code != http.StatusOK {
		t.Fatalf("恢复后模型调用失败: %d", code)
	}
	waitSnapshotRecord(t, h, recovered)
	verifySnapshotUpstream(t, received, baseline, blocked, old, recovered)
	if records := snapshotRecords(t, h); len(records) != 3 {
		t.Fatalf("恢复必须保留历史并排除停采提交: %d", len(records))
	}
	for refresh := 0; refresh < 2; refresh++ {
		if state := readCompatibilityStatus(t, h); !state.Available || state.State.Gaps["observer_unavailable"] != 1 {
			t.Fatal("恢复或刷新清除了既有桥接损失信号")
		}
	}
}

// TestCompatibilityMissingObserver 验证真实注册结果缺少观察者时停采，每次提交记一次桥接缺口而刷新不增加。
func TestCompatibilityMissingObserver(t *testing.T) {
	upstream, received := snapshotUpstream()
	defer upstream.Close()
	h, _ := startCompatibilityHost(t, "observer-missing", upstream.URL)
	status := readCompatibilityStatus(t, h)
	if status.Available || status.Reason != "observer_bridge_unavailable" {
		t.Fatalf("不能把配置与 seed 就绪当成观察者可用: available=%v reason=%q", status.Available, status.Reason)
	}
	for round := uint64(1); round <= 2; round++ {
		marker := "compatibility-observer-missing"
		if code, _ := h.request(t, "POST", "/v1/chat/completions", keyA, snapshotInput(t, marker)); code != http.StatusOK {
			t.Fatalf("观察者缺失改变模型调用: %d", code)
		}
		verifySnapshotUpstream(t, received, marker)
		for refresh := 0; refresh < 2; refresh++ {
			state := readCompatibilityStatus(t, h)
			if state.State.Gaps["observer_unavailable"] != round {
				t.Fatalf("桥接缺口应为 %d，实际 %d", round, state.State.Gaps["observer_unavailable"])
			}
		}
	}
}

// TestCompatibilityContractReasons 验证旧契约、协议不齐和桥接不可用分别报告固定原因，模型仍成功。
func TestCompatibilityContractReasons(t *testing.T) {
	for _, test := range []struct{ mode, reason string }{
		{"version-incompatible", "observer_version_incompatible"},
		{"protocol-incompatible", "observer_version_incompatible"},
		{"bridge-unavailable", "observer_bridge_unavailable"},
		{"callback-unavailable", "observer_unavailable"},
	} {
		t.Run(test.mode, func(t *testing.T) {
			upstream, received := snapshotUpstream()
			defer upstream.Close()
			h, _ := startCompatibilityHost(t, test.mode, upstream.URL)
			status := readCompatibilityStatus(t, h)
			if status.Available || status.Reason != test.reason {
				t.Fatalf("兼容故障原因不准确: available=%v reason=%q", status.Available, status.Reason)
			}
			marker := "compatibility-" + test.mode
			if code, _ := h.request(t, "POST", "/v1/chat/completions", keyA, snapshotInput(t, marker)); code != http.StatusOK {
				t.Fatalf("兼容故障改变模型调用: %d", code)
			}
			verifySnapshotUpstream(t, received, marker)
		})
	}
}

// TestCompatibilityMissingExtension 验证宿主独立扩展缺失有明确原因且真实模型请求与上游输入保持不变。
func TestCompatibilityMissingExtension(t *testing.T) {
	upstream, received := snapshotUpstream()
	defer upstream.Close()
	h, _ := startCompatibilityHost(t, "extension-missing", upstream.URL)
	if code, _ := h.request(t, "GET", api+"/status", "", nil); code != http.StatusUnauthorized {
		t.Fatalf("兼容性状态必须受保护: %d", code)
	}
	status := readCompatibilityStatus(t, h)
	if status.Available || status.Reason != "observer_extension_missing" {
		t.Fatalf("扩展缺失应停采且说明原因: available=%v reason=%q", status.Available, status.Reason)
	}
	marker := "compatibility-missing-extension"
	if code, _ := h.request(t, "POST", "/v1/chat/completions", keyA, snapshotInput(t, marker)); code != http.StatusOK {
		t.Fatalf("扩展缺失改变模型调用: %d", code)
	}
	verifySnapshotUpstream(t, received, marker)
}
