// 本文件管理容量滞回及固定大小基础诊断；摘要不保存输入、身份或逐提交日志。
package audit

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"syscall"
	"time"
)

const diagnosticSize int64 = 4096
const diagnosticName = "capacity.json"

// 最大编码行和新分段目录开销之外多留256KiB，恢复后不会因相同大小的下一条输入立即停采。
const resumeRequiredBytes = auxiliaryReserve + maintenanceReserve + maxLine + 4096 + (256 << 10)

type diagnosticSummary struct {
	Version      int               `json:"version"`
	CapacityGap  bool              `json:"capacity_gap"`
	LastAt       string            `json:"last_at"`
	SessionDirty bool              `json:"session_dirty"`
	GapHistory   map[string]string `json:"gap_history,omitempty"`
	Recovery     Recovery          `json:"recovery"`
	Alerts       *gapDisk          `json:"alerts,omitempty"`
}

// openDiagnostics 在ioMu内打开固定分配文件；会话开始先同步dirty，重复打开不重新解释本会话。
func (c *Collector) openDiagnostics() error {
	info, err := c.root.Lstat(diagnosticName)
	create := errors.Is(err, os.ErrNotExist)
	if err != nil && !create {
		return err
	}
	if !create && (!info.Mode().IsRegular() || info.Mode().Perm() != 0600 || info.Size() != diagnosticSize) {
		return errors.New("storage_error")
	}
	if create {
		_, used, err := c.scanDirectory()
		if err != nil {
			return err
		}
		c.mu.Lock()
		budget := c.status.BudgetBytes
		c.mu.Unlock()
		if used+diagnosticSize+4096 > budget {
			return errors.New("budget_exhausted")
		}
	}
	flags := os.O_RDWR | syscall.O_NOFOLLOW
	if create {
		flags |= os.O_CREATE | os.O_EXCL
	}
	file, err := c.root.OpenFile(diagnosticName, flags, 0600)
	if err != nil {
		return err
	}
	var summary diagnosticSummary
	if create {
		if err := file.Truncate(diagnosticSize); err != nil {
			file.Close()
			return err
		}
		// 新目录没有先前采集证据；已有配置/分段却没有标记时不能倒推出正常退出。
		_, configErr := c.root.Lstat("monitors.json")
		if configErr != nil && !errors.Is(configErr, os.ErrNotExist) {
			file.Close()
			return configErr
		}
		if errors.Is(configErr, os.ErrNotExist) && len(c.segments) == 0 {
			c.mu.Lock()
			if !c.sessionStarted {
				c.status.Recovery = Recovery{}
			}
			c.mu.Unlock()
		}
	} else {
		data := make([]byte, diagnosticSize)
		if _, err := file.ReadAt(data, 0); err != nil {
			file.Close()
			return err
		}
		if summary, err = c.decodeDiagnostics(data); err != nil {
			file.Close()
			return err
		}
	}
	c.mu.Lock()
	if !c.sessionStarted && !create {
		c.status.CapacityGap, c.capacityLastAt = summary.CapacityGap, summary.LastAt
		if summary.Version == 1 {
			c.status.Recovery = Recovery{Uncertain: true, DetectedAt: auditNow().UTC().Format(time.RFC3339Nano), Reason: "legacy_unknown"}
		} else {
			c.status.GapHistory, c.status.Recovery = summary.GapHistory, summary.Recovery
			if summary.Version == 3 {
				c.gapEntries = summary.Alerts.Entries
				c.alertRevision, c.acknowledgedRevision = summary.Alerts.Revision, summary.Alerts.Acknowledged
				c.status.GapHistory = make(map[string]string)
				c.previousActive = make(map[string]gapEntry)
				for reason, entry := range c.gapEntries {
					if entry.Flags&gapActive != 0 {
						c.previousActive[reason] = entry
					}
					// 当前健康必须重新探测；磁盘中的活动标记不能把已恢复故障永远保留。
					entry.Flags &^= gapActive
					c.gapEntries[reason] = entry
					if entry.Last != 0 {
						c.status.GapHistory[reason] = gapTime(entry.Last)
					}
				}
			} else {
				for reason, last := range summary.GapHistory {
					at, _ := time.Parse(time.RFC3339Nano, last)
					c.recordGap(reason, 0, false, at)
					entry := c.gapEntries[reason]
					entry.First = 0
					c.gapEntries[reason] = entry
				}
			}
			if summary.SessionDirty {
				c.status.Recovery = Recovery{Uncertain: true, DetectedAt: auditNow().UTC().Format(time.RFC3339Nano), Reason: "unclean_shutdown"}
			}
		}
	}
	if c.status.CapacityGap {
		if _, exists := c.gapEntries["budget_exhausted"]; !exists {
			at, _ := time.Parse(time.RFC3339Nano, c.capacityLastAt)
			c.recordGap("budget_exhausted", 0, false, at)
			entry := c.gapEntries["budget_exhausted"]
			entry.First = 0
			c.gapEntries["budget_exhausted"] = entry
		}
	}
	if r := c.status.Recovery; r.Uncertain && (create || summary.Version != 3 || summary.SessionDirty) {
		at, _ := time.Parse(time.RFC3339Nano, r.DetectedAt)
		c.recordGap(r.Reason, 0, false, at)
	}
	if c.status.State == "paused" {
		reason := c.status.Reason
		c.beginFault(reason)
	}
	c.sessionDirty = true
	c.mu.Unlock()
	c.diagnostics = file
	// 同步dirty之后才允许取得capture快照；创建时还必须同步目录条目。
	if err := c.saveDiagnostics(); err != nil {
		return err
	}
	if err := c.directory.Sync(); err != nil {
		c.mu.Lock()
		c.status.DiagnosticsPersisted = false
		c.mu.Unlock()
		return err
	}
	c.mu.Lock()
	c.sessionStarted = true
	c.mu.Unlock()
	return nil
}

// decodeDiagnostics 校验磁盘边界的固定模式；损坏或未知版本不得被重建成无损失状态。
func (c *Collector) decodeDiagnostics(data []byte) (diagnosticSummary, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	var summary diagnosticSummary
	fields, err := protocolObject(data)
	if err != nil || json.Unmarshal(data, &summary) != nil || (summary.Version != 1 && summary.Version != 2 && summary.Version != 3) {
		return summary, errors.New("storage_error")
	}
	for _, key := range []string{"version", "capacity_gap", "last_at"} {
		if _, ok := fields[key]; !ok {
			return summary, errors.New("storage_error")
		}
	}
	var gap *bool
	if json.Unmarshal(fields["capacity_gap"], &gap) != nil || gap == nil || summary.CapacityGap != (summary.LastAt != "") {
		return summary, errors.New("storage_error")
	}
	if summary.LastAt != "" {
		if _, err := time.Parse(time.RFC3339Nano, summary.LastAt); err != nil {
			return summary, errors.New("storage_error")
		}
	}
	if summary.Version == 1 {
		if len(fields) != 3 {
			return summary, errors.New("storage_error")
		}
		return summary, nil
	}
	historyField := "gap_history"
	if summary.Version == 3 {
		historyField = "alerts"
	}
	for _, key := range []string{"session_dirty", historyField, "recovery"} {
		if _, ok := fields[key]; !ok {
			return summary, errors.New("storage_error")
		}
	}
	var dirty *bool
	if len(fields) != 6 || json.Unmarshal(fields["session_dirty"], &dirty) != nil || dirty == nil {
		return summary, errors.New("storage_error")
	}
	if summary.Version == 3 {
		if err := validateGapDisk(fields["alerts"], summary.Alerts); err != nil {
			return summary, err
		}
	} else if summary.GapHistory == nil || len(summary.GapHistory) > len(c.status.Gaps) {
		return summary, errors.New("storage_error")
	}
	for reason, last := range summary.GapHistory {
		if _, known := c.status.Gaps[reason]; !known {
			return summary, errors.New("storage_error")
		}
		if _, err := time.Parse(time.RFC3339Nano, last); err != nil {
			return summary, errors.New("storage_error")
		}
	}
	recoveryFields, err := protocolObject(fields["recovery"])
	r := summary.Recovery
	if err != nil || len(recoveryFields) != 6 || r.LostCount != nil || r.LostFrom != nil || r.LostUntil != nil {
		return summary, errors.New("storage_error")
	}
	for _, key := range []string{"uncertain", "detected_at", "reason", "lost_count", "lost_from", "lost_until"} {
		if _, ok := recoveryFields[key]; !ok {
			return summary, errors.New("storage_error")
		}
	}
	var uncertain *bool
	if json.Unmarshal(recoveryFields["uncertain"], &uncertain) != nil || uncertain == nil {
		return summary, errors.New("storage_error")
	}
	if r.Uncertain {
		if r.Reason != "unclean_shutdown" && r.Reason != "legacy_unknown" && r.Reason != "recovery_unavailable" && r.Reason != "observer_unavailable" {
			return summary, errors.New("storage_error")
		}
		if _, err := time.Parse(time.RFC3339Nano, r.DetectedAt); err != nil {
			return summary, errors.New("storage_error")
		}
	} else if r.DetectedAt != "" || r.Reason != "" {
		return summary, errors.New("storage_error")
	}
	return summary, nil
}

// saveDiagnostics 由单写入者原位覆盖既有4KiB；原位写不原子，失败时绝不保证告警已落盘。
func (c *Collector) saveDiagnostics() error {
	c.mu.Lock()
	c.pruneGaps(auditNow())
	alerts := &gapDisk{Entries: make(map[string]gapEntry, len(c.gapEntries)), Revision: c.alertRevision, Acknowledged: c.acknowledgedRevision}
	for reason, entry := range c.gapEntries {
		alerts.Entries[reason] = entry
	}
	summary := diagnosticSummary{Version: 3, CapacityGap: c.status.CapacityGap, LastAt: c.capacityLastAt, SessionDirty: c.sessionDirty, Recovery: c.status.Recovery, Alerts: alerts}
	revision := c.diagnosticRevision
	c.status.DiagnosticsPersisted = false
	c.mu.Unlock()
	current, err := c.root.Lstat(diagnosticName)
	if err != nil || !current.Mode().IsRegular() || current.Mode().Perm() != 0600 || current.Size() != diagnosticSize {
		return errors.New("storage_error")
	}
	opened, err := c.diagnostics.Stat()
	if err != nil || !os.SameFile(current, opened) {
		return errors.New("storage_error")
	}
	data := bytes.Repeat([]byte{' '}, int(diagnosticSize))
	encoded, err := json.Marshal(summary)
	if err != nil || len(encoded) > len(data) {
		return errors.New("storage_error")
	}
	copy(data, encoded)
	if n, err := c.diagnostics.WriteAt(data, 0); err != nil || n != len(data) {
		return errors.New("storage_error")
	}
	if err := c.diagnostics.Sync(); err != nil {
		return err
	}
	c.mu.Lock()
	c.status.DiagnosticsPersisted = c.diagnosticRevision == revision
	c.mu.Unlock()
	return nil
}

// refreshCapacity 在维护或配置事务末尾更新全部占用；只解除容量原因，不清除主动暂停或真实存储故障。
func (c *Collector) refreshCapacity() error {
	c.mu.Lock()
	usable := c.configValid && (c.status.State == "active" || c.status.Reason == "budget_exhausted")
	c.mu.Unlock()
	if !usable {
		return nil
	}
	if c.diagnostics == nil {
		if err := c.openDiagnostics(); err != nil {
			return err
		}
	}
	_, used, err := c.scanDirectory()
	if err != nil {
		return err
	}
	c.mu.Lock()
	c.status.UsedBytes = used
	if used+auxiliaryReserve+maintenanceReserve >= c.status.BudgetBytes {
		if c.status.Reason != "budget_exhausted" {
			c.status.State = "paused"
			c.status.Reason = "budget_exhausted"
			c.recordGap("budget_exhausted", 0, false, auditNow())
			entry := c.gapEntries["budget_exhausted"]
			entry.Flags |= gapActive
			c.gapEntries["budget_exhausted"] = entry
			c.markGapHistory("budget_exhausted")
		}
	} else if c.status.Reason == "budget_exhausted" && c.status.BudgetBytes-used >= resumeRequiredBytes {
		c.status.State = "active"
		c.status.Reason = ""
		entry := c.gapEntries["budget_exhausted"]
		entry.Flags &^= gapActive
		c.gapEntries["budget_exhausted"] = entry
		c.diagnosticsChanged()
	}
	// 摘要以最后一次事件起算；活动故障持续显示，调长不延长旧摘要。
	if c.status.CapacityGap && c.status.Reason != "budget_exhausted" && c.expired(Record{ReceivedAt: c.capacityLastAt}, auditNow()) {
		c.status.CapacityGap = false
		c.capacityLastAt = ""
		c.status.DiagnosticsPersisted = false
	}
	for reason, last := range c.status.GapHistory {
		if c.status.Reason != reason && c.expired(Record{ReceivedAt: last}, auditNow()) {
			delete(c.status.GapHistory, reason)
		}
	}
	if c.status.Recovery.Uncertain && c.expired(Record{ReceivedAt: c.status.Recovery.DetectedAt}, auditNow()) {
		c.status.Recovery = Recovery{}
	}
	c.mu.Unlock()
	return c.saveDiagnostics()
}

// pauseStorage 将容量不足与真实I/O故障分开，避免一次维护容量不足阻止后续自动清理。
func (c *Collector) pauseStorage(err error) {
	reason := "storage_error"
	if err.Error() == "budget_exhausted" {
		reason = "budget_exhausted"
	}
	c.pause(reason)
}
