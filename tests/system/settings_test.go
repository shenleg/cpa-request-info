// 本文件通过真实受保护管理接口验证单条正文上限，不调用插件私有配置或 handler。
package system

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// settingsLimit 读取公开上限配置；返回字节数，HTTP、JSON 或字段集合不符时终止测试。
func settingsLimit(t *testing.T, h *instance) int {
	t.Helper()
	code, raw := h.request(t, "GET", api+"/settings", adminKey, nil)
	var fields map[string]json.RawMessage
	var limit int
	if code != http.StatusOK || json.Unmarshal(raw, &fields) != nil || len(fields) != 3 || fields["retention_days"] == nil || fields["budget_bytes"] == nil || json.Unmarshal(fields["max_text_bytes"], &limit) != nil {
		t.Fatalf("settings 应返回正文上限、保留天数和总预算，实际 HTTP=%d", code)
	}
	return limit
}

// TestSettingsStrictJSONAndAuthorization 验证字段大小写、重复键、值类型与范围严格校验，拒绝写入不改变已保存上限。
func TestSettingsStrictJSONAndAuthorization(t *testing.T) {
	h := startHost(t, 1<<30, true, "")
	for _, method := range []string{"GET", "POST"} {
		for _, key := range []string{"", keyA} {
			code, _ := h.request(t, method, api+"/settings", key, []byte(`{"max_text_bytes":9}`))
			if code != http.StatusUnauthorized && code != http.StatusForbidden {
				t.Fatalf("非管理凭据不能访问settings，method=%s HTTP=%d", method, code)
			}
		}
	}
	code, _ := h.request(t, "POST", api+"/settings", adminKey, []byte(`{"max_text_bytes":9}`))
	if code != http.StatusOK {
		t.Fatalf("合法settings写入失败，HTTP=%d", code)
	}
	for _, input := range []string{
		`{}`, `null`, `[]`, `{"max_text_bytes":null}`, `{"max_text_bytes":0}`, `{"max_text_bytes":-1}`,
		`{"max_text_bytes":65537}`, `{"max_text_bytes":1.5}`, `{"max_text_bytes":"8"}`, `{"max_text_bytes":true}`,
		`{"Max_Text_Bytes":8}`, `{"max_text_bytes":8,"Max_Text_Bytes":9}`, `{"max_text_bytes":8,"unknown":false}`,
		`{"max_text_bytes":8,"max_text_bytes":9}`, `{"max_text_bytes":8} {}`, `{"max_text_bytes":8`,
	} {
		code, _ := h.request(t, "POST", api+"/settings", adminKey, []byte(input))
		if code != http.StatusBadRequest {
			t.Fatalf("非法settings应拒绝，案例=%q HTTP=%d", input, code)
		}
		if got := settingsLimit(t, h); got != 9 {
			t.Fatalf("拒绝写入改变上限，实际%d", got)
		}
	}
	for _, input := range []string{`{"max_text_bytes":1}`, `{"max_text_bytes":65536}`} {
		code, _ := h.request(t, "POST", api+"/settings", adminKey, []byte(input))
		if code != http.StatusOK {
			t.Fatalf("合法边界值应接受，HTTP=%d", code)
		}
	}
}

// TestSettingsLegacyConfigurations 验证旧map与version2三字段默认上限，并能通过管理API保存新上限。
func TestSettingsLegacyConfigurations(t *testing.T) {
	for _, versioned := range []bool{false, true} {
		t.Run(fmt.Sprintf("versioned-%t", versioned), func(t *testing.T) {
			h := startHost(t, 1<<30, true, "")
			ids := h.monitorAll(t)
			stateDir := filepath.Join(h.dir, "audit")
			h.stop()
			fixture := fmt.Sprintf(`{%q:"旧对象"}`, ids[0])
			if versioned {
				fixture = fmt.Sprintf(`{"version":2,"capture_paused":false,"objects":{%q:{"name":"旧对象","monitored":true}}}`, ids[0])
			}
			if err := os.WriteFile(filepath.Join(stateDir, "monitors.json"), []byte(fixture), 0600); err != nil {
				t.Fatal("旧格式合成配置写入失败")
			}
			h = startHost(t, 1<<30, true, "", stateDir)
			if got := settingsLimit(t, h); got != 65536 {
				t.Fatalf("旧格式默认上限错误，实际%d", got)
			}
			code, raw := h.request(t, "GET", api+"/objects", adminKey, nil)
			var catalog struct {
				Objects []struct {
					ID        string `json:"id"`
					Name      string `json:"name"`
					Monitored bool   `json:"monitored"`
				} `json:"objects"`
			}
			if code != http.StatusOK || json.Unmarshal(raw, &catalog) != nil {
				t.Fatal("旧格式对象目录不可用")
			}
			found := false
			for _, object := range catalog.Objects {
				if object.ID == ids[0] && object.Name == "旧对象" && object.Monitored {
					found = true
				}
			}
			if !found {
				t.Fatal("旧格式对象的名称或监控资格丢失")
			}
			snapshotMutation(t, h, "/settings", map[string]int{"max_text_bytes": 11})
			h.stop()
			h = startHost(t, 1<<30, true, "", stateDir)
			if got := settingsLimit(t, h); got != 11 {
				t.Fatalf("旧格式升级后上限未保存，实际%d", got)
			}
		})
	}
}

// TestSettingsRejectsInvalidPersistedConfiguration 验证磁盘上限字段非法时明确停采，禁止用默认值覆盖损坏配置。
func TestSettingsRejectsInvalidPersistedConfiguration(t *testing.T) {
	for name, fixture := range map[string]string{
		"null":         `{"version":2,"capture_paused":false,"objects":{},"max_text_bytes":null}`,
		"zero":         `{"version":2,"capture_paused":false,"objects":{},"max_text_bytes":0}`,
		"too-large":    `{"version":2,"capture_paused":false,"objects":{},"max_text_bytes":65537}`,
		"fraction":     `{"version":2,"capture_paused":false,"objects":{},"max_text_bytes":1.5}`,
		"string":       `{"version":2,"capture_paused":false,"objects":{},"max_text_bytes":"7"}`,
		"alias":        `{"version":2,"capture_paused":false,"objects":{},"Max_Text_Bytes":7}`,
		"unknown":      `{"version":2,"capture_paused":false,"objects":{},"max_text_bytes":7,"extra":true}`,
		"duplicate":    `{"version":2,"capture_paused":false,"objects":{},"max_text_bytes":7,"max_text_bytes":8}`,
		"null-version": `{"version":null,"capture_paused":false,"objects":{},"max_text_bytes":7}`,
	} {
		t.Run(name, func(t *testing.T) {
			h := startHost(t, 1<<30, true, "")
			stateDir := filepath.Join(h.dir, "audit")
			h.stop()
			if err := os.WriteFile(filepath.Join(stateDir, "monitors.json"), []byte(fixture), 0600); err != nil {
				t.Fatal("非法持久配置夹具写入失败")
			}
			h = startHost(t, 1<<30, true, "", stateDir)
			code, raw := h.request(t, "GET", api+"/status", adminKey, nil)
			var status struct {
				State struct {
					State  string `json:"state"`
					Reason string `json:"reason"`
				} `json:"state"`
			}
			if code != http.StatusOK || json.Unmarshal(raw, &status) != nil || status.State.State != "paused" || status.State.Reason != "storage_error" {
				t.Fatal("非法持久配置没有明确停采")
			}
			code, _ = h.request(t, "POST", api+"/settings", adminKey, []byte(`{"max_text_bytes":13}`))
			if code == http.StatusOK {
				t.Fatal("损坏配置被默认值覆盖")
			}
		})
	}
}

// TestSettingsPersistenceFailureDoesNotPublish 验证外部目录故障导致保存失败时不发布新上限，并显示停采诊断。
func TestSettingsPersistenceFailureDoesNotPublish(t *testing.T) {
	h := startHost(t, 1<<30, true, "")
	snapshotMutation(t, h, "/settings", map[string]int{"max_text_bytes": 13})
	if err := os.WriteFile(filepath.Join(h.dir, "audit", "external-settings-fault"), []byte("synthetic fault only"), 0400); err != nil {
		t.Fatal("合成权限故障创建失败")
	}
	code, _ := h.request(t, "POST", api+"/settings", adminKey, []byte(`{"max_text_bytes":29}`))
	if code == http.StatusOK {
		t.Fatal("存储故障被报告为保存成功")
	}
	if got := settingsLimit(t, h); got != 13 {
		t.Fatalf("保存失败发布了新上限，实际%d", got)
	}
	code, raw := h.request(t, "GET", api+"/status", adminKey, nil)
	var status struct {
		State struct {
			State        string `json:"state"`
			Reason       string `json:"reason"`
			MaxTextBytes int    `json:"max_text_bytes"`
		} `json:"state"`
	}
	if code != http.StatusOK || json.Unmarshal(raw, &status) != nil || status.State.State != "paused" || status.State.Reason != "storage_error" || status.State.MaxTextBytes != 13 {
		t.Fatal("保存失败未保留已发布上限和存储故障")
	}
}

// TestSettingsPageControls 验证公开静态页面提供明确上限控件及当前 HTTP 执行支持说明，不内嵌审计数据。
func TestSettingsPageControls(t *testing.T) {
	h := startHost(t, 1<<30, true, "")
	code, raw := h.request(t, "GET", "/v0/resource/plugins/request-input-audit/index.html", "", nil)
	for _, want := range []string{`id="max-text-bytes" type="number" min="1" max="65536"`, `id="save-settings"`, "HTTP 执行", "后续提交"} {
		if code != http.StatusOK || !strings.Contains(string(raw), want) {
			t.Fatalf("管理页面缺少上限控件或支持说明，目标=%q HTTP=%d", want, code)
		}
	}
}

// TestSettingsDefaultAndPersistence 验证默认上限、管理保存和正常重启后的公开读取一致。
func TestSettingsDefaultAndPersistence(t *testing.T) {
	h := startHost(t, 1<<30, true, "")
	if got := settingsLimit(t, h); got != 65536 {
		t.Fatalf("默认上限应为65536，实际%d", got)
	}
	code, _ := h.request(t, "POST", api+"/settings", adminKey, []byte(`{"max_text_bytes":7}`))
	if code != http.StatusOK {
		t.Fatalf("保存上限应成功，实际 HTTP=%d", code)
	}
	if got := settingsLimit(t, h); got != 7 {
		t.Fatalf("保存后上限应为7，实际%d", got)
	}
	stateDir := filepath.Join(h.dir, "audit")
	h.stop()
	h = startHost(t, 1<<30, true, "", stateDir)
	if got := settingsLimit(t, h); got != 7 {
		t.Fatalf("重启后上限应为7，实际%d", got)
	}
}
