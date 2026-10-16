//go:build legacy_patched

// 本文件保留三协议路由前和读体错误的私有观察契约；当前未知模型不可见由 TestOfficialUnknownModelInvisible 验证。
package system

import (
	"bytes"
	"fmt"
	"testing"
	"time"
)

// TestHTTPProtocolsReadFailures 验证各协议路由前仍采集，认证失败不采集，含完整JSON的传输前缀不能冒充完整输入。
func TestHTTPProtocolsReadFailures(t *testing.T) {
	for _, p := range httpAuditProtocols {
		t.Run(p.name, func(t *testing.T) {
			h := startHostWithManagementAuth(t, 1<<30, true, "", true)
			baseline := startHostWithManagementAuth(t, 1<<30, false, "", true)
			h.monitorAll(t)
			_, _, gaps := snapshotCounters(t, h)
			for round := 0; round < 2; round++ {
				marker := fmt.Sprintf("MATRIX-ROUTING-%s-%d-%d", p.name, round, time.Now().UnixNano())
				body := p.input(t, marker, false, "missing")
				code, raw := h.request(t, "POST", p.path, keyA, body)
				originalCode, originalRaw := baseline.request(t, "POST", p.path, keyA, body)
				if code != 400 || code != originalCode || !bytes.Equal(raw, originalRaw) {
					t.Fatal("审计改变了路由前失败")
				}
				waitSnapshotRecord(t, h, marker)
				code, _ = h.request(t, "POST", p.path, "synthetic-invalid-client", p.input(t, "FORBIDDEN_MATRIX_UNAUTHENTICATED", false, "missing"))
				if code != 401 {
					t.Fatal("原宿主认证边界改变")
				}
				partial := p.input(t, "FORBIDDEN_MATRIX_PARTIAL", false, "missing")
				outcome := incompleteHTTPOnce(t, h, partial, p.path)
				original := incompleteHTTPOnce(t, baseline, partial, p.path)
				if outcome.err != nil || original.err != nil || outcome.code != 400 || original.code != outcome.code || !bytes.Equal(original.body, outcome.body) {
					t.Fatal("审计改变了不完整传输错误")
				}
				gaps["read_error"]++
				invalid := p.input(t, "FORBIDDEN_MATRIX_MALFORMED", false, "missing")
				invalid = invalid[:len(invalid)-1]
				code, raw = h.request(t, "POST", p.path, keyA, invalid)
				originalCode, originalRaw = baseline.request(t, "POST", p.path, keyA, invalid)
				if code != originalCode || !bytes.Equal(raw, originalRaw) {
					t.Fatal("审计改变了畸形输入错误")
				}
				gaps["invalid_json"]++
				barrier := fmt.Sprintf("MATRIX-READ-BARRIER-%s-%d-%d", p.name, round, time.Now().UnixNano())
				h.request(t, "POST", p.path, keyA, p.input(t, barrier, false, "missing"))
				waitSnapshotRecord(t, h, barrier)
				assertHTTPOnceState(t, h, int64(2*(round+1)), gaps)
			}
			assertMatrixPrivacy(t, h)
		})
	}
}
