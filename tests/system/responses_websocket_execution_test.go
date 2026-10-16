//go:build legacy_patched

// 历史私有原帧执行契约；当前明确排除所有 WebSocket，替代用例为 TestOfficialWebsocketExcluded。
// 本文件核对 WebSocket 原执行语义：历史恢复、审计开关对照、内部重试和异步保存失败。
package system

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"maps"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"
)

// awaitAuditUpstream 等待本次可控上游收到的请求；返回借用字节，超时终止测试，不输出或保存请求。
func awaitAuditUpstream(t *testing.T, received <-chan []byte) []byte {
	t.Helper()
	select {
	case body := <-received:
		return body
	case <-time.After(4 * time.Second):
		t.Fatal("本次WebSocket上游观察缺失")
		return nil
	}
}

// TestResponsesWebsocketHistoryAndEquivalence 验证真实历史补全仅影响执行，审计开关不改变请求或公开事件。
func TestResponsesWebsocketHistoryAndEquivalence(t *testing.T) {
	upstream, received := auditWebsocketUpstream()
	defer upstream.Close()
	on := startHostWithConfiguration(t, 1<<30, true, upstream.URL+"/v1", true, "")
	on.monitorAll(t)
	off := startHostWithConfiguration(t, 1<<30, false, upstream.URL+"/v1", true, "")
	for round := 0; round < 2; round++ {
		root := fmt.Sprintf("WS-HISTORY-ROOT-%d", round)
		tail := fmt.Sprintf("WS-HISTORY-NEW-%d", round)
		inputs := []string{
			fmt.Sprintf(`{"type":"response.create","model":"responses-client-model","instructions":"FORBIDDEN_WS_SYSTEM","input":[{"role":"user","content":"FORBIDDEN_WS_RESTORED_HISTORY"},{"role":"user","content":%q}]}`, root),
			fmt.Sprintf(`{"type":"response.append","previous_response_id":"synthetic-ws-reply","input":[{"role":"user","content":%q}]}`, tail),
			`{"type":"response.append","input":[{"type":"function_call_output","call_id":"synthetic-call","output":"FORBIDDEN_WS_RESTORED_TOOL"}]}`,
			fmt.Sprintf(`{"type":"response.create","model":"responses-client-model","input":[{"role":"user","content":%q}]}`, tail),
		}
		var baselineRequests, baselineEvents [][]byte
		for index, host := range []*instance{off, on} {
			conn := dialAuditWebsocket(t, host, "/v1/responses", keyA)
			for step, body := range inputs {
				sendAuditFrame(t, conn, body)
				events := bytes.Join(readAuditEvents(t, conn), []byte{'\n'})
				request := awaitAuditUpstream(t, received)
				if step == 1 && (!bytes.Contains(request, []byte("FORBIDDEN_WS_RESTORED_HISTORY")) || !bytes.Contains(request, []byte(root)) || !bytes.Contains(request, []byte(tail))) {
					t.Fatal("夹具未触发宿主真实历史补全")
				}
				if index == 0 {
					baselineRequests = append(baselineRequests, request)
					baselineEvents = append(baselineEvents, events)
				} else if !bytes.Equal(request, baselineRequests[step]) || !bytes.Equal(events, baselineEvents[step]) {
					t.Fatal("审计改变历史补全、实际请求或公开事件")
				}
			}
			_ = conn.Close()
		}
		records := on.waitRecords(t, (round+1)*3)
		if records[0].ID == records[1].ID {
			t.Fatal("显式重发没有独立提交标识")
		}
		latest := waitChatDetail(t, on, tail)
		if latest.Text != tail {
			t.Fatal("审计从恢复上下文补回历史")
		}
	}
	saved, queued, gaps := snapshotCounters(t, on)
	if saved != 6 || queued != 0 || gaps["unsupported_content"] != 0 {
		t.Fatal("历史或工具续跑制造了新输入记录")
	}
	assertChatPrivacy(t, on, "FORBIDDEN_WS_")
}

// TestResponsesWebsocketNativeEquivalence 验证原生duplex中截断/未知块及steering仅改变审计副本，原帧执行和确认不变。
func TestResponsesWebsocketNativeEquivalence(t *testing.T) {
	upstream, received := codexAuditUpstream()
	defer upstream.Close()
	cfg := codexAuditConfiguration(upstream.URL)
	on := startHostWithConfiguration(t, 1<<30, true, "", true, cfg)
	on.monitorAll(t)
	off := startHostWithConfiguration(t, 1<<30, false, "", true, cfg)
	snapshotMutation(t, on, "/settings", map[string]int{"max_text_bytes": 16})
	for round := 0; round < 2; round++ {
		prefix := fmt.Sprintf("WS-NATIVE-%d-aaaa", round)
		steer := fmt.Sprintf("WS-STEER-%d", round)
		bodies := []string{
			fmt.Sprintf(`{"type":"response.create","model":"ws-codex-model","prompt_cache_key":"FORBIDDEN_WS_NATIVE_SESSION","input":[{"role":"user","content":[{"type":"input_text","text":%q},{"type":"future","payload":"FORBIDDEN_WS_NATIVE_UNKNOWN"}]}]}`, prefix+"🙂FORBIDDEN_WS_NATIVE_TAIL"),
			fmt.Sprintf(`{"type":"response.steer","previous_response_id":"synthetic-codex-response-1","input":%q,"extension":"FORBIDDEN_WS_NATIVE_EXTENSION"}`, steer),
		}
		var originalRequests, originalEvents [][]byte
		for index, host := range []*instance{off, on} {
			conn := dialAuditWebsocket(t, host, "/v1/responses", keyA)
			for step, body := range bodies {
				sendAuditFrame(t, conn, body)
				var events []byte
				if step == 0 {
					events = bytes.Join(readAuditEvents(t, conn), nil)
				} else {
					events = readAuditSteer(t, conn)
				}
				request := awaitAuditUpstream(t, received)
				if index == 0 {
					originalRequests = append(originalRequests, request)
					originalEvents = append(originalEvents, events)
				} else if !bytes.Equal(request, originalRequests[step]) || !bytes.Equal(events, originalEvents[step]) {
					t.Fatal("原生WebSocket审计改变了上游完整业务输入或公开事件")
				}
			}
			_ = conn.Close()
		}
		record := waitChatDetail(t, on, prefix)
		if record.Text != prefix || !record.Truncated || !record.ExtractionIncomplete {
			t.Fatal("原生截断/未知块状态与独立预期不符")
		}
		waitSnapshotRecord(t, on, steer)
	}
	assertChatPrivacy(t, on, "FORBIDDEN_WS_")
}

// TestResponsesWebsocketPrewarm 验证本地generate:false显式输入记录一次，后续恢复历史不能补正文。
func TestResponsesWebsocketPrewarm(t *testing.T) {
	upstream, received := auditWebsocketUpstream()
	defer upstream.Close()
	h := startHostWithConfiguration(t, 1<<30, true, upstream.URL+"/v1", true, "")
	h.monitorAll(t)
	conn := dialAuditWebsocket(t, h, "/v1/responses", keyA)
	for round := 0; round < 2; round++ {
		marker := fmt.Sprintf("WS-PREWARM-%d", round)
		sendAuditFrame(t, conn, fmt.Sprintf(`{"type":"response.create","model":"responses-client-model","generate":false,"input":[{"role":"user","content":"FORBIDDEN_WS_PREWARM_HISTORY"},{"role":"user","content":%q}]}`, marker))
		events := readAuditEvents(t, conn)
		var terminal struct {
			Response struct {
				ID string `json:"id"`
			} `json:"response"`
		}
		if json.Unmarshal(events[len(events)-1], &terminal) != nil || terminal.Response.ID == "" {
			t.Fatal("没有收到宿主本地预热事件")
		}
		waitSnapshotRecord(t, h, marker)
		sendAuditFrame(t, conn, fmt.Sprintf(`{"type":"response.create","previous_response_id":%q,"input":[]}`, terminal.Response.ID))
		readAuditEvents(t, conn)
		request := awaitAuditUpstream(t, received)
		if !bytes.Contains(request, []byte(marker)) || !bytes.Contains(request, []byte("FORBIDDEN_WS_PREWARM_HISTORY")) {
			t.Fatal("宿主未真正恢复预热历史执行")
		}
	}
	// 同reader后续唯一输入的保存是空续跑负向检查屏障，不依赖回复时刻推测已保存。
	sendAuditFrame(t, conn, `{"type":"response.create","model":"responses-client-model","input":[{"role":"user","content":"WS-PREWARM-DRAIN"}]}`)
	readAuditEvents(t, conn)
	awaitAuditUpstream(t, received)
	waitSnapshotRecord(t, h, "WS-PREWARM-DRAIN")
	saved, queued, gaps := snapshotCounters(t, h)
	if saved != 3 || queued != 0 || gaps["unsupported_content"] != 0 {
		t.Fatal("恢复预热历史被重复审计")
	}
	assertChatPrivacy(t, h, "FORBIDDEN_WS_")
}

// TestResponsesWebsocketRetry 验证流式首token前内部重试真实发生，一个客户端提交仍只产生一条输入记录。
func TestResponsesWebsocketRetry(t *testing.T) {
	received := make(chan []byte, 16)
	var attempt atomic.Int64
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			w.WriteHeader(400)
			return
		}
		received <- body
		w.Header().Set("Content-Type", "text/event-stream")
		if attempt.Add(1)%2 == 1 {
			_, _ = io.WriteString(w, "event: error\ndata: {\"error\":{\"message\":\"synthetic retry\",\"type\":\"server_error\"}}\n\n")
			return
		}
		_, _ = io.WriteString(w, httpOnceSSE)
	}))
	defer upstream.Close()
	cfg := "routing:\n  retry:\n    request-retry: 1\n    max-retry-interval: 0\n  cooldown:\n    disable-cooling: true\n"
	h := startHostWithConfiguration(t, 1<<30, true, upstream.URL+"/v1", true, cfg)
	h.monitorAll(t)
	conn := dialAuditWebsocket(t, h, "/v1/responses", keyA)
	for round := 0; round < 2; round++ {
		marker := fmt.Sprintf("WS-RETRY-%d", round)
		sendAuditFrame(t, conn, fmt.Sprintf(`{"type":"response.create","model":"responses-client-model","input":[{"role":"user","content":%q}]}`, marker))
		readAuditEvents(t, conn)
		first, second := awaitAuditUpstream(t, received), awaitAuditUpstream(t, received)
		if !bytes.Equal(first, second) || !bytes.Contains(first, []byte(marker)) {
			t.Fatal("内部重试改变原本次执行请求")
		}
		waitSnapshotRecord(t, h, marker)
	}
	saved, queued, gaps := snapshotCounters(t, h)
	if saved != 2 || queued != 0 || gaps["unsupported_content"] != 0 {
		t.Fatal("内部重试被当作新的客户端提交")
	}
	assertChatPrivacy(t, h)
}

// TestResponsesWebsocketStorageFailure 验证真实目录权限故障仅报告存储缺口，不改变原有模型执行或公开事件。
func TestResponsesWebsocketStorageFailure(t *testing.T) {
	upstream, received := auditWebsocketUpstream()
	defer upstream.Close()
	h := startHostWithConfiguration(t, 1<<30, true, upstream.URL+"/v1", true, "")
	h.monitorAll(t)
	off := startHostWithConfiguration(t, 1<<30, false, upstream.URL+"/v1", true, "")
	conn := dialAuditWebsocket(t, h, "/v1/responses", keyA)
	sendAuditFrame(t, conn, `{"type":"response.create","model":"responses-client-model","input":[{"role":"user","content":"WS-FAULT-SEED"}]}`)
	readAuditEvents(t, conn)
	awaitAuditUpstream(t, received)
	waitSnapshotRecord(t, h, "WS-FAULT-SEED")
	_, _, before := snapshotCounters(t, h)
	if os.WriteFile(filepath.Join(h.dir, "audit", "external-ws-storage-fault"), []byte("synthetic fault only"), 0400) != nil {
		t.Fatal("隔离存储故障设置失败")
	}
	body := `{"type":"response.create","model":"responses-client-model","input":[{"role":"user","content":"FORBIDDEN_WS_FAILED_SAVE"}]}`
	// 新连接使执行历史与未启用审计的对照完全相同。
	_ = conn.Close()
	conn = dialAuditWebsocket(t, h, "/v1/responses", keyA)
	baseline := dialAuditWebsocket(t, off, "/v1/responses", keyA)
	sendAuditFrame(t, baseline, body)
	plainEvents := bytes.Join(readAuditEvents(t, baseline), nil)
	plainRequest := awaitAuditUpstream(t, received)
	sendAuditFrame(t, conn, body)
	events := bytes.Join(readAuditEvents(t, conn), nil)
	request := awaitAuditUpstream(t, received)
	if !bytes.Equal(events, plainEvents) || !bytes.Equal(request, plainRequest) {
		t.Fatal("采集故障改变了WebSocket执行或公开事件")
	}
	saved, after := waitHTTPOnceGap(t, h, "storage_error", before["storage_error"])
	want := maps.Clone(before)
	want["storage_error"]++
	if saved != 1 || !maps.Equal(want, after) {
		t.Fatal("失败副本冒充保存或模型结果被记作采集错误")
	}
	assertChatPrivacy(t, h, "FORBIDDEN_WS_")
}
