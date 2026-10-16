// 本文件验收官方无补丁宿主的 HTTP 执行采集、已发现对象、POST 查询和 WebSocket 明确排除。
// 成功/上游失败/SSE、取消、重试和 callback 嵌套的两轮真实执行分别见 TestHTTPProtocolsBeforeReply、
// TestHTTPProtocolsCancellation、TestHTTPProtocolsSSEDisconnect、TestHTTPProtocolsRetries 和 TestHTTPProtocolsNestedExecution。
// 原帧观察、读体前快照及私有 observer 的历史断言保存在 legacy_patched 文件，不作为当前能力。
package system

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// assertOfficialStatus 核对公开能力只声明三种 HTTP Before 观察；接口或能力不符终止测试，不输出完整状态。
func assertOfficialStatus(t *testing.T, h *instance) {
	t.Helper()
	code, raw := h.request(t, "GET", api+"/status", adminKey, nil)
	var status struct {
		Available bool     `json:"available"`
		Mode      string   `json:"capture_mode"`
		Protocols []string `json:"supported_protocols"`
		Websocket bool     `json:"responses_websocket"`
	}
	if code != http.StatusOK || json.Unmarshal(raw, &status) != nil || !status.Available || status.Mode != "http_execution" || status.Websocket || len(status.Protocols) != 3 {
		t.Fatal("正式插件没有声明官方 HTTP 执行能力或错误宣称支持 WebSocket")
	}
	for _, p := range httpAuditProtocols {
		found := false
		for _, protocol := range status.Protocols {
			found = found || protocol == p.name
		}
		if !found {
			t.Fatal("公开状态缺少已支持的 HTTP 协议")
		}
	}
}

// TestOfficialKnownCallerDirectory 验证目录不枚举宿主 Key，成功执行才发现，选择后只保存后续提交，重启不轮换对象。
func TestOfficialKnownCallerDirectory(t *testing.T) {
	h := startHostWithRuntime(t, 1<<30, true, "", true, "", "", "", "", "")
	assertOfficialStatus(t, h)
	if len(lifecycleObjects(t, h)) != 0 {
		t.Fatal("全新目录未经调用就枚举了宿主 Key")
	}
	var previous []savedRecord
	for round, key := range []string{keyA, keyB} {
		marker := fmt.Sprintf("OFFICIAL-KNOWN-%d-%d", round, time.Now().UnixNano())
		if code, _ := h.request(t, "POST", "/v1/chat/completions", key, snapshotInput(t, marker+"-discover")); code != 200 {
			t.Fatal("用于发现对象的真实本地模型调用失败")
		}
		objects := lifecycleObjects(t, h)
		if len(objects) != round+1 || searchRecords(t, h, nil).Total != round {
			t.Fatal("默认未监控发现调用被保存，或对象目录未按实际调用增长")
		}
		known := make(map[string]bool)
		for _, record := range previous {
			known[record.ObjectID] = true
		}
		id := ""
		for _, object := range objects {
			if !known[object.ID] {
				if object.Monitored || object.HasHistory || !object.Valid || object.ID == "" {
					t.Fatal("新发现对象不是未监控且无历史的匿名对象")
				}
				id = object.ID
			}
		}
		if id == "" {
			t.Fatal("未发现本轮独立调用者")
		}
		lifecycleWrite(t, h, "/monitor", map[string]string{"object_id": id, "name": "已发现匿名对象"})
		before := time.Now()
		if code, _ := h.request(t, "POST", "/v1/chat/completions", key, snapshotInput(t, marker+"-saved")); code != 200 {
			t.Fatal("选择后的真实模型调用失败")
		}
		record := waitSnapshotRecord(t, h, marker+"-saved")
		if record.ObjectID != id || record.ReceivedAt.Before(before) || record.ReceivedAt.After(time.Now()) {
			t.Fatal("官方 Before 时间或已发现对象关联错误")
		}
		previous = append(previous, record)
	}
	h.stop()
	restarted := startHost(t, 1<<30, true, "", h.stateDir)
	if len(lifecycleObjects(t, restarted)) != 2 || searchRecords(t, restarted, nil).Total != 2 {
		t.Fatal("重启丢失已发现目录或默认发现请求污染了历史")
	}
	for round, record := range previous {
		key := []string{keyA, keyB}[round]
		marker := fmt.Sprintf("OFFICIAL-KNOWN-RESTART-%d-%d", round, time.Now().UnixNano())
		history := lifecycleSaved(t, restarted, record.Text)
		lifecycleSubmit(t, restarted, key, marker)
		current := waitSnapshotRecord(t, restarted, marker)
		if history.ID != record.ID || current.ObjectID != record.ObjectID || current.ID == record.ID {
			t.Fatal("重启改变历史标识、调用者身份或重复使用执行标识")
		}
	}
	assertChatPrivacy(t, restarted, "synthetic-discovery-only", "synthetic-discovery-upstream-only")
}

// TestOfficialUnknownModelInvisible 验证模型路由/解析失败发生在 Before 之前，没有记录、对象或虚构采集缺口。
func TestOfficialUnknownModelInvisible(t *testing.T) {
	h := startHostWithRuntime(t, 1<<30, true, "", true, "", "", "", "", "")
	for round := 0; round < 2; round++ {
		for _, p := range httpAuditProtocols {
			code, _ := h.request(t, "POST", p.path, keyA, p.input(t, "FORBIDDEN_OFFICIAL_UNKNOWN", false, "not-configured-official-model"))
			if code != http.StatusBadRequest {
				t.Fatal("未知模型没有保留原宿主路由错误")
			}
		}
		if len(lifecycleObjects(t, h)) != 0 || searchRecords(t, h, nil).Total != 0 {
			t.Fatal("未知模型在官方 Before 之前失败却被发现或采集")
		}
		saved, queued, gaps := snapshotCounters(t, h)
		if saved != 0 || queued != 0 {
			t.Fatal("未进入官方执行的请求产生了记录或排队副本")
		}
		for _, count := range gaps {
			if count != 0 {
				t.Fatal("未进入官方执行的请求产生了虚构采集缺口")
			}
		}
	}
	assertChatPrivacy(t, h, "FORBIDDEN_OFFICIAL_UNKNOWN")
}

// TestOfficialBeforeUsesCurrentConfiguration 验证读体期间启用对象/改上限会作用于随后 Before，不再冻结路由前快照，同实例两轮。
func TestOfficialBeforeUsesCurrentConfiguration(t *testing.T) {
	upstream, received := snapshotUpstream()
	t.Cleanup(upstream.Close)
	h := startHostWithManagementAuth(t, 1<<30, true, upstream.URL+"/v1", true)
	h.monitorAll(t)
	seed := fmt.Sprintf("OFFICIAL-BEFORE-OBJECT-%d", time.Now().UnixNano())
	lifecycleSubmit(t, h, keyA, seed)
	object := waitSnapshotRecord(t, h, seed).ObjectID
	verifySnapshotUpstream(t, received, seed)
	for round := 0; round < 2; round++ {
		lifecycleWrite(t, h, "/remove", map[string]string{"object_id": object})
		snapshotMutation(t, h, "/settings", map[string]int{"max_text_bytes": 128})
		marker := fmt.Sprintf("BC%d-%d-current", round, time.Now().UnixNano())
		complete := beginSnapshotSubmission(t, h, snapshotInput(t, marker))
		lifecycleWrite(t, h, "/monitor", map[string]string{"object_id": object, "name": "Before 当前配置"})
		snapshotMutation(t, h, "/settings", map[string]int{"max_text_bytes": 7})
		before := time.Now()
		complete()
		verifySnapshotUpstream(t, received, marker)
		record := waitChatDetail(t, h, marker[:7])
		if record.Text != marker[:7] || !record.Truncated || record.ObjectID != object || record.ReceivedAt.Before(before) {
			t.Fatal("官方 Before 沿用了读体前的名单、上限或时刻")
		}
	}
	assertChatPrivacy(t, h)
}

// TestOfficialLegacyIdentityRejected 验证旧 seed 或没有新 marker 的已有目录被拒绝且不被改写，每种目录同实例两轮调用。
func TestOfficialLegacyIdentityRejected(t *testing.T) {
	for _, kind := range []string{"old-seed", "unmarked-data"} {
		t.Run(kind, func(t *testing.T) {
			root, err := filepath.Abs("../../.test-data")
			if err != nil {
				t.Fatal("身份验收路径无效")
			}
			dir, err := os.MkdirTemp(root, "legacy-caller-namespace-")
			if err != nil {
				t.Fatal("身份验收目录创建失败")
			}
			name, original := "monitors.json", []byte(`{}`)
			if kind == "old-seed" {
				name, original = "identity.seed", bytes.Repeat([]byte{0x31}, 32)
			}
			path := filepath.Join(dir, name)
			if err := os.WriteFile(path, original, 0600); err != nil {
				t.Fatal("旧身份夹具创建失败")
			}
			h := startHostWithRuntime(t, 1<<30, true, "", true, "", "", "", "", "", dir)
			for round := 0; round < 2; round++ {
				code, _ := h.request(t, "POST", "/v1/chat/completions", keyA, snapshotInput(t, "FORBIDDEN_LEGACY_NAMESPACE"))
				if code != 200 {
					t.Fatal("旧 namespace 拒绝影响了正常模型调用")
				}
				code, raw := h.request(t, "GET", api+"/status", adminKey, nil)
				var status struct {
					State struct{ State, Reason string } `json:"state"`
				}
				if code != 200 || json.Unmarshal(raw, &status) != nil || status.State.State != "paused" || status.State.Reason != "identity_unavailable" {
					t.Fatal("旧 namespace 没有明确拒绝并暂停采集")
				}
			}
			after, err := os.ReadFile(path)
			if err != nil || !bytes.Equal(after, original) {
				t.Fatal("拒绝旧 namespace 时改写了原数据")
			}
			if _, err := os.Stat(filepath.Join(dir, "caller-scope-v1.seed")); !os.IsNotExist(err) {
				t.Fatal("拒绝旧目录时仍创建了新身份 marker")
			}
		})
	}
}

// TestOfficialCurrentDDModel 验证 Messages 的 DD 改写先于 Before，记录当前模型而非原始 DD 模型，同实例两轮。
func TestOfficialCurrentDDModel(t *testing.T) {
	h := startHostWithManagementAuth(t, 1<<30, true, "", true)
	h.monitorAll(t)
	for _, p := range httpAuditProtocols[2:] {
		for round := 0; round < 2; round++ {
			marker := fmt.Sprintf("OFFICIAL-DD-%s-%d-%d", p.name, round, time.Now().UnixNano())
			body := p.input(t, marker, false, "claude-fable-5-dd-ledom-tset-tidua")
			code, _ := h.request(t, "POST", p.path, keyA, body)
			if code != http.StatusOK {
				t.Fatal("DD 夹具没有真正执行已配置本地模型")
			}
			record := waitSnapshotRecord(t, h, marker)
			if record.Model != "audit-test-model" || record.Protocol != p.name {
				t.Fatal("记录没有采用官方 Before 已改写的当前模型及来源协议")
			}
		}
	}
}

// TestOfficialPOSTSearchAndAuthorization 验证搜索词只在 POST 正文、旧 GET 不受支持，匿名/客户端/对象标识均不能管理。
func TestOfficialPOSTSearchAndAuthorization(t *testing.T) {
	h := startHostWithManagementAuth(t, 1<<30, true, "", true)
	h.monitorAll(t)
	for round := 0; round < 2; round++ {
		marker := fmt.Sprintf("OFFICIAL-SEARCH-%d-%d", round, time.Now().UnixNano())
		record := searchSubmit(t, h, keyA, "audit-test-model", marker)
		for _, key := range []string{"", keyA, record.ObjectID} {
			code, _ := h.search(t, key, map[string]any{"q": marker, "offset": 0, "limit": 1})
			if code != http.StatusUnauthorized && code != http.StatusForbidden {
				t.Fatal("非管理员可通过 POST 搜索匿名对象数据")
			}
		}
		page := searchRecords(t, h, mustSearchValues(t, "limit=1&offset=0"))
		code, raw := h.search(t, adminKey, map[string]any{"q": marker, "model": record.Model, "object_id": record.ObjectID, "from": record.ReceivedAt.Format(time.RFC3339Nano), "to": record.ReceivedAt.Format(time.RFC3339Nano), "offset": 0, "limit": 1})
		var filtered searchPage
		if page.Total != round+1 || code != 200 || json.Unmarshal(raw, &filtered) != nil || filtered.Total != 1 || len(filtered.Records) != 1 || filtered.Records[0].ID != record.ID {
			t.Fatal("POST 正文的全条件交集、总数或分页错误")
		}
		code, _ = h.request(t, "GET", api+"/records", adminKey, nil)
		if code != http.StatusNotFound && code != http.StatusMethodNotAllowed {
			t.Fatal("旧 GET 记录查询仍被支持")
		}
		code, _ = h.request(t, "GET", api+"/records/search", adminKey, nil)
		if code != http.StatusNotFound && code != http.StatusMethodNotAllowed {
			t.Fatal("搜索入口错误接受 GET")
		}
	}
	log, err := os.ReadFile(filepath.Join(h.dir, "host.log"))
	if err != nil || bytes.Contains(log, []byte("OFFICIAL-SEARCH-")) || bytes.Contains(log, []byte("?q=")) {
		t.Fatal("查询关键词进入宿主 URL 日志")
	}
	assertChatPrivacy(t, h)
}

// TestOfficialWebsocketExcluded 验证 ordinary 与 Codex duplex 两条真实 WebSocket 路径各两轮正常完成但均零采集。
func TestOfficialWebsocketExcluded(t *testing.T) {
	for _, mode := range []string{"ordinary", "duplex"} {
		t.Run(mode, func(t *testing.T) {
			upstream, received := auditWebsocketUpstream()
			config, model, business := "", "responses-client-model", upstream.URL+"/v1"
			if mode == "duplex" {
				upstream.Close()
				upstream, received = codexAuditUpstream()
				config, model, business = codexAuditConfiguration(upstream.URL), "ws-codex-model", ""
			}
			t.Cleanup(upstream.Close)
			h := startHostWithConfiguration(t, 1<<30, true, business, true, config)
			h.monitorAll(t)
			assertOfficialStatus(t, h)
			_, _, gaps := snapshotCounters(t, h)
			conn := dialAuditWebsocket(t, h, "/v1/responses", keyA)
			for round := 0; round < 2; round++ {
				marker := fmt.Sprintf("FORBIDDEN_OFFICIAL_WS_%s_%d", mode, round)
				sendAuditFrame(t, conn, fmt.Sprintf(`{"type":"response.create","model":%q,"input":[{"role":"user","content":%q}]}`, model, marker))
				frames := readAuditEvents(t, conn)
				if !strings.Contains(string(frames[len(frames)-1]), "response.completed") {
					t.Fatal("官方 WebSocket 实际模型调用没有完成")
				}
				select {
				case raw := <-received:
					if !bytes.Contains(raw, []byte(marker)) {
						t.Fatal("本轮真实上游未收到 WebSocket 输入")
					}
				case <-time.After(5 * time.Second):
					t.Fatal("本轮 WebSocket 没有进入上游执行")
				}
				assertHTTPOnceState(t, h, 0, gaps)
				if len(snapshotRecords(t, h)) != 0 {
					t.Fatal("不支持的 WebSocket 通过执行回调被错误采集")
				}
			}
			_ = conn.Close()
			assertChatPrivacy(t, h, "FORBIDDEN_OFFICIAL_WS_")
		})
	}
}
