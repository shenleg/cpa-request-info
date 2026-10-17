// 本文件经真实合成宿主验证思考声明、模型目录、管理鉴权和新旧记录的公开 JSON，不自动删除测试文件。
package system

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"
)

// assertReasoningJSON 检查公开字段显式存在且与 want 的对象或 null 相同；无返回值，解码或语义不符终止测试。
func assertReasoningJSON(t *testing.T, raw json.RawMessage, want string) {
	t.Helper()
	if len(raw) == 0 {
		t.Fatal("公开 API 缺少 request_reasoning 字段")
	}
	var actual, expected any
	if json.Unmarshal(raw, &actual) != nil || json.Unmarshal([]byte(want), &expected) != nil || !reflect.DeepEqual(actual, expected) {
		t.Fatalf("公开思考声明不符：got=%s want=%s", raw, want)
	}
}

// assertMetadataRecord 检查本轮唯一记录的列表和详情思考字段；marker 是独立正文预期，want 是预期 JSON，失败终止测试。
func assertMetadataRecord(t *testing.T, h *instance, marker, want string) savedRecord {
	t.Helper()
	page := searchRecords(t, h, url.Values{"q": {marker}, "limit": {"1"}})
	if page.Total != 1 || len(page.Records) != 1 {
		t.Fatalf("元数据列表不是本轮唯一记录：total=%d", page.Total)
	}
	item := page.Records[0]
	assertReasoningJSON(t, item.RequestReasoning, want)
	code, raw := h.request(t, http.MethodGet, api+"/detail?id="+url.QueryEscape(item.ID), adminKey, nil)
	var detail savedRecord
	if code != http.StatusOK || json.Unmarshal(raw, &detail) != nil || detail.ID != item.ID || detail.ObjectID != item.ObjectID || detail.Text != marker || detail.Model != item.Model || detail.Protocol != item.Protocol {
		t.Fatal("元数据详情与列表或本轮正文不符")
	}
	assertReasoningJSON(t, detail.RequestReasoning, want)
	return detail
}

// metadataModels 读取管理模型目录并返回稳定数组；缺字段、null、HTTP 或 JSON 错误终止测试。
func metadataModels(t *testing.T, h *instance) []string {
	t.Helper()
	code, raw := h.request(t, http.MethodGet, api+"/models", adminKey, nil)
	var result struct {
		Models []string `json:"models"`
	}
	if code != http.StatusOK || json.Unmarshal(raw, &result) != nil || result.Models == nil {
		t.Fatalf("模型目录没有提供数组：HTTP=%d", code)
	}
	return result.Models
}

// TestHTTPRecordReasoningProtocols 验证三协议 HTTP/SSE 显式声明及空对象，列表/详情一致且审计不改上游请求或响应。
func TestHTTPRecordReasoningProtocols(t *testing.T) {
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
			if got := metadataModels(t, h); len(got) != 0 {
				t.Fatalf("首次发现即保存了模型：%q", got)
			}
			cases := []struct{ name, fields, want string }{{"missing", "", `{}`}}
			switch p.name {
			case "chat-completions":
				cases = append(cases, struct{ name, fields, want string }{"effort", `"reasoning_effort":"high"`, `{"effort":"high"}`})
			case "responses":
				cases = append(cases, struct{ name, fields, want string }{"effort", `"reasoning":{"effort":"medium"}`, `{"effort":"medium"}`})
			case "anthropic-messages":
				cases = append(cases, []struct{ name, fields, want string }{
					{"all", `"output_config":{"effort":"high"},"thinking":{"type":"enabled","budget_tokens":4096}`, `{"effort":"high","type":"enabled","budget_tokens":4096}`},
					{"zero", `"thinking":{"type":"disabled","budget_tokens":0}`, `{"type":"disabled","budget_tokens":0}`},
					{"budget_missing", `"thinking":{"type":"adaptive"}`, `{"type":"adaptive"}`},
				}...)
			}
			var lastMarker, lastWant string
			for _, stream := range []bool{false, true} {
				for _, tc := range cases {
					t.Run(fmt.Sprintf("%s/stream=%t", tc.name, stream), func(t *testing.T) {
						marker := fmt.Sprintf("REASONING-%s-%s-%t-%d", p.name, tc.name, stream, time.Now().UnixNano())
						body := p.input(t, marker, stream, "audit-test-model")
						if tc.fields != "" {
							body = append(body[:len(body)-1], []byte(","+tc.fields+"}")...)
						}
						plainCode, plainResponse := off.request(t, http.MethodPost, p.path, keyA, body)
						plain := receiveMatrixUpstream(t, received, marker)
						code, response := h.request(t, http.MethodPost, p.path, keyA, body)
						actual := receiveMatrixUpstream(t, received, marker)
						if code != http.StatusOK || code != plainCode || !bytes.Equal(actual, plain) || !bytes.Equal(response, plainResponse) || !bytes.Contains(response, []byte("FORBIDDEN_HTTP_ONCE_REPLY")) {
							t.Fatal("思考元数据采集改变上游请求或 HTTP/SSE 响应")
						}
						waitSnapshotRecord(t, h, marker)
						if record := assertMetadataRecord(t, h, marker, tc.want); record.Protocol != p.name || record.Model != "audit-test-model" {
							t.Fatal("采集思考声明改变原协议或请求模型")
						}
						lastMarker, lastWant = marker, tc.want
					})
				}
			}
			if got := metadataModels(t, h); !reflect.DeepEqual(got, []string{"audit-test-model"}) {
				t.Fatalf("模型目录没有去重：%q", got)
			}
			before := assertMetadataRecord(t, h, lastMarker, lastWant)
			h.stop()
			restarted := startHostWithConfiguration(t, 1<<30, true, upstream.URL+"/v1", true, httpOnceNoRetry, h.stateDir)
			if after := assertMetadataRecord(t, restarted, lastMarker, lastWant); after.ID != before.ID {
				t.Fatal("重启改变思考记录身份")
			}
			if got := metadataModels(t, restarted); !reflect.DeepEqual(got, []string{"audit-test-model"}) {
				t.Fatalf("重启丢失模型目录：%q", got)
			}
			assertMatrixPrivacy(t, restarted)
		})
	}
}

// TestHTTPModelsCatalogAndAuthorization 验证真实多对象模型目录忽略筛选分页、区分大小写并稳定排序，客户端与匿名身份均不可读取。
func TestHTTPModelsCatalogAndAuthorization(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if respondDiscovery(w, r) {
			return
		}
		_, _ = io.Copy(io.Discard, r.Body)
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, httpMatrixReply)
	}))
	t.Cleanup(upstream.Close)
	config := fmt.Sprintf(`request-retry: 0
openai-compatibility:
  - name: metadata-models
    base-url: %q
    api-key-entries:
      - api-key: synthetic-upstream-only
    models:
      - name: audit-test-model
        alias: audit-test-model
      - name: audit-test-model
        alias: Alpha
      - name: audit-test-model
        alias: alpha
      - name: audit-test-model
        alias: zeta
`, upstream.URL+"/v1")
	h := startHostWithConfiguration(t, 1<<30, true, "", true, config)
	h.monitorAll(t)
	if got := metadataModels(t, h); len(got) != 0 {
		t.Fatalf("空库目录不为空：%q", got)
	}
	var first savedRecord
	for i, sample := range []struct{ model, key string }{{"zeta", keyA}, {"Alpha", keyB}, {"alpha", keyA}, {"zeta", keyB}} {
		marker := fmt.Sprintf("MODELS-CATALOG-%d-%d", i, time.Now().UnixNano())
		body, err := json.Marshal(map[string]any{"model": sample.model, "messages": []map[string]string{{"role": "user", "content": marker}}})
		if err != nil {
			t.Fatal(err)
		}
		if code, _ := h.request(t, http.MethodPost, "/v1/chat/completions", sample.key, body); code != http.StatusOK {
			t.Fatal("目录夹具模型请求失败")
		}
		record := waitSnapshotRecord(t, h, marker)
		if i == 0 {
			first = record
		}
	}
	want := []string{"Alpha", "alpha", "zeta"}
	for _, query := range []url.Values{
		{"limit": {"1"}},
		{"offset": {"1"}, "limit": {"1"}},
		{"model": {"alpha"}, "object_id": {first.ObjectID}, "q": {"MODELS-CATALOG-"}, "limit": {"1"}},
		{"q": {"missing-model-metadata-text"}},
	} {
		searchRecords(t, h, query)
		if got := metadataModels(t, h); !reflect.DeepEqual(got, want) {
			t.Fatalf("筛选或分页影响模型目录：%q", got)
		}
	}
	for _, key := range []string{"", keyA, keyB, first.ObjectID} {
		if code, _ := h.request(t, http.MethodGet, api+"/models", key, nil); code != http.StatusUnauthorized && code != http.StatusForbidden {
			t.Fatalf("非管理身份读取模型目录：HTTP=%d", code)
		}
	}
	h.stop()
	h = startHostWithConfiguration(t, 1<<30, true, "", true, config, h.stateDir)
	if got := metadataModels(t, h); !reflect.DeepEqual(got, want) {
		t.Fatalf("重启模型目录不符：%q", got)
	}
}

// TestHTTPModelsLogicalExpiry 验证管理模型目录在七天边界立即排除未物理清理的记录，重启后不恢复过期模型。
func TestHTTPModelsLogicalExpiry(t *testing.T) {
	start := time.Date(2033, 6, 7, 8, 9, 10, 0, time.UTC)
	clock := newRetentionClock(t, start)
	h := startRetentionHost(t, 1<<30, "", clock)
	h.monitorAll(t)
	first := fmt.Sprintf("MODELS-EXPIRY-OLD-%d", time.Now().UnixNano())
	body, err := json.Marshal(map[string]any{"model": "audit-test-model", "messages": []map[string]string{{"role": "user", "content": first}}, "reasoning_effort": "high"})
	if err != nil {
		t.Fatal(err)
	}
	if code, _ := h.request(t, http.MethodPost, "/v1/chat/completions", keyA, body); code != http.StatusOK {
		t.Fatal("到期旧模型夹具提交失败")
	}
	waitSnapshotRecord(t, h, first)
	assertMetadataRecord(t, h, first, `{"effort":"high"}`)
	retentionClock(t, clock, start.Add(time.Hour), 0)
	second := fmt.Sprintf("MODELS-EXPIRY-NEW-%d", time.Now().UnixNano())
	body, err = json.Marshal(map[string]any{"model": "responses-client-model", "input": second, "reasoning": map[string]string{"effort": "medium"}})
	if err != nil {
		t.Fatal(err)
	}
	if code, _ := h.request(t, http.MethodPost, "/v1/responses", keyB, body); code != http.StatusOK {
		t.Fatal("到期新模型夹具提交失败")
	}
	waitSnapshotRecord(t, h, second)
	assertMetadataRecord(t, h, second, `{"effort":"medium"}`)
	original := retentionOutput(t, h.stateDir)
	retentionClock(t, clock, start.Add(7*24*time.Hour-time.Nanosecond), 0)
	if got := metadataModels(t, h); !reflect.DeepEqual(got, []string{"audit-test-model", "responses-client-model"}) {
		t.Fatalf("模型提前到期：%q", got)
	}
	retentionClock(t, clock, start.Add(7*24*time.Hour), 0)
	if got := metadataModels(t, h); !reflect.DeepEqual(got, []string{"responses-client-model"}) {
		t.Fatalf("逻辑到期模型仍可见：%q", got)
	}
	if !bytes.Equal(original, retentionOutput(t, h.stateDir)) {
		t.Fatal("时钟推进触发物理清理，未验证未清理模型边界")
	}
	h.stop()
	h = startRetentionHost(t, 1<<30, "", clock, h.stateDir)
	if got := metadataModels(t, h); !reflect.DeepEqual(got, []string{"responses-client-model"}) {
		t.Fatalf("重启恢复了到期模型：%q", got)
	}
	assertMetadataRecord(t, h, second, `{"effort":"medium"}`)
	retentionClock(t, clock, start.Add(time.Hour+7*24*time.Hour), 0)
	if got := metadataModels(t, h); len(got) != 0 {
		t.Fatalf("全部到期仍有模型：%q", got)
	}
}

// TestHTTPRecordReasoningLegacyNull 验证旧无字段记录的列表和详情显式返回 null，新缺声明返回空对象且重启不改写旧行。
func TestHTTPRecordReasoningLegacyNull(t *testing.T) {
	h := startHostWithManagementAuth(t, 1<<30, true, "", true)
	h.monitorAll(t)
	marker := fmt.Sprintf("REASONING-NEW-EMPTY-%d", time.Now().UnixNano())
	body, err := json.Marshal(map[string]any{"model": "audit-test-model", "messages": []map[string]string{{"role": "user", "content": marker}}})
	if err != nil {
		t.Fatal(err)
	}
	if code, _ := h.request(t, http.MethodPost, "/v1/chat/completions", keyA, body); code != http.StatusOK {
		t.Fatal("新空声明夹具提交失败")
	}
	newRecord := waitSnapshotRecord(t, h, marker)
	assertMetadataRecord(t, h, marker, `{}`)
	h.stop()
	legacyMarker := fmt.Sprintf("REASONING-LEGACY-%d", time.Now().UnixNano())
	legacy := []byte(fmt.Sprintf("{ \"text\":%q, \"id\":\"metadata-legacy\", \"object_id\":%q, \"received_at\":%q, \"protocol\":\"responses\", \"model\":\"legacy-model\", \"truncated\":false }\n", legacyMarker, newRecord.ObjectID, time.Now().UTC().Format(time.RFC3339Nano)))
	path := filepath.Join(h.stateDir, "records-000002.jsonl")
	if err := os.WriteFile(path, legacy, 0600); err != nil {
		t.Fatal(err)
	}
	h = startHostWithManagementAuth(t, 1<<30, true, "", true, h.stateDir)
	assertMetadataRecord(t, h, marker, `{}`)
	assertMetadataRecord(t, h, legacyMarker, `null`)
	if got := metadataModels(t, h); !reflect.DeepEqual(got, []string{"audit-test-model", "legacy-model"}) {
		t.Fatalf("新旧模型目录不符：%q", got)
	}
	h.stop()
	after, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(after, legacy) {
		t.Fatalf("读取改写了旧记录：err=%v", err)
	}
}
