// 本文件回收已到期分段；未到期记录保留原始行，原子替换成功前原段始终是权威副本。
package audit

import (
	"errors"
	"io"
	"os"
	"syscall"
	"time"
)

// 每次只改写一个最大1MiB分段，额外目录块计入维护峰值；配置预留另行保留。
const maintenanceReserve int64 = segmentSize + 4096
const retentionTemporary = "retention.tmp"

// sweepExpired 在ioMu内清理完整校验过的存储，不删除未知文件或损坏尾部。
func (c *Collector) sweepExpired(now time.Time) error {
	c.mu.Lock()
	usable := c.configValid && (c.status.State == "active" || c.status.Reason == "budget_exhausted")
	c.mu.Unlock()
	if !usable {
		return nil
	}
	// 异常中断只可能留下未提交临时副本；原段或原子替换后的段已经过启动完整校验。
	if info, err := c.root.Lstat(retentionTemporary); err == nil {
		if !info.Mode().IsRegular() || info.Mode().Perm() != 0600 || info.Size() > segmentSize {
			return errors.New("storage_error")
		}
		if err := c.root.Remove(retentionTemporary); err != nil {
			return err
		}
		if err := c.directory.Sync(); err != nil {
			return err
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	capacityBlocked := false
	for index := 0; index < len(c.segments); {
		seg := c.segments[index]
		expired := 0
		if err := c.readSegment(seg, func(record Record, _ int64) error {
			if c.expired(record, now) {
				expired++
			}
			return nil
		}); err != nil {
			return err
		}
		if expired == 0 {
			index++
			continue
		}
		// 活动句柄不能跨替换继续追加到已取消目录关联的旧inode。
		if c.active != nil {
			err := c.active.Close()
			c.active = nil
			if err != nil {
				return err
			}
		}
		c.pageHints = nil
		if expired == seg.count {
			if err := c.root.Remove(seg.name); err != nil {
				return err
			}
			c.segments = append(c.segments[:index], c.segments[index+1:]...)
		} else {
			committed, err := c.retainLiveLines(seg, now)
			if err != nil {
				if err.Error() != "budget_exhausted" {
					return err
				}
				// 降额后混合段可能无副本空间；后面的全到期段仍能不增加占用地释放空间。
				capacityBlocked = true
				index++
				continue
			}
			c.segments[index].committed = committed
			c.segments[index].count -= expired
			index++
		}
		if err := c.directory.Sync(); err != nil {
			return err
		}
		c.mu.Lock()
		c.status.Saved -= int64(expired)
		c.mu.Unlock()
	}
	_, used, err := c.scanDirectory()
	c.mu.Lock()
	c.status.UsedBytes = used
	c.mu.Unlock()
	if err == nil && capacityBlocked {
		return errors.New("budget_exhausted")
	}
	return err
}

// retainLiveLines 有界复制原始幸存行；只有临时文件完整同步后才替换，写入失败不动原段。
func (c *Collector) retainLiveLines(seg segment, now time.Time) (int64, error) {
	_, used, err := c.scanDirectory()
	if err != nil {
		return 0, err
	}
	c.mu.Lock()
	budget := c.status.BudgetBytes
	c.mu.Unlock()
	if used+rounded(seg.committed)+4096+auxiliaryReserve > budget {
		return 0, errors.New("budget_exhausted")
	}
	source, err := c.root.OpenFile(seg.name, os.O_RDONLY|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return 0, err
	}
	defer source.Close()
	target, err := c.root.OpenFile(retentionTemporary, os.O_WRONLY|os.O_CREATE|os.O_EXCL|syscall.O_NOFOLLOW, 0600)
	if err != nil {
		return 0, err
	}
	var offset, committed int64
	err = c.readSegment(seg, func(record Record, length int64) error {
		defer func() { offset += length }()
		if c.expired(record, now) {
			return nil
		}
		n, err := io.CopyN(target, io.NewSectionReader(source, offset, length), length)
		committed += n
		return err
	})
	if err == nil {
		err = target.Sync()
	}
	closeErr := target.Close()
	if err != nil {
		return 0, err
	}
	if closeErr != nil {
		return 0, closeErr
	}
	if err := c.root.Rename(retentionTemporary, seg.name); err != nil {
		return 0, err
	}
	return committed, nil
}
