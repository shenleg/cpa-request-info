// 本文件通过公开采集与存储接口验证请求声明的思考参数，保留测试目录且不推断模型后缀。
package audit_test

import (
	"bytes"
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/shenleg/cpa-request-info/internal/audit"
)

// reasoningInput 将 fields 原样放入指定协议的合法用户输入；返回完整正文，未知协议使测试失败。
func reasoningInput(t *testing.T, protocol, fields string) []byte {
	t.Helper()
	content := `"input":"reasoning input"`
	switch protocol {
	case "chat-completions", "anthropic-messages":
		content = `"messages":[{"role":"user","content":"reasoning input"}]`
	case "responses":
	default:
		t.Fatalf("未知测试协议：%s", protocol)
	}
	if fields != "" {
		fields = "," + fields
	}
	return []byte(`{"model":"synthetic-model(high)",` + content + fields + `}`)
}

// assertRequestReasoning 核对结构及其 JSON 字段，特别区分缺失预算和零预算；不符终止测试。
func assertRequestReasoning(t *testing.T, got, want *audit.RequestReasoning) {
	t.Helper()
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("思考声明不符：got=%+v want=%+v", got, want)
	}
	raw, err := json.Marshal(got)
	if err != nil {
		t.Fatal(err)
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		t.Fatal(err)
	}
	if want != nil {
		if _, exists := fields["budget_tokens"]; exists != (want.BudgetTokens != nil) {
			t.Fatalf("预算零值与缺失未区分：%s", raw)
		}
	}
}

// TestRequestReasoningProtocols 验证三协议显式参数、组合、零预算和边界字节长度，缺声明时不从模型后缀推断。
func TestRequestReasoningProtocols(t *testing.T) {
	zero, budget, maximum := int64(0), int64(4096), int64(9223372036854775807)
	cases := []struct {
		name, protocol, fields string
		want                   audit.RequestReasoning
	}{
		{"chat_effort", "chat-completions", `"reasoning_effort":"high"`, audit.RequestReasoning{Effort: "high"}},
		{"responses_effort", "responses", `"reasoning":{"effort":"medium"}`, audit.RequestReasoning{Effort: "medium"}},
		{"messages_effort", "anthropic-messages", `"output_config":{"effort":"low"}`, audit.RequestReasoning{Effort: "low"}},
		{"messages_type", "anthropic-messages", `"thinking":{"type":"adaptive"}`, audit.RequestReasoning{Type: "adaptive"}},
		{"messages_budget", "anthropic-messages", `"thinking":{"budget_tokens":4096}`, audit.RequestReasoning{BudgetTokens: &budget}},
		{"messages_all", "anthropic-messages", `"output_config":{"effort":"high"},"thinking":{"type":"enabled","budget_tokens":4096}`, audit.RequestReasoning{Effort: "high", Type: "enabled", BudgetTokens: &budget}},
		{"messages_zero", "anthropic-messages", `"thinking":{"type":"disabled","budget_tokens":0}`, audit.RequestReasoning{Type: "disabled", BudgetTokens: &zero}},
		{"messages_maximum", "anthropic-messages", `"thinking":{"budget_tokens":9223372036854775807}`, audit.RequestReasoning{BudgetTokens: &maximum}},
		{"chat_bounded_future", "chat-completions", `"reasoning_effort":"future-effort"`, audit.RequestReasoning{Effort: "future-effort"}},
		{"chat_effort_boundary", "chat-completions", `"reasoning_effort":"` + strings.Repeat("x", 64) + `"`, audit.RequestReasoning{Effort: strings.Repeat("x", 64)}},
		{"responses_effort_boundary", "responses", `"reasoning":{"effort":"` + strings.Repeat("中", 21) + `x"}`, audit.RequestReasoning{Effort: strings.Repeat("中", 21) + "x"}},
		{"messages_type_boundary", "anthropic-messages", `"thinking":{"type":"` + strings.Repeat("x", 32) + `"}`, audit.RequestReasoning{Type: strings.Repeat("x", 32)}},
		{"chat_missing", "chat-completions", "", audit.RequestReasoning{}},
		{"responses_missing", "responses", "", audit.RequestReasoning{}},
		{"messages_missing", "anthropic-messages", "", audit.RequestReasoning{}},
		{"responses_empty_object", "responses", `"reasoning":{}`, audit.RequestReasoning{}},
		{"messages_empty_objects", "anthropic-messages", `"output_config":{},"thinking":{}`, audit.RequestReasoning{}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := openAudit(t, newAuditDirectory(t), 0)
			monitorScope(t, c, "reasoning-scope")
			c.CaptureExecution("reasoning-record", "reasoning-scope", tc.protocol, audit.PurposeUnknown, reasoningInput(t, tc.protocol, tc.fields))
			waitSaved(t, c, 1)
			detail, err := c.Detail("reasoning-record")
			if err != nil || detail.Model != "synthetic-model(high)" || detail.Text != "reasoning input" {
				t.Fatalf("原始输入元数据丢失：%+v err=%v", detail, err)
			}
			assertRequestReasoning(t, detail.RequestReasoning, &tc.want)
			listed, total, err := c.List(0, 1, audit.Query{})
			if err != nil || total != 1 || len(listed) != 1 {
				t.Fatalf("列表失败：total=%d err=%v", total, err)
			}
			assertRequestReasoning(t, listed[0].RequestReasoning, &tc.want)
			for reason, count := range c.Status().Gaps {
				if count != 0 {
					t.Fatalf("合法声明产生缺口：%s=%d", reason, count)
				}
			}
		})
	}
}

// TestRequestReasoningRejectsInvalidInput 验证非法声明整条不保存，只产生相应缺口，不污染之后的正常输入。
func TestRequestReasoningRejectsInvalidInput(t *testing.T) {
	cases := []struct{ name, protocol, fields, reason string }{}
	for _, value := range []string{`null`, `""`, `1`, `true`, `{}`, `[]`} {
		cases = append(cases,
			struct{ name, protocol, fields, reason string }{"chat_" + value, "chat-completions", `"reasoning_effort":` + value, "unsupported_content"},
			struct{ name, protocol, fields, reason string }{"responses_" + value, "responses", `"reasoning":{"effort":` + value + `}`, "unsupported_content"},
			struct{ name, protocol, fields, reason string }{"messages_effort_" + value, "anthropic-messages", `"output_config":{"effort":` + value + `}`, "unsupported_content"},
			struct{ name, protocol, fields, reason string }{"messages_type_" + value, "anthropic-messages", `"thinking":{"type":` + value + `}`, "unsupported_content"})
	}
	for _, value := range []string{`null`, `-1`, `1.5`, `1.0`, `1e3`, `9223372036854775808`, `"4096"`, `true`, `{}`, `[]`} {
		cases = append(cases, struct{ name, protocol, fields, reason string }{"budget_" + value, "anthropic-messages", `"thinking":{"budget_tokens":` + value + `}`, "unsupported_content"})
	}
	cases = append(cases, []struct{ name, protocol, fields, reason string }{
		{"responses_null", "responses", `"reasoning":null`, "unsupported_content"},
		{"responses_array", "responses", `"reasoning":[]`, "unsupported_content"},
		{"messages_config_null", "anthropic-messages", `"output_config":null`, "unsupported_content"},
		{"messages_config_string", "anthropic-messages", `"output_config":"high"`, "unsupported_content"},
		{"messages_thinking_null", "anthropic-messages", `"thinking":null`, "unsupported_content"},
		{"messages_thinking_array", "anthropic-messages", `"thinking":[]`, "unsupported_content"},
		{"duplicate_chat", "chat-completions", `"reasoning_effort":"high","reasoning_effort":"low"`, "invalid_json"},
		{"duplicate_reasoning", "responses", `"reasoning":{"effort":"high"},"reasoning":{"effort":"low"}`, "invalid_json"},
		{"duplicate_effort", "responses", `"reasoning":{"effort":"high","effort":"low"}`, "unsupported_content"},
		{"duplicate_output_effort", "anthropic-messages", `"output_config":{"effort":"high","effort":"low"}`, "unsupported_content"},
		{"duplicate_type", "anthropic-messages", `"thinking":{"type":"enabled","type":"disabled"}`, "unsupported_content"},
		{"duplicate_budget", "anthropic-messages", `"thinking":{"budget_tokens":0,"budget_tokens":1}`, "unsupported_content"},
		{"chat_effort_65", "chat-completions", `"reasoning_effort":"` + strings.Repeat("x", 65) + `"`, "metadata_too_large"},
		{"responses_effort_bytes", "responses", `"reasoning":{"effort":"` + strings.Repeat("中", 22) + `"}`, "metadata_too_large"},
		{"messages_effort_65", "anthropic-messages", `"output_config":{"effort":"` + strings.Repeat("x", 65) + `"}`, "metadata_too_large"},
		{"messages_type_33", "anthropic-messages", `"thinking":{"type":"` + strings.Repeat("中", 11) + `"}`, "metadata_too_large"},
	}...)
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := openAudit(t, newAuditDirectory(t), 0)
			monitorScope(t, c, "invalid-reasoning-scope")
			c.CaptureExecution("invalid-reasoning", "invalid-reasoning-scope", tc.protocol, audit.PurposeUnknown, reasoningInput(t, tc.protocol, tc.fields))
			c.CaptureExecution("valid-barrier", "invalid-reasoning-scope", "responses", audit.PurposeUnknown, reasoningInput(t, "responses", ""))
			waitSaved(t, c, 1)
			if _, err := c.Detail("invalid-reasoning"); err == nil || err.Error() != "not_found" {
				t.Fatalf("非法声明仍保存：%v", err)
			}
			status := c.Status()
			if status.Gaps[tc.reason] != 1 {
				t.Fatalf("非法声明缺口错误：%+v", status)
			}
			for reason, count := range status.Gaps {
				if reason != tc.reason && count != 0 {
					t.Fatalf("非法声明产生额外缺口：%s=%d", reason, count)
				}
			}
		})
	}
}

// TestRequestReasoningMixedRestart 验证旧缺字段为 nil、新空声明为对象，组合参数及零预算在维护和重启后保持原值与原行。
func TestRequestReasoningMixedRestart(t *testing.T) {
	dir := newAuditDirectory(t)
	c := openAudit(t, dir, 0)
	const scope = "mixed-reasoning-scope"
	objectID := monitorScope(t, c, scope)
	c.Close()
	stamp := time.Now().UTC()
	legacy := []byte(fmt.Sprintf("{ \"text\":\"legacy\", \"id\":\"legacy-reasoning\", \"object_id\":%q, \"received_at\":%q, \"protocol\":\"responses\", \"model\":\"synthetic\", \"truncated\":false }\n", objectID, stamp.Format(time.RFC3339Nano)))
	writeAuditFile(t, dir, "records-000001.jsonl", legacy)
	c = openAudit(t, dir, 0)
	c.CaptureExecution("new-empty", scope, "responses", audit.PurposeUnknown, reasoningInput(t, "responses", ""))
	c.CaptureExecution("new-all", scope, "anthropic-messages", audit.PurposeMain, reasoningInput(t, "anthropic-messages", `"output_config":{"effort":"high"},"thinking":{"type":"enabled","budget_tokens":0}`))
	waitSaved(t, c, 3)
	c.Close()
	original := readAuditFile(t, dir, "records-000001.jsonl")
	if !bytes.HasPrefix(original, legacy) {
		t.Fatal("追加时改写了旧记录")
	}
	expired := bytes.Replace(legacy, []byte("legacy-reasoning"), []byte("expired-reasoning"), 1)
	expired = bytes.Replace(expired, []byte(stamp.Format(time.RFC3339Nano)), []byte(stamp.Add(-8*24*time.Hour).Format(time.RFC3339Nano)), 1)
	writeAuditFile(t, dir, "records-000001.jsonl", append(expired, original...))
	c = openAudit(t, dir, 0)
	zero := int64(0)
	want := map[string]*audit.RequestReasoning{
		"legacy-reasoning": nil,
		"new-empty":        {},
		"new-all":          {Effort: "high", Type: "enabled", BudgetTokens: &zero},
	}
	records, total, err := c.List(0, 10, audit.Query{})
	if err != nil || total != len(want) || len(records) != len(want) {
		t.Fatalf("混合重启记录错误：total=%d err=%v", total, err)
	}
	for _, listed := range records {
		expected, exists := want[listed.ID]
		if !exists {
			t.Fatalf("意外记录：%s", listed.ID)
		}
		assertRequestReasoning(t, listed.RequestReasoning, expected)
		detail, err := c.Detail(listed.ID)
		if err != nil {
			t.Fatal(err)
		}
		assertRequestReasoning(t, detail.RequestReasoning, expected)
		raw, err := json.Marshal(detail)
		if err != nil {
			t.Fatal(err)
		}
		if bytes.Contains(raw, []byte(`"request_reasoning"`)) != (expected != nil) {
			t.Fatalf("旧记录被补字段或新对象被省略：%s", raw)
		}
	}
	c.Close()
	if !bytes.Equal(original, readAuditFile(t, dir, "records-000001.jsonl")) {
		t.Fatal("维护或重启改写幸存记录")
	}
}

// TestRequestReasoningRejectsInvalidStorage 验证持久对象的空值、错型、重复、未知字段和超长声明均明确停采且保留原文件。
func TestRequestReasoningRejectsInvalidStorage(t *testing.T) {
	for _, value := range []string{
		`null`, `[]`, `"high"`, `42`,
		`{"effort":""}`, `{"effort":null}`, `{"effort":1}`, `{"effort":[]}`,
		`{"type":""}`, `{"type":null}`, `{"type":false}`, `{"type":{}}`,
		`{"budget_tokens":null}`, `{"budget_tokens":-1}`, `{"budget_tokens":1.5}`, `{"budget_tokens":1.0}`, `{"budget_tokens":9223372036854775808}`, `{"budget_tokens":"0"}`,
		`{"effort":"high","effort":"low"}`, `{"type":"enabled","type":"disabled"}`, `{"budget_tokens":0,"budget_tokens":1}`,
		`{"unknown":"private-field"}`, `{"Effort":"high"}`, `{"effort":"high","thinking":{}}`,
		`{"effort":"` + strings.Repeat("x", 65) + `"}`, `{"type":"` + strings.Repeat("x", 33) + `"}`,
	} {
		t.Run(value, func(t *testing.T) {
			dir := newAuditDirectory(t)
			c := openAudit(t, dir, 0)
			id := monitorScope(t, c, "storage-reasoning-scope")
			c.Close()
			line := []byte(fmt.Sprintf("{\"id\":\"invalid-reasoning\",\"object_id\":%q,\"received_at\":%q,\"protocol\":\"responses\",\"model\":\"synthetic\",\"text\":\"content\",\"truncated\":false,\"request_reasoning\":%s}\n", id, time.Now().UTC().Format(time.RFC3339Nano), value))
			writeAuditFile(t, dir, "records-000001.jsonl", line)
			c = openAudit(t, dir, 0)
			if status := c.Status(); status.State != "paused" || status.Reason != "storage_error" || status.Saved != 0 {
				t.Fatalf("非法持久思考声明没有拒绝：%+v", status)
			}
			c.Close()
			if !bytes.Equal(line, readAuditFile(t, dir, "records-000001.jsonl")) {
				t.Fatal("非法历史被改写")
			}
		})
	}
}
