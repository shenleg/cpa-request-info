// 本文件从三协议的真实native请求拦截发起内部执行，验证内部输入不成为额外客户端审计记录。
package system

import (
	"bytes"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// TestHTTPProtocolsNestedExecution 验证每个协议在原HTTP context内发生内部和客户端两次执行，却只保存一次客户端输入。
func TestHTTPProtocolsNestedExecution(t *testing.T) {
	withAudit, withoutAudit := buildHTTPOnceFixture(t)
	for _, p := range httpAuditProtocols {
		t.Run(p.name, func(t *testing.T) {
			received := make(chan []byte, 8)
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				raw, err := io.ReadAll(r.Body)
				if err != nil {
					w.WriteHeader(400)
					return
				}
				received <- raw
				w.Header().Set("Content-Type", "application/json")
				_, _ = io.WriteString(w, httpMatrixReply)
			}))
			t.Cleanup(upstream.Close)
			pluginConfig := "    http-once-fixture:\n      enabled: true\n"
			h := startHostWithPluginDirectory(t, 1<<30, true, upstream.URL+"/v1", true, "", withAudit, pluginConfig)
			baseline := startHostWithPluginDirectory(t, 1<<30, true, upstream.URL+"/v1", true, "", withoutAudit, pluginConfig)
			h.monitorAll(t)
			_, _, gaps := snapshotCounters(t, h)
			for round := 0; round < 2; round++ {
				marker := fmt.Sprintf("HTTP-NESTED-MATRIX-%s-%d-%d", p.name, round, time.Now().UnixNano())
				body := p.input(t, marker, false, "audit-test-model")
				code, raw := h.request(t, "POST", p.path, keyA, body)
				originalCode, originalRaw := baseline.request(t, "POST", p.path, keyA, body)
				if code != 200 || code != originalCode || !bytes.Equal(raw, originalRaw) {
					t.Fatal("审计改变了嵌套执行后的公开调用")
				}
				nested := []byte(fmt.Sprintf(`{"messages":[{"content":%q,"role":"user"}],"model":"audit-test-model"}`, "FORBIDDEN_NESTED_EXECUTION-"+marker))
				var audited []byte
				for index := 0; index < 4; index++ {
					select {
					case actual := <-received:
						if index%2 == 0 {
							if !bytes.Equal(actual, nested) {
								t.Fatal("原协议未触发真实内部模型执行")
							}
						} else {
							assertMatrixUpstream(t, actual, marker)
							if index == 1 {
								audited = actual
							} else if !bytes.Equal(audited, actual) {
								t.Fatal("嵌套执行的原始请求被审计改变")
							}
						}
					case <-time.After(4 * time.Second):
						t.Fatal("未观察到本轮内部和客户端两次执行")
					}
				}
				waitSnapshotRecord(t, h, marker)
				assertHTTPOnceState(t, h, int64(round+1), gaps)
			}
			assertMatrixPrivacy(t, h, "FORBIDDEN_NESTED_EXECUTION")
		})
	}
}
