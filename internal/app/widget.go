package app

import (
	"crypto/subtle"
	"errors"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type widgetCredential struct {
	ID     uint   `gorm:"primaryKey"`
	Digest string `gorm:"not null"`
}

func (a *App) widgetCredentialStatus(c *gin.Context) {
	var count int64
	if err := a.DB.Model(&widgetCredential{}).Where("id = ?", 1).Count(&count).Error; err != nil {
		fail(c, 500, "读取小部件凭证失败")
		return
	}
	c.JSON(200, object{"configured": count > 0})
}

func (a *App) createWidgetCredential(c *gin.Context) {
	token := "lgw_" + randomToken()
	credential := widgetCredential{ID: 1, Digest: digest(token)}
	if err := a.DB.Clauses(clause.OnConflict{Columns: []clause.Column{{Name: "id"}}, DoUpdates: clause.AssignmentColumns([]string{"digest"})}).Create(&credential).Error; err != nil {
		fail(c, 500, "保存小部件凭证失败")
		return
	}
	c.JSON(200, object{"token": token})
}

func (a *App) revokeWidgetCredential(c *gin.Context) {
	if err := a.DB.Delete(&widgetCredential{}, 1).Error; err != nil {
		fail(c, 500, "撤销小部件凭证失败")
		return
	}
	c.Status(204)
}

type widgetAccount struct {
	ID               uint       `json:"id"`
	Name             string     `json:"name"`
	Currency         string     `json:"currency"`
	Balance          *float64   `json:"balance"`
	TodayUsed        *float64   `json:"today_used"`
	TodayDate        string     `json:"today_date"`
	Status           string     `json:"status"`
	Enabled          bool       `json:"enabled"`
	CheckedAt        *time.Time `json:"checked_at"`
	TodayCollectedAt *time.Time `json:"today_collected_at"`
	IntervalSeconds  int        `json:"interval_seconds"`
	CollectionNotes  []string   `json:"collection_notes" gorm:"-"`
	Warnings         string     `json:"-"`
	HasWarning       bool       `json:"has_warning" gorm:"-"`
}

type widgetRank struct {
	Name     string `json:"name"`
	Requests int64  `json:"requests"`
	Tokens   int64  `json:"tokens"`
}

type widgetTrend struct {
	Date                string `json:"date"`
	Available           bool   `json:"available"`
	Requests            int64  `json:"requests"`
	Errors              int64  `json:"errors"`
	InputTokens         int64  `json:"input_tokens"`
	OutputTokens        int64  `json:"output_tokens"`
	CostMicros          int64  `json:"cost_micros"`
	CostUnknownRequests int64  `json:"cost_unknown_requests"`
}

type widgetPeriod struct {
	Days                int           `json:"days"`
	RetentionDays       int           `json:"retention_days"`
	Requests            int64         `json:"requests"`
	Errors              int64         `json:"errors"`
	InputTokens         int64         `json:"input_tokens"`
	OutputTokens        int64         `json:"output_tokens"`
	CacheReadTokens     int64         `json:"cache_read_tokens"`
	CacheWriteTokens    int64         `json:"cache_write_tokens"`
	CostMicros          int64         `json:"cost_micros"`
	CostUnknownRequests int64         `json:"cost_unknown_requests"`
	Trend               []widgetTrend `json:"trend" gorm:"-"`
	Models              []widgetRank  `json:"models" gorm:"-"`
	Channels            []widgetRank  `json:"channels" gorm:"-"`
}

func (a *App) widgetOverview(c *gin.Context) {
	value := c.GetHeader("Authorization")
	if !strings.HasPrefix(value, "Bearer lgw_") || len(value) > 100 {
		fail(c, 401, "小部件凭证无效")
		return
	}
	var credential widgetCredential
	err := a.DB.First(&credential, 1).Error
	if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
		fail(c, 500, "读取小部件凭证失败")
		return
	}
	if err != nil || subtle.ConstantTimeCompare([]byte(credential.Digest), []byte(digest(strings.TrimPrefix(value, "Bearer ")))) != 1 {
		fail(c, 401, "小部件凭证无效或已撤销")
		return
	}
	days := 1
	switch c.DefaultQuery("days", "1") {
	case "1":
	case "7":
		days = 7
	case "30":
		days = 30
	default:
		fail(c, 400, "统计周期须为 1、7 或 30 天")
		return
	}
	todayStart := usageDay(time.Now())
	today := todayStart.Format("2006-01-02")
	start := todayStart.AddDate(0, 0, 1-days)
	accounts := []widgetAccount{}
	day := RequestDay{Date: today}
	var period widgetPeriod
	err = a.DB.Transaction(func(tx *gorm.DB) error {
		if err := tx.Model(&Account{}).Select("id", "name", "currency", "balance", "today_used", "today_date", "today_collected_at", "interval_seconds", "status", "enabled", "checked_at", "warnings").Order("id").Scan(&accounts).Error; err != nil {
			return err
		}
		if err := tx.First(&day, "date = ?", today).Error; err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
			return err
		}
		var setting Setting
		if err := tx.Select("request_summary_retention_days").First(&setting, 1).Error; err != nil {
			return err
		}
		if err := tx.Model(&RequestDay{}).Where("date >= ? AND date <= ?", start.Format("2006-01-02"), today).
			Select(`COALESCE(SUM(requests),0) AS requests, COALESCE(SUM(errors),0) AS errors,
				COALESCE(SUM(input_tokens),0) AS input_tokens, COALESCE(SUM(output_tokens),0) AS output_tokens,
				COALESCE(SUM(cache_read_tokens),0) AS cache_read_tokens, COALESCE(SUM(cache_write_tokens),0) AS cache_write_tokens,
				COALESCE(SUM(cost_micros),0) AS cost_micros, COALESCE(SUM(cost_unknown_requests),0) AS cost_unknown_requests`).Scan(&period).Error; err != nil {
			return err
		}
		period.Days, period.RetentionDays = days, setting.RequestSummaryRetentionDays
		storedTrend := []widgetTrend{}
		if err := tx.Model(&RequestDay{}).Where("date >= ? AND date <= ?", start.Format("2006-01-02"), today).
			Select("date, requests, errors, input_tokens, output_tokens, cost_micros, cost_unknown_requests").Order("date").Scan(&storedTrend).Error; err != nil {
			return err
		}
		trendByDate := make(map[string]widgetTrend, len(storedTrend))
		for _, row := range storedTrend {
			row.Available = true
			trendByDate[row.Date] = row
		}
		period.Trend = make([]widgetTrend, 0, days)
		for offset := days - 1; offset >= 0; offset-- {
			date := todayStart.AddDate(0, 0, -offset).Format("2006-01-02")
			row, ok := trendByDate[date]
			if !ok {
				row.Date = date
			}
			period.Trend = append(period.Trend, row)
		}
		period.Models, period.Channels = []widgetRank{}, []widgetRank{}
		ranking := tx.Model(&RequestHour{}).Where("hour >= ? AND hour < ?", start.Unix(), todayStart.AddDate(0, 0, 1).Unix())
		if err := ranking.Select("COALESCE(NULLIF(model,''),'未指定模型') AS name, SUM(requests) AS requests, SUM(input_tokens + output_tokens) AS tokens").
			Group("model").Order("tokens DESC, requests DESC, name ASC").Limit(5).Scan(&period.Models).Error; err != nil {
			return err
		}
		if err := tx.Model(&RequestHour{}).Where("hour >= ? AND hour < ?", start.Unix(), todayStart.AddDate(0, 0, 1).Unix()).
			Select("COALESCE(NULLIF(MAX(channel_name),''),'未选择渠道') AS name, SUM(requests) AS requests, SUM(input_tokens + output_tokens) AS tokens").
			Group("channel_id").Order("tokens DESC, requests DESC, name ASC").Limit(5).Scan(&period.Channels).Error; err != nil {
			return err
		}
		return nil
	})
	if err != nil {
		fail(c, 500, "读取小部件统计失败")
		return
	}
	for i := range accounts {
		accounts[i].HasWarning = strings.TrimSpace(accounts[i].Warnings) != ""
		// 只返回固定分类，不把可能包含上游响应或凭据的原始错误下发小部件。
		accounts[i].CollectionNotes = []string{}
		for _, field := range []string{"今日消费", "累计消费", "倍率", "订阅", "公告"} {
			if strings.Contains(accounts[i].Warnings, field) {
				accounts[i].CollectionNotes = append(accounts[i].CollectionNotes, field+"采集提醒")
			}
		}
		if accounts[i].TodayDate != today {
			accounts[i].TodayUsed = nil
			accounts[i].TodayCollectedAt = nil
		}
	}
	c.JSON(200, object{"date": today, "accounts": accounts, "requests": day.Requests, "input_tokens": day.InputTokens, "output_tokens": day.OutputTokens, "period": period})
}
