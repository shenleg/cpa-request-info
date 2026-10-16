//go:build legacy_patched

// 本文件保留私有原始入站观察点的业务改写前契约；当前模型来源由 TestOfficialCurrentDDModel 验证。
package system

import (
	"bytes"
	"encoding/json"
	"io"
	"maps"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// TestResponsesHTTPBeforeBusinessRewrite 验证宿主把孤立工具输出改写成 user 执行时，审计仍只看到改写前本次输入。
func TestResponsesHTTPBeforeBusinessRewrite(t *testing.T) {
	received := make(chan []byte, 4)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if respondDiscovery(w, r) {
			return
		}
		raw, err := io.ReadAll(r.Body)
		if err != nil {
			http.Error(w, "synthetic read failure", 400)
			return
		}
		received <- raw
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"id":"synthetic-rewrite","created":1,"object":"chat.completion","choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}]}`)
	}))
	defer upstream.Close()
	// 使用宿主已有 opt-in 业务功能触发可观察的 input 改写，不增加审计故障或调试后门。
	config := "upstream:\n  codex:\n    orphan-delegation-compatibility: true\n"
	off := startHostWithConfiguration(t, 1<<30, false, upstream.URL+"/v1", false, config)
	h := startHostWithConfiguration(t, 1<<30, true, upstream.URL+"/v1", false, config)
	h.monitorAll(t)
	_, _, before := snapshotCounters(t, h)
	body := `{"model":"responses-client-model","previous_response_id":"FORBIDDEN_RESPONSES_REWRITE_PREVIOUS","input":[{"role":"user","content":"FORBIDDEN_RESPONSES_REWRITE_HISTORY"},{"type":"function_call_output","namespace":"codex_app","name":"create_thread","output":"FORBIDDEN_RESPONSES_REWRITE_TOOL"}]}`
	var plainRequest, plainResponse []byte
	for _, host := range []*instance{off, h} {
		req, err := http.NewRequest("POST", host.base+"/v1/responses", strings.NewReader(body))
		if err != nil {
			t.Fatal("改写夹具构造失败")
		}
		req.Header.Set("Authorization", "Bearer "+keyA)
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("X-Openai-Subagent", "collab_spawn")
		response, err := host.client.Do(req)
		if err != nil {
			t.Fatal("改写夹具模型请求失败")
		}
		raw, readErr := io.ReadAll(response.Body)
		closeErr := response.Body.Close()
		if response.StatusCode != 200 || readErr != nil || closeErr != nil {
			t.Fatal("业务改写的原调用未成功")
		}
		select {
		case actual := <-received:
			var forwarded struct {
				Messages []struct {
					Role    string          `json:"role"`
					Content json.RawMessage `json:"content"`
				} `json:"messages"`
			}
			if json.Unmarshal(actual, &forwarded) != nil || len(forwarded.Messages) != 2 || forwarded.Messages[1].Role != "user" || !bytes.Contains(forwarded.Messages[1].Content, []byte("FORBIDDEN_RESPONSES_REWRITE_TOOL")) {
				t.Fatal("夹具没有真正触发宿主工具输出到 user 的业务改写")
			}
			if plainRequest == nil {
				plainRequest, plainResponse = actual, raw
			} else if !bytes.Equal(plainRequest, actual) || !bytes.Equal(plainResponse, raw) {
				t.Fatal("审计改变了实际业务改写或响应")
			}
		case <-time.After(3 * time.Second):
			t.Fatal("未取得本轮业务改写的实际上游输入")
		}
	}
	h.request(t, "POST", "/v1/responses", keyA, []byte(`{"model":"audit-test-model","input":"RESPONSES-REWRITE-DRAIN"}`))
	waitSnapshotRecord(t, h, "RESPONSES-REWRITE-DRAIN")
	saved, queued, after := snapshotCounters(t, h)
	if saved != 1 || queued != 0 || !maps.Equal(before, after) {
		t.Fatal("宿主改写的工具内容/历史扩大了审计范围")
	}
	assertChatPrivacy(t, h, "FORBIDDEN_RESPONSES_")
}
