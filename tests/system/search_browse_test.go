// 本文件通过真实客户端提交与受保护管理查询验证组合筛选、字面搜索和有界浏览，不访问私有存储。
package system

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"strings"
	"testing"
	"time"
)

type searchPage struct {
	Records []struct {
		savedRecord
		Preview string `json:"preview"`
	} `json:"records"`
	Total int `json:"total"`
}

// searchRecords 使用公开查询参数读取一页；返回记录及匹配总数，HTTP或解码失败终止测试。
func searchRecords(t *testing.T, h *instance, query url.Values) searchPage {
	t.Helper()
	code, raw := h.request(t, "GET", api+"/records?"+query.Encode(), adminKey, nil)
	var page searchPage
	if code != 200 || json.Unmarshal(raw, &page) != nil {
		t.Fatalf("公开搜索不可用: status=%d", code)
	}
	return page
}

// searchSubmit 提交指定客户端模型与文本，再以唯一前缀等待保存；返回独立详情，提交或查询失败终止测试。
func searchSubmit(t *testing.T, h *instance, key, model, text string) savedRecord {
	t.Helper()
	body, err := json.Marshal(map[string]any{"model": model, "messages": []map[string]string{{"role": "user", "content": text}}})
	if err != nil {
		t.Fatal("搜索夹具编码失败")
	}
	h.request(t, "POST", "/v1/chat/completions", key, body)
	return waitChatDetail(t, h, text)
}

// TestSearchIntersection 验证四条件取交集、时间端点按时刻比较，以及筛选先于计数和分页。
func TestSearchIntersection(t *testing.T) {
	h := startHostWithManagementAuth(t, 1<<30, true, "", true)
	h.monitorAll(t)
	first := searchSubmit(t, h, keyA, "client-search-model", "SEARCH-FIRST-Needle中文")
	other := searchSubmit(t, h, keyB, "client-search-model", "SEARCH-OTHER-Needle中文")
	last := searchSubmit(t, h, keyA, "client-search-model", "SEARCH-LAST-Needle中文")
	different := searchSubmit(t, h, keyA, "different-model", "SEARCH-DIFFERENT-Needle中文")
	searchSubmit(t, h, keyA, "client-search-model", "SEARCH-NO-MATCH")
	cases := []struct {
		name  string
		query url.Values
		want  []string
	}{
		{"keyword", url.Values{"q": {"nEeDlE中文"}}, []string{different.ID, last.ID, other.ID, first.ID}},
		{"model", url.Values{"model": {"different-model"}}, []string{different.ID}},
		{"intersection", url.Values{"object_id": {first.ObjectID}, "model": {"client-search-model"}, "q": {"needle中文"}, "from": {first.ReceivedAt.Format(time.RFC3339Nano)}, "to": {last.ReceivedAt.Format(time.RFC3339Nano)}}, []string{last.ID, first.ID}},
		{"exact_time_with_offset", url.Values{"from": {first.ReceivedAt.In(time.FixedZone("east", 8*60*60)).Format(time.RFC3339Nano)}, "to": {first.ReceivedAt.Format(time.RFC3339Nano)}}, []string{first.ID}},
		{"before_first", url.Values{"to": {first.ReceivedAt.Add(-time.Nanosecond).Format(time.RFC3339Nano)}}, []string{}},
		{"model_is_exact", url.Values{"model": {"CLIENT-SEARCH-MODEL"}}, []string{}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			page := searchRecords(t, h, tc.query)
			ids := make([]string, 0, len(page.Records))
			for _, r := range page.Records {
				ids = append(ids, r.ID)
			}
			if page.Total != len(tc.want) || !reflect.DeepEqual(ids, tc.want) {
				t.Fatalf("筛选结果错误: total=%d count=%d want=%d", page.Total, len(ids), len(tc.want))
			}
		})
	}
	query := url.Values{"object_id": {first.ObjectID}, "q": {"needle中文"}, "model": {"client-search-model"}, "limit": {"1"}}
	page := searchRecords(t, h, query)
	if page.Total != 2 || len(page.Records) != 1 || page.Records[0].ID != last.ID {
		t.Fatal("筛选第一页或总数错误")
	}
	query.Set("offset", "1")
	page = searchRecords(t, h, query)
	if page.Total != 2 || len(page.Records) != 1 || page.Records[0].ID != first.ID {
		t.Fatal("筛选第二页错误")
	}
	query.Set("offset", "2")
	page = searchRecords(t, h, query)
	if page.Total != 2 || len(page.Records) != 0 {
		t.Fatal("筛选页尾错误")
	}
}

// TestSearchSavedTextOnly 验证只搜索已保存全文，语法字符不解释，未保存载荷与历史不命中且状态不丢失。
func TestSearchSavedTextOnly(t *testing.T) {
	h := startHostWithManagementAuth(t, 1<<30, true, "", true)
	h.monitorAll(t)
	marker := "SEARCH-LITERAL-"
	prefix := marker + strings.Repeat("x", 65535-len(marker)-len(" 深处中文 MiXeD .* [a-z] foo OR bar  ")) + " 深处中文 MiXeD .* [a-z] foo OR bar  "
	body, _ := json.Marshal(map[string]any{
		"model": "search-literal-model", "metadata": map[string]string{"session_id": "FORBIDDEN_SEARCH_SESSION"},
		"messages": []any{
			map[string]string{"role": "system", "content": "FORBIDDEN_SEARCH_SYSTEM"},
			map[string]string{"role": "user", "content": "FORBIDDEN_SEARCH_HISTORY"},
			map[string]any{"role": "user", "content": []any{
				map[string]string{"type": "text", "text": prefix + "🙂FORBIDDEN_SEARCH_TAIL"},
				map[string]any{"type": "image_url", "image_url": map[string]string{"url": "https://invalid.example/FORBIDDEN_SEARCH_IMAGE"}},
				map[string]string{"type": "future", "payload": "FORBIDDEN_SEARCH_UNKNOWN"},
			}},
		},
	})
	h.request(t, "POST", "/v1/chat/completions", keyA, body)
	r := waitChatDetail(t, h, marker)
	if r.Text != prefix || !r.Truncated || !r.ExtractionIncomplete {
		t.Fatal("搜索状态夹具不符合独立预期")
	}
	for _, keyword := range []string{"深处中文", "mixed", ".*", "[a-z]", "foo OR bar", "bar  "} {
		page := searchRecords(t, h, url.Values{"q": {keyword}})
		if page.Total != 1 || len(page.Records) != 1 || page.Records[0].ID != r.ID || !page.Records[0].Truncated || !page.Records[0].ExtractionIncomplete || strings.Contains(page.Records[0].Preview, keyword) {
			t.Fatal("预览之外的已保存内容不可搜索或内容状态丢失")
		}
		if len(page.Records[0].Preview) > 512 || page.Records[0].Text != "" {
			t.Fatal("列表返回了完整正文")
		}
	}
	for _, keyword := range []string{"FORBIDDEN_SEARCH_", keyA, keyB, adminKey, "search-literal-model", r.ObjectID, "foo|bar", "深处.*中文", "  bar"} {
		if page := searchRecords(t, h, url.Values{"q": {keyword}}); page.Total != 0 || len(page.Records) != 0 {
			t.Fatal("搜索解释了语法或匹配了未保存正文")
		}
	}
	for _, key := range []string{"", keyA, keyB, r.ObjectID} {
		for _, path := range []string{"/records?q=MiXeD&model=search-literal-model", "/detail?id=" + r.ID, "/objects", "/status", "/settings"} {
			code, _ := h.request(t, "GET", api+path, key, nil)
			if code != 401 && code != 403 {
				t.Fatal("非管理身份可读搜索或审计数据")
			}
		}
	}
	code, shell := h.request(t, "GET", "/v0/resource/plugins/request-input-audit/index.html", "", nil)
	if code != 200 || strings.Contains(string(shell), marker) || strings.Contains(string(shell), r.ObjectID) || strings.Contains(string(shell), adminKey) {
		t.Fatal("公开 shell 包含动态数据")
	}
	assertChatPrivacy(t, h, "FORBIDDEN_SEARCH_")
}

// TestSearchProtocols 验证四种已支持提交共用搜索、请求模型和时间条件，不误匹配实际上游模型。
func TestSearchProtocols(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"id":"synthetic-search","choices":[{"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}]}`)
	}))
	defer upstream.Close()
	h := startHostWithManagementAuth(t, 1<<30, true, upstream.URL+"/v1", true)
	h.monitorAll(t)
	first := searchSubmit(t, h, keyA, "responses-client-model", "SEARCH-PROTOCOL-CHAT")
	for _, fixture := range []struct{ path, body, marker string }{
		{"/v1/responses", `{"model":"responses-client-model","input":"SEARCH-PROTOCOL-RESPONSES"}`, "SEARCH-PROTOCOL-RESPONSES"},
		{"/v1/messages", `{"model":"responses-client-model","max_tokens":8,"messages":[{"role":"user","content":"SEARCH-PROTOCOL-MESSAGES"}]}`, "SEARCH-PROTOCOL-MESSAGES"},
	} {
		h.request(t, "POST", fixture.path, keyA, []byte(fixture.body))
		waitChatDetail(t, h, fixture.marker)
	}
	conn := dialAuditWebsocket(t, h, "/v1/responses", keyA)
	sendAuditFrame(t, conn, `{"type":"response.create","generate":false,"model":"responses-client-model","input":[{"role":"user","content":"SEARCH-PROTOCOL-WEBSOCKET"}]}`)
	last := waitChatDetail(t, h, "SEARCH-PROTOCOL-WEBSOCKET")
	page := searchRecords(t, h, url.Values{"q": {"search-protocol-"}, "object_id": {first.ObjectID}, "model": {"responses-client-model"}, "from": {first.ReceivedAt.Format(time.RFC3339Nano)}, "to": {last.ReceivedAt.Format(time.RFC3339Nano)}})
	protocols := make(map[string]bool)
	for _, record := range page.Records {
		protocols[record.Protocol] = true
	}
	if page.Total != 4 || len(page.Records) != 4 || !reflect.DeepEqual(protocols, map[string]bool{"chat-completions": true, "responses": true, "anthropic-messages": true, "responses-websocket": true}) {
		t.Fatal("四协议未共享组合查询")
	}
	if page := searchRecords(t, h, url.Values{"model": {"audit-test-model"}}); page.Total != 0 {
		t.Fatal("搜索错误采用上游实际模型")
	}
	for round := 0; round < 2; round++ {
		marker := fmt.Sprintf("SEARCH-REPEAT-%d", round)
		r := searchSubmit(t, h, keyB, "client-repeat", marker)
		page := searchRecords(t, h, url.Values{"q": {marker}})
		if page.Total != 1 || page.Records[0].ID != r.ID {
			t.Fatal("本轮搜索误用历史结果")
		}
	}
}

// TestSearchValidation 验证非法或重复参数返回固定错误，不回显查询内容，也不破坏合法边界。
func TestSearchValidation(t *testing.T) {
	h := startHostWithManagementAuth(t, 1<<30, true, "", true)
	h.monitorAll(t)
	searchSubmit(t, h, keyA, strings.Repeat("m", 1024), "SEARCH-VALIDATION-BOUNDARY")
	for _, query := range []string{
		"q=SEARCH-VALIDATION&q=other", "model=x&model=y", "object_id=&object_id=", "from=2000-01-01T00:00:00Z&from=2001-01-01T00:00:00Z",
		"to=2000-01-01T00:00:00Z&to=2001-01-01T00:00:00Z", "offset=0&offset=1", "limit=1&limit=2",
		"from=", "to=", "from=not-a-time", "from=2026-01-02T00:00:00Z&to=2026-01-01T00:00:00Z",
		"object_id=invalid", "offset=-1", "offset=10000001", "offset=99999999999999999999", "limit=0", "limit=101", "limit=NaN",
		"model=" + strings.Repeat("m", 1025), "q=" + strings.Repeat("x", 65537),
	} {
		code, raw := h.request(t, "GET", api+"/records?"+query, adminKey, nil)
		var result map[string]string
		if code != 400 || json.Unmarshal(raw, &result) != nil || len(result) != 1 {
			t.Errorf("非法查询未返回固定400: status=%d", code)
			continue
		}
		switch result["error"] {
		case "invalid_query", "invalid_object", "invalid_pagination", "invalid_time_range":
		default:
			t.Error("查询错误包含非固定原因")
		}
	}
	for _, tc := range []struct {
		query url.Values
		total int
	}{
		{url.Values{"to": {"0001-01-01T00:00:00Z"}}, 0},
		{url.Values{"q": {strings.Repeat("x", 65536)}}, 0},
		{url.Values{"model": {strings.Repeat("m", 1024)}, "limit": {"100"}}, 1},
	} {
		if page := searchRecords(t, h, tc.query); page.Total != tc.total {
			t.Error("合法查询边界错误")
		}
	}
}
