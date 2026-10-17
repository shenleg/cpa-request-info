// 本文件经真实官方宿主提交合成用途头，验收副本分类、匿名发现及上游不变；不证明真实客户端已发送这些标记。
package system

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"maps"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"
)

// requestWithPurposeHeaders 提交指定身份、路由、正文与合成用途头，返回状态及完整响应；构造、网络或读体错误终止测试。
// 逐值复制保留重复头测试，不修改调用者的头；不输出头值、凭据或响应正文。
func requestWithPurposeHeaders(t *testing.T, h *instance, path, key string, body []byte, headers http.Header) (int, []byte) {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost, h.base+path, bytes.NewReader(body))
	if err != nil {
		t.Fatal("用途请求构造失败")
	}
	req.Header = headers.Clone()
	if req.Header == nil {
		req.Header = make(http.Header)
	}
	req.Header.Set("Authorization", "Bearer "+key)
	req.Header.Set("Content-Type", "application/json")
	response, err := h.client.Do(req)
	if err != nil {
		t.Fatal("用途请求执行失败")
	}
	defer func() {
		if err := response.Body.Close(); err != nil {
			t.Error("用途响应关闭失败")
		}
	}()
	raw, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatal("用途响应读取失败")
	}
	return response.StatusCode, raw
}

// waitPurposeReady 等待 h 的插件管理接口可用；仅查询状态，不发送发现请求，失败或超时终止测试，成功无返回值。
func waitPurposeReady(t *testing.T, h *instance) {
	t.Helper()
	// 原始宿主启动器只等待根路径；首次辅助发现必须先等插件，而不能用模型请求预热匿名目录。
	deadline := time.Now().Add(15 * time.Second)
	for {
		code, raw := h.request(t, http.MethodGet, api+"/status", adminKey, nil)
		var status struct {
			Available bool `json:"available"`
		}
		if code == http.StatusOK {
			if json.Unmarshal(raw, &status) != nil {
				t.Fatal("用途插件就绪状态无效")
			}
			if status.Available {
				return
			}
		} else if code != http.StatusNotFound {
			t.Fatalf("用途插件就绪查询失败: HTTP=%d", code)
		}
		if time.Now().After(deadline) {
			t.Fatalf("用途插件未就绪: HTTP=%d", code)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// assertSavedPurpose 按本轮 marker 查询唯一记录并核对列表与详情的用途、身份、协议和正文；返回详情，不符终止测试。
// 调用者已等待唯一 FIFO 屏障，负向结果不依赖固定等待；marker 与 wantText 分开以支持长模板和截断预览。
func assertSavedPurpose(t *testing.T, h *instance, marker, wantText, purpose, protocol string) savedRecord {
	t.Helper()
	page := searchRecords(t, h, url.Values{"q": {marker}, "limit": {"100"}})
	if page.Total != 1 || len(page.Records) != 1 {
		t.Fatalf("用途列表没有本轮唯一记录: matches=%d", page.Total)
	}
	item := page.Records[0]
	if item.Purpose != purpose || item.Protocol != protocol {
		t.Fatalf("用途列表分类不符: purpose=%q want=%q protocol=%q", item.Purpose, purpose, item.Protocol)
	}
	code, raw := h.request(t, http.MethodGet, api+"/detail?id="+url.QueryEscape(item.ID), adminKey, nil)
	var record savedRecord
	if code != http.StatusOK || json.Unmarshal(raw, &record) != nil || record.ID != item.ID || record.ObjectID != item.ObjectID || record.Purpose != purpose || record.Protocol != protocol || record.Text != wantText {
		t.Fatal("用途详情与列表或独立正文预期不符")
	}
	return record
}

// TestHTTPRequestPurposeProtocols 验证三协议 HTTP/SSE 的用途分类、合法父子组合与损坏/冲突保留，并逐字节比较审计开关前后的上游及响应。
func TestHTTPRequestPurposeProtocols(t *testing.T) {
	cases := []struct {
		name, purpose string
		headers       http.Header
	}{
		{"main", "main", http.Header{"x-ClAuDe-CoDe-ReQuEsT-ClAsS": {"main"}}},
		{"turn", "turn", http.Header{"x-codex-turn-metadata": {`{"request_kind":"turn","thread_id":"FORBIDDEN_PURPOSE_METADATA"}`}}},
		{"missing", "unknown", nil},
		{"unknown_claude", "unknown", http.Header{"x-claude-code-request-class": {"future-class"}}},
		{"unknown_codex", "unknown", http.Header{"x-codex-turn-metadata": {`{"request_kind":"future-kind"}`}}},
		{"malformed_codex", "unknown", http.Header{"x-codex-turn-metadata": {`{"request_kind":`}}},
		{"main_and_turn", "main", http.Header{"x-claude-code-request-class": {"main"}, "x-codex-turn-metadata": {`{"request_kind":"turn"}`}}},
		{"main_and_compaction", "unknown", http.Header{"x-claude-code-request-class": {"main"}, "x-codex-turn-metadata": {`{"request_kind":"compaction"}`}}},
		{"turn_and_auxiliary", "unknown", http.Header{"x-claude-code-request-class": {"auxiliary"}, "x-codex-turn-metadata": {`{"request_kind":"turn"}`}}},
		{"main_and_bad_codex", "unknown", http.Header{"x-claude-code-request-class": {"main"}, "x-codex-turn-metadata": {`not-json`}}},
		{"unknown_and_turn", "unknown", http.Header{"x-claude-code-request-class": {"future-class"}, "x-codex-turn-metadata": {`{"request_kind":"turn"}`}}},
		{"same_class_twice", "main", http.Header{"x-claude-code-request-class": {"main", "main"}}},
		{"conflicting_class", "unknown", http.Header{"x-claude-code-request-class": {"main", "auxiliary"}}},
		{"same_metadata_twice", "turn", http.Header{"x-codex-turn-metadata": {`{"request_kind":"turn"}`, `{"request_kind":"turn"}`}}},
		{"conflicting_metadata", "unknown", http.Header{"x-codex-turn-metadata": {`{"request_kind":"turn"}`, `{"request_kind":"compaction"}`}}},
		{"empty_agent_id", "unknown", http.Header{"x-claude-code-agent-id": {" "}}},
		{"claude_subagent", "", http.Header{"x-claude-code-request-class": {"subagent"}}},
		{"claude_workflow", "", http.Header{"x-claude-code-request-class": {"workflow"}}},
		{"claude_compaction", "", http.Header{"x-claude-code-request-class": {"compaction"}}},
		{"claude_auxiliary", "", http.Header{"x-claude-code-request-class": {"auxiliary"}}},
		{"codex_prewarm", "", http.Header{"x-codex-turn-metadata": {`{"request_kind":"prewarm"}`}}},
		{"codex_compaction", "", http.Header{"x-codex-turn-metadata": {`{"request_kind":"compaction"}`}}},
		{"codex_memory", "", http.Header{"x-codex-turn-metadata": {`{"request_kind":"memory"}`}}},
		{"main_with_agent", "", http.Header{"x-claude-code-request-class": {"main"}, "x-claude-code-agent-id": {"FORBIDDEN_PURPOSE_AGENT"}}},
		{"turn_with_subagent", "", http.Header{"x-codex-turn-metadata": {`{"request_kind":"turn"}`}, "x-openai-subagent": {"FORBIDDEN_PURPOSE_SUBAGENT"}}},
		{"bad_metadata_with_subagent", "", http.Header{"x-codex-turn-metadata": {`not-json`}, "x-openai-subagent": {"FORBIDDEN_PURPOSE_SUBAGENT"}}},
	}
	for _, p := range httpAuditProtocols {
		t.Run(p.name, func(t *testing.T) {
			received := make(chan []byte, 4)
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if respondDiscovery(w, r) {
					return
				}
				raw, err := io.ReadAll(r.Body)
				var request struct{ Stream bool }
				if err != nil || json.Unmarshal(raw, &request) != nil {
					w.WriteHeader(http.StatusBadRequest)
					return
				}
				received <- raw
				if request.Stream {
					w.Header().Set("Content-Type", "text/event-stream")
					_, _ = io.WriteString(w, httpMatrixSSE)
					return
				}
				w.Header().Set("Content-Type", "application/json")
				_, _ = io.WriteString(w, httpMatrixReply)
			}))
			t.Cleanup(upstream.Close)
			h := startHostWithConfiguration(t, 1<<30, true, upstream.URL+"/v1", true, httpOnceNoRetry)
			off := startHostWithConfiguration(t, 1<<30, false, upstream.URL+"/v1", true, httpOnceNoRetry)
			h.monitorAll(t)
			for _, stream := range []bool{false, true} {
				for _, tc := range cases {
					t.Run(fmt.Sprintf("%s/stream=%t", tc.name, stream), func(t *testing.T) {
						marker := fmt.Sprintf("PURPOSE-%s-%s-%t-%d", p.name, tc.name, stream, time.Now().UnixNano())
						if tc.purpose == "" {
							marker = "FORBIDDEN_PURPOSE_SKIPPED-" + marker
						}
						body := p.input(t, marker, stream, "audit-test-model")
						savedBefore, _, gaps := snapshotCounters(t, h)
						plainCode, plainResponse := requestWithPurposeHeaders(t, off, p.path, keyA, body, tc.headers)
						plainRequest := receiveMatrixUpstream(t, received, marker)
						code, response := requestWithPurposeHeaders(t, h, p.path, keyA, body, tc.headers)
						actual := receiveMatrixUpstream(t, received, marker)
						if code != http.StatusOK || plainCode != code || !bytes.Equal(plainRequest, actual) || !bytes.Equal(plainResponse, response) || !bytes.Contains(response, []byte("FORBIDDEN_HTTP_ONCE_REPLY")) {
							t.Fatal("用途过滤改变了实际上游正文或 HTTP/SSE 响应")
						}
						barrier := fmt.Sprintf("PURPOSE-BARRIER-%d", time.Now().UnixNano())
						if code, _ := h.request(t, http.MethodPost, p.path, keyA, p.input(t, barrier, false, "audit-test-model")); code != http.StatusOK {
							t.Fatal("用途 FIFO 屏障请求失败")
						}
						receiveMatrixUpstream(t, received, barrier)
						waitSnapshotRecord(t, h, barrier)
						wantSaved := savedBefore + 1
						if tc.purpose == "" {
							if searchRecords(t, h, url.Values{"q": {marker}}).Total != 0 {
								t.Fatal("明确辅助或子任务正文被保存")
							}
						} else {
							wantSaved++
							assertSavedPurpose(t, h, marker, marker, tc.purpose, p.name)
						}
						saved, queued, after := snapshotCounters(t, h)
						if saved != wantSaved || queued != 0 || !maps.Equal(gaps, after) {
							t.Fatalf("用途过滤新增记录或缺口: saved=%d want=%d queued=%d gaps_changed=%t", saved, wantSaved, queued, !maps.Equal(gaps, after))
						}
					})
				}
			}
			assertMatrixPrivacy(t, h, "FORBIDDEN_PURPOSE_")
		})
	}
}

// TestHTTPRequestPurposeAuxiliaryDiscovery 验证真实辅助调用首次仍发现匿名对象，监控前后均不存辅助正文、不增加缺口；两身份不混用。
func TestHTTPRequestPurposeAuxiliaryDiscovery(t *testing.T) {
	for _, p := range httpAuditProtocols {
		t.Run(p.name, func(t *testing.T) {
			h := startHostWithRuntime(t, 1<<30, true, "", true, "", "", "", "", "")
			waitPurposeReady(t, h)
			known := make(map[string]bool)
			for round, key := range []string{keyA, keyB} {
				beforeSaved, _, gaps := snapshotCounters(t, h)
				marker := fmt.Sprintf("FORBIDDEN_PURPOSE_DISCOVERY-%s-%d-%d", p.name, round, time.Now().UnixNano())
				headers := http.Header{"x-codex-turn-metadata": {`{"request_kind":"turn"}`}, "x-openai-subagent": {"FORBIDDEN_PURPOSE_DISCOVERY_AGENT"}}
				body := p.input(t, marker, false, "audit-test-model")
				if code, response := requestWithPurposeHeaders(t, h, p.path, key, body, headers); code != http.StatusOK || !bytes.Contains(response, []byte("FORBIDDEN_HTTP_ONCE_REPLY")) {
					t.Fatal("辅助首次发现改变真实模型响应")
				}
				objects := lifecycleObjects(t, h)
				if len(objects) != round+1 {
					t.Fatal("辅助首次发现丢失或混用调用者")
				}
				id := ""
				for _, object := range objects {
					if !known[object.ID] {
						if object.Monitored || object.HasHistory || !object.Valid || object.ID == "" {
							t.Fatal("辅助调用没有保留默认未监控、无历史的匿名对象")
						}
						id = object.ID
					}
				}
				if id == "" {
					t.Fatal("辅助调用未发现本轮身份")
				}
				known[id] = true
				lifecycleWrite(t, h, "/monitor", map[string]string{"object_id": id, "name": "用途辅助发现"})
				if code, _ := requestWithPurposeHeaders(t, h, p.path, key, body, headers); code != http.StatusOK {
					t.Fatal("选择监控后辅助调用被拒绝")
				}
				barrier := fmt.Sprintf("PURPOSE-DISCOVERY-BARRIER-%s-%d-%d", p.name, round, time.Now().UnixNano())
				if code, _ := h.request(t, http.MethodPost, p.path, key, p.input(t, barrier, false, "audit-test-model")); code != http.StatusOK {
					t.Fatal("辅助发现 FIFO 屏障失败")
				}
				record := waitSnapshotRecord(t, h, barrier)
				if record.ObjectID != id {
					t.Fatal("辅助发现与正常调用未使用同一个匿名身份")
				}
				saved, queued, after := snapshotCounters(t, h)
				if saved != beforeSaved+1 || queued != 0 || !maps.Equal(gaps, after) || searchRecords(t, h, url.Values{"q": {marker}}).Total != 0 {
					t.Fatalf("辅助发现或跳过状态不符: saved=%d want=%d queued=%d", saved, beforeSaved+1, queued)
				}
			}
			assertChatPrivacy(t, h, "FORBIDDEN_PURPOSE_")
		})
	}
}

// TestHTTPRequestPurposeCompactPaths 验证两条非流式 compact 路径实际成功路由，main/turn 不能覆盖专用压缩路径，首次发现不丢失。
func TestHTTPRequestPurposeCompactPaths(t *testing.T) {
	const reply = `{"id":"synthetic-purpose-compact","object":"response.compaction","created_at":1,"output":[],"usage":{"input_tokens":1,"output_tokens":1,"total_tokens":2}}`
	type observed struct {
		path string
		body []byte
	}
	received := make(chan observed, 4)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if respondDiscovery(w, r) {
			return
		}
		body, err := io.ReadAll(r.Body)
		if err != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		received <- observed{r.URL.Path, body}
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/v1/responses/compact" {
			_, _ = io.WriteString(w, reply)
			return
		}
		_, _ = io.WriteString(w, httpMatrixReply)
	}))
	t.Cleanup(upstream.Close)
	h := startHostWithRuntime(t, 1<<30, true, upstream.URL+"/v1", true, httpOnceNoRetry, "", "", "", "")
	waitPurposeReady(t, h)
	off := startHostWithConfiguration(t, 1<<30, false, upstream.URL+"/v1", true, httpOnceNoRetry)
	// receive 同步本地上游实际执行，返回路径及正文；超时终止对应子测试，不将成功状态单独当作 compact 到达证据。
	receive := func(t *testing.T) observed {
		t.Helper()
		select {
		case actual := <-received:
			return actual
		case <-time.After(4 * time.Second):
			t.Fatal("compact 可控上游观察缺失")
			return observed{}
		}
	}
	for round, path := range []string{"/v1/responses/compact", "/backend-api/codex/responses/compact"} {
		t.Run([]string{"v1", "codex"}[round], func(t *testing.T) {
			key := []string{keyA, keyB}[round]
			headers := http.Header{"x-claude-code-request-class": {"main"}, "x-codex-turn-metadata": {`{"request_kind":"turn"}`}}
			marker := fmt.Sprintf("FORBIDDEN_PURPOSE_COMPACT-%d-%d", round, time.Now().UnixNano())
			p := httpAuditProtocols[1]
			body := p.input(t, marker, false, "audit-test-model")
			plainCode, plainResponse := requestWithPurposeHeaders(t, off, path, key, body, headers)
			plain := receive(t)
			beforeSaved, _, gaps := snapshotCounters(t, h)
			code, response := requestWithPurposeHeaders(t, h, path, key, body, headers)
			actual := receive(t)
			if code != http.StatusOK || code != plainCode || !bytes.Equal(response, plainResponse) || !bytes.Equal(response, []byte(reply)) || plain.path != "/v1/responses/compact" || actual.path != plain.path || !bytes.Equal(actual.body, plain.body) || !bytes.Contains(actual.body, []byte(marker)) {
				t.Fatalf("compact 未成功路由或审计改变上游请求/响应: route=%s HTTP=%d", path, code)
			}
			t.Logf("真实官方宿主 compact 成功路由: route=%s upstream=%s HTTP=%d", path, actual.path, code)
			objects := lifecycleObjects(t, h)
			if len(objects) != round+1 {
				t.Fatal("compact 首次到达没有发现匿名对象")
			}
			id := ""
			for _, object := range objects {
				if !object.Monitored {
					id = object.ID
				}
			}
			if id == "" {
				t.Fatal("compact 新匿名对象没有保持默认未监控")
			}
			lifecycleWrite(t, h, "/monitor", map[string]string{"object_id": id, "name": "compact 用途发现"})
			if code, repeated := requestWithPurposeHeaders(t, h, path, key, body, headers); code != http.StatusOK || !bytes.Equal(repeated, plainResponse) {
				t.Fatal("监控后的 compact 请求改变模型响应")
			}
			if repeated := receive(t); repeated.path != plain.path || !bytes.Equal(repeated.body, plain.body) {
				t.Fatal("监控后的 compact 请求改变上游正文")
			}
			barrier := fmt.Sprintf("PURPOSE-COMPACT-BARRIER-%d-%d", round, time.Now().UnixNano())
			if code, _ := h.request(t, http.MethodPost, p.path, key, p.input(t, barrier, false, "audit-test-model")); code != http.StatusOK {
				t.Fatal("compact FIFO 屏障失败")
			}
			receive(t)
			waitSnapshotRecord(t, h, barrier)
			saved, queued, after := snapshotCounters(t, h)
			if saved != beforeSaved+1 || queued != 0 || !maps.Equal(gaps, after) || searchRecords(t, h, url.Values{"q": {marker}}).Total != 0 {
				t.Fatalf("compact 用途未正常跳过: route=%s saved=%d want=%d queued=%d", path, saved, beforeSaved+1, queued)
			}
		})
	}
	assertChatPrivacy(t, h, "FORBIDDEN_PURPOSE_")
}
