// 本文件只判断 Responses 本次显式 input；不查询 previous_response_id 或宿主会话上下文。
package audit

import (
	"bytes"
	"encoding/json"
)

// responsesContent 返回允许正文的 UTF-8 前缀与两个独立状态；无法安全判断时不保存原始输入。
func responsesContent(raw []byte, limit int) (string, bool, bool) {
	raw = bytes.TrimSpace(raw)
	if len(raw) > 0 && raw[0] == '"' {
		return chatContent(raw, limit)
	}
	if bytes.Equal(raw, []byte("null")) {
		return "", false, false
	}
	var items []json.RawMessage
	if len(raw) == 0 || raw[0] != '[' || json.Unmarshal(raw, &items) != nil {
		return "", false, true
	}
	if len(items) == 0 {
		return "", false, false
	}
	// 只检查末尾项；前面的 user 即使客户端显式附带，也不因工具续跑或未知尾部而被回找。
	last, err := protocolObject(items[len(items)-1])
	if err != nil {
		return "", false, true
	}
	if rawType, exists := last["type"]; exists {
		var kind string
		if json.Unmarshal(rawType, &kind) != nil {
			return "", false, true
		}
		if responsesExcludedItem(kind) {
			return "", false, false
		}
		if kind != "message" {
			return "", false, true
		}
	}
	var role string
	if json.Unmarshal(last["role"], &role) != nil {
		return "", false, true
	}
	switch role {
	case "assistant", "system", "developer":
		return "", false, false
	case "user":
		return responsesMessageContent(last["content"], limit)
	default:
		return "", false, true
	}
}

func responsesMessageContent(raw []byte, limit int) (string, bool, bool) {
	raw = bytes.TrimSpace(raw)
	if bytes.Equal(raw, []byte("null")) || (len(raw) > 0 && raw[0] == '"') {
		return chatContent(raw, limit)
	}
	var blocks []json.RawMessage
	if len(raw) == 0 || raw[0] != '[' || json.Unmarshal(raw, &blocks) != nil {
		return "", false, true
	}
	return contentBlocks(blocks, limit, responsesPiece)
}

func responsesPiece(raw []byte) (string, bool) {
	block, err := protocolObject(raw)
	if err != nil {
		return "", false
	}
	var kind string
	if json.Unmarshal(block["type"], &kind) != nil {
		return "", false
	}
	if responsesExcludedItem(kind) {
		// 工具结果整体排除，不能遍历内部的 text/image 或把 output 当作用户正文。
		return "", true
	}
	switch kind {
	case "input_text":
		var text string
		value := block["text"]
		if len(value) > 0 && value[0] == '"' && json.Unmarshal(value, &text) == nil {
			return text, true
		}
	case "input_image":
		if responsesPayloadString(block, "image_url", "file_id") {
			return "[图片]", true
		}
	case "input_file":
		if responsesPayloadString(block, "file_data", "file_url", "file_id") {
			return "[文件]", true
		}
	case "input_audio":
		// 固定宿主接受嵌套和扁平音频字节；只识别容器，不把 data/format 转成正文。
		audio, err := protocolObject(block["input_audio"])
		if (err == nil && responsesPayloadString(audio, "data")) || responsesPayloadString(block, "data") {
			return "[音频]", true
		}
	case "input_video":
		video, err := protocolObject(block["video_url"])
		if responsesPayloadString(block, "video_url") || (err == nil && responsesPayloadString(video, "url")) {
			return "[视频]", true
		}
	}
	return "", false
}

// responsesPayloadString 只判定附件来源是非空字符串；返回值不携带 URL、引用标识或载荷。
func responsesPayloadString(block map[string]json.RawMessage, fields ...string) bool {
	for _, field := range fields {
		value := block[field]
		var text string
		if len(value) > 0 && value[0] == '"' && json.Unmarshal(value, &text) == nil && text != "" {
			return true
		}
	}
	return false
}

func responsesExcludedItem(kind string) bool {
	switch kind {
	case "function_call", "function_call_output", "custom_tool_call", "custom_tool_call_output", "computer_call", "computer_call_output", "web_search_call", "file_search_call", "code_interpreter_call", "image_generation_call", "local_shell_call", "local_shell_call_output", "shell_call", "shell_call_output", "apply_patch_call", "apply_patch_call_output", "mcp_call", "mcp_list_tools", "mcp_approval_request", "mcp_approval_response", "reasoning", "compaction":
		return true
	default:
		return false
	}
}
