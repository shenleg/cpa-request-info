// 本文件维护固定原因的有界缺口摘要；历史、当前故障和已阅版本独立，不复制身份或正文。
package audit

import (
	"encoding/json"
	"errors"
	"slices"
	"sort"
	"time"
)

// GapSummary 是单个原因的保守历史；次数单位不等同于独立丢失请求总数，观察时刻也不是损失边界。
type GapSummary struct {
	Reason          string  `json:"reason"`
	FirstObservedAt *string `json:"first_observed_at"`
	LastObservedAt  string  `json:"last_observed_at"`
	AffectedCount   *uint64 `json:"affected_count"`
	CountAccuracy   string  `json:"count_accuracy"`
	CountUnit       string  `json:"count_unit"`
	LostFrom        *string `json:"lost_from"`
	LostUntil       *string `json:"lost_until"`
	Active          bool    `json:"active"`
	HistoryRetained bool    `json:"history_retained"`
	Unread          bool    `json:"unread"`
}

// GapAlerts 是已展示历史的确认快照；Generation 防止重启后的旧页面确认另一运行会话。
type GapAlerts struct {
	Generation           string       `json:"generation"`
	Revision             uint64       `json:"revision"`
	AcknowledgedRevision uint64       `json:"acknowledged_revision"`
	Unread               bool         `json:"unread"`
	Items                []GapSummary `json:"items"`
	RetentionUncertain   bool         `json:"retention_uncertain,omitempty"`
}

const (
	gapUnknown = 1 << iota
	gapActive
)

// 短磁盘字段和固定原因数使所有摘要、最大计数及版本号同时存在也能放入原有4KiB诊断。
type gapEntry struct {
	First    int64  `json:"f"`
	Last     int64  `json:"l"`
	Count    uint64 `json:"n"`
	Revision uint64 `json:"r"`
	Flags    uint8  `json:"u"`
}

type gapDisk struct {
	Entries      map[string]gapEntry `json:"e"`
	Revision     uint64              `json:"r"`
	Acknowledged uint64              `json:"a"`
}

var gapReasons = []string{"read_error", "invalid_event", "invalid_json", "unsupported_protocol", "unsupported_content", "metadata_too_large", "queue_full", "storage_error", "budget_exhausted", "observer_unavailable", "observer_extension_missing", "observer_version_incompatible", "observer_bridge_unavailable", "configuration_invalid", "storage_unavailable", "unclean_shutdown", "legacy_unknown", "recovery_unavailable", "identity_unavailable"}

// validateGapDisk 在持久边界拒绝未知原因、歧义字段及不成立的版本/时间关系。
func validateGapDisk(raw []byte, disk *gapDisk) error {
	fields, err := protocolObject(raw)
	if err != nil || len(fields) != 3 || disk == nil || disk.Entries == nil || len(disk.Entries) > len(gapReasons) || disk.Acknowledged > disk.Revision {
		return errors.New("storage_error")
	}
	for _, key := range []string{"e", "r", "a"} {
		if len(fields[key]) == 0 || string(fields[key]) == "null" {
			return errors.New("storage_error")
		}
	}
	entries, err := protocolObject(fields["e"])
	if err != nil {
		return errors.New("storage_error")
	}
	for reason, entry := range disk.Entries {
		values, err := protocolObject(entries[reason])
		if err != nil || len(values) != 5 || !slices.Contains(gapReasons, reason) || entry.Revision > disk.Revision || entry.First < 0 || entry.Last < entry.First || entry.Flags > gapUnknown|gapActive || (entry.Last == 0 && entry.Flags&gapActive == 0) {
			return errors.New("storage_error")
		}
		for _, key := range []string{"f", "l", "n", "r", "u"} {
			if len(values[key]) == 0 || string(values[key]) == "null" || !json.Valid(values[key]) {
				return errors.New("storage_error")
			}
		}
	}
	return nil
}

func gapTime(n int64) string { return time.Unix(0, n).UTC().Format(time.RFC3339Nano) }

// pruneGaps 在持锁状态下移除到期证据；跨到期边界的聚合丢弃精确次数，而不是让旧次数随新事件续期。
func (c *Collector) pruneGaps(now time.Time) bool {
	changed := false
	for reason, entry := range c.gapEntries {
		if entry.Last == 0 {
			if entry.Flags&gapActive == 0 {
				delete(c.gapEntries, reason)
				changed = true
			}
			continue
		}
		if c.expired(Record{ReceivedAt: gapTime(entry.Last)}, now) {
			if entry.Flags&gapActive != 0 {
				entry.First, entry.Last, entry.Count = 0, 0, 0
				entry.Flags |= gapUnknown
				c.gapEntries[reason] = entry
			} else {
				delete(c.gapEntries, reason)
			}
			changed = true
		} else if entry.First != 0 && c.expired(Record{ReceivedAt: gapTime(entry.First)}, now) {
			entry.First, entry.Count = 0, 0
			entry.Flags |= gapUnknown
			c.gapEntries[reason] = entry
			changed = true
		}
	}
	return changed
}

func (c *Collector) diagnosticsChanged() {
	c.diagnosticRevision++
	c.status.DiagnosticsPersisted = false
	select {
	case c.diagnosticWake <- struct{}{}:
	default:
	}
}

// beginFault 首次启动核验同一持续故障时保留已阅；恢复后再次出现才创建新提醒。调用者持有mu。
func (c *Collector) beginFault(reason string) {
	if previous, continuous := c.previousActive[reason]; continuous {
		if _, exists := c.gapEntries[reason]; !exists {
			c.gapEntries[reason] = previous
		}
		delete(c.previousActive, reason)
		c.diagnosticsChanged()
	} else {
		c.recordGap(reason, 0, false, auditNow())
	}
	entry := c.gapEntries[reason]
	entry.Flags |= gapActive
	c.gapEntries[reason] = entry
}

// recordGap 只在真实新事件上推进提醒；调用者持有mu，count是该原因可确定的受影响次数。
func (c *Collector) recordGap(reason string, count uint64, exact bool, at time.Time) {
	c.pruneGaps(auditNow())
	entry := c.gapEntries[reason]
	if entry.Last == 0 {
		entry.First = at.UnixNano()
		entry.Flags &= gapActive
	}
	entry.Last = at.UnixNano()
	if !exact {
		entry.Flags |= gapUnknown
	}
	if count > ^uint64(0)-entry.Count {
		entry.Count = ^uint64(0)
		entry.Flags |= gapUnknown
	} else {
		entry.Count += count
	}
	// 饱和后永远保持未阅，不能因整数回绕把新缺口当作旧版本。
	if c.alertRevision != ^uint64(0) {
		c.alertRevision++
	}
	entry.Revision = c.alertRevision
	c.gapEntries[reason] = entry
	c.diagnosticsChanged()
}

func (c *Collector) gapAlerts() GapAlerts {
	if c.pruneGaps(auditNow()) {
		c.diagnosticsChanged()
	}
	result := GapAlerts{Generation: c.generation, Revision: c.alertRevision, AcknowledgedRevision: c.acknowledgedRevision, Items: []GapSummary{}}
	for reason, entry := range c.gapEntries {
		item := GapSummary{Reason: reason, CountAccuracy: "unknown", CountUnit: "submissions", Active: entry.Flags&gapActive != 0, HistoryRetained: entry.Last != 0, Unread: entry.Revision > c.acknowledgedRevision || entry.Revision == ^uint64(0)}
		if entry.Last != 0 {
			item.LastObservedAt = gapTime(entry.Last)
		}
		if entry.First != 0 {
			first := gapTime(entry.First)
			item.FirstObservedAt = &first
		}
		if entry.Flags&gapUnknown == 0 || entry.Count > 0 {
			count := entry.Count
			item.AffectedCount = &count
			item.CountAccuracy = "exact"
			if entry.Flags&gapUnknown != 0 {
				item.CountAccuracy = "at_least"
			}
		}
		switch reason {
		case "unsupported_content":
			item.CountUnit = "incomplete_submissions"
		case "observer_unavailable":
			item.CountUnit = "bridge_failures"
		case "observer_extension_missing", "observer_version_incompatible", "observer_bridge_unavailable", "configuration_invalid", "storage_unavailable", "unclean_shutdown", "legacy_unknown", "recovery_unavailable":
			item.CountUnit = "unknown"
		}
		result.Unread = result.Unread || item.Unread
		result.Items = append(result.Items, item)
	}
	sort.Slice(result.Items, func(i, j int) bool { return result.Items[i].Reason < result.Items[j].Reason })
	return result
}

// AcknowledgeGaps 确认管理员已看到的同会话提醒版本；返回确认是否已同步落盘。
// 版本已变或会话不匹配返回gap_snapshot_changed；I/O失败返回diagnostics_unavailable并保留运行期已阅状态。
func (c *Collector) AcknowledgeGaps(generation string, revision uint64) (bool, error) {
	c.ioMu.Lock()
	defer c.ioMu.Unlock()
	c.mu.Lock()
	if generation != c.generation || revision != c.alertRevision {
		c.mu.Unlock()
		return false, errors.New("gap_snapshot_changed")
	}
	if c.acknowledgedRevision != revision {
		c.acknowledgedRevision = revision
		c.diagnosticsChanged()
	}
	c.mu.Unlock()
	if c.diagnostics == nil {
		return false, errors.New("diagnostics_unavailable")
	}
	if err := c.saveDiagnostics(); err != nil {
		c.pauseStorage(err)
		return false, errors.New("diagnostics_unavailable")
	}
	c.mu.Lock()
	persisted := c.status.DiagnosticsPersisted
	c.mu.Unlock()
	return persisted, nil
}

// ReportPastFault 合并采集器建立前已发现的固定故障；first/last是发现时间，受影响次数和损失区间未知。
// acknowledged表示该运行证据已阅；无返回值或异常，不执行I/O或推断未观察到的提交。
func (c *Collector) ReportPastFault(reason, first, last string, acknowledged bool) {
	from, _ := time.Parse(time.RFC3339Nano, first)
	until, _ := time.Parse(time.RFC3339Nano, last)
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.expired(Record{ReceivedAt: last}, auditNow()) {
		return
	}
	previous, existed := c.gapEntries[reason]
	c.recordGap(reason, 0, false, until)
	entry := c.gapEntries[reason]
	if acknowledged && (!existed || previous.Revision <= c.acknowledgedRevision) {
		entry.Revision = c.acknowledgedRevision
	}
	if c.expired(Record{ReceivedAt: first}, auditNow()) {
		entry.First = 0
	} else if entry.First == 0 || from.UnixNano() < entry.First {
		entry.First = from.UnixNano()
	}
	c.gapEntries[reason] = entry
}

// RestoreBridgeBaseline 接收采集器建立前已消费的本次宿主累计；total不是保留期内历史次数。
// 无返回值或异常，不新增缺口；仅在同一宿主首次建立采集器并移交运行证据时调用。
func (c *Collector) RestoreBridgeBaseline(total uint64) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.bridgeFailures = total
	c.status.Gaps["observer_unavailable"] = total
}

// ReportObserverState 更新宿主当前故障；空reason表示恢复，重复报告不生成新提醒。
// 无返回值或异常，仅接受内部固定原因，不执行I/O；恢复不会删除未到期历史。
func (c *Collector) ReportObserverState(reason string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.observerReason == reason {
		c.previousActive = nil
		return
	}
	if old := c.observerReason; old != "" {
		entry := c.gapEntries[old]
		entry.Flags &^= gapActive
		c.gapEntries[old] = entry
		c.diagnosticsChanged()
	}
	c.observerReason = reason
	if reason != "" {
		c.beginFault(reason)
	}
	// 首轮宿主核验完成后，未再次出现的旧故障已恢复，之后复发必须重新提醒。
	c.previousActive = nil
}
