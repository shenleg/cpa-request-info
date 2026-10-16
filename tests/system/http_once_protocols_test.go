// 本文件以相同公开行为矩阵逐一验收三种HTTP协议，真实上游执行不作为新增客户端提交。
package system

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// Responses转换器在上游缺少created时会采用当前时刻；固定合成时间避免基线请求跨秒造成伪差异。
const httpMatrixReply = `{"id":"synthetic-http-matrix","created":1,"object":"chat.completion","choices":[{"index":0,"message":{"role":"assistant","content":"FORBIDDEN_HTTP_ONCE_REPLY"},"finish_reason":"stop"}]}`
const httpMatrixSSE = "data: {\"id\":\"synthetic-http-matrix\",\"created\":1,\"object\":\"chat.completion.chunk\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"FORBIDDEN_HTTP_ONCE_REPLY\"},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n"
const httpMatrixBrokenSSE = "data: {\"id\":\"synthetic-http-matrix\",\"created\":1,\"object\":\"chat.completion.chunk\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"FORBIDDEN_HTTP_ONCE_REPLY\"},\"finish_reason\":null}]}\n\nevent: error\ndata: {\"error\":{\"message\":\"synthetic terminal stream failure\",\"type\":\"server_error\"}}\n\n"

type httpAuditProtocol struct {
	name, path string
}

var httpAuditProtocols = []httpAuditProtocol{
	{"chat-completions", "/v1/chat/completions"},
	{"responses", "/v1/responses"},
	{"anthropic-messages", "/v1/messages"},
}

// input 返回协议原始提交；marker是唯一允许正文，stream控制公开流式调用，model用于路由失败夹具。
// 夹具包含不应进入审计的历史、指令和会话字段；编码错误终止测试，不输出正文。
func (p httpAuditProtocol) input(t *testing.T, marker string, stream bool, model string) []byte {
	t.Helper()
	body := map[string]any{"model": model, "stream": stream, "metadata": map[string]string{"session_id": "FORBIDDEN_MATRIX_SESSION"}}
	switch p.name {
	case "responses":
		body["instructions"] = "FORBIDDEN_MATRIX_SYSTEM"
		body["input"] = []any{map[string]string{"role": "user", "content": "FORBIDDEN_MATRIX_HISTORY"}, map[string]string{"role": "user", "content": marker}}
	case "anthropic-messages":
		body["system"], body["max_tokens"] = "FORBIDDEN_MATRIX_SYSTEM", 32
		body["messages"] = []any{map[string]string{"role": "user", "content": "FORBIDDEN_MATRIX_HISTORY"}, map[string]string{"role": "user", "content": marker}}
	case "chat-completions":
		body["stream_options"] = map[string]bool{"include_usage": true}
		body["messages"] = []any{map[string]string{"role": "system", "content": "FORBIDDEN_MATRIX_SYSTEM"}, map[string]string{"role": "user", "content": "FORBIDDEN_MATRIX_HISTORY"}, map[string]string{"role": "user", "content": marker}}
	}
	raw, err := json.Marshal(body)
	if err != nil {
		t.Fatal("协议提交夹具编码失败")
	}
	return raw
}

// assertMatrixUpstream 核对本地OpenAI兼容上游确实收到完整业务输入、历史和指令；raw只在本轮内存中比较。
// marker是独立预期正文；不匹配终止测试，不将宿主正常协议转换当作审计改写。
func assertMatrixUpstream(t *testing.T, raw []byte, marker string) {
	t.Helper()
	var forwarded struct {
		Model    string
		Messages []struct {
			Role    string
			Content json.RawMessage
		}
	}
	if json.Unmarshal(raw, &forwarded) != nil || forwarded.Model != "audit-test-model" || len(forwarded.Messages) < 3 {
		t.Fatal("可控上游未收到原提交业务内容")
	}
	last := forwarded.Messages[len(forwarded.Messages)-1]
	if last.Role != "user" || !bytes.Contains(last.Content, []byte(marker)) || !bytes.Contains(raw, []byte("FORBIDDEN_MATRIX_HISTORY")) || !bytes.Contains(raw, []byte("FORBIDDEN_MATRIX_SYSTEM")) {
		t.Fatal("审计截断或改变了实际上游输入")
	}
}

// receiveMatrixUpstream 等待本轮真实执行请求并核对独立预期；返回字节供审计开关对照，超时终止测试。
func receiveMatrixUpstream(t *testing.T, received <-chan []byte, marker string) []byte {
	t.Helper()
	select {
	case raw := <-received:
		assertMatrixUpstream(t, raw, marker)
		return raw
	case <-time.After(4 * time.Second):
		t.Fatal("本轮协议上游接收屏障未建立")
		return nil
	}
}

// assertMatrixPrivacy 检查管理输出、完整审计目录及诊断均不新增历史、指令、凭据、会话或回复；失败不输出原文。
func assertMatrixPrivacy(t *testing.T, h *instance, markers ...string) {
	t.Helper()
	forbidden := append([]string{keyA, keyB, adminKey, "synthetic-upstream-only", "FORBIDDEN_MATRIX_", "FORBIDDEN_HTTP_ONCE_REPLY"}, markers...)
	assertChatPrivacy(t, h, forbidden...)
	paths := []string{"/objects", "/settings", "/status", "/records/search"}
	for _, record := range snapshotRecords(t, h) {
		paths = append(paths, "/detail?id="+record.ID)
	}
	for _, path := range paths {
		code, raw := managementRead(t, h, path, adminKey)
		if code != 200 {
			t.Fatal("隐私检查管理输出不可用")
		}
		for _, marker := range forbidden {
			if bytes.Contains(raw, []byte(marker)) {
				t.Fatal("管理输出包含禁止内容")
			}
		}
	}
}

// TestHTTPProtocolsBeforeReply 验证每种协议的成功、失败、408及SSE在回复/首token前已保存，回复后仍仅一条。
func TestHTTPProtocolsBeforeReply(t *testing.T) {
	for _, p := range httpAuditProtocols {
		t.Run(p.name, func(t *testing.T) {
			for _, tc := range []struct {
				name   string
				stream bool
				code   int
				reply  string
			}{
				{"success", false, 200, httpMatrixReply},
				{"upstream_failure", false, 400, httpOnceError},
				{"upstream_408", false, 408, httpOnceError},
				{"sse", true, 200, httpMatrixSSE},
				{"sse_terminal_failure", true, 200, httpMatrixBrokenSSE},
			} {
				t.Run(tc.name, func(t *testing.T) {
					received := make(chan []byte, 4)
					releases := make(chan (<-chan struct{}), 4)
					upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
						if respondDiscovery(w, r) {
							return
						}
						raw, err := io.ReadAll(r.Body)
						if err != nil {
							w.WriteHeader(400)
							return
						}
						received <- raw
						select {
						case release := <-releases:
							select {
							case <-release:
							case <-r.Context().Done():
								return
							}
						case <-r.Context().Done():
							return
						}
						if tc.stream {
							w.Header().Set("Content-Type", "text/event-stream")
						} else {
							w.Header().Set("Content-Type", "application/json")
						}
						w.WriteHeader(tc.code)
						_, _ = io.WriteString(w, tc.reply)
					}))
					t.Cleanup(upstream.Close)
					h := startHostWithConfiguration(t, 1<<30, true, upstream.URL+"/v1", true, httpOnceNoRetry)
					baseline := startHostWithConfiguration(t, 1<<30, false, upstream.URL+"/v1", true, httpOnceNoRetry)
					h.monitorAll(t)
					_, _, gaps := snapshotCounters(t, h)
					for round := 0; round < 2; round++ {
						marker := fmt.Sprintf("MATRIX-%s-%s-%d-%d", p.name, tc.name, round, time.Now().UnixNano())
						body := p.input(t, marker, tc.stream, "audit-test-model")
						release := make(chan struct{})
						releases <- release
						unblock := func() {
							select {
							case <-release:
							default:
								close(release)
							}
						}
						t.Cleanup(unblock)
						result := beginHTTPOnce(t, h, t.Context(), body, p.path)
						actual := receiveMatrixUpstream(t, received, marker)
						record := waitSnapshotRecord(t, h, marker)
						if record.Protocol != p.name || record.Model != "audit-test-model" {
							t.Fatal("原协议或客户端模型丢失")
						}
						select {
						case <-result:
							t.Fatal("上游未回复却已有完成响应")
						default:
						}
						unblock()
						outcome := awaitHTTPOnce(t, result)
						ready := make(chan struct{})
						close(ready)
						releases <- ready
						original := awaitHTTPOnce(t, beginHTTPOnce(t, baseline, t.Context(), body, p.path))
						control := receiveMatrixUpstream(t, received, marker)
						if outcome.err != nil || original.err != nil || outcome.code != tc.code || outcome.code != original.code || !bytes.Equal(actual, control) || !bytes.Equal(outcome.body, original.body) {
							t.Fatal("审计改变了协议调用、上游字节或公开响应")
						}
						if tc.code == 200 && !bytes.Contains(outcome.body, []byte("FORBIDDEN_HTTP_ONCE_REPLY")) {
							t.Fatal("公开模型调用没有实际交付合成回复")
						}
						if tc.name == "sse_terminal_failure" && !bytes.Contains(outcome.body, []byte("synthetic terminal stream failure")) {
							t.Fatal("SSE未实际呈现首token后的失败")
						}
						assertHTTPOnceState(t, h, int64(round+1), gaps)
					}
					assertMatrixPrivacy(t, h)
					assertHTTPOnceErrorPrivacy(t, h, baseline, "FORBIDDEN_HTTP_ONCE_ERROR")
				})
			}
		})
	}
}
