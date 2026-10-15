// 本文件通过公开重试策略与实际账号使用证明三协议的内部重试/切换只对应一次客户端提交。
package system

import (
	"bytes"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

// matrixRetryAttempts 核对本轮两次真实执行、完整业务内容及同账号/账号切换；返回字节供基线对照。
// attempts仅含本地合成请求和凭据，marker为独立预期；不符或超时终止测试，不输出原文。
func matrixRetryAttempts(t *testing.T, attempts <-chan httpOnceAttempt, marker string, switchAccount bool) []byte {
	t.Helper()
	accounts := map[string]bool{}
	var first []byte
	for i := 0; i < 2; i++ {
		select {
		case attempt := <-attempts:
			assertMatrixUpstream(t, attempt.body, marker)
			if attempt.credential == "" {
				t.Fatal("无法确定实际重试账号")
			}
			accounts[attempt.credential] = true
			if i == 0 {
				first = attempt.body
			} else if !bytes.Equal(first, attempt.body) {
				t.Fatal("内部重试改变了完整上游请求")
			}
		case <-time.After(5 * time.Second):
			t.Fatal("没有发生本轮两次真实上游执行")
		}
	}
	want := 1
	if switchAccount {
		want = 2
	}
	if len(accounts) != want {
		t.Fatal("实际重试账号数量不符")
	}
	return first
}

// TestHTTPProtocolsRetries 验证三协议的同账号、换账号及首token前SSE重试，多次执行仍保存一次且原调用不变。
func TestHTTPProtocolsRetries(t *testing.T) {
	for _, p := range httpAuditProtocols {
		for _, tc := range []struct {
			name                  string
			stream, switchAccount bool
			retries               int
		}{
			{"same_account", false, false, 1}, {"account_switch", false, true, 0}, {"sse_pre_token", true, false, 1},
		} {
			t.Run(p.name+"/"+tc.name, func(t *testing.T) {
				attempts := make(chan httpOnceAttempt, 8)
				releases := make(chan (<-chan struct{}), 4)
				var count atomic.Int64
				upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					raw, err := io.ReadAll(r.Body)
					if err != nil {
						w.WriteHeader(400)
						return
					}
					attempts <- httpOnceAttempt{raw, r.Header.Get("Authorization")}
					if count.Add(1)%2 == 1 {
						if tc.stream {
							w.Header().Set("Content-Type", "text/event-stream")
							_, _ = io.WriteString(w, "event: error\ndata: {\"error\":{\"message\":\"FORBIDDEN_HTTP_RETRY_ERROR\",\"type\":\"server_error\"}}\n\n")
						} else {
							w.Header().Set("Content-Type", "application/json")
							w.WriteHeader(503)
							_, _ = io.WriteString(w, `{"error":{"message":"FORBIDDEN_HTTP_RETRY_ERROR","type":"server_error"}}`)
						}
						return
					}
					select {
					case release := <-releases:
						select {
						case <-release:
						case <-r.Context().Done():
							return
						}
					case <-r.Context().Done():
						return
					}
					if tc.stream {
						w.Header().Set("Content-Type", "text/event-stream")
						_, _ = io.WriteString(w, httpMatrixSSE)
					} else {
						w.Header().Set("Content-Type", "application/json")
						_, _ = io.WriteString(w, httpMatrixReply)
					}
				}))
				t.Cleanup(upstream.Close)
				keys := "      - api-key: synthetic-http-retry-account-a\n"
				if tc.switchAccount {
					keys += "      - api-key: synthetic-http-retry-account-b\n"
				}
				config := fmt.Sprintf(`openai-compatibility:
  - name: controlled-retry
    base-url: %q
    api-key-entries:
%s    models:
      - name: audit-test-model
        alias: audit-test-model
routing:
  retry:
    request-retry: %d
    max-retry-credentials: 0
    max-retry-interval: 0
  cooldown:
    disable-cooling: true
requests:
  streaming:
    bootstrap-retries: 0
`, upstream.URL+"/v1", keys, tc.retries)
				h := startHostWithConfiguration(t, 1<<30, true, "", true, config)
				baseline := startHostWithConfiguration(t, 1<<30, false, "", true, config)
				h.monitorAll(t)
				_, _, gaps := snapshotCounters(t, h)
				for round := 0; round < 2; round++ {
					marker := fmt.Sprintf("MATRIX-RETRY-%s-%s-%d-%d", p.name, tc.name, round, time.Now().UnixNano())
					body := p.input(t, marker, tc.stream, "audit-test-model")
					release := make(chan struct{})
					releases <- release
					unblock := func() {
						select {
						case <-release:
						default:
							close(release)
						}
					}
					t.Cleanup(unblock)
					result := beginHTTPOnce(t, h, t.Context(), body, p.path)
					actual := matrixRetryAttempts(t, attempts, marker, tc.switchAccount)
					waitSnapshotRecord(t, h, marker)
					assertHTTPOnceState(t, h, int64(round+1), gaps)
					select {
					case <-result:
						t.Fatal("第二次执行未回复却已完成")
					default:
					}
					unblock()
					outcome := awaitHTTPOnce(t, result)
					ready := make(chan struct{})
					close(ready)
					releases <- ready
					control := beginHTTPOnce(t, baseline, t.Context(), body, p.path)
					originalBody := matrixRetryAttempts(t, attempts, marker, tc.switchAccount)
					original := awaitHTTPOnce(t, control)
					if outcome.err != nil || original.err != nil || outcome.code != 200 || original.code != 200 || !bytes.Equal(actual, originalBody) || !bytes.Equal(outcome.body, original.body) {
						t.Fatal("审计改变了重试后的模型调用")
					}
					if count.Load() != int64(4*(round+1)) {
						t.Fatal("实际执行次数不符")
					}
					assertHTTPOnceState(t, h, int64(round+1), gaps)
				}
				assertMatrixPrivacy(t, h, "synthetic-http-retry-account-a", "synthetic-http-retry-account-b")
				assertHTTPOnceErrorPrivacy(t, h, baseline, "FORBIDDEN_HTTP_RETRY_ERROR")
			})
		}
	}
}
