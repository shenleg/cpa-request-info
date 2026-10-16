// 本文件提供本地 Codex duplex 上游与公开配置；当前不采集 WebSocket，旧原帧断言在 legacy_patched 文件。
package system

import (
	"encoding/json"
	"fmt"
	"github.com/gorilla/websocket"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// codexAuditUpstream 接受真实宿主的本地上游连接并按请求发固定公开事件；返回收到的原帧，调用者关闭服务器。
// 读写失败结束本连接；事件只在内存中传递，不保存正文或鉴权材料。
func codexAuditUpstream() (*httptest.Server, <-chan []byte) {
	received := make(chan []byte, 64)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if respondDiscovery(w, r) {
			return
		}
		upgrader := websocket.Upgrader{}
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer func() { _ = conn.Close() }()
		count := 0
		for {
			_, body, err := conn.ReadMessage()
			if err != nil {
				return
			}
			received <- body
			var frame struct {
				Type     string `json:"type"`
				Previous string `json:"previous_response_id"`
			}
			if json.Unmarshal(body, &frame) != nil {
				return
			}
			count++
			if frame.Type == "response.steer" {
				event := fmt.Sprintf(`{"type":"response.steer.failed","steer":{"id":"synthetic-steer-%d","previous_response_id":%q},"error":{"message":"synthetic steering rejection"}}`, count, frame.Previous)
				if conn.WriteMessage(websocket.TextMessage, []byte(event)) != nil {
					return
				}
				continue
			}
			if frame.Type == "response.interrupt" {
				if conn.WriteMessage(websocket.TextMessage, []byte(`{"type":"response.completed","response":{"id":"synthetic-interrupted","status":"completed","model":"gpt-5-codex","output":[]}}`)) != nil {
					return
				}
				continue
			}
			id := fmt.Sprintf("synthetic-codex-response-%d", count)
			for _, event := range []string{fmt.Sprintf(`{"type":"response.created","response":{"id":%q,"status":"in_progress","model":"gpt-5-codex","output":[]}}`, id), fmt.Sprintf(`{"type":"response.completed","response":{"id":%q,"status":"completed","model":"gpt-5-codex","output":[],"usage":{"input_tokens":1,"output_tokens":1,"total_tokens":2}}}`, id)} {
				if conn.WriteMessage(websocket.TextMessage, []byte(event)) != nil {
					return
				}
			}
		}
	}))
	return server, received
}

// readAuditSteer 读取本次 steering 公开确认/失败；返回事件原帧，读取期限或类型不符终止测试。
func readAuditSteer(t *testing.T, conn *websocket.Conn) []byte {
	t.Helper()
	if conn.SetReadDeadline(time.Now().Add(8*time.Second)) != nil {
		t.Fatal("steering读取期限失败")
	}
	for {
		_, body, err := conn.ReadMessage()
		if err != nil {
			t.Fatal("未收到上游原生 steering 事件")
		}
		var event struct {
			Type string `json:"type"`
		}
		if json.Unmarshal(body, &event) != nil {
			t.Fatal("steering事件非法")
		}
		if strings.HasPrefix(event.Type, "response.steer.") {
			return body
		}
		if event.Type == "error" {
			t.Fatal("steering未进入宿主duplex执行路径")
		}
	}
}

// codexAuditConfiguration 返回本地 Codex 上游的公开配置；upstream 是合成URL，无I/O或异常。
func codexAuditConfiguration(upstream string) string {
	return fmt.Sprintf(`upstream:
  codex:
    response-steering: true
api-keys:
  codex:
    - base-url: %q
      models:
        - name: gpt-5-codex
          alias: ws-codex-model
      keys:
        - api-key: synthetic-upstream-only
          websockets: true
`, upstream)
}
