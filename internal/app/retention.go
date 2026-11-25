package app

import (
	"context"
	"errors"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

func validateRetention(setting Setting) error {
	for _, days := range []int{setting.RetentionDays, setting.RequestSummaryRetentionDays} {
		if days < 1 || days > 365 {
			return errors.New("历史保留天数应为 1–365 天")
		}
	}
	return nil
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
		 COALESCE(SUM(CASE WHEN ` + cacheReadSampleSQL + ` THEN 1 ELSE 0 END), 0) AS cache_sample_requests,
		 COALESCE(SUM(CASE WHEN ` + cacheReadSampleSQL + ` THEN input_tokens ELSE 0 END), 0) AS cache_sample_input_tokens,
		 COALESCE(SUM(CASE WHEN ` + cacheReadSampleSQL + ` THEN cache_read_tokens ELSE 0 END), 0) AS cache_sample_read_tokens,
		 COALESCE(SUM(CASE WHEN ` + cacheReadSampleSQL + ` AND cache_read_tokens > 0 THEN 1 ELSE 0 END), 0) AS cache_hit_requests,
		 COALESCE(SUM(CASE WHEN ` + cacheWriteSampleSQL + ` THEN 1 ELSE 0 END), 0) AS cache_write_sample_requests,
		 COALESCE(SUM(CASE WHEN cost_status = 'unknown' THEN 0 ELSE cost_micros END), 0) AS cost_micros,
		 COALESCE(SUM(CASE WHEN cost_status = 'unknown' OR state IN ('running', 'streaming') THEN 1 ELSE 0 END), 0) AS cost_unknown_requests`).
		Group("date(created_at, '+8 hours')").Order("date").Scan(&rows).Error
	return rows, err
}

func saveRequestDays(tx *gorm.DB, rows []RequestDay) error {
	if len(rows) == 0 {
		return nil
	}
	return tx.Clauses(clause.OnConflict{Columns: []clause.Column{{Name: "date"}}, DoUpdates: clause.AssignmentColumns(requestDayColumns)}).CreateInBatches(&rows, 100).Error
}

func (a *App) cleanupHistory(ctx context.Context, now time.Time) error {
	if err := a.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var setting Setting
		if err := tx.First(&setting, 1).Error; err != nil {
			return err
		}
		if err := validateRetention(setting); err != nil {
			return err
		}
		if err := trimRequestSummaries(tx, now, setting.RuntimeOptions); err != nil {
			return err
		}
		if err := tx.Where("date < ?", usageDay(now).AddDate(0, 0, -399).Format("2006-01-02")).Delete(&KeyUsageDay{}).Error; err != nil {
			return err
		}
		if err := tx.Where("created_at < ?", now.AddDate(0, 0, -30)).Delete(&CollectionEvent{}).Error; err != nil {
			return err
		}
		today := usageDay(now)
		summaryStart := today.AddDate(0, 0, 1-setting.RequestSummaryRetentionDays)
		if err := tx.Where("hour < ?", summaryStart.Unix()).Delete(&RequestHour{}).Error; err != nil {
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
	}); err != nil {
		return err
	}
	return a.reclaimDatabase(ctx)
}
