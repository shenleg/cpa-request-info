// 本文件提供已同步记录的有界查询；仅缓存最近一页的文件位置，避免详情核对反复解析历史正文。
package audit

import (
	"errors"
	"io"
	"os"
	"syscall"
)

type recordLocation struct {
	segmentIndex int
	offset       int64
	length       int64
}

// List 按分段/追加顺序倒序返回已保存记录及总数；offset 必须非负，limit 范围为 1 至 100。
// 返回独立记录副本；参数错误、关闭或存储读取失败返回固定错误，不读取队列和未提交尾部。
func (c *Collector) List(offset, limit int) ([]Record, int, error) {
	if offset < 0 || limit < 1 || limit > 100 {
		return nil, 0, errors.New("invalid_pagination")
	}
	c.ioMu.Lock()
	defer c.ioMu.Unlock()
	if c.root == nil {
		return nil, 0, errors.New("collector_closed")
	}
	total := 0
	for _, seg := range c.segments {
		total += seg.count
	}
	result := make([]Record, 0, limit)
	c.pageHints = nil
	hints := make(map[string]recordLocation, limit)
	if offset >= total {
		return result, total, nil
	}
	remainingOffset := offset
	for index := len(c.segments) - 1; index >= 0 && len(result) < limit; index-- {
		seg := c.segments[index]
		if remainingOffset >= seg.count {
			remainingOffset -= seg.count
			continue
		}
		end := seg.count - remainingOffset
		start := end - (limit - len(result))
		if start < 0 {
			start = 0
		}
		selected := make([]Record, 0, end-start)
		current := 0
		var position int64
		err := c.readSegment(seg, func(record Record, bytes int64) error {
			if current >= start && current < end {
				selected = append(selected, record)
				hints[record.ID] = recordLocation{segmentIndex: index, offset: position, length: bytes}
			}
			position += bytes
			current++
			return nil
		})
		if err != nil {
			c.pause("storage_error")
			return nil, total, errors.New("storage_error")
		}
		for i := len(selected) - 1; i >= 0; i-- {
			result = append(result, selected[i])
		}
		remainingOffset = 0
	}
	c.pageHints = hints
	return result, total, nil
}

// Detail 返回 id 的独立白名单记录；最近一页以受限位置读取，其余回退扫描已同步记录。
// 位置缓存最多100条且不含正文，查询仍校验记录及提交边界，单行最多512KiB。
// id 长度须为 1 至 128 字节；未找到、关闭和存储错误返回固定错误，不读取队列或未提交尾部。
func (c *Collector) Detail(id string) (Record, error) {
	if len(id) == 0 || len(id) > 128 {
		return Record{}, errors.New("invalid_id")
	}
	c.ioMu.Lock()
	defer c.ioMu.Unlock()
	if c.root == nil {
		return Record{}, errors.New("collector_closed")
	}
	if location, ok := c.pageHints[id]; ok {
		record, err := c.readLocated(location)
		if err != nil || record.ID != id {
			c.pause("storage_error")
			return Record{}, errors.New("storage_error")
		}
		return record, nil
	}
	found := errors.New("found")
	var result Record
	for index := len(c.segments) - 1; index >= 0; index-- {
		err := c.readSegment(c.segments[index], func(record Record, _ int64) error {
			if record.ID == id {
				result = record
				return found
			}
			return nil
		})
		if errors.Is(err, found) {
			return result, nil
		}
		if err != nil {
			c.pause("storage_error")
			return Record{}, errors.New("storage_error")
		}
	}
	return Record{}, errors.New("not_found")
}

func (c *Collector) readLocated(location recordLocation) (Record, error) {
	if location.segmentIndex < 0 || location.segmentIndex >= len(c.segments) || location.offset < 0 || location.length <= 0 || location.length > maxLine {
		return Record{}, errors.New("storage_error")
	}
	seg := c.segments[location.segmentIndex]
	if location.offset+location.length > seg.committed {
		return Record{}, errors.New("storage_error")
	}
	file, err := c.root.OpenFile(seg.name, os.O_RDONLY|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return Record{}, err
	}
	defer func() {
		if file.Close() != nil {
			c.pause("storage_error")
		}
	}()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm() != 0600 || info.Size() < seg.committed {
		return Record{}, errors.New("storage_error")
	}
	var record Record
	count := 0
	err = eachRecord(io.NewSectionReader(file, location.offset, location.length), location.length, func(value Record, _ int64) error { record = value; count++; return nil })
	if err != nil || count != 1 {
		return Record{}, errors.New("storage_error")
	}
	return record, nil
}

func (c *Collector) readSegment(seg segment, visit func(Record, int64) error) error {
	file, err := c.root.OpenFile(seg.name, os.O_RDONLY|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm() != 0600 || info.Size() < seg.committed {
		return errors.New("storage_error")
	}
	// 即使文件存在部分写入尾部，LimitReader 也将查询约束到上次 fsync 后发布的偏移。
	return eachRecord(io.LimitReader(file, seg.committed), seg.committed, visit)
}
