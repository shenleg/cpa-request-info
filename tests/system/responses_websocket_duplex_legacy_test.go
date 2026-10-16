//go:build legacy_patched

// 本文件保留私有 duplex 原帧审计；当前 duplex 正常执行但零采集由 TestOfficialWebsocketExcluded 验证。
package system

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"
)

// TestResponsesWebsocketDuplex 验证 create/append/steer 的原始显式输入和失败确认，工具续跑不补回历史。
func TestResponsesWebsocketDuplex(t *testing.T) {
	upstream, received := codexAuditUpstream()
	defer upstream.Close()
	cfg := codexAuditConfiguration(upstream.URL)
	h := startHostWithConfiguration(t, 1<<30, true, "", true, cfg)
	h.monitorAll(t)
	conn := dialAuditWebsocket(t, h, "/v1/responses", keyA)
	for round := 0; round < 2; round++ {
		marker := fmt.Sprintf("WS-DUPLEX-CREATE-%d", round)
		sendAuditFrame(t, conn, fmt.Sprintf(`{"type":"response.create","model":"ws-codex-model","input":[{"role":"user","content":%q}]}`, marker))
		frames := readAuditEvents(t, conn)
		var completed struct {
			Response struct {
				ID string `json:"id"`
			} `json:"response"`
		}
		if json.Unmarshal(frames[len(frames)-1], &completed) != nil || completed.Response.ID == "" {
			t.Fatal("Codex公开完成事件缺失")
		}
		waitSnapshotRecord(t, h, marker)
		select {
		case <-received:
		case <-time.After(3 * time.Second):
			t.Fatal("Codex上游未收到create")
		}
		steer := fmt.Sprintf("WS-DUPLEX-STEER-%d", round)
		sendAuditFrame(t, conn, fmt.Sprintf(`{"type":"response.steer","previous_response_id":%q,"input":%q,"extension":"FORBIDDEN_WS_STEER_EXTENSION"}`, completed.Response.ID, steer))
		event := readAuditSteer(t, conn)
		if !strings.Contains(string(event), "response.steer.failed") {
			t.Fatal("审计修改了上游steering失败确认")
		}
		record := waitSnapshotRecord(t, h, steer)
		if record.Model != "" || record.Protocol != "responses-websocket" {
			t.Fatal("steering审计从执行层猜测模型或错用HTTP协议")
		}
		select {
		case actual := <-received:
			if !strings.Contains(string(actual), steer) || !strings.Contains(string(actual), "FORBIDDEN_WS_STEER_EXTENSION") {
				t.Fatal("审计截断或改写了原生steering业务输入")
			}
		case <-time.After(3 * time.Second):
			t.Fatal("上游未收到原生steer")
		}
		tail := fmt.Sprintf("WS-DUPLEX-APPEND-%d", round)
		sendAuditFrame(t, conn, fmt.Sprintf(`{"type":"response.append","input":[{"role":"user","content":%q}]}`, tail))
		readAuditEvents(t, conn)
		follow := waitSnapshotRecord(t, h, tail)
		if follow.Model != "" {
			t.Fatal("append从继承上下文补猜模型")
		}
		select {
		case <-received:
		case <-time.After(3 * time.Second):
			t.Fatal("上游未收到duplex append")
		}
		sendAuditFrame(t, conn, `{"type":"response.append","input":[{"type":"function_call_output","call_id":"synthetic-tool","output":"FORBIDDEN_WS_DUPLEX_TOOL"}]}`)
		readAuditEvents(t, conn)
		select {
		case <-received:
		case <-time.After(3 * time.Second):
			t.Fatal("上游未收到工具续跑")
		}
	}
	barrier := "WS-DUPLEX-DRAIN"
	sendAuditFrame(t, conn, fmt.Sprintf(`{"type":"response.append","input":[{"role":"user","content":%q}]}`, barrier))
	readAuditEvents(t, conn)
	waitSnapshotRecord(t, h, barrier)
	select {
	case <-received:
	case <-time.After(3 * time.Second):
		t.Fatal("上游未收到duplex屏障")
	}
	saved, queued, gaps := snapshotCounters(t, h)
	if saved != 7 || queued != 0 || gaps["unsupported_content"] != 0 {
		t.Fatal("duplex提交遗漏、重记或误采工具续跑")
	}
	assertChatPrivacy(t, h, "FORBIDDEN_WS_")
}
