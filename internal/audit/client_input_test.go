// 本文件通过公开采集和查询接口验证客户端辅助请求不保存、不回捞历史，也不产生缺口。
package audit_test

import (
	"encoding/json"
	"maps"
	"strings"
	"testing"
	"time"

	"github.com/shenleg/cpa-request-info/internal/audit"
)

const clientSuggestionFixture = "[SUGGESTION MODE: Suggest what the user might naturally type next into Claude Code.]\n\nFIRST: Look at the user's recent messages and original request.\n\nYour job is to predict what THEY would type - not what you think they should do.\n\nFormat: 2-12 words, match the user's style. Or nothing.\n\nReply with ONLY the suggestion, no quotes or explanation."
const clientClassifierFixture = `Err on the side of blocking. Your ENTIRE response MUST begin with <block>. Do NOT output any analysis, reasoning, or commentary before <block>. No "Looking at..." or similar preamble.`

// waitClientBarrier 等待 c 的本轮屏障可查询；成功无返回值，故障或五秒超时终止测试。
func waitClientBarrier(t *testing.T, c *audit.Collector) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := c.Detail("client-barrier"); err == nil {
			return
		} else if err.Error() != "not_found" {
			t.Fatalf("查询屏障失败：%v", err)
		}
		if status := c.Status(); status.State != "active" {
			t.Fatalf("采集意外停止：%+v", status)
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("等待本轮保存屏障超时")
}

// captureClientInput 提交合成 Anthropic 消息并用后续保存作为 FIFO 屏障；返回本次记录与是否保存，编码或保存失败终止测试。
func captureClientInput(t *testing.T, messages []any) (string, bool) {
	t.Helper()
	c := openAudit(t, newAuditDirectory(t), 0)
	scope := "synthetic-client-input-scope"
	monitorScope(t, c, scope)
	before := c.Status().Gaps
	body, err := json.Marshal(map[string]any{"model": "synthetic-model", "messages": messages})
	if err != nil {
		t.Fatal(err)
	}
	c.CaptureExecution("client-input", scope, "anthropic-messages", body)
	c.CaptureExecution("client-barrier", scope, "anthropic-messages", []byte(`{"model":"synthetic-model","messages":[{"role":"user","content":"client-barrier"}]}`))
	// 等待可查询的已保存屏障，不能用固定延时证明辅助内容没有保存。
	waitClientBarrier(t, c)
	if !maps.Equal(before, c.Status().Gaps) {
		t.Fatalf("正常辅助过滤产生缺口：%v", c.Status().Gaps)
	}
	record, err := c.Detail("client-input")
	if err != nil && err.Error() != "not_found" {
		t.Fatalf("查询本轮记录失败：%v", err)
	}
	wantSaved := int64(1)
	if err == nil {
		wantSaved++
	}
	if status := c.Status(); status.Saved != wantSaved || status.Queued != 0 {
		t.Fatalf("末尾输入记录数不符：%+v", status)
	}
	return record.Text, err == nil
}

// clientTextMessage 构造合成末尾 user 消息；content 为协议正文，返回消息，无错误或副作用。
func clientTextMessage(content any) map[string]any {
	return map[string]any{"role": "user", "content": content}
}

// clientTextBlocks 把 texts 分别放在协议 text 块中；返回内容数组，无错误或副作用。
func clientTextBlocks(texts ...string) []any {
	blocks := make([]any, 0, len(texts))
	for _, text := range texts {
		blocks = append(blocks, map[string]any{"type": "text", "text": text})
	}
	return blocks
}

// TestClientInputAuxiliary 验证五类客户端辅助内容在字符串、跨块和跨消息形式下均不保存。
func TestClientInputAuxiliary(t *testing.T) {
	const title = "Write the title in the predominant language of the session — a stray word or code token in another language doesn't change it, and neither does the English of these instructions."
	const action = "Describe your most recent action in 3-5 words using present tense (-ing). Name the file or function, not the branch. Do not use tools.\n\nGood: \"Reading runAgent.ts\"\nBad (past tense): \"Analyzed the branch diff\""
	const transcript = "<transcript>\n{\"user\":\"仅供上下文的历史原话\"}{\"Agent\":\"synthetic tool result\"}\n</transcript>\n检查最后一个动作"
	for _, tc := range []struct {
		name     string
		messages []any
	}{
		{"title", []any{clientTextMessage("<session>\n用户实际原话\n</session>\n\n" + title)}},
		{"title_language", []any{clientTextMessage("<session>\n用户实际原话\n</session>\n\nWrite the title in Chinese. Keep technical terms and code identifiers in their original form.")}},
		{"title_blocks", []any{clientTextMessage(clientTextBlocks("<ses", "sion>\n用户实际原话\n</session>\n\n", title))}},
		{"action", []any{clientTextMessage(action)}},
		{"suggestion_blocks", []any{clientTextMessage(clientTextBlocks(clientSuggestionFixture[:35], clientSuggestionFixture[35:]))}},
		{"transcript", []any{clientTextMessage(transcript)}},
		{"transcript_blocks", []any{clientTextMessage(clientTextBlocks("<trans", "cript>\n", "{\"user\":\"历史\"}{\"Bash\":\"tool\"}", "\n</transcript>\n", "检查最后一个动作"))}},
		{"transcript_messages", []any{clientTextMessage(clientTextBlocks("<transcript>\n")), clientTextMessage(clientTextBlocks("{\"user\":\"历史\"}{\"Bash\":\"tool\"}")), clientTextMessage(clientTextBlocks("</transcript>\n")), clientTextMessage(clientTextBlocks(clientClassifierFixture))}},
		{"skill_string", []any{clientTextMessage("Base directory for this skill: /synthetic/skills/test\n\n# 技能说明\n仅由客户端加载")}},
		{"skill_blocks", []any{clientTextMessage(clientTextBlocks("Base directory for this skill: /synthetic/skills/test\n\n# 技能说明"))}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if text, saved := captureClientInput(t, tc.messages); saved {
				t.Fatalf("辅助内容被保存：%q", text)
			}
		})
	}
}

// TestClientInputCleanContent 验证明确客户端块被排除，独立正文和原始空白仍保留，引用模板不误删。
func TestClientInputCleanContent(t *testing.T) {
	const reminder = "<system-reminder>\n客户端附加说明\n</system-reminder>"
	const skill = "Base directory for this skill: /synthetic/path\n\n# 技能\n客户端加载说明"
	for _, tc := range []struct {
		name    string
		content any
		want    string
	}{
		{"string", "用户实际正文\n\n" + reminder, "用户实际正文\n\n"},
		{"blocks", clientTextBlocks(reminder, "用户实际正文", reminder), "用户实际正文"},
		{"split", clientTextBlocks("用户实际正文\n\n<system-rem", "inder>\n附加说明\n</system-reminder>"), "用户实际正文\n\n"},
		{"ide", "<ide_opened_file>\nThe user opened /synthetic/test.go in the IDE.\n</ide_opened_file>\n正文", "\n正文"},
		{"only_reminder", reminder, ""},
		{"nested_reminder", "正文\n<system-reminder>\n外部说明\n" + reminder + "\n剩余说明\n</system-reminder>", "正文\n"},
		{"identified_reminder", "正文\n<system-reminder id=\"1234567890abcdef\">\n附加说明\n</system-reminder id=\"1234567890abcdef\">", "正文\n"},
		{"mismatched_reminder", "正文\n<system-reminder id=\"1234567890abcdef\">\n说明\n</system-reminder id=\"fedcba0987654321\">", "正文\n<system-reminder id=\"1234567890abcdef\">\n说明\n</system-reminder id=\"fedcba0987654321\">"},
		{"skill_mixed", clientTextBlocks(skill, "真实正文"), "真实正文"},
		{"inline", "请解释 <system-reminder>普通 XML</system-reminder> 示例", "请解释 <system-reminder>普通 XML</system-reminder> 示例"},
		{"fenced", "```xml\n" + reminder + "\n```", "```xml\n" + reminder + "\n```"},
		{"split_fenced", clientTextBlocks("```text\n", skill, "\n```"), "```text\n" + skill + "\n```"},
		{"unpaired", "正文\n<system-reminder>\n缺少结束标签", "正文\n<system-reminder>\n缺少结束标签"},
		{"spaces", " \n\t 正文  \n", " \n\t 正文  \n"},
		{"plain_session", "<session>\n用户原话\n</session>\n\n请解释此标签", "<session>\n用户原话\n</session>\n\n请解释此标签"},
		{"plain_transcript", "<transcript>\nexample\n</transcript>", "<transcript>\nexample\n</transcript>"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			text, saved := captureClientInput(t, []any{clientTextMessage(tc.content)})
			if text != tc.want || saved != (tc.want != "") {
				t.Fatalf("正文清理不符：text=%q saved=%t want=%q", text, saved, tc.want)
			}
		})
	}
}

// TestClientInputManualBoundaries 验证仅引用建议首行、转录后的真实输入和跨工具代码围栏不会被误删。
func TestClientInputManualBoundaries(t *testing.T) {
	const question = "[SUGGESTION MODE: Suggest what the user might naturally type next into Claude Code.]\n请解释这个标记为什么出现。"
	const fenced = "```xml\n<system-reminder>\n示例\n</system-reminder>\n```"
	for _, tc := range []struct {
		name     string
		messages []any
		want     string
	}{
		{"suggestion_header", []any{clientTextMessage(question)}, question},
		{"after_split_transcript", []any{clientTextMessage("<transcript>\n"), clientTextMessage("{\"user\":\"历史\"}"), clientTextMessage("</transcript>"), clientTextMessage("请修复 parser.go")}, "请修复 parser.go"},
		{"fence_across_tool", []any{clientTextMessage([]any{map[string]any{"type": "text", "text": "```xml\n"}, map[string]any{"type": "tool_result", "content": "ignored"}, map[string]any{"type": "text", "text": "<system-reminder>\n示例\n</system-reminder>\n```"}})}, fenced},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if text, saved := captureClientInput(t, tc.messages); !saved || text != tc.want {
				t.Fatalf("真实正文被误删：saved=%t text=%q want=%q", saved, text, tc.want)
			}
		})
	}
}

// TestClientInputBeforeTruncation 验证辅助识别早于长度限制，正文清理后的UTF-8前缀及未知尾块状态独立。
func TestClientInputBeforeTruncation(t *testing.T) {
	c := openAudit(t, newAuditDirectory(t), 0)
	scope := "synthetic-client-limit-scope"
	monitorScope(t, c, scope)
	limit := 4
	if err := c.SetSettings(audit.SettingsChange{MaxTextBytes: &limit}); err != nil {
		t.Fatal(err)
	}
	// send 将 content 编码为本次 id 的普通用户请求；编码失败终止测试，无返回值。
	send := func(id string, content any) {
		t.Helper()
		body, err := json.Marshal(map[string]any{"model": "synthetic", "messages": []any{clientTextMessage(content)}})
		if err != nil {
			t.Fatal(err)
		}
		c.CaptureExecution(id, scope, "anthropic-messages", body)
	}
	send("long-title", "<session>\n"+strings.Repeat("历史", 40000)+"\n</session>\n\nWrite the title in Chinese. Keep technical terms and code identifiers in their original form.")
	send("long-transcript", "<transcript>\n{\"user\":\""+strings.Repeat("历史", 40000)+"\"}\n</transcript>\n附加指令")
	content := clientTextBlocks("<system-reminder>\n"+strings.Repeat("附加", 40000)+"\n</system-reminder>", "中🙂z")
	content = append(content, map[string]any{"type": "future", "payload": "未知载荷"})
	send("short-real", content)
	send("client-barrier", "done")
	waitClientBarrier(t, c)
	for _, id := range []string{"long-title", "long-transcript"} {
		if _, err := c.Detail(id); err == nil {
			t.Fatalf("长辅助请求被截断后误存：%s", id)
		}
	}
	record, err := c.Detail("short-real")
	if err != nil || record.Text != "中" || !record.Truncated || !record.ExtractionIncomplete {
		t.Fatalf("清理后UTF-8或双状态不符：%+v err=%v", record, err)
	}
	if status := c.Status(); status.Saved != 2 || status.Gaps["unsupported_content"] != 1 {
		t.Fatalf("辅助请求或未知块计数不符：%+v", status)
	}
}

// TestClientInputNestedPayload 验证附件和工具内部的模板不参与分类，普通协议不会按Anthropic客户端规则改写。
func TestClientInputNestedPayload(t *testing.T) {
	const template = "[SUGGESTION MODE: Suggest what the user might naturally type next into Claude Code.]"
	tool := map[string]any{"type": "tool_result", "content": clientTextBlocks(template)}
	image := map[string]any{"type": "image", "source": map[string]any{"type": "url", "url": template}}
	content := append([]any{tool, image}, clientTextBlocks("真实正文")...)
	if text, saved := captureClientInput(t, []any{clientTextMessage(content)}); !saved || text != "[图片]真实正文" {
		t.Fatalf("嵌套载荷改变正文分类：%q saved=%t", text, saved)
	}
	for _, protocol := range []string{"chat-completions", "responses"} {
		t.Run(protocol, func(t *testing.T) {
			c := openAudit(t, newAuditDirectory(t), 0)
			scope := "synthetic-other-protocol"
			monitorScope(t, c, scope)
			request := map[string]any{"model": "synthetic", "messages": []any{clientTextMessage(template)}}
			if protocol == "responses" {
				request = map[string]any{"model": "synthetic", "input": template}
			}
			body, err := json.Marshal(request)
			if err != nil {
				t.Fatal(err)
			}
			c.CaptureExecution("other-protocol", scope, protocol, body)
			waitSaved(t, c, 1)
			record, err := c.Detail("other-protocol")
			if err != nil || record.Text != template {
				t.Fatalf("其他协议文本被改变：%+v err=%v", record, err)
			}
		})
	}
}

// TestClientInputSuggestion 验证客户端建议请求整体跳过，不把它之前的真实原话重复保存。
func TestClientInputSuggestion(t *testing.T) {
	text, saved := captureClientInput(t, []any{
		map[string]any{"role": "user", "content": "仅供上下文的历史原话"},
		clientTextMessage(clientSuggestionFixture),
	})
	if saved {
		t.Fatalf("建议请求被当作手动输入保存：%q", text)
	}
}
