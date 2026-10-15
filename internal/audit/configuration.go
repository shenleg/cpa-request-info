// 本文件维护有界对象目录、正文上限、保留期和总预算；持久化成功后才发布，停用不删除未到期历史。
package audit

import (
	"encoding/json"
	"errors"
	"strings"
	"unicode/utf8"
)

// Object 是持久化对象元数据；Name 为当前显示名称，Monitored 表示是否接受后续提交，不包含凭据。
type Object struct {
	Name      string `json:"name"`
	Monitored bool   `json:"monitored"`
}

type configuration struct {
	Version       int               `json:"version"`
	CapturePaused bool              `json:"capture_paused"`
	MaxTextBytes  int               `json:"max_text_bytes"`
	Objects       map[string]Object `json:"objects"`
	RetentionDays int               `json:"retention_days"`
	RetentionCaps []retentionCap    `json:"retention_caps"`
	BudgetBytes   int64             `json:"budget_bytes"`
}

// Monitor 添加或重新启用 objectID 并设置当前 name；名称最多256 UTF-8字节，身份为64位十六进制。
// 返回验证、容量或存储固定错误；仅同步成功后发布内存配置，不改写输入历史。
// 文件替换后的同步/扫描失败不能保证磁盘回滚，须停采核对配置，重启可能加载已替换值。
func (c *Collector) Monitor(objectID, name string) error {
	if !validObjectID(objectID) || !validName(name) {
		return errors.New("invalid_monitor")
	}
	return c.changeConfiguration(func(next *configuration) error {
		next.Objects[strings.Clone(objectID)] = Object{Name: strings.Clone(name), Monitored: true}
		return nil
	})
}

// Remove 停用已知 objectID，保留名称和历史；重复停用仍同步保存，未知身份或保存失败返回固定错误。
// 保存失败不发布内存配置但会停采，文件替换后失败的磁盘结果可能不确定，须核对而非假定已回滚。
func (c *Collector) Remove(objectID string) error {
	return c.changeConfiguration(func(next *configuration) error {
		object, exists := next.Objects[objectID]
		if !exists {
			return errors.New("unknown_object")
		}
		object.Monitored = false
		next.Objects[objectID] = object
		return nil
	})
}

// Rename 更新已知 objectID 的当前 name；不改变监控资格，历史查询立即使用当前名称。
// 名称非法、未知对象、关闭或存储失败返回固定错误，不改写历史记录；保存失败不发布内存名称。
// 文件已替换后的同步/扫描失败不能保证磁盘回滚，须停采核对，重启可能加载新名称。
func (c *Collector) Rename(objectID, name string) error {
	if !validName(name) {
		return errors.New("invalid_name")
	}
	return c.changeConfiguration(func(next *configuration) error {
		object, exists := next.Objects[objectID]
		if !exists {
			return errors.New("unknown_object")
		}
		object.Name = strings.Clone(name)
		next.Objects[objectID] = object
		return nil
	})
}

// SetPaused 同步保存主动暂停配置；paused 表示后续提交预期跳过，不取消已接受副本或解除存储故障。
// 无成功返回值，关闭、容量或存储失败返回固定错误；失败不发布内存暂停值，但可能新增存储故障。
// 文件已替换后的同步/扫描失败不能保证磁盘回滚，须停采核对，重启可能加载新暂停值。
func (c *Collector) SetPaused(paused bool) error {
	return c.changeConfiguration(func(next *configuration) error {
		next.CapturePaused = paused
		return nil
	})
}

// SetMaxTextBytes 同步保存后续提交的正文上限；limit 为1至65536个UTF-8字节，不追溯已接收副本。
// 成功无返回值，非法上限、关闭或存储失败返回固定错误；保存失败不发布新上限，并遵循配置失败停采规则。
func (c *Collector) SetMaxTextBytes(limit int) error {
	return c.SetSettings(SettingsChange{MaxTextBytes: &limit})
}

// Objects 返回已知对象目录的独立副本；无参数或 I/O 异常，包含已停用对象，调用者修改不影响配置。
func (c *Collector) Objects() map[string]Object {
	c.mu.Lock()
	defer c.mu.Unlock()
	return cloneObjects(c.config.Objects)
}

// Monitors 返回所有已知对象的当前名称；兼容查询投影，包含停用对象，无参数或 I/O 异常。
func (c *Collector) Monitors() map[string]string {
	objects := c.Objects()
	names := make(map[string]string, len(objects))
	for id, object := range objects {
		names[id] = object.Name
	}
	return names
}

func validName(name string) bool {
	return utf8.ValidString(name) && strings.TrimSpace(name) != "" && len(name) <= 256
}

func cloneObjects(objects map[string]Object) map[string]Object {
	result := make(map[string]Object, len(objects))
	for id, object := range objects {
		result[id] = object
	}
	return result
}

func (c *Collector) changeConfiguration(change func(*configuration) error) error {
	c.ioMu.Lock()
	defer c.ioMu.Unlock()
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return errors.New("collector_closed")
	}
	if !c.configValid {
		c.mu.Unlock()
		return errors.New("configuration_unavailable")
	}
	next := c.config
	next.Version = 4
	next.RetentionCaps = append([]retentionCap{}, c.config.RetentionCaps...)
	next.Objects = cloneObjects(c.config.Objects)
	c.mu.Unlock()
	if err := change(&next); err != nil {
		return err
	}
	// 已停用对象也计入上限，避免反复加入/移除形成无界名称目录。
	if len(next.Objects) > 1024 {
		return errors.New("monitor_limit")
	}
	data, err := json.Marshal(next)
	if err != nil || len(data) > maxLine {
		return errors.New("monitor_limit")
	}
	// 降额先按旧预算完成原子替换；提高预算则允许使用管理员新授权的空间。
	c.mu.Lock()
	writeBudget := max(c.config.BudgetBytes, next.BudgetBytes)
	c.mu.Unlock()
	if err = c.saveMonitors(data, writeBudget); err != nil {
		reason := "storage_error"
		if err.Error() == "budget_exhausted" {
			reason = "budget_exhausted"
		}
		c.pause(reason)
		return err
	}
	c.mu.Lock()
	c.config = next
	c.status.BudgetBytes = next.BudgetBytes
	c.mu.Unlock()
	if err := c.refreshCapacity(); err != nil {
		// 配置已经同步成功；独立的写入健康故障不能伪装为本次设置未保存。
		c.pauseStorage(err)
	}
	return nil
}

func decodeConfiguration(data []byte) (configuration, error) {
	fields, err := protocolObject(data)
	if err != nil {
		return configuration{}, errors.New("invalid_configuration")
	}
	config := configuration{Version: 4, MaxTextBytes: maxText, RetentionDays: defaultRetentionDays, RetentionCaps: []retentionCap{}, Objects: make(map[string]Object)}
	if _, versioned := fields["version"]; versioned {
		var version *int
		var paused *bool
		expectedFields := 3
		if rawLimit, present := fields["max_text_bytes"]; present {
			expectedFields++
			var limit *int
			if json.Unmarshal(rawLimit, &limit) != nil || limit == nil || *limit < 1 || *limit > maxText {
				return configuration{}, errors.New("invalid_configuration")
			}
			config.MaxTextBytes = *limit
		}
		// 旧version2缺省保留期为七天；新版要求完整期限字段，不能把损坏配置当作默认设置。
		if json.Unmarshal(fields["version"], &version) != nil || version == nil || (*version != 2 && *version != 3 && *version != 4) {
			return configuration{}, errors.New("invalid_configuration")
		}
		if *version == 4 {
			expectedFields++
			var budget *int64
			if json.Unmarshal(fields["budget_bytes"], &budget) != nil || budget == nil || *budget < minBudget || *budget > defaultBudget {
				return configuration{}, errors.New("invalid_configuration")
			}
			config.BudgetBytes = *budget
		}
		if *version >= 3 {
			expectedFields += 2
			var days *int
			if json.Unmarshal(fields["retention_days"], &days) != nil || days == nil || *days < 1 || *days > maxRetentionDays || decodeRetentionCaps(fields["retention_caps"], *days, &config.RetentionCaps) != nil {
				return configuration{}, errors.New("invalid_configuration")
			}
			config.RetentionDays = *days
			if _, present := fields["max_text_bytes"]; !present {
				return configuration{}, errors.New("invalid_configuration")
			}
		}
		if len(fields) != expectedFields || json.Unmarshal(fields["capture_paused"], &paused) != nil || paused == nil {
			return configuration{}, errors.New("invalid_configuration")
		}
		config.CapturePaused = *paused
		objects, err := protocolObject(fields["objects"])
		if err != nil || len(objects) > 1024 {
			return configuration{}, errors.New("invalid_configuration")
		}
		for id, raw := range objects {
			meta, err := protocolObject(raw)
			var name string
			var monitored *bool
			// Go 默认大小写匹配及未知字段忽略会掩盖歧义配置；持久配置要求精确白名单。
			if err != nil || len(meta) != 2 || json.Unmarshal(meta["name"], &name) != nil || json.Unmarshal(meta["monitored"], &monitored) != nil || monitored == nil {
				return configuration{}, errors.New("invalid_configuration")
			}
			config.Objects[id] = Object{Name: name, Monitored: *monitored}
		}
	} else {
		if len(fields) > 1024 {
			return configuration{}, errors.New("invalid_configuration")
		}
		for id, raw := range fields {
			var name string
			if json.Unmarshal(raw, &name) != nil {
				return configuration{}, errors.New("invalid_configuration")
			}
			config.Objects[id] = Object{Name: name, Monitored: true}
		}
	}
	for id, object := range config.Objects {
		if !validObjectID(id) || !validName(object.Name) {
			return configuration{}, errors.New("invalid_configuration")
		}
	}
	return config, nil
}
