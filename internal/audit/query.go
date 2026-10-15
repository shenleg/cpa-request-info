// 本文件统一已同步且未到期记录的查询；只缓存最近一页位置，详情仍逐次核验到期状态。
package audit

import (
	"errors"
	"io"
	"os"
	"strings"
	"syscall"
	"time"
)

type recordLocation struct {
	segmentIndex int
	offset       int64
	length       int64
}

// Query 指定已保存记录的交集条件；空字符串及 nil 时间表示不限制，时间包含两端，Model 精确匹配客户端请求名。
// Keyword 只匹配已保存正文的字面子串，ASCII 英文字母忽略大小写，不解释搜索语法。
type Query struct {
	ObjectID string
	From     *time.Time
	To       *time.Time
	Model    string
	Keyword  string
}

func (q Query) matches(record Record) bool {
	if q.ObjectID != "" && record.ObjectID != q.ObjectID || q.Model != "" && record.Model != q.Model {
		return false
	}
	if q.From != nil || q.To != nil {
		// 持久化读取已验证接收时间；此处比较时刻，不受序列化时区或小数位数影响。
		received, _ := time.Parse(time.RFC3339Nano, record.ReceivedAt)
		if q.From != nil && received.Before(*q.From) || q.To != nil && received.After(*q.To) {
			return false
		}
	}
	return q.Keyword == "" || strings.Contains(lowerASCII(record.Text), q.Keyword)
}

func lowerASCII(text string) string {
	return strings.Map(func(r rune) rune {
		if r >= 'A' && r <= 'Z' {
			return r + ('a' - 'A')
		}
		return r
	}, text)
}

// List 按分段/追加顺序倒序返回已保存记录及匹配总数；offset 非负，limit 范围1至100，query 在分页前取交集。
// 返回独立记录副本；身份/分页非法、关闭或读取失败返回固定错误，不读取队列和未提交尾部。
func (c *Collector) List(offset, limit int, query Query) ([]Record, int, error) {
	if query.ObjectID != "" && !validObjectID(query.ObjectID) {
		return nil, 0, errors.New("invalid_object")
	}
	if offset < 0 || limit < 1 || limit > 100 {
		return nil, 0, errors.New("invalid_pagination")
	}
	c.ioMu.Lock()
	defer c.ioMu.Unlock()
	if c.root == nil {
		return nil, 0, errors.New("collector_closed")
	}
	query.Keyword = lowerASCII(query.Keyword)
	// 物理条数包含尚待清理的过期记录；空查询也必须先判定可见性再计数和分页。
	return c.listFiltered(offset, limit, query, auditNow())
}

// listFiltered 两遍扫描已提交边界：先独立计数，再选取匹配序号，避免先分页后过滤导致总数和页内容错误。
// 列表与搜索共用记录读取入口；到期可见性应在查询模块统一实现，不能只在关键词分支过滤。
func (c *Collector) listFiltered(offset, limit int, query Query, now time.Time) ([]Record, int, error) {
	c.pageHints = nil
	total := 0
	for _, seg := range c.segments {
		if err := c.readSegment(seg, func(record Record, _ int64) error {
			if !c.expired(record, now) && query.matches(record) {
				total++
			}
			return nil
		}); err != nil {
			c.pause("storage_error")
			return nil, 0, errors.New("storage_error")
		}
	}
	result := make([]Record, 0, limit)
	if offset >= total {
		return result, total, nil
	}
	end := total - offset
	start := end - limit
	if start < 0 {
		start = 0
	}
	hints := make(map[string]recordLocation, limit)
	current := 0
	for index, seg := range c.segments {
		var position int64
		if err := c.readSegment(seg, func(record Record, bytes int64) error {
			if !c.expired(record, now) && query.matches(record) {
				if current >= start && current < end {
					result = append(result, record)
					hints[record.ID] = recordLocation{segmentIndex: index, offset: position, length: bytes}
				}
				current++
			}
			position += bytes
			return nil
		}); err != nil {
			c.pause("storage_error")
			return nil, total, errors.New("storage_error")
		}
	}
	for left, right := 0, len(result)-1; left < right; left, right = left+1, right-1 {
		result[left], result[right] = result[right], result[left]
	}
	c.pageHints = hints
	return result, total, nil
}

// HistoryObjects 返回已有已提交历史的对象集合；无参数，最多1024个无秘密身份，不缓存正文或记录ID。
// 关闭、读取故障或身份目录超限返回固定错误，不访问队列及未提交尾部。
func (c *Collector) HistoryObjects() (map[string]bool, error) {
	c.ioMu.Lock()
	defer c.ioMu.Unlock()
	if c.root == nil {
		return nil, errors.New("collector_closed")
	}
	now := auditNow()
	result := make(map[string]bool)
	for _, seg := range c.segments {
		if err := c.readSegment(seg, func(record Record, _ int64) error {
			if c.expired(record, now) {
				return nil
			}
			result[record.ObjectID] = true
			if len(result) > 1024 {
				return errors.New("object_limit")
			}
			return nil
		}); err != nil {
			c.pause("storage_error")
			return nil, errors.New("storage_error")
		}
	}
	return result, nil
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
	now := auditNow()
	if location, ok := c.pageHints[id]; ok {
		record, err := c.readLocated(location)
		if err != nil || record.ID != id {
			c.pause("storage_error")
			return Record{}, errors.New("storage_error")
		}
		if c.expired(record, now) {
			return Record{}, errors.New("not_found")
		}
		return record, nil
	}
	found := errors.New("found")
	var result Record
	for index := len(c.segments) - 1; index >= 0; index-- {
		err := c.readSegment(c.segments[index], func(record Record, _ int64) error {
			if record.ID == id && !c.expired(record, now) {
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
