// 本文件通过公开配置与隔离文件故障验证保留设置的持久化边界，不调用私有配置方法。
package system

import (
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

// TestRetentionTemporaryFailurePreservesLiveHistory 验证回收遇到外部临时路径故障时不修改原始段，历史仍按期限查询。
func TestRetentionTemporaryFailurePreservesLiveHistory(t *testing.T) {
	received := time.Date(2030, 6, 1, 0, 0, 0, 0, time.UTC)
	clock := newRetentionClock(t, received)
	h := startRetentionHost(t, 1<<30, "", clock)
	h.monitorAll(t)
	marker := fmt.Sprintf("RETENTION-FAULT-%d", time.Now().UnixNano())
	old := searchSubmit(t, h, keyA, "retention-model", marker+"-OLD")
	retentionClock(t, clock, received.Add(24*time.Hour), 0)
	live := searchSubmit(t, h, keyA, "retention-model", marker+"-LIVE")
	original := retentionOutput(t, h.stateDir)
	// 故障属于隔离磁盘边界，不为生产加入注入API；不能把不安全路径当作可删除的临时副本。
	if err := os.WriteFile(filepath.Join(h.stateDir, "retention.tmp"), []byte("synthetic external fault"), 0400); err != nil {
		t.Fatal(err)
	}
	retentionClock(t, clock, received.Add(7*24*time.Hour), 1)
	deadline := time.Now().Add(5 * time.Second)
	for {
		code, raw := h.request(t, "GET", api+"/status", adminKey, nil)
		var status struct {
			State struct {
				Reason string `json:"reason"`
			} `json:"state"`
		}
		if code != 200 || json.Unmarshal(raw, &status) != nil {
			t.Fatal("清理故障状态不可读")
		}
		if status.State.Reason == "storage_error" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("清理故障没有明确报告")
		}
		time.Sleep(20 * time.Millisecond)
	}
	if string(retentionOutput(t, h.stateDir)) != string(original) {
		t.Fatal("维护失败修改或删除了权威历史")
	}
	code, _ := h.request(t, "GET", api+"/detail?id="+old.ID, adminKey, nil)
	if code != 404 {
		t.Fatal("清理失败使到期记录仍可查询")
	}
	code, _ = h.request(t, "GET", api+"/detail?id="+live.ID, adminKey, nil)
	if code != 200 {
		t.Fatal("清理失败损坏了未到期历史")
	}
}
