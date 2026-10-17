// 本文件回归截图确认的自动摘要、会话/网页包装、技能重入及 Chat 精确指令，验证用途和独立正文边界。
package audit_test

import (
	"encoding/json"
	"maps"
	"strings"
	"testing"

	"github.com/shenleg/cpa-request-info/internal/audit"
)

const clientRecapFixture = "The user stepped away and is coming back. Recap in under 40 words. 1-2 plain sentences, no markdown. Lead with the overall goal and current state."
const clientReinvocationFixture = "(Re-invocation of /run — the skill instructions were previously loaded; the arguments or dynamic output below are new.)"
const clientWebFixture = "Web page content:\n---\n# 合成网页\n网页正文"

// TestClientInputConfirmedAuxiliary 验证摘要与会话包装仅在未知用途整条排除，不绑定会变化的标题或回找历史。
func TestClientInputConfirmedAuxiliary(t *testing.T) {
	for _, tc := range []struct {
		name    string
		content any
		text    string
	}{
		{"recap", clientRecapFixture, clientRecapFixture},
		{"recap_blocks", clientTextBlocks(clientRecapFixture[:40], clientRecapFixture[40:]), clientRecapFixture},
		{"conversation", "<conversation>\n# 合成任务 A\n## Context\n上下文", "<conversation>\n# 合成任务 A\n## Context\n上下文"},
		{"conversation_other_title", "<conversation>\n不同内容\n</conversation>", "<conversation>\n不同内容\n</conversation>"},
		{"conversation_blocks", clientTextBlocks("<conver", "sation>\n合成上下文"), "<conversation>\n合成上下文"},
	} {
		for _, purpose := range []audit.RequestPurpose{audit.PurposeUnknown, audit.PurposeMain, audit.PurposeTurn} {
			t.Run(tc.name+"/"+string(purpose), func(t *testing.T) {
				text, saved := captureClientInput(t, purpose, []any{clientTextMessage("不回找的历史"), clientTextMessage(tc.content)})
				if purpose == audit.PurposeUnknown {
					if saved {
						t.Fatalf("已确认的未知用途辅助请求被保存：%q", text)
					}
					return
				}
				if !saved || text != tc.text {
					t.Fatalf("明确用途改变整请求模板规则：saved=%t text=%q", saved, text)
				}
			})
		}
	}
}

// TestClientInputConfirmedBlocks 验证独立自动块对所有保留用途清理，其他独立正文和附件仍保存。
func TestClientInputConfirmedBlocks(t *testing.T) {
	for _, tc := range []struct {
		name    string
		content any
		want    string
	}{
		{"reinvocation_string", clientReinvocationFixture, ""},
		{"reinvocation_output", clientReinvocationFixture + "\n\n[Request interrupted by user]\n客户端动态输出", ""},
		{"web_string", clientWebFixture, ""},
		{"web_plain", "Web page content: --- Clipboard: writeText() method - Web APIs | MDN\n合成网页正文", ""},
		{"mixed", clientTextBlocks(clientReinvocationFixture, " 原始正文\n", clientWebFixture), " 原始正文\n"},
		{"mixed_attachment", append(clientTextBlocks(clientWebFixture, "正文"), map[string]any{"type": "image", "source": map[string]any{"type": "url", "url": "https://synthetic.invalid/image"}}), "正文[图片]"},
		{"web_between_text", clientTextBlocks("前文", clientWebFixture, "后文"), "前文后文"},
	} {
		for _, purpose := range []audit.RequestPurpose{audit.PurposeUnknown, audit.PurposeMain, audit.PurposeTurn} {
			t.Run(tc.name+"/"+string(purpose), func(t *testing.T) {
				text, saved := captureClientInput(t, purpose, []any{clientTextMessage(tc.content)})
				if text != tc.want || saved != (tc.want != "") {
					t.Fatalf("独立自动块清理不符：saved=%t text=%q want=%q", saved, text, tc.want)
				}
			})
		}
	}
}

// TestClientInputConfirmedManualBoundaries 验证普通引用、代码围栏、近似模板和工具内文字不触发新增过滤。
func TestClientInputConfirmedManualBoundaries(t *testing.T) {
	for _, text := range []string{
		"请解释 " + clientRecapFixture,
		"The user stepped away and is coming back. 请解释这句话。",
		"```text\n" + clientRecapFixture + "\n```",
		"请解释\n<conversation>\n上下文",
		"<conversation>普通 XML</conversation>",
		"<conversation>\n \t",
		"```xml\n<conversation>\n上下文\n```",
		"<other>\n上下文\n</other>",
		"请解释 " + clientReinvocationFixture,
		"(Re-invocation of /run — 请解释这句话。)",
		clientReinvocationFixture + " 此行是引用说明",
		"Web page content: 普通描述而非网页包装",
		"Web page content:\n---",
		"Web page content: ---不是分隔符",
		"请分析\n" + clientWebFixture,
	} {
		t.Run(text[:min(len(text), 35)], func(t *testing.T) {
			got, saved := captureClientInput(t, audit.PurposeUnknown, []any{clientTextMessage(text)})
			if !saved || got != text {
				t.Fatalf("近似模板或引用被误删：saved=%t got=%q want=%q", saved, got, text)
			}
		})
	}
	for _, template := range []string{clientReinvocationFixture, clientWebFixture} {
		t.Run("fence/"+template[:12], func(t *testing.T) {
			content := clientTextBlocks("```text\n", template, "\n```")
			got, saved := captureClientInput(t, audit.PurposeUnknown, []any{clientTextMessage(content)})
			if !saved || got != "```text\n"+template+"\n```" {
				t.Fatalf("跨块围栏中的模板被删：saved=%t got=%q", saved, got)
			}
		})
	}
	content := []any{
		map[string]any{"type": "tool_result", "content": clientRecapFixture},
		map[string]any{"type": "text", "text": "<conversation>\n有工具的普通正文"},
	}
	if text, saved := captureClientInput(t, audit.PurposeUnknown, []any{clientTextMessage(content)}); !saved || text != "<conversation>\n有工具的普通正文" {
		t.Fatalf("混合工具内容参与整请求模板识别：saved=%t text=%q", saved, text)
	}
}

// TestClientInputConfirmedBeforeTruncation 验证超长自动内容先识别，清理后保留正文的 UTF-8 与未知块状态不变。
func TestClientInputConfirmedBeforeTruncation(t *testing.T) {
	c := openAudit(t, newAuditDirectory(t), 0)
	const scope = "confirmed-client-limit"
	monitorScope(t, c, scope)
	if err := c.SetMaxTextBytes(4); err != nil {
		t.Fatal(err)
	}
	// send 提交 id/content 合成消息；编码失败终止测试，无返回值。
	send := func(id string, content any) {
		t.Helper()
		body, err := json.Marshal(map[string]any{"messages": []any{clientTextMessage(content)}})
		if err != nil {
			t.Fatal(err)
		}
		c.CaptureExecution(id, scope, "anthropic-messages", audit.PurposeUnknown, body)
	}
	wantGaps := maps.Clone(c.Status().Gaps)
	wantGaps["unsupported_content"]++
	long := strings.Repeat("自动内容", 20000)
	send("long-recap", clientRecapFixture+long)
	send("long-conversation", "<conversation>\n"+long)
	send("long-reinvocation", clientReinvocationFixture+"\n"+long)
	send("long-web", clientWebFixture+long)
	content := clientTextBlocks(clientWebFixture+long, "中🙂z", clientReinvocationFixture+"\n"+long)
	content = append(content, map[string]any{"type": "future", "payload": "不保存的未知载荷"})
	send("bounded-real", content)
	send("client-barrier", "done")
	waitClientBarrier(t, c)
	for _, id := range []string{"long-recap", "long-conversation", "long-reinvocation", "long-web"} {
		if _, err := c.Detail(id); err == nil || err.Error() != "not_found" {
			t.Fatalf("超长自动内容未正常跳过：id=%s err=%v", id, err)
		}
	}
	record, err := c.Detail("bounded-real")
	if err != nil || record.Text != "中" || !record.Truncated || !record.ExtractionIncomplete {
		t.Fatalf("清理后正文状态错误：%+v err=%v", record, err)
	}
	if status := c.Status(); status.Saved != 2 || !maps.Equal(status.Gaps, wantGaps) {
		t.Fatalf("正常跳过或未知块计数错误：%+v", status)
	}
}

// TestClientInputChatExactInstruction 验证只有未知用途 Chat 的整条精确辅助指令被排除，其他用途和近似文字保留。
func TestClientInputChatExactInstruction(t *testing.T) {
	for _, tc := range []struct {
		name    string
		content any
		text    string
		exact   bool
	}{
		{"string", "follow system prompt", "follow system prompt", true},
		{"text_blocks", clientTextBlocks("follow system ", "prompt"), "follow system prompt", true},
		{"prefix", "请解释 follow system prompt", "请解释 follow system prompt", false},
		{"suffix", "follow system prompt 请解释", "follow system prompt 请解释", false},
		{"case", "Follow system prompt", "Follow system prompt", false},
		{"whitespace", " follow system prompt\n", " follow system prompt\n", false},
		{"fence", "```\nfollow system prompt\n```", "```\nfollow system prompt\n```", false},
		{"attachment", append(clientTextBlocks("follow system prompt"), map[string]any{"type": "image_url", "image_url": map[string]any{"url": "https://synthetic.invalid/image"}}), "follow system prompt[图片]", false},
	} {
		for _, purpose := range []audit.RequestPurpose{audit.PurposeUnknown, audit.PurposeMain, audit.PurposeTurn} {
			t.Run(tc.name+"/"+string(purpose), func(t *testing.T) {
				text, saved := captureClientProtocolInput(t, "chat-completions", purpose, []any{clientTextMessage("历史正文"), clientTextMessage(tc.content)})
				wantSaved := !tc.exact || purpose != audit.PurposeUnknown
				if saved != wantSaved || saved && text != tc.text {
					t.Fatalf("Chat 精确指令边界错误：saved=%t text=%q wantSaved=%t", saved, text, wantSaved)
				}
			})
		}
	}
	if text, saved := captureClientInput(t, audit.PurposeUnknown, []any{clientTextMessage("follow system prompt")}); !saved || text != "follow system prompt" {
		t.Fatal("Chat 专用规则改变了 Anthropic 文本")
	}
	c := openAudit(t, newAuditDirectory(t), 0)
	monitorScope(t, c, "responses-exact-instruction")
	c.CaptureExecution("responses-exact", "responses-exact-instruction", "responses", audit.PurposeUnknown, []byte(`{"input":"follow system prompt"}`))
	waitSaved(t, c, 1)
	if record, err := c.Detail("responses-exact"); err != nil || record.Text != "follow system prompt" {
		t.Fatal("Chat 专用规则改变了 Responses 文本")
	}
}
