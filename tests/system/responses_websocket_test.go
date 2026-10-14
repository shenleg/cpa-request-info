// 本文件通过真实 WebSocket 提交和受保护管理查询验证消息级审计，不访问 handler 私有状态。
package system

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

// dialAuditWebsocket 在宿主正式入口按 key 建立真实连接；返回客户端，升级失败终止测试，清理只关闭连接。
func dialAuditWebsocket(t *testing.T, h *instance, path, key string) *websocket.Conn {
	t.Helper()
	header := http.Header{}
	if key != "" {
		header.Set("Authorization", "Bearer "+key)
	}
	conn, response, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(h.base, "http")+path, header)
	if response != nil {
		_ = response.Body.Close()
	}
	if err != nil {
		t.Fatal("合成 WebSocket 升级失败")
	}
	t.Cleanup(func() { _ = conn.Close() })
	return conn
}

// sendAuditFrame 发送原始文本帧；body 不被改写，失败终止测试，无返回值。
func sendAuditFrame(t *testing.T, conn *websocket.Conn, body string) {
	t.Helper()
	if err := conn.WriteMessage(websocket.TextMessage, []byte(body)); err != nil {
		t.Fatal("WebSocket 帧发送失败")
	}
}

// readAuditEvents 读取至本次完成或错误事件；返回公开帧，超时/读错终止测试，不保存帧。
func readAuditEvents(t *testing.T, conn *websocket.Conn) [][]byte {
	t.Helper()
	if err := conn.SetReadDeadline(time.Now().Add(8 * time.Second)); err != nil {
		t.Fatal("测试读取期限设置失败")
	}
	var frames [][]byte
	for {
		_, body, err := conn.ReadMessage()
		if err != nil {
			if closeError, ok := err.(*websocket.CloseError); ok {
				t.Fatalf("WebSocket 公开事件读取失败: close-code=%d", closeError.Code)
			}
			t.Fatal("WebSocket 公开事件读取失败")
		}
		frames = append(frames, body)
		var event struct {
			Type string `json:"type"`
		}
		if json.Unmarshal(body, &event) != nil {
			t.Fatal("WebSocket 公开事件不是合法 JSON")
		}
		if event.Type == "response.completed" || event.Type == "response.incomplete" || event.Type == "response.failed" || event.Type == "error" {
			return frames
		}
	}
}

// auditWebsocketUpstream 提供本地 Chat SSE 回复及实际请求通道；返回服务器，调用者关闭，读取失败返回 HTTP 错误。
func auditWebsocketUpstream() (*httptest.Server, <-chan []byte) {
	received := make(chan []byte, 64)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			http.Error(w, "synthetic read failure", 400)
			return
		}
		received <- body
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "data: {\"id\":\"synthetic-ws-reply\",\"created\":1,\"object\":\"chat.completion.chunk\",\"model\":\"audit-test-model\",\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\",\"content\":\"ok\"},\"finish_reason\":null}]}\n\ndata: {\"id\":\"synthetic-ws-reply\",\"created\":1,\"object\":\"chat.completion.chunk\",\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n")
	}))
	return server, received
}

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
