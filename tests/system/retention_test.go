// 本文件通过真实宿主接口和隔离 SQLite 记录快照验证保留期、维护与持久内容。
package system

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	_ "github.com/mattn/go-sqlite3"
)

// retentionSettings 读取公开保留设置；返回天数和正文上限，接口或JSON不符则终止测试。
func retentionSettings(t *testing.T, h *instance) (int, int) {
	t.Helper()
	code, raw := h.request(t, "GET", api+"/settings", adminKey, nil)
	var settings struct {
		Days  int `json:"retention_days"`
		Limit int `json:"max_text_bytes"`
	}
	if code != http.StatusOK || json.Unmarshal(raw, &settings) != nil {
		t.Fatal("无法读取保留配置")
	}
	return settings.Days, settings.Limit
}

// TestRetentionSettingsStrictJSON 验证保留设置的范围、重复字段和确认值均严格校验，失败不改变配置。
func TestRetentionSettingsStrictJSON(t *testing.T) {
	h := startHost(t, 1<<30, true, "")
	for _, input := range []string{
		`{"retention_days":0}`, `{"retention_days":31}`, `{"retention_days":-1}`, `{"retention_days":1.5}`,
		`{"retention_days":null}`, `{"retention_days":"7"}`, `{"Retention_Days":1}`, `{"retention_days":1,"retention_days":2}`,
		`{"retention_days":1,"confirm_retention_shortening":null}`, `{"retention_days":1,"confirm_retention_shortening":"true"}`,
		`{"confirm_retention_shortening":true}`, `{"retention_days":1,"confirm_retention_shortening":true,"unknown":7}`,
		`{"retention_days":1,"confirm_retention_shortening":true,"confirm_retention_shortening":false}`,
	} {
		code, _ := h.request(t, "POST", api+"/settings", adminKey, []byte(input))
		if code != http.StatusBadRequest {
			t.Fatalf("非法保留设置未拒绝：%q，HTTP=%d", input, code)
		}
		if days, limit := retentionSettings(t, h); days != 7 || limit != 65536 {
			t.Fatal("非法配置改变了设置")
		}
	}
}

// TestRetentionSettingsConfirmation 验证缩短需确认、失败不部分发布，且调长和重启保持配置。
func TestRetentionSettingsConfirmation(t *testing.T) {
	h := startHost(t, 1<<30, true, "")
	if days, limit := retentionSettings(t, h); days != 7 || limit != 65536 {
		t.Fatalf("默认配置应为7天/65536字节，实际%d/%d", days, limit)
	}
	code, raw := h.request(t, "POST", api+"/settings", adminKey, []byte(`{"retention_days":1,"max_text_bytes":11}`))
	var rejection struct {
		Error string `json:"error"`
	}
	if code != http.StatusConflict || json.Unmarshal(raw, &rejection) != nil || rejection.Error != "retention_confirmation_required" {
		t.Fatalf("未经确认缩短应明确拒绝，HTTP=%d", code)
	}
	if days, limit := retentionSettings(t, h); days != 7 || limit != 65536 {
		t.Fatal("拒绝缩短却部分发布了配置")
	}
	code, _ = h.request(t, "POST", api+"/settings", adminKey, []byte(`{"retention_days":1,"max_text_bytes":11,"confirm_retention_shortening":true}`))
	if code != http.StatusOK {
		t.Fatalf("确认缩短失败，HTTP=%d", code)
	}
	if days, limit := retentionSettings(t, h); days != 1 || limit != 11 {
		t.Fatal("确认后配置未发布")
	}
	code, _ = h.request(t, "POST", api+"/settings", adminKey, []byte(`{"retention_days":30}`))
	if code != http.StatusOK {
		t.Fatalf("调长保留期失败，HTTP=%d", code)
	}
	stateDir := filepath.Join(h.dir, "audit")
	h.stop()
	h = startHost(t, 1<<30, true, "", stateDir)
	if days, limit := retentionSettings(t, h); days != 30 || limit != 11 {
		t.Fatal("重启丢失配置")
	}
}

// retentionClock 写入独立验收时钟；sweep序号改变才触发后台维护，失败终止测试。
func retentionClock(t *testing.T, path string, now time.Time, sweep int) {
	t.Helper()
	raw, err := json.Marshal(map[string]any{"now": now.Format(time.RFC3339Nano), "sweep": sweep})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path+".next", raw, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(path+".next", path); err != nil {
		t.Fatal(err)
	}
}

// newRetentionClock 创建保留的隔离时钟文件；返回绝对路径，目录或写入失败终止测试。
func newRetentionClock(t *testing.T, now time.Time) string {
	t.Helper()
	root, err := filepath.Abs("../../.test-data")
	if err != nil {
		t.Fatal(err)
	}
	dir, err := os.MkdirTemp(root, "retention-clock-")
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "clock.json")
	retentionClock(t, path, now, 0)
	return path
}

// openRecordDatabase 打开已有隔离 SQLite；writable 仅用于停机后的有效 schema 夹具，失败终止测试，不创建缺失数据库。
// 返回连接由调用者关闭；只读查询不把正常维护引起的数据库元数据变化当作记录改写。
func openRecordDatabase(t *testing.T, stateDir string, writable bool) *sql.DB {
	t.Helper()
	mode := "ro"
	if writable {
		mode = "rw"
	}
	location := &url.URL{Scheme: "file", Path: filepath.Join(stateDir, "records.sqlite")}
	query := url.Values{"mode": {mode}, "_busy_timeout": {"5000"}}
	if writable {
		query.Set("_journal_mode", "PERSIST")
	}
	location.RawQuery = query.Encode()
	db, err := sql.Open("sqlite3", location.String())
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	if err := db.Ping(); err != nil {
		db.Close()
		t.Fatal("隔离 SQLite 数据库不可读取", err)
	}
	return db
}

// assertRecordSchema 验证真实 records 表只含业务字段和必要的提交/时间索引列，且至少有 minimum 条记录。
// stateDir 必须已有 SQLite；返回前关闭连接，缺库、未知列、空夹具或查询失败均终止测试。
func assertRecordSchema(t *testing.T, stateDir string, minimum int) {
	t.Helper()
	db := openRecordDatabase(t, stateDir, false)
	defer db.Close()
	rows, err := db.Query("PRAGMA table_info(records)")
	if err != nil {
		t.Fatal(err)
	}
	allowed := map[string]bool{"seq": true, "id": true, "object_id": true, "received_at": true, "received_sec": true, "received_ns": true, "protocol": true, "purpose": true, "model": true, "request_reasoning": true, "text": true, "truncated": true, "extraction_incomplete": true}
	seen := 0
	for rows.Next() {
		var ordinal, notNull, primary int
		var name, kind string
		var defaultValue any
		if err := rows.Scan(&ordinal, &name, &kind, &notNull, &defaultValue, &primary); err != nil {
			t.Fatal(err)
		}
		if !allowed[name] {
			t.Fatalf("SQLite 记录新增非白名单列：%s", name)
		}
		seen++
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	rows.Close()
	if seen != len(allowed) {
		t.Fatal("SQLite 记录 schema 缺少约定列")
	}
	var count int
	if err := db.QueryRow("SELECT count(*) FROM records").Scan(&count); err != nil || count < minimum {
		t.Fatalf("SQLite 记录夹具数量不符：count=%d minimum=%d err=%v", count, minimum, err)
	}
}

// retentionOutput 读取 SQLite 中按提交顺序排列的全部记录，包括尚未物理删除的过期记录。
// 返回每条记录的确定性 JSON 快照而非数据库原字节；缺库、损坏或查询失败终止测试，零条记录返回空切片。
func retentionOutput(t *testing.T, stateDir string) []byte {
	t.Helper()
	db := openRecordDatabase(t, stateDir, false)
	defer db.Close()
	rows, err := db.Query("SELECT seq,id,object_id,received_at,received_sec,received_ns,protocol,purpose,model,request_reasoning,text,truncated,extraction_incomplete FROM records ORDER BY seq")
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var output []byte
	for rows.Next() {
		values := make([]any, 13)
		targets := make([]any, len(values))
		for i := range values {
			targets[i] = &values[i]
		}
		if err := rows.Scan(targets...); err != nil {
			t.Fatal(err)
		}
		data, err := json.Marshal(values)
		if err != nil {
			t.Fatal(err)
		}
		output = append(output, data...)
		output = append(output, '\n')
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return output
}

// TestRetentionExpiresAcrossQueries 验证七天边界立即隐藏全部查询入口，且隐藏不依赖物理清理。
func TestRetentionExpiresAcrossQueries(t *testing.T) {
	received := time.Date(2030, 1, 1, 12, 0, 0, 0, time.UTC)
	clock := newRetentionClock(t, received)
	h := startRetentionHost(t, 1<<30, "", clock)
	h.monitorAll(t)
	marker := fmt.Sprintf("RETENTION-BOUNDARY-%d", time.Now().UnixNano())
	// 客户端自报的未来时间不能改变宿主接收时刻。
	body, _ := json.Marshal(map[string]any{"model": "retention-model", "received_at": "2099-01-01T00:00:00Z", "messages": []map[string]string{{"role": "user", "content": marker}}})
	h.request(t, "POST", "/v1/chat/completions", keyA, body)
	record := waitChatDetail(t, h, marker)
	if !record.ReceivedAt.Equal(received) {
		t.Fatal("接收时间没有使用服务端时钟")
	}
	retentionClock(t, clock, received.Add(7*24*time.Hour-time.Nanosecond), 0)
	if page := searchRecords(t, h, nil); page.Total != 1 {
		t.Fatal("七天前记录提前到期")
	}
	// 先读取到期前详情；重复读取仍须重新检查逻辑保留期限。
	code, _ := h.request(t, "GET", api+"/detail?id="+record.ID, adminKey, nil)
	if code != 200 {
		t.Fatal("到期前详情不可读")
	}
	retentionClock(t, clock, received.Add(7*24*time.Hour), 0)
	code, _ = h.request(t, "GET", api+"/detail?id="+record.ID, adminKey, nil)
	if code != 404 {
		t.Fatalf("重复读取详情暴露过期正文，HTTP=%d", code)
	}
	for _, query := range []url.Values{nil, {"q": {marker}}, {"object_id": {record.ObjectID}, "model": {"retention-model"}, "limit": {"1"}}, {"offset": {"1"}}} {
		if page := searchRecords(t, h, query); page.Total != 0 || len(page.Records) != 0 {
			t.Fatal("过期记录仍进入列表、搜索或分页总数")
		}
	}
	code, _ = h.request(t, "GET", api+"/detail?id="+record.ID, adminKey, nil)
	if code != 404 {
		t.Fatal("扫描详情暴露过期正文")
	}
	code, raw := h.request(t, "GET", api+"/objects", adminKey, nil)
	var catalog struct {
		Objects []struct {
			ID         string `json:"id"`
			HasHistory bool   `json:"has_history"`
		} `json:"objects"`
	}
	if code != 200 || json.Unmarshal(raw, &catalog) != nil {
		t.Fatal("对象目录不可读")
	}
	for _, object := range catalog.Objects {
		if object.ID == record.ObjectID && object.HasHistory {
			t.Fatal("过期历史仍被对象目录标为可查")
		}
	}
	if !bytes.Contains(retentionOutput(t, h.stateDir), []byte(marker)) {
		t.Fatal("本测试尚未触发后台维护，记录应仍在隔离输出中")
	}
}

// waitRetentionOutput 等待本轮持久输出满足条件；超时或读取失败终止测试，不以固定等待猜测清理结束。
func waitRetentionOutput(t *testing.T, h *instance, matches func([]byte) bool) []byte {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		output := retentionOutput(t, h.stateDir)
		if matches(output) {
			return output
		}
		if time.Now().After(deadline) {
			t.Fatal("隔离持久化输出未出现本轮清理结果")
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// retentionUsage 读取公开保守占用；返回字节数，接口或解码失败终止测试。
func retentionUsage(t *testing.T, h *instance) int64 {
	t.Helper()
	code, raw := h.request(t, "GET", api+"/status", adminKey, nil)
	var status struct {
		State struct {
			Used int64 `json:"used_bytes"`
		} `json:"state"`
	}
	if code != 200 || json.Unmarshal(raw, &status) != nil {
		t.Fatal("无法读取保留空间占用")
	}
	return status.State.Used
}

// TestRetentionSweepPreservesLiveLines 验证主动暂停期间仍删除到期记录、释放 SQLite 空间，并完整保留幸存记录。
func TestRetentionSweepPreservesLiveLines(t *testing.T) {
	received := time.Date(2030, 2, 1, 0, 0, 0, 0, time.UTC)
	clock := newRetentionClock(t, received)
	h := startRetentionHost(t, 1<<30, "", clock)
	h.monitorAll(t)
	marker := fmt.Sprintf("RETENTION-SWEEP-%d", time.Now().UnixNano())
	body, _ := json.Marshal(map[string]any{"model": "retention-model", "messages": []map[string]string{{"role": "user", "content": marker + "-OLD-" + strings.Repeat("X", 32000)}}})
	h.request(t, "POST", "/v1/chat/completions", keyA, body)
	old := waitChatDetail(t, h, marker+"-OLD-")
	retentionClock(t, clock, received.Add(2*24*time.Hour), 0)
	live := searchSubmit(t, h, keyA, "retention-model", marker+"-LIVE")
	original := retentionOutput(t, h.stateDir)
	var liveLine []byte
	for _, line := range bytes.SplitAfter(original, []byte("\n")) {
		if bytes.Contains(line, []byte(live.ID)) {
			liveLine = append([]byte(nil), line...)
		}
	}
	if len(liveLine) == 0 {
		t.Fatal("未找到未过期对照行")
	}
	// 先经列表和详情访问两条记录，维护后查询必须反映已提交的删除。
	searchRecords(t, h, nil)
	snapshotMutation(t, h, "/pause", map[string]bool{"paused": true})
	usedBefore := retentionUsage(t, h)
	retentionClock(t, clock, received.Add(7*24*time.Hour), 0)
	if !bytes.Contains(retentionOutput(t, h.stateDir), []byte(old.ID)) {
		t.Fatal("尚未触发周期维护就删除了物理记录")
	}
	retentionClock(t, clock, received.Add(7*24*time.Hour), 1)
	output := waitRetentionOutput(t, h, func(raw []byte) bool { return !bytes.Contains(raw, []byte(old.ID)) })
	if !bytes.Equal(output, liveLine) || len(output) >= len(original) {
		t.Fatal("SQLite 维护修改了幸存记录，或没有删除到期记录")
	}
	code, raw := h.request(t, "GET", api+"/detail?id="+live.ID, adminKey, nil)
	var detail savedRecord
	if code != 200 || json.Unmarshal(raw, &detail) != nil || detail.Text != marker+"-LIVE" {
		t.Fatal("SQLite 维护后幸存记录详情不符")
	}
	if page := searchRecords(t, h, nil); page.Total != 1 || len(page.Records) != 1 || page.Records[0].ID != live.ID {
		t.Fatal("清理后分页计数错误")
	}
	if usedAfter := retentionUsage(t, h); usedAfter >= usedBefore {
		t.Fatal("后台回收没有减少公开目录占用")
	}
	// 没有新提交时也清理最后一条记录，主动暂停不冻结期限。
	retentionClock(t, clock, received.Add(9*24*time.Hour), 2)
	waitRetentionOutput(t, h, func(raw []byte) bool { return len(raw) == 0 })
}

// TestRetentionRepeatedIncreaseAtSameInstant 验证同一时钟刻度的连续调长不会生成重启无法读取的历史规则。
func TestRetentionRepeatedIncreaseAtSameInstant(t *testing.T) {
	now := time.Date(2030, 3, 1, 0, 0, 0, 0, time.UTC)
	clock := newRetentionClock(t, now)
	h := startRetentionHost(t, 1<<30, "", clock)
	snapshotMutation(t, h, "/settings", map[string]int{"retention_days": 8})
	snapshotMutation(t, h, "/settings", map[string]int{"retention_days": 30})
	stateDir := h.stateDir
	h.stop()
	h = startRetentionHost(t, 1<<30, "", clock, stateDir)
	if days, _ := retentionSettings(t, h); days != 30 {
		t.Fatal("相同时刻的重复设置破坏了持久配置")
	}
}

// TestRetentionNeverExtendsHistory 验证调长不延长已执行的旧提交，缩短再调长及重启不能复活历史。
func TestRetentionNeverExtendsHistory(t *testing.T) {
	received := time.Date(2030, 4, 1, 0, 0, 0, 0, time.UTC)
	clock := newRetentionClock(t, received)
	upstream, _ := snapshotUpstream()
	defer upstream.Close()
	h := startRetentionHost(t, 1<<30, upstream.URL, clock)
	h.monitorAll(t)
	marker := fmt.Sprintf("RETENTION-LIFETIME-%d", time.Now().UnixNano())
	old := searchSubmit(t, h, keyA, "audit-test-model", marker+"-OLD")
	retentionClock(t, clock, received.Add(time.Hour), 0)
	snapshotMutation(t, h, "/settings", map[string]int{"retention_days": 30})
	retentionClock(t, clock, received.Add(6*24*time.Hour), 0)
	later := searchSubmit(t, h, keyA, "audit-test-model", marker+"-LATER")
	retentionClock(t, clock, received.Add(7*24*time.Hour), 0)
	code, _ := h.request(t, "GET", api+"/detail?id="+old.ID, adminKey, nil)
	if code != 404 {
		t.Fatal("调长延长了已有历史的七天寿命")
	}
	if page := searchRecords(t, h, nil); page.Total != 1 || page.Records[0].ID != later.ID {
		t.Fatal("新提交没有采用30天，或旧记录被延长")
	}
	retentionClock(t, clock, received.Add(8*24*time.Hour), 0)
	snapshotMutation(t, h, "/settings", map[string]any{"retention_days": 1, "confirm_retention_shortening": true})
	if page := searchRecords(t, h, nil); page.Total != 0 {
		t.Fatal("确认缩短没有使历史提前到期")
	}
	snapshotMutation(t, h, "/settings", map[string]int{"retention_days": 30})
	if page := searchRecords(t, h, nil); page.Total != 0 {
		t.Fatal("再次调长复活了尚未物理删除的历史")
	}
	fresh := searchSubmit(t, h, keyA, "audit-test-model", marker+"-FRESH")
	stateDir := h.stateDir
	h.stop()
	retentionClock(t, clock, received.Add(9*24*time.Hour), 0)
	h = startRetentionHost(t, 1<<30, upstream.URL, clock, stateDir)
	if days, _ := retentionSettings(t, h); days != 30 {
		t.Fatal("重启丢失当前保留期")
	}
	if page := searchRecords(t, h, nil); page.Total != 1 || page.Records[0].ID != fresh.ID {
		t.Fatal("重启丢失历史期限约束")
	}
	output := retentionOutput(t, stateDir)
	if bytes.Contains(output, []byte(old.ID)) || bytes.Contains(output, []byte(later.ID)) || bytes.Contains(output, []byte(marker+"-SLOW")) || !bytes.Contains(output, []byte(fresh.ID)) {
		t.Fatal("启动没有清理过期历史，或删除了未到期对照")
	}
}

// TestRetentionRemovedAndDowntime 验证移除监控与停机不冻结期限，启动先清理到期输入而不重置寿命。
func TestRetentionRemovedAndDowntime(t *testing.T) {
	received := time.Date(2030, 5, 1, 0, 0, 0, 0, time.UTC)
	clock := newRetentionClock(t, received)
	h := startRetentionHost(t, 1<<30, "", clock)
	h.monitorAll(t)
	marker := fmt.Sprintf("RETENTION-DOWNTIME-%d", time.Now().UnixNano())
	record := searchSubmit(t, h, keyA, "retention-model", marker)
	snapshotMutation(t, h, "/remove", map[string]string{"object_id": record.ObjectID})
	retentionClock(t, clock, received.Add(6*24*time.Hour), 0)
	if page := searchRecords(t, h, nil); page.Total != 1 {
		t.Fatal("移除监控提前删除了历史")
	}
	stateDir := h.stateDir
	h.stop()
	retentionClock(t, clock, received.Add(7*24*time.Hour), 0)
	h = startRetentionHost(t, 1<<30, "", clock, stateDir)
	if page := searchRecords(t, h, nil); page.Total != 0 {
		t.Fatal("停机冻结或重置了保留期限")
	}
	if len(retentionOutput(t, stateDir)) != 0 {
		t.Fatal("启动没有删除停机期间到期的 SQLite 记录")
	}
}
