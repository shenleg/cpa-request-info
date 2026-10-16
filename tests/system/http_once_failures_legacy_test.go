//go:build legacy_patched

// 本文件保留路由前和读体错误的私有观察契约；当前未知模型不可见见 TestOfficialUnknownModelInvisible，实际畸形回调见 TestResponsesHTTPMetadataAndAmbiguity。
package system

import (
	"bytes"
	"fmt"
	"testing"
	"time"
)

// TestHTTPOnceRoutingAndReadFailures 验证路由前输入仍采集，鉴权拒绝不记录，不完整或畸形提交只报可确定的缺口。
func TestHTTPOnceRoutingAndReadFailures(t *testing.T) {
	h := startHostWithManagementAuth(t, 1<<30, true, "", true)
	baseline := startHostWithManagementAuth(t, 1<<30, false, "", true)
	h.monitorAll(t)
	_, _, gaps := snapshotCounters(t, h)
	for round := 0; round < 2; round++ {
		for index, model := range []string{`"missing"`, `""`, `123`} {
			marker := fmt.Sprintf("HTTP-ROUTE-%d-%d-%d", round, index, time.Now().UnixNano())
			body := []byte(fmt.Sprintf(`{"model":%s,"messages":[{"role":"user","content":%q}]}`, model, marker))
			code, raw := h.request(t, "POST", "/v1/chat/completions", keyA, body)
			originalCode, originalRaw := baseline.request(t, "POST", "/v1/chat/completions", keyA, body)
			if code != 400 || code != originalCode || !bytes.Equal(raw, originalRaw) {
				t.Fatal("观察改变了路由前失败")
			}
			waitSnapshotRecord(t, h, marker)
		}
		unauthorized := snapshotInput(t, "FORBIDDEN_HTTP_UNAUTHENTICATED")
		code, _ := h.request(t, "POST", "/v1/chat/completions", "synthetic-invalid-client", unauthorized)
		if code != 401 {
			t.Fatal("宿主鉴权拒绝行为改变")
		}
		partial := snapshotInput(t, "FORBIDDEN_HTTP_PARTIAL_INPUT")
		outcome := incompleteHTTPOnce(t, h, partial)
		original := incompleteHTTPOnce(t, baseline, partial)
		if outcome.err != nil || original.err != nil || outcome.code != 400 || original.code != outcome.code || !bytes.Equal(original.body, outcome.body) {
			t.Fatal("观察改变了不完整读体的公开错误")
		}
		gaps["read_error"]++
		invalid := []byte(`{"model":"missing","messages":[{"role":"user","content":"FORBIDDEN_HTTP_MALFORMED_INPUT"}]`)
		code, raw := h.request(t, "POST", "/v1/chat/completions", keyA, invalid)
		originalCode, originalRaw := baseline.request(t, "POST", "/v1/chat/completions", keyA, invalid)
		if code != originalCode || !bytes.Equal(raw, originalRaw) {
			t.Fatal("观察改变了畸形输入的公开错误")
		}
		gaps["invalid_json"]++
		// 后续唯一提交落盘是负向断言屏障，不用 sleep 猜测前面的跳过/失败已处理。
		barrier := fmt.Sprintf("HTTP-FAILURE-BARRIER-%d-%d", round, time.Now().UnixNano())
		h.request(t, "POST", "/v1/chat/completions", keyA, snapshotInput(t, barrier))
		waitSnapshotRecord(t, h, barrier)
		assertHTTPOnceState(t, h, int64(4*(round+1)), gaps)
	}
	assertChatPrivacy(t, h, "FORBIDDEN_HTTP_UNAUTHENTICATED", "FORBIDDEN_HTTP_PARTIAL_INPUT", "FORBIDDEN_HTTP_MALFORMED_INPUT")
}
