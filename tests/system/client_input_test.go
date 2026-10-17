// 本文件通过真实 Messages 提交、管理查询和 FIFO 屏障验证客户端辅助内容不作为用户正文保存。
package system

import (
	"encoding/json"
	"fmt"
	"maps"
	"strings"
	"testing"
)

// TestAnthropicClientSuggestionSkipped 验证完整建议请求正常跳过，不回找末尾 user 之前的历史，也不报告采集缺口。
func TestAnthropicClientSuggestionSkipped(t *testing.T) {
	h := startHost(t, 1<<30, true, "")
	h.monitorAll(t)
	_, _, before := snapshotCounters(t, h)
	body, err := json.Marshal(map[string]any{
		"model": "audit-test-model", "max_tokens": 32,
		"messages": []any{
			map[string]string{"role": "user", "content": "FORBIDDEN_CLIENT_INPUT_HISTORY"},
			map[string]string{"role": "user", "content": "[SUGGESTION MODE: Suggest what the user might naturally type next into Claude Code.]\n\nFIRST: Look at the user's recent messages and original request.\n\nYour job is to predict what THEY would type - not what you think they should do.\n\nFORBIDDEN_CLIENT_INPUT_SUGGESTION\nFormat: 2-12 words, match the user's style. Or nothing.\n\nReply with ONLY the suggestion, no quotes or explanation."},
		},
	})
	if err != nil {
		t.Fatal("客户端建议夹具编码失败")
	}
	code, _ := h.request(t, "POST", "/v1/messages", keyA, body)
	if code != 200 {
		t.Fatal("客户端建议合成请求被拒绝")
	}
	h.request(t, "POST", "/v1/messages", keyA, []byte(`{"model":"audit-test-model","messages":[{"role":"user","content":"CLIENT-SUGGESTION-BARRIER"}]}`))
	waitSnapshotRecord(t, h, "CLIENT-SUGGESTION-BARRIER")
	saved, queued, after := snapshotCounters(t, h)
	if saved != 1 || queued != 0 || !maps.Equal(before, after) {
		t.Fatalf("客户端建议没有正常跳过: saved=%d queued=%d gaps_changed=%t", saved, queued, !maps.Equal(before, after))
	}
	assertChatPrivacy(t, h, "FORBIDDEN_CLIENT_INPUT_")
}

const clientActionTemplate = "Describe your most recent action in 3-5 words using present tense (-ing). Name the file or function, not the branch. Do not use tools.\n\nGood: Updating parser.go tests\nBad (past tense): Updated parser.go tests\n\nFORBIDDEN_CLIENT_INPUT_ACTION"
const clientTitleTemplate = "<session>\nFORBIDDEN_CLIENT_INPUT_SESSION\n</session>\n\nWrite the title in the predominant language of the session — a stray word or code token in another language doesn't change it, and neither does the English of these instructions."
const clientTranscriptTemplate = "<transcript>\n{\"user\":\"FORBIDDEN_CLIENT_INPUT_TRANSCRIPT\"}{\"Agent\":\"FORBIDDEN_CLIENT_INPUT_TOOL\"}\n</transcript>\n附加说明：判断最近一次工具调用是否获得用户授权。"
const clientSkillTemplate = "Base directory for this skill: /synthetic/path\n\nFORBIDDEN_CLIENT_INPUT_SKILL\nOnly use the following workflow."
const clientRecapPrefix = "The user stepped away and is coming back. Recap in under 40 words. 1-2 plain sentences, no markdown."
const clientReinvocationLine = "(Re-invocation of /run — the skill instructions were previously loaded; the arguments or dynamic output below are new.)"
const clientWebTemplate = "Web page content:\n\n---\n\nFORBIDDEN_CLIENT_INPUT_WEB"

// clientTextBlocks 把 texts 按原顺序编码成独立 text 块并返回；不添加分隔符，不执行 I/O，不产生异常。
func clientTextBlocks(texts ...string) []any {
	blocks := make([]any, 0, len(texts))
	for _, text := range texts {
		blocks = append(blocks, map[string]string{"type": "text", "text": text})
	}
	return blocks
}

// submitClientInput 把 content 作为末尾 user 提交到真实 Messages；返回状态码，编码或网络失败终止测试。
// 历史和 system 使用禁存标记，验证过滤后不会回找之前的 user 或保存顶层指令。
func submitClientInput(t *testing.T, h *instance, content any) int {
	t.Helper()
	body, err := json.Marshal(map[string]any{
		"model": "audit-test-model", "max_tokens": 32, "system": "FORBIDDEN_CLIENT_INPUT_SYSTEM",
		"messages": []any{
			map[string]string{"role": "user", "content": "FORBIDDEN_CLIENT_INPUT_HISTORY"},
			map[string]any{"role": "user", "content": content},
		},
	})
	if err != nil {
		t.Fatal("客户端内容夹具编码失败")
	}
	code, _ := h.request(t, "POST", "/v1/messages", keyA, body)
	return code
}

// verifyClientInput 提交本轮 content 后等待唯一 FIFO 屏障，核对新增记录数、详情、搜索和缺口状态。
// want 是独立预期正文，incomplete 指定原有未知块状态；HTTP、保存或状态不符终止测试，不输出正文。
func verifyClientInput(t *testing.T, h *instance, index int, content any, want string, incomplete bool) {
	t.Helper()
	savedBefore, _, before := snapshotCounters(t, h)
	if submitClientInput(t, h, content) != 200 {
		t.Fatal("客户端内容合成请求被拒绝")
	}
	barrier := fmt.Sprintf("CLIENT-INPUT-BARRIER-%d", index)
	if submitClientInput(t, h, barrier) != 200 {
		t.Fatal("客户端内容 FIFO 屏障被拒绝")
	}
	waitSnapshotRecord(t, h, barrier)
	saved, queued, after := snapshotCounters(t, h)
	wantSaved := savedBefore + 1
	if want != "" {
		wantSaved++
		code, raw := h.search(t, adminKey, map[string]any{"q": want, "limit": 100})
		var result struct {
			Total   int           `json:"total"`
			Records []savedRecord `json:"records"`
		}
		if code != 200 || json.Unmarshal(raw, &result) != nil || result.Total != 1 || len(result.Records) != 1 {
			t.Fatal("过滤后的本轮正文没有唯一搜索结果")
		}
		code, raw = h.request(t, "GET", api+"/detail?id="+result.Records[0].ID, adminKey, nil)
		var record savedRecord
		if code != 200 || json.Unmarshal(raw, &record) != nil || record.Text != want || record.Protocol != "anthropic-messages" || record.Model != "audit-test-model" || record.Truncated || record.ExtractionIncomplete != incomplete || result.Records[0].ExtractionIncomplete != incomplete {
			t.Fatal("过滤后的正文、协议或提取状态不符")
		}
	}
	wantGaps := maps.Clone(before)
	if incomplete {
		wantGaps["unsupported_content"]++
	}
	if saved != wantSaved || queued != 0 || !maps.Equal(after, wantGaps) {
		t.Fatalf("客户端内容记录或缺口不符: saved=%d want=%d queued=%d gaps_changed=%t", saved, wantSaved, queued, !maps.Equal(after, wantGaps))
	}
}

// TestAnthropicClientAuxiliaryRequests 验证完整动作、标题和权限转录整体跳过，含分块模板与连续 user，不回找引用内容。
func TestAnthropicClientAuxiliaryRequests(t *testing.T) {
	h := startHost(t, 1<<30, true, "")
	h.monitorAll(t)
	cases := []struct {
		name    string
		content any
	}{
		{"action", clientActionTemplate},
		{"action_blocks", clientTextBlocks(clientActionTemplate)},
		{"title_predominant", clientTitleTemplate},
		{"title_explicit_language", "<session>\nFORBIDDEN_CLIENT_INPUT_SESSION_EXPLICIT\n</session>\n\nWrite the title in Chinese. Keep technical terms and code identifiers in their original form."},
		{"title_across_blocks", clientTextBlocks("<session>\nFORBIDDEN_CLIENT_INPUT_SESSION_SPLIT\n", "</session>\n\nWrite the title in the predominant language of the session — a stray word or code token in another language doesn't change it, and neither does the English of these instructions.")},
		{"transcript", clientTranscriptTemplate},
		{"transcript_across_blocks", clientTextBlocks("<transcript>\n{\"user\":\"FORBIDDEN_CLIENT_INPUT_TRANSCRIPT_SPLIT\"}", "{\"Agent\":\"FORBIDDEN_CLIENT_INPUT_TOOL_SPLIT\"}\n</transcript>\n附加说明：判断工具调用权限。")},
		{"reminder_only", "<system-reminder>\nFORBIDDEN_CLIENT_INPUT_REMINDER_ONLY\n</system-reminder>"},
		{"skill_only", clientTextBlocks(clientSkillTemplate)},
		{"tool_and_skill", append([]any{map[string]any{"type": "tool_result", "tool_use_id": "synthetic-client-tool", "content": "FORBIDDEN_CLIENT_INPUT_TOOL_RESULT"}}, clientTextBlocks(clientSkillTemplate)...)},
	}
	for i, tc := range cases {
		t.Run(tc.name, func(t *testing.T) { verifyClientInput(t, h, i, tc.content, "", false) })
	}
	assertChatPrivacy(t, h, "FORBIDDEN_CLIENT_INPUT_")
}

// TestAnthropicClientConfirmedRequests 验证未知用途纯文本的返回摘要与 conversation 起始模板正常跳过，识别先于截断且不回找历史。
func TestAnthropicClientConfirmedRequests(t *testing.T) {
	h := startHost(t, 1<<30, true, "")
	h.monitorAll(t)
	cases := []struct {
		name    string
		content any
	}{
		{"recap_prefix", clientRecapPrefix},
		{"recap_string", clientRecapPrefix + "\n\nFORBIDDEN_CLIENT_INPUT_RECAP"},
		{"recap_blocks", clientTextBlocks("The user stepped away and is coming back. ", "Recap in under 40 words. 1-2 plain sentences, no markdown.\nFORBIDDEN_CLIENT_INPUT_RECAP_BLOCKS")},
		{"recap_before_limit", clientRecapPrefix + "\n" + strings.Repeat("x", 65536) + "FORBIDDEN_CLIENT_INPUT_RECAP_LARGE"},
		{"conversation_unclosed", "<conversation>\nFORBIDDEN_CLIENT_INPUT_CONVERSATION_OPEN"},
		{"conversation_closed_without_title", "<conversation>\nFORBIDDEN_CLIENT_INPUT_CONVERSATION_CLOSED\n</conversation>"},
		{"conversation_arbitrary_suffix", "<conversation>\nFORBIDDEN_CLIENT_INPUT_CONVERSATION_SUFFIX\n</conversation>\n\n并非标题生成指令"},
		{"conversation_blocks", clientTextBlocks("<conversation>\n", "FORBIDDEN_CLIENT_INPUT_CONVERSATION_BLOCKS")},
		// 首个非空字符在保存上限之后，不能先截断再判断 conversation 是否有内容。
		{"conversation_content_after_limit", "<conversation>\n" + strings.Repeat(" ", 65536) + "FORBIDDEN_CLIENT_INPUT_CONVERSATION_LARGE"},
	}
	for i, tc := range cases {
		t.Run(tc.name, func(t *testing.T) { verifyClientInput(t, h, i, tc.content, "", false) })
	}
	assertChatPrivacy(t, h, "FORBIDDEN_CLIENT_INPUT_")
}

// TestAnthropicClientConfirmedBlocks 验证重新调用技能与网页独立块删除自身，正文、附件及未知块状态保留，长辅助块不占正文额度。
func TestAnthropicClientConfirmedBlocks(t *testing.T) {
	h := startHost(t, 1<<30, true, "")
	h.monitorAll(t)
	reinvocation := clientReinvocationLine + "\n\nFORBIDDEN_CLIENT_INPUT_REINVOCATION"
	webWithTitle := "Web page content:\n\n---\n\n# 任意网页标题\n\nFORBIDDEN_CLIENT_INPUT_WEB"
	cases := []struct {
		name, want string
		content    any
		incomplete bool
	}{
		{"reinvocation_string", "", reinvocation, false},
		{"reinvocation_line_only", "", clientReinvocationLine, false},
		{"reinvocation_block", "", clientTextBlocks(reinvocation), false},
		{"web_string", "", clientWebTemplate, false},
		{"web_title_string", "", webWithTitle, false},
		{"web_block", "", clientTextBlocks(clientWebTemplate), false},
		{"web_title_block", "", clientTextBlocks(webWithTitle), false},
		{"web_blank_lines", "", "Web page content:\n \t\n---\n\t \nFORBIDDEN_CLIENT_INPUT_WEB_SPACES", false},
		{"reinvocation_split_line_literal", clientReinvocationLine + "\nCLIENT-REINVOKE-SPLIT 正文", clientTextBlocks("(Re-invocation of /run — ", "the skill instructions were previously loaded; the arguments or dynamic output below are new.)\nCLIENT-REINVOKE-SPLIT 正文"), false},
		{"web_split_blocks_literal", "Web page content:\n\n---\n\nCLIENT-WEB-SPLIT 正文", clientTextBlocks("Web page content:\n\n", "---\n\nCLIENT-WEB-SPLIT 正文"), false},
		{"reinvocation_fence_across_blocks", "```text\n" + clientReinvocationLine + "\nCLIENT-REINVOKE-FENCE 正文\n```", clientTextBlocks("```text\n", clientReinvocationLine+"\nCLIENT-REINVOKE-FENCE 正文\n", "```"), false},
		{"web_fence_across_blocks", "```text\nWeb page content:\n\n---\n\nCLIENT-WEB-FENCE 正文\n```", clientTextBlocks("```text\n", "Web page content:\n\n---\n\nCLIENT-WEB-FENCE 正文\n", "```"), false},
		{"reinvocation_mixed", "CLIENT-REINVOKE-REAL 前后", clientTextBlocks("CLIENT-REINVOKE-REAL 前", reinvocation, "后"), false},
		{"web_mixed", "CLIENT-WEB-REAL 前后", clientTextBlocks("CLIENT-WEB-REAL 前", webWithTitle, "后"), false},
		{"both_and_attachments", "CLIENT-CONFIRMED-ATTACHMENT 正文[图片][文件]", append(clientTextBlocks(reinvocation, "CLIENT-CONFIRMED-ATTACHMENT 正文", clientWebTemplate), map[string]any{"type": "image", "source": map[string]string{"type": "url", "url": "FORBIDDEN_CLIENT_INPUT_CONFIRMED_IMAGE"}}, map[string]any{"type": "document", "source": map[string]string{"type": "file", "file_id": "FORBIDDEN_CLIENT_INPUT_CONFIRMED_FILE"}}), false},
		{"both_and_unknown", "CLIENT-CONFIRMED-UNKNOWN 正文", append(clientTextBlocks(reinvocation, clientWebTemplate, "CLIENT-CONFIRMED-UNKNOWN 正文"), map[string]string{"type": "future", "payload": "FORBIDDEN_CLIENT_INPUT_CONFIRMED_UNKNOWN"}), true},
		{"reinvocation_before_limit", "CLIENT-REINVOKE-LIMIT 正文", clientTextBlocks(clientReinvocationLine+"\nFORBIDDEN_CLIENT_INPUT_REINVOKE_LARGE"+strings.Repeat("x", 65536), "CLIENT-REINVOKE-LIMIT 正文"), false},
		{"web_before_limit", "CLIENT-WEB-LIMIT 正文", clientTextBlocks(clientWebTemplate+strings.Repeat("x", 65536), "CLIENT-WEB-LIMIT 正文"), false},
		// 网页正文超过上限才开始，仍应识别完整独立块而不是保存空白前缀。
		{"web_content_after_limit", "CLIENT-WEB-LATE-CONTENT 正文", clientTextBlocks("Web page content:\n\n---\n\n"+strings.Repeat(" ", 65536)+"FORBIDDEN_CLIENT_INPUT_WEB_LATE", "CLIENT-WEB-LATE-CONTENT 正文"), false},
	}
	for i, tc := range cases {
		t.Run(tc.name, func(t *testing.T) { verifyClientInput(t, h, i, tc.content, tc.want, tc.incomplete) })
	}
	assertChatPrivacy(t, h, "FORBIDDEN_CLIENT_INPUT_")
}

// TestAnthropicClientTranscriptConsecutiveUsers 验证显式跨 user 转录整体跳过，但历史出现转录不能阻止末条真实输入。
func TestAnthropicClientTranscriptConsecutiveUsers(t *testing.T) {
	h := startHost(t, 1<<30, true, "")
	h.monitorAll(t)
	_, _, before := snapshotCounters(t, h)
	cases := []struct {
		name     string
		messages []any
	}{
		{"transcript", []any{
			map[string]string{"role": "user", "content": "<transcript>\n"},
			map[string]string{"role": "user", "content": "{\"user\":\"FORBIDDEN_CLIENT_INPUT_USER_TRANSCRIPT\"}{\"Agent\":\"FORBIDDEN_CLIENT_INPUT_AGENT_TRANSCRIPT\"}\n"},
			map[string]string{"role": "user", "content": "</transcript>\n附加说明：判断最近一次动作的权限。"},
		}},
		{"real_after_history", []any{
			map[string]string{"role": "user", "content": clientTranscriptTemplate},
			map[string]string{"role": "user", "content": "CLIENT-TRANSCRIPT-HISTORY-REAL 正文"},
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			body, err := json.Marshal(map[string]any{"model": "audit-test-model", "max_tokens": 32, "messages": tc.messages})
			if err != nil {
				t.Fatal("连续用户转录夹具编码失败")
			}
			code, _ := h.request(t, "POST", "/v1/messages", keyA, body)
			if code != 200 {
				t.Fatal("连续用户转录请求被拒绝")
			}
		})
	}
	if submitClientInput(t, h, "CLIENT-TRANSCRIPT-USERS-BARRIER") != 200 {
		t.Fatal("连续用户转录屏障失败")
	}
	waitSnapshotRecord(t, h, "CLIENT-TRANSCRIPT-USERS-BARRIER")
	r := waitChatDetail(t, h, "CLIENT-TRANSCRIPT-HISTORY-REAL")
	saved, queued, after := snapshotCounters(t, h)
	if r.Text != "CLIENT-TRANSCRIPT-HISTORY-REAL 正文" || r.Truncated || r.ExtractionIncomplete || saved != 2 || queued != 0 || !maps.Equal(before, after) {
		t.Fatal("连续用户转录被保存，或历史转录导致新正文丢失")
	}
	assertChatPrivacy(t, h, "FORBIDDEN_CLIENT_INPUT_")
}

// TestAnthropicClientAttachments 验证附加客户端块只删除自身，保留独立正文、附件占位及未知块状态，且先过滤再应用长度上限。
func TestAnthropicClientAttachments(t *testing.T) {
	h := startHost(t, 1<<30, true, "")
	h.monitorAll(t)
	reminder := "<system-reminder>\nFORBIDDEN_CLIENT_INPUT_REMINDER\n</system-reminder>"
	opened := "<ide_opened_file>\nThe user opened the file /synthetic/FORBIDDEN_CLIENT_INPUT_IDE.go in the IDE. This may or may not be related to the current task.\n</ide_opened_file>"
	selection := "<ide_selection>\nThe user selected lines 1 to 2 from /synthetic/file.go:\nFORBIDDEN_CLIENT_INPUT_SELECTION\n</ide_selection>"
	tool := map[string]any{"type": "tool_result", "tool_use_id": "synthetic-client-tool", "content": "FORBIDDEN_CLIENT_INPUT_TOOL_RESULT"}
	cases := []struct {
		name, want string
		content    any
		incomplete bool
	}{
		{"reminder_string", "CLIENT-REMINDER-STRING 正文\n\n", "CLIENT-REMINDER-STRING 正文\n\n" + reminder, false},
		{"reminder_blocks", "CLIENT-REMINDER-BLOCKS 正文", clientTextBlocks(reminder, "CLIENT-REMINDER-BLOCKS 正文", reminder), false},
		{"reminder_across_blocks", "CLIENT-REMINDER-SPLIT 正文\n\n", clientTextBlocks("CLIENT-REMINDER-SPLIT 正文\n\n<system-reminder>\nFORBIDDEN_CLIENT_INPUT_SPLIT", "\n</system-reminder>"), false},
		{"ide_string", "CLIENT-IDE-STRING 正文\n\n\n\n", "CLIENT-IDE-STRING 正文\n\n" + opened + "\n\n" + selection, false},
		{"ide_blocks", "CLIENT-IDE-BLOCKS 正文", clientTextBlocks(opened, "CLIENT-IDE-BLOCKS 正文", selection), false},
		{"skill_and_text", "CLIENT-SKILL-REAL 正文", clientTextBlocks(clientSkillTemplate, "CLIENT-SKILL-REAL 正文"), false},
		{"tool_skill_and_text", "CLIENT-TOOL-SKILL-REAL 正文", append([]any{tool}, clientTextBlocks(clientSkillTemplate, "CLIENT-TOOL-SKILL-REAL 正文")...), false},
		{"attachment", "CLIENT-ATTACHMENT 正文[图片][文件]", append(clientTextBlocks(reminder, "CLIENT-ATTACHMENT 正文"), map[string]any{"type": "image", "source": map[string]string{"type": "url", "url": "FORBIDDEN_CLIENT_INPUT_IMAGE"}}, map[string]any{"type": "document", "source": map[string]string{"type": "file", "file_id": "FORBIDDEN_CLIENT_INPUT_FILE"}}), false},
		{"unknown", "CLIENT-UNKNOWN 正文", append(clientTextBlocks(reminder, "CLIENT-UNKNOWN 正文"), map[string]string{"type": "future", "payload": "FORBIDDEN_CLIENT_INPUT_UNKNOWN"}), true},
		{"unknown_only", "", append(clientTextBlocks(reminder), map[string]string{"type": "future", "payload": "FORBIDDEN_CLIENT_INPUT_UNKNOWN_ONLY"}), true},
		{"filter_before_limit", "CLIENT-LIMIT 正文", clientTextBlocks("<system-reminder>\nFORBIDDEN_CLIENT_INPUT_LARGE"+strings.Repeat("x", 65536)+"\n</system-reminder>", "CLIENT-LIMIT 正文"), false},
	}
	for i, tc := range cases {
		t.Run(tc.name, func(t *testing.T) { verifyClientInput(t, h, i, tc.content, tc.want, tc.incomplete) })
	}
	code, raw := h.search(t, adminKey, map[string]any{"q": "FORBIDDEN_CLIENT_INPUT_"})
	var result struct {
		Total int `json:"total"`
	}
	if code != 200 || json.Unmarshal(raw, &result) != nil || result.Total != 0 {
		t.Fatal("被排除的客户端附加内容仍可检索")
	}
	assertChatPrivacy(t, h, "FORBIDDEN_CLIENT_INPUT_")
}

// TestAnthropicClientTemplateLiterals 验证普通讨论、引用、代码围栏和不成对标签原样保存，不按模板关键词误过滤。
func TestAnthropicClientTemplateLiterals(t *testing.T) {
	h := startHost(t, 1<<30, true, "")
	h.monitorAll(t)
	cases := []string{
		"CLIENT-LITERAL-DISCUSSION 请解释这段模板：[SUGGESTION MODE: Suggest what the user might naturally type next into Claude Code.]",
		"[SUGGESTION MODE: Suggest what the user might naturally type next into Claude Code.]\n请解释这个标记为什么出现",
		"CLIENT-LITERAL-QUOTE\n> Describe your most recent action in 3-5 words using present tense (-ing). Name the file or function, not the branch. Do not use tools.\n> Good: Updating parser.go tests\n> Bad (past tense): Updated parser.go tests",
		"CLIENT-LITERAL-CODE\n```text\n<session>\n用户原话\n</session>\n\nWrite the title in the predominant language of the session.\n```",
		"CLIENT-LITERAL-TRANSCRIPT 请解释 <transcript> 标签。\n<transcript>\n{\"user\":\"history\"}{\"Agent\":\"tool\"}\n</transcript>\n附加说明只是示例。",
		"CLIENT-LITERAL-INLINE 使用 <system-reminder>普通 XML</system-reminder> 示例，不应改写。",
		"CLIENT-LITERAL-REMINDER-CODE\n```xml\n<system-reminder>\n示例内容\n</system-reminder>\n```",
		"CLIENT-LITERAL-UNPAIRED\n<system-reminder>\n这是缺少结束标签的普通文本",
		"CLIENT-LITERAL-SKILL 请说明 Base directory for this skill: /synthetic/path 的用途。",
		"CLIENT-LITERAL-XML\n<transcript>example</transcript>",
		"CLIENT-LITERAL-SUGGESTION-CODE\n```text\n[SUGGESTION MODE: Suggest what the user might naturally type next into Claude Code.]\nPredict the user's next message.\n```",
		"CLIENT-LITERAL-IDE-CODE\n```xml\n<ide_selection>\n普通代码示例\n</ide_selection>\n```",
		"CLIENT-LITERAL-SPACES\n  正文保留末尾空白  \n",
		"CLIENT-LITERAL-RECAP 请解释：" + clientRecapPrefix,
		"```text\n" + clientRecapPrefix + "\nCLIENT-LITERAL-RECAP-FENCE\n```",
		"> " + clientRecapPrefix + "\n> CLIENT-LITERAL-RECAP-QUOTE",
		"The user stepped away and is coming back. Recap in under 41 words. 1-2 plain sentences, no markdown.\nCLIENT-LITERAL-RECAP-NEAR",
		"<conversation>",
		"<conversation>\n \t ",
		"<conversation>CLIENT-LITERAL-CONVERSATION-INLINE",
		"CLIENT-LITERAL-CONVERSATION-PREFIX\n<conversation>\n普通示例",
		"```xml\n<conversation>\nCLIENT-LITERAL-CONVERSATION-FENCE\n```",
		"> <conversation>\n> CLIENT-LITERAL-CONVERSATION-QUOTE",
		"CLIENT-LITERAL-REINVOKE-PREFIX\n" + clientReinvocationLine + "\n普通说明",
		clientReinvocationLine + " 同行还有 CLIENT-LITERAL-REINVOKE-SUFFIX",
		strings.Replace(clientReinvocationLine, "—", "-", 1) + "\nCLIENT-LITERAL-REINVOKE-NEAR",
		"```text\n" + clientReinvocationLine + "\nCLIENT-LITERAL-REINVOKE-FENCE\n```",
		"> " + clientReinvocationLine + "\n> CLIENT-LITERAL-REINVOKE-QUOTE",
		"CLIENT-LITERAL-WEB-PREFIX\nWeb page content:\n\n---\n\n普通正文",
		"# 任意网页标题\n\nWeb page content:\n\n---\n\nCLIENT-LITERAL-WEB-HEADING-FIRST",
		"Web page content:\n\n--\n\nCLIENT-LITERAL-WEB-NEAR",
		"Web page content:\n\n---\n \t ",
		"```markdown\n# 标题\n\nWeb page content:\n\n---\n\nCLIENT-LITERAL-WEB-FENCE\n```",
		"> Web page content:\n> \n> ---\n> \n> CLIENT-LITERAL-WEB-QUOTE",
	}
	for i, text := range cases {
		t.Run(fmt.Sprintf("literal_%d", i), func(t *testing.T) { verifyClientInput(t, h, i, text, text, false) })
	}
	assertChatPrivacy(t, h, "FORBIDDEN_CLIENT_INPUT_")
}
