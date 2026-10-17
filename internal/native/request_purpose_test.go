// 本文件验证用途头和专用路径的分类，以及辅助请求不破坏发现或修改模型请求。
package native

import (
	"encoding/json"
	"maps"
	"testing"
	"time"

	"github.com/shenleg/cpa-request-info/internal/audit"
)

// TestRequestPurpose 验证明确依据优先，损坏或互斥的可选信号只降低为未知，不把对话轮次冒充主会话。
func TestRequestPurpose(t *testing.T) {
	for _, tc := range []struct {
		name, path, headers string
		want                audit.RequestPurpose
	}{
		{"absent", "/v1/messages", `null`, audit.PurposeUnknown},
		{"unrelated", "/v1/messages", `{"User-Agent":["claude"],"x-claude-code-session-id":["session"],"x-claude-code-context-compacted":["auto"]}`, audit.PurposeUnknown},
		{"main", "/v1/messages", `{"x-claude-code-request-class":["main"]}`, audit.PurposeMain},
		{"same_aliases", "/v1/messages", `{"X-Claude-Code-Request-Class":["main","main"],"x-claude-code-request-class":["main"]}`, audit.PurposeMain},
		{"conflicting_aliases", "/v1/messages", `{"X-Claude-Code-Request-Class":["main"],"x-claude-code-request-class":["auxiliary"]}`, audit.PurposeUnknown},
		{"conflicting_values", "/v1/messages", `{"x-claude-code-request-class":["main","auxiliary"]}`, audit.PurposeUnknown},
		{"unknown_class", "/v1/messages", `{"x-claude-code-request-class":["future"]}`, audit.PurposeUnknown},
		{"bad_class", "/v1/messages", `{"x-claude-code-request-class":42}`, audit.PurposeUnknown},
		{"empty_class", "/v1/messages", `{"x-claude-code-request-class":[]}`, audit.PurposeUnknown},
		{"null_value", "/v1/messages", `{"x-claude-code-request-class":[null]}`, audit.PurposeUnknown},
		{"subagent", "/v1/messages", `{"x-claude-code-request-class":["subagent"]}`, audit.PurposeSkip},
		{"workflow", "/v1/messages", `{"x-claude-code-request-class":["workflow"]}`, audit.PurposeSkip},
		{"compaction", "/v1/messages", `{"x-claude-code-request-class":["compaction"]}`, audit.PurposeSkip},
		{"auxiliary", "/v1/messages", `{"x-claude-code-request-class":["auxiliary"]}`, audit.PurposeSkip},
		{"agent_id", "/v1/messages", `{"X-Claude-Code-Agent-Id":["opaque-agent"]}`, audit.PurposeSkip},
		{"agent_over_main", "/v1/messages", `{"x-claude-code-agent-id":["opaque-agent"],"x-claude-code-request-class":["main"]}`, audit.PurposeSkip},
		{"agent_over_bad_class", "/v1/messages", `{"x-claude-code-agent-id":["opaque-agent"],"x-claude-code-request-class":42}`, audit.PurposeSkip},
		{"empty_agent", "/v1/messages", `{"x-claude-code-agent-id":[" "],"x-claude-code-request-class":["main"]}`, audit.PurposeUnknown},
		{"conflicting_agents", "/v1/messages", `{"x-claude-code-agent-id":["first","second"],"x-claude-code-request-class":["main"]}`, audit.PurposeUnknown},
		{"turn", "/v1/responses", `{"x-codex-turn-metadata":["{\"request_kind\":\"turn\"}"]}`, audit.PurposeTurn},
		{"prewarm", "/v1/responses", `{"x-codex-turn-metadata":["{\"request_kind\":\"prewarm\"}"]}`, audit.PurposeSkip},
		{"codex_compaction", "/v1/responses", `{"x-codex-turn-metadata":["{\"request_kind\":\"compaction\"}"]}`, audit.PurposeSkip},
		{"memory", "/v1/responses", `{"x-codex-turn-metadata":["{\"request_kind\":\"memory\"}"]}`, audit.PurposeSkip},
		{"bad_json", "/v1/responses", `{"x-codex-turn-metadata":["{"]}`, audit.PurposeUnknown},
		{"duplicate_kind", "/v1/responses", `{"x-codex-turn-metadata":["{\"request_kind\":\"turn\",\"request_kind\":\"memory\"}"]}`, audit.PurposeUnknown},
		{"missing_kind", "/v1/responses", `{"x-codex-turn-metadata":["{}"]}`, audit.PurposeUnknown},
		{"unknown_kind", "/v1/responses", `{"x-codex-turn-metadata":["{\"request_kind\":\"future\"}"]}`, audit.PurposeUnknown},
		{"turn_subagent", "/v1/responses", `{"x-codex-turn-metadata":["{\"request_kind\":\"turn\"}"],"x-openai-subagent":["future-label"]}`, audit.PurposeSkip},
		{"both_dialogue", "/v1/responses", `{"x-claude-code-request-class":["main"],"x-codex-turn-metadata":["{\"request_kind\":\"turn\"}"]}`, audit.PurposeMain},
		{"both_auxiliary", "/v1/responses", `{"x-claude-code-request-class":["auxiliary"],"x-codex-turn-metadata":["{\"request_kind\":\"memory\"}"]}`, audit.PurposeSkip},
		{"opposed_purposes", "/v1/responses", `{"x-claude-code-request-class":["main"],"x-codex-turn-metadata":["{\"request_kind\":\"compaction\"}"]}`, audit.PurposeUnknown},
		{"bad_kind_with_main", "/v1/responses", `{"x-claude-code-request-class":["main"],"x-codex-turn-metadata":["{"]}`, audit.PurposeUnknown},
		{"compact_path", "/v1/responses/compact", `{"x-claude-code-request-class":["main"]}`, audit.PurposeSkip},
		{"codex_compact_path", "/backend-api/codex/responses/compact", `{}`, audit.PurposeSkip},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := requestPurpose(tc.path, json.RawMessage(tc.headers)); got != tc.want {
				t.Fatalf("purpose=%q want=%q", got, tc.want)
			}
		})
	}
}

// TestPurposeDiscoveryAndRecords 验证辅助首次发现、监控后跳过和列表/详情用途一致，且 Before/After 不返回请求修改。
func TestPurposeDiscoveryAndRecords(t *testing.T) {
	a, _ := newTestApp(t)
	input := testInterception("/v1/messages", "claude")
	input["Headers"] = map[string][]string{"x-claude-code-request-class": {"auxiliary"}}
	result, err := handleInterception(t, a, input)
	raw, _ := json.Marshal(result)
	if err != nil || string(raw) != "{}" {
		t.Fatal("辅助回调改变了请求")
	}
	ids, _ := a.observed.snapshot()
	if len(ids) != 1 || a.collector.Status().Saved != 0 {
		t.Fatal("辅助首次到达必须发现但不保存正文")
	}
	var objectID string
	for id := range ids {
		objectID = id
	}
	selection, _ := json.Marshal(map[string]string{"object_id": objectID, "name": "用途测试"})
	if code, _ := managementResult(t, a, "POST", "/monitor", selection, nil); code != 200 {
		t.Fatal("不能选择已发现辅助调用的对象")
	}
	before := maps.Clone(a.collector.Status().Gaps)
	input["RequestID"] = "auxiliary-after-monitor"
	if _, err := handleInterception(t, a, input); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		id, path, format string
		headers          map[string][]string
	}{
		{"purpose-main", "/v1/messages", "claude", map[string][]string{"x-claude-code-request-class": {"main"}}},
		{"purpose-turn", "/v1/responses", "openai-response", map[string][]string{"x-codex-turn-metadata": {`{"request_kind":"turn"}`}}},
		{"purpose-unknown", "/v1/messages", "claude", map[string][]string{}},
	} {
		request := testInterception(tc.path, tc.format)
		request["RequestID"], request["Headers"] = tc.id, tc.headers
		// 即使正文带可识别的用途元数据，首版也只依据请求头与专用路径。
		request["Body"] = []byte(`{"model":"execution-model","client_metadata":{"x-codex-turn-metadata":"{\"request_kind\":\"memory\"}"},"messages":[{"role":"user","content":"native purpose text"}],"input":"native purpose text"}`)
		if _, err := handleInterception(t, a, request); err != nil {
			t.Fatal(err)
		}
	}
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := a.collector.Detail("purpose-unknown"); err == nil && a.collector.Status().Queued == 0 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	status := a.collector.Status()
	if status.Saved != 3 || status.Queued != 0 || !maps.Equal(before, status.Gaps) {
		t.Fatalf("辅助跳过或已保存数量不符: saved=%d queued=%d", status.Saved, status.Queued)
	}
	code, list := managementResult(t, a, "POST", "/records/search", []byte(`{}`), nil)
	if code != 200 || list["total"] != float64(3) {
		t.Fatal("用途记录列表不符")
	}
	want := map[string]string{"purpose-main": "main", "purpose-turn": "turn", "purpose-unknown": "unknown"}
	for _, item := range list["records"].([]any) {
		record := item.(map[string]any)
		id := record["id"].(string)
		code, detail := managementResult(t, a, "GET", "/detail", nil, map[string][]string{"id": {id}})
		if code != 200 || record["purpose"] != want[id] || detail["purpose"] != want[id] {
			t.Fatalf("列表/详情用途不符: id=%s", id)
		}
	}
	result, err = a.Handle("request.intercept_after", nil)
	raw, _ = json.Marshal(result)
	if err != nil || string(raw) != "{}" {
		t.Fatal("After 改变了模型请求")
	}
}
