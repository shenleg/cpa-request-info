// 本文件核对 Responses 审计副本与真实执行相互独立；用可控上游观察协议转换，不保存其正文。
package system

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"maps"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// TestResponsesHTTPUTF8AndFailOpen 验证 UTF-8 字符前缀、双状态和原协议来源，审计开关不改变上游请求及公开响应。
func TestResponsesHTTPUTF8AndFailOpen(t *testing.T) {
	received := make(chan []byte, 16)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, err := io.ReadAll(r.Body)
		if err != nil {
			http.Error(w, "synthetic read failure", 400)
			return
		}
		received <- raw
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"id":"synthetic-responses-reply","created":1,"object":"chat.completion","model":"different-reply-model","choices":[{"index":0,"message":{"role":"assistant","content":"FORBIDDEN_RESPONSES_UPSTREAM_REPLY"},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}}`)
	}))
	defer upstream.Close()
	off := startHost(t, 1<<30, false, upstream.URL+"/v1")
	h := startHost(t, 1<<30, true, upstream.URL+"/v1")
	h.monitorAll(t)
	// 65536 为正文上限；汉字三字节、emoji 四字节、[图片]八字节，期望不来自提取器。
	cases := []struct {
		name                  string
		asciiBytes            int
		blocks                []any
		stringInput           bool
		wantSuffix            string
		truncated, incomplete bool
	}{
		{"exact_string", 65536, nil, true, "", false, false},
		{"split_emoji_string", 65535, []any{map[string]string{"type": "input_text", "text": "🙂FORBIDDEN_RESPONSES_STRING_TAIL"}}, true, "", true, false},
		{"exact_image", 65528, []any{map[string]string{"type": "input_image", "image_url": "FORBIDDEN_RESPONSES_LIMIT_IMAGE"}}, false, "[图片]", false, false},
		{"split_chinese", 65535, []any{map[string]string{"type": "input_text", "text": "中FORBIDDEN_RESPONSES_CHINESE_TAIL"}, map[string]string{"type": "input_text", "text": "z"}}, false, "", true, false},
		{"combined", 65535, []any{map[string]string{"type": "input_text", "text": "🙂FORBIDDEN_RESPONSES_EMOJI_TAIL"}, map[string]string{"type": "future", "payload": "FORBIDDEN_RESPONSES_LIMIT_UNKNOWN"}, map[string]string{"type": "input_text", "text": "z"}}, false, "", true, true},
	}
	var combinedID string
	for i, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			marker := fmt.Sprintf("RESPONSES-UTF8-%d-", i)
			ascii := marker + strings.Repeat("a", tc.asciiBytes-len(marker))
			var input any = []any{map[string]any{"role": "user", "content": "FORBIDDEN_RESPONSES_LIMIT_HISTORY"}, map[string]any{"type": "message", "role": "user", "content": append([]any{map[string]string{"type": "input_text", "text": ascii}}, tc.blocks...)}}
			if tc.stringInput {
				input = ascii
				if len(tc.blocks) > 0 {
					input = ascii + "🙂FORBIDDEN_RESPONSES_STRING_TAIL"
				}
			}
			body, err := json.Marshal(map[string]any{"model": "responses-client-model", "input": input, "instructions": "FORBIDDEN_RESPONSES_LIMIT_INSTRUCTIONS", "previous_response_id": "FORBIDDEN_RESPONSES_LIMIT_PREVIOUS", "metadata": map[string]string{"session_id": "FORBIDDEN_RESPONSES_SESSION"}})
			if err != nil {
				t.Fatal("Responses 容量夹具编码失败")
			}
			var plainRequest, plainResponse []byte
			for _, host := range []*instance{off, h} {
				code, response := host.request(t, "POST", "/v1/responses", keyA, body)
				if code != 200 {
					t.Fatal("Responses 审计改变或拒绝了可控模型调用")
				}
				select {
				case actual := <-received:
					if plainRequest == nil {
						plainRequest, plainResponse = actual, response
					} else if !bytes.Equal(plainRequest, actual) || !bytes.Equal(plainResponse, response) {
						t.Fatal("Responses 审计改变实际上游字节或公开响应")
					}
					var forwarded struct {
						Model string `json:"model"`
					}
					if json.Unmarshal(actual, &forwarded) != nil || forwarded.Model != "audit-test-model" {
						t.Fatal("合成客户端别名没有映射到独立上游模型")
					}
				case <-time.After(3 * time.Second):
					t.Fatal("可控上游没有收到本轮 Responses 提交")
				}
			}
			r := waitChatDetail(t, h, marker)
			if r.Text != ascii+tc.wantSuffix || r.Model != "responses-client-model" || r.Protocol != "responses" || r.Truncated != tc.truncated || r.ExtractionIncomplete != tc.incomplete {
				t.Fatal("Responses 原协议输入、请求模型或容量状态不符合独立预期")
			}
			if tc.incomplete {
				combinedID = r.ID
			}
		})
	}
	for _, body := range []string{
		`{"model":"responses-client-model","input":[{"role":"user","content":[{"type":"future","payload":"FORBIDDEN_RESPONSES_ALL_UNKNOWN"}]}]}`,
		`{"model":"responses-client-model","input":[`,
	} {
		plainCode, plainResponse := off.request(t, "POST", "/v1/responses", keyA, []byte(body))
		code, response := h.request(t, "POST", "/v1/responses", keyA, []byte(body))
		if code != plainCode || !bytes.Equal(plainResponse, response) {
			t.Fatal("Responses 未知块/解析缺口改变原宿主响应")
		}
		if code == 200 {
			for _, host := range []string{"off", "on"} {
				select {
				case actual := <-received:
					if host == "off" {
						plainResponse = actual
					} else if !bytes.Equal(plainResponse, actual) {
						t.Fatal("Responses 解析失败改变了上游转发")
					}
				case <-time.After(3 * time.Second):
					t.Fatal("本轮失败边界上游观察缺失")
				}
			}
		}
	}
	h.request(t, "POST", "/v1/responses", keyA, []byte(`{"model":"missing","input":"RESPONSES-UTF8-DRAIN"}`))
	waitSnapshotRecord(t, h, "RESPONSES-UTF8-DRAIN")
	assertChatPrivacy(t, h, "FORBIDDEN_RESPONSES_")
	h.stop()
	restarted := startHost(t, 1<<30, true, "", filepath.Join(h.dir, "audit"))
	code, raw := restarted.request(t, "GET", api+"/detail?id="+combinedID, adminKey, nil)
	var r savedRecord
	if code != 200 || json.Unmarshal(raw, &r) != nil || r.Protocol != "responses" || !r.Truncated || !r.ExtractionIncomplete {
		t.Fatal("Responses 正常重启丢失协议或双状态记录")
	}
}

// TestResponsesHTTPRecordedBeforeReply 验证上游未回复时记录已落盘，随后模型失败不能取消该提交。
func TestResponsesHTTPRecordedBeforeReply(t *testing.T) {
	received := make(chan struct{}, 1)
	release := make(chan struct{})
	var once sync.Once
	unblock := func() { once.Do(func() { close(release) }) }
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if _, err := io.Copy(io.Discard, r.Body); err != nil {
			http.Error(w, "synthetic read failure", 400)
			return
		}
		received <- struct{}{}
		<-release
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(400)
		_, _ = io.WriteString(w, `{"error":{"message":"synthetic upstream rejection","type":"invalid_request_error"}}`)
	}))
	t.Cleanup(func() { unblock(); upstream.Close() })
	h := startHost(t, 1<<30, true, upstream.URL+"/v1")
	h.monitorAll(t)
	req, err := http.NewRequest("POST", h.base+"/v1/responses", strings.NewReader(`{"model":"responses-client-model","input":"RESPONSES-BEFORE-FAILED-REPLY"}`))
	if err != nil {
		t.Fatal("合成请求构造失败")
	}
	req.Header.Set("Authorization", "Bearer "+keyA)
	req.Header.Set("Content-Type", "application/json")
	outcome := make(chan int, 1)
	go func() {
		resp, err := h.client.Do(req)
		if err != nil {
			outcome <- 0
			return
		}
		_, _ = io.Copy(io.Discard, resp.Body)
		_ = resp.Body.Close()
		outcome <- resp.StatusCode
	}()
	select {
	case <-received:
	case <-time.After(3 * time.Second):
		t.Fatal("上游阻塞屏障未建立")
	}
	r := waitChatDetail(t, h, "RESPONSES-BEFORE-FAILED-REPLY")
	if r.Text != "RESPONSES-BEFORE-FAILED-REPLY" {
		t.Fatal("记录错误地等待模型回复")
	}
	unblock()
	select {
	case code := <-outcome:
		if code != 400 {
			t.Fatal("审计改变上游失败结果")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("模型请求没有完成")
	}
	code, _ := h.request(t, "GET", api+"/detail?id="+r.ID, adminKey, nil)
	if code != 200 {
		t.Fatal("上游失败撤销了已经保存的输入")
	}
	assertChatPrivacy(t, h)
}

// TestResponsesHTTPBeforeBusinessRewrite 验证宿主把孤立工具输出改写成 user 执行时，审计仍只看到改写前本次输入。
func TestResponsesHTTPBeforeBusinessRewrite(t *testing.T) {
	received := make(chan []byte, 4)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
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
	h.request(t, "POST", "/v1/responses", keyA, []byte(`{"model":"missing","input":"RESPONSES-REWRITE-DRAIN"}`))
	waitSnapshotRecord(t, h, "RESPONSES-REWRITE-DRAIN")
	saved, queued, after := snapshotCounters(t, h)
	if saved != 1 || queued != 0 || !maps.Equal(before, after) {
		t.Fatal("宿主改写的工具内容/历史扩大了审计范围")
	}
	assertChatPrivacy(t, h, "FORBIDDEN_RESPONSES_")
}

// TestResponsesHTTPSSE 验证 Responses 流式调用采用同一原始提交观察点，不依赖 SSE 完成后采集。
func TestResponsesHTTPSSE(t *testing.T) {
	received := make(chan []byte, 2)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, err := io.ReadAll(r.Body)
		if err != nil {
			http.Error(w, "synthetic read failure", 400)
			return
		}
		received <- raw
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "data: {\"id\":\"synthetic-sse\",\"created\":1,\"object\":\"chat.completion.chunk\",\"model\":\"different-reply-model\",\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\",\"content\":\"FORBIDDEN_RESPONSES_SSE_REPLY\"},\"finish_reason\":null}]}\n\ndata: {\"id\":\"synthetic-sse\",\"created\":1,\"object\":\"chat.completion.chunk\",\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n")
	}))
	defer upstream.Close()
	off := startHost(t, 1<<30, false, upstream.URL+"/v1")
	h := startHost(t, 1<<30, true, upstream.URL+"/v1")
	h.monitorAll(t)
	body := []byte(`{"model":"responses-client-model","stream":true,"input":"RESPONSES-SSE-INPUT"}`)
	var plainRequest, plainResponse []byte
	for _, host := range []*instance{off, h} {
		code, response := host.request(t, "POST", "/v1/responses", keyA, body)
		if code != 200 || !bytes.Contains(response, []byte("response.completed")) || !bytes.Contains(response, []byte("FORBIDDEN_RESPONSES_SSE_REPLY")) {
			t.Fatal("Responses SSE 原调用没有完整响应")
		}
		select {
		case actual := <-received:
			if plainRequest == nil {
				plainRequest, plainResponse = actual, response
			} else if !bytes.Equal(plainRequest, actual) || !bytes.Equal(plainResponse, response) {
				t.Fatal("审计改变了 SSE 上游请求或公开事件流")
			}
		case <-time.After(3 * time.Second):
			t.Fatal("SSE 上游没有收到本轮请求")
		}
	}
	r := waitChatDetail(t, h, "RESPONSES-SSE-INPUT")
	if r.Text != "RESPONSES-SSE-INPUT" || r.Protocol != "responses" || r.Model != "responses-client-model" {
		t.Fatal("SSE 新输入或请求模型被回复替代")
	}
	assertChatPrivacy(t, h, "FORBIDDEN_RESPONSES_")
}
