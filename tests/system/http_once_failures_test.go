// 本文件验证重交、真实客户端取消/超时、不完整接收及路由失败；模型失败与采集缺口分别从公开接口观察。
package system

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// TestHTTPOnceRepeatedClientIDs 验证相同文本、自报请求 ID 和会话信息重交独立保存，两轮不误读历史。
func TestHTTPOnceRepeatedClientIDs(t *testing.T) {
	upstream, received := snapshotUpstream()
	t.Cleanup(upstream.Close)
	h := startHostWithManagementAuth(t, 1<<30, true, upstream.URL+"/v1", true)
	h.monitorAll(t)
	_, _, gaps := snapshotCounters(t, h)
	for round := 0; round < 2; round++ {
		marker := fmt.Sprintf("HTTP-REPEATED-%d-%d", round, time.Now().UnixNano())
		body := []byte(fmt.Sprintf(`{"model":"audit-test-model","request_id":"FORBIDDEN_CLIENT_REQUEST_ID","user":"FORBIDDEN_CLIENT_USER","metadata":{"session_id":"FORBIDDEN_CLIENT_SESSION"},"messages":[{"role":"user","content":%q}]}`, marker))
		ids := map[string]bool{}
		for submission := 0; submission < 2; submission++ {
			req, err := http.NewRequestWithContext(t.Context(), "POST", h.base+"/v1/chat/completions", bytes.NewReader(body))
			if err != nil {
				t.Fatal("重交请求构造失败")
			}
			req.Header.Set("Authorization", "Bearer "+keyA)
			req.Header.Set("Content-Type", "application/json")
			req.Header.Set("X-Request-ID", "FORBIDDEN_CLIENT_REQUEST_ID")
			req.Header.Set("X-Session-ID", "FORBIDDEN_CLIENT_SESSION")
			resp, err := h.client.Do(req)
			if err != nil {
				t.Fatal("重交请求失败")
			}
			_, readErr := io.Copy(io.Discard, resp.Body)
			closeErr := resp.Body.Close()
			if resp.StatusCode != 200 || readErr != nil || closeErr != nil {
				t.Fatal("重交改变了成功模型调用")
			}
			verifySnapshotUpstream(t, received, marker)
			deadline := time.Now().Add(5 * time.Second)
			for {
				for _, record := range snapshotRecords(t, h) {
					if record.Text == marker {
						ids[record.ID] = true
						code, raw := h.request(t, "GET", api+"/detail?id="+record.ID, adminKey, nil)
						var detail savedRecord
						if code != 200 || json.Unmarshal(raw, &detail) != nil || detail.Text != marker || detail.ID != record.ID || detail.ID == "FORBIDDEN_CLIENT_REQUEST_ID" {
							t.Fatal("重交详情或宿主提交标识不符")
						}
					}
				}
				if len(ids) == submission+1 {
					break
				}
				if time.Now().After(deadline) {
					t.Fatal("相同文本或客户端 ID 被错误去重")
				}
				time.Sleep(20 * time.Millisecond)
			}
		}
		assertHTTPOnceState(t, h, int64(2*(round+1)), gaps)
	}
	assertChatPrivacy(t, h, "FORBIDDEN_CLIENT_REQUEST_ID", "FORBIDDEN_CLIENT_USER", "FORBIDDEN_CLIENT_SESSION")
}

// TestHTTPOnceClientCancellation 验证收完整输入后的真实 HTTP 取消/期限超时传到上游，但不撤销输入、不产生采集失败。
func TestHTTPOnceClientCancellation(t *testing.T) {
	for _, mode := range []string{"disconnect", "deadline"} {
		t.Run(mode, func(t *testing.T) {
			received := make(chan []byte, 2)
			canceled := make(chan struct{}, 2)
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
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
			h := startHostWithManagementAuth(t, 1<<30, true, upstream.URL+"/v1", true)
			h.monitorAll(t)
			_, _, gaps := snapshotCounters(t, h)
			for round := 0; round < 2; round++ {
				marker := fmt.Sprintf("HTTP-CANCEL-%s-%d-%d", mode, round, time.Now().UnixNano())
				body := snapshotInput(t, marker)
				ctx, cancel := context.WithCancel(t.Context())
				wantErr := context.Canceled
				if mode == "deadline" {
					cancel()
					// 此期限是待验证的客户端超时行为，不用固定等待制造采集结果。
					ctx, cancel = context.WithTimeout(t.Context(), time.Second)
					wantErr = context.DeadlineExceeded
				}
				t.Cleanup(cancel)
				result := beginHTTPOnce(t, h, ctx, body)
				select {
				case raw := <-received:
					if !bytes.Equal(raw, body) {
						t.Fatal("取消前上游收到的正文不完整")
					}
				case <-time.After(3 * time.Second):
					t.Fatal("取消前未收到完整上游输入")
				}
				if mode == "disconnect" {
					cancel()
				}
				outcome := awaitHTTPOnce(t, result)
				cancel()
				if !errors.Is(outcome.err, wantErr) {
					t.Fatal("真实客户端取消或超时结果不符")
				}
				select {
				case <-canceled:
				case <-time.After(3 * time.Second):
					t.Fatal("宿主没有保持已有上游取消行为")
				}
				waitSnapshotRecord(t, h, marker)
				assertHTTPOnceState(t, h, int64(round+1), gaps)
			}
			assertChatPrivacy(t, h)
		})
	}
}

// incompleteHTTPOnce 经真实 TCP 在100屏障后发送未完整 chunk（内含完整 JSON），半关闭写端触发读体错误。
// 返回公开错误响应；连接或读取失败终止测试，cleanup 仅关闭连接，不保存网络原文。
func incompleteHTTPOnce(t *testing.T, h *instance, body []byte) httpOnceOutcome {
	t.Helper()
	u, err := url.Parse(h.base)
	if err != nil {
		t.Fatal("不完整提交地址无效")
	}
	conn, err := net.DialTimeout("tcp", u.Host, 5*time.Second)
	if err != nil {
		t.Fatal("不完整提交连接失败")
	}
	defer func() { _ = conn.Close() }()
	if err := conn.SetDeadline(time.Now().Add(5 * time.Second)); err != nil {
		t.Fatal("不完整提交期限设置失败")
	}
	// chunked 避免日志中间件鉴权前读体；100此时证明请求已进入原协议读体边界。
	if _, err := fmt.Fprintf(conn, "POST /v1/chat/completions HTTP/1.1\r\nHost: %s\r\nAuthorization: Bearer %s\r\nContent-Type: application/json\r\nTransfer-Encoding: chunked\r\nExpect: 100-continue\r\nConnection: close\r\n\r\n", u.Host, keyA); err != nil {
		t.Fatal("不完整提交头发送失败")
	}
	reader := bufio.NewReader(conn)
	req := &http.Request{Method: "POST"}
	response, err := http.ReadResponse(reader, req)
	if err != nil || response.StatusCode != 100 {
		t.Fatal("不完整提交未进入认证后读体边界")
	}
	_ = response.Body.Close()
	// 即便收到的前缀是有效 JSON，也不能把尚欠传输字节的请求捏造成完整输入。
	if _, err := fmt.Fprintf(conn, "%x\r\n%s", len(body)+5, body); err != nil {
		t.Fatal("不完整提交前缀发送失败")
	}
	if err := conn.(*net.TCPConn).CloseWrite(); err != nil {
		t.Fatal("不完整提交半关闭失败")
	}
	response, err = http.ReadResponse(reader, req)
	if err != nil {
		t.Fatal("不完整提交最终响应读取失败")
	}
	raw, readErr := io.ReadAll(response.Body)
	_ = response.Body.Close()
	return httpOnceOutcome{code: response.StatusCode, body: raw, err: readErr}
}

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

// waitHTTPOnceGap 等待管理状态出现本轮固定原因的新增缺口；返回状态计数，失败或超时终止测试。
func waitHTTPOnceGap(t *testing.T, h *instance, reason string, before uint64) (int64, map[string]uint64) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		saved, queued, gaps := snapshotCounters(t, h)
		if gaps[reason] > before && queued == 0 {
			return saved, gaps
		}
		if time.Now().After(deadline) {
			t.Fatal("本轮采集失败没有公开缺口")
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// TestHTTPOnceStorageFailureIndependent 验证异步保存失败单独报告，即使同一提交的模型也失败，公开错误仍与未加载审计一致。
func TestHTTPOnceStorageFailureIndependent(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(400)
		_, _ = io.WriteString(w, httpOnceError)
	}))
	t.Cleanup(upstream.Close)
	h := startHostWithManagementAuth(t, 1<<30, true, upstream.URL+"/v1", true)
	baseline := startHostWithManagementAuth(t, 1<<30, false, upstream.URL+"/v1", true)
	h.monitorAll(t)
	seed := fmt.Sprintf("HTTP-FAULT-SEED-%d", time.Now().UnixNano())
	h.request(t, "POST", "/v1/chat/completions", keyA, snapshotInput(t, seed))
	waitSnapshotRecord(t, h, seed)
	_, _, before := snapshotCounters(t, h)
	// 外部辅助文件违反专用目录权限要求；不用私有存储方法或生产故障 API 控制 writer。
	if err := os.WriteFile(filepath.Join(h.dir, "audit", "external-http-storage-fault"), []byte("synthetic fault only"), 0400); err != nil {
		t.Fatal("隔离存储故障设置失败")
	}
	body := snapshotInput(t, "FORBIDDEN_HTTP_FAILED_SAVE")
	code, raw := h.request(t, "POST", "/v1/chat/completions", keyA, body)
	originalCode, originalRaw := baseline.request(t, "POST", "/v1/chat/completions", keyA, body)
	if code != 400 || code != originalCode || !bytes.Equal(raw, originalRaw) {
		t.Fatal("存储故障改变或拒绝了原模型错误")
	}
	saved, after := waitHTTPOnceGap(t, h, "storage_error", before["storage_error"])
	if saved != 1 || after["storage_error"] != before["storage_error"]+1 {
		t.Fatal("异步保存失败被冒充保存或重复计为模型失败")
	}
	delete(after, "storage_error")
	delete(before, "storage_error")
	if !maps.Equal(before, after) {
		t.Fatal("模型错误被计为其他采集故障")
	}
	assertChatPrivacy(t, h, "FORBIDDEN_HTTP_FAILED_SAVE", "FORBIDDEN_HTTP_ONCE_ERROR")
}
