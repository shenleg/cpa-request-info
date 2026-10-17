// 本文件验证用途新增字符串的队列预算；只使用内存采集器，不启动写入者或操作数据目录。
package audit

import (
	"strings"
	"testing"
)

// TestRequestPurposeQueueBytes 验证用途计入完整副本，恰好达到字节上限可接受、超过一字节则拒绝。
func TestRequestPurposeQueueBytes(t *testing.T) {
	for _, tc := range []struct {
		purpose RequestPurpose
		bytes   int64
	}{
		// 固定夹具有99字节字符串载荷和184字节结构/思考对象开销；用途另加4或7字节。
		{PurposeMain, 287},
		{PurposeTurn, 287},
		{PurposeUnknown, 290},
	} {
		t.Run(string(tc.purpose), func(t *testing.T) {
			c := &Collector{
				generation:  "queue-budget",
				queue:       make(chan queuedRecord, 1),
				queuedBytes: maxQueuedBytes - tc.bytes,
				gapEntries:  make(map[string]gapEntry),
				status:      Status{State: "active", Gaps: map[string]uint64{}, GapHistory: map[string]string{}},
				config:      configuration{RetentionDays: defaultRetentionDays},
			}
			event := Event{
				ID:         "r",
				ObjectID:   strings.Repeat("a", 64),
				ReceivedAt: "2026-10-17T00:00:00Z",
				Protocol:   "responses",
				Purpose:    tc.purpose,
				Body:       []byte(`{"model":"m","input":"text"}`),
				Snapshot:   Snapshot{Version: 3, Decision: "capture", MaxTextBytes: maxText, CollectorGeneration: "queue-budget"},
			}
			c.Observe(event)
			select {
			case queued := <-c.queue:
				if queued.bytes != tc.bytes || c.queuedBytes != maxQueuedBytes || c.Status().Queued != 1 {
					t.Fatalf("用途未计入排队副本：record=%d total=%d want=%d", queued.bytes, c.queuedBytes, tc.bytes)
				}
				if queued.record.Purpose != tc.purpose {
					t.Fatalf("用途未进入白名单副本：%q", queued.record.Purpose)
				}
			default:
				t.Fatal("恰好达到字节预算的记录未接受")
			}
			c.queuedBytes = maxQueuedBytes - tc.bytes + 1
			c.Observe(event)
			if len(c.queue) != 0 || c.Status().Gaps["queue_full"] != 1 {
				t.Fatal("用途使副本超出一字节时未拒绝")
			}
		})
	}
}
