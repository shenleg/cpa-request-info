// 本文件管理安全目录、独占锁、身份种子、配置原子替换及 SQLite 单写入者生命周期。
package audit

import (
	"bytes"
	"crypto/rand"
	"errors"
	"io"
	"os"
	"path/filepath"
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
		// 打开后核对 inode，拒绝 Lstat 与 OpenRoot 之间的目录替换。
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
	identityReady := false
	defer func() {
		if c.diagnostics == nil && identityReady {
			if _, err := c.root.Lstat(diagnosticName); err == nil {
				if err := c.openDiagnostics(); err != nil {
					c.pauseStorage(err)
				}
			}
		}
	}()
	var err error
	c.path, err = filepath.Abs(c.path)
	if err != nil {
		return err
	}
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
	if err = c.initializeCallerScopeSeed(); err != nil {
		c.pause("identity_unavailable")
		return nil
	}
	identityReady = true
	names, used, err := c.scanDirectory()
	c.status.UsedBytes = used
	if err != nil {
		c.pause("storage_error")
		return nil
	}
	for _, name := range names {
		if strings.HasPrefix(name, "records-") || name == "retention.tmp" || name == "monitors.tmp" || name == databaseName+"-wal" || name == databaseName+"-shm" {
			c.pause("storage_error")
			return nil
		}
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
			config.BudgetBytes = c.status.BudgetBytes
		}
		c.config = config
		c.status.BudgetBytes = config.BudgetBytes
	} else if !errors.Is(err, os.ErrNotExist) {
		c.pause("storage_error")
		return nil
	}
	if err := c.openSQLite(); err != nil {
		c.pauseStorage(err)
		return nil
	}
	c.configValid = true
	if err := c.openDiagnostics(); err != nil {
		c.pauseStorage(err)
	}
	if err := c.sweepExpired(auditNow()); err != nil {
		c.pauseStorage(err)
	}
	if c.status.State == "active" {
		if _, err := c.sqliteWindowLimit(); err != nil {
			c.pauseStorage(err)
		}
	}
	// 新进程也必须满足恢复滞回，不能通过重启绕过容量暂停门槛。
	if c.status.State == "active" && c.status.BudgetBytes-c.status.UsedBytes < c.status.ResumeRequiredBytes {
		c.pause("budget_exhausted")
	}
	if err := c.refreshCapacity(); err != nil {
		c.pauseStorage(err)
	}
	return nil
}

const callerScopeSeedName = "caller-scope-v1-sqlite-v1.seed"
const callerScopeSeedMagic = "cpa-request-info:caller-scope-seed:sqlite:v1\n"
const callerScopeSeedBytes = 32

// initializeCallerScopeSeed 只在持锁空目录初始化 SQLite 身份域；失败现场保留，不迁移或轮换。
func (c *Collector) initializeCallerScopeSeed() error {
	for _, name := range []string{"identity.seed", "caller-scope-v1.seed"} {
		if _, err := c.root.Lstat(name); !errors.Is(err, os.ErrNotExist) {
			return errors.New("identity_unavailable")
		}
	}
	info, err := c.root.Lstat(callerScopeSeedName)
	if errors.Is(err, os.ErrNotExist) {
		directory, openErr := c.root.Open(".")
		if openErr != nil {
			return openErr
		}
		entries, readErr := directory.ReadDir(2)
		closeErr := directory.Close()
		if (readErr != nil && !errors.Is(readErr, io.EOF)) || closeErr != nil || len(entries) != 1 || entries[0].Name() != ".audit.lock" {
			return errors.New("identity_unavailable")
		}
		data := make([]byte, len(callerScopeSeedMagic)+callerScopeSeedBytes)
		copy(data, callerScopeSeedMagic)
		if _, err := rand.Read(data[len(callerScopeSeedMagic):]); err != nil {
			return err
		}
		file, createErr := c.root.OpenFile(callerScopeSeedName, os.O_WRONLY|os.O_CREATE|os.O_EXCL|syscall.O_NOFOLLOW, 0600)
		if createErr != nil {
			return createErr
		}
		n, writeErr := file.Write(data)
		syncErr := file.Sync()
		closeErr = file.Close()
		if writeErr != nil || n != len(data) || syncErr != nil || closeErr != nil {
			return errors.New("identity_unavailable")
		}
		c.newStorage = true
		info, err = c.root.Lstat(callerScopeSeedName)
	}
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm() != 0600 || info.Size() != int64(len(callerScopeSeedMagic)+callerScopeSeedBytes) {
		return errors.New("identity_unavailable")
	}
	file, err := c.root.OpenFile(callerScopeSeedName, os.O_RDONLY|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return err
	}
	opened, err := file.Stat()
	if err != nil || !os.SameFile(info, opened) || !opened.Mode().IsRegular() || opened.Mode().Perm() != 0600 || opened.Size() != info.Size() {
		file.Close()
		return errors.New("identity_unavailable")
	}
	data, readErr := io.ReadAll(io.LimitReader(file, int64(len(callerScopeSeedMagic)+callerScopeSeedBytes+1)))
	syncErr := file.Sync()
	closeErr := file.Close()
	if readErr != nil || len(data) != len(callerScopeSeedMagic)+callerScopeSeedBytes || !bytes.HasPrefix(data, []byte(callerScopeSeedMagic)) || syncErr != nil || closeErr != nil {
		return errors.New("identity_unavailable")
	}
	if err := c.directory.Sync(); err != nil {
		return err
	}
	c.callerScopeSeed = data[len(callerScopeSeedMagic):]
	return nil
}

// scanDirectory 将专用目录、未知辅助文件和嵌套文件的保守分配空间全部计入预算，拒绝链接和特殊文件。
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
					if err := walk(name, depth+1); err != nil {
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
	// 同时计算逻辑长度与实际块数；即使稀疏文件和空文件也不能逃过预算。
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

func (c *Collector) saveMonitors(data []byte, budget int64) error {
	_, used, err := c.scanDirectory()
	if err != nil {
		return errors.New("storage_error")
	}
	c.mu.Lock()
	c.status.UsedBytes = used
	c.mu.Unlock()
	if used+rounded(int64(len(data)))+4096 > budget {
		return errors.New("budget_exhausted")
	}
	file, err := c.root.OpenFile("monitors.tmp", os.O_WRONLY|os.O_CREATE|os.O_EXCL|syscall.O_NOFOLLOW, 0600)
	if err != nil {
		return errors.New("storage_error")
	}
	if n, err := file.Write(data); err != nil || n != len(data) {
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
		active, reason := c.status.State == "active", c.status.Reason
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
		// 查询可用作 FIFO 保存屏障；释放 I/O 锁前完成队列记账，避免已查到记录却仍显示排队。
		c.mu.Lock()
		c.queuedBytes -= queued.bytes
		c.status.Queued--
		c.mu.Unlock()
		c.ioMu.Unlock()
	}
}

func (c *Collector) releaseFiles() {
	failed := false
	if c.connection != nil {
		if c.connection.Close() != nil {
			failed = true
		}
		c.connection = nil
	}
	if c.db != nil {
		if c.db.Close() != nil {
			failed = true
		}
		c.db = nil
	}
	for _, file := range []*os.File{c.database, c.journal, c.diagnostics} {
		if file != nil && file.Close() != nil {
			failed = true
		}
	}
	c.database, c.journal, c.diagnostics = nil, nil, nil
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
