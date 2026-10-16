//go:build legacy_patched

// 历史私有原帧接收与取消契约；当前明确排除所有 WebSocket，替代用例为 TestOfficialWebsocketExcluded。
// 本文件以真实上游屏障验证帧接收时快照、客户端取消/断开和已接收输入不依赖模型完成。
package system

import (
	"bytes"
	"encoding/json"
	"fmt"
	"github.com/gorilla/websocket"
	"io"
	"maps"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// TestResponsesWebsocketIncompleteFrame 验证完整JSON前缀若没有WebSocket消息终帧，只报告读取缺口而不保存正文。
func TestResponsesWebsocketIncompleteFrame(t *testing.T) {
	h := startHostWithConfiguration(t, 1<<30, true, "", true, "")
	h.monitorAll(t)
	_, _, before := snapshotCounters(t, h)
	conn := dialAuditWebsocket(t, h, "/v1/responses", keyA)
	writer, err := conn.NextWriter(websocket.TextMessage)
	if err != nil {
		t.Fatal("不完整消息夹具创建失败")
	}
	// 超过客户端写缓冲促使非FIN数据帧发出，故意不Close writer；有效JSON不能代替完整消息。
	body := append([]byte(`{"type":"response.create","model":"missing","input":"FORBIDDEN_WS_PARTIAL_INPUT"}`), bytes.Repeat([]byte{' '}, 8192)...)
	if _, err := writer.Write(body); err != nil {
		t.Fatal("不完整数据帧发送失败")
	}
	if err := conn.UnderlyingConn().Close(); err != nil {
		t.Fatal("不完整消息连接关闭失败")
	}
	saved, after := waitHTTPOnceGap(t, h, "read_error", before["read_error"])
	want := maps.Clone(before)
	want["read_error"]++
	if saved != 0 || !maps.Equal(want, after) {
		t.Fatal("不完整消息被视为完整输入或读取缺口不准确")
	}
	assertChatPrivacy(t, h, "FORBIDDEN_WS_")
}

// TestResponsesWebsocketQueuedSnapshot 验证活跃模型阻塞时后续帧已按接收快照保存，配置变更不影响已接收副本。
func TestResponsesWebsocketQueuedSnapshot(t *testing.T) {
	received := make(chan []byte, 16)
	releases := make(chan (<-chan struct{}), 16)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			w.WriteHeader(400)
			return
		}
		received <- body
		release := <-releases
		select {
		case <-release:
		case <-r.Context().Done():
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, httpOnceSSE)
	}))
	t.Cleanup(upstream.Close)
	h := startHostWithConfiguration(t, 1<<30, true, upstream.URL+"/v1", true, "")
	h.monitorAll(t)
	conn := dialAuditWebsocket(t, h, "/v1/responses", keyA)
	_, _, gaps := snapshotCounters(t, h)
	open := make(chan struct{})
	close(open)
	for round := 0; round < 2; round++ {
		hold := make(chan struct{})
		releases <- hold
		t.Cleanup(func() {
			select {
			case <-hold:
			default:
				close(hold)
			}
		})
		first := fmt.Sprintf("WS-QUEUED-FIRST-%d", round)
		second := fmt.Sprintf("WS-QUEUED-SECOND-%d-FULL", round)
		sendAuditFrame(t, conn, fmt.Sprintf(`{"type":"response.create","model":"responses-client-model","input":[{"role":"user","content":%q}]}`, first))
		awaitAuditUpstream(t, received)
		waitSnapshotRecord(t, h, first)
		sendAuditFrame(t, conn, fmt.Sprintf(`{"type":"response.append","input":[{"role":"user","content":%q}]}`, second))
		pending := waitSnapshotRecord(t, h, second)
		if pending.Model != "" {
			t.Fatal("排队帧从继承上下文补猜模型")
		}
		snapshotMutation(t, h, "/settings", map[string]int{"max_text_bytes": 3})
		snapshotMutation(t, h, "/remove", map[string]string{"object_id": pending.ObjectID})
		snapshotMutation(t, h, "/pause", map[string]bool{"paused": true})
		sendAuditFrame(t, conn, `{"type":"response.append","input":[{"role":"user","content":"FORBIDDEN_WS_QUEUED_PAUSED"}]}`)
		// Ping的Pong只会在reader完成前面完整帧的同步观察后处理，是暂停帧已接收的公开传输屏障。
		pong := make(chan struct{}, 1)
		conn.SetPongHandler(func(string) error { pong <- struct{}{}; return nil })
		if conn.WriteControl(websocket.PingMessage, []byte("snapshot-barrier"), time.Now().Add(3*time.Second)) != nil {
			t.Fatal("传输屏障发送失败")
		}
		releases <- open
		releases <- open
		close(hold)
		readAuditEvents(t, conn)
		readAuditEvents(t, conn)
		readAuditEvents(t, conn)
		for count := 0; count < 2; count++ {
			request := awaitAuditUpstream(t, received)
			if count == 0 && !bytes.Contains(request, []byte(second)) {
				t.Fatal("审计上限改变了已排队的真实执行输入")
			}
		}
		select {
		case <-pong:
		case <-time.After(3 * time.Second):
			t.Fatal("暂停帧接收屏障未完成")
		}
		code, raw := h.request(t, "GET", api+"/detail?id="+pending.ID, adminKey, nil)
		var record savedRecord
		if code != 200 || json.Unmarshal(raw, &record) != nil || record.Text != second || record.Truncated {
			t.Fatal("接收后配置变更取消或截断了旧副本")
		}
		snapshotMutation(t, h, "/monitor", map[string]string{"object_id": pending.ObjectID, "name": "WS排队对象"})
		snapshotMutation(t, h, "/pause", map[string]bool{"paused": false})
		snapshotMutation(t, h, "/settings", map[string]int{"max_text_bytes": 65536})
	}
	// 后续新提交保存用于此前暂停帧的负向检查。
	releases <- open
	sendAuditFrame(t, conn, `{"type":"response.create","model":"responses-client-model","input":[{"role":"user","content":"WS-QUEUED-DRAIN"}]}`)
	readAuditEvents(t, conn)
	awaitAuditUpstream(t, received)
	waitSnapshotRecord(t, h, "WS-QUEUED-DRAIN")
	assertHTTPOnceState(t, h, 5, gaps)
	assertChatPrivacy(t, h, "FORBIDDEN_WS_")
}

// TestResponsesWebsocketCancellation 验证首token后真实interrupt或断开传到上游，但不会删除已接收记录或计为采集错误。
func TestResponsesWebsocketCancellation(t *testing.T) {
	for _, mode := range []string{"interrupt", "disconnect"} {
		t.Run(mode, func(t *testing.T) {
			received := make(chan []byte, 4)
			cancelled := make(chan struct{}, 4)
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				body, err := io.ReadAll(r.Body)
				if err != nil {
					w.WriteHeader(400)
					return
				}
				received <- body
				w.Header().Set("Content-Type", "text/event-stream")
				_, _ = io.WriteString(w, "data: {\"id\":\"synthetic-cancel-response\",\"object\":\"chat.completion.chunk\",\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\",\"content\":\"ok\"},\"finish_reason\":null}]}\n\n")
				w.(http.Flusher).Flush()
				<-r.Context().Done()
				cancelled <- struct{}{}
			}))
			h := startHostWithConfiguration(t, 1<<30, true, upstream.URL+"/v1", true, "")
			h.monitorAll(t)
			// 宿主必须先停掉本轮连接，避免失败测试留下等待取消的上游handler。
			t.Cleanup(func() { h.stop(); upstream.Close() })
			_, _, before := snapshotCounters(t, h)
			for round := 0; round < 2; round++ {
				conn := dialAuditWebsocket(t, h, "/v1/responses", keyA)
				marker := fmt.Sprintf("WS-CANCEL-%s-%d", mode, round)
				sendAuditFrame(t, conn, fmt.Sprintf(`{"type":"response.create","model":"responses-client-model","input":[{"role":"user","content":%q}]}`, marker))
				awaitAuditUpstream(t, received)
				if conn.SetReadDeadline(time.Now().Add(5*time.Second)) != nil {
					t.Fatal("首token观察期限设置失败")
				}
				for {
					_, body, err := conn.ReadMessage()
					if err != nil {
						t.Fatal("未观察到首token前活跃模型")
					}
					var event struct {
						Type string `json:"type"`
					}
					if json.Unmarshal(body, &event) != nil {
						t.Fatal("取消前公开事件非法")
					}
					if event.Type == "response.output_text.delta" {
						break
					}
				}
				record := waitSnapshotRecord(t, h, marker)
				if mode == "interrupt" {
					sendAuditFrame(t, conn, `{"type":"response.interrupt","response_id":"synthetic-cancel-response","input":"FORBIDDEN_WS_CANCEL_CONTROL"}`)
					events := readAuditEvents(t, conn)
					if !bytes.Contains(events[len(events)-1], []byte(`"reason":"interrupted"`)) {
						t.Fatal("原宿主未按控制事件完成本地取消")
					}
				}
				_ = conn.Close()
				select {
				case <-cancelled:
				case <-time.After(4 * time.Second):
					t.Fatal("客户端取消/断开未传到真实上游")
				}
				code, raw := h.request(t, "GET", api+"/detail?id="+record.ID, adminKey, nil)
				var retained savedRecord
				if code != 200 || json.Unmarshal(raw, &retained) != nil || retained.Text != marker {
					t.Fatal("取消或断开删除了此前已保存输入")
				}
			}
			saved, queued, after := snapshotCounters(t, h)
			if saved != 2 || queued != 0 || !maps.Equal(before, after) {
				t.Fatal("取消控制事件生成正文或被记为采集故障")
			}
			assertChatPrivacy(t, h, "FORBIDDEN_WS_")
		})
	}
}
