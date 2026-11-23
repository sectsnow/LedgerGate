package app

import (
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

var requestDayColumns = []string{"requests", "errors", "input_tokens", "output_tokens", "cache_read_tokens", "cache_write_tokens", "cache_write1h_tokens", "cost_micros", "cost_unknown_requests"}
var requestHourColumns = []string{"requests", "errors", "successes", "input_tokens", "output_tokens", "cost_micros", "cost_unknown_requests", "duration_ms", "first_token_ms", "first_token_count", "semantic_first_token_ms", "semantic_first_token_count", "generation_ms", "generation_tokens"}

func incrementSummary(tx *gorm.DB, row any, table string, keys []clause.Column, columns []string) error {
	updates := map[string]any{}
	for _, column := range columns {
		updates[column] = gorm.Expr(table + "." + column + " + excluded." + column)
	}
	return tx.Clauses(clause.OnConflict{Columns: keys, DoUpdates: clause.Assignments(updates)}).Create(row).Error
}

func recordRequestUsage(tx *gorm.DB, log *RequestLog) error {
	day := RequestDay{Date: usageDay(log.CreatedAt).Format("2006-01-02"), Requests: 1,
		InputTokens: log.InputTokens, OutputTokens: log.OutputTokens, CacheReadTokens: log.CacheReadTokens,
		CacheWriteTokens: log.CacheWriteTokens, CacheWrite1hTokens: log.CacheWrite1hTokens, CostMicros: log.CostMicros}
	if log.Status >= 400 {
		day.Errors = 1
	}
	if log.CostStatus == "unknown" {
		day.CostMicros, day.CostUnknownRequests = 0, 1
	}
	if err := incrementSummary(tx, &day, "request_days", []clause.Column{{Name: "date"}}, requestDayColumns); err != nil {
		return err
	}
	hour := RequestHour{Hour: log.CreatedAt.Unix() / 3600 * 3600, ChannelID: log.ChannelID, ChannelName: log.ChannelName,
		Model: log.Model, Requests: 1, Errors: day.Errors, InputTokens: day.InputTokens, OutputTokens: day.OutputTokens,
		CostMicros: day.CostMicros, CostUnknownRequests: day.CostUnknownRequests, DurationMS: log.DurationMS}
	if log.Status >= 200 && log.Status < 400 {
		hour.Successes = 1
	}
	if log.FirstTokenMS != nil {
		hour.FirstTokenMS, hour.FirstTokenCount = *log.FirstTokenMS, 1
		if log.MetricsVersion >= 2 {
			hour.SemanticFirstTokenMS, hour.SemanticFirstTokenCount = *log.FirstTokenMS, 1
		}
	}
	if log.GenerationMS != nil && *log.GenerationMS > 0 {
		hour.GenerationMS, hour.GenerationTokens = *log.GenerationMS, log.OutputTokens
	}
	return incrementSummary(tx, &hour, "request_hours", []clause.Column{{Name: "hour"}, {Name: "channel_id"}, {Name: "model"}}, requestHourColumns)
}
