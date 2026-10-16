//go:build legacy_patched

// 历史私有原帧内容与配置契约；当前明确排除所有 WebSocket，替代用例为 TestOfficialWebsocketExcluded。
// 本文件在真实 WebSocket 接收边界验证末尾输入、配置逐提交生效及禁存字段，不检查私有提取器。
package system

import (
	"encoding/json"
	"fmt"
	"maps"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestResponsesWebsocketContent 验证字符串、结构化尾项、非文本、未知块、空白与歧义事件保持 Responses 规则。
func TestResponsesWebsocketContent(t *testing.T) {
	h := startHostWithConfiguration(t, 1<<30, true, "", true, "")
	h.monitorAll(t)
	conn := dialAuditWebsocket(t, h, "/v1/responses", keyA)
	cases := []struct {
		input, want string
		incomplete  bool
	}{
		{`"WS-STRING"`, "WS-STRING", false},
		{`[{"role":"user","content":"FORBIDDEN_WS_OLD"},{"role":"user","content":"WS-LAST"}]`, "WS-LAST", false},
		{`[{"role":"user","content":[{"type":"input_text","text":"WS-MIX"},{"type":"input_image","image_url":"FORBIDDEN_WS_IMAGE"},{"type":"function_call_output","output":"FORBIDDEN_WS_TOOL_NESTED"},{"type":"input_file","file_id":"FORBIDDEN_WS_FILE"},{"type":"future","payload":"FORBIDDEN_WS_UNKNOWN"},{"type":"input_text","text":"END"}]}]`, "WS-MIX[图片][文件]END", true},
		{`" \t\n "`, " \t\n ", false},
	}
	count := 0
	for round := 0; round < 2; round++ {
		_, _, before := snapshotCounters(t, h)
		for _, tc := range cases {
			sendAuditFrame(t, conn, fmt.Sprintf(`{"type":"response.create","model":"missing","input":%s,"instructions":"FORBIDDEN_WS_INSTRUCTIONS","previous_response_id":"FORBIDDEN_WS_PREVIOUS"}`, tc.input))
			readAuditEvents(t, conn)
			count++
			record := h.waitRecords(t, count)[0]
			code, raw := h.request(t, "GET", api+"/detail?id="+record.ID, adminKey, nil)
			if code != 200 || json.Unmarshal(raw, &record) != nil || record.Text != tc.want || record.ExtractionIncomplete != tc.incomplete || record.Protocol != "responses-websocket" {
				t.Fatal("WebSocket 本次内容与独立规则预期不符")
			}
		}
		for _, input := range []string{`null`, `""`, `[]`, `[{"role":"user","content":"FORBIDDEN_WS_HISTORY"},{"type":"function_call_output","call_id":"FORBIDDEN_WS_CALL","output":"FORBIDDEN_WS_TOOL"}]`, `[{"role":"user","content":"FORBIDDEN_WS_HISTORY"},{"role":"assistant","content":"FORBIDDEN_WS_REPLY"}]`} {
			sendAuditFrame(t, conn, fmt.Sprintf(`{"type":"response.append","model":"missing","input":%s}`, input))
			readAuditEvents(t, conn)
		}
		for index, body := range []string{
			`{"type":"response.steer","model":"missing"}`,
			`{"type":"response.cancel","input":"FORBIDDEN_WS_CANCEL"}`,
			`{"type":"response.interrupt","response_id":"FORBIDDEN_WS_INTERRUPT"}`,
			`{"type":"future.event","input":"FORBIDDEN_WS_UNKNOWN_EVENT"}`,
			`{"type":"response.create","type":"response.cancel","input":"FORBIDDEN_WS_DUPLICATE"}`,
			`{"type":"response.create","model":"missing","input":[`,
		} {
			t.Run(fmt.Sprintf("round-%d-event-%d", round, index), func(t *testing.T) {
				sendAuditFrame(t, conn, body)
				readAuditEvents(t, conn)
			})
		}
		marker := fmt.Sprintf("WS-CONTENT-DRAIN-%d", round)
		sendAuditFrame(t, conn, fmt.Sprintf(`{"type":"response.create","model":"missing","input":%q}`, marker))
		readAuditEvents(t, conn)
		waitSnapshotRecord(t, h, marker)
		count++
		saved, queued, after := snapshotCounters(t, h)
		want := maps.Clone(before)
		want["unsupported_content"] += 2
		want["invalid_json"] += 2
		if saved != int64(count) || queued != 0 || !maps.Equal(want, after) {
			t.Fatal("WebSocket 预期跳过、未知事件或解析缺口计数不符")
		}
	}
	assertChatPrivacy(t, h, "FORBIDDEN_WS_")
}

// TestResponsesWebsocketEmptyTruncatedPrefix 验证1字节上限截断多字节首字符或占位符时仍保留记录，不伪装成真正空输入。
func TestResponsesWebsocketEmptyTruncatedPrefix(t *testing.T) {
	h := startHostWithConfiguration(t, 1<<30, true, "", true, "")
	h.monitorAll(t)
	snapshotMutation(t, h, "/settings", map[string]int{"max_text_bytes": 1})
	conn := dialAuditWebsocket(t, h, "/v1/responses", keyA)
	inputs := []string{`"中"`, `"🙂"`, `[{"role":"user","content":[{"type":"input_image","image_url":"FORBIDDEN_WS_EMPTY_PREFIX_IMAGE"}]}]`, `[{"role":"user","content":[{"type":"input_text","text":"中"},{"type":"future","payload":"FORBIDDEN_WS_EMPTY_PREFIX_UNKNOWN"}]}]`}
	// 占位符以ASCII左括号开头，1字节安全前缀为“[”，而汉字/emoji前缀确实为空。
	prefixes := []string{"", "", "[", ""}
	var ids []string
	for index, input := range inputs {
		sendAuditFrame(t, conn, fmt.Sprintf(`{"type":"response.create","model":"missing","previous_response_id":"synthetic-missing-parent","input":%s}`, input))
		readAuditEvents(t, conn)
		record := h.waitRecords(t, index+1)[0]
		code, raw := h.request(t, "GET", api+"/detail?id="+record.ID, adminKey, nil)
		if code != 200 || json.Unmarshal(raw, &record) != nil || record.Text != prefixes[index] || !record.Truncated || record.ExtractionIncomplete != (index == 3) {
			t.Fatal("UTF-8空前缀被跳过或丢失独立截断/不完整状态")
		}
		ids = append(ids, record.ID)
	}
	sendAuditFrame(t, conn, `{"type":"response.create","model":"missing","input":""}`)
	readAuditEvents(t, conn)
	snapshotMutation(t, h, "/settings", map[string]int{"max_text_bytes": 65536})
	sendAuditFrame(t, conn, `{"type":"response.create","model":"missing","input":"WS-EMPTY-PREFIX-DRAIN"}`)
	readAuditEvents(t, conn)
	waitSnapshotRecord(t, h, "WS-EMPTY-PREFIX-DRAIN")
	saved, queued, gaps := snapshotCounters(t, h)
	if saved != 5 || queued != 0 || gaps["unsupported_content"] != 1 {
		t.Fatal("真正空输入或空截断前缀分类错误")
	}
	assertChatPrivacy(t, h, "FORBIDDEN_WS_")
	h.stop()
	restarted := startHostWithConfiguration(t, 1<<30, true, "", true, "", filepath.Join(h.dir, "audit"))
	for index, id := range ids {
		code, raw := restarted.request(t, "GET", api+"/detail?id="+id, adminKey, nil)
		var record savedRecord
		if code != 200 || json.Unmarshal(raw, &record) != nil || record.Text != prefixes[index] || !record.Truncated {
			t.Fatal("正常重启不能读取空截断前缀记录")
		}
	}
}

// TestResponsesWebsocketConfiguration 验证长连接身份不由消息Key决定，名单、名称、暂停和UTF-8上限无需重连。
func TestResponsesWebsocketConfiguration(t *testing.T) {
	h := startHostWithConfiguration(t, 1<<30, true, "", true, "")
	a := dialAuditWebsocket(t, h, "/v1/responses", keyA)
	b := dialAuditWebsocket(t, h, "/v1/responses", keyB)
	sendAuditFrame(t, a, `{"type":"response.create","model":"missing","input":"FORBIDDEN_WS_UNMONITORED"}`)
	readAuditEvents(t, a)
	h.monitorAll(t)
	for round := 0; round < 2; round++ {
		marker := fmt.Sprintf("WS-CONFIG-A-%d", round)
		sendAuditFrame(t, a, fmt.Sprintf(`{"type":"response.create","model":"missing","input":%q,"api_key":%q}`, marker, keyB))
		readAuditEvents(t, a)
		original := waitSnapshotRecord(t, h, marker)
		markerB := fmt.Sprintf("WS-CONFIG-B-%d", round)
		sendAuditFrame(t, b, fmt.Sprintf(`{"type":"response.create","model":"missing","input":%q,"api_key":%q}`, markerB, keyA))
		readAuditEvents(t, b)
		other := waitSnapshotRecord(t, h, markerB)
		if original.ObjectID == other.ObjectID {
			t.Fatal("消息携带的伪Key改变连接的实际认证身份")
		}
		snapshotMutation(t, h, "/rename", map[string]string{"object_id": original.ObjectID, "name": "WS当前名称"})
		snapshotMutation(t, h, "/remove", map[string]string{"object_id": original.ObjectID})
		sendAuditFrame(t, a, `{"type":"response.create","model":"missing","input":"FORBIDDEN_WS_REMOVED"}`)
		readAuditEvents(t, a)
		snapshotMutation(t, h, "/pause", map[string]bool{"paused": true})
		sendAuditFrame(t, b, `{"type":"response.create","model":"missing","input":"FORBIDDEN_WS_PAUSED"}`)
		readAuditEvents(t, b)
		snapshotMutation(t, h, "/pause", map[string]bool{"paused": false})
		snapshotMutation(t, h, "/monitor", map[string]string{"object_id": original.ObjectID, "name": "WS当前名称"})
		snapshotMutation(t, h, "/settings", map[string]int{"max_text_bytes": 16})
		prefix := fmt.Sprintf("WS-LIMIT-%d-", round) + strings.Repeat("a", 4)
		sendAuditFrame(t, a, fmt.Sprintf(`{"type":"response.create","model":"missing","input":%q}`, prefix+"🙂FORBIDDEN_WS_LIMIT_TAIL"))
		readAuditEvents(t, a)
		limited := waitChatDetail(t, h, prefix)
		if limited.Text != prefix || !limited.Truncated {
			t.Fatal("WebSocket 下一提交没有使用新上限或切断UTF-8字符")
		}
		snapshotMutation(t, h, "/settings", map[string]int{"max_text_bytes": 65536})
		drain := fmt.Sprintf("WS-CONFIG-DRAIN-%d", round)
		sendAuditFrame(t, a, fmt.Sprintf(`{"type":"response.create","model":"missing","input":%q}`, drain))
		readAuditEvents(t, a)
		waitSnapshotRecord(t, h, drain)
		code, raw := h.request(t, "GET", api+"/detail?id="+original.ID, adminKey, nil)
		var old savedRecord
		if code != 200 || json.Unmarshal(raw, &old) != nil || old.Text != marker || old.Name != "WS当前名称" {
			t.Fatal("改名/上限变更追溯改变了历史内容或取消旧记录")
		}
	}
	saved, queued, gaps := snapshotCounters(t, h)
	if saved != 8 || queued != 0 || gaps["unsupported_content"] != 0 {
		t.Fatal("名单暂停/恢复在同连接下一提交不生效")
	}
	assertChatPrivacy(t, h, "FORBIDDEN_WS_")
	h.stop()
	restarted := startHostWithConfiguration(t, 1<<30, true, "", true, "", filepath.Join(h.dir, "audit"))
	records := restarted.waitRecords(t, 8)
	if records[0].Protocol != "responses-websocket" {
		t.Fatal("WebSocket 记录重启被判不兼容")
	}
}

// TestResponsesWebsocketAuthenticatedIdentity 验证原生Key移除拒绝新鉴权，却不重新鉴权已有连接或改变其审计身份。
func TestResponsesWebsocketAuthenticatedIdentity(t *testing.T) {
	upstream, received := auditWebsocketUpstream()
	defer upstream.Close()
	h := startHostWithConfiguration(t, 1<<30, true, upstream.URL+"/v1", true, "")
	h.monitorAll(t)
	conn := dialAuditWebsocket(t, h, "/v1/responses", keyA)
	for round := 0; round < 2; round++ {
		marker := fmt.Sprintf("WS-IDENTITY-BEFORE-%d", round)
		sendAuditFrame(t, conn, fmt.Sprintf(`{"type":"response.create","model":"responses-client-model","input":[{"role":"user","content":%q}]}`, marker))
		readAuditEvents(t, conn)
		awaitAuditUpstream(t, received)
		before := waitSnapshotRecord(t, h, marker)
		lifecycleKeys(t, h, []string{keyB})
		deadline := time.Now().Add(8 * time.Second)
		for {
			code, _ := h.request(t, "GET", "/v1/models", keyA, nil)
			if code == 401 || code == 403 {
				break
			}
			if time.Now().After(deadline) {
				t.Fatal("合成Key移除没有拒绝新的原生鉴权")
			}
			time.Sleep(20 * time.Millisecond)
		}
		afterMarker := fmt.Sprintf("WS-IDENTITY-AFTER-%d", round)
		sendAuditFrame(t, conn, fmt.Sprintf(`{"type":"response.create","model":"responses-client-model","input":[{"role":"user","content":%q}]}`, afterMarker))
		readAuditEvents(t, conn)
		awaitAuditUpstream(t, received)
		after := waitSnapshotRecord(t, h, afterMarker)
		if before.ObjectID != after.ObjectID {
			t.Fatal("已有连接被重新鉴权或更换了审计身份")
		}
		lifecycleKeys(t, h, []string{keyA, keyB})
	}
	assertChatPrivacy(t, h)
}
