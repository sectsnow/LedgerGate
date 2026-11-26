package app

import (
	"strconv"
	"time"

	"github.com/gin-gonic/gin"
)

type qualityRow struct {
	ID                       string   `json:"id"`
	Name                     string   `json:"name"`
	Requests                 int64    `json:"requests"`
	Successes                int64    `json:"successes"`
	InputTokens              int64    `json:"input_tokens"`
	OutputTokens             int64    `json:"output_tokens"`
	CacheReadTokens          int64    `json:"cache_read_tokens"`
	CacheWriteTokens         int64    `json:"cache_write_tokens"`
	CacheWrite1hTokens       int64    `json:"cache_write_1h_tokens"`
	CacheSampleRequests      int64    `json:"cache_sample_requests"`
	CacheSampleInputTokens   int64    `json:"cache_sample_input_tokens"`
	CacheSampleReadTokens    int64    `json:"cache_sample_read_tokens"`
	CacheHitRequests         int64    `json:"cache_hit_requests"`
	CacheWriteSampleRequests int64    `json:"cache_write_sample_requests"`
	CostMicros               int64    `json:"cost_micros"`
	UnknownCosts             int64    `json:"unknown_costs"`
	AverageDurationMS        *float64 `json:"average_duration_ms"`
	AverageFirstTokenMS      *float64 `json:"average_first_token_ms"`
	OutputPerSecond          *float64 `json:"output_per_second"`
	ObservedRequests         int64    `json:"observed_requests"`
	FirstAttemptSuccesses    int64    `json:"first_attempt_successes"`
	RetriedSuccesses         int64    `json:"retried_successes"`
	FailedRequests           int64    `json:"failed_requests"`
	AttemptOverheadMS        int64    `json:"attempt_overhead_ms"`
	LastRequestUnix          int64    `json:"last_request_unix"`
}

func (a *App) requestQuality(c *gin.Context) {
	now := time.Now()
	start, end := now.Truncate(time.Hour).Add(-23*time.Hour), now
	if c.Query("start") != "" || c.Query("end") != "" {
		location := usageDay(now).Location()
		var err error
		start, err = time.ParseInLocation("2006-01-02", c.Query("start"), location)
		if err != nil {
			fail(c, 400, "开始日期无效")
			return
		}
		end, err = time.ParseInLocation("2006-01-02", c.Query("end"), location)
		if err != nil || end.Before(start) || end.Sub(start) > 364*24*time.Hour {
			fail(c, 400, "日期范围须为 1–365 天")
			return
		}
		end = end.AddDate(0, 0, 1)
		if end.After(now) {
			end = now
		}
	}
	group, id, label := "channel_id", "CAST(channel_id AS TEXT)", "COALESCE(NULLIF(MAX(channel_name),''),'未选择渠道')"
	switch c.DefaultQuery("group_by", "channel") {
	case "channel":
	case "model":
		group, id, label = "model", "model", "COALESCE(NULLIF(model,''),'未指定模型')"
	default:
		fail(c, 400, "统计维度无效")
		return
	}
	rows := []qualityRow{}
	query := a.DB.WithContext(c.Request.Context()).Model(&RequestHour{}).
		Where("hour >= ? AND hour < ?", start.Unix()/3600*3600, end.Unix()).
		Select(id + " AS id, " + label + ` AS name, SUM(requests) AS requests,
            SUM(successes) AS successes,
            COALESCE(SUM(input_tokens),0) AS input_tokens, COALESCE(SUM(output_tokens),0) AS output_tokens,
			COALESCE(SUM(cache_read_tokens),0) AS cache_read_tokens, COALESCE(SUM(cache_write_tokens),0) AS cache_write_tokens, COALESCE(SUM(cache_write1h_tokens),0) AS cache_write1h_tokens,
			COALESCE(SUM(cache_sample_requests),0) AS cache_sample_requests, COALESCE(SUM(cache_sample_input_tokens),0) AS cache_sample_input_tokens, COALESCE(SUM(cache_sample_read_tokens),0) AS cache_sample_read_tokens,
			COALESCE(SUM(cache_hit_requests),0) AS cache_hit_requests, COALESCE(SUM(cache_write_sample_requests),0) AS cache_write_sample_requests,
			COALESCE(SUM(cost_micros),0) AS cost_micros, SUM(cost_unknown_requests) AS unknown_costs,
			1.0*SUM(duration_ms)/NULLIF(SUM(requests),0) AS average_duration_ms,
			1.0*SUM(semantic_first_token_ms)/NULLIF(SUM(semantic_first_token_count),0) AS average_first_token_ms,
			1000.0*NULLIF(SUM(output_tokens),0)/NULLIF(SUM(duration_ms),0) AS output_per_second,
            SUM(observed_requests) AS observed_requests, SUM(first_attempt_successes) AS first_attempt_successes, SUM(retried_successes) AS retried_successes, SUM(failed_requests) AS failed_requests, SUM(attempt_overhead_ms) AS attempt_overhead_ms, MAX(last_request_unix) AS last_request_unix`).
		Group(group).Order("requests DESC").Limit(500)
	if configured := c.Query("configured"); configured != "" {
		if configured != "1" || group != "model" {
			fail(c, 400, "模型范围无效")
			return
		}
		query = query.Where("model IN (?)", a.DB.Model(&Group{}).Select("name")).
			Order("last_request_unix DESC").Order("(SELECT id FROM `groups` WHERE `groups`.name = request_hours.model) ASC").Limit(5)
	}
	model := c.Query("model")
	if len(model) > 200 {
		fail(c, 400, "模型筛选过长")
		return
	}
	if model != "" {
		query = query.Where("model = ?", model)
	}
	models := []string{}
	if err := a.DB.Model(&RequestHour{}).Where("hour >= ? AND hour < ?", start.Unix()/3600*3600, end.Unix()).Distinct("model").Order("model").Limit(500).Pluck("model", &models).Error; err != nil {
		fail(c, 500, "读取模型筛选失败")
		return
	}
	if err := query.Scan(&rows).Error; err != nil {
		fail(c, 500, "读取渠道与模型统计失败")
		return
	}
	result := object{"items": rows, "start": start, "end": end, "models": models}
	if detail := c.Query("detail"); detail != "" || c.Query("include_insights") == "1" {
		var channel *uint
		var selectedModel *string
		if model != "" {
			selectedModel = &model
		}
		if group == "channel_id" {
			id, err := strconv.ParseUint(detail, 10, 32)
			if err != nil {
				fail(c, 400, "渠道编号无效")
				return
			}
			value := uint(id)
			channel = &value
		} else {
			if len(detail) > 200 {
				fail(c, 400, "模型筛选过长")
				return
			}
			selectedModel = &detail
		}
		report, err := loadRequestInsights(a.DB.WithContext(c.Request.Context()), start, end, channel, selectedModel)
		if err != nil {
			fail(c, 500, "读取详细统计失败")
			return
		}
		result["insights"] = report
	}
	c.JSON(200, result)
}
