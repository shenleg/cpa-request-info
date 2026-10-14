// 本文件提取 Anthropic 末尾 user 的新内容；工具结果及文档内部内容不会被递归提取。
package audit

import (
	"bytes"
	"encoding/json"
)

// anthropicContent 只组合本协议允许块，返回受限 UTF-8 正文、截断和不完整状态。
func anthropicContent(raw []byte, limit int) (string, bool, bool) {
	raw = bytes.TrimSpace(raw)
	if bytes.Equal(raw, []byte("null")) || (len(raw) > 0 && raw[0] == '"') {
		return chatContent(raw, limit)
	}
	var blocks []json.RawMessage
	if len(raw) == 0 || raw[0] != '[' || json.Unmarshal(raw, &blocks) != nil {
		return "", false, true
	}
	return contentBlocks(blocks, limit, anthropicPiece)
}

func anthropicPiece(raw []byte) (string, bool) {
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
		if len(value) > 0 && value[0] == '"' && json.Unmarshal(value, &text) == nil {
			return text, true
		}
	case "image", "document":
		// 来源只判定容器及协议标识；文档 text/content 仍是附件，不等于用户的普通 text 块。
		source, err := protocolObject(block["source"])
		if err != nil {
			return "", false
		}
		var sourceType string
		if json.Unmarshal(source["type"], &sourceType) != nil {
			return "", false
		}
		supported := false
		switch sourceType {
		case "base64", "text":
			supported = (sourceType == "base64" || kind == "document") && hasNonEmptyStringField(source, "data")
		case "url":
			supported = hasNonEmptyStringField(source, "url")
		case "file":
			supported = hasNonEmptyStringField(source, "file_id")
		case "content":
			content := bytes.TrimSpace(source["content"])
			supported = kind == "document" && len(content) > 0 && (content[0] == '"' || content[0] == '[')
		}
		if supported {
			if kind == "image" {
				return "[图片]", true
			}
			return "[文件]", true
		}
	case "tool_result", "tool_use", "server_tool_use", "thinking", "redacted_thinking":
		// 不因 user role 或工具内嵌图片/文件而扩大新用户输入，亦不校验或遍历被排除的载荷。
		return "", true
	}
	return "", false
}
