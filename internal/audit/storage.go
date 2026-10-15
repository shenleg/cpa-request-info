// 本文件管理安全目录、独占锁、配置原子替换与单写入者分段JSONL；仅完整校验后的到期历史可由维护回收。
package audit

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"syscall"
)

func openDirectory(path string) (*os.Root, error) {
	absolute, err := filepath.Abs(path)
	if err != nil {
		return nil, err
	}
	current, err := os.OpenRoot(string(filepath.Separator))
	if err != nil {
		return nil, err
	}
	parts := strings.Split(strings.TrimPrefix(absolute, string(filepath.Separator)), string(filepath.Separator))
	for _, part := range parts {
		if part == "" {
			continue
		}
		info, err := current.Lstat(part)
		if errors.Is(err, os.ErrNotExist) {
			if err = current.Mkdir(part, 0700); err == nil {
				info, err = current.Lstat(part)
			}
		}
		if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			current.Close()
			return nil, errors.New("unsafe_directory")
		}
		next, err := current.OpenRoot(part)
		if err != nil {
			current.Close()
			return nil, err
		}
		opened, err := next.Stat(".")
		current.Close()
		// 对比打开前后 inode，避免 Lstat 与 OpenRoot 之间替换为链接或其他目录。
		if err != nil || !os.SameFile(info, opened) {
			next.Close()
			return nil, errors.New("unsafe_directory")
		}
		current = next
	}
	info, err := current.Stat(".")
	if err != nil || info.Mode().Perm() != 0700 {
		current.Close()
		return nil, errors.New("unsafe_permissions")
	}
	return current, nil
}

func (c *Collector) initialize() error {
	locked := false
	// 历史扫描失败不意味着运行标记不可读；仍尽力恢复既有诊断，避免损坏尾部遮住异常退出。
	// 未有标记时不在未知目录上新建文件，原故障状态保持可见。
	defer func() {
		if c.diagnostics == nil && locked {
			if _, err := c.root.Lstat(diagnosticName); err == nil {
				if err := c.openDiagnostics(); err != nil {
					c.pauseStorage(err)
				}
			}
		}
	}()
	var err error
	c.directory, err = c.root.Open(".")
	if err != nil {
		return err
	}
	c.lock, err = c.root.OpenFile(".audit.lock", os.O_CREATE|os.O_RDWR|syscall.O_NOFOLLOW, 0600)
	if err != nil {
		return err
	}
	info, err := c.lock.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm() != 0600 {
		return errors.New("unsafe_lock")
	}
	if err = syscall.Flock(int(c.lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		return err
	}
	locked = true
	names, used, err := c.scanDirectory()
	c.status.UsedBytes = used
	if err != nil {
		c.pause("storage_error")
		return nil
	}
	// 身份状态属于宿主；这里只核验，不创建、轮换或读取其秘密内容。
	seed, err := c.root.Lstat("identity.seed")
	if err != nil || !seed.Mode().IsRegular() || seed.Mode().Perm() != 0600 || seed.Size() != 32 {
		c.pause("identity_unavailable")
		return nil
	}
	if _, err = c.root.Lstat("monitors.tmp"); err == nil {
		c.pause("storage_error")
		return nil
	} else if !errors.Is(err, os.ErrNotExist) {
		c.pause("storage_error")
		return nil
	}
	if _, err = c.root.Lstat("monitors.json"); err == nil {
		data, err := c.readBounded("monitors.json", maxLine)
		if err != nil {
			c.pause("storage_error")
			return nil
		}
		config, err := decodeConfiguration(data)
		if err != nil {
			c.pause("storage_error")
			return nil
		}
		if config.BudgetBytes == 0 {
			// 只有旧版没有持久预算；后续保存后启动默认值不再覆盖管理员设置。
			config.BudgetBytes = c.status.BudgetBytes
		}
		c.config = config
		c.status.BudgetBytes = config.BudgetBytes
	} else if !errors.Is(err, os.ErrNotExist) {
		c.pause("storage_error")
		return nil
	}
	c.configValid = true
	sort.Strings(names)
	for _, name := range names {
		if !strings.HasPrefix(name, "records-") {
			continue
		}
		if _, ok := segmentNumber(name); !ok {
			c.pause("storage_error")
			return nil
		}
		seg := segment{name: name}
		err := c.scanSegment(&seg)
		c.segments = append(c.segments, seg)
		c.status.Saved += int64(seg.count)
		if err != nil {
			c.pause("storage_error")
			return nil
		}
	}
	if err := c.openDiagnostics(); err != nil {
		c.pauseStorage(err)
	}
	if err := c.sweepExpired(auditNow()); err != nil {
		c.pauseStorage(err)
	}
	// 新实例没有上一运行期的暂停原因；启动也须留出最坏写入和滞回空间，不能因默认active绕过恢复门槛。
	if c.status.State == "active" && c.status.BudgetBytes-c.status.UsedBytes < resumeRequiredBytes {
		c.pause("budget_exhausted")
	}
	if err := c.refreshCapacity(); err != nil {
		c.pauseStorage(err)
	}
	return nil
}

// scanDirectory 将整个专用目录（包括未知辅助文件及嵌套文件）的保守分配空间纳入预算。
// 分批读取及硬性条目/深度上限防止目录自身成为无界索引，任何链接/特殊文件均停采。
func (c *Collector) scanDirectory() ([]string, int64, error) {
	var names []string
	var used int64
	entries := 0
	var walk func(string, int) error
	walk = func(path string, depth int) error {
		if depth > 16 {
			return errors.New("directory_limit")
		}
		dir, err := c.root.Open(path)
		if err != nil {
			return err
		}
		defer dir.Close()
		info, err := dir.Stat()
		if err != nil {
			return err
		}
		used += allocated(info)
		for {
			batch, readErr := dir.ReadDir(128)
			for _, entry := range batch {
				entries++
				if entries > 4096 {
					return errors.New("directory_limit")
				}
				name := filepath.Join(path, entry.Name())
				info, err := c.root.Lstat(name)
				if err != nil {
					return err
				}
				if info.Mode()&os.ModeSymlink != 0 {
					return errors.New("unsafe_file")
				}
				if info.IsDir() {
					if info.Mode().Perm() != 0700 {
						return errors.New("unsafe_permissions")
					}
					if err = walk(name, depth+1); err != nil {
						return err
					}
				} else {
					if !info.Mode().IsRegular() || info.Mode().Perm() != 0600 {
						return errors.New("unsafe_file")
					}
					used += allocated(info)
					if path == "." {
						names = append(names, entry.Name())
					}
				}
			}
			if errors.Is(readErr, io.EOF) {
				return nil
			}
			if readErr != nil {
				return readErr
			}
		}
	}
	err := walk(".", 0)
	return names, used, err
}

func allocated(info os.FileInfo) int64 {
	// 逻辑长度按 4 KiB 向上取整；实际 blocks 更大时优先采用实际值，空文件/目录也预留一块。
	size := rounded(info.Size())
	if stat, ok := info.Sys().(*syscall.Stat_t); ok && stat.Blocks*512 > size {
		size = stat.Blocks * 512
	}
	return size
}
func rounded(size int64) int64 {
	if size == 0 {
		return 4096
	}
	return (size + 4095) / 4096 * 4096
}
func segmentNumber(name string) (int, bool) {
	if len(name) != len("records-000001.jsonl") || !strings.HasPrefix(name, "records-") || !strings.HasSuffix(name, ".jsonl") {
		return 0, false
	}
	n, err := strconv.Atoi(name[8:14])
	return n, err == nil && n > 0 && fmt.Sprintf("records-%06d.jsonl", n) == name
}
func (c *Collector) readBounded(name string, maximum int) ([]byte, error) {
	file, err := c.root.OpenFile(name, os.O_RDONLY|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, int64(maximum)+1))
	if err != nil || len(data) > maximum {
		return nil, errors.New("storage_error")
	}
	return data, nil
}
func (c *Collector) scanSegment(seg *segment) error {
	file, err := c.root.OpenFile(seg.name, os.O_RDONLY|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm() != 0600 || info.Size() > segmentSize {
		return errors.New("invalid_segment")
	}
	return eachRecord(file, info.Size(), func(record Record, bytes int64) error { seg.committed += bytes; seg.count++; return nil })
}

// eachRecord 只允许完整换行记录；读缓冲/每条记录均有硬上限，不将整段正文载入内存。
func eachRecord(file io.Reader, limit int64, visit func(Record, int64) error) error {
	if limit >= 0 {
		file = io.LimitReader(file, limit)
	}
	reader := bufio.NewReaderSize(file, 32<<10)
	for {
		var line []byte
		for {
			part, err := reader.ReadSlice('\n')
			if len(line)+len(part) > maxLine {
				return errors.New("invalid_record")
			}
			line = append(line, part...)
			if errors.Is(err, bufio.ErrBufferFull) {
				continue
			}
			if errors.Is(err, io.EOF) && len(line) == 0 {
				return nil
			}
			if err != nil {
				return errors.New("partial_record")
			}
			break
		}
		var record Record
		decoder := json.NewDecoder(bytes.NewReader(line))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&record); err != nil {
			return errors.New("invalid_record")
		}
		var trailing any
		if decoder.Decode(&trailing) != io.EOF || !validEvent(Event{ID: record.ID, ObjectID: record.ObjectID, ReceivedAt: record.ReceivedAt, Protocol: record.Protocol}) || !supportedProtocol(record.Protocol) || len(record.Model) > 1024 || len(record.Text) > maxText {
			return errors.New("invalid_record")
		}
		if err := visit(record, int64(len(line))); err != nil {
			return err
		}
	}
}

func (c *Collector) saveMonitors(data []byte, budget int64) error {
	_, used, err := c.scanDirectory()
	if err != nil {
		return errors.New("storage_error")
	}
	c.mu.Lock()
	c.status.UsedBytes = used
	c.mu.Unlock()
	// 配置可以使用自己的预留，但临时副本和目录增长必须仍在事务预算内。
	if used+rounded(int64(len(data)))+4096 > budget {
		return errors.New("budget_exhausted")
	}
	file, err := c.root.OpenFile("monitors.tmp", os.O_WRONLY|os.O_CREATE|os.O_EXCL|syscall.O_NOFOLLOW, 0600)
	if err != nil {
		return errors.New("storage_error")
	}
	if n, writeErr := file.Write(data); writeErr != nil || n != len(data) {
		file.Close()
		return errors.New("storage_error")
	}
	syncErr := file.Sync()
	closeErr := file.Close()
	if syncErr != nil || closeErr != nil || c.root.Rename("monitors.tmp", "monitors.json") != nil || c.directory.Sync() != nil {
		return errors.New("storage_error")
	}
	_, used, err = c.scanDirectory()
	if err != nil {
		return errors.New("storage_error")
	}
	c.mu.Lock()
	c.status.UsedBytes = used
	c.mu.Unlock()
	return nil
}

func (c *Collector) writeLoop() {
	defer close(c.done)
	ticks, stop := newMaintenanceTimer()
	defer stop()
	for {
		var queued queuedRecord
		select {
		case <-c.diagnosticWake:
			c.ioMu.Lock()
			if c.diagnostics != nil {
				if err := c.saveDiagnostics(); err != nil {
					c.pauseStorage(err)
				}
			}
			c.ioMu.Unlock()
			continue
		case <-ticks:
			c.ioMu.Lock()
			if err := c.sweepExpired(auditNow()); err != nil {
				c.pauseStorage(err)
			}
			if err := c.refreshCapacity(); err != nil {
				c.pauseStorage(err)
			}
			c.ioMu.Unlock()
			continue
		case record, ok := <-c.queue:
			if !ok {
				return
			}
			queued = record
		}
		c.ioMu.Lock()
		c.mu.Lock()
		active := c.status.State == "active"
		reason := c.status.Reason
		c.mu.Unlock()
		if active {
			if err := c.appendRecord(queued.record); err != nil {
				reason = "storage_error"
				if err.Error() == "budget_exhausted" {
					reason = "budget_exhausted"
				}
				c.gap(reason)
				c.pause(reason)
			}
		} else {
			if reason != "budget_exhausted" {
				reason = "storage_error"
			}
			c.gap(reason)
		}
		c.ioMu.Unlock()
		c.mu.Lock()
		c.queuedBytes -= queued.bytes
		c.status.Queued--
		c.mu.Unlock()
	}
}

func (c *Collector) appendRecord(record Record) error {
	data, err := json.Marshal(record)
	if err != nil || len(data)+1 > maxLine {
		return errors.New("storage_error")
	}
	data = append(data, '\n')
	_, used, err := c.scanDirectory()
	if err != nil {
		return err
	}
	c.mu.Lock()
	c.status.UsedBytes = used
	budget := c.status.BudgetBytes
	c.mu.Unlock()
	rotate := len(c.segments) == 0 || c.segments[len(c.segments)-1].committed+int64(len(data)) > segmentSize
	growth := rounded(int64(len(data))) + 4096
	if !rotate {
		old := c.segments[len(c.segments)-1].committed
		growth = rounded(old+int64(len(data))) - rounded(old)
	}
	if used+growth+auxiliaryReserve+maintenanceReserve > budget {
		return errors.New("budget_exhausted")
	}
	if rotate {
		if c.active != nil {
			if err = c.active.Close(); err != nil {
				c.active = nil
				return err
			}
			c.active = nil
		}
		number := 1
		if len(c.segments) > 0 {
			number, _ = segmentNumber(c.segments[len(c.segments)-1].name)
			number++
		}
		name := fmt.Sprintf("records-%06d.jsonl", number)
		c.active, err = c.root.OpenFile(name, os.O_WRONLY|os.O_CREATE|os.O_EXCL|syscall.O_NOFOLLOW, 0600)
		if err != nil {
			return err
		}
		if err = c.directory.Sync(); err != nil {
			return err
		}
		c.segments = append(c.segments, segment{name: name})
	} else if c.active == nil {
		c.active, err = c.root.OpenFile(c.segments[len(c.segments)-1].name, os.O_WRONLY|os.O_APPEND|syscall.O_NOFOLLOW, 0600)
		if err != nil {
			return err
		}
	}
	seg := &c.segments[len(c.segments)-1]
	info, err := c.active.Stat()
	if err != nil || info.Size() != seg.committed {
		return errors.New("storage_error")
	}
	n, err := c.active.Write(data)
	if err != nil || n != len(data) {
		return errors.New("storage_error")
	}
	if err = c.active.Sync(); err != nil {
		return err
	}
	// 只有 fsync 成功才推进查询边界；失败的尾部保留原样，并停采等待人工恢复。
	seg.committed += int64(len(data))
	seg.count++
	c.mu.Lock()
	c.status.Saved++
	c.mu.Unlock()
	_, used, err = c.scanDirectory()
	c.mu.Lock()
	c.status.UsedBytes = used
	c.mu.Unlock()
	if err != nil {
		// 正文已经同步并公开；后续目录核验失败只改变健康状态，不能把同一条计为丢失。
		c.pauseStorage(err)
	}
	return nil
}

func (c *Collector) releaseFiles() {
	failed := false
	if c.active != nil {
		if c.active.Close() != nil {
			failed = true
		}
		c.active = nil
	}
	if c.diagnostics != nil {
		if c.diagnostics.Close() != nil {
			failed = true
		}
		c.diagnostics = nil
	}
	if c.lock != nil {
		if syscall.Flock(int(c.lock.Fd()), syscall.LOCK_UN) != nil {
			failed = true
		}
		if c.lock.Close() != nil {
			failed = true
		}
		c.lock = nil
	}
	if c.directory != nil {
		if c.directory.Close() != nil {
			failed = true
		}
		c.directory = nil
	}
	if c.root != nil {
		if c.root.Close() != nil {
			failed = true
		}
		c.root = nil
	}
	if failed {
		c.pause("storage_error")
	}
}
