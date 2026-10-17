// 本文件验证外部短暂只读快照不会令异步 SQLite 写入者永久停采。
package audit_test

import (
	"database/sql"
	"net/url"
	"path/filepath"
	"testing"
	"time"

	"github.com/shenleg/cpa-request-info/internal/audit"
)

// TestSQLiteShortReaderDoesNotPauseCapture 验证短暂共享读锁只延迟后台提交，不变成永久 storage_error。
func TestSQLiteShortReaderDoesNotPauseCapture(t *testing.T) {
	dir := newAuditDirectory(t)
	collector := openAudit(t, dir, 0)
	monitorScope(t, collector, "sqlite-reader-scope")
	body := []byte(`{"model":"synthetic","input":"reader-contention"}`)
	collector.CaptureExecution("reader-before", "sqlite-reader-scope", "responses", audit.PurposeUnknown, body)
	waitSaved(t, collector, 1)

	u := url.URL{Scheme: "file", Path: filepath.Join(dir, "records.sqlite")}
	u.RawQuery = "mode=ro"
	reader, err := sql.Open("sqlite3", u.String())
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	tx, err := reader.Begin()
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	var count int
	if err := tx.QueryRow("SELECT count(*) FROM records").Scan(&count); err != nil || count != 1 {
		t.Fatalf("只读对照事务未取得已保存记录：count=%d err=%v", count, err)
	}
	collector.CaptureExecution("reader-after", "sqlite-reader-scope", "responses", audit.PurposeUnknown, body)
	// 明确保留共享锁250ms，覆盖后台写入；无需等待或阻塞模型调用路径。
	released := make(chan error, 1)
	timer := time.AfterFunc(250*time.Millisecond, func() { released <- tx.Rollback() })
	defer timer.Stop()
	waitSaved(t, collector, 2)
	if err := <-released; err != nil {
		t.Fatal(err)
	}
	if state := collector.Status(); state.State != "active" || state.Gaps["storage_error"] != 0 {
		t.Fatalf("短暂只读锁使正常采集停止：%+v", state)
	}
}
