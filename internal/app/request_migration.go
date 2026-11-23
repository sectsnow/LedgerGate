package app

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"

	"gorm.io/gorm"
)

func (a *App) archiveRequestHistory() (string, error) {
	parent := filepath.Join(a.dataDir, "backups")
	if err := os.MkdirAll(parent, 0700); err != nil {
		return "", err
	}
	dir, err := os.MkdirTemp(parent, "request-history-")
	if err != nil {
		return "", err
	}
	complete := false
	defer func() {
		if !complete {
			os.RemoveAll(dir)
		}
	}()
	if err := a.DB.Exec("VACUUM INTO ?", filepath.Join(dir, "ledgergate.db")).Error; err != nil {
		return "", err
	}
	if err := os.Chmod(filepath.Join(dir, "ledgergate.db"), 0600); err != nil {
		return "", err
	}
	database, err := os.OpenFile(filepath.Join(dir, "ledgergate.db"), os.O_RDWR, 0600)
	if err != nil {
		return "", err
	}
	err = database.Sync()
	if err = errors.Join(err, database.Close()); err != nil {
		return "", err
	}
	for _, name := range []string{"master.key", "admin-password"} {
		data, err := os.ReadFile(filepath.Join(a.dataDir, name))
		if name == "admin-password" && a.adminPassword != "" {
			data, err = []byte(a.adminPassword+"\n"), nil
		}
		if err != nil {
			return "", err
		}
		if err := saveAdminPassword(filepath.Join(dir, name), string(data)); err != nil {
			return "", err
		}
	}
	complete = true
	return dir, nil
}

func (a *App) migrateRequestHistory() error {
	var setting Setting
	if err := a.DB.First(&setting, 1).Error; err != nil {
		return err
	}
	if setting.RequestHistoryMigrated {
		return nil
	}
	var requests, details int64
	if err := a.DB.Model(&RequestLog{}).Count(&requests).Error; err != nil {
		return err
	}
	if err := a.DB.Model(&RequestDetail{}).Count(&details).Error; err != nil {
		return err
	}
	archive := ""
	if requests > 0 || details > 0 {
		var err error
		archive, err = a.archiveRequestHistory()
		if err != nil {
			return fmt.Errorf("旧请求数据归档失败，尚未删除请求和正文；请检查数据目录权限及剩余空间：%w", err)
		}
	}
	err := a.DB.Transaction(func(tx *gorm.DB) error {
		// 旧清理按整天删除原记录，存在原记录的日期可安全覆盖旧汇总。
		rows, err := aggregateRequestDays(tx.Model(&RequestLog{}))
		if err != nil {
			return err
		}
		if err := saveRequestDays(tx, rows); err != nil {
			return err
		}
		if err := tx.Exec(`INSERT INTO request_hours
			(hour, channel_id, model, channel_name, requests, errors, successes, input_tokens, output_tokens,
			 cost_micros, cost_unknown_requests, duration_ms, first_token_ms, first_token_count,
			 semantic_first_token_ms, semantic_first_token_count, generation_ms, generation_tokens)
			SELECT (CAST(strftime('%s', created_at) AS INTEGER) / 3600) * 3600,
			 channel_id, model, COALESCE(MAX(channel_name), ''), COUNT(*),
			 SUM(CASE WHEN status >= 400 THEN 1 ELSE 0 END), SUM(CASE WHEN status >= 200 AND status < 400 THEN 1 ELSE 0 END),
			 COALESCE(SUM(input_tokens), 0), COALESCE(SUM(output_tokens), 0),
			 COALESCE(SUM(CASE WHEN cost_status = 'unknown' THEN 0 ELSE cost_micros END), 0),
			 SUM(CASE WHEN cost_status = 'unknown' THEN 1 ELSE 0 END), COALESCE(SUM(duration_ms), 0),
			 COALESCE(SUM(first_token_ms), 0), COUNT(first_token_ms),
			 COALESCE(SUM(CASE WHEN metrics_version >= 2 THEN first_token_ms ELSE 0 END), 0),
			 SUM(CASE WHEN metrics_version >= 2 AND first_token_ms IS NOT NULL THEN 1 ELSE 0 END),
			 COALESCE(SUM(CASE WHEN generation_ms > 0 THEN generation_ms ELSE 0 END), 0),
			 COALESCE(SUM(CASE WHEN generation_ms > 0 THEN output_tokens ELSE 0 END), 0)
			FROM request_logs GROUP BY 1, channel_id, model`).Error; err != nil {
			return err
		}
		for _, table := range []any{&RequestDetail{}, &RequestLog{}} {
			if err := tx.Where("1 = 1").Delete(table).Error; err != nil {
				return err
			}
		}
		return tx.Model(&Setting{}).Where("id = 1").Update("request_history_migrated", true).Error
	})
	if err != nil {
		return fmt.Errorf("请求数据迁移失败，数据库事务已回滚：%w", err)
	}
	if archive != "" {
		slog.Info("旧请求详情已归档，在线数据库仅保留用量汇总", "archive", archive, "requests", requests, "details", details)
		if err := a.reclaimDatabase(context.Background()); err != nil {
			slog.Warn("升级后空间回收暂未完成，将在定时维护时重试", "error", err)
		}
	}
	return nil
}
