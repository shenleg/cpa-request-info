//go:build officialprobe

// 本文件以未经补丁修改的官方宿主验证实验探针；只保存合成标记、计数与摘要，不改变正式审计测试。
package system

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

const officialProbeAPI = "/v0/management/plugins/official-probe/report"

type officialProbeEvent struct {
	Phase              string   `json:"phase"`
	RequestID          string   `json:"request_id"`
	TraceID            string   `json:"trace_id"`
	SourceFormat       string   `json:"source_format"`
	Model              string   `json:"model"`
	RequestedModel     string   `json:"requested_model"`
	BodyModel          string   `json:"body_model"`
	CallerScopeHash    string   `json:"caller_scope_hash"`
	BodySHA256         string   `json:"body_sha256"`
	Markers            []string `json:"markers"`
	HasSystem          bool     `json:"has_system"`
	HasHistory         bool     `json:"has_history"`
	HasTools           bool     `json:"has_tools"`
	CredentialsPresent bool     `json:"credentials_present"`
	Outcome            string   `json:"outcome"`
}

type officialProbeReport struct {
	ExtensionProbe string               `json:"extension_probe"`
	Events         []officialProbeEvent `json:"events"`
	Dropped        int                  `json:"dropped"`
}

type officialProbeObservation struct {
	Scenario     string               `json:"scenario"`
	Round        int                  `json:"round"`
	Sent         int                  `json:"sent"`
	Upstream     int                  `json:"upstream"`
	HTTPStatus   int                  `json:"http_status,omitempty"`
	InputSHA256  string               `json:"input_sha256,omitempty"`
	Before       int                  `json:"before"`
	After        int                  `json:"after"`
	Complete     int                  `json:"complete"`
	ExecutionIDs int                  `json:"execution_ids"`
	TraceIDs     int                  `json:"trace_ids"`
	Events       []officialProbeEvent `json:"events"`
	Note         string               `json:"note,omitempty"`
}

// officialProbeHost 启动指定官方产物与独立探针，额外配置只作用于合成实例；启动或探针握手失败终止测试。
func officialProbeHost(t *testing.T, out, upstream, extra string, nested bool) *instance {
	t.Helper()
	plugins := "plugins"
	pluginConfig := "    official-probe:\n      enabled: true\n      priority: 100\n"
	if nested {
		plugins = "nested-plugins"
		pluginConfig += "    http-once-fixture:\n      enabled: true\n      priority: 200\n"
	}
	h := startHostWithRuntime(t, 1<<30, true, upstream, false, extra, filepath.Join(out, plugins), pluginConfig, filepath.Join(out, "bin/cliproxyapi"), "")
	report := officialProbeRead(t, h, false)
	if report.ExtensionProbe != "unsupported" {
		t.Fatalf("被测宿主并非已确认缺少自定义扩展的官方版：%s", report.ExtensionProbe)
	}
	code, raw := h.request(t, "GET", "/v0/resource/plugins/official-probe/probe.txt", "", nil)
	if code != http.StatusOK || strings.Contains(string(raw), "events") {
		t.Fatal("探针无数据静态资源未加载或错误暴露观测结果")
	}
	for _, key := range []string{"", keyA, keyB} {
		status, _ := h.request(t, "GET", officialProbeAPI, key, nil)
		if status != http.StatusUnauthorized && status != http.StatusForbidden {
			t.Fatalf("探针结果接口未受宿主管理鉴权保护：%d", status)
		}
	}
	return h
}

// officialProbeRead 读取有限报告；settled要求已观察执行均出现异步完成通知，等待失败终止测试，不读取宿主日志。
func officialProbeRead(t *testing.T, h *instance, settled bool) officialProbeReport {
	t.Helper()
	deadline := time.Now().Add(8 * time.Second)
	for {
		code, raw := h.request(t, "GET", officialProbeAPI, adminKey, nil)
		var report officialProbeReport
		if code == 200 && json.Unmarshal(raw, &report) == nil {
			for _, secret := range []string{keyA, keyB, adminKey, "synthetic-upstream-only", "FORBIDDEN_MATRIX_", "FORBIDDEN_PROBE_", "FORBIDDEN_NESTED_EXECUTION"} {
				if bytes.Contains(raw, []byte(secret)) {
					t.Fatal("实验报告保存了凭据、历史或工具/指令正文")
				}
			}
			if report.Dropped != 0 {
				t.Fatal("探针有界缓冲已溢出，不能据此判断接口覆盖")
			}
			pending := map[string]bool{}
			for _, event := range report.Events {
				if event.Phase == "before" {
					pending[event.RequestID] = true
				}
			}
			for _, event := range report.Events {
				if event.Phase == "complete" {
					delete(pending, event.RequestID)
				}
			}
			if !settled || len(pending) == 0 {
				return report
			}
		}
		if time.Now().After(deadline) {
			t.Fatal("官方探针未就绪或执行完成通知未到达")
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// officialProbeDelta 将当前场景的事件与公开客户端/上游计数汇总；不推断没有回调的请求身份。
func officialProbeDelta(t *testing.T, h *instance, offset int, scenario string, round, sent, upstream, status int, body []byte) officialProbeObservation {
	t.Helper()
	// duplex 的执行生命周期跨多条消息，不能在连接仍活跃时等待 complete。
	settled := !strings.HasPrefix(scenario, "ws_duplex_response.")
	report := officialProbeRead(t, h, settled)
	observation := officialProbeObservation{Scenario: scenario, Round: round, Sent: sent, Upstream: upstream, HTTPStatus: status, Events: slices.Clone(report.Events[offset:])}
	if body != nil {
		sum := sha256.Sum256(body)
		observation.InputSHA256 = hex.EncodeToString(sum[:])
	}
	ids, traces := map[string]bool{}, map[string]bool{}
	for _, event := range observation.Events {
		switch event.Phase {
		case "before":
			observation.Before++
		case "after":
			observation.After++
		case "complete":
			observation.Complete++
		}
		if event.RequestID != "" {
			ids[event.RequestID] = true
		}
		if event.TraceID != "" {
			traces[event.TraceID] = true
		}
	}
	observation.ExecutionIDs, observation.TraceIDs = len(ids), len(traces)
	t.Logf("%s round=%d sent=%d upstream=%d before=%d after=%d complete=%d executions=%d traces=%d", scenario, round, sent, upstream, observation.Before, observation.After, observation.Complete, len(ids), len(traces))
	return observation
}

// officialProbeReceive 等待独立上游屏障，并确认本轮标记实际到达；返回内存正文，超时或标记不符终止测试。
func officialProbeReceive(t *testing.T, received <-chan []byte, marker string) []byte {
	t.Helper()
	select {
	case raw := <-received:
		if !bytes.Contains(raw, []byte(marker)) {
			t.Fatal("本轮上游屏障收到其他请求，不能作为覆盖证据")
		}
		return raw
	case <-time.After(5 * time.Second):
		t.Fatal("本轮输入未到达本地合成上游")
		return nil
	}
}

// officialProbeHTTPUpstream 提供固定非流式/SSE成功响应与独立接收通道，正文只在内存保存；调用者关闭服务。
func officialProbeHTTPUpstream() (*httptest.Server, <-chan []byte) {
	received := make(chan []byte, 64)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, err := io.ReadAll(r.Body)
		if err != nil {
			w.WriteHeader(400)
			return
		}
		received <- raw
		if bytes.Contains(raw, []byte(`"stream":true`)) {
			w.Header().Set("Content-Type", "text/event-stream")
			_, _ = io.WriteString(w, httpMatrixSSE)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, httpMatrixReply)
	}))
	return server, received
}

// officialProbeInput 在既有协议夹具中加入有效工具定义；仅返回合成正文，编码失败终止测试。
func officialProbeInput(t *testing.T, protocol httpAuditProtocol, marker string, stream bool) []byte {
	t.Helper()
	var body map[string]any
	if json.Unmarshal(protocol.input(t, marker, stream, "audit-test-model"), &body) != nil {
		t.Fatal("协议实验夹具解码失败")
	}
	definition := map[string]any{"name": "probe_tool", "description": "FORBIDDEN_PROBE_TOOL", "parameters": map[string]any{"type": "object", "properties": map[string]any{}}}
	switch protocol.name {
	case "chat-completions":
		body["tools"] = []any{map[string]any{"type": "function", "function": definition}}
	case "responses":
		definition["type"] = "function"
		body["tools"] = []any{definition}
	case "anthropic-messages":
		definition["input_schema"] = definition["parameters"]
		delete(definition, "parameters")
		body["tools"] = []any{definition}
	}
	raw, err := json.Marshal(body)
	if err != nil {
		t.Fatal("协议实验夹具编码失败")
	}
	return raw
}

// TestOfficialProbe 实测官方插件的入站替代能力；基础设施错误失败，覆盖不足作为明确测量结果而非静默通过。
func TestOfficialProbe(t *testing.T) {
	out := os.Getenv("CPA_OFFICIAL_PROBE_DIR")
	if out == "" {
		t.Fatal("请使用 make verify-official-probe 构建并运行精确官方宿主")
	}
	var observations []officialProbeObservation
	t.Cleanup(func() {
		raw, err := json.MarshalIndent(struct {
			Platform      string                     `json:"platform"`
			HarnessPassed bool                       `json:"harness_passed"`
			Observations  []officialProbeObservation `json:"observations"`
		}{runtime.GOOS + "/" + runtime.GOARCH, !t.Failed(), observations}, "", "  ")
		if err != nil || os.WriteFile(filepath.Join(out, "result.json"), raw, 0600) != nil {
			t.Error("实验有限结果保存失败")
		}
	})

	t.Run("http_protocols_identity_and_repeated_input", func(t *testing.T) {
		upstream, received := officialProbeHTTPUpstream()
		t.Cleanup(upstream.Close)
		h := officialProbeHost(t, out, upstream.URL+"/v1", httpOnceNoRetry, false)
		for _, protocol := range httpAuditProtocols {
			for round := 0; round < 2; round++ {
				marker := fmt.Sprintf("PROBE-HTTP-%s-%d", protocol.name, round)
				for _, stream := range []bool{false, true} {
					body := officialProbeInput(t, protocol, marker, stream)
					offset := len(officialProbeRead(t, h, true).Events)
					code, _ := h.request(t, "POST", protocol.path, keyA, body)
					if code != 200 {
						t.Fatalf("官方三协议合成调用失败：%s status=%d", protocol.name, code)
					}
					officialProbeReceive(t, received, marker)
					observations = append(observations, officialProbeDelta(t, h, offset, fmt.Sprintf("http_%s_stream_%t", protocol.name, stream), round, 1, 1, code, body))
				}
			}
		}
		for round := 0; round < 2; round++ {
			// 宿主已有的 DD 模型形式会在 Messages handler 中解码，检验回调是否仍保留原客户端模型。
			body := httpAuditProtocols[2].input(t, fmt.Sprintf("PROBE-MODEL-REWRITE-%d", round), false, "claude-fable-5-dd-ledom-tset-tidua")
			offset := len(officialProbeRead(t, h, true).Events)
			code, _ := h.request(t, "POST", "/v1/messages", keyA, body)
			if code != 200 {
				t.Fatal("DD 模型改写场景未到达模型执行")
			}
			officialProbeReceive(t, received, fmt.Sprintf("PROBE-MODEL-REWRITE-%d", round))
			observations = append(observations, officialProbeDelta(t, h, offset, "messages_model_rewrite", round, 1, 1, code, body))
		}
		for round := 0; round < 2; round++ {
			body := httpAuditProtocols[0].input(t, fmt.Sprintf("PROBE-SAME-TEXT-%d", round), false, "audit-test-model")
			for index, key := range []string{keyA, keyA, keyB} {
				offset := len(officialProbeRead(t, h, true).Events)
				code, _ := h.request(t, "POST", "/v1/chat/completions", key, body)
				if code != 200 {
					t.Fatal("身份/重交合成请求失败")
				}
				officialProbeReceive(t, received, fmt.Sprintf("PROBE-SAME-TEXT-%d", round))
				observations = append(observations, officialProbeDelta(t, h, offset, fmt.Sprintf("identity_repeat_%d", index), round, 1, 1, code, body))
			}
		}
		for round := 0; round < 2; round++ {
			body := httpAuditProtocols[0].input(t, fmt.Sprintf("PROBE-ROUTE-FAIL-%d", round), false, "nonexistent-probe-model")
			offset := len(officialProbeRead(t, h, true).Events)
			code, _ := h.request(t, "POST", "/v1/chat/completions", keyA, body)
			if code < 400 {
				t.Fatal("路由前失败场景未实际失败")
			}
			select {
			case <-received:
				t.Fatal("路由前失败请求意外到达上游")
			default:
			}
			observations = append(observations, officialProbeDelta(t, h, offset, "pre_route_failure", round, 1, 0, code, body))
		}
	})

	t.Run("retry_and_account_switch", func(t *testing.T) {
		for _, switchAccount := range []bool{false, true} {
			t.Run(fmt.Sprintf("switch_%t", switchAccount), func(t *testing.T) {
				attempts := make(chan httpOnceAttempt, 8)
				var count atomic.Int64
				upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					raw, err := io.ReadAll(r.Body)
					if err != nil {
						w.WriteHeader(400)
						return
					}
					attempts <- httpOnceAttempt{raw, r.Header.Get("Authorization")}
					w.Header().Set("Content-Type", "application/json")
					if count.Add(1)%2 == 1 {
						w.WriteHeader(503)
						// invalid_request_error 会被官方标记为不可重试，503 必须配合真实的 server_error。
						_, _ = io.WriteString(w, `{"error":{"message":"synthetic probe retry","type":"server_error"}}`)
						return
					}
					_, _ = io.WriteString(w, httpMatrixReply)
				}))
				t.Cleanup(upstream.Close)
				keys, retries := "      - api-key: synthetic-probe-upstream-a\n", 1
				if switchAccount {
					keys += "      - api-key: synthetic-probe-upstream-b\n"
					retries = 0
				}
				config := fmt.Sprintf("openai-compatibility:\n  - name: controlled-probe\n    base-url: %q\n    api-key-entries:\n%s    models:\n      - name: audit-test-model\n        alias: audit-test-model\nrouting:\n  retry:\n    request-retry: %d\n    max-retry-credentials: 0\n    max-retry-interval: 0\n  cooldown:\n    disable-cooling: true\n", upstream.URL+"/v1", keys, retries)
				h := officialProbeHost(t, out, "", config, false)
				for round := 0; round < 2; round++ {
					body := snapshotInput(t, fmt.Sprintf("PROBE-RETRY-%t-%d", switchAccount, round))
					offset := len(officialProbeRead(t, h, true).Events)
					code, _ := h.request(t, "POST", "/v1/chat/completions", keyA, body)
					if code != 200 {
						t.Fatalf("实际重试/切换后未成功：%d", code)
					}
					awaitHTTPOnceAttempts(t, attempts, body, switchAccount)
					observations = append(observations, officialProbeDelta(t, h, offset, fmt.Sprintf("retry_switch_%t", switchAccount), round, 1, 2, code, body))
				}
			})
		}
	})

	t.Run("nested_execution", func(t *testing.T) {
		upstream, received := officialProbeHTTPUpstream()
		t.Cleanup(upstream.Close)
		h := officialProbeHost(t, out, upstream.URL+"/v1", httpOnceNoRetry, true)
		for round := 0; round < 2; round++ {
			marker := fmt.Sprintf("HTTP-NESTED-PROBE-NESTED-%d", round)
			body := snapshotInput(t, marker)
			offset := len(officialProbeRead(t, h, true).Events)
			code, _ := h.request(t, "POST", "/v1/chat/completions", keyA, body)
			if code != 200 {
				t.Fatal("真实嵌套执行调用未成功")
			}
			nested := officialProbeReceive(t, received, marker)
			original := officialProbeReceive(t, received, marker)
			if !bytes.Contains(nested, []byte("FORBIDDEN_NESTED_EXECUTION-")) || !bytes.Equal(original, body) {
				t.Fatal("嵌套和原始执行未按预期分别到达上游")
			}
			observations = append(observations, officialProbeDelta(t, h, offset, "nested_execution", round, 1, 2, code, body))
		}
	})

	t.Run("execution_failure_and_cancel", func(t *testing.T) {
		for _, cancelRequest := range []bool{false, true} {
			t.Run(fmt.Sprintf("cancel_%t", cancelRequest), func(t *testing.T) {
				received := make(chan []byte, 4)
				release := make(chan struct{})
				upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					raw, err := io.ReadAll(r.Body)
					if err != nil {
						return
					}
					received <- raw
					if cancelRequest {
						select {
						case <-r.Context().Done():
						case <-release:
						}
						return
					}
					w.Header().Set("Content-Type", "application/json")
					w.WriteHeader(400)
					_, _ = io.WriteString(w, httpOnceError)
				}))
				t.Cleanup(upstream.Close)
				t.Cleanup(func() { close(release) })
				h := officialProbeHost(t, out, upstream.URL+"/v1", httpOnceNoRetry, false)
				for round := 0; round < 2; round++ {
					marker := fmt.Sprintf("PROBE-FAIL-CANCEL-%t-%d", cancelRequest, round)
					body := snapshotInput(t, marker)
					offset := len(officialProbeRead(t, h, true).Events)
					ctx, cancel := context.WithCancel(t.Context())
					result := beginHTTPOnce(t, h, ctx, body)
					officialProbeReceive(t, received, marker)
					if cancelRequest {
						cancel()
					}
					outcome := awaitHTTPOnce(t, result)
					cancel()
					if cancelRequest && outcome.err == nil || !cancelRequest && (outcome.err != nil || outcome.code != 400) {
						t.Fatal("执行失败/客户端取消场景未真实发生")
					}
					observations = append(observations, officialProbeDelta(t, h, offset, fmt.Sprintf("execution_cancel_%t", cancelRequest), round, 1, 1, outcome.code, body))
				}
			})
		}
	})

	t.Run("websocket_normal", func(t *testing.T) {
		upstream, received := auditWebsocketUpstream()
		t.Cleanup(upstream.Close)
		h := officialProbeHost(t, out, upstream.URL+"/v1", httpOnceNoRetry, false)
		conn := dialAuditWebsocket(t, h, "/v1/responses", keyA)
		for round := 0; round < 2; round++ {
			for message := 0; message < 2; message++ {
				marker := fmt.Sprintf("PROBE-WS-NORMAL-%d-%d", round, message)
				body := []byte(fmt.Sprintf(`{"type":"response.create","model":"responses-client-model","input":[{"role":"user","content":%q}]}`, marker))
				offset := len(officialProbeRead(t, h, true).Events)
				sendAuditFrame(t, conn, string(body))
				frames := readAuditEvents(t, conn)
				if !bytes.Contains(frames[len(frames)-1], []byte("response.completed")) {
					t.Fatal("普通WS提交未完成")
				}
				officialProbeReceive(t, received, marker)
				observations = append(observations, officialProbeDelta(t, h, offset, fmt.Sprintf("ws_normal_message_%d", message), round, 1, 1, 0, body))
			}
		}
		_ = conn.Close()
	})

	t.Run("websocket_duplex", func(t *testing.T) {
		upstream, received := codexAuditUpstream()
		t.Cleanup(upstream.Close)
		h := officialProbeHost(t, out, "", codexAuditConfiguration(upstream.URL), false)
		for round := 0; round < 2; round++ {
			conn := dialAuditWebsocket(t, h, "/v1/responses", keyA)
			var previous string
			for message, kind := range []string{"response.create", "response.append", "response.steer"} {
				marker := fmt.Sprintf("PROBE-WS-DUPLEX-%d-%d", round, message)
				body := []byte(fmt.Sprintf(`{"type":%q,"model":"ws-codex-model","previous_response_id":%q,"input":[{"role":"user","content":%q}]}`, kind, previous, marker))
				offset := len(officialProbeRead(t, h, false).Events)
				sendAuditFrame(t, conn, string(body))
				if kind == "response.steer" {
					readAuditSteer(t, conn)
				} else {
					frames := readAuditEvents(t, conn)
					var completed struct {
						Type     string
						Response struct{ ID string }
					}
					if json.Unmarshal(frames[len(frames)-1], &completed) != nil || completed.Type != "response.completed" || completed.Response.ID == "" {
						t.Fatal("duplex提交未获得真实完成事件")
					}
					previous = completed.Response.ID
				}
				officialProbeReceive(t, received, marker)
				observations = append(observations, officialProbeDelta(t, h, offset, "ws_duplex_"+kind, round, 1, 1, 0, body))
			}
			offset := len(officialProbeRead(t, h, false).Events)
			_ = conn.Close()
			observations = append(observations, officialProbeDelta(t, h, offset, "ws_duplex_connection_closed", round, 0, 0, 0, nil))
		}
	})
}
