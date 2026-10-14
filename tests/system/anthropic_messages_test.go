// 本文件通过真实 Anthropic HTTP、管理查询与持久化输出验收最新用户输入；不测试私有提取器。
package system

import (
	"encoding/json"
	"fmt"
	"maps"
	"slices"
	"testing"
)

// TestAnthropicMessagesBeforeRouting 验证原始消息在模型路由失败前采集，列表和详情保留协议及客户端请求模型。
func TestAnthropicMessagesBeforeRouting(t *testing.T) {
	h := startHost(t, 1<<30, true, "")
	ids := h.monitorAll(t)
	body := []byte(`{"model":"anthropic-missing-client-model","max_tokens":32,"system":"FORBIDDEN_ANTHROPIC_SYSTEM","messages":[{"role":"user","content":"ANTHROPIC-BEFORE-ROUTING"}]}`)
	code, _ := h.request(t, "POST", "/v1/messages", keyA, body)
	if code != 400 {
		t.Fatal("审计不应改变 Anthropic 原路由失败")
	}
	r := waitChatDetail(t, h, "ANTHROPIC-BEFORE-ROUTING")
	if r.Text != "ANTHROPIC-BEFORE-ROUTING" || r.Model != "anthropic-missing-client-model" || r.Protocol != "anthropic-messages" || r.Truncated || r.ExtractionIncomplete || (r.ObjectID != ids[0] && r.ObjectID != ids[1]) {
		t.Fatal("Anthropic 原始输入、可信客户端对象或请求元数据不正确")
	}
	code, raw := h.request(t, "GET", api+"/records", adminKey, nil)
	var list struct {
		Records []savedRecord `json:"records"`
	}
	if code != 200 || json.Unmarshal(raw, &list) != nil || len(list.Records) != 1 || list.Records[0].Protocol != r.Protocol || list.Records[0].Model != r.Model {
		t.Fatal("Anthropic 管理列表协议或请求模型丢失")
	}
	assertChatPrivacy(t, h, "FORBIDDEN_ANTHROPIC_")
}

// TestAnthropicMessagesAdmission 验证未监控、失败鉴权和 count_tokens 不采集，状态准确声明三协议 HTTP 支持。
func TestAnthropicMessagesAdmission(t *testing.T) {
	h := startHost(t, 1<<30, true, "")
	h.request(t, "POST", "/v1/messages", keyA, []byte(`{"model":"missing","messages":[{"role":"user","content":"FORBIDDEN_ANTHROPIC_UNMONITORED"}]}`))
	h.monitorAll(t)
	_, _, before := snapshotCounters(t, h)
	code, _ := h.request(t, "POST", "/v1/messages", "synthetic-invalid-client", []byte(`{"model":"missing","messages":[{"role":"user","content":"FORBIDDEN_ANTHROPIC_UNAUTHENTICATED"}]}`))
	if code != 401 {
		t.Fatal("Anthropic 客户端鉴权被改变")
	}
	h.request(t, "POST", "/v1/messages/count_tokens", keyA, []byte(`{"model":"missing","messages":[{"role":"user","content":"FORBIDDEN_ANTHROPIC_COUNT_TOKENS"}]}`))
	h.request(t, "POST", "/v1/messages", keyA, []byte(`{"model":"missing","messages":[{"role":"user","content":"ANTHROPIC-ADMISSION-DRAIN"}]}`))
	waitSnapshotRecord(t, h, "ANTHROPIC-ADMISSION-DRAIN")
	saved, queued, after := snapshotCounters(t, h)
	if saved != 1 || queued != 0 || !maps.Equal(before, after) {
		t.Fatal("Anthropic 未监控/认证失败/非模型提交错误采集或计缺口")
	}
	code, raw := h.request(t, "GET", api+"/status", adminKey, nil)
	var status struct {
		Available          bool     `json:"available"`
		SupportedProtocols []string `json:"supported_protocols"`
		ResponsesWebSocket bool     `json:"responses_websocket"`
	}
	if code != 200 || json.Unmarshal(raw, &status) != nil || !status.Available || !slices.Contains(status.SupportedProtocols, "anthropic-messages") || !status.ResponsesWebSocket || !slices.Contains(status.SupportedProtocols, "responses-websocket") {
		t.Fatal("管理状态没有准确声明 Anthropic Messages HTTP 支持")
	}
	for _, key := range []string{"", keyA} {
		for _, path := range []string{"/records", "/detail?id=synthetic", "/status", "/objects"} {
			code, _ := h.request(t, "GET", api+path, key, nil)
			if code != 401 && code != 403 {
				t.Fatal("非管理用户能访问 Anthropic 审计数据")
			}
		}
	}
	assertChatPrivacy(t, h, "FORBIDDEN_ANTHROPIC_")
}

// TestAnthropicMessagesContent 验证附件来源、未知/畸形块、字段歧义和空输入，不兜底保存原请求。
func TestAnthropicMessagesContent(t *testing.T) {
	h := startHost(t, 1<<30, true, "")
	h.monitorAll(t)
	cases := []struct {
		name, content, want string
		gap                 bool
	}{
		{"image_base64", `[{"type":"image","source":{"type":"base64","media_type":"image/png","data":"FORBIDDEN_ANTHROPIC_IMAGE_DATA"}}]`, "[图片]", false},
		{"image_file", `[{"type":"image","source":{"type":"file","file_id":"FORBIDDEN_ANTHROPIC_IMAGE_FILE"}}]`, "[图片]", false},
		{"pdf_base64", `[{"type":"document","source":{"type":"base64","media_type":"application/pdf","data":"FORBIDDEN_ANTHROPIC_PDF"},"title":"FORBIDDEN_ANTHROPIC_TITLE","context":"FORBIDDEN_ANTHROPIC_CONTEXT"}]`, "[文件]", false},
		{"document_url", `[{"type":"document","source":{"type":"url","url":"FORBIDDEN_ANTHROPIC_DOCUMENT_URL"}}]`, "[文件]", false},
		{"document_file", `[{"type":"document","source":{"type":"file","file_id":"FORBIDDEN_ANTHROPIC_DOCUMENT_FILE"}}]`, "[文件]", false},
		{"document_content", `[{"type":"document","source":{"type":"content","content":[{"type":"text","text":"FORBIDDEN_ANTHROPIC_DOCUMENT_CONTENT"},{"type":"image","source":{"type":"url","url":"FORBIDDEN_ANTHROPIC_DOCUMENT_IMAGE"}}]}}]`, "[文件]", false},
		{"document_string", `[{"type":"document","source":{"type":"content","content":"FORBIDDEN_ANTHROPIC_DOCUMENT_STRING"}}]`, "[文件]", false},
		{"literal", `"ANTHROPIC-LITERAL-https://example.invalid/a.png data:image/png;base64,U1lOVEhFVElD synthetic-text-secret"`, "ANTHROPIC-LITERAL-https://example.invalid/a.png data:image/png;base64,U1lOVEhFVElD synthetic-text-secret", false},
		{"partial", `[{"type":"text","text":"ANTHROPIC-PARTIAL-前"},{"type":"future","payload":"FORBIDDEN_ANTHROPIC_UNKNOWN"},{"type":"text","text":"后"}]`, "ANTHROPIC-PARTIAL-前后", true},
		{"unknown_only", `[{"type":"future","text":"FORBIDDEN_ANTHROPIC_UNKNOWN_TEXT"}]`, "", true},
		{"wrong_protocol", `[{"type":"image_url","image_url":{"url":"FORBIDDEN_ANTHROPIC_CHAT_IMAGE"}},{"type":"input_text","text":"FORBIDDEN_ANTHROPIC_RESPONSES_TEXT"}]`, "", true},
		{"duplicate_type", `[{"type":"tool_result","type":"text","text":"FORBIDDEN_ANTHROPIC_DUPLICATE_TYPE"},{"type":"text","text":"ANTHROPIC-DUPLICATE-SAFE"}]`, "ANTHROPIC-DUPLICATE-SAFE", true},
		{"text_alias", `[{"type":"text","Text":"FORBIDDEN_ANTHROPIC_TEXT_ALIAS"}]`, "", true},
		{"null_text", `[{"type":"text","text":null}]`, "", true},
		{"image_missing_source", `[{"type":"image","data":"FORBIDDEN_ANTHROPIC_BAD_IMAGE"}]`, "", true},
		{"unknown_source", `[{"type":"document","source":{"type":"future","data":"FORBIDDEN_ANTHROPIC_UNKNOWN_SOURCE"}}]`, "", true},
		{"duplicate_source", `[{"type":"image","source":{"type":"url","type":"file","file_id":"FORBIDDEN_ANTHROPIC_DUPLICATE_SOURCE"}}]`, "", true},
		{"excluded_blocks", `[{"type":"tool_use","input":{"text":"FORBIDDEN_ANTHROPIC_TOOL_INPUT"}},{"type":"server_tool_use","input":"FORBIDDEN_ANTHROPIC_SERVER_TOOL"},{"type":"thinking","thinking":"FORBIDDEN_ANTHROPIC_THINKING"},{"type":"redacted_thinking","data":"FORBIDDEN_ANTHROPIC_REDACTED"}]`, "", false},
		{"wrong_content", `{"text":"FORBIDDEN_ANTHROPIC_CONTENT_OBJECT"}`, "", true},
		{"null_block", `[null]`, "", true},
		{"empty_string", `""`, "", false},
		{"empty_array", `[]`, "", false},
		{"empty_null", `null`, "", false},
		{"empty_text", `[{"type":"text","text":""}]`, "", false},
		{"whitespace", `" \n\t "`, " \n\t ", false},
	}
	seenIDs := make(map[string]bool)
	for i, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			savedBefore, _, before := snapshotCounters(t, h)
			h.request(t, "POST", "/v1/messages", keyA, []byte(`{"model":"missing","messages":[{"role":"user","content":`+tc.content+`}]}`))
			// 唯一本轮记录落盘提供 FIFO 屏障，不以固定 sleep 证明空输入/工具续跑跳过。
			barrier := fmt.Sprintf("ANTHROPIC-CONTENT-BARRIER-%d", i)
			h.request(t, "POST", "/v1/messages", keyA, []byte(fmt.Sprintf(`{"model":"missing","messages":[{"role":"user","content":%q}]}`, barrier)))
			waitSnapshotRecord(t, h, barrier)
			saved, queued, after := snapshotCounters(t, h)
			wantSaved := savedBefore + 1
			if tc.want != "" {
				wantSaved++
				r := waitChatDetail(t, h, tc.want)
				if r.Text != tc.want || r.Protocol != "anthropic-messages" || r.ExtractionIncomplete != tc.gap || r.Truncated || seenIDs[r.ID] {
					t.Fatal("Anthropic 允许内容或状态与本轮独立预期不符")
				}
				seenIDs[r.ID] = true
			}
			wantGaps := maps.Clone(before)
			if tc.gap {
				wantGaps["unsupported_content"]++
			}
			if saved != wantSaved || queued != 0 || !maps.Equal(after, wantGaps) {
				t.Fatal("Anthropic 未知块混淆为空输入，或同次提交重复记录/计缺口")
			}
		})
	}
	assertChatPrivacy(t, h, "FORBIDDEN_ANTHROPIC_")
}

// TestAnthropicMessagesAmbiguity 验证模型未知仍采集，歧义角色/内容及畸形 JSON 不会回退为完整输入。
func TestAnthropicMessagesAmbiguity(t *testing.T) {
	h := startHost(t, 1<<30, true, "")
	h.monitorAll(t)
	for i, model := range []string{"", `"model":null,`, `"model":123,`} {
		marker := fmt.Sprintf("ANTHROPIC-UNKNOWN-MODEL-%d", i)
		h.request(t, "POST", "/v1/messages", keyA, []byte(fmt.Sprintf(`{%s"messages":[{"role":"user","content":%q}]}`, model, marker)))
		r := waitChatDetail(t, h, marker)
		if r.Model != "" || r.Protocol != "anthropic-messages" || r.Text != marker {
			t.Fatal("Anthropic 模型不可靠时丢弃或猜测了输入")
		}
	}
	_, _, before := snapshotCounters(t, h)
	for _, body := range []string{
		`{"model":"missing","messages":[{"role":"assistant","role":"user","content":"FORBIDDEN_ANTHROPIC_DUPLICATE_ROLE"}]}`,
		`{"model":"missing","messages":[{"role":"user","content":"FORBIDDEN_ANTHROPIC_DUPLICATE_CONTENT","content":"FORBIDDEN_ANTHROPIC_DUPLICATE_CONTENT_2"}]}`,
		`{"model":"missing","messages":[{"role":"user","content":"FORBIDDEN_ANTHROPIC_DUPLICATE_MESSAGES"}],"messages":[]}`,
		`{"model":"missing","messages":[`,
	} {
		h.request(t, "POST", "/v1/messages", keyA, []byte(body))
	}
	for _, terminal := range []string{`{"Role":"user","content":"FORBIDDEN_ANTHROPIC_ROLE_ALIAS"}`, `{"role":"future","content":"FORBIDDEN_ANTHROPIC_UNKNOWN_ROLE"}`, `{"role":"user"}`} {
		h.request(t, "POST", "/v1/messages", keyA, []byte(`{"model":"missing","messages":[{"role":"user","content":"FORBIDDEN_ANTHROPIC_AMBIGUOUS_HISTORY"},`+terminal+`]}`))
	}
	h.request(t, "POST", "/v1/messages", keyA, []byte(`{"model":"missing","messages":[{"role":"user","content":"ANTHROPIC-AMBIGUITY-DRAIN"}]}`))
	waitSnapshotRecord(t, h, "ANTHROPIC-AMBIGUITY-DRAIN")
	saved, queued, after := snapshotCounters(t, h)
	wantGaps := maps.Clone(before)
	wantGaps["invalid_json"] += 4
	wantGaps["unsupported_content"] += 3
	if saved != 4 || queued != 0 || !maps.Equal(after, wantGaps) {
		t.Fatal("Anthropic 字段歧义或缺失被保存/缺口分类错误")
	}
	assertChatPrivacy(t, h, "FORBIDDEN_ANTHROPIC_")
}

// TestAnthropicMessagesToolBoundary 验证纯工具续跑正常跳过，混合消息仅留新内容且不递归提取工具结果。
func TestAnthropicMessagesToolBoundary(t *testing.T) {
	h := startHost(t, 1<<30, true, "")
	h.monitorAll(t)
	_, _, before := snapshotCounters(t, h)
	tool := `{"type":"tool_result","tool_use_id":"FORBIDDEN_ANTHROPIC_TOOL_ID","content":[{"type":"text","text":"FORBIDDEN_ANTHROPIC_TOOL_TEXT"},{"type":"image","source":{"type":"base64","data":"FORBIDDEN_ANTHROPIC_TOOL_IMAGE"}},{"type":"document","source":{"type":"file","file_id":"FORBIDDEN_ANTHROPIC_TOOL_FILE"}},{"type":"future","reference":"FORBIDDEN_ANTHROPIC_TOOL_REFERENCE"}]}`
	for _, terminal := range []string{
		`{"role":"user","content":[` + tool + `]}`,
		`{"role":"assistant","content":[{"type":"tool_use","id":"FORBIDDEN_ANTHROPIC_CALL","name":"synthetic","input":{"text":"FORBIDDEN_ANTHROPIC_ARGUMENT"}}]}`,
		`{"role":"assistant","content":"FORBIDDEN_ANTHROPIC_REPLY"}`,
		`{"role":"system","content":"FORBIDDEN_ANTHROPIC_MESSAGE_SYSTEM"}`,
		`{"role":"developer","content":"FORBIDDEN_ANTHROPIC_DEVELOPER"}`,
	} {
		h.request(t, "POST", "/v1/messages", keyA, []byte(`{"model":"missing","max_tokens":32,"messages":[{"role":"user","content":"FORBIDDEN_ANTHROPIC_HISTORY"},`+terminal+`]}`))
	}
	body := []byte(`{"model":"missing","max_tokens":32,"system":"FORBIDDEN_ANTHROPIC_SYSTEM","messages":[{"role":"user","content":"FORBIDDEN_ANTHROPIC_CONSECUTIVE"},{"role":"user","content":[` + tool + `,{"type":"text","text":"ANTHROPIC-MIXED-前"},{"type":"image","source":{"type":"url","url":"FORBIDDEN_ANTHROPIC_IMAGE_URL"}},{"type":"document","source":{"type":"text","media_type":"text/plain","data":"FORBIDDEN_ANTHROPIC_DOCUMENT_TEXT"},"title":"FORBIDDEN_ANTHROPIC_FILENAME"},{"type":"text","text":"后"}]}]}`)
	for i := 0; i < 2; i++ {
		h.request(t, "POST", "/v1/messages", keyA, body)
		barrier := fmt.Sprintf("ANTHROPIC-TOOL-BARRIER-%d", i)
		h.request(t, "POST", "/v1/messages", keyA, []byte(fmt.Sprintf(`{"model":"missing","messages":[{"role":"user","content":%q}]}`, barrier)))
		waitSnapshotRecord(t, h, barrier)
	}
	r := waitChatDetail(t, h, "ANTHROPIC-MIXED-前")
	saved, queued, after := snapshotCounters(t, h)
	if r.Text != "ANTHROPIC-MIXED-前[图片][文件]后" || r.Protocol != "anthropic-messages" || r.ExtractionIncomplete || saved != 4 || queued != 0 || !maps.Equal(before, after) {
		t.Fatal("Anthropic 工具续跑/嵌套内容/连续用户边界或重复提交不正确")
	}
	assertChatPrivacy(t, h, "FORBIDDEN_ANTHROPIC_")
}
