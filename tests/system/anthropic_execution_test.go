// 本文件用本地 Anthropic 上游验收原协议、官方 Before 观察、UTF-8 前缀和不等待模型回复的边界。
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

// 本地上游通常立即应答；三秒只用于检测夹具失联/死锁，不是模型执行的生产超时或固定等待。
const anthropicUpstreamWait = 3 * time.Second

// startAnthropicHost 通过宿主公开配置启动合成原协议上游，plugin 控制审计加载，upstream 只指向本地。
// 返回隔离实例；配置或启动失败终止测试；不继承真实凭据，不添加生产调试接口。
func startAnthropicHost(t *testing.T, plugin bool, upstream string) *instance {
	t.Helper()
	config := fmt.Sprintf(`claude-api-key:
  - api-key: synthetic-upstream-only
    base-url: %q
    cloak:
      mode: never
    models:
      - name: claude-synthetic-upstream
        alias: anthropic-client-model
`, upstream)
	return startHostWithConfiguration(t, 1<<30, plugin, "", false, config)
}

// TestAnthropicMessagesUTF8AndFailOpen 验证原生上游完整收到消息，审计开关不改请求/响应，记录使用 DD 改写后的当前模型和 UTF-8 安全前缀。
func TestAnthropicMessagesUTF8AndFailOpen(t *testing.T) {
	received := make(chan []byte, 16)
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
		_, _ = io.WriteString(w, `{"id":"synthetic-message-reply","type":"message","role":"assistant","model":"different-reply-model","content":[{"type":"text","text":"FORBIDDEN_ANTHROPIC_UPSTREAM_REPLY"}],"stop_reason":"end_turn","usage":{"input_tokens":1,"output_tokens":1}}`)
	}))
	defer upstream.Close()
	off := startAnthropicHost(t, false, upstream.URL)
	h := startAnthropicHost(t, true, upstream.URL)
	h.monitorAll(t)
	// 请求模型是宿主已有 DD 改写形式：反转为 anthropic-client-model，再映射上游别名；期望写死，不调用改写逻辑。
	const clientModel = "claude-fable-5-dd-ledom-tneilc-ciporhtna"
	// 64KiB = 65536；汉字三字节、emoji 四字节、[图片]八字节。组合期望独立于提取器计算。
	cases := []struct {
		name                  string
		asciiBytes            int
		tail                  []any
		wantSuffix            string
		truncated, incomplete bool
	}{
		{"exact_text", 65536, nil, "", false, false},
		{"exact_image", 65528, []any{map[string]any{"type": "image", "source": map[string]string{"type": "url", "url": "FORBIDDEN_ANTHROPIC_LIMIT_IMAGE"}}}, "[图片]", false, false},
		{"split_chinese", 65535, []any{map[string]string{"type": "text", "text": "中FORBIDDEN_ANTHROPIC_CHINESE_TAIL"}, map[string]string{"type": "text", "text": "z"}}, "", true, false},
		{"split_emoji", 65535, []any{map[string]string{"type": "text", "text": "🙂FORBIDDEN_ANTHROPIC_EMOJI_TAIL"}}, "", true, false},
		{"split_placeholder", 65530, []any{map[string]any{"type": "image", "source": map[string]string{"type": "url", "url": "FORBIDDEN_ANTHROPIC_SPLIT_IMAGE"}}}, "[图", true, false},
		{"combined", 65535, []any{map[string]string{"type": "text", "text": "🙂FORBIDDEN_ANTHROPIC_COMBINED_TAIL"}, map[string]string{"type": "future", "payload": "FORBIDDEN_ANTHROPIC_LIMIT_UNKNOWN"}, map[string]string{"type": "text", "text": "z"}}, "", true, true},
	}
	var combinedID string
	for i, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			marker := fmt.Sprintf("ANTHROPIC-UTF8-%d-", i)
			ascii := marker + strings.Repeat("a", tc.asciiBytes-len(marker))
			content := append([]any{map[string]string{"type": "text", "text": ascii}}, tc.tail...)
			body, err := json.Marshal(map[string]any{"model": clientModel, "max_tokens": 32, "system": "FORBIDDEN_ANTHROPIC_LIMIT_SYSTEM", "messages": []any{map[string]string{"role": "user", "content": "FORBIDDEN_ANTHROPIC_LIMIT_HISTORY"}, map[string]any{"role": "user", "content": content}}})
			if err != nil {
				t.Fatal("Anthropic 容量夹具编码失败")
			}
			var plainRequest, plainResponse []byte
			for _, host := range []*instance{off, h} {
				code, response := host.request(t, "POST", "/v1/messages", keyA, body)
				if code != 200 {
					t.Fatal("Anthropic 原协议可控调用被改变或拒绝")
				}
				select {
				case actual := <-received:
					if plainRequest == nil {
						plainRequest, plainResponse = actual, response
					} else if !bytes.Equal(plainRequest, actual) || !bytes.Equal(plainResponse, response) {
						t.Fatal("Anthropic 审计改写实际上游字节或公开响应")
					}
					var forwarded struct {
						Model    string          `json:"model"`
						System   json.RawMessage `json:"system"`
						Messages []struct {
							Role    string          `json:"role"`
							Content json.RawMessage `json:"content"`
						} `json:"messages"`
					}
					if json.Unmarshal(actual, &forwarded) != nil || forwarded.Model != "claude-synthetic-upstream" || !bytes.Contains(forwarded.System, []byte("FORBIDDEN_ANTHROPIC_LIMIT_SYSTEM")) || len(forwarded.Messages) != 2 || !bytes.Contains(forwarded.Messages[0].Content, []byte("FORBIDDEN_ANTHROPIC_LIMIT_HISTORY")) {
						t.Fatal("原协议上游没有真正收到独立模型、指令和历史")
					}
					var blocks []map[string]any
					if json.Unmarshal(forwarded.Messages[1].Content, &blocks) != nil {
						t.Fatal("上游没有收到原协议内容数组")
					}
					// 原宿主会增加缓存标记；审计开关的完整字节已比较，这里仅忽略原有 cache_control 再核对内容不截断。
					for _, block := range blocks {
						delete(block, "cache_control")
					}
					wantRaw, _ := json.Marshal(content)
					actualRaw, _ := json.Marshal(blocks)
					if !bytes.Equal(wantRaw, actualRaw) {
						t.Fatal("审计截断或改变了上游内容块")
					}
				case <-time.After(anthropicUpstreamWait):
					t.Fatal("本轮 Anthropic 上游提交缺失")
				}
			}
			r := waitChatDetail(t, h, marker)
			if r.Text != ascii+tc.wantSuffix || r.Protocol != "anthropic-messages" || r.Model != "anthropic-client-model" || r.Truncated != tc.truncated || r.ExtractionIncomplete != tc.incomplete {
				t.Fatal("Anthropic 原协议、请求模型或 UTF-8 状态与独立预期不符")
			}
			if tc.incomplete {
				combinedID = r.ID
			}
		})
	}
	for _, body := range []string{
		`{"model":"anthropic-client-model","max_tokens":32,"messages":[{"role":"user","content":[{"type":"future","payload":"FORBIDDEN_ANTHROPIC_ALL_UNKNOWN"}]}]}`,
		`{"model":"anthropic-client-model","messages":[`,
	} {
		plainCode, plainResponse := off.request(t, "POST", "/v1/messages", keyA, []byte(body))
		code, response := h.request(t, "POST", "/v1/messages", keyA, []byte(body))
		if code != plainCode || !bytes.Equal(plainResponse, response) {
			t.Fatal("Anthropic 未知/畸形请求被审计改变响应")
		}
		if code == 200 {
			var plain []byte
			for i := 0; i < 2; i++ {
				select {
				case actual := <-received:
					if i == 0 {
						plain = actual
					} else if !bytes.Equal(plain, actual) {
						t.Fatal("Anthropic 解析缺口改变上游请求")
					}
				case <-time.After(anthropicUpstreamWait):
					t.Fatal("本轮 Anthropic 失败边界上游观察缺失")
				}
			}
		}
	}
	h.request(t, "POST", "/v1/messages", keyA, []byte(`{"model":"anthropic-client-model","messages":[{"role":"user","content":"ANTHROPIC-UTF8-DRAIN"}]}`))
	waitSnapshotRecord(t, h, "ANTHROPIC-UTF8-DRAIN")
	assertChatPrivacy(t, h, "FORBIDDEN_ANTHROPIC_")
	h.stop()
	restarted := startHost(t, 1<<30, true, "", filepath.Join(h.dir, "audit"))
	code, raw := restarted.request(t, "GET", api+"/detail?id="+combinedID, adminKey, nil)
	var r savedRecord
	if code != 200 || json.Unmarshal(raw, &r) != nil || r.Protocol != "anthropic-messages" || !r.Truncated || !r.ExtractionIncomplete || r.Model != "anthropic-client-model" {
		t.Fatal("正常重启丢失 Anthropic 协议或双状态")
	}
}

// TestAnthropicMessagesSSE 验证流式提交沿用官方 Before 观察，审计不改变上游请求或 SSE 事件流。
func TestAnthropicMessagesSSE(t *testing.T) {
	received := make(chan []byte, 2)
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
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"id\":\"synthetic-stream\",\"type\":\"message\",\"role\":\"assistant\",\"model\":\"different-reply-model\",\"content\":[],\"usage\":{\"input_tokens\":1,\"output_tokens\":0}}}\n\nevent: content_block_start\ndata: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"text\",\"text\":\"\"}}\n\nevent: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"FORBIDDEN_ANTHROPIC_SSE_REPLY\"}}\n\nevent: content_block_stop\ndata: {\"type\":\"content_block_stop\",\"index\":0}\n\nevent: message_delta\ndata: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"end_turn\"},\"usage\":{\"output_tokens\":1}}\n\nevent: message_stop\ndata: {\"type\":\"message_stop\"}\n\n")
	}))
	defer upstream.Close()
	off := startAnthropicHost(t, false, upstream.URL)
	h := startAnthropicHost(t, true, upstream.URL)
	h.monitorAll(t)
	body := []byte(`{"model":"anthropic-client-model","stream":true,"max_tokens":32,"messages":[{"role":"user","content":"ANTHROPIC-SSE-INPUT"}]}`)
	var plainRequest, plainResponse []byte
	for _, host := range []*instance{off, h} {
		code, response := host.request(t, "POST", "/v1/messages", keyA, body)
		if code != 200 || !bytes.Contains(response, []byte("message_stop")) || !bytes.Contains(response, []byte("FORBIDDEN_ANTHROPIC_SSE_REPLY")) {
			t.Fatal("Anthropic SSE 原调用没有完整响应")
		}
		select {
		case actual := <-received:
			if plainRequest == nil {
				plainRequest, plainResponse = actual, response
			} else if !bytes.Equal(plainRequest, actual) || !bytes.Equal(plainResponse, response) {
				t.Fatal("审计改变 Anthropic SSE 上游请求或事件流")
			}
		case <-time.After(anthropicUpstreamWait):
			t.Fatal("本轮 Anthropic SSE 上游请求缺失")
		}
	}
	r := waitChatDetail(t, h, "ANTHROPIC-SSE-INPUT")
	if r.Text != "ANTHROPIC-SSE-INPUT" || r.Protocol != "anthropic-messages" || r.Model != "anthropic-client-model" {
		t.Fatal("Anthropic SSE 新输入或客户端模型被回复替代")
	}
	assertChatPrivacy(t, h, "FORBIDDEN_ANTHROPIC_")
}

// TestAnthropicMessagesBeforeFailedReply 验证上游未回复时新输入已保存，之后上游失败不撤销输入。
func TestAnthropicMessagesBeforeFailedReply(t *testing.T) {
	received := make(chan struct{}, 1)
	release := make(chan struct{})
	var once sync.Once
	unblock := func() { once.Do(func() { close(release) }) }
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if respondDiscovery(w, r) {
			return
		}
		if _, err := io.Copy(io.Discard, r.Body); err != nil {
			http.Error(w, "synthetic read failure", 400)
			return
		}
		received <- struct{}{}
		<-release
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(400)
		_, _ = io.WriteString(w, `{"type":"error","error":{"type":"invalid_request_error","message":"synthetic upstream rejection"}}`)
	}))
	t.Cleanup(func() { unblock(); upstream.Close() })
	h := startAnthropicHost(t, true, upstream.URL)
	h.monitorAll(t)
	req, err := http.NewRequest("POST", h.base+"/v1/messages", strings.NewReader(`{"model":"anthropic-client-model","max_tokens":32,"messages":[{"role":"user","content":"ANTHROPIC-BEFORE-FAILED-REPLY"}]}`))
	if err != nil {
		t.Fatal("合成请求构造失败")
	}
	req.Header.Set("Authorization", "Bearer "+keyA)
	req.Header.Set("Content-Type", "application/json")
	outcome := make(chan int, 1)
	go func() {
		response, err := h.client.Do(req)
		if err != nil {
			outcome <- 0
			return
		}
		_, _ = io.Copy(io.Discard, response.Body)
		_ = response.Body.Close()
		outcome <- response.StatusCode
	}()
	select {
	case <-received:
	case <-time.After(anthropicUpstreamWait):
		t.Fatal("本轮上游阻塞屏障未建立")
	}
	r := waitChatDetail(t, h, "ANTHROPIC-BEFORE-FAILED-REPLY")
	if r.Text != "ANTHROPIC-BEFORE-FAILED-REPLY" || r.Protocol != "anthropic-messages" {
		t.Fatal("Anthropic 审计错误地等待模型回复")
	}
	unblock()
	select {
	case code := <-outcome:
		if code != 400 {
			t.Fatal("审计改变了 Anthropic 上游失败")
		}
	case <-time.After(anthropicUpstreamWait):
		t.Fatal("Anthropic 模型请求未完成")
	}
	code, _ := h.request(t, "GET", api+"/detail?id="+r.ID, adminKey, nil)
	if code != 200 {
		t.Fatal("Anthropic 失败撤销已保存提交")
	}
	assertChatPrivacy(t, h)
}

// TestAnthropicClientInputUpstreamUnchanged 验证辅助请求和附加块仅影响审计副本，上游/响应原字节不变，同文重交独立保存。
func TestAnthropicClientInputUpstreamUnchanged(t *testing.T) {
	received := make(chan []byte, 8)
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
		_, _ = io.WriteString(w, `{"id":"synthetic-client-filter-reply","type":"message","role":"assistant","model":"claude-synthetic-upstream","content":[{"type":"text","text":"FORBIDDEN_CLIENT_NATIVE_REPLY"}],"stop_reason":"end_turn","usage":{"input_tokens":1,"output_tokens":1}}`)
	}))
	defer upstream.Close()
	off := startAnthropicHost(t, false, upstream.URL)
	h := startAnthropicHost(t, true, upstream.URL)
	h.monitorAll(t)
	_, _, before := snapshotCounters(t, h)
	cases := []struct {
		name      string
		content   any
		forwarded []string
	}{
		{"auxiliary", "[SUGGESTION MODE: Suggest what the user might naturally type next into Claude Code.]\n\nFIRST: Look at the user's recent messages and original request.\n\nYour job is to predict what THEY would type - not what you think they should do.\n\nFORBIDDEN_CLIENT_NATIVE_SUGGESTION\nFormat: 2-12 words, match the user's style. Or nothing.\n\nReply with ONLY the suggestion, no quotes or explanation.", []string{"FORBIDDEN_CLIENT_NATIVE_SUGGESTION"}},
		{"real", clientTextBlocks("<system-reminder>\nFORBIDDEN_CLIENT_NATIVE_REMINDER\n</system-reminder>", clientSkillTemplate, "CLIENT-NATIVE-INPUT 正文"), []string{"FORBIDDEN_CLIENT_NATIVE_REMINDER", "FORBIDDEN_CLIENT_INPUT_SKILL", "CLIENT-NATIVE-INPUT"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			body, err := json.Marshal(map[string]any{
				"model": "anthropic-client-model", "max_tokens": 32, "system": "FORBIDDEN_CLIENT_NATIVE_SYSTEM",
				"messages": []any{
					map[string]string{"role": "user", "content": "FORBIDDEN_CLIENT_NATIVE_HISTORY"},
					map[string]any{"role": "user", "content": tc.content},
				},
			})
			if err != nil {
				t.Fatal("原协议客户端过滤夹具编码失败")
			}
			var plainRequest, plainResponse []byte
			// 两次相同正文使用不同官方执行 ID；不开审计的请求作为逐字节对照。
			for _, host := range []*instance{off, h, h} {
				code, response := host.request(t, "POST", "/v1/messages", keyA, body)
				if code != 200 {
					t.Fatal("原协议客户端过滤改变请求状态")
				}
				select {
				case actual := <-received:
					if plainRequest == nil {
						plainRequest, plainResponse = actual, response
					} else if !bytes.Equal(plainRequest, actual) || !bytes.Equal(plainResponse, response) {
						t.Fatal("客户端过滤改变上游请求或公开响应字节")
					}
					for _, marker := range append([]string{"FORBIDDEN_CLIENT_NATIVE_SYSTEM", "FORBIDDEN_CLIENT_NATIVE_HISTORY"}, tc.forwarded...) {
						if !bytes.Contains(actual, []byte(marker)) {
							t.Fatal("原上游未收到独立指令、历史或审计应排除的客户端内容")
						}
					}
				case <-time.After(anthropicUpstreamWait):
					t.Fatal("本轮客户端过滤上游观察缺失")
				}
			}
		})
	}
	barrier := []byte(`{"model":"anthropic-client-model","messages":[{"role":"user","content":"CLIENT-NATIVE-BARRIER"}]}`)
	code, _ := h.request(t, "POST", "/v1/messages", keyA, barrier)
	if code != 200 {
		t.Fatal("原协议客户端过滤屏障请求失败")
	}
	waitSnapshotRecord(t, h, "CLIENT-NATIVE-BARRIER")
	saved, queued, after := snapshotCounters(t, h)
	if saved != 3 || queued != 0 || !maps.Equal(before, after) {
		t.Fatalf("辅助请求未跳过或同文重交被合并: saved=%d queued=%d gaps_changed=%t", saved, queued, !maps.Equal(before, after))
	}
	code, raw := h.search(t, adminKey, map[string]any{"q": "CLIENT-NATIVE-INPUT", "limit": 100})
	var result struct {
		Total   int           `json:"total"`
		Records []savedRecord `json:"records"`
	}
	if code != 200 || json.Unmarshal(raw, &result) != nil || result.Total != 2 || len(result.Records) != 2 || result.Records[0].ID == result.Records[1].ID {
		t.Fatal("相同真实正文没有两条独立执行记录")
	}
	for _, item := range result.Records {
		code, raw = h.request(t, "GET", api+"/detail?id="+item.ID, adminKey, nil)
		var record savedRecord
		if code != 200 || json.Unmarshal(raw, &record) != nil || record.Text != "CLIENT-NATIVE-INPUT 正文" || record.Model != "anthropic-client-model" || record.Protocol != "anthropic-messages" || record.Truncated || record.ExtractionIncomplete {
			t.Fatal("原协议客户端过滤的独立详情或状态不正确")
		}
	}
	assertChatPrivacy(t, h, "FORBIDDEN_CLIENT_NATIVE_", "FORBIDDEN_CLIENT_INPUT_")
}
