package app

import (
	"gorm.io/gorm"
)

func recordRoutingQuality(tx *gorm.DB, log *RequestLog, payload requestPayload) error {
	if log.Attempts == 0 {
		return nil
	}
	values := map[string]any{"observed_requests": gorm.Expr("observed_requests + 1"), "last_request_unix": gorm.Expr("MAX(COALESCE(last_request_unix,0), ?)", log.CreatedAt.Unix())}
	if log.Status >= 200 && log.Status < 400 {
		column := "first_attempt_successes"
		if log.Attempts > 1 {
			column = "retried_successes"
		}
		values[column] = gorm.Expr(column + " + 1")
	} else {
		values["failed_requests"] = gorm.Expr("failed_requests + 1")
	}
	if len(payload.Attempts) > 1 {
		overhead := max(0, payload.Attempts[len(payload.Attempts)-1].StartedAt.Sub(log.CreatedAt).Milliseconds())
		values["attempt_overhead_ms"] = gorm.Expr("attempt_overhead_ms + ?", overhead)
	}
	return tx.Model(&RequestHour{}).Where("hour = ? AND channel_id = ? AND model = ?", log.CreatedAt.Unix()/3600*3600, log.ChannelID, log.Model).Updates(values).Error
}
