// 本文件使用结构化 SQL 查询已提交且未到期记录；正文按 Go ASCII 字面子串规则匹配。
package audit

import (
	"context"
	"database/sql"
	"errors"
	"strconv"
	"strings"
	"time"
)

// Query 指定已保存记录的交集条件；空字符串及 nil 时间表示不限制，时间包含两端，Model 精确匹配 Before 正文中的执行输入模型。
// Keyword 只匹配已保存正文的字面子串，ASCII 英文字母忽略大小写，不解释搜索语法。
type Query struct {
	ObjectID string
	From     *time.Time
	To       *time.Time
	Model    string
	Keyword  string
}

func lowerASCII(text string) string {
	return strings.Map(func(r rune) rune {
		if r >= 'A' && r <= 'Z' {
			return r + ('a' - 'A')
		}
		return r
	}, text)
}

func formatInteger(value int64) string { return strconv.FormatInt(value, 10) }

// visibilitySQL 与 expired 使用相同的瞬时时间和 RetentionCaps；秒/纳秒避免 RFC3339 格式及 UnixNano 溢出影响比较。
func (c *Collector) visibilitySQL(now time.Time) (string, []any) {
	cutoff := now.Add(-time.Duration(c.config.RetentionDays) * 24 * time.Hour)
	conditions := []string{"(received_sec,received_ns) > (?,?)"}
	args := []any{cutoff.Unix(), cutoff.Nanosecond()}
	for _, cap := range c.config.RetentionCaps {
		cutoff := now.Add(-time.Duration(cap.Days) * 24 * time.Hour)
		conditions = append(conditions, "((received_sec,received_ns) >= (?,?) OR (received_sec,received_ns) > (?,?))")
		args = append(args, cap.Before.Unix(), cap.Before.Nanosecond(), cutoff.Unix(), cutoff.Nanosecond())
	}
	return strings.Join(conditions, " AND "), args
}

func (c *Collector) queryReady() error {
	if c.root == nil {
		return errors.New("collector_closed")
	}
	if c.connection == nil || !c.configValid {
		return errors.New("storage_error")
	}
	if err := c.verifySQLiteFiles(); err != nil {
		c.pause("storage_error")
		return errors.New("storage_error")
	}
	return nil
}

// List 按事务提交的追加序号倒序返回记录及匹配总数；offset 非负，limit 范围1至100，条件在分页前取交集。
// 返回独立白名单副本；身份/分页非法、关闭或读取失败返回固定错误，不读取队列和未提交事务。
func (c *Collector) List(offset, limit int, query Query) ([]Record, int, error) {
	if query.ObjectID != "" && !validObjectID(query.ObjectID) {
		return nil, 0, errors.New("invalid_object")
	}
	if offset < 0 || limit < 1 || limit > 100 {
		return nil, 0, errors.New("invalid_pagination")
	}
	c.ioMu.Lock()
	defer c.ioMu.Unlock()
	if err := c.queryReady(); err != nil {
		return nil, 0, err
	}
	where, args := c.visibilitySQL(auditNow())
	if query.ObjectID != "" {
		where += " AND object_id=?"
		args = append(args, query.ObjectID)
	}
	if query.Model != "" {
		where += " AND model=?"
		args = append(args, query.Model)
	}
	if query.From != nil {
		where += " AND (received_sec,received_ns) >= (?,?)"
		args = append(args, query.From.Unix(), query.From.Nanosecond())
	}
	if query.To != nil {
		where += " AND (received_sec,received_ns) <= (?,?)"
		args = append(args, query.To.Unix(), query.To.Nanosecond())
	}
	if query.Keyword != "" {
		where += " AND audit_contains(text,?)"
		args = append(args, lowerASCII(query.Keyword))
	}
	var total int
	if err := c.connection.QueryRowContext(context.Background(), "SELECT count(*) FROM records WHERE "+where, args...).Scan(&total); err != nil {
		c.pause("storage_error")
		return nil, 0, errors.New("storage_error")
	}
	result := make([]Record, 0, limit)
	args = append(args, limit, offset)
	rows, err := c.connection.QueryContext(context.Background(), "SELECT "+recordColumns+" FROM records WHERE "+where+" ORDER BY seq DESC LIMIT ? OFFSET ?", args...)
	if err != nil {
		c.pause("storage_error")
		return nil, 0, errors.New("storage_error")
	}
	for rows.Next() {
		record, _, err := scanRecord(rows)
		if err != nil {
			rows.Close()
			c.pause("storage_error")
			return nil, 0, errors.New("storage_error")
		}
		result = append(result, record)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		c.pause("storage_error")
		return nil, 0, errors.New("storage_error")
	}
	if err := rows.Close(); err != nil {
		c.pause("storage_error")
		return nil, 0, errors.New("storage_error")
	}
	return result, total, nil
}

// HistoryObjects 返回所有未到期已提交记录的对象集合，最多1024项；无参数，不缓存正文或记录ID。
// 关闭、数据库读取失败或对象超限返回固定错误，不访问队列。
func (c *Collector) HistoryObjects() (map[string]bool, error) {
	c.ioMu.Lock()
	defer c.ioMu.Unlock()
	if err := c.queryReady(); err != nil {
		return nil, err
	}
	where, args := c.visibilitySQL(auditNow())
	rows, err := c.connection.QueryContext(context.Background(), "SELECT DISTINCT object_id FROM records WHERE "+where+" LIMIT 1025", args...)
	if err != nil {
		c.pause("storage_error")
		return nil, errors.New("storage_error")
	}
	result := make(map[string]bool)
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil || !validObjectID(id) {
			rows.Close()
			c.pause("storage_error")
			return nil, errors.New("storage_error")
		}
		result[id] = true
		if len(result) > 1024 {
			rows.Close()
			c.pause("storage_error")
			return nil, errors.New("storage_error")
		}
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		c.pause("storage_error")
		return nil, errors.New("storage_error")
	}
	if err := rows.Close(); err != nil {
		c.pause("storage_error")
		return nil, errors.New("storage_error")
	}
	return result, nil
}

// Models 返回全部未到期已提交记录的非空模型，按原字符串排序并精确去重，不受筛选或分页影响。
// 无参数，不访问队列；关闭或存储读取失败返回固定错误。
func (c *Collector) Models() ([]string, error) {
	c.ioMu.Lock()
	defer c.ioMu.Unlock()
	if err := c.queryReady(); err != nil {
		return nil, err
	}
	where, args := c.visibilitySQL(auditNow())
	rows, err := c.connection.QueryContext(context.Background(), "SELECT DISTINCT model FROM records WHERE model<>'' AND "+where+" ORDER BY model COLLATE BINARY", args...)
	if err != nil {
		c.pause("storage_error")
		return nil, errors.New("storage_error")
	}
	result := make([]string, 0)
	for rows.Next() {
		var model string
		if err := rows.Scan(&model); err != nil {
			rows.Close()
			c.pause("storage_error")
			return nil, errors.New("storage_error")
		}
		result = append(result, model)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		c.pause("storage_error")
		return nil, errors.New("storage_error")
	}
	if err := rows.Close(); err != nil {
		c.pause("storage_error")
		return nil, errors.New("storage_error")
	}
	return result, nil
}

// Detail 返回 id 最新提交且未到期的白名单记录；相同 ID 的执行各自保留，不因列表页改变选择结果。
// id 长度须为1至128字节；未找到、关闭或读取故障返回固定错误，不读取队列或未提交事务。
func (c *Collector) Detail(id string) (Record, error) {
	if len(id) == 0 || len(id) > 128 {
		return Record{}, errors.New("invalid_id")
	}
	c.ioMu.Lock()
	defer c.ioMu.Unlock()
	if err := c.queryReady(); err != nil {
		return Record{}, err
	}
	where, args := c.visibilitySQL(auditNow())
	args = append(args, id)
	row := c.connection.QueryRowContext(context.Background(), "SELECT "+recordColumns+" FROM records WHERE "+where+" AND id=? ORDER BY seq DESC LIMIT 1", args...)
	record, _, err := scanRecord(row)
	if errors.Is(err, sql.ErrNoRows) {
		return Record{}, errors.New("not_found")
	}
	if err != nil {
		c.pause("storage_error")
		return Record{}, errors.New("storage_error")
	}
	return record, nil
}
