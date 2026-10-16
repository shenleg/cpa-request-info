// 本文件提供真实 WebSocket 连接、帧和本地 SSE 上游辅助；当前零采集由 TestOfficialWebsocketExcluded 验证。
package system

import (
	"encoding/json"
	"github.com/gorilla/websocket"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
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
		if respondDiscovery(w, r) {
			return
		}
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
