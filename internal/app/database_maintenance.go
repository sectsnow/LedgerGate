package app

import (
	"context"
	"errors"
	"fmt"
	"time"
)

func (a *App) reclaimDatabase(parent context.Context) error {
	a.mu.Lock()
	busy := len(a.activeRequests) > 0 || len(a.monitoring) > 0 || a.modelTests > 0
	a.mu.Unlock()
	if busy || !a.backupMu.TryLock() {
		return nil
	}
	defer a.backupMu.Unlock()
	ctx, cancel := context.WithTimeout(parent, 30*time.Second)
	defer cancel()
	db := a.DB.WithContext(ctx)
	var pages, free, pageSize, mode int64
	for _, query := range []struct {
		name  string
		value *int64
	}{
		{"page_count", &pages}, {"freelist_count", &free}, {"page_size", &pageSize}, {"auto_vacuum", &mode},
	} {
		if err := db.Raw("PRAGMA " + query.name).Scan(query.value).Error; err != nil {
			return err
		}
	}
	// 大量空页才整理旧库，避免每次清理都重写仍在使用的数据库。
	if free*pageSize < 8<<20 || free*5 < pages {
		return nil
	}
	if mode != 2 {
		if err := db.Exec("PRAGMA auto_vacuum=INCREMENTAL").Error; err != nil {
			return err
		}
		if err := db.Exec("VACUUM").Error; err != nil {
			return fmt.Errorf("数据库空间回收失败，原数据保留：%w", err)
		}
	} else {
		// 此 PRAGMA 每释放一页返回一行，必须读到结束才能完成空间回收。
		rows, err := db.Raw("PRAGMA incremental_vacuum").Rows()
		if err != nil {
			return err
		}
		for rows.Next() {
		}
		readErr, closeErr := rows.Err(), rows.Close()
		if err := errors.Join(readErr, closeErr); err != nil {
			return err
		}
	}
	return db.Exec("PRAGMA wal_checkpoint(TRUNCATE)").Error
}
