// 本文件保存按接收时刻划分的有限期限上限；调长不能延长历史或使已过期记录重新可查。
package audit

import (
	"encoding/json"
	"errors"
	"time"
)

const defaultRetentionDays = 7
const maxRetentionDays = 30

// retentionCap 约束 Before 之前的提交；天数严格递增且低于当前值，最多29条。
type retentionCap struct {
	Before time.Time `json:"before"`
	Days   int       `json:"days"`
}

// SettingsChange 只包含本次要修改的参数；nil表示保持原值，确认仅授权本次缩短影响。
type SettingsChange struct {
	MaxTextBytes               *int
	RetentionDays              *int
	ConfirmRetentionShortening bool
}

// SetSettings 原子保存保留期及后续提交的正文上限；缩短必须由管理员显式确认。
// 保留期接受1–30整天，上限接受1–65536字节；验证、确认或持久化失败返回固定错误，不部分发布。
func (c *Collector) SetSettings(change SettingsChange) error {
	if change.MaxTextBytes != nil && (*change.MaxTextBytes < 1 || *change.MaxTextBytes > maxText) {
		return errors.New("invalid_max_text_bytes")
	}
	if change.RetentionDays != nil && (*change.RetentionDays < 1 || *change.RetentionDays > maxRetentionDays) {
		return errors.New("invalid_retention_days")
	}
	return c.changeConfiguration(func(next *configuration) error {
		if change.RetentionDays != nil {
			days := *change.RetentionDays
			if days < next.RetentionDays && !change.ConfirmRetentionShortening {
				return errors.New("retention_confirmation_required")
			}
			if days > next.RetentionDays {
				// 事务开始时刻就是持久策略的生效时刻；重叠提交依接收时刻归属，而非依落盘先后。
				before := auditNow().UTC()
				if len(next.RetentionCaps) > 0 && before.Before(next.RetentionCaps[len(next.RetentionCaps)-1].Before) {
					return errors.New("retention_clock_regressed")
				}
				// 同一时钟刻度此前更小的上限已经涵盖相同提交，不再保存重复边界。
				if len(next.RetentionCaps) == 0 || before.After(next.RetentionCaps[len(next.RetentionCaps)-1].Before) {
					next.RetentionCaps = append(next.RetentionCaps, retentionCap{Before: before, Days: next.RetentionDays})
				}
			}
			// 更晚且更小的上限覆盖此前规则；整数天数保证规则数量不随设置次数增长。
			caps := make([]retentionCap, 0, len(next.RetentionCaps))
			for _, cap := range next.RetentionCaps {
				if cap.Days >= days {
					continue
				}
				for len(caps) > 0 && caps[len(caps)-1].Days >= cap.Days {
					caps = caps[:len(caps)-1]
				}
				caps = append(caps, cap)
			}
			next.RetentionCaps = caps
			next.RetentionDays = days
		}
		if change.MaxTextBytes != nil {
			next.MaxTextBytes = *change.MaxTextBytes
		}
		return nil
	})
}

// expired 的入参均来自已校验持久记录和配置；调用者在同一ioMu事务内固定now和配置。
func (c *Collector) expired(record Record, now time.Time) bool {
	received, _ := time.Parse(time.RFC3339Nano, record.ReceivedAt)
	days := c.config.RetentionDays
	for _, cap := range c.config.RetentionCaps {
		if received.Before(cap.Before) && cap.Days < days {
			days = cap.Days
		}
	}
	return !now.Before(received.Add(time.Duration(days) * 24 * time.Hour))
}

// decodeRetentionCaps 严格读取持久化边界，拒绝无界、歧义或不规范的期限规则。
func decodeRetentionCaps(raw []byte, days int, result *[]retentionCap) error {
	var entries []json.RawMessage
	if json.Unmarshal(raw, &entries) != nil || entries == nil || len(entries) >= maxRetentionDays {
		return errors.New("invalid_configuration")
	}
	caps := make([]retentionCap, 0, len(entries))
	for _, entry := range entries {
		fields, err := protocolObject(entry)
		var cap retentionCap
		if err != nil || len(fields) != 2 || json.Unmarshal(fields["before"], &cap.Before) != nil || cap.Before.IsZero() || json.Unmarshal(fields["days"], &cap.Days) != nil || cap.Days < 1 || cap.Days >= days {
			return errors.New("invalid_configuration")
		}
		if len(caps) > 0 && (!cap.Before.After(caps[len(caps)-1].Before) || cap.Days <= caps[len(caps)-1].Days) {
			return errors.New("invalid_configuration")
		}
		caps = append(caps, cap)
	}
	*result = caps
	return nil
}
