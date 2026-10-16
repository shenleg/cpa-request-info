// 本文件验证官方回调的 HTTP 边界、匿名发现目录、代际隔离和 POST 搜索，不依赖旧宿主扩展。
package native

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"gopkg.in/yaml.v3"
)

// nativeTestDirectory 创建0700空目录，返回不含符号链接的路径；失败立即终止测试，目录保留不自动删除。
func nativeTestDirectory(t *testing.T) string {
	t.Helper()
	base, err := filepath.EvalSymlinks(os.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	dir, err := os.MkdirTemp(base, "cpa-native-unit-")
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("保留测试目录：%s", dir)
	return dir
}

// newTestApp 打开临时空目录并注册官方插件，返回实例及目录；失败立即终止测试，结束时关闭采集器。
func newTestApp(t *testing.T) (*App, string) {
	t.Helper()
	dir := nativeTestDirectory(t)
	a := New(func(method string, payload any) (json.RawMessage, error) {
		if method != "host.log" {
			t.Errorf("unexpected host method %s", method)
		}
		return json.RawMessage(`{}`), nil
	})
	t.Cleanup(a.Close)
	configureTestApp(t, a, dir)
	return a, dir
}

// configureTestApp 用指定目录重配置实例；断言注册契约和存储可用，失败立即终止测试。
func configureTestApp(t *testing.T, a *App, dir string) {
	t.Helper()
	config, err := yaml.Marshal(map[string]any{"state-dir": dir, "budget-bytes": 16 << 20})
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(map[string]any{"schema_version": 6, "config_yaml": config})
	if err != nil {
		t.Fatal(err)
	}
	result, err := a.Handle("plugin.register", raw)
	if err != nil {
		t.Fatal(err)
	}
	registration := result.(map[string]any)
	caps := registration["capabilities"].(map[string]bool)
	if len(caps) != 2 || !caps["request_interceptor"] || !caps["management_api"] || !a.available {
		t.Fatalf("registration %v, reason %s", registration, a.reason)
	}
}

// testInterception 返回官方字段名的合成执行回调；path 和 format 用于协议判定，正文不含真实凭据，无错误。
func testInterception(path, format string) map[string]any {
	return map[string]any{
		"RequestID": "execution-1", "TraceID": "same-trace", "SourceFormat": format,
		"Metadata": map[string]any{"request_path": path, "caller_scope": "synthetic-scope"},
		"Headers":  map[string][]string{"Authorization": {"SYNTHETIC-DO-NOT-READ"}},
		"Body":     []byte(`{"model":"execution-model","messages":[{"role":"user","content":"native execution text"}],"input":"native execution text"}`),
	}
}

// handleInterception 序列化合成回调并返回 Handle 的结果及错误；序列化失败立即终止测试。
func handleInterception(t *testing.T, a *App, input map[string]any) (any, error) {
	t.Helper()
	raw, err := json.Marshal(input)
	if err != nil {
		t.Fatal(err)
	}
	return a.Handle("request.intercept_before", raw)
}

// managementResult 调用正式管理入口并解码响应；返回状态码及 JSON 对象，RPC 或序列化失败立即终止测试。
func managementResult(t *testing.T, a *App, method, path string, body []byte, query map[string][]string) (int, map[string]any) {
	t.Helper()
	raw, err := json.Marshal(managementRequest{Method: method, Path: "/plugins/request-input-audit" + path, Body: body, Query: query})
	if err != nil {
		t.Fatal(err)
	}
	result, err := a.Handle("management.handle", raw)
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(result)
	if err != nil {
		t.Fatal(err)
	}
	var response struct {
		StatusCode int
		Body       []byte
	}
	if json.Unmarshal(encoded, &response) != nil {
		t.Fatal("invalid response envelope")
	}
	var data map[string]any
	if json.Unmarshal(response.Body, &data) != nil {
		t.Fatal("invalid response JSON")
	}
	return response.StatusCode, data
}

// TestHTTPExecutionDiscovery 验证三协议仅在路径和格式同时匹配时发现，默认不保存正文，且回调不等待管理锁。
func TestHTTPExecutionDiscovery(t *testing.T) {
	a, _ := newTestApp(t)
	for i, route := range []struct{ path, format string }{
		{"/v1/chat/completions", "openai"}, {"/v1/messages", "claude"},
		{"/v1/responses", "openai-response"}, {"/v1/responses/compact", "openai-response"},
		{"/backend-api/codex/responses", "openai-response"}, {"/backend-api/codex/responses/compact", "openai-response"},
	} {
		input := testInterception(route.path, route.format)
		input["Metadata"].(map[string]any)["caller_scope"] = fmt.Sprintf("scope-%d", i)
		if _, err := handleInterception(t, a, input); err != nil {
			t.Fatal(err)
		}
	}
	ids, full := a.observed.snapshot()
	if len(ids) != 6 || full || a.collector.Status().Saved != 0 || len(a.collector.Objects()) != 0 {
		t.Fatalf("unexpected unselected discovery: %v, full %v", ids, full)
	}
	for _, input := range []map[string]any{
		testInterception("/v1/chat/completions", "claude"), testInterception("/other", "openai"),
		{"Metadata": map[string]any{"source": "plugin_host_model_callback"}, "Headers": false},
	} {
		if _, err := handleInterception(t, a, input); err != nil {
			t.Fatal(err)
		}
	}
	ws := testInterception("/backend-api/codex/responses", "openai-response")
	ws["Headers"] = map[string][]string{"uPgRaDe": {"WebSocket"}, "cOnNeCtIoN": {"keep-alive, UpGrAdE"}}
	delete(ws, "RequestID")
	delete(ws["Metadata"].(map[string]any), "caller_scope")
	if _, err := handleInterception(t, a, ws); err != nil {
		t.Fatal(err)
	}
	if ids, _ := a.observed.snapshot(); len(ids) != 6 || a.collector.Status().Gaps["invalid_event"] != 0 {
		t.Fatal("skipped callbacks changed discovery or diagnostics")
	}
	a.mu.Lock()
	done := make(chan error, 1)
	go func() {
		_, err := handleInterception(t, a, testInterception("/v1/chat/completions", "openai"))
		done <- err
	}()
	select {
	case err := <-done:
		if err != nil {
			t.Error(err)
		}
	case <-time.After(2 * time.Second):
		t.Error("callback waited for management lock")
	}
	a.mu.Unlock()
}

// TestInvalidInterceptionReportsActualFailure 验证缺 ID/scope 和非法 JSON 都返回固定错误并只计实际收到的 invalid_event。
func TestInvalidInterceptionReportsActualFailure(t *testing.T) {
	a, _ := newTestApp(t)
	for _, field := range []string{"RequestID", "caller_scope", "Body"} {
		input := testInterception("/v1/chat/completions", "openai")
		switch field {
		case "caller_scope":
			delete(input["Metadata"].(map[string]any), field)
		case "Body":
			input[field] = []byte(`{"invalid":`)
		default:
			delete(input, field)
		}
		if _, err := handleInterception(t, a, input); err == nil || err.Error() != "invalid_interception" {
			t.Fatalf("%s: %v", field, err)
		}
	}
	for _, raw := range []string{`null`, `{`, `{"Metadata":{},"Metadata":{}}`, `[]`} {
		if _, err := a.Handle("request.intercept_before", []byte(raw)); err == nil || err.Error() != "invalid_interception" {
			t.Fatalf("invalid JSON: %v", err)
		}
	}
	if count := a.collector.Status().Gaps["invalid_event"]; count != 7 {
		t.Fatalf("actual failures = %d", count)
	}
	result, err := a.Handle("request.intercept_after", nil)
	raw, _ := json.Marshal(result)
	if err != nil || string(raw) != "{}" {
		t.Fatalf("After must return empty modification: %s, %v", raw, err)
	}
}

// TestPOSTSearchBoundary 验证搜索仅接收 POST JSON，拒绝重复/null/越界/未知参数且错误不回显搜索词。
func TestPOSTSearchBoundary(t *testing.T) {
	a, _ := newTestApp(t)
	code, status := managementResult(t, a, "GET", "/status", nil, nil)
	if code != 200 || status["capture_mode"] != "http_execution" || status["responses_websocket"] != false || status["catalog"] != "observed" {
		t.Fatalf("status %v", status)
	}
	for _, test := range []struct {
		method, path, body string
		query              map[string][]string
		want               int
	}{
		{"POST", "/records/search", `{"offset":0,"limit":20,"q":"SYNTHETIC-KEYWORD"}`, nil, 200},
		{"GET", "/records", ``, map[string][]string{"q": {"SYNTHETIC-KEYWORD"}}, 404},
		{"GET", "/records/search", ``, nil, 405},
		{"POST", "/records/search", `{}`, map[string][]string{"q": {"SYNTHETIC-KEYWORD"}}, 400},
		{"POST", "/records/search", `{"q":"SYNTHETIC-KEYWORD","q":"duplicate"}`, nil, 400},
		{"POST", "/records/search", `{"q":null}`, nil, 400},
		{"POST", "/records/search", `{"offset":"0"}`, nil, 400},
		{"POST", "/records/search", `{"limit":101}`, nil, 400},
		{"POST", "/records/search", `{"offset":-1}`, nil, 400},
		{"POST", "/records/search", `{"unknown":"SYNTHETIC-KEYWORD"}`, nil, 400},
		{"POST", "/records/search", `{"from":"invalid"}`, nil, 400},
		{"POST", "/records/search", `{} {}`, nil, 400},
	} {
		code, result := managementResult(t, a, test.method, test.path, []byte(test.body), test.query)
		raw, _ := json.Marshal(result)
		if code != test.want || strings.Contains(string(raw), "SYNTHETIC-KEYWORD") {
			t.Errorf("%s %s: code %d want %d, result %s", test.method, test.path, code, test.want, raw)
		}
	}
}

// TestObservedCatalogGeneration 验证满额显式报告且不阻止已选采集；换目录不会混入旧回调发现，重启只恢复已选配置。
func TestObservedCatalogGeneration(t *testing.T) {
	a, dir := newTestApp(t)
	input := testInterception("/v1/chat/completions", "openai")
	if _, err := handleInterception(t, a, input); err != nil {
		t.Fatal(err)
	}
	ids, _ := a.observed.snapshot()
	var selected string
	for id := range ids {
		selected = id
	}
	body, _ := json.Marshal(map[string]string{"object_id": selected, "name": "合成对象"})
	if code, result := managementResult(t, a, "POST", "/monitor", body, nil); code != 200 {
		t.Fatalf("monitor %d: %v", code, result)
	}
	for i := 0; i < maxObservedObjects; i++ {
		a.observed.add(fmt.Sprintf("%064x", i))
	}
	if code, result := managementResult(t, a, "GET", "/objects", nil, nil); code != 200 || result["catalog_full"] != true {
		t.Fatalf("full catalog %v", result)
	}
	for _, id := range []string{"execution-2", "execution-3"} {
		input["RequestID"] = id
		if _, err := handleInterception(t, a, input); err != nil {
			t.Fatal(err)
		}
	}
	deadline := time.Now().Add(3 * time.Second)
	for a.collector.Status().Saved < 2 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if a.collector.Status().Saved != 2 {
		t.Fatal("catalog full or shared TraceID blocked independent executions")
	}
	code, records := managementResult(t, a, "POST", "/records/search", []byte(`{}`), nil)
	if code != 200 || records["total"] != float64(2) {
		t.Fatalf("execution records %v", records)
	}
	for _, item := range records["records"].([]any) {
		id := item.(map[string]any)["id"]
		if id != "execution-2" && id != "execution-3" {
			t.Fatalf("record ID is not RequestID: %v", id)
		}
	}
	old := a.admission.Load()
	configureTestApp(t, a, nativeTestDirectory(t))
	old.observed.add(strings.Repeat("f", 64))
	if ids, full := a.observed.snapshot(); len(ids) != 0 || full {
		t.Fatal("old collector discovery leaked into new directory")
	}
	a.Close()
	configureTestApp(t, a, dir)
	if ids, _ := a.observed.snapshot(); len(ids) != 0 || !a.collector.Objects()[selected].Monitored {
		t.Fatal("restart must drop unselected discovery and restore monitored objects")
	}
	if code, result := managementResult(t, a, "GET", "/objects", nil, nil); code != 200 || len(result["objects"].([]any)) != 1 {
		t.Fatalf("restored catalog %v", result)
	}
}
