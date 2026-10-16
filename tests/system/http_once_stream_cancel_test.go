// 本文件在客户端真正收到首个 SSE token 后断开连接，验证审计副本不随执行取消撤销。
package system

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// beginHTTPOnceStream 读取首个公开 SSE 数据行后继续等待流结束；返回首行和最终结果。
// ctx 控制真实连接取消，body 是合成输入；网络错误随结果返回，调用者须取消并等待结束，不保留流历史。
func beginHTTPOnceStream(t *testing.T, h *instance, ctx context.Context, body []byte) (<-chan []byte, <-chan httpOnceOutcome) {
	t.Helper()
	req, err := http.NewRequestWithContext(ctx, "POST", h.base+"/v1/chat/completions", bytes.NewReader(body))
	if err != nil {
		t.Fatal("SSE 取消请求构造失败")
	}
	req.Header.Set("Authorization", "Bearer "+keyA)
	req.Header.Set("Content-Type", "application/json")
	first, result := make(chan []byte, 1), make(chan httpOnceOutcome, 1)
	go func() {
		resp, err := h.client.Do(req)
		if err != nil {
			result <- httpOnceOutcome{err: err}
			return
		}
		reader := bufio.NewReader(resp.Body)
		line, err := reader.ReadBytes('\n')
		if err == nil {
			first <- line
			_, err = io.Copy(io.Discard, reader)
		}
		closeErr := resp.Body.Close()
		if err == nil {
			err = closeErr
		}
		result <- httpOnceOutcome{code: resp.StatusCode, err: err}
	}()
	return first, result
}

// TestHTTPOnceSSEDisconnect 验证首 token 后真实断开仍保留唯一输入，公开 token 和上游取消行为与禁用审计相同。
func TestHTTPOnceSSEDisconnect(t *testing.T) {
	received := make(chan []byte, 4)
	canceled := make(chan struct{}, 4)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if respondDiscovery(w, r) {
			return
		}
		raw, err := io.ReadAll(r.Body)
		if err != nil {
			w.WriteHeader(400)
			return
		}
		received <- raw
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "data: {\"id\":\"synthetic-http-once\",\"object\":\"chat.completion.chunk\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"FORBIDDEN_HTTP_STREAM_TOKEN\"},\"finish_reason\":null}]}\n\n")
		w.(http.Flusher).Flush()
		<-r.Context().Done()
		canceled <- struct{}{}
	}))
	t.Cleanup(upstream.Close)
	h := startHostWithManagementAuth(t, 1<<30, true, upstream.URL+"/v1", true)
	baseline := startHostWithManagementAuth(t, 1<<30, false, upstream.URL+"/v1", true)
	h.monitorAll(t)
	_, _, gaps := snapshotCounters(t, h)
	for round := 0; round < 2; round++ {
		marker := fmt.Sprintf("HTTP-SSE-DISCONNECT-%d-%d", round, time.Now().UnixNano())
		body := []byte(fmt.Sprintf(`{"model":"audit-test-model","stream":true,"stream_options":{"include_usage":true},"messages":[{"role":"user","content":%q}]}`, marker))
		var auditedFrame []byte
		for _, host := range []*instance{h, baseline} {
			ctx, cancel := context.WithCancel(t.Context())
			t.Cleanup(cancel)
			first, result := beginHTTPOnceStream(t, host, ctx, body)
			select {
			case frame := <-first:
				if !bytes.Contains(frame, []byte("FORBIDDEN_HTTP_STREAM_TOKEN")) {
					t.Fatal("SSE 未实际交付首 token")
				}
				if host == h {
					auditedFrame = frame
					waitSnapshotRecord(t, h, marker)
				} else if !bytes.Equal(frame, auditedFrame) {
					t.Fatal("审计改变了取消前公开 SSE token")
				}
			case <-time.After(3 * time.Second):
				t.Fatal("SSE 首 token 公开屏障未建立")
			}
			cancel()
			outcome := awaitHTTPOnce(t, result)
			if outcome.code != 200 || !errors.Is(outcome.err, context.Canceled) {
				t.Fatal("SSE 真实断开行为不符")
			}
			select {
			case raw := <-received:
				if !bytes.Equal(raw, body) {
					t.Fatal("SSE 取消前上游原请求被改变")
				}
			case <-time.After(3 * time.Second):
				t.Fatal("SSE 上游未收到本轮完整输入")
			}
			select {
			case <-canceled:
			case <-time.After(3 * time.Second):
				t.Fatal("SSE 上游未保持原取消行为")
			}
		}
		waitSnapshotRecord(t, h, marker)
		assertHTTPOnceState(t, h, int64(round+1), gaps)
	}
	assertChatPrivacy(t, h, "FORBIDDEN_HTTP_STREAM_TOKEN")
}
