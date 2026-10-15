// 本文件在审计目录无法打开时保留有界运行期故障；可写后交给同一诊断存储，不创建旁路文件。
package native

import (
	"crypto/rand"
	"time"

	"github.com/shenleg/cpa-request-info/internal/audit"
)

// pendingFaultLocked 只接收宿主适配产生的固定原因，重复状态查询不会重新提醒。
func (a *App) pendingFaultLocked(reason string) {
	if a.pendingAlerts.Generation == "" {
		a.pendingAlerts.Generation = rand.Text()
		a.pendingAlerts.Items = []audit.GapSummary{}
	}
	a.pendingAlerts.RetentionUncertain = true
	now := time.Now().UTC()
	items := a.pendingAlerts.Items[:0]
	for _, item := range a.pendingAlerts.Items {
		last, _ := time.Parse(time.RFC3339Nano, item.LastObservedAt)
		// 配置尚不可读取时只能采用允许的最长30天；恢复后再按实际策略过滤，不能假定七天。
		if !now.Before(last.Add(30 * 24 * time.Hour)) {
			if !item.Active {
				continue
			}
			item.FirstObservedAt, item.LastObservedAt, item.HistoryRetained = nil, "", false
		}
		items = append(items, item)
	}
	a.pendingAlerts.Items = items
	if a.pendingReason == reason {
		return
	}
	for i := range a.pendingAlerts.Items {
		a.pendingAlerts.Items[i].Active = false
	}
	a.pendingReason = reason
	if reason == "" {
		return
	}
	at := now.Format(time.RFC3339Nano)
	a.pendingAlerts.Revision++
	a.pendingAlerts.Unread = true
	for i := range a.pendingAlerts.Items {
		item := &a.pendingAlerts.Items[i]
		if item.Reason == reason {
			if item.FirstObservedAt == nil {
				item.FirstObservedAt = &at
			}
			item.LastObservedAt, item.Active, item.HistoryRetained, item.Unread = at, true, true, true
			return
		}
	}
	a.pendingAlerts.Items = append(a.pendingAlerts.Items, audit.GapSummary{Reason: reason, FirstObservedAt: &at, LastObservedAt: at, CountAccuracy: "unknown", CountUnit: "unknown", Active: true, HistoryRetained: true, Unread: true})
}

// pendingBridgeLocked 将没有采集器期间的宿主累计转换成有界增量；发现时刻不冒充损失时段。
func (a *App) pendingBridgeLocked(total uint64) {
	if total <= a.pendingBridgeFailures {
		return
	}
	a.pendingBridgeFailures = total
	at := time.Now().UTC().Format(time.RFC3339Nano)
	a.pendingAlerts.Revision++
	a.pendingAlerts.Unread = true
	for i := range a.pendingAlerts.Items {
		item := &a.pendingAlerts.Items[i]
		if item.Reason != "observer_unavailable" {
			continue
		}
		// 尚不能读取实际保留策略，不能把全运行期累计当作保留期内精确次数。
		item.AffectedCount, item.CountAccuracy, item.LastObservedAt, item.Unread = nil, "unknown", at, true
		return
	}
	a.pendingAlerts.Items = append(a.pendingAlerts.Items, audit.GapSummary{Reason: "observer_unavailable", FirstObservedAt: &at, LastObservedAt: at, AffectedCount: nil, CountAccuracy: "unknown", CountUnit: "bridge_failures", HistoryRetained: true, Unread: true})
}

// importPendingLocked 仅在成功取得采集器时移交运行证据；确认不能让不可写期间变成完整历史。
func (a *App) importPendingLocked() {
	for _, item := range a.pendingAlerts.Items {
		if !item.HistoryRetained {
			continue
		}
		first := item.LastObservedAt
		if item.FirstObservedAt != nil {
			first = *item.FirstObservedAt
		}
		a.collector.ReportPastFault(item.Reason, first, item.LastObservedAt, !item.Unread)
	}
	a.collector.RestoreBridgeBaseline(a.pendingBridgeFailures)
	a.pendingAlerts = audit.GapAlerts{}
	a.pendingReason = ""
}
