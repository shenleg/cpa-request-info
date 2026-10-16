// 本文件用真实客户端期限、取消及首token后断开分别验收三协议，不用HTTP408冒充传输超时。
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

// TestHTTPProtocolsCancellation 验证各协议收完整输入后，真实取消/deadline传到上游，已保存输入不撤销或重计。
func TestHTTPProtocolsCancellation(t *testing.T) {
	for _, p := range httpAuditProtocols {
		for _, mode := range []string{"disconnect", "deadline"} {
			t.Run(p.name+"/"+mode, func(t *testing.T) {
				received, canceled := make(chan []byte, 4), make(chan struct{}, 4)
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
					<-r.Context().Done()
					canceled <- struct{}{}
				}))
				t.Cleanup(upstream.Close)
				h := startHostWithConfiguration(t, 1<<30, true, upstream.URL+"/v1", true, httpOnceNoRetry)
				baseline := startHostWithConfiguration(t, 1<<30, false, upstream.URL+"/v1", true, httpOnceNoRetry)
				h.monitorAll(t)
				_, _, gaps := snapshotCounters(t, h)
				for round := 0; round < 2; round++ {
					marker := fmt.Sprintf("MATRIX-CANCEL-%s-%s-%d-%d", p.name, mode, round, time.Now().UnixNano())
					body := p.input(t, marker, false, "audit-test-model")
					var audited []byte
					for _, host := range []*instance{h, baseline} {
						ctx, cancel := context.WithCancel(t.Context())
						wantErr := context.Canceled
						if mode == "deadline" {
							cancel()
							ctx, cancel = context.WithTimeout(t.Context(), time.Second)
							wantErr = context.DeadlineExceeded
						}
						t.Cleanup(cancel)
						result := beginHTTPOnce(t, host, ctx, body, p.path)
						raw := receiveMatrixUpstream(t, received, marker)
						if host == h {
							audited = raw
							waitSnapshotRecord(t, h, marker)
						} else if !bytes.Equal(audited, raw) {
							t.Fatal("审计改变了取消前的完整上游请求")
						}
						if mode == "disconnect" {
							cancel()
						}
						outcome := awaitHTTPOnce(t, result)
						cancel()
						if !errors.Is(outcome.err, wantErr) {
							t.Fatal("真实客户端取消或期限结果不符")
						}
						select {
						case <-canceled:
						case <-time.After(4 * time.Second):
							t.Fatal("宿主未保留上游真实取消行为")
						}
					}
					assertHTTPOnceState(t, h, int64(round+1), gaps)
				}
				assertMatrixPrivacy(t, h)
			})
		}
	}
}

// beginProtocolStream 等待公开流中真正包含合成token的数据行，而不是把协议头/response.created当作首token。
// p选择协议，ctx控制真实取消；返回token行及结束结果，网络/读取错误随结果返回，调用者必须取消并等待。
func beginProtocolStream(t *testing.T, h *instance, p httpAuditProtocol, ctx context.Context, body []byte) (<-chan []byte, <-chan httpOnceOutcome) {
	t.Helper()
	req, err := http.NewRequestWithContext(ctx, "POST", h.base+p.path, bytes.NewReader(body))
	if err != nil {
		t.Fatal("三协议SSE请求构造失败")
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
		for {
			line, readErr := reader.ReadBytes('\n')
			if readErr != nil {
				err = readErr
				break
			}
			if bytes.Contains(line, []byte("FORBIDDEN_HTTP_STREAM_TOKEN")) {
				first <- line
				_, err = io.Copy(io.Discard, reader)
				break
			}
		}
		closeErr := resp.Body.Close()
		if err == nil {
			err = closeErr
		}
		result <- httpOnceOutcome{code: resp.StatusCode, err: err}
	}()
	return first, result
}

// TestHTTPProtocolsSSEDisconnect 验证每种协议真正交付首token后断开，不撤销已接收提交且不新增模型取消缺口。
func TestHTTPProtocolsSSEDisconnect(t *testing.T) {
	for _, p := range httpAuditProtocols {
		t.Run(p.name, func(t *testing.T) {
			received, canceled := make(chan []byte, 4), make(chan struct{}, 4)
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
				_, _ = io.WriteString(w, "data: {\"id\":\"synthetic-matrix-stream\",\"created\":1,\"object\":\"chat.completion.chunk\",\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\",\"content\":\"FORBIDDEN_HTTP_STREAM_TOKEN\"},\"finish_reason\":null}]}\n\n")
				w.(http.Flusher).Flush()
				<-r.Context().Done()
				canceled <- struct{}{}
			}))
			t.Cleanup(upstream.Close)
			h := startHostWithConfiguration(t, 1<<30, true, upstream.URL+"/v1", true, httpOnceNoRetry)
			baseline := startHostWithConfiguration(t, 1<<30, false, upstream.URL+"/v1", true, httpOnceNoRetry)
			h.monitorAll(t)
			_, _, gaps := snapshotCounters(t, h)
			for round := 0; round < 2; round++ {
				marker := fmt.Sprintf("MATRIX-STREAM-CANCEL-%s-%d-%d", p.name, round, time.Now().UnixNano())
				body := p.input(t, marker, true, "audit-test-model")
				var audited, token []byte
				for _, host := range []*instance{h, baseline} {
					ctx, cancel := context.WithCancel(t.Context())
					t.Cleanup(cancel)
					first, result := beginProtocolStream(t, host, p, ctx, body)
					raw := receiveMatrixUpstream(t, received, marker)
					select {
					case frame := <-first:
						if host == h {
							audited, token = raw, frame
							waitSnapshotRecord(t, h, marker)
						} else if !bytes.Equal(raw, audited) || !bytes.Equal(frame, token) {
							t.Fatal("审计改变了首token或上游请求")
						}
					case <-time.After(4 * time.Second):
						t.Fatal("公开首token屏障未建立")
					}
					cancel()
					outcome := awaitHTTPOnce(t, result)
					if outcome.code != 200 || !errors.Is(outcome.err, context.Canceled) {
						t.Fatal("SSE断开结果不符")
					}
					select {
					case <-canceled:
					case <-time.After(4 * time.Second):
						t.Fatal("SSE断开未传到真实上游")
					}
				}
				assertHTTPOnceState(t, h, int64(round+1), gaps)
			}
			assertMatrixPrivacy(t, h, "FORBIDDEN_HTTP_STREAM_TOKEN")
		})
	}
}
