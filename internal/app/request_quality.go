package app

import (
	"time"

	"github.com/gin-gonic/gin"
)

type qualityRow struct {
	ID                  string   `json:"id"`
	Name                string   `json:"name"`
	Requests            int64    `json:"requests"`
	Successes           int64    `json:"successes"`
	InputTokens         int64    `json:"input_tokens"`
	OutputTokens        int64    `json:"output_tokens"`
	CostMicros          int64    `json:"cost_micros"`
	UnknownCosts        int64    `json:"unknown_costs"`
	AverageDurationMS   *float64 `json:"average_duration_ms"`
	AverageFirstTokenMS *float64 `json:"average_first_token_ms"`
	OutputPerSecond     *float64 `json:"output_per_second"`
}

func (a *App) requestQuality(c *gin.Context) {
	now := time.Now()
	start, end := now.Add(-24*time.Hour), now
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
	query := a.DB.WithContext(c.Request.Context()).Model(&RequestLog{}).
		Where("julianday(created_at)>=julianday(?) AND julianday(created_at)<julianday(?) AND state NOT IN ('running','streaming')", start, end).
		Select(id + " AS id, " + label + ` AS name, COUNT(*) AS requests,
            SUM(CASE WHEN status>=200 AND status<400 THEN 1 ELSE 0 END) AS successes,
            COALESCE(SUM(input_tokens),0) AS input_tokens, COALESCE(SUM(output_tokens),0) AS output_tokens,
            COALESCE(SUM(CASE WHEN cost_status='unknown' THEN 0 ELSE cost_micros END),0) AS cost_micros,
            SUM(CASE WHEN cost_status='unknown' THEN 1 ELSE 0 END) AS unknown_costs,
            AVG(duration_ms) AS average_duration_ms, AVG(first_token_ms) AS average_first_token_ms,
            1000.0*SUM(CASE WHEN generation_ms>0 THEN output_tokens ELSE 0 END)/NULLIF(SUM(CASE WHEN generation_ms>0 THEN generation_ms ELSE 0 END),0) AS output_per_second`).
		Group(group).Order("requests DESC").Limit(500)
	if err := query.Scan(&rows).Error; err != nil {
		fail(c, 500, "读取渠道与模型统计失败")
		return
	}
	c.JSON(200, object{"items": rows, "start": start, "end": end})
}
