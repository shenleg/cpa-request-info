//go:build audittest

// 本文件使用已有文件时钟验证思考元数据及模型目录的逻辑到期，不依赖后台物理清理。
package audit_test

import (
	"bytes"
	"testing"
	"time"

	"github.com/shenleg/cpa-request-info/internal/audit"
)

// TestModelsAndReasoningLogicalExpiry 验证模型只从未到期记录汇总，相同模型仍有新记录时保留，未清理正文在重启后也不可恢复。
func TestModelsAndReasoningLogicalExpiry(t *testing.T) {
	clockDir, dir := newAuditDirectory(t), newAuditDirectory(t)
	start := time.Date(2032, 5, 6, 7, 8, 9, 0, time.UTC)
	setCaptureClock(t, clockDir, "models-start.json", start)
	c := openAudit(t, dir, 0)
	monitorScope(t, c, "models-expiry-scope")
	c.CaptureExecution("old-unique-model", "models-expiry-scope", "responses", audit.PurposeUnknown, []byte(`{"model":"expired-only","input":"old unique","reasoning":{"effort":"high"}}`))
	c.CaptureExecution("old-shared-model", "models-expiry-scope", "responses", audit.PurposeUnknown, []byte(`{"model":"shared","input":"old shared"}`))
	waitSaved(t, c, 2)
	setCaptureClock(t, clockDir, "models-new.json", start.Add(time.Hour))
	c.CaptureExecution("new-shared-model", "models-expiry-scope", "anthropic-messages", audit.PurposeUnknown, []byte(`{"model":"shared","messages":[{"role":"user","content":"new shared"}],"output_config":{"effort":"medium"},"thinking":{"type":"enabled","budget_tokens":0}}`))
	c.CaptureExecution("new-unique-model", "models-expiry-scope", "responses", audit.PurposeUnknown, []byte(`{"model":"new-only","input":"new unique"}`))
	waitSaved(t, c, 4)
	original := readAuditFile(t, dir, "records.sqlite")
	setCaptureClock(t, clockDir, "models-before-expiry.json", start.Add(7*24*time.Hour-time.Nanosecond))
	assertModels(t, c, []string{"expired-only", "new-only", "shared"})
	if _, _, err := c.List(0, 10, audit.Query{}); err != nil {
		t.Fatal(err)
	}
	setCaptureClock(t, clockDir, "models-expiry.json", start.Add(7*24*time.Hour))
	assertModels(t, c, []string{"new-only", "shared"})
	if _, err := c.Detail("old-unique-model"); err == nil || err.Error() != "not_found" {
		t.Fatalf("到期的思考记录仍可读：%v", err)
	}
	if !bytes.Equal(original, readAuditFile(t, dir, "records.sqlite")) {
		t.Fatal("只推进查询时钟却触发物理清理，未验证未清理边界")
	}
	zero := int64(0)
	want := &audit.RequestReasoning{Effort: "medium", Type: "enabled", BudgetTokens: &zero}
	detail, err := c.Detail("new-shared-model")
	if err != nil {
		t.Fatal(err)
	}
	assertRequestReasoning(t, detail.RequestReasoning, want)
	c.Close()
	c = openAudit(t, dir, 0)
	assertModels(t, c, []string{"new-only", "shared"})
	detail, err = c.Detail("new-shared-model")
	if err != nil {
		t.Fatal(err)
	}
	assertRequestReasoning(t, detail.RequestReasoning, want)
	setCaptureClock(t, clockDir, "models-all-expired.json", start.Add(time.Hour+7*24*time.Hour))
	assertModels(t, c, []string{})
	c.Close()
	c = openAudit(t, dir, 0)
	assertModels(t, c, []string{})
}
