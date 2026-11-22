package app

import (
	"context"
	"errors"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

func validateRetention(setting Setting) error {
	for _, days := range []int{setting.RetentionDays, setting.RequestBodyRetentionDays, setting.RequestRetentionDays, setting.RequestSummaryRetentionDays} {
		if days < 1 || days > 365 {
			return errors.New("历史保留天数应为 1–365 天")
		}
	}
	if setting.RequestBodyRetentionDays > setting.RequestRetentionDays || setting.RequestRetentionDays > setting.RequestSummaryRetentionDays {
		return errors.New("正文保留天数不能超过请求统计，汇总保留天数不能少于请求统计")
	}
	return nil
}

func requestDays(db *gorm.DB, start, now time.Time) ([]RequestDay, error) {
	return aggregateRequestDays(db.Model(&RequestLog{}).
		Where("julianday(created_at) >= julianday(?) AND julianday(created_at) <= julianday(?)", start, now))
}

func aggregateRequestDays(query *gorm.DB) ([]RequestDay, error) {
	rows := []RequestDay{}
	err := query.
		Select(`date(created_at, '+8 hours') AS date, COUNT(*) AS requests,
		 COALESCE(SUM(CASE WHEN status >= 400 THEN 1 ELSE 0 END), 0) AS errors,
		 COALESCE(SUM(input_tokens), 0) AS input_tokens, COALESCE(SUM(output_tokens), 0) AS output_tokens,
		 COALESCE(SUM(cache_read_tokens), 0) AS cache_read_tokens,
		 COALESCE(SUM(cache_write_tokens), 0) AS cache_write_tokens,
		 COALESCE(SUM(cache_write1h_tokens), 0) AS cache_write1h_tokens,
		 COALESCE(SUM(CASE WHEN cost_status = 'unknown' THEN 0 ELSE cost_micros END), 0) AS cost_micros,
		 COALESCE(SUM(CASE WHEN cost_status = 'unknown' OR state IN ('running', 'streaming') THEN 1 ELSE 0 END), 0) AS cost_unknown_requests`).
		Group("date(created_at, '+8 hours')").Order("date").Scan(&rows).Error
	return rows, err
}

func saveRequestDays(tx *gorm.DB, rows []RequestDay) error {
	if len(rows) == 0 {
		return nil
	}
	updates := []string{"requests", "errors", "input_tokens", "output_tokens", "cache_read_tokens", "cache_write_tokens", "cache_write1h_tokens", "cost_micros", "cost_unknown_requests"}
	return tx.Clauses(clause.OnConflict{Columns: []clause.Column{{Name: "date"}}, DoUpdates: clause.AssignmentColumns(updates)}).CreateInBatches(&rows, 100).Error
}

func (a *App) cleanupHistory(ctx context.Context, now time.Time) error {
	return a.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var setting Setting
		if err := tx.First(&setting, 1).Error; err != nil {
			return err
		}
		if err := validateRetention(setting); err != nil {
			return err
		}
		today := usageDay(now)
		summaryStart := today.AddDate(0, 0, 1-setting.RequestSummaryRetentionDays)
		rows, err := requestDays(tx, summaryStart, now)
		if err != nil {
			return err
		}
		// 先按整天覆盖汇总，再删除原记录，重复清理不会重复累计。
		if err := saveRequestDays(tx, rows); err != nil {
			return err
		}
		bodyStart := today.AddDate(0, 0, 1-setting.RequestBodyRetentionDays)
		bodyIDs := tx.Model(&RequestLog{}).Select("id").Where("julianday(created_at) < julianday(?) AND state NOT IN ('running', 'streaming')", bodyStart)
		if err := tx.Where("request_log_id IN (?)", bodyIDs).Delete(&RequestDetail{}).Error; err != nil {
			return err
		}
		requestStart := today.AddDate(0, 0, 1-setting.RequestRetentionDays)
		// 同一天尚有进行中的请求时保留整天，避免只剩部分原记录后覆盖完整汇总。
		activeDates := tx.Model(&RequestLog{}).Select("date(created_at, '+8 hours')").Where("state IN ('running', 'streaming') AND julianday(created_at) IS NOT NULL")
		if err := tx.Where("julianday(created_at) < julianday(?) AND date(created_at, '+8 hours') NOT IN (?)", requestStart, activeDates).Delete(&RequestLog{}).Error; err != nil {
			return err
		}
		if err := tx.Where("date < ?", summaryStart.Format("2006-01-02")).Delete(&RequestDay{}).Error; err != nil {
			return err
		}
		legacyStart := now.Add(-time.Duration(setting.RetentionDays) * 24 * time.Hour)
		if err := tx.Where("julianday(created_at) < julianday(?)", legacyStart).Delete(&NotificationDelivery{}).Error; err != nil {
			return err
		}
		for _, table := range []any{&Snapshot{}, &Alert{}} {
			if err := tx.Where("julianday(created_at) < julianday(?)", legacyStart).Delete(table).Error; err != nil {
				return err
			}
		}
		return tx.Where("expires_at < ?", now).Delete(&Session{}).Error
	})
}
