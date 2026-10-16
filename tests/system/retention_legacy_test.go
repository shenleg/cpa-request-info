//go:build legacy_patched

// 本文件保留原读体前保留期快照；当前 Before 时间与保留期分别见 TestOfficialKnownCallerDirectory 和 TestRetentionNeverExtendsHistory。
package system

import (
	"bytes"
	"fmt"
	"net/url"
	"testing"
	"time"
)

// TestLegacyRetentionNeverExtendsHistory 验证调长不延长旧提交，慢体从接收计时，缩短再调长及重启不能复活历史。
func TestLegacyRetentionNeverExtendsHistory(t *testing.T) {
	received := time.Date(2030, 4, 1, 0, 0, 0, 0, time.UTC)
	clock := newRetentionClock(t, received)
	upstream, _ := snapshotUpstream()
	defer upstream.Close()
	h := startRetentionHost(t, 1<<30, upstream.URL, clock)
	h.monitorAll(t)
	marker := fmt.Sprintf("RETENTION-LIFETIME-%d", time.Now().UnixNano())
	old := searchSubmit(t, h, keyA, "audit-test-model", marker+"-OLD")
	complete := beginSnapshotSubmission(t, h, snapshotInput(t, marker+"-SLOW"))
	retentionClock(t, clock, received.Add(time.Hour), 0)
	snapshotMutation(t, h, "/settings", map[string]int{"retention_days": 30})
	retentionClock(t, clock, received.Add(6*24*time.Hour), 0)
	later := searchSubmit(t, h, keyA, "audit-test-model", marker+"-LATER")
	retentionClock(t, clock, received.Add(7*24*time.Hour), 0)
	code, _ := h.request(t, "GET", api+"/detail?id="+old.ID, adminKey, nil)
	if code != 404 {
		t.Fatal("调长延长了已有历史的七天寿命")
	}
	complete()
	// 等待本轮慢体确已持久化，再判断不可查，防止把尚未保存误认为已到期。
	waitRetentionOutput(t, h, func(raw []byte) bool { return bytes.Contains(raw, []byte(marker+"-SLOW")) })
	if page := searchRecords(t, h, url.Values{"q": {marker + "-SLOW"}}); page.Total != 0 {
		t.Fatal("慢体从完成或落盘重新取得保留期限")
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
