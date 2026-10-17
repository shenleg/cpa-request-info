// 本文件验证模型目录来自全部已保存记录，独立于对象、分页、筛选与重启；不自动删除测试文件。
package audit_test

import (
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/shenleg/cpa-request-info/internal/audit"
)

// assertModels 核对公开模型目录的稳定顺序与精确去重；接口失败或结果不符终止测试，无返回值。
func assertModels(t *testing.T, c *audit.Collector, want []string) {
	t.Helper()
	got, err := c.Models()
	if err != nil || !reflect.DeepEqual(got, want) {
		t.Fatalf("模型目录不符：got=%q want=%q err=%v", got, want, err)
	}
}

// TestModelsEmptyAndClosed 验证空库返回非 nil 空数组，关闭后明确返回错误。
func TestModelsEmptyAndClosed(t *testing.T) {
	c := openAudit(t, newAuditDirectory(t), 0)
	assertModels(t, c, []string{})
	c.Close()
	if _, err := c.Models(); err == nil {
		t.Fatal("关闭后仍返回成功模型目录")
	}
}

// TestModelsAcrossPagesAndRestart 验证模型跨页和跨对象排序去重，保留大小写差异且忽略空模型，筛选不改变目录。
func TestModelsAcrossPagesAndRestart(t *testing.T) {
	dir := newAuditDirectory(t)
	c := openAudit(t, dir, 0)
	first := monitorScope(t, c, "models-first-scope")
	second := monitorScope(t, c, "models-second-scope")
	c.Close()
	// 各批独有模型均在较早记录，第一页看不到它们；所有夹具都是真实提交的 SQLite 行。
	models := [][]string{{"segment-one-only", "zeta", "Alpha", ""}, {"segment-two-only", "alpha", "zeta", "Alpha", "latest"}}
	index := 0
	for _, names := range models {
		for _, model := range names {
			objectID := first
			if index%2 != 0 {
				objectID = second
			}
			record := fixtureRecord(fmt.Sprintf("model-record-%d", index), objectID, time.Now().UTC(), "model catalog text")
			record.Model = model
			insertFixtureRecords(t, dir, record)
			index++
		}
	}
	want := []string{"Alpha", "alpha", "latest", "segment-one-only", "segment-two-only", "zeta"}
	c = openAudit(t, dir, 0)
	if status := c.Status(); status.State != "active" || status.Saved != int64(index) {
		t.Fatalf("多段夹具不可读：%+v", status)
	}
	assertModels(t, c, want)
	for offset := 0; offset < index; offset++ {
		page, total, err := c.List(offset, 1, audit.Query{})
		if err != nil || total != index || len(page) != 1 {
			t.Fatalf("分页失败：offset=%d total=%d err=%v", offset, total, err)
		}
		assertModels(t, c, want)
		detail, err := c.Detail(page[0].ID)
		if err != nil || detail.ID != page[0].ID || detail.Model != page[0].Model {
			t.Fatalf("模型目录影响当前页详情：%+v err=%v", detail, err)
		}
	}
	for _, query := range []audit.Query{{ObjectID: first}, {Model: "alpha"}, {Keyword: "no such text"}} {
		if _, _, err := c.List(0, 1, query); err != nil {
			t.Fatal(err)
		}
		assertModels(t, c, want)
	}
	body := []byte(`{"model":"new-synchronously-saved","input":"new model text"}`)
	c.CaptureExecution("new-model", "models-second-scope", "responses", audit.PurposeUnknown, body)
	waitSaved(t, c, int64(index+1))
	want = []string{"Alpha", "alpha", "latest", "new-synchronously-saved", "segment-one-only", "segment-two-only", "zeta"}
	assertModels(t, c, want)
	c.Close()
	c = openAudit(t, dir, 0)
	assertModels(t, c, want)
	if _, err := c.Detail("new-model"); err != nil {
		t.Fatal(err)
	}
	// 未选择的第三个对象只用于发现，不得进入模型目录。
	c.CaptureExecution("unmonitored-model", "models-unmonitored-scope", "responses", audit.PurposeUnknown, []byte(`{"model":"`+strings.Repeat("x", 16)+`","input":"not saved"}`))
	assertModels(t, c, want)
}
