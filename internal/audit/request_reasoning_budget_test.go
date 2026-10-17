// 本文件验证思考等级及模式字符串计入队列字节预算，仅使用内存采集器，不创建或删除文件。
package audit

import (
	"strings"
	"testing"
)

// reasoningQueueCollector 创建不启动写入者的活动采集器；返回独立队列夹具，无磁盘操作或错误。
func reasoningQueueCollector() *Collector {
	return &Collector{
		generation: "reasoning-queue-budget",
		queue:      make(chan queuedRecord, 1),
		gapEntries: make(map[string]gapEntry),
		status:     Status{State: "active", Gaps: map[string]uint64{}, GapHistory: map[string]string{}},
		config:     configuration{RetentionDays: defaultRetentionDays},
	}
}

// TestRequestReasoningQueueBytes 验证 effort/type 动态字节增加相应预算，恰好达到上限可接受，超出一字节则拒绝。
func TestRequestReasoningQueueBytes(t *testing.T) {
	event := Event{
		ID: "reasoning-queue-record", ObjectID: strings.Repeat("a", 64),
		ReceivedAt: "2026-10-17T00:00:00Z", Protocol: "anthropic-messages", Purpose: PurposeMain,
		Body:     []byte(`{"model":"m","messages":[{"role":"user","content":"text"}]}`),
		Snapshot: Snapshot{Version: 3, Decision: "capture", MaxTextBytes: maxText, CollectorGeneration: "reasoning-queue-budget"},
	}
	baseline := reasoningQueueCollector()
	baseline.Observe(event)
	var baseBytes int64
	select {
	case queued := <-baseline.queue:
		baseBytes = queued.bytes
	default:
		t.Fatal("基础思考空对象未接受")
	}
	for _, tc := range []struct{ name, fields, effort, mode string }{
		{"effort", `"output_config":{"effort":"high"}`, "high", ""},
		{"type", `"thinking":{"type":"adaptive"}`, "", "adaptive"},
		{"all_zero_budget", `"output_config":{"effort":"high"},"thinking":{"type":"enabled","budget_tokens":0}`, "high", "enabled"},
		{"all_budget", `"output_config":{"effort":"medium"},"thinking":{"type":"enabled","budget_tokens":4096}`, "medium", "enabled"},
		{"utf8", `"output_config":{"effort":"中"},"thinking":{"type":"模式"}`, "中", "模式"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			input := event
			input.Body = []byte(`{"model":"m","messages":[{"role":"user","content":"text"}],` + tc.fields + `}`)
			wantBytes := baseBytes + int64(len(tc.effort)+len(tc.mode))
			c := reasoningQueueCollector()
			c.queuedBytes = maxQueuedBytes - wantBytes
			c.Observe(input)
			select {
			case queued := <-c.queue:
				if queued.bytes != wantBytes || c.queuedBytes != maxQueuedBytes || queued.record.RequestReasoning.Effort != tc.effort || queued.record.RequestReasoning.Type != tc.mode {
					t.Fatalf("动态思考字符串未按字节计费：record=%d want=%d total=%d", queued.bytes, wantBytes, c.queuedBytes)
				}
			default:
				t.Fatal("恰好达到预算的思考记录被拒绝")
			}
			c = reasoningQueueCollector()
			c.queuedBytes = maxQueuedBytes - wantBytes + 1
			c.Observe(input)
			if len(c.queue) != 0 || c.Status().Gaps["queue_full"] != 1 {
				t.Fatal("动态思考字符串超出一字节时未拒绝")
			}
		})
	}
}
