// 本文件通过公开重试配置和可控上游验证内部执行尝试不是客户端提交；账号仅在夹具内瞬时核对。
package system

import (
	"bytes"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"
)

type httpOnceAttempt struct {
	body       []byte
	credential string
}

// awaitHTTPOnceAttempts 等待本轮两个实际上游尝试并核对完整请求及账号切换；返回账号集合。
// body 是独立合成期望，switchAccount 决定账号是否必须不同；超时或不符终止测试，不输出凭据。
func awaitHTTPOnceAttempts(t *testing.T, attempts <-chan httpOnceAttempt, body []byte, switchAccount bool) map[string]bool {
	t.Helper()
	accounts := map[string]bool{}
	for i := 0; i < 2; i++ {
		select {
		case attempt := <-attempts:
			if !bytes.Equal(attempt.body, body) || attempt.credential == "" {
				t.Fatal("重试改变了上游请求或缺少可确定的账号证据")
			}
			accounts[attempt.credential] = true
		case <-time.After(4 * time.Second):
			t.Fatalf("未观察到本轮两个真实上游执行尝试：received=%d", i)
		}
	}
	want := 1
	if switchAccount {
		want = 2
	}
	if len(accounts) != want {
		t.Fatal("真实重试或账号切换行为与场景不符")
	}
	return accounts
}

// TestHTTPOnceRetries 验证同账号重试、账号切换及首 token 前 bootstrap 重试各只保存一条，公开行为与禁用审计一致。
func TestHTTPOnceRetries(t *testing.T) {
	for _, tc := range []struct {
		name          string
		stream        bool
		switchAccount bool
		retryRounds   int
		bootstrap     int
	}{
		{"same_account", false, false, 1, 0},
		{"account_switch", false, true, 0, 0},
		{"sse_pre_token", true, false, 1, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			attempts := make(chan httpOnceAttempt, 8)
			releases := make(chan (<-chan struct{}), 4)
			var count atomic.Int64
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if respondDiscovery(w, r) {
					return
				}
				raw, err := io.ReadAll(r.Body)
				if err != nil {
					w.WriteHeader(400)
					return
				}
				attempts <- httpOnceAttempt{raw, r.Header.Get("Authorization")}
				if count.Add(1)%2 == 1 {
					if tc.stream {
						// 真正建立 SSE 后在任何 token 之前发出错误帧，触发宿主流式 bootstrap 的内部重试。
						w.Header().Set("Content-Type", "text/event-stream")
						_, _ = io.WriteString(w, "event: error\ndata: {\"error\":{\"message\":\"FORBIDDEN_HTTP_RETRY_ERROR\",\"type\":\"server_error\"}}\n\n")
					} else {
						w.Header().Set("Content-Type", "application/json")
						w.WriteHeader(503)
						_, _ = io.WriteString(w, `{"error":{"message":"FORBIDDEN_HTTP_RETRY_ERROR","type":"server_error"}}`)
					}
					return
				}
				var release <-chan struct{}
				select {
				case release = <-releases:
				case <-r.Context().Done():
					return
				}
				select {
				case <-release:
				case <-r.Context().Done():
					return
				}
				if tc.stream {
					w.Header().Set("Content-Type", "text/event-stream")
					_, _ = io.WriteString(w, httpOnceSSE)
				} else {
					w.Header().Set("Content-Type", "application/json")
					_, _ = io.WriteString(w, httpOnceReply)
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
    bootstrap-retries: %d
`, upstream.URL+"/v1", keys, tc.retryRounds, tc.bootstrap)
			h := startHostWithConfiguration(t, 1<<30, true, "", true, config)
			baseline := startHostWithConfiguration(t, 1<<30, false, "", true, config)
			h.monitorAll(t)
			_, _, gaps := snapshotCounters(t, h)
			for round := 0; round < 2; round++ {
				marker := fmt.Sprintf("HTTP-RETRY-%s-%d-%d", tc.name, round, time.Now().UnixNano())
				body := []byte(fmt.Sprintf(`{"model":"audit-test-model","stream":%t,"stream_options":{"include_usage":true},"messages":[{"role":"system","content":"FORBIDDEN_HTTP_RETRY_SYSTEM"},{"role":"user","content":%q}]}`, tc.stream, marker))
				release := make(chan struct{})
				releases <- release
				// 通道关闭只由测试 goroutine 或随后 cleanup 执行，异常路径也能回收上游。
				unblock := func() {
					select {
					case <-release:
					default:
						close(release)
					}
				}
				t.Cleanup(unblock)
				result := beginHTTPOnce(t, h, t.Context(), body)
				awaitHTTPOnceAttempts(t, attempts, body, tc.switchAccount)
				waitSnapshotRecord(t, h, marker)
				assertHTTPOnceState(t, h, int64(round+1), gaps)
				select {
				case <-result:
					t.Fatal("第二次执行尚未释放却已完成响应")
				default:
				}
				unblock()
				outcome := awaitHTTPOnce(t, result)
				controlRelease := make(chan struct{})
				close(controlRelease)
				releases <- controlRelease
				control := beginHTTPOnce(t, baseline, t.Context(), body)
				awaitHTTPOnceAttempts(t, attempts, body, tc.switchAccount)
				original := awaitHTTPOnce(t, control)
				if outcome.err != nil || original.err != nil || outcome.code != 200 || original.code != outcome.code || !bytes.Equal(original.body, outcome.body) {
					t.Fatal("审计改变了重试后的公开响应")
				}
				if count.Load() != int64(4*(round+1)) {
					t.Fatal("本轮实际执行尝试数与公开场景不符")
				}
				assertHTTPOnceState(t, h, int64(round+1), gaps)
			}
			assertChatPrivacy(t, h, "synthetic-http-retry-account-a", "synthetic-http-retry-account-b", "FORBIDDEN_HTTP_RETRY_SYSTEM", "FORBIDDEN_HTTP_ONCE_REPLY")
			assertHTTPOnceErrorPrivacy(t, h, baseline, "FORBIDDEN_HTTP_RETRY_ERROR")
		})
	}
}

// assertHTTPOnceErrorPrivacy 检查 errorMarker 不在审计持久化目录，并比较开关审计的既有宿主 warning 次数。
// 宿主原有执行失败日志会含上游错误；不擅自改动它或把它当新增审计日志。无返回值，读失败或新增输出终止测试。
func assertHTTPOnceErrorPrivacy(t *testing.T, h, baseline *instance, errorMarker string) {
	t.Helper()
	if err := filepath.WalkDir(filepath.Join(h.dir, "audit"), func(path string, entry os.DirEntry, err error) error {
		if err != nil || entry.IsDir() {
			return err
		}
		raw, err := os.ReadFile(path)
		if err == nil && bytes.Contains(raw, []byte(errorMarker)) {
			t.Fatal("上游错误正文进入审计持久化输出")
		}
		return err
	}); err != nil {
		t.Fatal("上游错误隐私检查不可用")
	}
	enabledLog, err := os.ReadFile(filepath.Join(h.dir, "host.log"))
	if err != nil {
		t.Fatal("审计开启诊断检查不可用")
	}
	originalLog, err := os.ReadFile(filepath.Join(baseline.dir, "host.log"))
	if err != nil {
		t.Fatal("审计关闭诊断检查不可用")
	}
	if bytes.Count(enabledLog, []byte(errorMarker)) != bytes.Count(originalLog, []byte(errorMarker)) {
		t.Fatal("审计额外记录了上游执行错误正文")
	}
}
