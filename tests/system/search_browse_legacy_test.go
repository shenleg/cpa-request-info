//go:build legacy_patched

// 本文件保留四协议组合查询的旧契约；当前 HTTP 查询和 WebSocket 排除分别见 TestSearchProtocols 与 TestOfficialWebsocketExcluded。
package system

import (
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"testing"
	"time"
)

// TestLegacySearchProtocols 验证四种已支持提交共用搜索、请求模型和时间条件，不误匹配实际上游模型。
func TestLegacySearchProtocols(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"id":"synthetic-search","choices":[{"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}]}`)
	}))
	defer upstream.Close()
	h := startHostWithManagementAuth(t, 1<<30, true, upstream.URL+"/v1", true)
	h.monitorAll(t)
	first := searchSubmit(t, h, keyA, "responses-client-model", "SEARCH-PROTOCOL-CHAT")
	for _, fixture := range []struct{ path, body, marker string }{
		{"/v1/responses", `{"model":"responses-client-model","input":"SEARCH-PROTOCOL-RESPONSES"}`, "SEARCH-PROTOCOL-RESPONSES"},
		{"/v1/messages", `{"model":"responses-client-model","max_tokens":8,"messages":[{"role":"user","content":"SEARCH-PROTOCOL-MESSAGES"}]}`, "SEARCH-PROTOCOL-MESSAGES"},
	} {
		h.request(t, "POST", fixture.path, keyA, []byte(fixture.body))
		waitChatDetail(t, h, fixture.marker)
	}
	conn := dialAuditWebsocket(t, h, "/v1/responses", keyA)
	sendAuditFrame(t, conn, `{"type":"response.create","generate":false,"model":"responses-client-model","input":[{"role":"user","content":"SEARCH-PROTOCOL-WEBSOCKET"}]}`)
	last := waitChatDetail(t, h, "SEARCH-PROTOCOL-WEBSOCKET")
	page := searchRecords(t, h, url.Values{"q": {"search-protocol-"}, "object_id": {first.ObjectID}, "model": {"responses-client-model"}, "from": {first.ReceivedAt.Format(time.RFC3339Nano)}, "to": {last.ReceivedAt.Format(time.RFC3339Nano)}})
	protocols := make(map[string]bool)
	for _, record := range page.Records {
		protocols[record.Protocol] = true
	}
	if page.Total != 4 || len(page.Records) != 4 || !reflect.DeepEqual(protocols, map[string]bool{"chat-completions": true, "responses": true, "anthropic-messages": true, "responses-websocket": true}) {
		t.Fatal("四协议未共享组合查询")
	}
	if page := searchRecords(t, h, url.Values{"model": {"audit-test-model"}}); page.Total != 0 {
		t.Fatal("搜索错误采用上游实际模型")
	}
	for round := 0; round < 2; round++ {
		marker := fmt.Sprintf("SEARCH-REPEAT-%d", round)
		r := searchSubmit(t, h, keyB, "client-repeat", marker)
		page := searchRecords(t, h, url.Values{"q": {marker}})
		if page.Total != 1 || page.Records[0].ID != r.ID {
			t.Fatal("本轮搜索误用历史结果")
		}
	}
}
