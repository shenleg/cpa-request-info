// 本文件管理容量滞回与固定大小基础告警；告警不保存输入、身份或逐提交日志。
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

// openDiagnostics 在ioMu内打开或初始化固定分配文件；旧目录无空间时不创建越预算的告警。
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
	if create {
		if err := file.Truncate(diagnosticSize); err != nil {
			file.Close()
			return err
		}
	} else {
		data := make([]byte, diagnosticSize)
		if _, err := file.ReadAt(data, 0); err != nil {
			file.Close()
			return err
		}
		fields, err := protocolObject(data)
		var version int
		var gap *bool
		var last string
		if err != nil || len(fields) != 3 || json.Unmarshal(fields["version"], &version) != nil || version != 1 || json.Unmarshal(fields["capacity_gap"], &gap) != nil || gap == nil || json.Unmarshal(fields["last_at"], &last) != nil || (*gap != (last != "")) {
			file.Close()
			return errors.New("storage_error")
		}
		if last != "" {
			if _, err := time.Parse(time.RFC3339Nano, last); err != nil {
				file.Close()
				return errors.New("storage_error")
			}
		}
		c.mu.Lock()
		c.status.CapacityGap = *gap
		c.capacityLastAt = last
		c.mu.Unlock()
	}
	c.diagnostics = file
	// 启动也实际写入和同步，不能仅因目录可读就宣布可采集。
	if err := c.saveDiagnostics(); err != nil {
		return err
	}
	if err := c.directory.Sync(); err != nil {
		c.mu.Lock()
		c.status.DiagnosticsPersisted = false
		c.mu.Unlock()
		return err
	}
	return nil
}

// saveDiagnostics 由单写入者原位覆盖既有4KiB，不创建临时文件；调用期间新增缺口仍保留待保存标记。
func (c *Collector) saveDiagnostics() error {
	c.mu.Lock()
	gap, last := c.status.CapacityGap, c.capacityLastAt
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
	encoded, _ := json.Marshal(struct {
		Version int    `json:"version"`
		Gap     bool   `json:"capacity_gap"`
		LastAt  string `json:"last_at"`
	}{Version: 1, Gap: gap, LastAt: last})
	copy(data, encoded)
	if n, err := c.diagnostics.WriteAt(data, 0); err != nil || n != len(data) {
		c.mu.Lock()
		c.status.DiagnosticsPersisted = false
		c.mu.Unlock()
		return errors.New("storage_error")
	}
	if err := c.diagnostics.Sync(); err != nil {
		c.mu.Lock()
		c.status.DiagnosticsPersisted = false
		c.mu.Unlock()
		return err
	}
	c.mu.Lock()
	c.status.DiagnosticsPersisted = c.capacityLastAt == last
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
			c.incrementGap("budget_exhausted")
		}
	} else if c.status.Reason == "budget_exhausted" && c.status.BudgetBytes-used >= resumeRequiredBytes {
		c.status.State = "active"
		c.status.Reason = ""
	}
	// 有界摘要以最后一次容量事件起算；活动故障持续显示，调长仍受既有期限边界约束。
	if c.status.CapacityGap && c.status.Reason != "budget_exhausted" && c.expired(Record{ReceivedAt: c.capacityLastAt}, auditNow()) {
		c.status.CapacityGap = false
		c.capacityLastAt = ""
		c.status.DiagnosticsPersisted = false
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
