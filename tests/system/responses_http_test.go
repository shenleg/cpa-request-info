// 本文件通过真实 Responses HTTP、管理查询与可控上游验收原始本次输入；不调用私有提取器。
package system

import (
	"encoding/json"
	"fmt"
	"maps"
	"slices"
	"testing"
)

// TestResponsesHTTPStringBeforeRouting 验证路由失败前已观察字符串输入，列表与详情保留客户端请求模型和协议。
func TestResponsesHTTPStringBeforeRouting(t *testing.T) {
	h := startHost(t, 1<<30, true, "")
	h.monitorAll(t)
	body := []byte(`{"model":"responses-missing-client-model","input":"RESPONSES-STRING-BEFORE-ROUTING","instructions":"FORBIDDEN_RESPONSES_INSTRUCTIONS","previous_response_id":"FORBIDDEN_PREVIOUS_RESPONSE_ID"}`)
	code, _ := h.request(t, "POST", "/v1/responses", keyA, body)
	if code != 400 {
		t.Fatal("审计不应改变宿主模型路由失败")
	}
	record := waitChatDetail(t, h, "RESPONSES-STRING-BEFORE-ROUTING")
	if record.Text != "RESPONSES-STRING-BEFORE-ROUTING" || record.Model != "responses-missing-client-model" || record.Protocol != "responses" || record.Truncated || record.ExtractionIncomplete {
		t.Fatal("Responses 字符串输入或客户端请求元数据不正确")
	}
	code, raw := h.request(t, "GET", api+"/records", adminKey, nil)
	var list struct {
		Records []savedRecord `json:"records"`
	}
	if code != 200 || json.Unmarshal(raw, &list) != nil || len(list.Records) != 1 || list.Records[0].Protocol != "responses" || list.Records[0].Model != record.Model {
		t.Fatal("Responses 列表协议或请求模型丢失")
	}
	assertChatPrivacy(t, h, "FORBIDDEN_RESPONSES_INSTRUCTIONS", "FORBIDDEN_PREVIOUS_RESPONSE_ID")
}

// TestResponsesHTTPTerminalMessage 验证只取结构化末尾明确 user，工具续跑不回找旧输入，显式重发独立记录。
func TestResponsesHTTPTerminalMessage(t *testing.T) {
	h := startHost(t, 1<<30, true, "")
	h.monitorAll(t)
	_, _, before := snapshotCounters(t, h)
	for _, terminal := range []string{
		`{"type":"message","role":"assistant","content":[{"type":"output_text","text":"FORBIDDEN_RESPONSES_REPLY"}]}`,
		`{"role":"system","content":"FORBIDDEN_RESPONSES_SYSTEM"}`,
		`{"role":"developer","content":"FORBIDDEN_RESPONSES_DEVELOPER"}`,
		`{"type":"function_call","arguments":"FORBIDDEN_RESPONSES_ARGUMENTS"}`,
		`{"type":"function_call_output","call_id":"FORBIDDEN_RESPONSES_CALL_ID","output":[{"type":"input_text","text":"FORBIDDEN_RESPONSES_TOOL_TEXT"},{"type":"input_image","image_url":"FORBIDDEN_RESPONSES_TOOL_IMAGE"}]}`,
		`{"type":"custom_tool_call_output","output":"FORBIDDEN_RESPONSES_CUSTOM_TOOL"}`,
		`{"type":"computer_call_output","output":{"type":"computer_screenshot","image_url":"FORBIDDEN_RESPONSES_SCREENSHOT"}}`,
		`{"type":"reasoning","encrypted_content":"FORBIDDEN_RESPONSES_REASONING"}`,
	} {
		body := []byte(`{"model":"missing","previous_response_id":"FORBIDDEN_RESPONSES_PREVIOUS","input":[{"role":"user","content":"FORBIDDEN_RESPONSES_OLD_USER"},` + terminal + `]}`)
		code, _ := h.request(t, "POST", "/v1/responses", keyA, body)
		if code != 400 {
			t.Fatal("审计不应改变结构化请求的原路由失败")
		}
	}
	for i := 0; i < 2; i++ {
		body := []byte(`{"model":"missing","previous_response_id":"FORBIDDEN_RESPONSES_PREVIOUS","input":[{"role":"user","content":"FORBIDDEN_RESPONSES_CONSECUTIVE"},{"type":"message","role":"user","content":[{"type":"input_text","text":"RESPONSES-EXPLICIT-RESEND"}]}]}`)
		h.request(t, "POST", "/v1/responses", keyA, body)
		barrier := fmt.Sprintf("RESPONSES-TERMINAL-BARRIER-%d", i)
		h.request(t, "POST", "/v1/responses", keyA, []byte(fmt.Sprintf(`{"model":"missing","input":%q}`, barrier)))
		waitSnapshotRecord(t, h, barrier)
	}
	record := waitChatDetail(t, h, "RESPONSES-EXPLICIT-RESEND")
	saved, queued, after := snapshotCounters(t, h)
	if record.Text != "RESPONSES-EXPLICIT-RESEND" || saved != 4 || queued != 0 || !maps.Equal(before, after) {
		t.Fatal("末尾新输入、独立重发或工具预期跳过不符合规范")
	}
	assertChatPrivacy(t, h, "FORBIDDEN_RESPONSES_REPLY", "FORBIDDEN_RESPONSES_SYSTEM", "FORBIDDEN_RESPONSES_DEVELOPER", "FORBIDDEN_RESPONSES_ARGUMENTS", "FORBIDDEN_RESPONSES_CALL_ID", "FORBIDDEN_RESPONSES_TOOL_TEXT", "FORBIDDEN_RESPONSES_TOOL_IMAGE", "FORBIDDEN_RESPONSES_CUSTOM_TOOL", "FORBIDDEN_RESPONSES_SCREENSHOT", "FORBIDDEN_RESPONSES_REASONING", "FORBIDDEN_RESPONSES_PREVIOUS", "FORBIDDEN_RESPONSES_OLD_USER", "FORBIDDEN_RESPONSES_CONSECUTIVE")
}

// TestResponsesHTTPContent 验证原协议混合内容、占位符、空白和未知项诊断，不保存原始块作为回退。
func TestResponsesHTTPContent(t *testing.T) {
	h := startHost(t, 1<<30, true, "")
	h.monitorAll(t)
	cases := []struct {
		name, input, want string
		gap               bool
	}{
		{"mixed", `[{"role":"user","content":[{"type":"input_text","text":"RESPONSES-MIXED-前"},{"type":"input_image","image_url":"FORBIDDEN_RESPONSES_IMAGE_URL"},{"type":"input_audio","input_audio":{"data":"FORBIDDEN_RESPONSES_AUDIO","format":"wav"}},{"type":"input_video","video_url":"FORBIDDEN_RESPONSES_VIDEO"},{"type":"input_file","filename":"FORBIDDEN_RESPONSES_FILENAME","file_data":"FORBIDDEN_RESPONSES_FILE_DATA"},{"type":"function_call_output","output":[{"type":"input_text","text":"FORBIDDEN_RESPONSES_NESTED_TOOL"}]},{"type":"input_text","text":"后"}]}]`, "RESPONSES-MIXED-前[图片][音频][视频][文件]后", false},
		{"image_reference", `[{"type":"message","role":"user","content":[{"type":"input_image","file_id":"FORBIDDEN_RESPONSES_IMAGE_ID"}]}]`, "[图片]", false},
		{"file_reference", `[{"role":"user","content":[{"type":"input_file","file_id":"FORBIDDEN_RESPONSES_FILE_ID"}]}]`, "[文件]", false},
		{"file_url", `[{"role":"user","content":[{"type":"input_file","file_url":"FORBIDDEN_RESPONSES_FILE_URL"}]}]`, "[文件]", false},
		{"audio_flat", `[{"role":"user","content":[{"type":"input_audio","data":"FORBIDDEN_RESPONSES_FLAT_AUDIO","format":"wav"}]}]`, "[音频]", false},
		{"video_object", `[{"role":"user","content":[{"type":"input_video","video_url":{"url":"FORBIDDEN_RESPONSES_VIDEO_OBJECT","processing":"static"}}]}]`, "[视频]", false},
		{"literal", `"RESPONSES-LITERAL-https://example.invalid/a.png data:image/png;base64,U1lOVEhFVElD synthetic-text-secret"`, "RESPONSES-LITERAL-https://example.invalid/a.png data:image/png;base64,U1lOVEhFVElD synthetic-text-secret", false},
		{"partial", `[{"role":"user","content":[{"type":"input_text","text":"RESPONSES-PARTIAL-前"},{"type":"future","payload":"FORBIDDEN_RESPONSES_UNKNOWN"},{"type":"input_text","text":"后"}]}]`, "RESPONSES-PARTIAL-前后", true},
		{"duplicate_type", `[{"role":"user","content":[{"type":"function_call_output","type":"input_text","text":"FORBIDDEN_RESPONSES_DUPLICATE_TYPE"},{"type":"input_text","text":"RESPONSES-DUPLICATE-SAFE"}]}]`, "RESPONSES-DUPLICATE-SAFE", true},
		{"unknown_tail", `[{"role":"user","content":"FORBIDDEN_RESPONSES_UNKNOWN_HISTORY"},{"type":"future","role":"user","content":"FORBIDDEN_RESPONSES_UNKNOWN_ROLE"}]`, "", true},
		{"item_reference", `[{"type":"item_reference","id":"FORBIDDEN_RESPONSES_ITEM_REF"}]`, "", true},
		{"unknown_only", `[{"role":"user","content":[{"type":"future","text":"FORBIDDEN_RESPONSES_UNKNOWN_TEXT"}]}]`, "", true},
		{"output_text", `[{"role":"user","content":[{"type":"output_text","text":"FORBIDDEN_RESPONSES_OUTPUT_TEXT"}]}]`, "", true},
		{"missing_role", `[{"type":"message","content":"FORBIDDEN_RESPONSES_MISSING_ROLE"}]`, "", true},
		{"role_alias", `[{"Role":"user","content":"FORBIDDEN_RESPONSES_ROLE_ALIAS"}]`, "", true},
		{"duplicate_role", `[{"role":"assistant","role":"user","content":"FORBIDDEN_RESPONSES_DUPLICATE_ROLE"}]`, "", true},
		{"missing_content", `[{"role":"user"}]`, "", true},
		{"missing_text", `[{"role":"user","content":[{"type":"input_text","Text":"FORBIDDEN_RESPONSES_TEXT_ALIAS"}]}]`, "", true},
		{"null_text", `[{"role":"user","content":[{"type":"input_text","text":null}]}]`, "", true},
		{"malformed_image", `[{"role":"user","content":[{"type":"input_image","image_url":{"url":"FORBIDDEN_RESPONSES_WRONG_IMAGE"}}]}]`, "", true},
		{"malformed_file", `[{"role":"user","content":[{"type":"input_file","filename":"FORBIDDEN_RESPONSES_FILENAME_ONLY"}]}]`, "", true},
		{"malformed_audio", `[{"role":"user","content":[{"type":"input_audio","input_audio":null}]}]`, "", true},
		{"wrong_input", `{"role":"user","content":"FORBIDDEN_RESPONSES_INPUT_OBJECT"}`, "", true},
		{"null_item", `[null]`, "", true},
		{"empty_string", `""`, "", false},
		{"empty_array", `[]`, "", false},
		{"empty_null", `null`, "", false},
		{"empty_content", `[{"role":"user","content":[]}]`, "", false},
		{"empty_text", `[{"role":"user","content":[{"type":"input_text","text":""}]}]`, "", false},
		{"whitespace", `[{"role":"user","content":" \n\t "}]`, " \n\t ", false},
	}
	seenIDs := make(map[string]bool)
	for i, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			savedBefore, _, before := snapshotCounters(t, h)
			body := []byte(`{"model":"missing","input":` + tc.input + `}`)
			h.request(t, "POST", "/v1/responses", keyA, body)
			// 本轮唯一提交实际落盘是 FIFO 屏障；不能用固定等待判断跳过。
			barrier := fmt.Sprintf("RESPONSES-CONTENT-BARRIER-%d", i)
			h.request(t, "POST", "/v1/responses", keyA, []byte(fmt.Sprintf(`{"model":"missing","input":%q}`, barrier)))
			waitSnapshotRecord(t, h, barrier)
			saved, queued, after := snapshotCounters(t, h)
			wantSaved := savedBefore + 1
			if tc.want != "" {
				wantSaved++
				r := waitChatDetail(t, h, tc.want)
				if r.Text != tc.want || r.Protocol != "responses" || r.ExtractionIncomplete != tc.gap || r.Truncated || seenIDs[r.ID] {
					t.Fatal("Responses 允许正文或提取状态不符合本轮独立预期")
				}
				seenIDs[r.ID] = true
			}
			wantGaps := maps.Clone(before)
			if tc.gap {
				wantGaps["unsupported_content"]++
			}
			if saved != wantSaved || queued != 0 || !maps.Equal(after, wantGaps) {
				t.Fatal("Responses 空输入与解析缺口混淆，或同次提交重复记录/计缺口")
			}
		})
	}
	assertChatPrivacy(t, h, "FORBIDDEN_RESPONSES_")
}

// TestResponsesHTTPRoutesAndAdmission 验证标准/compact/Codex 别名入口及 HTTP 支持范围，鉴权失败、未监控和握手不采集。
func TestResponsesHTTPRoutesAndAdmission(t *testing.T) {
	h := startHost(t, 1<<30, true, "")
	for _, path := range []string{"/v1/responses", "/v1/responses/compact", "/backend-api/codex/responses", "/backend-api/codex/responses/compact"} {
		h.request(t, "POST", path, keyA, []byte(`{"model":"missing","input":"FORBIDDEN_RESPONSES_UNMONITORED"}`))
	}
	h.monitorAll(t)
	_, _, before := snapshotCounters(t, h)
	for i, path := range []string{"/v1/responses", "/v1/responses/compact", "/backend-api/codex/responses", "/backend-api/codex/responses/compact"} {
		code, _ := h.request(t, "POST", path, "synthetic-invalid-client", []byte(`{"model":"missing","input":"FORBIDDEN_RESPONSES_UNAUTHENTICATED"}`))
		if code != 401 {
			t.Fatal("Responses 鉴权行为被改变")
		}
		marker := fmt.Sprintf("RESPONSES-HTTP-ROUTE-%d", i)
		// stream=true 的 compact 在协议分支就失败，也必须先观察允许输入。
		body := []byte(fmt.Sprintf(`{"model":"missing","stream":true,"input":%q}`, marker))
		code, _ = h.request(t, "POST", path, keyA, body)
		if code != 400 {
			t.Fatal("Responses 原宿主早期失败响应不一致")
		}
		r := waitChatDetail(t, h, marker)
		if r.Text != marker || r.Protocol != "responses" {
			t.Fatal("Responses HTTP 入口没有观察原协议输入")
		}
	}
	for _, path := range []string{"/v1/responses", "/backend-api/codex/responses"} {
		h.request(t, "GET", path, keyA, nil)
	}
	h.request(t, "POST", "/v1/responses", keyA, []byte(`{"model":"missing","input":"RESPONSES-ADMISSION-DRAIN"}`))
	waitSnapshotRecord(t, h, "RESPONSES-ADMISSION-DRAIN")
	saved, queued, after := snapshotCounters(t, h)
	if saved != 5 || queued != 0 || !maps.Equal(before, after) {
		t.Fatal("未监控/鉴权失败/握手被采集或误报缺口")
	}
	code, raw := h.request(t, "GET", api+"/status", adminKey, nil)
	var status struct {
		Available          bool     `json:"available"`
		SupportedProtocols []string `json:"supported_protocols"`
		ResponsesWebSocket bool     `json:"responses_websocket"`
	}
	if code != 200 || json.Unmarshal(raw, &status) != nil || !status.Available || !slices.Contains(status.SupportedProtocols, "responses") || !status.ResponsesWebSocket || !slices.Contains(status.SupportedProtocols, "responses-websocket") {
		t.Fatal("管理状态没有准确声明 Responses HTTP 支持范围")
	}
	assertChatPrivacy(t, h, "FORBIDDEN_RESPONSES_")
}

// TestResponsesHTTPMetadataAndAmbiguity 验证缺失/不可靠模型不阻止正文采集，重复协议字段只报告缺口而不兜底保存。
func TestResponsesHTTPMetadataAndAmbiguity(t *testing.T) {
	h := startHost(t, 1<<30, true, "")
	h.monitorAll(t)
	for i, field := range []string{"", `"model":null,`, `"model":123,`, `"model":{"name":"FORBIDDEN_RESPONSES_BAD_MODEL"},`} {
		marker := fmt.Sprintf("RESPONSES-UNKNOWN-MODEL-%d", i)
		h.request(t, "POST", "/v1/responses", keyA, []byte(fmt.Sprintf(`{%s"input":%q}`, field, marker)))
		r := waitChatDetail(t, h, marker)
		if r.Model != "" || r.Text != marker || r.Protocol != "responses" {
			t.Fatal("Responses 无法确定模型时猜测或丢弃了输入")
		}
	}
	_, _, before := snapshotCounters(t, h)
	for _, body := range []string{
		`{"model":"missing","input":"FORBIDDEN_RESPONSES_DUPLICATE_INPUT","input":"FORBIDDEN_RESPONSES_DUPLICATE_INPUT_2"}`,
		`{"model":"missing","input":"FORBIDDEN_RESPONSES_DUPLICATE_MODEL","model":"missing2"}`,
		`{"model":"missing","input":[`,
	} {
		h.request(t, "POST", "/v1/responses", keyA, []byte(body))
	}
	h.request(t, "POST", "/v1/responses", keyA, []byte(`{"model":"missing","input":"RESPONSES-METADATA-DRAIN"}`))
	waitSnapshotRecord(t, h, "RESPONSES-METADATA-DRAIN")
	saved, queued, after := snapshotCounters(t, h)
	wantGaps := maps.Clone(before)
	wantGaps["invalid_json"] += 3
	if saved != 5 || queued != 0 || !maps.Equal(wantGaps, after) {
		t.Fatal("Responses 歧义/畸形协议被保存或缺口分类错误")
	}
	assertChatPrivacy(t, h, "FORBIDDEN_RESPONSES_")
}
