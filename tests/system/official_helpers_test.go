// 本文件提供官方宿主的本地发现模型与 JSON 查询辅助，不输出身份材料或请求正文。
package system

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"

	"gopkg.in/yaml.v3"
)

const discoveryModel = "audit-discovery-model"

// configureDiscovery 为隔离实例配置真实本地模型；没有业务上游时同时提供内容与存储测试的模型。
// config 是完整合成 YAML，syntheticBusiness 选择独立业务路由；返回新 YAML，解析失败终止测试，服务器由测试关闭。
func configureDiscovery(t *testing.T, config string, syntheticBusiness bool) string {
	t.Helper()
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request struct {
			Stream bool `json:"stream"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			http.Error(w, "synthetic invalid JSON", http.StatusBadRequest)
			return
		}
		if request.Stream {
			w.Header().Set("Content-Type", "text/event-stream")
			_, _ = io.WriteString(w, httpMatrixSSE)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, httpMatrixReply)
	}))
	t.Cleanup(upstream.Close)
	var document map[string]any
	if err := yaml.Unmarshal([]byte(config), &document); err != nil {
		t.Fatal("合成宿主配置解析失败")
	}
	models := []any{map[string]any{"name": discoveryModel, "alias": discoveryModel}}
	if syntheticBusiness {
		for _, alias := range []string{"audit-test-model", "responses-client-model", "client-search-model", "different-model", "search-literal-model", "retention-model", "client-repeat", strings.Repeat("m", 1024)} {
			models = append(models, map[string]any{"name": "audit-test-model", "alias": alias})
		}
	}
	providers, _ := document["openai-compatibility"].([]any)
	if !syntheticBusiness && len(providers) != 0 {
		provider := providers[0].(map[string]any)
		configured := provider["models"].([]any)
		model := configured[0].(map[string]any)["name"]
		provider["models"] = append(configured, map[string]any{"name": model, "alias": discoveryModel})
		raw, err := yaml.Marshal(document)
		if err != nil {
			t.Fatal("合成宿主配置编码失败")
		}
		return string(raw)
	}
	// 原生 Messages 夹具仍使用同一个本地服务器，发现请求与正式断言通道分开。
	baseURL := upstream.URL + "/v1"
	if entries, ok := document["claude-api-key"].([]any); ok {
		baseURL = entries[0].(map[string]any)["base-url"].(string)
	}
	document["openai-compatibility"] = append(providers, map[string]any{
		"name": "audit-local-discovery", "base-url": baseURL,
		"api-key-entries": []any{map[string]any{"api-key": "synthetic-discovery-upstream-only"}}, "models": models,
	})
	raw, err := yaml.Marshal(document)
	if err != nil {
		t.Fatal("合成宿主配置编码失败")
	}
	return string(raw)
}

// respondDiscovery 在真实已配置本地上游单独应答发现请求，不向正式场景通道发送额外消息。
// 返回是否已处理；普通请求恢复正文，读体失败直接返回400，不输出或持久化内容。
func respondDiscovery(w http.ResponseWriter, r *http.Request) bool {
	if r.Method != http.MethodPost {
		return false
	}
	raw, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, "synthetic read failure", http.StatusBadRequest)
		return true
	}
	r.Body = io.NopCloser(bytes.NewReader(raw))
	if !bytes.Contains(raw, []byte("synthetic-discovery-only")) {
		return false
	}
	w.Header().Set("Content-Type", "application/json")
	_, _ = io.WriteString(w, httpMatrixReply)
	return true
}

// discoverCallers 仅在新且可用的目录发起两个真实成功模型调用，发现默认未监控的匿名对象。
// 返回前核对无记录；恢复目录和明确不可用目录不发送发现请求，管理或调用失败终止测试。
func (h *instance) discoverCallers(t *testing.T) {
	t.Helper()
	code, raw := h.request(t, "GET", api+"/status", adminKey, nil)
	// 根路由可先于动态插件路由就绪；环境管理密码不做 bcrypt，首次查询更容易命中这个窗口。
	deadline := time.Now().Add(15 * time.Second)
	for code == http.StatusNotFound && time.Now().Before(deadline) {
		time.Sleep(25 * time.Millisecond)
		code, raw = h.request(t, "GET", api+"/status", adminKey, nil)
	}
	var status struct {
		Available bool `json:"available"`
		State     struct {
			State string `json:"state"`
		} `json:"state"`
	}
	if code != http.StatusOK || json.Unmarshal(raw, &status) != nil {
		t.Fatalf("发现前采集状态不可用：HTTP=%d", code)
	}
	// 损坏配置和数据库的回归夹具仍须能启动宿主；不能要求其历史查询成功才允许断言故障。
	if !status.Available || status.State.State != "active" {
		return
	}
	code, raw = h.request(t, "GET", api+"/objects", adminKey, nil)
	var catalog struct {
		Available bool `json:"available"`
		Objects   []struct {
			ID string `json:"id"`
		} `json:"objects"`
	}
	if code != http.StatusOK || json.Unmarshal(raw, &catalog) != nil {
		t.Fatal("发现前对象目录不可用")
	}
	if !catalog.Available || len(catalog.Objects) != 0 {
		return
	}
	for _, key := range []string{keyA, keyB} {
		body := []byte(`{"model":"audit-discovery-model","messages":[{"role":"user","content":"synthetic-discovery-only"}]}`)
		code, _ := h.request(t, "POST", "/v1/chat/completions", key, body)
		if code != http.StatusOK {
			t.Fatalf("已配置本地发现模型调用失败: HTTP=%d", code)
		}
	}
	code, raw = h.search(t, adminKey, map[string]any{"limit": 1})
	var page struct {
		Total int `json:"total"`
	}
	if code != http.StatusOK || json.Unmarshal(raw, &page) != nil || page.Total != 0 {
		t.Fatal("默认未监控的发现调用污染了正式记录")
	}
}

// search 把全部筛选放入 JSON 正文，返回管理状态与响应；编码或网络失败终止测试，不把关键词放入 URL。
func (h *instance) search(t *testing.T, key string, filters map[string]any) (int, []byte) {
	t.Helper()
	if filters == nil {
		filters = map[string]any{}
	}
	body, err := json.Marshal(filters)
	if err != nil {
		t.Fatal("查询正文编码失败")
	}
	return h.request(t, "POST", api+"/records/search", key, body)
}

// managementRead 查询路径表中的管理资源；搜索专用 POST，其他资源用 GET，返回状态与响应，网络失败终止测试。
func managementRead(t *testing.T, h *instance, path, key string) (int, []byte) {
	t.Helper()
	if path == "/records/search" {
		return h.search(t, key, nil)
	}
	return h.request(t, "GET", api+path, key, nil)
}

// mustSearchValues 解码旧表格中的合成筛选字面量，返回参数；无效编码终止测试，不进行网络请求。
func mustSearchValues(t *testing.T, encoded string) url.Values {
	t.Helper()
	query, err := url.ParseQuery(encoded)
	if err != nil {
		t.Fatal("合成筛选编码错误")
	}
	return query
}

// searchValues 将现有语义测试的筛选转换为 JSON 字段；分页转为整数，重复值以数组发送以验证拒绝。
// 返回状态和响应；参数解析失败终止测试，不发送查询字符串。
func (h *instance) searchValues(t *testing.T, key string, query url.Values) (int, []byte) {
	t.Helper()
	filters := make(map[string]any, len(query))
	for name, values := range query {
		if len(values) != 1 {
			filters[name] = values
			continue
		}
		if name == "offset" || name == "limit" {
			value, err := strconv.Atoi(values[0])
			if err != nil {
				filters[name] = values[0]
			} else {
				filters[name] = value
			}
		} else {
			filters[name] = values[0]
		}
	}
	return h.search(t, key, filters)
}
