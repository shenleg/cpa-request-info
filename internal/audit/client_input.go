// 本文件在截断前识别已知客户端辅助输入；只处理审计副本，不改变模型请求或从历史提取正文。
package audit

import (
	"bytes"
	"encoding/json"
	"io"
	"regexp"
	"strings"
)

const suggestionPrompt = "[SUGGESTION MODE: Suggest what the user might naturally type next into Claude Code.]"
const actionPrompt = "Describe your most recent action in 3-5 words using present tense (-ing). Name the file or function, not the branch. Do not use tools."
const titlePrompt = "Write the title in the predominant language of the session — a stray word or code token in another language doesn't change it, and neither does the English of these instructions."

// anthropicUserContent 提取已确认的末尾 user；仅 unknown 用途按整请求辅助模板判断，避免误删明确对话中的模板引用。
// messages 为协议消息，purpose 为有限用途，limit 为正文上限；返回正文、截断、不完整及正常跳过标记，无错误或存储操作。
func anthropicUserContent(messages []json.RawMessage, purpose RequestPurpose, limit int) (text string, truncated, incomplete, skip bool) {
	last, _ := protocolObject(messages[len(messages)-1])
	if purpose == PurposeUnknown {
		if value, ok := clientMessageText(last["content"]); ok && clientAuxiliary(value) {
			return "", false, false, true
		}
		if clientSplitTranscript(messages) {
			return "", false, false, true
		}
	}
	text, truncated, incomplete = anthropicClientContent(last["content"], limit)
	return text, truncated, incomplete, false
}

// clientMessageText 只组合纯 text 消息以识别外层辅助结构，不读取工具、未知块或附件内的任何文本。
func clientMessageText(raw []byte) (string, bool) {
	raw = bytes.TrimSpace(raw)
	var text string
	if len(raw) > 0 && raw[0] == '"' {
		err := json.Unmarshal(raw, &text)
		return text, err == nil
	}
	var blocks []json.RawMessage
	if json.Unmarshal(raw, &blocks) != nil {
		return "", false
	}
	var joined strings.Builder
	for _, rawBlock := range blocks {
		block, err := protocolObject(rawBlock)
		var kind string
		value := bytes.TrimSpace(block["text"])
		if err != nil || json.Unmarshal(block["type"], &kind) != nil || kind != "text" || len(value) == 0 || value[0] != '"' || json.Unmarshal(value, &text) != nil {
			return "", false
		}
		joined.WriteString(text)
	}
	return joined.String(), true
}

// clientAuxiliary 只匹配已核对的模板及外壳；普通引用或孤立标签不代表辅助请求。
func clientAuxiliary(text string) bool {
	if strings.HasPrefix(text, suggestionPrompt+"\n\nFIRST: Look at the user's recent messages and original request.\n\n") &&
		strings.Contains(text, "\n\nYour job is to predict what THEY would type - not what you think they should do.\n") &&
		strings.Contains(text, "\nFormat: 2-12 words, match the user's style. Or nothing.\n") &&
		strings.HasSuffix(text, "\n\nReply with ONLY the suggestion, no quotes or explanation.") {
		return true
	}
	if strings.HasPrefix(text, actionPrompt) && strings.Contains(text, "\nGood: ") && strings.Contains(text, "\nBad (") {
		return true
	}
	if strings.HasPrefix(text, "<session>\n") {
		if end := strings.LastIndex(text, "\n</session>\n\n"); end >= 0 {
			instruction := text[end+len("\n</session>\n\n"):]
			if instruction == titlePrompt {
				return true
			}
			const prefix = "Write the title in "
			const suffix = ". Keep technical terms and code identifiers in their original form."
			if strings.HasPrefix(instruction, prefix) && strings.HasSuffix(instruction, suffix) {
				language := strings.TrimSuffix(strings.TrimPrefix(instruction, prefix), suffix)
				return language != "" && !strings.ContainsAny(language, "\r\n")
			}
		}
	}
	if strings.HasPrefix(text, "<transcript>\n") {
		if end := strings.LastIndex(text, "</transcript>"); end >= 0 {
			return clientTranscript(text[len("<transcript>\n"):end])
		}
	}
	return false
}

// clientTranscript 校验客户端的连续 JSON 转录条目；只判断格式，不从中提取要保存的用户原话。
func clientTranscript(text string) bool {
	decoder := json.NewDecoder(strings.NewReader(text))
	conversation := false
	for {
		var raw json.RawMessage
		err := decoder.Decode(&raw)
		if err == io.EOF {
			return conversation
		}
		if err != nil {
			return false
		}
		entry, err := protocolObject(raw)
		if err != nil || len(entry) == 0 {
			return false
		}
		for key := range entry {
			if key == "user" || key == "assistant" {
				conversation = true
			}
		}
	}
}

// clientSplitTranscript 仅识别独立起止消息及其间纯文本构成的末尾转录，不回溯到 assistant 之前。
func clientSplitTranscript(messages []json.RawMessage) bool {
	end := len(messages) - 1
	get := func(index int) (string, bool) {
		message, err := protocolObject(messages[index])
		var role string
		if err != nil || json.Unmarshal(message["role"], &role) != nil || role != "user" {
			return "", false
		}
		return clientMessageText(message["content"])
	}
	last, ok := get(end)
	if !ok {
		return false
	}
	if !strings.HasPrefix(last, "</transcript>\n") && last != "</transcript>" {
		// 独立闭合后的新 user 只有匹配已核对的分类指令，才属于该辅助请求。
		if !clientClassifierSuffix(last) {
			return false
		}
		end--
		if end < 0 {
			return false
		}
		last, ok = get(end)
		if !ok || strings.TrimSpace(last) != "</transcript>" {
			return false
		}
	}
	var pieces []string
	for i := end - 1; i >= 0; i-- {
		value, ok := get(i)
		if !ok {
			return false
		}
		if strings.TrimSpace(value) == "<transcript>" {
			var joined strings.Builder
			for j := len(pieces) - 1; j >= 0; j-- {
				joined.WriteString(pieces[j])
			}
			return clientTranscript(joined.String())
		}
		pieces = append(pieces, value)
	}
	return false
}

// clientClassifierSuffix 仅接受已核对的权限分类尾指令，防止把历史转录之后的新用户消息误判为辅助输入。
func clientClassifierSuffix(text string) bool {
	switch strings.TrimSpace(text) {
	case `Err on the side of blocking. Your ENTIRE response MUST begin with <block>. Do NOT output any analysis, reasoning, or commentary before <block>. No "Looking at..." or similar preamble.`,
		`Err on the side of blocking. Stage 1 does NOT apply user intent or ALLOW exceptions — stage 2 will handle those. Judge the action by its full effect — what it runs, sends, publishes, or enables — not its surface form. Block if ANY rule could apply. Your ENTIRE response MUST begin with <block>. Do NOT output any analysis, reasoning, or commentary before <block>. No "Looking at..." or similar preamble.`,
		`Review the classification process and follow it carefully, making sure you deny actions that should be blocked. As a reminder, explicit (not suggestive or implicit) user confirmation is required to override blocks. Use <thinking> before responding with <block>. Think longer on ambiguous or borderline actions; keep reasoning brief for clear-cut ones.`,
		"Stage 1 does NOT apply user intent or ALLOW exceptions — stage 2 will handle those.\nRespond with <severity>N</severity> ONLY. Grade HARM ONLY — do NOT reduce for user intent. No other text.",
		"Review the classification process and follow it carefully. As a reminder, explicit (not suggestive or implicit) user confirmation is required to override blocks.\nUse <thinking> first, then respond with <severity>N</severity>, plus <category>Exact BLOCK Rule Name</category> when the action matches a BLOCK rule (see Output Format). No other text. Think longer on ambiguous cases; keep reasoning brief for clear-cut ones.":
		return true
	default:
		return false
	}
}

type clientTextState struct {
	fence       byte
	fenceLength int
}

// anthropicClientContent 保留协议块边界排除独立技能，再组合相邻文本；未识别内容沿用原白名单与双状态。
func anthropicClientContent(raw []byte, limit int) (string, bool, bool) {
	raw = bytes.TrimSpace(raw)
	if len(raw) > 0 && raw[0] == '"' {
		var text string
		if json.Unmarshal(raw, &text) != nil {
			return "", false, true
		}
		prefix := contentPrefix{limit: limit}
		prefix.append(clientCleanText([]string{text}, &clientTextState{}))
		return prefix.text.String(), prefix.truncated, false
	}
	var blocks []json.RawMessage
	if len(raw) == 0 || raw[0] != '[' || json.Unmarshal(raw, &blocks) != nil {
		return anthropicContent(raw, limit)
	}
	prefix := contentPrefix{limit: limit}
	incomplete := false
	var run []string
	state := clientTextState{}
	flush := func() {
		prefix.append(clientCleanText(run, &state))
		run = nil
	}
	for _, rawBlock := range blocks {
		block, err := protocolObject(rawBlock)
		var kind string
		var text string
		value := bytes.TrimSpace(block["text"])
		if err == nil && json.Unmarshal(block["type"], &kind) == nil && kind == "text" && len(value) > 0 && value[0] == '"' && json.Unmarshal(value, &text) == nil {
			run = append(run, text)
			continue
		}
		flush()
		text, supported := anthropicPiece(rawBlock)
		if !supported {
			incomplete = true
		}
		prefix.append(text)
	}
	flush()
	return prefix.text.String(), prefix.truncated, incomplete
}

// clientCleanText 只移除行边界或独立 text 块中的成对客户端说明，正文空白和代码围栏保持原样。
func clientCleanText(parts []string, state *clientTextState) string {
	text := strings.Join(parts, "")
	boundaries := map[int]bool{0: true, len(text): true}
	skills := make(map[int]int)
	offset := 0
	for _, part := range parts {
		boundaries[offset] = true
		if clientSkill(part) {
			skills[offset] = offset + len(part)
		}
		offset += len(part)
	}
	var clean strings.Builder
	kept := 0
	for i := 0; i < len(text); {
		boundary := boundaries[i] || i == 0 || text[i-1] == '\n'
		if boundary {
			lineEnd := strings.IndexByte(text[i:], '\n')
			if lineEnd < 0 {
				lineEnd = len(text) - i
			}
			line := text[i : i+lineEnd]
			trimmed := strings.TrimLeft(line, " ")
			if len(line)-len(trimmed) <= 3 && len(trimmed) >= 3 && (trimmed[0] == '`' || trimmed[0] == '~') {
				n := 0
				for n < len(trimmed) && trimmed[n] == trimmed[0] {
					n++
				}
				if n >= 3 {
					if state.fence == 0 {
						state.fence, state.fenceLength = trimmed[0], n
					} else if state.fence == trimmed[0] && n >= state.fenceLength && strings.TrimSpace(trimmed[n:]) == "" {
						state.fence = 0
					}
					i += lineEnd
					if i < len(text) {
						i++
					}
					continue
				}
			}
		}
		if state.fence == 0 && boundary {
			end := skills[i]
			if end == 0 && text[i] == '<' {
				end = clientContextEnd(text, i, boundaries)
			}
			if end > i {
				clean.WriteString(text[kept:i])
				i, kept = end, end
				continue
			}
		}
		i++
	}
	if kept == 0 {
		return text
	}
	clean.WriteString(text[kept:])
	return clean.String()
}

// 客户端提醒标识为16至32位小写十六进制；闭合标签保留同一标识，不接受任意属性或泛XML。
var clientContextOpening = regexp.MustCompile(`^<(system-reminder(?: id="[0-9a-f]{16,32}")?|ide_opened_file|ide_selection)>\n`)

// clientContextEnd 返回独立成对客户端块的结束位置；嵌套须完整闭合，缺少边界或结束标签返回0。
func clientContextEnd(text string, start int, boundaries map[int]bool) int {
	opening := clientContextOpening.FindStringSubmatch(text[start:])
	if opening == nil {
		return 0
	}
	stack := []string{opening[1]}
	for position := start + len(opening[0]); position < len(text); {
		index := strings.IndexByte(text[position:], '<')
		if index < 0 {
			return 0
		}
		index += position
		position = index + 1
		if !boundaries[index] && text[index-1] != '\n' {
			continue
		}
		closing := "</" + stack[len(stack)-1] + ">"
		if strings.HasPrefix(text[index:], closing) {
			end := index + len(closing)
			if boundaries[end] || text[end] == '\n' {
				stack = stack[:len(stack)-1]
				if len(stack) == 0 {
					return end
				}
				position = end
			}
		} else if nested := clientContextOpening.FindStringSubmatch(text[index:]); nested != nil {
			stack = append(stack, nested[1])
			position = index + len(nested[0])
		}
	}
	return 0
}

func clientSkill(text string) bool {
	const header = "Base directory for this skill: "
	if !strings.HasPrefix(text, header) {
		return false
	}
	path, _, ok := strings.Cut(text[len(header):], "\n\n")
	return ok && path != "" && !strings.ContainsAny(path, "\r\n")
}
