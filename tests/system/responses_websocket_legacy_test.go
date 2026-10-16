//go:build legacy_patched

// 本文件保留私有 ordinary WebSocket 原帧审计；当前正常执行但零采集由 TestOfficialWebsocketExcluded 验证。
package system

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"
)

// TestResponsesWebsocketSubmissions 验证两个正式入口同连接的重复提交独立记录，握手不形成输入记录。
func TestResponsesWebsocketSubmissions(t *testing.T) {
	upstream, received := auditWebsocketUpstream()
	defer upstream.Close()
	h := startHostWithConfiguration(t, 1<<30, true, upstream.URL+"/v1", true, "")
	h.monitorAll(t)
	for route, path := range []string{"/v1/responses", "/backend-api/codex/responses"} {
		conn := dialAuditWebsocket(t, h, path, keyA)
		marker := fmt.Sprintf("WS-SUBMISSION-%d", route)
		var ids []string
		for repeat := 0; repeat < 2; repeat++ {
			sendAuditFrame(t, conn, fmt.Sprintf(`{"type":"response.create","model":"responses-client-model","input":[{"role":"user","content":%q}],"event_id":"FORBIDDEN_WS_CLIENT_ID","session_id":"FORBIDDEN_WS_SESSION","api_key":%q}`, marker, keyB))
			frames := readAuditEvents(t, conn)
			if !strings.Contains(string(frames[len(frames)-1]), "response.completed") {
				t.Fatal("宿主原有 WebSocket 模型提交未完成")
			}
			select {
			case <-received:
			case <-time.After(3 * time.Second):
				t.Fatal("可控上游未收到本轮 WebSocket 请求")
			}
			records := h.waitRecords(t, route*2+repeat+1)
			record := records[0]
			code, raw := h.request(t, "GET", api+"/detail?id="+record.ID, adminKey, nil)
			if code != 200 || json.Unmarshal(raw, &record) != nil || record.Text != marker || record.Protocol != "responses-websocket" || record.Model != "responses-client-model" {
				t.Fatal("WebSocket 本次显式提交未作为独立原协议记录保存")
			}
			ids = append(ids, record.ID)
		}
		if ids[0] == ids[1] {
			t.Fatal("重复客户端提交复用了宿主提交标识")
		}
		_ = conn.Close()
	}
	saved, queued, gaps := snapshotCounters(t, h)
	if saved != 4 || queued != 0 || gaps["invalid_json"] != 0 {
		t.Fatal("握手/重复执行改变消息级记录数")
	}
	assertChatPrivacy(t, h, "FORBIDDEN_WS_")
}
