// 本文件从真实 HTTP 入站、可控上游和受保护管理接口验证一次提交，不以执行回调或私有队列为证据。
package system

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"maps"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

const httpOnceReply = `{"id":"synthetic-http-once","object":"chat.completion","choices":[{"index":0,"message":{"role":"assistant","content":"FORBIDDEN_HTTP_ONCE_REPLY"},"finish_reason":"stop"}]}`
const httpOnceSSE = "data: {\"id\":\"synthetic-http-once\",\"object\":\"chat.completion.chunk\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"FORBIDDEN_HTTP_ONCE_REPLY\"},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n"
const httpOnceError = `{"error":{"message":"FORBIDDEN_HTTP_ONCE_ERROR","type":"invalid_request_error"}}`
const httpOnceBrokenSSE = "data: {\"id\":\"synthetic-http-once\",\"object\":\"chat.completion.chunk\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"FORBIDDEN_HTTP_ONCE_REPLY\"},\"finish_reason\":null}]}\n\nevent: error\ndata: {\"error\":{\"message\":\"synthetic terminal stream failure\",\"type\":\"server_error\"}}\n\n"

// 失败场景只验证本轮响应，不让宿主额外重试/冷却等待混入屏障；重试另有专门公开配置测试。
const httpOnceNoRetry = "routing:\n  retry:\n    request-retry: 0\n    max-retry-interval: 0\n  cooldown:\n    disable-cooling: true\n"

type httpOnceOutcome struct {
	code int
	body []byte
	err  error
}

// beginHTTPOnce 发出公开模型请求并异步读取完整响应；ctx 可取消，body 是合成原始提交。
// path 省略时使用 Chat 路径；返回结果通道，网络/读取错误随结果返回，调用者必须等待或取消。
func beginHTTPOnce(t *testing.T, h *instance, ctx context.Context, body []byte, path ...string) <-chan httpOnceOutcome {
	t.Helper()
	route := "/v1/chat/completions"
	if len(path) > 0 {
		route = path[0]
	}
	req, err := http.NewRequestWithContext(ctx, "POST", h.base+route, bytes.NewReader(body))
	if err != nil {
		t.Fatal("一次提交请求构造失败")
	}
	req.Header.Set("Authorization", "Bearer "+keyA)
	req.Header.Set("Content-Type", "application/json")
	result := make(chan httpOnceOutcome, 1)
	go func() {
		resp, err := h.client.Do(req)
		if err != nil {
			result <- httpOnceOutcome{err: err}
			return
		}
		raw, err := io.ReadAll(resp.Body)
		closeErr := resp.Body.Close()
		if err == nil {
			err = closeErr
		}
		result <- httpOnceOutcome{code: resp.StatusCode, body: raw, err: err}
	}()
	return result
}

// awaitHTTPOnce 等待本轮公开 HTTP 结束；返回结果，超时终止测试，不输出可能含合成输入的响应原文。
func awaitHTTPOnce(t *testing.T, result <-chan httpOnceOutcome) httpOnceOutcome {
	t.Helper()
	select {
	case outcome := <-result:
		return outcome
	case <-time.After(6 * time.Second):
		t.Fatal("一次提交客户端未在期限内结束")
		return httpOnceOutcome{}
	}
}

// assertHTTPOnceState 核对保存数、队列排空和独立预期缺口；无返回值，不一致终止测试。
// 调用者先等待本轮唯一详情或故障状态，避免将入队当作保存，也避免固定 sleep 的负向检查。
func assertHTTPOnceState(t *testing.T, h *instance, wantSaved int64, wantGaps map[string]uint64) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		saved, queued, gaps := snapshotCounters(t, h)
		if queued == 0 {
			if saved != wantSaved || !maps.Equal(gaps, wantGaps) {
				t.Fatalf("一次提交计数不符：saved=%d want=%d", saved, wantSaved)
			}
			for _, record := range snapshotRecords(t, h) {
				assertHTTPOnceDetailMetadata(t, h, record.ID)
			}
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("一次提交审计队列未排空")
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// assertHTTPOnceDetailMetadata 核对 id 的公开详情仅含输入白名单及当前名称，不混入响应、执行状态或追踪信息。
// 无返回值，HTTP、JSON 或白名单校验失败终止测试；不输出详情原文。
func assertHTTPOnceDetailMetadata(t *testing.T, h *instance, id string) {
	t.Helper()
	code, raw := h.request(t, "GET", api+"/detail?id="+id, adminKey, nil)
	var fields map[string]json.RawMessage
	if code != 200 || json.Unmarshal(raw, &fields) != nil || fields["id"] == nil || fields["text"] == nil {
		t.Fatal("一次提交公开详情不可用")
	}
	for key := range fields {
		switch key {
		case "id", "object_id", "name", "received_at", "protocol", "purpose", "model", "request_reasoning", "text", "truncated", "extraction_incomplete":
		default:
			t.Fatal("管理详情含执行结果或追踪白名单之外的字段")
		}
	}
}

// TestHTTPOnceCapturedBeforeReply 验证成功、失败及 SSE 首 token 前已经保存，每轮回复后仍只有一条且不新增模型失败缺口。
func TestHTTPOnceCapturedBeforeReply(t *testing.T) {
	for _, tc := range []struct {
		name   string
		stream bool
		code   int
		reply  string
	}{
		{"success", false, 200, httpOnceReply},
		{"upstream_failure", false, 400, httpOnceError},
		{"upstream_timeout", false, 408, `{"error":{"message":"synthetic upstream timeout","type":"server_error"}}`},
		{"sse", true, 200, httpOnceSSE},
		{"sse_terminal_failure", true, 200, httpOnceBrokenSSE},
	} {
		t.Run(tc.name, func(t *testing.T) {
			type exchange struct {
				received chan []byte
				release  chan struct{}
			}
			plans := make(chan exchange, 2)
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if respondDiscovery(w, r) {
					return
				}
				var plan exchange
				select {
				case plan = <-plans:
				case <-r.Context().Done():
					return
				}
				raw, err := io.ReadAll(r.Body)
				if err != nil {
					w.WriteHeader(400)
					return
				}
				plan.received <- raw
				select {
				case <-plan.release:
				case <-r.Context().Done():
					return
				}
				contentType := "application/json"
				if tc.stream {
					contentType = "text/event-stream"
				}
				w.Header().Set("Content-Type", contentType)
				w.WriteHeader(tc.code)
				_, _ = io.WriteString(w, tc.reply)
			}))
			t.Cleanup(upstream.Close)
			h := startHostWithConfiguration(t, 1<<30, true, upstream.URL+"/v1", true, httpOnceNoRetry)
			baseline := startHostWithConfiguration(t, 1<<30, false, upstream.URL+"/v1", true, httpOnceNoRetry)
			h.monitorAll(t)
			_, _, gaps := snapshotCounters(t, h)
			for round := 0; round < 2; round++ {
				plan := exchange{make(chan []byte, 1), make(chan struct{})}
				plans <- plan
				// Cleanup 先于上游 Close 执行；失败时也释放阻塞请求，不遗留模型执行进程。
				release := func() {
					select {
					case <-plan.release:
					default:
						close(plan.release)
					}
				}
				t.Cleanup(release)
				marker := fmt.Sprintf("HTTP-ONCE-%s-%d-%d", tc.name, round, time.Now().UnixNano())
				// 宿主流式 executor 既有逻辑添加 include_usage；显式提交它避免把正常业务改写误判为审计改写。
				body := []byte(fmt.Sprintf(`{"model":"audit-test-model","stream":%t,"stream_options":{"include_usage":true},"messages":[{"role":"system","content":"FORBIDDEN_HTTP_ONCE_SYSTEM"},{"role":"user","content":%q}]}`, tc.stream, marker))
				result := beginHTTPOnce(t, h, t.Context(), body)
				select {
				case raw := <-plan.received:
					if !bytes.Equal(raw, body) {
						t.Fatal("接收观察改变了上游原始请求")
					}
				case <-time.After(3 * time.Second):
					t.Fatal("上游接收屏障未建立")
				}
				record := waitSnapshotRecord(t, h, marker)
				if record.Protocol != "chat-completions" || record.Model != "audit-test-model" {
					t.Fatal("未响应提交的协议或请求模型不符")
				}
				select {
				case <-result:
					t.Fatal("上游未释放却已有完成响应")
				default:
				}
				release()
				outcome := awaitHTTPOnce(t, result)
				if outcome.err != nil || outcome.code != tc.code {
					t.Fatal("公开成功、失败或 SSE 响应未正常结束")
				}
				control := exchange{make(chan []byte, 1), make(chan struct{})}
				close(control.release)
				plans <- control
				original := awaitHTTPOnce(t, beginHTTPOnce(t, baseline, t.Context(), body))
				// SSE framing 属于宿主既有输出，比较关闭审计的公开结果，而非假设是上游 wire bytes。
				if original.err != nil || original.code != outcome.code || !bytes.Equal(original.body, outcome.body) {
					t.Fatal("审计改变了公开成功、失败或 SSE 响应")
				}
				select {
				case raw := <-control.received:
					if !bytes.Equal(raw, body) {
						t.Fatal("基准宿主转发请求与合成原始提交不符")
					}
				default:
					t.Fatal("未收到关闭审计的基准上游提交")
				}
				if tc.stream {
					wantDone := tc.name != "sse_terminal_failure"
					if !bytes.Contains(outcome.body, []byte("FORBIDDEN_HTTP_ONCE_REPLY")) || bytes.Contains(outcome.body, []byte("data: [DONE]")) != wantDone {
						t.Fatal("SSE 成功/失败完成语义不符")
					}
					if !wantDone && !bytes.Contains(outcome.body, []byte("synthetic terminal stream failure")) {
						t.Fatal("SSE 失败被伪装为成功完成")
					}
				}
				assertHTTPOnceState(t, h, int64(round+1), gaps)
				code, raw := h.request(t, "GET", api+"/detail?id="+record.ID, adminKey, nil)
				var detail savedRecord
				if code != 200 || json.Unmarshal(raw, &detail) != nil || detail.ID != record.ID || detail.Text != marker {
					t.Fatal("模型结束撤销或改变了已保存输入")
				}
			}
			assertChatPrivacy(t, h, "FORBIDDEN_HTTP_ONCE_SYSTEM", "FORBIDDEN_HTTP_ONCE_REPLY", "FORBIDDEN_HTTP_ONCE_ERROR")
		})
	}
}
