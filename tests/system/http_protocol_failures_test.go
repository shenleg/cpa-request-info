// 本文件补齐三协议重交、认证/路由/读体失败及模型失败与存储故障独立诊断，并检查恢复后的隐私。
package system

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// TestHTTPProtocolsRepeatedSubmission 验证同文本及自报请求/会话标识不能去重，每次公开提交有独立宿主记录。
func TestHTTPProtocolsRepeatedSubmission(t *testing.T) {
	for _, p := range httpAuditProtocols {
		t.Run(p.name, func(t *testing.T) {
			h := startHostWithManagementAuth(t, 1<<30, true, "", true)
			h.monitorAll(t)
			_, _, gaps := snapshotCounters(t, h)
			for round := 0; round < 2; round++ {
				marker := fmt.Sprintf("MATRIX-REPEATED-%s-%d-%d", p.name, round, time.Now().UnixNano())
				body := p.input(t, marker, false, "missing")
				for submission := 0; submission < 2; submission++ {
					req, err := http.NewRequestWithContext(t.Context(), "POST", h.base+p.path, bytes.NewReader(body))
					if err != nil {
						t.Fatal("重交请求构造失败")
					}
					req.Header.Set("Authorization", "Bearer "+keyA)
					req.Header.Set("Content-Type", "application/json")
					req.Header.Set("X-Request-ID", "FORBIDDEN_MATRIX_REQUEST_ID")
					req.Header.Set("X-Session-ID", "FORBIDDEN_MATRIX_SESSION")
					resp, err := h.client.Do(req)
					if err != nil {
						t.Fatal("协议重交请求失败")
					}
					_, readErr := io.Copy(io.Discard, resp.Body)
					closeErr := resp.Body.Close()
					if resp.StatusCode != 400 || readErr != nil || closeErr != nil {
						t.Fatal("重交改变了原路由失败")
					}
					deadline := time.Now().Add(5 * time.Second)
					for {
						ids := map[string]bool{}
						for _, record := range snapshotRecords(t, h) {
							if record.Text == marker {
								ids[record.ID] = true
							}
						}
						if len(ids) == submission+1 {
							break
						}
						if time.Now().After(deadline) {
							t.Fatal("客户端重交被错误去重")
						}
						time.Sleep(20 * time.Millisecond)
					}
				}
				assertHTTPOnceState(t, h, int64(2*(round+1)), gaps)
			}
			assertMatrixPrivacy(t, h)
		})
	}
}

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

// TestHTTPProtocolsStorageFailure 验证各协议模型错误与异步保存故障独立，旧历史/缺口跨恢复保留且禁止内容不落盘。
func TestHTTPProtocolsStorageFailure(t *testing.T) {
	for _, p := range httpAuditProtocols {
		t.Run(p.name, func(t *testing.T) {
			received := make(chan []byte, 4)
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				raw, err := io.ReadAll(r.Body)
				if err != nil {
					w.WriteHeader(400)
					return
				}
				received <- raw
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(400)
				_, _ = io.WriteString(w, httpOnceError)
			}))
			t.Cleanup(upstream.Close)
			h := startHostWithConfiguration(t, 1<<30, true, upstream.URL+"/v1", true, httpOnceNoRetry)
			baseline := startHostWithConfiguration(t, 1<<30, false, upstream.URL+"/v1", true, httpOnceNoRetry)
			h.monitorAll(t)
			marker := fmt.Sprintf("MATRIX-STORAGE-SEED-%s-%d", p.name, time.Now().UnixNano())
			h.request(t, "POST", p.path, keyA, p.input(t, marker, false, "audit-test-model"))
			receiveMatrixUpstream(t, received, marker)
			record := waitSnapshotRecord(t, h, marker)
			_, _, before := snapshotCounters(t, h)
			fault := filepath.Join(h.stateDir, "external-protocol-storage-fault")
			if err := os.WriteFile(fault, []byte("synthetic fault only"), 0400); err != nil {
				t.Fatal("独立存储故障设置失败")
			}
			body := p.input(t, "FORBIDDEN_MATRIX_FAILED_SAVE", false, "audit-test-model")
			code, raw := h.request(t, "POST", p.path, keyA, body)
			actual := receiveMatrixUpstream(t, received, "FORBIDDEN_MATRIX_FAILED_SAVE")
			originalCode, originalRaw := baseline.request(t, "POST", p.path, keyA, body)
			control := receiveMatrixUpstream(t, received, "FORBIDDEN_MATRIX_FAILED_SAVE")
			if code != 400 || originalCode != code || !bytes.Equal(raw, originalRaw) || !bytes.Equal(actual, control) {
				t.Fatal("保存失败改变了原模型错误或完整请求")
			}
			saved, after := waitHTTPOnceGap(t, h, "storage_error", before["storage_error"])
			if saved != 1 || after["storage_error"] != before["storage_error"]+1 {
				t.Fatal("模型失败被错误重复计为采集缺口")
			}
			// 启用实例多执行了种子请求，错误标记的既有warning数量不适合直接相等；补齐基线再比较。
			baseline.request(t, "POST", p.path, keyA, p.input(t, marker, false, "audit-test-model"))
			receiveMatrixUpstream(t, received, marker)
			assertHTTPOnceErrorPrivacy(t, h, baseline, "FORBIDDEN_HTTP_ONCE_ERROR")
			assertMatrixPrivacy(t, h)
			if err := os.Chmod(fault, 0600); err != nil {
				t.Fatal("隔离辅助文件权限恢复失败")
			}
			h.stop()
			restarted := startHostWithManagementAuth(t, 1<<30, true, "", true, h.stateDir)
			code, raw = restarted.request(t, "GET", api+"/detail?id="+record.ID, adminKey, nil)
			var detail savedRecord
			if code != 200 || json.Unmarshal(raw, &detail) != nil || detail.Text != marker || detail.Protocol != p.name {
				t.Fatal("恢复丢失已同步历史")
			}
			history := gapItem(t, readGapAlerts(t, restarted), "storage_error")
			if !history.HistoryRetained || history.Active {
				t.Fatal("恢复抹去历史或错误维持已恢复故障")
			}
			assertMatrixPrivacy(t, restarted, "FORBIDDEN_HTTP_ONCE_ERROR")
		})
	}
}
