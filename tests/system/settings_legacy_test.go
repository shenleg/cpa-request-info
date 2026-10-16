//go:build legacy_patched

// 本文件保留读体前冻结上限的旧契约；当前 Before 上限时点由 TestOfficialBeforeUsesCurrentConfiguration 验证。
package system

import (
	"encoding/json"
	"fmt"
	"net/http"
	"testing"
	"time"
)

// TestSettingsSnapshotKeepsAcceptedLimit 验证慢体提交冻结旧上限，后续提交使用新上限且上游收到完整文本。
func TestSettingsSnapshotKeepsAcceptedLimit(t *testing.T) {
	upstream, received := snapshotUpstream()
	defer upstream.Close()
	h := startHost(t, 1<<30, true, upstream.URL)
	h.monitorAll(t)
	for round := 1; round <= 2; round++ {
		snapshotMutation(t, h, "/settings", map[string]int{"max_text_bytes": 128})
		marker := fmt.Sprintf("SETTINGS-SNAPSHOT-%d-%d", round, time.Now().UnixNano())
		complete := beginSnapshotSubmission(t, h, snapshotInput(t, marker+"-BEFORE"))
		snapshotMutation(t, h, "/settings", map[string]int{"max_text_bytes": 7})
		complete()
		before := waitSnapshotRecord(t, h, marker+"-BEFORE")
		if before.Truncated {
			t.Fatal("配置变更追溯截断已接收副本")
		}
		code, _ := h.request(t, "POST", "/v1/chat/completions", keyA, snapshotInput(t, marker+"-AFTER"))
		if code != http.StatusOK {
			t.Fatal("新上限改变实际模型调用")
		}
		verifySnapshotUpstream(t, received, marker+"-BEFORE", marker+"-AFTER")
		list := h.waitRecords(t, round*2)
		code, raw := h.request(t, "GET", api+"/detail?id="+list[0].ID, adminKey, nil)
		var detail savedRecord
		if code != http.StatusOK || json.Unmarshal(raw, &detail) != nil || detail.Text != "SETTING" || !detail.Truncated {
			t.Fatal("下一提交未采用7字节上限")
		}
	}
}
