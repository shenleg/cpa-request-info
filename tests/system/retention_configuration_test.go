// 本文件通过公开配置与隔离文件故障验证保留设置的持久化边界，不调用私有配置方法。
package system

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// TestRetentionPersistenceFailureDoesNotPublish 验证存储失败不部分发布已确认的缩短或正文上限。
func TestRetentionPersistenceFailureDoesNotPublish(t *testing.T) {
	h := startHost(t, 1<<30, true, "")
	if err := os.WriteFile(filepath.Join(h.stateDir, "external-retention-fault"), []byte("synthetic fault only"), 0400); err != nil {
		t.Fatal(err)
	}
	code, _ := h.request(t, "POST", api+"/settings", adminKey, []byte(`{"retention_days":1,"max_text_bytes":11,"confirm_retention_shortening":true}`))
	if code == http.StatusOK {
		t.Fatal("存储失败却报告设置成功")
	}
	if days, limit := retentionSettings(t, h); days != 7 || limit != 65536 {
		t.Fatal("存储失败发布了部分配置")
	}
	code, raw := h.request(t, "GET", api+"/status", adminKey, nil)
	var status struct {
		State struct {
			State  string `json:"state"`
			Reason string `json:"reason"`
		} `json:"state"`
	}
	if code != 200 || json.Unmarshal(raw, &status) != nil || status.State.State != "paused" || status.State.Reason != "storage_error" {
		t.Fatal("配置存储失败没有明确停采")
	}
}

// TestRetentionRejectsInvalidPersistedSettings 验证未知或不规范保留配置不会被默认值覆盖并继续采集。
func TestRetentionRejectsInvalidPersistedSettings(t *testing.T) {
	for name, extra := range map[string]string{
		"null-days":      `"retention_days":null,"retention_caps":[]`,
		"zero-days":      `"retention_days":0,"retention_caps":[]`,
		"large-days":     `"retention_days":31,"retention_caps":[]`,
		"missing-caps":   `"retention_days":7`,
		"null-caps":      `"retention_days":7,"retention_caps":null`,
		"unknown-caps":   `"retention_days":7,"retention_caps":[{"before":"2030-01-01T00:00:00Z","days":1,"extra":true}]`,
		"duplicate-days": `"retention_days":7,"retention_caps":[{"before":"2030-01-01T00:00:00Z","days":1,"days":2}]`,
		"unordered-caps": `"retention_days":7,"retention_caps":[{"before":"2030-01-02T00:00:00Z","days":1},{"before":"2030-01-01T00:00:00Z","days":2}]`,
		"unbounded-caps": `"retention_days":7,"retention_caps":[{"before":"2030-01-01T00:00:00Z","days":30}]`,
	} {
		t.Run(name, func(t *testing.T) {
			h := startHost(t, 1<<30, true, "")
			stateDir := h.stateDir
			h.stop()
			fixture := fmt.Sprintf(`{"version":3,"capture_paused":false,"max_text_bytes":65536,"objects":{},%s}`, extra)
			if err := os.WriteFile(filepath.Join(stateDir, "monitors.json"), []byte(fixture), 0600); err != nil {
				t.Fatal(err)
			}
			h = startHost(t, 1<<30, true, "", stateDir)
			code, raw := h.request(t, "GET", api+"/status", adminKey, nil)
			var status struct {
				State struct {
					State  string `json:"state"`
					Reason string `json:"reason"`
				} `json:"state"`
			}
			if code != 200 || json.Unmarshal(raw, &status) != nil || status.State.State != "paused" || status.State.Reason != "storage_error" {
				t.Fatal("非法保留配置没有明确停采")
			}
			code, _ = h.request(t, "POST", api+"/settings", adminKey, []byte(`{"retention_days":7}`))
			if code == 200 {
				t.Fatal("非法保留配置被默认值覆盖")
			}
		})
	}
}

// TestRetentionSQLitePermissionFailurePreservesHistory 验证主库及 PERSIST journal 权限故障明确停采，修复权限后幸存内容不变。
func TestRetentionSQLitePermissionFailurePreservesHistory(t *testing.T) {
	for _, name := range []string{"records.sqlite", "records.sqlite-journal"} {
		t.Run(name, func(t *testing.T) {
			received := time.Date(2030, 6, 1, 0, 0, 0, 0, time.UTC)
			clock := newRetentionClock(t, received)
			h := startRetentionHost(t, 1<<30, "", clock)
			h.monitorAll(t)
			marker := fmt.Sprintf("RETENTION-FAULT-%d", time.Now().UnixNano())
			old := searchSubmit(t, h, keyA, "retention-model", marker+"-OLD")
			retentionClock(t, clock, received.Add(24*time.Hour), 0)
			live := searchSubmit(t, h, keyA, "retention-model", marker+"-LIVE")
			assertRecordSchema(t, h.stateDir, 2)
			original := retentionOutput(t, h.stateDir)
			path := filepath.Join(h.stateDir, name)
			if err := os.Chmod(path, 0400); err != nil {
				t.Fatal(err)
			}
			retentionClock(t, clock, received.Add(7*24*time.Hour), 1)
			waitBudgetState(t, h, func(s budgetState) bool { return s.Reason == "storage_error" })
			// 故障期间查询也必须拒绝不安全文件；先修复权限，再单独核对存活内容及逻辑期限。
			if err := os.Chmod(path, 0600); err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(original, retentionOutput(t, h.stateDir)) {
				t.Fatal("权限故障期间维护修改或删除了 SQLite 记录")
			}
			if code, _ := h.request(t, "GET", api+"/detail?id="+old.ID, adminKey, nil); code != 404 {
				t.Fatal("维护失败使到期记录仍可查询")
			}
			code, raw := h.request(t, "GET", api+"/detail?id="+live.ID, adminKey, nil)
			var detail savedRecord
			if code != 200 || json.Unmarshal(raw, &detail) != nil || detail.Text != marker+"-LIVE" {
				t.Fatal("维护失败损坏了未到期历史")
			}
			if state := readBudgetState(t, h); state.Reason != "storage_error" {
				t.Fatal("仅恢复权限就自动解除真实存储故障")
			}
		})
	}
}

// TestRetentionSQLiteCorruptionIsNotRebuilt 验证损坏主库在重启时明确拒绝，不重建数据库或覆盖原始故障现场。
func TestRetentionSQLiteCorruptionIsNotRebuilt(t *testing.T) {
	h := startHost(t, 1<<30, true, "")
	h.monitorAll(t)
	searchSubmit(t, h, keyA, "audit-test-model", "RETENTION-CORRUPT-SQLITE-CONTROL")
	h.stop()
	path := filepath.Join(h.stateDir, "records.sqlite")
	original, err := os.ReadFile(path)
	if err != nil || len(original) < 100 {
		t.Fatal("缺少有效 SQLite 损坏夹具", err)
	}
	corrupt := append([]byte(nil), original...)
	copy(corrupt[:16], []byte("corrupt-sqlite-v1"))
	if err := os.WriteFile(path, corrupt, 0600); err != nil {
		t.Fatal(err)
	}
	h = startHost(t, 1<<30, true, "", h.stateDir)
	waitBudgetState(t, h, func(s budgetState) bool { return s.State == "paused" && s.Reason == "storage_error" })
	if code, _ := h.request(t, "POST", "/v1/chat/completions", keyA, snapshotInput(t, "FORBIDDEN_CORRUPT_SQLITE_INPUT")); code != 200 {
		t.Fatal("SQLite 损坏影响了模型请求")
	}
	h.stop()
	after, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(after, corrupt) {
		t.Fatal("损坏数据库被重建或改写", err)
	}
}
