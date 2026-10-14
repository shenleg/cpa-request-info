// 本文件以本地 Codex WebSocket 上游核对 duplex 和 steering 不绕过原始消息审计。
package system

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

// codexAuditUpstream 接受真实宿主的本地上游连接并按请求发固定公开事件；返回收到的原帧，调用者关闭服务器。
// 读写失败结束本连接；事件只在内存中传递，不保存正文或鉴权材料。
func codexAuditUpstream() (*httptest.Server, <-chan []byte) {
	received := make(chan []byte, 64)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
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
