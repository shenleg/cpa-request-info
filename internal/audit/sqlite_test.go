// 本文件提供关闭采集器后使用的真实 SQLite 夹具，并验证事务、字面查询、格式拒绝和空间回收。
package audit_test

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/shenleg/cpa-request-info/internal/audit"
)

// fixtureDatabase 打开已初始化且已关闭的审计库，保留 PERSIST 日志；返回连接，打开失败终止测试。
func fixtureDatabase(t *testing.T, dir string) *sql.DB {
	t.Helper()
	u := url.URL{Scheme: "file", Path: filepath.Join(dir, "records.sqlite")}
	u.RawQuery = "mode=rw&_journal_mode=PERSIST&_synchronous=FULL"
	db, err := sql.Open("sqlite3", u.String())
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	if err := db.Ping(); err != nil {
		db.Close()
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := db.Close(); err != nil {
			t.Error(err)
		}
	})
	return db
}

// closeFixture 同步结束直接 SQL 夹具，必须先于重新打开 Collector；失败终止测试，无返回值。
func closeFixture(t *testing.T, db *sql.DB) {
	t.Helper()
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
}

// insertFixtureRecords 将白名单记录逐条提交到真实库，保留 nil 与空思考对象差异；非法时间或 SQL 失败终止测试。
func insertFixtureRecords(t *testing.T, dir string, records ...audit.Record) {
	t.Helper()
	db := fixtureDatabase(t, dir)
	for _, record := range records {
		received, err := time.Parse(time.RFC3339Nano, record.ReceivedAt)
		if err != nil {
			t.Fatal(err)
		}
		var reasoning any
		if record.RequestReasoning != nil {
			raw, err := json.Marshal(record.RequestReasoning)
			if err != nil {
				t.Fatal(err)
			}
			reasoning = string(raw)
		}
		if _, err := db.Exec(`INSERT INTO records(id,object_id,received_at,received_sec,received_ns,protocol,purpose,model,request_reasoning,text,truncated,extraction_incomplete) VALUES(?,?,?,?,?,?,?,?,?,?,?,?)`, record.ID, record.ObjectID, record.ReceivedAt, received.Unix(), received.Nanosecond(), record.Protocol, string(record.Purpose), record.Model, reasoning, record.Text, record.Truncated, record.ExtractionIncomplete); err != nil {
			t.Fatal(err)
		}
	}
	closeFixture(t, db)
}

// fixtureRecord 返回具有指定身份、时间和正文的有效 SQLite 记录，不执行 I/O 或抛出错误。
func fixtureRecord(id, objectID string, at time.Time, text string) audit.Record {
	return audit.Record{ID: id, ObjectID: objectID, ReceivedAt: at.Format(time.RFC3339Nano), Protocol: "responses", Text: text, Model: "synthetic"}
}

// TestSQLiteLiteralSearchAndDuplicateIDs 验证 Unicode/NUL/通配符按字面匹配、时间含端点，详情始终选择最新存活重复 ID。
func TestSQLiteLiteralSearchAndDuplicateIDs(t *testing.T) {
	dir := newAuditDirectory(t)
	c := openAudit(t, dir, 0)
	id := monitorScope(t, c, "literal-scope")
	c.Close()
	at := time.Now().UTC().Truncate(time.Second)
	first := fixtureRecord("same-id", id, at, "A%_\x00Ä中 word")
	second := fixtureRecord("same-id", id, at.Add(time.Nanosecond), "newest")
	third := fixtureRecord("different-id", id, at.Add(time.Second), "aZXä中 WORD")
	insertFixtureRecords(t, dir, first, second, third)
	c = openAudit(t, dir, 0)
	for _, tc := range []struct {
		keyword string
		total   int
	}{
		{"a%_\x00Ä中", 1}, {"%", 1}, {"_", 1}, {"\x00", 1}, {"ä", 1}, {"Ä", 1}, {"WORD", 2}, {"中", 2}, {"not-present", 0},
	} {
		page, total, err := c.List(0, 100, audit.Query{Keyword: tc.keyword})
		if err != nil || total != tc.total || len(page) != tc.total {
			t.Fatalf("字面搜索 %q: total=%d records=%d err=%v", tc.keyword, total, len(page), err)
		}
	}
	from := at.In(time.FixedZone("offset", 8*3600))
	to := at.Add(time.Nanosecond)
	page, total, err := c.List(0, 1, audit.Query{From: &from, To: &to})
	if err != nil || total != 2 || len(page) != 1 || page[0].Text != "newest" {
		t.Fatalf("时间及分页: %+v total=%d err=%v", page, total, err)
	}
	for _, query := range []audit.Query{{}, {Keyword: "A%"}, {Keyword: "newest"}} {
		if _, _, err := c.List(0, 1, query); err != nil {
			t.Fatal(err)
		}
		got, err := c.Detail("same-id")
		if err != nil || !reflect.DeepEqual(got, second) {
			t.Fatalf("重复ID详情受页影响: %+v err=%v", got, err)
		}
	}
	if c.Status().Saved != 3 {
		t.Fatal("重复ID被合并")
	}
}

// TestSQLiteRollbackAndIndexes 验证未提交事务对重开不可见，结构化筛选和详情存在对应索引。
func TestSQLiteRollbackAndIndexes(t *testing.T) {
	dir := newAuditDirectory(t)
	c := openAudit(t, dir, 0)
	id := monitorScope(t, c, "rollback-scope")
	c.Close()
	db := fixtureDatabase(t, dir)
	tx, err := db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	at := time.Now().UTC()
	if _, err := tx.Exec(`INSERT INTO records(id,object_id,received_at,received_sec,received_ns,protocol,purpose,model,request_reasoning,text,truncated,extraction_incomplete) VALUES(?,?,?,?,?,'responses','unknown','synthetic','{}','uncommitted',0,0)`, "rollback", id, at.Format(time.RFC3339Nano), at.Unix(), at.Nanosecond()); err != nil {
		t.Fatal(err)
	}
	if err := tx.Rollback(); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct{ query, index string }{
		{"SELECT seq FROM records WHERE id='id' ORDER BY seq DESC LIMIT 1", "records_id_seq"},
		{"SELECT seq FROM records WHERE object_id='id' ORDER BY seq DESC", "records_object_seq"},
		{"SELECT seq FROM records WHERE model='model' ORDER BY seq DESC", "records_model_seq"},
		{"SELECT seq FROM records WHERE (received_sec,received_ns)>=(0,0)", "records_received"},
	} {
		rows, err := db.Query("EXPLAIN QUERY PLAN " + tc.query)
		if err != nil {
			t.Fatal(err)
		}
		var descriptions []string
		for rows.Next() {
			var a, b, d int
			var text string
			if err := rows.Scan(&a, &b, &d, &text); err != nil {
				t.Fatal(err)
			}
			descriptions = append(descriptions, text)
		}
		if err := rows.Err(); err != nil {
			t.Fatal(err)
		}
		if err := rows.Close(); err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(strings.Join(descriptions, " "), tc.index) {
			t.Fatalf("未使用索引%s: %v", tc.index, descriptions)
		}
	}
	closeFixture(t, db)
	c = openAudit(t, dir, 0)
	if c.Status().Saved != 0 {
		t.Fatal("回滚记录计入物理条数")
	}
	if _, err := c.Detail("rollback"); err == nil || err.Error() != "not_found" {
		t.Fatalf("回滚记录可见: %v", err)
	}
}

// TestSQLiteRejectsDamagedState 验证缺库、空库、坏库、绑定不符及半初始化不自动重建，保留原始文件。
func TestSQLiteRejectsDamagedState(t *testing.T) {
	for _, kind := range []string{"missing", "empty", "corrupt", "wrong-seed", "schema", "application-id", "version"} {
		t.Run(kind, func(t *testing.T) {
			dir := newAuditDirectory(t)
			c := openAudit(t, dir, 0)
			c.Close()
			path := filepath.Join(dir, "records.sqlite")
			switch kind {
			case "missing":
				if err := os.Rename(path, filepath.Join(dir, "preserved.sqlite")); err != nil {
					t.Fatal(err)
				}
			case "empty":
				writeAuditFile(t, dir, "records.sqlite", nil)
			case "corrupt":
				writeAuditFile(t, dir, "records.sqlite", bytes.Repeat([]byte{'x'}, 4096))
			case "wrong-seed":
				seed := readAuditFile(t, dir, scopeSeedFile)
				seed[len(seed)-1] ^= 1
				writeAuditFile(t, dir, scopeSeedFile, seed)
			default:
				db := fixtureDatabase(t, dir)
				statement := map[string]string{"schema": "ALTER TABLE records ADD COLUMN private_data TEXT", "application-id": "PRAGMA application_id=1", "version": "PRAGMA user_version=99"}[kind]
				if _, err := db.Exec(statement); err != nil {
					t.Fatal(err)
				}
				closeFixture(t, db)
			}
			var original []byte
			if kind != "missing" {
				original = readAuditFile(t, dir, "records.sqlite")
			}
			c = openAudit(t, dir, 0)
			if status := c.Status(); status.Reason != "storage_error" || status.State != "paused" {
				t.Fatalf("接受损坏状态: %+v", status)
			}
			c.Close()
			if kind == "missing" {
				if _, err := os.Lstat(path); !os.IsNotExist(err) {
					t.Fatalf("缺库被重建: %v", err)
				}
			} else if !bytes.Equal(original, readAuditFile(t, dir, "records.sqlite")) {
				t.Fatal("损坏库被改写")
			}
		})
	}
}

// TestSQLiteExpiryReleasesDatabase 验证混合历史只删除到期记录，实际缩小主库及日志，幸存字段保持原值。
func TestSQLiteExpiryReleasesDatabase(t *testing.T) {
	dir := newAuditDirectory(t)
	c := openAudit(t, dir, 0)
	id := monitorScope(t, c, "vacuum-scope")
	c.Close()
	now := time.Now().UTC()
	live := fixtureRecord("live", id, now, "survivor")
	records := []audit.Record{live}
	for n := 0; n < 96; n++ {
		records = append(records, fixtureRecord(fmt.Sprintf("expired-%d", n), id, now.Add(-8*24*time.Hour), strings.Repeat("x", 64<<10)))
	}
	insertFixtureRecords(t, dir, records...)
	before, err := os.Stat(filepath.Join(dir, "records.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	c = openAudit(t, dir, 0)
	if status := c.Status(); status.State != "active" || status.Saved != 1 {
		t.Fatalf("清理失败: %+v", status)
	}
	got, err := c.Detail("live")
	if err != nil || !reflect.DeepEqual(got, live) {
		t.Fatalf("幸存字段改变: %+v err=%v", got, err)
	}
	after, err := os.Stat(filepath.Join(dir, "records.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	if after.Size() >= before.Size()/2 {
		t.Fatalf("未真实缩库: before=%d after=%d", before.Size(), after.Size())
	}
	if status := c.Status(); status.UsedBytes != directoryCharge(t, dir) {
		t.Fatalf("清理后占用未更新: %+v", status)
	}
}

// TestSQLitePageBudgetRecovery 验证 SQL 前的页窗预算检查只报告容量暂停，已提交记录保留，调高预算可继续提交。
func TestSQLitePageBudgetRecovery(t *testing.T) {
	const budget int64 = 4 << 20
	dir := newAuditDirectory(t)
	c := openAudit(t, dir, budget)
	monitorScope(t, c, "page-budget-scope")
	text := strings.Repeat("x", 64<<10)
	body, err := json.Marshal(map[string]string{"input": text})
	if err != nil {
		t.Fatal(err)
	}
	paused := false
	for n := 0; n < 128; n++ {
		c.CaptureExecution(fmt.Sprintf("page-budget-%d", n), "page-budget-scope", "responses", audit.PurposeUnknown, body)
		deadline := time.Now().Add(5 * time.Second)
		for c.Status().Queued != 0 && time.Now().Before(deadline) {
			time.Sleep(time.Millisecond)
		}
		status := c.Status()
		if status.Queued != 0 || status.UsedBytes > budget {
			t.Fatalf("队列未排空或超出目录预算：%+v", status)
		}
		if status.State == "paused" {
			if status.Reason != "budget_exhausted" || status.Saved != int64(n) {
				t.Fatalf("页限拒收被误报为存储故障或已保存：%+v", status)
			}
			paused = true
			break
		}
	}
	if !paused || c.Status().Saved == 0 {
		t.Fatalf("夹具没有填满最小预算：%+v", c.Status())
	}
	if got, err := c.Detail("page-budget-0"); err != nil || got.Text != text {
		t.Fatalf("满额破坏已提交记录：%+v err=%v", got, err)
	}
	saved := c.Status().Saved
	raised := int64(16 << 20)
	if err := c.SetSettings(audit.SettingsChange{BudgetBytes: &raised}); err != nil {
		t.Fatal(err)
	}
	if c.Status().State != "active" {
		t.Fatalf("提高预算未恢复容量暂停：%+v", c.Status())
	}
	c.CaptureExecution("page-budget-resumed", "page-budget-scope", "responses", audit.PurposeUnknown, body)
	waitSaved(t, c, saved+1)
	c.Close()
	c = openAudit(t, dir, 0)
	if c.Status().Saved != saved+1 {
		t.Fatalf("满额与恢复记录重启不一致：%+v", c.Status())
	}
}

// TestSQLiteHotJournalRecovery 验证真实进程中断留下的 hot journal 先由 SQLite 恢复，未提交正文不会公开。
func TestSQLiteHotJournalRecovery(t *testing.T) {
	if dir := os.Getenv("CPA_AUDIT_UNIT_HOT_JOURNAL"); dir != "" {
		db := fixtureDatabase(t, dir)
		// 夹具故意启用 spill 制造提交前已改主库的 hot journal；正式采集器仍始终禁用 spill。
		if _, err := db.Exec("PRAGMA cache_size=1; PRAGMA cache_spill=ON"); err != nil {
			t.Fatal(err)
		}
		tx, err := db.Begin()
		if err != nil {
			t.Fatal(err)
		}
		if _, err := tx.Exec("UPDATE records SET text=?", strings.Repeat("uncommitted", 4096)); err != nil {
			t.Fatal(err)
		}
		// 不运行 defer、Rollback 或 Close，模拟宿主进程在事务中直接退出。
		os.Exit(79)
	}
	dir := newAuditDirectory(t)
	c := openAudit(t, dir, 0)
	id := monitorScope(t, c, "hot-journal-scope")
	c.Close()
	records := make([]audit.Record, 0, 16)
	for n := 0; n < 16; n++ {
		records = append(records, fixtureRecord(fmt.Sprintf("hot-%d", n), id, time.Now().UTC(), strings.Repeat("original", 8192)))
	}
	insertFixtureRecords(t, dir, records...)
	// 正式打开一次，保证恢复前就有足够的已计费日志空间。
	c = openAudit(t, dir, 0)
	if c.Status().State != "active" {
		t.Fatalf("hot journal 初始库不可用：%+v", c.Status())
	}
	c.Close()
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	command := exec.Command(executable, "-test.run=^TestSQLiteHotJournalRecovery$")
	command.Env = append(os.Environ(), "CPA_AUDIT_UNIT_HOT_JOURNAL="+dir)
	output, err := command.CombinedOutput()
	if exit, ok := err.(*exec.ExitError); !ok || exit.ExitCode() != 79 {
		t.Fatalf("hot journal 子进程未在事务中退出：err=%v output=%s", err, output)
	}
	journal := readAuditFile(t, dir, "records.sqlite-journal")
	if len(journal) < 8 || !bytes.Equal(journal[:8], []byte{0xd9, 0xd5, 0x05, 0xf9, 0x20, 0xa1, 0x63, 0xd7}) {
		t.Fatal("夹具没有生成真实 hot journal")
	}
	c = openAudit(t, dir, 0)
	if status := c.Status(); status.State != "active" || status.Saved != int64(len(records)) {
		t.Fatalf("hot journal 恢复后不可用：%+v", status)
	}
	for _, want := range records {
		got, err := c.Detail(want.ID)
		if err != nil || !reflect.DeepEqual(got, want) {
			t.Fatalf("hot journal 未恢复已提交记录：id=%s err=%v", want.ID, err)
		}
	}
}
