// 本文件只提取 Before 请求中的思考声明；不推断模型后缀、宿主默认值或最终上游配置。
package audit

import (
	"encoding/json"
	"errors"
	"strings"
)

// RequestReasoning 保存请求声明的等级、模式和预算；预算缺失与显式零值不同，三者不互相换算。
// 空对象表示本次未指定，Record 中的 nil 仅表示旧记录没有采集这个字段。
type RequestReasoning struct {
	Effort       string `json:"effort,omitempty"`
	Type         string `json:"type,omitempty"`
	BudgetTokens *int64 `json:"budget_tokens,omitempty"`
}

// requestReasoning 从已解析的协议正文提取独立标量副本；畸形或过大的声明返回固定缺口原因。
func requestReasoning(protocol string, request map[string]json.RawMessage) (*RequestReasoning, error) {
	var effort, mode, budget json.RawMessage
	switch protocol {
	case "chat-completions":
		effort = request["reasoning_effort"]
	case "responses", "responses-websocket":
		if raw, present := request["reasoning"]; present {
			fields, err := protocolObject(raw)
			if err != nil {
				return nil, errors.New("unsupported_content")
			}
			effort = fields["effort"]
		}
	case "anthropic-messages":
		if raw, present := request["output_config"]; present {
			fields, err := protocolObject(raw)
			if err != nil {
				return nil, errors.New("unsupported_content")
			}
			effort = fields["effort"]
		}
		if raw, present := request["thinking"]; present {
			fields, err := protocolObject(raw)
			if err != nil {
				return nil, errors.New("unsupported_content")
			}
			mode, budget = fields["type"], fields["budget_tokens"]
		}
	}
	return reasoningFields(effort, mode, budget)
}

// reasoningFields 校验有界的思考标量并返回独立副本；字段缺失允许，畸形值返回固定错误。
func reasoningFields(effort, mode, budget json.RawMessage) (*RequestReasoning, error) {
	result := &RequestReasoning{}
	// 等级和模式最多保留64/32字节，容纳协议标识且不让可选元数据形成无界副本。
	for _, field := range []struct {
		raw   json.RawMessage
		value *string
		limit int
	}{{effort, &result.Effort, 64}, {mode, &result.Type, 32}} {
		if field.raw == nil {
			continue
		}
		// 只保留短标量，不让可选元数据突破队列副本边界。
		if json.Unmarshal(field.raw, field.value) != nil || *field.value == "" {
			return nil, errors.New("unsupported_content")
		}
		if len(*field.value) > field.limit {
			return nil, errors.New("metadata_too_large")
		}
		*field.value = strings.Clone(*field.value)
	}
	if budget != nil {
		var value int64
		if string(budget) == "null" || json.Unmarshal(budget, &value) != nil || value < 0 {
			return nil, errors.New("unsupported_content")
		}
		result.BudgetTokens = &value
	}
	return result, nil
}

// storedReasoning 校验持久对象的精确白名单，拒绝 null、未知字段及大小写别名；旧记录由调用方区分缺失。
func storedReasoning(raw json.RawMessage) (*RequestReasoning, error) {
	fields, err := protocolObject(raw)
	if err != nil {
		return nil, errors.New("invalid_record")
	}
	for name := range fields {
		if name != "effort" && name != "type" && name != "budget_tokens" {
			return nil, errors.New("invalid_record")
		}
	}
	// 复用采集边界的标量校验，保证可写记录在重启后仍可读。
	return reasoningFields(fields["effort"], fields["type"], fields["budget_tokens"])
}
