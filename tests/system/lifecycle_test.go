// 本文件通过真实客户端提交、受保护接口和 SQLite 隐私白名单验证对象生命周期。
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

// lifecycleWrite 提交合成管理变更；path 指定公开入口，payload 为无秘密配置，失败终止测试，无返回值。
func lifecycleWrite(t *testing.T, h *instance, path string, payload any) {
	t.Helper()
	body, err := json.Marshal(payload)
	if err != nil {
		t.Fatal("合成管理配置编码失败")
	}
	code, _ := h.request(t, "POST", api+path, adminKey, body)
	if code != 200 {
		t.Fatalf("管理配置变更失败: path=%s status=%d", path, code)
	}
}

// lifecycleSubmit 发出末尾为 marker 的合成请求；key 选择客户端身份，返回模型状态，网络错误终止测试。
func lifecycleSubmit(t *testing.T, h *instance, key, marker string) int {
	t.Helper()
	body, err := json.Marshal(map[string]any{"model": "audit-test-model", "messages": []map[string]string{{"role": "user", "content": marker}}})
	if err != nil {
		t.Fatal("合成提交编码失败")
	}
	code, _ := h.request(t, "POST", "/v1/chat/completions", key, body)
	return code
}

// lifecycleRecords 读取公开分页结果；objectID 可为空，返回匹配记录和总数，接口异常终止测试。
func lifecycleRecords(t *testing.T, h *instance, objectID string) ([]savedRecord, int) {
	t.Helper()
	code, raw := h.search(t, adminKey, map[string]any{"limit": 100, "object_id": objectID})
	var list struct {
		Records []savedRecord `json:"records"`
		Total   int           `json:"total"`
	}
	if code != 200 || json.Unmarshal(raw, &list) != nil {
		t.Fatal("生命周期公开列表不可用")
	}
	return list.Records, list.Total
}

// lifecycleSaved 等待本轮唯一 marker 的已保存记录，再查询详情；返回记录，超时或内容不一致终止测试。
func lifecycleSaved(t *testing.T, h *instance, marker string) savedRecord {
	t.Helper()
	deadline := time.Now().Add(8 * time.Second)
	for {
		records, _ := lifecycleRecords(t, h, "")
		for _, record := range records {
			// 列表仅提供 preview，详情才是正文合同。
			code, raw := h.request(t, "GET", api+"/detail?id="+url.QueryEscape(record.ID), adminKey, nil)
			var detail savedRecord
			if code != 200 || json.Unmarshal(raw, &detail) != nil {
				t.Fatal("生命周期详情不可用")
			}
			if detail.Text == marker {
				return detail
			}
		}
		if time.Now().After(deadline) {
			t.Fatal("本轮唯一提交未出现在已保存结果中")
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// lifecycleState 返回公开运行状态；接口异常终止测试，不保存响应原文。
func lifecycleState(t *testing.T, h *instance) struct {
	State         string            `json:"state"`
	CapturePaused bool              `json:"capture_paused"`
	Saved         int64             `json:"saved"`
	Queued        int64             `json:"queued"`
	Gaps          map[string]uint64 `json:"gaps"`
} {
	t.Helper()
	var response struct {
		State struct {
			State         string            `json:"state"`
			CapturePaused bool              `json:"capture_paused"`
			Saved         int64             `json:"saved"`
			Queued        int64             `json:"queued"`
			Gaps          map[string]uint64 `json:"gaps"`
		} `json:"state"`
	}
	code, raw := h.request(t, "GET", api+"/status", adminKey, nil)
	if code != 200 || json.Unmarshal(raw, &response) != nil {
		t.Fatal("生命周期公开状态不可用")
	}
	return response.State
}

type lifecycleObject struct {
	ID         string `json:"id"`
	Name       string `json:"name"`
	Monitored  bool   `json:"monitored"`
	Valid      bool   `json:"valid"`
	HasHistory bool   `json:"has_history"`
}

// lifecycleObjects 返回公开对象目录；管理接口或解码失败终止测试，不保存无秘密标识的完整报告。
func lifecycleObjects(t *testing.T, h *instance) []lifecycleObject {
	t.Helper()
	var result struct {
		Objects []lifecycleObject `json:"objects"`
	}
	code, raw := h.request(t, "GET", api+"/objects", adminKey, nil)
	if code != 200 || json.Unmarshal(raw, &result) != nil {
		t.Fatal("生命周期对象目录不可用")
	}
	return result.Objects
}

// lifecycleKeys 通过宿主已有受保护接口替换合成 Key 名单；不调用插件监控操作，失败终止测试。
func lifecycleKeys(t *testing.T, h *instance, keys []string) {
	t.Helper()
	body, err := json.Marshal(keys)
	if err != nil {
		t.Fatal("合成名单编码失败")
	}
	code, _ := h.request(t, "PUT", "/v0/management/api-keys", adminKey, body)
	if code != 200 {
		t.Fatalf("宿主合成名单变更失败: status=%d", code)
	}
	deadline := time.Now().Add(8 * time.Second)
	for _, key := range keys {
		for {
			status, _ := h.request(t, "GET", "/v1/models", key, nil)
			if status != 401 && status != 403 {
				break
			}
			if time.Now().After(deadline) {
				t.Fatal("宿主新增Key认证没有在期限内生效")
			}
			time.Sleep(20 * time.Millisecond)
		}
	}
}

// TestIdentityUnavailableKeepsHistoryWithoutGuessing 验证已加载种子的外部权限故障导致停采，已保存历史与名称仍可读。
func TestIdentityUnavailableKeepsHistoryWithoutGuessing(t *testing.T) {
	h := startHost(t, 1<<30, true, "")
	h.monitorAll(t)
	marker := fmt.Sprintf("LIFECYCLE-SEED-FAULT-%d-", time.Now().UnixNano())
	modelCode := lifecycleSubmit(t, h, keyA, marker+"before")
	before := lifecycleSaved(t, h, marker+"before")
	seedPath := filepath.Join(h.stateDir, "caller-scope-v1-sqlite-v1.seed")
	if err := os.Chmod(seedPath, 0644); err != nil {
		t.Fatal("合成种子权限故障设置失败")
	}
	if lifecycleSubmit(t, h, keyB, marker+"must-not-save") != modelCode {
		t.Fatal("种子权限故障改变模型调用")
	}
	waitBudgetState(t, h, func(state budgetState) bool {
		return state.State == "paused" && state.Reason == "storage_error" && state.Queued == 0
	})
	if _, total := lifecycleRecords(t, h, ""); total != 1 {
		t.Fatal("种子权限故障后仍保存新记录")
	}
	if old := lifecycleSaved(t, h, marker+"before"); old.ID != before.ID || old.ObjectID != before.ObjectID || old.Name != before.Name {
		t.Fatal("种子故障改变了已保存历史关联")
	}
}

// TestLifecyclePrivacyWhitelist 验证生命周期新增持久配置、诊断和管理输出不含原Key/特征片段，输入记录白名单不扩大。
func TestLifecyclePrivacyWhitelist(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if respondDiscovery(w, r) {
			return
		}
		_, _ = io.Copy(io.Discard, r.Body)
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"id":"synthetic-privacy","choices":[{"index":0,"message":{"role":"assistant","content":"ok"}}]}`)
	}))
	defer upstream.Close()
	h := startHost(t, 1<<30, true, upstream.URL)
	if objects := lifecycleObjects(t, h); len(objects) != 2 {
		t.Fatal("上游凭据被误列为客户端对象")
	}
	h.monitorAll(t)
	marker := fmt.Sprintf("LIFECYCLE-PRIVACY-%d-", time.Now().UnixNano())
	lifecycleSubmit(t, h, keyA, marker+"before")
	before := lifecycleSaved(t, h, marker+"before")
	lifecycleWrite(t, h, "/remove", map[string]string{"object_id": before.ObjectID})
	lifecycleWrite(t, h, "/rename", map[string]string{"object_id": before.ObjectID, "name": "只在对象目录存放的当前名称"})
	lifecycleWrite(t, h, "/pause", map[string]bool{"paused": true})
	h.stop()
	stateDir := filepath.Join(h.dir, "audit")
	h = startHost(t, 1<<30, true, upstream.URL, stateDir)
	forbidden := []string{keyA, keyB, adminKey, "synthetic-upstream-only", "client-audit-a-only", "client-audit-b-only", "upstream-only"}
	for _, path := range []string{"/objects", "/status", "/records/search", "/detail?id=" + before.ID} {
		code, raw := managementRead(t, h, path, adminKey)
		if code != 200 {
			t.Fatal("生命周期隐私查询不可用")
		}
		for _, secret := range forbidden {
			if bytes.Contains(raw, []byte(secret)) {
				t.Fatal("管理输出包含原Key或特征片段")
			}
		}
	}
	assertRecordSchema(t, stateDir, 1)
	records := retentionOutput(t, stateDir)
	if !bytes.Contains(records, []byte(before.ID)) {
		t.Fatal("SQLite 隐私夹具没有保存本轮对照记录")
	}
	for _, secret := range forbidden {
		if bytes.Contains(records, []byte(secret)) {
			t.Fatal("SQLite 逻辑记录包含禁存 Key 或片段")
		}
	}
	if err := filepath.WalkDir(stateDir, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			return nil
		}
		raw, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		for _, secret := range forbidden {
			if bytes.Contains(raw, []byte(secret)) {
				return fmt.Errorf("新增存储出现禁存Key或片段")
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	log, err := os.ReadFile(filepath.Join(h.dir, "host.log"))
	if err != nil {
		t.Fatal("合成诊断读取失败")
	}
	for _, secret := range forbidden {
		if bytes.Contains(log, []byte(secret)) {
			t.Fatal("诊断包含认证Key或特征片段")
		}
	}
}

// TestLifecycleStableIdentityAcrossHostChanges 验证名单重排、插入、同值、轮换与重启均不依赖下标或名称。
func TestLifecycleStableIdentityAcrossHostChanges(t *testing.T) {
	h := startHost(t, 1<<30, true, "")
	h.monitorAll(t)
	marker := fmt.Sprintf("LIFECYCLE-IDENTITY-%d-", time.Now().UnixNano())
	lifecycleSubmit(t, h, keyA, marker+"a0")
	a := lifecycleSaved(t, h, marker+"a0")
	lifecycleSubmit(t, h, keyB, marker+"b0")
	b := lifecycleSaved(t, h, marker+"b0")
	const keyC = "synthetic-client-audit-c-lifecycle-only"
	lifecycleKeys(t, h, []string{keyB, keyC, keyA, keyA})
	lifecycleSubmit(t, h, keyC, marker+"discover-c")
	objects := lifecycleObjects(t, h)
	if len(objects) != 3 {
		t.Fatal("相同值 Key 产生重复审计对象或插入没有生效")
	}
	for key, expected := range map[string]string{keyA: a.ObjectID, keyB: b.ObjectID} {
		text := marker + "reordered-" + expected[:8]
		lifecycleSubmit(t, h, key, text)
		if got := lifecycleSaved(t, h, text); got.ObjectID != expected {
			t.Fatal("重排或插入改变稳定身份")
		}
	}
	newID := ""
	for _, object := range objects {
		if object.ID != a.ObjectID && object.ID != b.ObjectID {
			newID = object.ID
		}
	}
	if newID == "" {
		t.Fatal("新 Key 没有独立对象")
	}
	lifecycleWrite(t, h, "/monitor", map[string]string{"object_id": newID, "name": "轮换同名对象"})
	lifecycleWrite(t, h, "/rename", map[string]string{"object_id": a.ObjectID, "name": "轮换同名对象"})
	lifecycleKeys(t, h, []string{keyB, keyC})
	// 宿主配置保存与认证 provider 热重载异步；用公开认证结果等待生效，而不是猜测固定延迟。
	deadline := time.Now().Add(8 * time.Second)
	for {
		code, _ := h.request(t, "GET", "/v1/models", keyA, nil)
		if code == 401 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("宿主已移除Key的认证配置未生效")
		}
		time.Sleep(20 * time.Millisecond)
	}
	if code := lifecycleSubmit(t, h, keyA, marker+"invalid"); code != 401 {
		t.Fatalf("宿主认证状态未拒绝已移除Key: status=%d", code)
	}
	lifecycleSubmit(t, h, keyC, marker+"c")
	if c := lifecycleSaved(t, h, marker+"c"); c.ObjectID != newID || c.ObjectID == a.ObjectID {
		t.Fatal("不同Key轮换因同名合并历史")
	}
	lifecycleKeys(t, h, []string{keyC, keyA, keyB, keyA})
	lifecycleSubmit(t, h, keyA, marker+"readded")
	if got := lifecycleSaved(t, h, marker+"readded"); got.ObjectID != a.ObjectID {
		t.Fatal("同原始Key返回宿主名单未沿用身份")
	}
	lifecycleWrite(t, h, "/remove", map[string]string{"object_id": b.ObjectID})
	h.stop()
	stateDir := filepath.Join(h.dir, "audit")
	h = startHost(t, 1<<30, true, "", stateDir)
	lifecycleSubmit(t, h, keyA, marker+"restart")
	if got := lifecycleSaved(t, h, marker+"restart"); got.ObjectID != a.ObjectID || got.Name != "轮换同名对象" {
		t.Fatal("重启后新提交丢失身份或名称")
	}
	for _, object := range lifecycleObjects(t, h) {
		if object.ID == b.ObjectID && (object.Monitored || !object.HasHistory) {
			t.Fatal("重启后丢失停用状态或历史入口")
		}
	}
	if old := lifecycleSaved(t, h, marker+"a0"); old.ID != a.ID || old.Name != "轮换同名对象" {
		t.Fatal("重启旧记录未保持当前名称关联")
	}
}

// TestLifecycleWritesRequireManagementAuth 验证所有新增写入口拒绝匿名、客户端Key和对象ID，拒绝后配置不变。
func TestLifecycleWritesRequireManagementAuth(t *testing.T) {
	for _, path := range []string{"/monitor", "/remove", "/rename", "/pause"} {
		t.Run(path, func(t *testing.T) {
			// 每个路由独立实例，不把宿主既有错误管理凭据封禁误当配置写入结果。
			h := startHost(t, 1<<30, true, "")
			ids := h.monitorAll(t)
			before := lifecycleObjects(t, h)
			for _, key := range []string{"", keyA, ids[0], "synthetic-wrong-admin-lifecycle"} {
				body, _ := json.Marshal(map[string]any{"object_id": ids[0], "name": "拒绝改名", "paused": true})
				code, _ := h.request(t, "POST", api+path, key, body)
				if code != 401 && code != 403 {
					t.Fatalf("非管理员写入未拒绝: path=%s status=%d", path, code)
				}
			}
			if !reflect.DeepEqual(before, lifecycleObjects(t, h)) || lifecycleState(t, h).CapturePaused {
				t.Fatal("未授权写操作改变了配置")
			}
		})
	}
}

// TestLegacyMonitorConfigurationMigratesSafely 验证旧对象配置正常加载、保持名称和身份，仅在成功变更时升级。
func TestLegacyMonitorConfigurationMigratesSafely(t *testing.T) {
	h := startHost(t, 1<<30, true, "")
	ids := h.monitorAll(t)
	h.stop()
	stateDir := filepath.Join(h.dir, "audit")
	path := filepath.Join(stateDir, "monitors.json")
	if _, err := os.ReadFile(path); err != nil {
		t.Fatal("旧格式夹具配置不可读")
	}
	legacy, _ := json.Marshal(map[string]string{ids[0]: "旧格式对象"})
	if err := os.WriteFile(path, legacy, 0600); err != nil {
		t.Fatal("旧格式夹具写入失败")
	}
	h = startHost(t, 1<<30, true, "", stateDir)
	known := lifecycleObjects(t, h)
	found := false
	for _, object := range known {
		if object.ID == ids[0] {
			found = object.Monitored && object.Name == "旧格式对象"
		}
	}
	if !found {
		t.Fatal("旧格式名单未保留监控与名称")
	}
	initial, err := os.ReadFile(path)
	if err != nil || string(initial) != string(legacy) {
		t.Fatal("加载旧格式时擅自改写配置")
	}
	lifecycleWrite(t, h, "/rename", map[string]string{"object_id": ids[0], "name": "升级后对象"})
	h.stop()
	h = startHost(t, 1<<30, true, "", stateDir)
	found = false
	for _, object := range lifecycleObjects(t, h) {
		if object.ID == ids[0] {
			found = object.Monitored && object.Name == "升级后对象"
		}
	}
	if !found {
		t.Fatal("升级后配置重启丢失对象")
	}
}

// TestAmbiguousObjectConfigurationStopsCapture 验证版本化配置拒绝对象的额外字段，不能静默接受或覆盖损坏目录。
func TestAmbiguousObjectConfigurationStopsCapture(t *testing.T) {
	h := startHost(t, 1<<30, true, "")
	ids := h.monitorAll(t)
	h.stop()
	stateDir := filepath.Join(h.dir, "audit")
	configPath := filepath.Join(stateDir, "monitors.json")
	if _, err := os.ReadFile(configPath); err != nil {
		t.Fatal("合成配置读取失败")
	}
	// 只有本测试生成的配置被替换，用不合法额外字段探测升级格式是否真正严格。
	data := []byte(fmt.Sprintf(`{"version":2,"capture_paused":false,"objects":{"%s":{"name":"合成对象","monitored":true,"Name":"歧义名称"}}}`, ids[0]))
	if err := os.WriteFile(configPath, data, 0600); err != nil {
		t.Fatal("合成不合法配置写入失败")
	}
	h = startHost(t, 1<<30, true, "", stateDir)
	if state := lifecycleState(t, h); state.State != "paused" {
		t.Fatal("歧义配置被静默接受")
	}
	lifecycleSubmit(t, h, keyA, fmt.Sprintf("LIFECYCLE-INVALID-CONFIG-%d", time.Now().UnixNano()))
	log, err := os.ReadFile(filepath.Join(h.dir, "host.log"))
	if err != nil || bytes.Contains(log, []byte("LIFECYCLE-INVALID-CONFIG")) {
		t.Fatal("损坏配置的诊断泄漏了输入")
	}
	body, _ := json.Marshal(map[string]string{"object_id": ids[0], "name": "不应覆盖旧配置"})
	code, _ := h.request(t, "POST", api+"/monitor", adminKey, body)
	if code == 200 {
		t.Fatal("损坏配置被管理操作静默覆盖")
	}
	after, err := os.ReadFile(configPath)
	if err != nil || string(after) != string(data) {
		t.Fatal("失败的配置更新改写了原文件")
	}
}

// TestObjectFilterAndRenameKeepIndependentHistory 验证同名对象不混合历史，筛选分页在全局分页前执行。
func TestObjectFilterAndRenameKeepIndependentHistory(t *testing.T) {
	h := startHost(t, 1<<30, true, "")
	h.monitorAll(t)
	marker := fmt.Sprintf("LIFECYCLE-FILTER-%d-", time.Now().UnixNano())
	lifecycleSubmit(t, h, keyA, marker+"a1")
	a := lifecycleSaved(t, h, marker+"a1")
	lifecycleSubmit(t, h, keyB, marker+"b1")
	b := lifecycleSaved(t, h, marker+"b1")
	if a.ObjectID == b.ObjectID {
		t.Fatal("两个不同 Key 被串为同一对象")
	}
	for _, id := range []string{a.ObjectID, b.ObjectID} {
		lifecycleWrite(t, h, "/rename", map[string]string{"object_id": id, "name": "同名对象"})
	}
	lifecycleSubmit(t, h, keyA, marker+"a2")
	lifecycleSaved(t, h, marker+"a2")
	lifecycleSubmit(t, h, keyB, marker+"b2")
	lifecycleSaved(t, h, marker+"b2")
	code, raw := h.search(t, adminKey, map[string]any{"limit": 1, "offset": 1, "object_id": a.ObjectID})
	var list struct {
		Records []savedRecord `json:"records"`
		Total   int           `json:"total"`
	}
	if code != 200 || json.Unmarshal(raw, &list) != nil || list.Total != 2 || len(list.Records) != 1 || list.Records[0].ID != a.ID || list.Records[0].Name != "同名对象" {
		t.Fatal("同名对象筛选、倒序分页或当前名称不正确")
	}
	lifecycleWrite(t, h, "/remove", map[string]string{"object_id": a.ObjectID})
	lifecycleWrite(t, h, "/rename", map[string]string{"object_id": a.ObjectID, "name": "已移除对象当前名称"})
	if record := lifecycleSaved(t, h, marker+"a1"); record.Name != "已移除对象当前名称" {
		t.Fatal("已移除对象历史无法改名")
	}
}

// TestManualPausePersistsWithoutGaps 验证主动暂停可重启保留、跳过不增加缺口，恢复后正常续采。
func TestManualPausePersistsWithoutGaps(t *testing.T) {
	h := startHost(t, 1<<30, true, "")
	h.monitorAll(t)
	marker := fmt.Sprintf("LIFECYCLE-PAUSE-%d-", time.Now().UnixNano())
	modelCode := lifecycleSubmit(t, h, keyA, marker+"before")
	before := lifecycleSaved(t, h, marker+"before")
	baseline := lifecycleState(t, h)
	lifecycleWrite(t, h, "/pause", map[string]bool{"paused": true})
	if !lifecycleState(t, h).CapturePaused {
		t.Fatal("公开状态没有区分主动暂停")
	}
	if lifecycleSubmit(t, h, keyA, marker+"skipped") != modelCode {
		t.Fatal("主动暂停改变了模型响应")
	}
	state := lifecycleState(t, h)
	if state.Saved != baseline.Saved || !reflect.DeepEqual(state.Gaps, baseline.Gaps) {
		t.Fatal("主动暂停采集新副本或被误计为缺口")
	}
	h.stop()
	h = startHost(t, 1<<30, true, "", h.dir+"/audit")
	if !lifecycleState(t, h).CapturePaused {
		t.Fatal("重启丢失主动暂停配置")
	}
	if old := lifecycleSaved(t, h, marker+"before"); old.ID != before.ID || old.Name != before.Name {
		t.Fatal("暂停重启丢失名称或历史关联")
	}
	lifecycleSubmit(t, h, keyA, marker+"restart-skipped")
	if _, total := lifecycleRecords(t, h, ""); total != 1 {
		t.Fatal("重启后主动暂停被自动解除")
	}
	lifecycleWrite(t, h, "/pause", map[string]bool{"paused": false})
	lifecycleSubmit(t, h, keyA, marker+"after")
	after := lifecycleSaved(t, h, marker+"after")
	if after.ObjectID != before.ObjectID || lifecycleState(t, h).CapturePaused {
		t.Fatal("恢复后没有沿用原对象续采")
	}
}

// TestRemovedObjectKeepsHistory 验证移除停止续采而保留名称、历史和同 Key 重新加入后的关联。
func TestRemovedObjectKeepsHistory(t *testing.T) {
	h := startHost(t, 1<<30, true, "")
	h.monitorAll(t)
	marker := fmt.Sprintf("LIFECYCLE-REMOVE-%d-", time.Now().UnixNano())
	beforeCode := lifecycleSubmit(t, h, keyA, marker+"before")
	before := lifecycleSaved(t, h, marker+"before")
	lifecycleWrite(t, h, "/remove", map[string]string{"object_id": before.ObjectID})
	state := lifecycleState(t, h)
	if code := lifecycleSubmit(t, h, keyA, marker+"skipped"); code != beforeCode {
		t.Fatal("移除审计改变了模型请求状态")
	}
	records, total := lifecycleRecords(t, h, "")
	if total != 1 || len(records) != 1 || records[0].ID != before.ID || records[0].Name != before.Name || lifecycleState(t, h).Saved != state.Saved {
		t.Fatal("移除没有保留原历史或仍在采集")
	}
	lifecycleWrite(t, h, "/monitor", map[string]string{"object_id": before.ObjectID, "name": "重新加入对象"})
	lifecycleSubmit(t, h, keyA, marker+"after")
	after := lifecycleSaved(t, h, marker+"after")
	if after.ObjectID != before.ObjectID || after.Name != "重新加入对象" {
		t.Fatal("同一个 Key 重新加入没有沿用对象")
	}
	records, total = lifecycleRecords(t, h, "")
	if total != 2 || len(records) != 2 {
		t.Fatal("重新加入没有保留旧记录")
	}
	for _, record := range records {
		if record.Name != after.Name {
			t.Fatal("旧记录未显示对象当前名称")
		}
	}
}
