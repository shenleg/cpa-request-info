// 本文件只用请求头和明确路径分类采集用途，不保留原始标记，也不把对话调用当成人工来源证明。
package native

import (
	"encoding/json"
	"strings"

	"github.com/shenleg/cpa-request-info/internal/audit"
)

// requestPurpose 的排除依据先看专用接口和子任务来源；普通对话标记不能覆盖这些独立证据。
func requestPurpose(path string, raw json.RawMessage) audit.RequestPurpose {
	if path == "/v1/responses/compact" || path == "/backend-api/codex/responses/compact" {
		return audit.PurposeSkip
	}
	headers, err := jsonObject(raw)
	if err != nil {
		return audit.PurposeUnknown
	}
	invalidSource := false
	for _, name := range []string{"x-claude-code-agent-id", "x-openai-subagent"} {
		_, present, valid := purposeHeader(headers, name)
		if present && valid {
			return audit.PurposeSkip
		}
		invalidSource = invalidSource || present
	}
	if invalidSource {
		return audit.PurposeUnknown
	}
	claude, hasClaude, validClaude := purposeHeader(headers, "x-claude-code-request-class")
	codex, hasCodex, validCodex := purposeHeader(headers, "x-codex-turn-metadata")
	claudePurpose, codexPurpose := audit.PurposeUnknown, audit.PurposeUnknown
	if hasClaude {
		if !validClaude {
			return audit.PurposeUnknown
		}
		switch claude {
		case "main":
			claudePurpose = audit.PurposeMain
		case "subagent", "workflow", "compaction", "auxiliary":
			claudePurpose = audit.PurposeSkip
		default:
			return audit.PurposeUnknown
		}
	}
	if hasCodex {
		if !validCodex {
			return audit.PurposeUnknown
		}
		metadata, err := jsonObject([]byte(codex))
		if err != nil {
			return audit.PurposeUnknown
		}
		var kind string
		if json.Unmarshal(metadata["request_kind"], &kind) != nil {
			return audit.PurposeUnknown
		}
		switch kind {
		case "turn":
			codexPurpose = audit.PurposeTurn
		case "prewarm", "compaction", "memory":
			codexPurpose = audit.PurposeSkip
		default:
			return audit.PurposeUnknown
		}
	}
	if hasClaude && hasCodex {
		if claudePurpose == audit.PurposeMain && codexPurpose == audit.PurposeTurn {
			return audit.PurposeMain
		}
		if claudePurpose == codexPurpose {
			return claudePurpose
		}
		return audit.PurposeUnknown
	}
	if hasClaude {
		return claudePurpose
	}
	return codexPurpose
}

// purposeHeader 合并同名大小写别名和多值；只有全部值相同且非空时才提供可用证据，不读取无关请求头。
func purposeHeader(headers map[string]json.RawMessage, wanted string) (value string, present, valid bool) {
	valid = true
	for name, raw := range headers {
		if !strings.EqualFold(name, wanted) {
			continue
		}
		present = true
		var values []string
		if json.Unmarshal(raw, &values) != nil || len(values) == 0 {
			valid = false
			continue
		}
		for _, item := range values {
			item = strings.TrimSpace(item)
			if item == "" || value != "" && item != value {
				valid = false
			}
			value = item
		}
	}
	return value, present, valid
}
