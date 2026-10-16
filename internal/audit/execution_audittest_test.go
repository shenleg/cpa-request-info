//go:build audittest

// 本文件通过既有audittest文件时钟验证CaptureExecution的时间归属与保留期，不复制宿主时钟实现。
package audit_test

import (
	"encoding/json"
	"path/filepath"
	"testing"
	"time"

	"github.com/shenleg/cpa-request-info/internal/audit"
)

// setCaptureClock 将now写入新夹具并指定既有audit时钟；返回值为空，编码或写入失败终止测试，保留各时刻文件。
func setCaptureClock(t *testing.T, dir, name string, now time.Time) {
	t.Helper()
	data, err := json.Marshal(map[string]any{"now": now.Format(time.RFC3339Nano), "sweep": 0})
	if err != nil {
		t.Fatal(err)
	}
	writeAuditFile(t, dir, name, data)
	t.Setenv("CPA_AUDIT_TEST_CLOCK", filepath.Join(dir, name))
}

// TestCaptureExecutionRetentionClock 验证接收时间取自auditNow，调长不延长旧记录，到期查询不返回正文。
func TestCaptureExecutionRetentionClock(t *testing.T) {
	clockDir := newAuditDirectory(t)
	start := time.Date(2031, 3, 4, 5, 6, 7, 123456789, time.UTC)
	setCaptureClock(t, clockDir, "start.json", start)
	c := openAudit(t, newAuditDirectory(t), 0)
	scope := "retention-scope"
	monitorScope(t, c, scope)
	c.CaptureExecution("before-extension", scope, "responses", []byte(`{"input":"old"}`))
	waitSaved(t, c, 1)
	record, err := c.Detail("before-extension")
	if err != nil || record.ReceivedAt != start.Format(time.RFC3339Nano) {
		t.Fatalf("Capture未使用auditNow：%+v err=%v", record, err)
	}
	setCaptureClock(t, clockDir, "extension.json", start.Add(time.Hour))
	days := 30
	if err := c.SetSettings(audit.SettingsChange{RetentionDays: &days}); err != nil {
		t.Fatal(err)
	}
	c.CaptureExecution("after-extension", scope, "responses", []byte(`{"input":"new"}`))
	waitSaved(t, c, 2)
	setCaptureClock(t, clockDir, "before-expiry.json", start.Add(7*24*time.Hour-time.Nanosecond))
	if _, err := c.Detail("before-extension"); err != nil {
		t.Fatalf("旧记录提前到期：%v", err)
	}
	setCaptureClock(t, clockDir, "expiry.json", start.Add(7*24*time.Hour))
	if _, err := c.Detail("before-extension"); err == nil || err.Error() != "not_found" {
		t.Fatalf("调长使旧记录在七天后仍可读：%v", err)
	}
	records, total, err := c.List(0, 10, audit.Query{})
	if err != nil || total != 1 || len(records) != 1 || records[0].ID != "after-extension" {
		t.Fatalf("Capture时刻未决定期限归属：%+v total=%d err=%v", records, total, err)
	}
	setCaptureClock(t, clockDir, "new-expiry.json", start.Add(time.Hour+30*24*time.Hour))
	if records, total, err := c.List(0, 10, audit.Query{}); err != nil || total != 0 || len(records) != 0 {
		t.Fatalf("新记录没有按三十天到期：%+v total=%d err=%v", records, total, err)
	}
}
