// 本文件用小事务删除到期记录并增量回收主库页；维护复用已计费回滚日志，不建立全库副本。
package audit

import (
	"context"
	"errors"
	"time"
)

// sweepExpired 在 ioMu 内删除到期记录并真实截短主库；降额时将页限固定在当前 N，不允许扩库。
func (c *Collector) sweepExpired(now time.Time) error {
	c.mu.Lock()
	usable := c.configValid && (c.status.State == "active" || c.status.Reason == "budget_exhausted")
	c.mu.Unlock()
	if !usable {
		return nil
	}
	if err := c.verifySQLiteFiles(); err != nil {
		return err
	}
	pages, err := c.sqliteInteger("PRAGMA page_count")
	if err != nil {
		return err
	}
	if err := c.setSQLitePageLimit(pages); err != nil {
		return err
	}
	journal, err := c.journal.Stat()
	if err != nil {
		return err
	}
	if journal.Size() < sqliteJournalBytes(pages) {
		return errors.New("storage_error")
	}
	where, args := c.visibilitySQL(now)
	for {
		tx, err := c.connection.BeginTx(context.Background(), nil)
		if err != nil {
			return err
		}
		// 每批32条限制一次删除的内存和事务时间；日志上界依全部原始页而非猜测脏页数计算。
		result, err := tx.Exec("DELETE FROM records WHERE seq IN (SELECT seq FROM records WHERE NOT ("+where+") ORDER BY seq LIMIT 32)", args...)
		if err != nil {
			return c.rollbackSQLite(tx, err)
		}
		deleted, err := result.RowsAffected()
		if err != nil {
			return c.rollbackSQLite(tx, err)
		}
		if err := tx.Commit(); err != nil {
			return err
		}
		c.mu.Lock()
		c.status.Saved -= deleted
		c.mu.Unlock()
		if err := c.reclaimSQLitePages(); err != nil {
			return err
		}
		if deleted == 0 {
			break
		}
	}
	_, used, err := c.scanDirectory()
	c.mu.Lock()
	c.status.UsedBytes = used
	c.mu.Unlock()
	return err
}

// reclaimSQLitePages 每批最多回收64页后提交并缩短日志，避免每页一次完整提交和Sync拖延降额维护。
func (c *Collector) reclaimSQLitePages() error {
	for {
		pages, err := c.sqliteInteger("PRAGMA page_count")
		if err != nil {
			return err
		}
		free, err := c.sqliteInteger("PRAGMA freelist_count")
		if err != nil {
			return err
		}
		if free == 0 {
			if err := c.resizeJournal(pages); err != nil {
				return err
			}
			return c.setSQLitePageLimit(pages)
		}
		if err := c.verifySQLiteFiles(); err != nil {
			return err
		}
		tx, err := c.connection.BeginTx(context.Background(), nil)
		if err != nil {
			return err
		}
		// 显式事务合并最多256KiB的回收；每条只处理一页，避免Exec未排空PRAGMA多行而只回收首个页面。
		for step := int64(0); step < min(free, 64); step++ {
			if _, err := tx.Exec("PRAGMA incremental_vacuum(1)"); err != nil {
				return c.rollbackSQLite(tx, err)
			}
		}
		if err := tx.Commit(); err != nil {
			return err
		}
		remaining, err := c.sqliteInteger("PRAGMA page_count")
		if err != nil {
			return err
		}
		if remaining >= pages {
			return errors.New("storage_error")
		}
		if err := c.resizeJournal(remaining); err != nil {
			return err
		}
		if err := c.setSQLitePageLimit(remaining); err != nil {
			return err
		}
		// 管理状态必须看到已提交批次的实际进度；预算仍不足也继续非增长回收，不提前恢复采集。
		if _, err := c.sqliteWindowLimit(); err != nil && err.Error() != "budget_exhausted" {
			return err
		}
	}
}
