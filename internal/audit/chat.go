// 本文件提取选中 Chat 用户消息的允许内容，并提供各协议共用的 UTF-8 前缀组合；不保留协议块。
package audit

import (
	"bytes"
	"encoding/json"
	"strings"
	"unicode/utf8"
)

// chatContent 返回受限正文、截断及不完整标记；空正文仍可能是不完整，不能据空值抹掉缺口。
func chatContent(raw []byte, limit int) (string, bool, bool) {
	raw = bytes.TrimSpace(raw)
	prefix := contentPrefix{limit: limit}
	if bytes.Equal(raw, []byte("null")) {
		return "", false, false
	}
	if len(raw) > 0 && raw[0] == '"' {
		var text string
		if json.Unmarshal(raw, &text) != nil {
			return "", false, true
		}
		prefix.append(text)
		return prefix.text.String(), prefix.truncated, false
	}
	var blocks []json.RawMessage
	if len(raw) == 0 || raw[0] != '[' || json.Unmarshal(raw, &blocks) != nil {
		return "", false, true
	}
	return contentBlocks(blocks, limit, chatPiece)
}

// contentBlocks 共用按序组合与双状态规则；piece 仅识别本协议允许块，不扩展消息选择或工具边界。
func contentBlocks(blocks []json.RawMessage, limit int, piece func([]byte) (string, bool)) (string, bool, bool) {
	prefix := contentPrefix{limit: limit}
	incomplete := false
	for _, rawBlock := range blocks {
		text, supported := piece(rawBlock)
		if !supported {
			incomplete = true
			continue
		}
		// 即使容量已满也继续判定后续块，以免把未知尾部冒充为单纯截断。
		prefix.append(text)
	}
	return prefix.text.String(), prefix.truncated, incomplete
}

func chatPiece(raw []byte) (string, bool) {
	block, err := protocolObject(raw)
	if err != nil {
		return "", false
	}
	var kind string
	if json.Unmarshal(block["type"], &kind) != nil {
		return "", false
	}
	switch kind {
	case "text":
		var text string
		value := block["text"]
		if len(value) == 0 || value[0] != '"' || json.Unmarshal(value, &text) != nil {
			return "", false
		}
		return text, true
	case "image_url", "input_audio", "video_url", "file":
		// 只校验协议载荷容器，不遍历其内部字段，避免附件引用或元数据成为正文。
		payload := bytes.TrimSpace(block[kind])
		if len(payload) == 0 || payload[0] != '{' {
			return "", false
		}
		placeholder := map[string]string{"image_url": "[图片]", "input_audio": "[音频]", "video_url": "[视频]", "file": "[文件]"}
		return placeholder[kind], true
	case "tool_call", "tool_use", "tool_result":
		// 工具结果即使包含文本或图片也属于排除内容，不能递归寻找可记录部分。
		return "", true
	default:
		return "", false
	}
}

type contentPrefix struct {
	text      strings.Builder
	limit     int
	truncated bool
}

func (p *contentPrefix) append(text string) {
	if p.truncated || text == "" {
		return
	}
	end := len(text)
	if remaining := p.limit - p.text.Len(); end > remaining {
		end = remaining
		for end > 0 && !utf8.RuneStart(text[end]) {
			end--
		}
		// 首次越界后不能塞入后续短块，否则保存的就不是原始允许内容的前缀。
		p.truncated = true
	}
	p.text.WriteString(text[:end])
}
