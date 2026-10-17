// 本文件验证明确 main 仅放过整请求模板，附加块、协议边界和 UTF-8 截断仍只影响审计副本，不改原生 Anthropic 上游。
package system

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"maps"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

// TestHTTPRequestPurposeAnthropicMain 验证 main 保留完整辅助模板而 unknown 沿用旧规则，正文清理、工具排除、不完整状态及字节上限不变。
func TestHTTPRequestPurposeAnthropicMain(t *testing.T) {
	received := make(chan []byte, 4)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if respondDiscovery(w, r) {
			return
		}
		raw, err := io.ReadAll(r.Body)
		if err != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		received <- raw
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"id":"synthetic-purpose-anthropic","type":"message","role":"assistant","model":"claude-synthetic-upstream","content":[{"type":"text","text":"FORBIDDEN_PURPOSE_ANTHROPIC_REPLY"}],"stop_reason":"end_turn","usage":{"input_tokens":1,"output_tokens":1}}`)
	}))
	t.Cleanup(upstream.Close)
	off := startAnthropicHost(t, false, upstream.URL)
	h := startAnthropicHost(t, true, upstream.URL)
	h.monitorAll(t)
	// receive 返回本轮实际原协议请求；超时终止对应测试，不输出上游正文。
	receive := func(t *testing.T) []byte {
		t.Helper()
		select {
		case raw := <-received:
			return raw
		case <-time.After(anthropicUpstreamWait):
			t.Fatal("用途原生 Anthropic 上游请求缺失")
			return nil
		}
	}
	type sample struct {
		name, marker, want, purpose string
		content                     any
		assistantTail               bool
		incomplete, truncated       bool
	}
	var cases []sample
	for _, template := range []struct{ name, text, replace string }{
		{"action", clientActionTemplate, "FORBIDDEN_CLIENT_INPUT_ACTION"},
		{"title", clientTitleTemplate, "FORBIDDEN_CLIENT_INPUT_SESSION"},
		{"transcript", strings.ReplaceAll(clientTranscriptTemplate, "FORBIDDEN_CLIENT_INPUT_TOOL", "synthetic-template-agent"), "FORBIDDEN_CLIENT_INPUT_TRANSCRIPT"},
		{"suggestion", "[SUGGESTION MODE: Suggest what the user might naturally type next into Claude Code.]\n\nFIRST: Look at the user's recent messages and original request.\n\nYour job is to predict what THEY would type - not what you think they should do.\n\nSYNTHETIC_TEMPLATE_MARKER\nFormat: 2-12 words, match the user's style. Or nothing.\n\nReply with ONLY the suggestion, no quotes or explanation.", "SYNTHETIC_TEMPLATE_MARKER"},
	} {
		for _, purpose := range []string{"main", "unknown"} {
			marker := fmt.Sprintf("PURPOSE-ANTHROPIC-%s-%s-%d", template.name, purpose, time.Now().UnixNano())
			if purpose == "unknown" {
				marker = "FORBIDDEN_PURPOSE_ANTHROPIC_SKIPPED-" + marker
			}
			content := strings.ReplaceAll(template.text, template.replace, marker)
			want := content
			if purpose == "unknown" {
				want = ""
			}
			cases = append(cases, sample{name: template.name + "_" + purpose, marker: marker, content: content, want: want, purpose: purpose})
		}
	}
	marker := fmt.Sprintf("PURPOSE-ANTHROPIC-CLEAN-%d", time.Now().UnixNano())
	tool := map[string]any{"type": "tool_result", "tool_use_id": "synthetic-purpose-tool", "content": "FORBIDDEN_PURPOSE_ANTHROPIC_TOOL"}
	content := append([]any{tool}, clientTextBlocks(clientSkillTemplate, "<system-reminder>\nFORBIDDEN_PURPOSE_ANTHROPIC_REMINDER\n</system-reminder>", "<ide_opened_file>\nThe user opened /synthetic/FORBIDDEN_PURPOSE_ANTHROPIC_IDE.go in the IDE.\n</ide_opened_file>", marker)...)
	content = append(content,
		map[string]any{"type": "image", "source": map[string]string{"type": "url", "url": "FORBIDDEN_PURPOSE_ANTHROPIC_IMAGE"}},
		map[string]any{"type": "document", "source": map[string]string{"type": "file", "file_id": "FORBIDDEN_PURPOSE_ANTHROPIC_FILE"}},
		map[string]string{"type": "future", "payload": "FORBIDDEN_PURPOSE_ANTHROPIC_UNKNOWN"})
	cases = append(cases,
		sample{name: "main_content_boundaries", marker: marker, content: content, want: marker + "[图片][文件]", purpose: "main", incomplete: true},
		sample{name: "main_tool_only", marker: "FORBIDDEN_PURPOSE_ANTHROPIC_TOOL", content: []any{tool}, purpose: "main"},
		sample{name: "main_assistant_tail", marker: "FORBIDDEN_PURPOSE_ANTHROPIC_ASSISTANT", content: "FORBIDDEN_PURPOSE_ANTHROPIC_ASSISTANT", purpose: "main", assistantTail: true})
	marker = fmt.Sprintf("PURPOSE-ANTHROPIC-LIMIT-%d-", time.Now().UnixNano())
	prefix := marker + strings.Repeat("x", 65535-len(marker))
	cases = append(cases, sample{name: "main_utf8_limit", marker: marker, content: clientTextBlocks("<system-reminder>\nFORBIDDEN_PURPOSE_ANTHROPIC_LARGE"+strings.Repeat("x", 65536)+"\n</system-reminder>", prefix+"🙂FORBIDDEN_PURPOSE_ANTHROPIC_TAIL"), want: prefix, purpose: "main", truncated: true})
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			role := "user"
			if tc.assistantTail {
				role = "assistant"
			}
			body, err := json.Marshal(map[string]any{
				"model": "anthropic-client-model", "max_tokens": 32, "system": "FORBIDDEN_PURPOSE_ANTHROPIC_SYSTEM",
				"messages": []any{map[string]string{"role": "user", "content": "FORBIDDEN_PURPOSE_ANTHROPIC_HISTORY"}, map[string]any{"role": role, "content": tc.content}},
			})
			if err != nil {
				t.Fatal("用途原生 Anthropic 夹具编码失败")
			}
			var headers http.Header
			if tc.purpose == "main" {
				headers = http.Header{"x-claude-code-request-class": {"main"}}
			}
			beforeSaved, _, gaps := snapshotCounters(t, h)
			plainCode, plainResponse := requestWithPurposeHeaders(t, off, "/v1/messages", keyA, body, headers)
			plain := receive(t)
			code, response := requestWithPurposeHeaders(t, h, "/v1/messages", keyA, body, headers)
			actual := receive(t)
			if code != http.StatusOK || code != plainCode || !bytes.Equal(response, plainResponse) || !bytes.Equal(actual, plain) || !bytes.Contains(actual, []byte(tc.marker)) || !bytes.Contains(actual, []byte("FORBIDDEN_PURPOSE_ANTHROPIC_HISTORY")) {
				t.Fatal("用途分类或正文清理改变原生 Anthropic 上游请求/响应")
			}
			barrier := fmt.Sprintf("PURPOSE-ANTHROPIC-BARRIER-%d", time.Now().UnixNano())
			barrierBody, err := json.Marshal(map[string]any{"model": "anthropic-client-model", "max_tokens": 32, "messages": []map[string]string{{"role": "user", "content": barrier}}})
			if err != nil {
				t.Fatal("用途原生 Anthropic 屏障编码失败")
			}
			if code, _ := h.request(t, http.MethodPost, "/v1/messages", keyA, barrierBody); code != http.StatusOK {
				t.Fatal("用途原生 Anthropic FIFO 屏障失败")
			}
			receive(t)
			waitSnapshotRecord(t, h, barrier)
			wantSaved := beforeSaved + 1
			if tc.want == "" {
				if searchRecords(t, h, url.Values{"q": {tc.marker}}).Total != 0 {
					t.Fatal("用途未知模板或 main 的非用户内容被保存")
				}
			} else {
				wantSaved++
				record := assertSavedPurpose(t, h, tc.marker, tc.want, tc.purpose, "anthropic-messages")
				if record.Truncated != tc.truncated || record.ExtractionIncomplete != tc.incomplete {
					t.Fatal("main 改变既有字节上限或不完整状态")
				}
			}
			wantGaps := maps.Clone(gaps)
			if tc.incomplete {
				wantGaps["unsupported_content"]++
			}
			saved, queued, after := snapshotCounters(t, h)
			if saved != wantSaved || queued != 0 || !maps.Equal(after, wantGaps) {
				t.Fatalf("main 模板或协议边界状态不符: saved=%d want=%d queued=%d gaps_changed=%t", saved, wantSaved, queued, !maps.Equal(after, wantGaps))
			}
		})
	}
	assertChatPrivacy(t, h, "FORBIDDEN_PURPOSE_ANTHROPIC_", "FORBIDDEN_CLIENT_INPUT_SKILL")
}
