// 本文件实现单连接 SQLite、固定模式校验和已实际计费的 PERSIST 回滚日志工作空间。
package audit

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"
	"unicode/utf8"

	"github.com/mattn/go-sqlite3"
)

const databaseName = "records.sqlite"
const sqliteApplicationID = 0x43504149
const sqliteSchemaVersion = 1
const sqlitePageSize int64 = 4096

// 每次最多新增 1 MiB 主库额度；日志随当前页数增长，不按整个目录预算预分配。
const sqliteGrowthPages int64 = 256

const recordsSchema = `CREATE TABLE records (
 seq INTEGER PRIMARY KEY AUTOINCREMENT,
 id TEXT NOT NULL,
 object_id TEXT NOT NULL,
 received_at TEXT NOT NULL,
 received_sec INTEGER NOT NULL,
 received_ns INTEGER NOT NULL CHECK(received_ns BETWEEN 0 AND 999999999),
 protocol TEXT NOT NULL,
 purpose TEXT NOT NULL,
 model TEXT NOT NULL,
 request_reasoning TEXT,
 text TEXT NOT NULL,
 truncated INTEGER NOT NULL CHECK(truncated IN (0,1)),
 extraction_incomplete INTEGER NOT NULL CHECK(extraction_incomplete IN (0,1))
) STRICT`
const metadataSchema = `CREATE TABLE metadata (singleton INTEGER PRIMARY KEY CHECK(singleton=1), seed_digest TEXT NOT NULL) STRICT`

var sqliteIndexes = []string{
	`CREATE INDEX records_id_seq ON records(id, seq DESC)`,
	`CREATE INDEX records_object_seq ON records(object_id, seq DESC)`,
	`CREATE INDEX records_model_seq ON records(model, seq DESC)`,
	`CREATE INDEX records_received ON records(received_sec, received_ns)`,
}
var sqliteDriverOnce sync.Once

func (c *Collector) openSQLite() error {
	flags := os.O_RDWR | syscall.O_NOFOLLOW
	if c.newStorage {
		flags |= os.O_CREATE | os.O_EXCL
	}
	var err error
	c.database, err = c.root.OpenFile(databaseName, flags, 0600)
	if err != nil {
		return err
	}
	info, err := c.database.Stat()
	if err != nil || (!c.newStorage && info.Size() < sqlitePageSize) {
		return errors.New("storage_error")
	}
	c.journal, err = c.root.OpenFile(databaseName+"-journal", flags, 0600)
	if err != nil {
		return err
	}
	if err := c.verifySQLiteFiles(); err != nil {
		return err
	}
	if c.newStorage {
		// 第一次 SQLite 写入前也必须已有物理日志空间；空日志头无效，不会被误当成恢复记录。
		if err := c.resizeJournal(sqliteGrowthPages); err != nil {
			return err
		}
		if err := c.directory.Sync(); err != nil {
			return err
		}
	}
	sqliteDriverOnce.Do(func() {
		sql.Register("audit-sqlite3", &sqlite3.SQLiteDriver{ConnectHook: func(connection *sqlite3.SQLiteConn) error {
			// 直接复用 Go 字面匹配，SQLite LIKE 的通配符、NUL 和 Unicode 规则不参与搜索。
			return connection.RegisterFunc("audit_contains", func(text, keyword string) bool {
				return strings.Contains(lowerASCII(text), keyword)
			}, true)
		}})
	})
	u := url.URL{Scheme: "file", Path: filepath.Join(c.path, databaseName)}
	q := u.Query()
	q.Set("mode", "rw")
	// 外部只读检查可能短暂持有共享锁；等待只发生在异步写入者，不让正常读者造成永久停采。
	q.Set("_busy_timeout", "5000")
	q.Set("_journal_mode", "PERSIST")
	q.Set("_synchronous", "FULL")
	u.RawQuery = q.Encode()
	c.db, err = sql.Open("audit-sqlite3", u.String())
	if err != nil {
		return err
	}
	c.db.SetMaxOpenConns(1)
	c.db.SetMaxIdleConns(1)
	c.connection, err = c.db.Conn(context.Background())
	if err != nil {
		return err
	}
	// 连接建立及首次读取先让 SQLite 自己恢复 hot journal，应用不能在此之前改动既有日志。
	var schemaCount int64
	if err := c.connection.QueryRowContext(context.Background(), "SELECT count(*) FROM sqlite_schema").Scan(&schemaCount); err != nil {
		return err
	}
	if err := c.reopenRecoveredJournal(false); err != nil {
		return err
	}
	if c.newStorage {
		// SQLite 在初次读取空库时可能清除预建日志；恢复完成后、建表前重新分配。
		if err := c.resizeJournal(sqliteGrowthPages); err != nil {
			return err
		}
		if err := c.directory.Sync(); err != nil {
			return err
		}
	}
	for _, pragma := range []string{"PRAGMA cache_spill=OFF", "PRAGMA temp_store=MEMORY", "PRAGMA journal_size_limit=-1"} {
		if _, err := c.connection.ExecContext(context.Background(), pragma); err != nil {
			return err
		}
	}
	if c.newStorage {
		if schemaCount != 0 {
			return errors.New("storage_error")
		}
		for _, pragma := range []string{"PRAGMA page_size=4096", "PRAGMA auto_vacuum=INCREMENTAL", "PRAGMA max_page_count=256"} {
			if _, err := c.connection.ExecContext(context.Background(), pragma); err != nil {
				return err
			}
		}
		tx, err := c.connection.BeginTx(context.Background(), nil)
		if err != nil {
			return err
		}
		statements := []string{recordsSchema, metadataSchema}
		statements = append(statements, sqliteIndexes...)
		statements = append(statements, "PRAGMA application_id=1129333065", "PRAGMA user_version=1")
		for _, statement := range statements {
			if _, err := tx.Exec(statement); err != nil {
				return c.rollbackSQLite(tx, err)
			}
		}
		digest := sha256.Sum256(c.callerScopeSeed)
		if _, err := tx.Exec("INSERT INTO metadata(singleton,seed_digest) VALUES(1,?)", hex.EncodeToString(digest[:])); err != nil {
			return c.rollbackSQLite(tx, err)
		}
		if err := tx.Commit(); err != nil {
			return err
		}
		c.pageLimit = sqliteGrowthPages
		// 空库首次写锁还可能清除并重建日志；仅此受控 bootstrap 允许接纳 SQLite 新建的 inode。
		if err := c.reopenRecoveredJournal(true); err != nil {
			return err
		}
		if err := c.directory.Sync(); err != nil {
			return err
		}
	}
	if err := c.validateSQLite(); err != nil {
		return err
	}
	// 重开冻结在当前 N，先保留既有日志供维护使用，不能因降额破坏恢复空间。
	pages, err := c.sqliteInteger("PRAGMA page_count")
	if err != nil {
		return err
	}
	journal, err := c.journal.Stat()
	if err != nil {
		return err
	}
	if journal.Size() < sqliteJournalBytes(pages) {
		_, used, err := c.scanDirectory()
		if err != nil {
			return err
		}
		// 恢复删除了原日志时，允许恢复已在启动占用内计费的工作空间；降额不能因此阻断非增长维护。
		if used-allocated(journal)+sqliteJournalBytes(pages) > max(c.status.UsedBytes, c.status.BudgetBytes-auxiliaryReserve) {
			return errors.New("storage_error")
		}
		if err := c.resizeJournal(pages); err != nil {
			return err
		}
	}
	return c.setSQLitePageLimit(pages)
}

func (c *Collector) sqliteInteger(statement string) (int64, error) {
	var value int64
	err := c.connection.QueryRowContext(context.Background(), statement).Scan(&value)
	return value, err
}

func (c *Collector) validateSQLite() error {
	for statement, want := range map[string]int64{
		"PRAGMA application_id":     sqliteApplicationID,
		"PRAGMA user_version":       sqliteSchemaVersion,
		"PRAGMA page_size":          sqlitePageSize,
		"PRAGMA auto_vacuum":        2,
		"PRAGMA synchronous":        2,
		"PRAGMA cache_spill":        0,
		"PRAGMA temp_store":         2,
		"PRAGMA journal_size_limit": -1,
	} {
		got, err := c.sqliteInteger(statement)
		if err != nil || got != want {
			return errors.New("storage_error")
		}
	}
	var journalMode, integrity string
	if err := c.connection.QueryRowContext(context.Background(), "PRAGMA journal_mode").Scan(&journalMode); err != nil || journalMode != "persist" {
		return errors.New("storage_error")
	}
	rows, err := c.connection.QueryContext(context.Background(), "PRAGMA integrity_check")
	if err != nil {
		return err
	}
	count := 0
	for rows.Next() {
		if err := rows.Scan(&integrity); err != nil {
			rows.Close()
			return err
		}
		if integrity != "ok" {
			rows.Close()
			return errors.New("storage_error")
		}
		count++
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return err
	}
	if err := rows.Close(); err != nil {
		return err
	}
	if count != 1 {
		return errors.New("storage_error")
	}
	expected := map[string]string{"records": recordsSchema, "metadata": metadataSchema, "sqlite_sequence": "CREATE TABLE sqlite_sequence(name,seq)"}
	for _, statement := range sqliteIndexes {
		expected[strings.Fields(statement)[2]] = statement
	}
	rows, err = c.connection.QueryContext(context.Background(), "SELECT name,sql FROM sqlite_schema")
	if err != nil {
		return err
	}
	for rows.Next() {
		var name, statement string
		if err := rows.Scan(&name, &statement); err != nil {
			rows.Close()
			return err
		}
		if want, ok := expected[name]; !ok || statement != want {
			rows.Close()
			return errors.New("storage_error")
		}
		delete(expected, name)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return err
	}
	if err := rows.Close(); err != nil {
		return err
	}
	if len(expected) != 0 {
		return errors.New("storage_error")
	}
	digest := sha256.Sum256(c.callerScopeSeed)
	var storedDigest string
	if err := c.connection.QueryRowContext(context.Background(), "SELECT seed_digest FROM metadata WHERE singleton=1").Scan(&storedDigest); err != nil || storedDigest != hex.EncodeToString(digest[:]) {
		return errors.New("storage_error")
	}
	metadataCount, err := c.sqliteInteger("SELECT count(*) FROM metadata")
	if err != nil || metadataCount != 1 {
		return errors.New("storage_error")
	}
	rows, err = c.connection.QueryContext(context.Background(), "SELECT "+recordColumns+",received_sec,received_ns FROM records ORDER BY seq")
	if err != nil {
		return err
	}
	var saved int64
	for rows.Next() {
		var seconds, nanos int64
		if _, _, err := scanRecord(rows, &seconds, &nanos); err != nil {
			rows.Close()
			return err
		}
		saved++
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return err
	}
	if err := rows.Close(); err != nil {
		return err
	}
	c.mu.Lock()
	c.status.Saved = saved
	c.mu.Unlock()
	return nil
}

// verifySQLiteFiles 核对驱动实际使用的绝对路径和已打开句柄；os.Root 不会自动约束 SQLite 的文件打开。
func (c *Collector) verifySQLiteFiles() error {
	if err := c.verifySQLiteDirectory(); err != nil {
		return err
	}
	if err := c.verifySQLiteFile(databaseName, c.database); err != nil {
		return err
	}
	return c.verifySQLiteFile(databaseName+"-journal", c.journal)
}

func (c *Collector) verifySQLiteDirectory() error {
	current := string(filepath.Separator)
	for _, part := range strings.Split(strings.TrimPrefix(c.path, current), string(filepath.Separator)) {
		if part == "" {
			continue
		}
		current = filepath.Join(current, part)
		info, err := os.Lstat(current)
		if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return errors.New("storage_error")
		}
	}
	pathInfo, err := os.Lstat(c.path)
	if err != nil || pathInfo.Mode().Perm() != 0700 {
		return errors.New("storage_error")
	}
	opened, err := c.directory.Stat()
	if err != nil || !os.SameFile(pathInfo, opened) {
		return errors.New("storage_error")
	}
	return nil
}

func (c *Collector) verifySQLiteFile(name string, file *os.File) error {
	info, err := c.root.Lstat(name)
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm() != 0600 {
		return errors.New("storage_error")
	}
	if stat, ok := info.Sys().(*syscall.Stat_t); ok && stat.Nlink != 1 {
		return errors.New("storage_error")
	}
	opened, err := file.Stat()
	if err != nil || !os.SameFile(info, opened) {
		return errors.New("storage_error")
	}
	absolute, err := os.Lstat(filepath.Join(c.path, name))
	if err != nil || !os.SameFile(absolute, opened) {
		return errors.New("storage_error")
	}
	return nil
}

// reopenRecoveredJournal 仅在启动 SQLite 受控操作完成后重新取得日志；既有热日志绝不由应用提前改写。
// bootstrap 只允许新空库的建表提交接纳 SQLite 自己重建的普通文件，运行期不调用此函数。
func (c *Collector) reopenRecoveredJournal(bootstrap bool) error {
	if err := c.verifySQLiteDirectory(); err != nil {
		return err
	}
	if err := c.verifySQLiteFile(databaseName, c.database); err != nil {
		return err
	}
	current, err := c.root.Lstat(databaseName + "-journal")
	missing := errors.Is(err, os.ErrNotExist)
	if err != nil && !missing {
		return err
	}
	if !missing {
		if c.verifySQLiteFile(databaseName+"-journal", c.journal) == nil {
			return nil
		}
		if !bootstrap || !c.newStorage || !current.Mode().IsRegular() || current.Mode().Perm() != 0600 {
			return errors.New("storage_error")
		}
	}
	// 只有原日志已取消目录关联才接受 SQLite 的恢复清理；运行期的 inode 替换仍然拒绝。
	info, err := c.journal.Stat()
	if err != nil {
		return err
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || stat.Nlink != 0 {
		return errors.New("storage_error")
	}
	if err := c.journal.Close(); err != nil {
		return err
	}
	flags := os.O_RDWR | syscall.O_NOFOLLOW
	if missing {
		flags |= os.O_CREATE | os.O_EXCL
	}
	c.journal, err = c.root.OpenFile(databaseName+"-journal", flags, 0600)
	if err != nil {
		return err
	}
	if !missing {
		opened, err := c.journal.Stat()
		if err != nil || !os.SameFile(current, opened) {
			return errors.New("storage_error")
		}
	}
	if err := c.verifySQLiteFiles(); err != nil {
		return err
	}
	return c.directory.Sync()
}

// sqliteJournalBytes 的每页开销为页号4字节、4096字节正文和4字节校验；另留128KiB覆盖日志头与扇区填充。
// v1.14.52 amalgamation 的 pInJournal 位图保证原页面至多写一次；禁用 spill 后只有一个最大64KiB的日志头。
// 不使用 savepoint 或 ATTACH；128KiB另覆盖扇区填充，实际文件系统块数仍另行核算。
func sqliteJournalBytes(pages int64) int64 { return rounded((128 << 10) + pages*(sqlitePageSize+8)) }

// resizeJournal 只能在无事务且日志头失效时改尾部；逐块写零并 Sync，不能以稀疏 Truncate 冒充实际预分配。
func (c *Collector) resizeJournal(pages int64) error {
	if err := c.verifySQLiteFiles(); err != nil {
		return err
	}
	if c.connection != nil {
		if err := c.sqliteAutocommit(); err != nil {
			return err
		}
	}
	info, err := c.journal.Stat()
	if err != nil {
		return err
	}
	var header [28]byte
	n, err := c.journal.ReadAt(header[:], 0)
	if err != nil && !errors.Is(err, io.EOF) {
		return err
	}
	// PERSIST 提交将前28字节清零；其他失效头不由应用猜测或修复。
	if !bytes.Equal(header[:n], make([]byte, n)) {
		return errors.New("storage_error")
	}
	target := sqliteJournalBytes(pages)
	if info.Size() > target {
		if err := c.journal.Truncate(target); err != nil {
			return err
		}
	} else if info.Size() < target {
		var zero [64 << 10]byte
		for offset := info.Size(); offset < target; {
			amount := min(int64(len(zero)), target-offset)
			n, err := c.journal.WriteAt(zero[:amount], offset)
			if err != nil {
				return err
			}
			if int64(n) != amount {
				return io.ErrShortWrite
			}
			offset += int64(n)
		}
	}
	return c.journal.Sync()
}

func (c *Collector) sqliteAutocommit() error {
	return c.connection.Raw(func(driver any) error {
		if !driver.(*sqlite3.SQLiteConn).AutoCommit() {
			return errors.New("storage_error")
		}
		return nil
	})
}

func (c *Collector) rollbackSQLite(tx *sql.Tx, original error) error {
	// SQLITE_FULL 可能已自动回滚；仍须结束 database/sql 的 Tx，但不能把“没有事务”误报为回滚失败。
	alreadyRolledBack := c.sqliteAutocommit() == nil
	rollbackErr := tx.Rollback()
	if err := c.sqliteAutocommit(); err != nil {
		return err
	}
	if rollbackErr != nil && !errors.Is(rollbackErr, sql.ErrTxDone) && !alreadyRolledBack {
		return errors.New("storage_error")
	}
	return original
}

func (c *Collector) setSQLitePageLimit(pages int64) error {
	actual, err := c.sqliteInteger("PRAGMA max_page_count=" + formatInteger(pages))
	if err != nil || actual != pages {
		return errors.New("storage_error")
	}
	c.pageLimit = actual
	return nil
}

// sqliteWindowLimit 计算完整单次增长窗；不能区分页限 FULL 与 Unix VFS 的 ENOSPC，必须在 SQL 之前拒收。
func (c *Collector) sqliteWindowLimit() (int64, error) {
	if err := c.verifySQLiteFiles(); err != nil {
		return 0, err
	}
	pages, err := c.sqliteInteger("PRAGMA page_count")
	if err != nil {
		return 0, err
	}
	_, used, err := c.scanDirectory()
	if err != nil {
		return 0, err
	}
	dbInfo, err := c.database.Stat()
	if err != nil {
		return 0, err
	}
	journalInfo, err := c.journal.Stat()
	if err != nil {
		return 0, err
	}
	other := used - allocated(dbInfo) - allocated(journalInfo)
	// 实际块数超过逻辑页数的部分也需要计费。
	other += max(int64(0), allocated(dbInfo)-pages*sqlitePageSize)
	other += max(int64(0), allocated(journalInfo)-rounded(journalInfo.Size()))
	limit := pages + sqliteGrowthPages
	projected := other + limit*sqlitePageSize + sqliteJournalBytes(limit) + auxiliaryReserve
	c.mu.Lock()
	budget := c.status.BudgetBytes
	c.status.UsedBytes = used
	// 日志已经分配的部分包含在 used 中；只把尚未分配的页窗和配置空间列为预留。
	c.status.ReservedBytes = max(auxiliaryReserve, projected-used)
	c.status.ResumeRequiredBytes = c.status.ReservedBytes + capacityHysteresisBytes
	c.mu.Unlock()
	if projected > budget {
		return 0, errors.New("budget_exhausted")
	}
	return limit, nil
}

// sqliteWindow 先预分配足以容纳最大单条记录及各索引分裂的完整窗，再发布主库页限。
func (c *Collector) sqliteWindow() error {
	limit, err := c.sqliteWindowLimit()
	if err != nil {
		return err
	}
	if err := c.resizeJournal(limit); err != nil {
		return err
	}
	// 预分配后再次按实际块数计费，也更新 Status 使用的预留缓存。
	if _, err := c.sqliteWindowLimit(); err != nil {
		return err
	}
	return c.setSQLitePageLimit(limit)
}

// boundSQLitePages 在配置降额或外部文件增长后取消尚未使用的主库额度，既有日志保持不变。
func (c *Collector) boundSQLitePages() error {
	if err := c.verifySQLiteFiles(); err != nil {
		return err
	}
	_, used, err := c.scanDirectory()
	if err != nil {
		return err
	}
	info, err := c.database.Stat()
	if err != nil {
		return err
	}
	c.mu.Lock()
	budget := c.status.BudgetBytes
	c.mu.Unlock()
	if used+max(int64(0), c.pageLimit*sqlitePageSize-allocated(info))+auxiliaryReserve <= budget {
		return nil
	}
	pages, err := c.sqliteInteger("PRAGMA page_count")
	if err != nil {
		return err
	}
	return c.setSQLitePageLimit(pages)
}

func (c *Collector) appendRecord(record Record) error {
	if err := c.boundSQLitePages(); err != nil {
		return err
	}
	_, used, err := c.scanDirectory()
	if err != nil {
		return err
	}
	c.mu.Lock()
	budget := c.status.BudgetBytes
	c.status.UsedBytes = used
	c.mu.Unlock()
	if used+auxiliaryReserve > budget {
		return errors.New("budget_exhausted")
	}
	if err := c.sqliteWindow(); err != nil {
		return err
	}
	received, err := time.Parse(time.RFC3339Nano, record.ReceivedAt)
	if err != nil {
		return errors.New("storage_error")
	}
	var reasoning any
	if record.RequestReasoning != nil {
		raw, err := json.Marshal(record.RequestReasoning)
		if err != nil {
			return err
		}
		reasoning = string(raw)
	}
	tx, err := c.connection.BeginTx(context.Background(), nil)
	if err != nil {
		return err
	}
	_, err = tx.Exec(`INSERT INTO records(id,object_id,received_at,received_sec,received_ns,protocol,purpose,model,request_reasoning,text,truncated,extraction_incomplete) VALUES(?,?,?,?,?,?,?,?,?,?,?,?)`, record.ID, record.ObjectID, record.ReceivedAt, received.Unix(), received.Nanosecond(), record.Protocol, string(record.Purpose), record.Model, reasoning, record.Text, record.Truncated, record.ExtractionIncomplete)
	if err != nil {
		// Unix VFS 对 ENOSPC 也返回 FULL 且清除 errno；任何 SQL FULL 都保持真实存储故障，不再扩窗或改报预算。
		return c.rollbackSQLite(tx, err)
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	c.mu.Lock()
	c.status.Saved++
	c.mu.Unlock()
	// 提交已经成功，后续核验故障不能把这条记录再计为丢失。
	if err := c.verifySQLiteFiles(); err != nil {
		c.pauseStorage(err)
		return nil
	}
	if _, err := c.sqliteWindowLimit(); err != nil && err.Error() != "budget_exhausted" {
		c.pauseStorage(err)
	}
	return nil
}

const recordColumns = "seq,id,object_id,received_at,protocol,purpose,model,request_reasoning,text,truncated,extraction_incomplete"

type recordScanner interface{ Scan(...any) error }

// scanRecord 在数据库读取边界检查白名单及秒/纳秒一致性，不将损坏字段作为空记录公开。
func scanRecord(row recordScanner, timestamp ...*int64) (Record, int64, error) {
	var record Record
	var seq int64
	var reasoning sql.NullString
	var truncated, incomplete int
	values := []any{&seq, &record.ID, &record.ObjectID, &record.ReceivedAt, &record.Protocol, &record.Purpose, &record.Model, &reasoning, &record.Text, &truncated, &incomplete}
	for _, value := range timestamp {
		values = append(values, value)
	}
	if err := row.Scan(values...); err != nil {
		return record, 0, err
	}
	if seq <= 0 || !validEvent(Event{ID: record.ID, ObjectID: record.ObjectID, ReceivedAt: record.ReceivedAt, Protocol: record.Protocol}) || !supportedProtocol(record.Protocol) || len(record.Model) > 1024 || len(record.Text) > maxText || !utf8.ValidString(record.Text) || !utf8.ValidString(record.Model) || (record.Purpose != "" && record.Purpose != PurposeMain && record.Purpose != PurposeTurn && record.Purpose != PurposeUnknown) || (truncated != 0 && truncated != 1) || (incomplete != 0 && incomplete != 1) {
		return record, 0, errors.New("storage_error")
	}
	if len(timestamp) != 0 {
		received, _ := time.Parse(time.RFC3339Nano, record.ReceivedAt)
		if received.Unix() != *timestamp[0] || int64(received.Nanosecond()) != *timestamp[1] {
			return record, 0, errors.New("storage_error")
		}
	}
	if reasoning.Valid {
		var err error
		record.RequestReasoning, err = storedReasoning([]byte(reasoning.String))
		if err != nil {
			return record, 0, errors.New("storage_error")
		}
	}
	record.Truncated, record.ExtractionIncomplete = truncated == 1, incomplete == 1
	return record, seq, nil
}
