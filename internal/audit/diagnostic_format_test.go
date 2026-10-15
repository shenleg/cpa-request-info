// 本文件只验证固定诊断格式的编码上界，不通过私有结构推断业务行为。
package audit

import (
	"encoding/json"
	"math"
	"testing"
)

// TestDiagnosticEncodingBound 验证全部固定原因与最长数字同时出现时仍可编码进原有4KiB文件。
func TestDiagnosticEncodingBound(t *testing.T) {
	entries := make(map[string]gapEntry, len(gapReasons))
	for _, reason := range gapReasons {
		entries[reason] = gapEntry{First: math.MaxInt64, Last: math.MaxInt64, Count: math.MaxUint64, Revision: math.MaxUint64, Flags: gapUnknown | gapActive}
	}
	summary := diagnosticSummary{Version: 3, CapacityGap: true, LastAt: "9999-12-31T23:59:59.999999999Z", SessionDirty: true, Recovery: Recovery{Uncertain: true, DetectedAt: "9999-12-31T23:59:59.999999999Z", Reason: "observer_unavailable"}, Alerts: &gapDisk{Entries: entries, Revision: math.MaxUint64, Acknowledged: math.MaxUint64}}
	encoded, err := json.Marshal(summary)
	if err != nil || len(encoded) > int(diagnosticSize) {
		t.Fatalf("最坏诊断编码超过固定分配：bytes=%d err=%v", len(encoded), err)
	}
	t.Logf("全部%d个固定原因的最坏诊断编码：%d / %d字节", len(entries), len(encoded), diagnosticSize)
}
