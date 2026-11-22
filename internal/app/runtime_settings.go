package app

import (
	"errors"
	"math"
)

type RuntimeOptions struct {
	FirstOutputSeconds      int      `json:"first_output_seconds" gorm:"not null;default:60"`
	StreamIdleSeconds       int      `json:"stream_idle_seconds" gorm:"not null;default:120"`
	RequestTimeoutSeconds   int      `json:"request_timeout_seconds" gorm:"not null;default:600"`
	MaxAttempts             int      `json:"max_attempts" gorm:"not null;default:8"`
	MonitorConcurrency      int      `json:"monitor_concurrency" gorm:"not null;default:4"`
	NotificationType        string   `json:"notification_type" gorm:"not null;default:webhook"`
	SubscriptionLowPercent  float64  `json:"subscription_low_percent" gorm:"not null;default:10"`
	SubscriptionExpiryHours int      `json:"subscription_expiry_hours" gorm:"not null;default:72"`
	RateChangePercent       float64  `json:"rate_change_percent" gorm:"not null;default:0"`
	NotificationRetries     int      `json:"notification_retries" gorm:"not null;default:10"`
	NotificationEvents      []string `json:"notification_events" gorm:"serializer:json;type:text"`
	NotificationAccounts    []uint   `json:"notification_accounts" gorm:"serializer:json;type:text"`
	PriceSyncHours          int      `json:"price_sync_hours" gorm:"not null;default:0"`
	ModelSyncHours          int      `json:"model_sync_hours" gorm:"not null;default:12"`
}

func (options RuntimeOptions) validate() error {
	if options.FirstOutputSeconds < 1 || options.FirstOutputSeconds > 3600 || options.StreamIdleSeconds < 1 || options.StreamIdleSeconds > 3600 || options.RequestTimeoutSeconds < 1 || options.RequestTimeoutSeconds > 10800 || options.MaxAttempts < 1 || options.MaxAttempts > 32 || options.MonitorConcurrency < 1 || options.MonitorConcurrency > 8 {
		return errors.New("首输出和流空闲超时须为 1–3600 秒，总超时为 1–10800 秒，最多尝试 1–32 次，采集并发 1–8")
	}
	if options.NotificationType != "webhook" && options.NotificationType != "feishu" && options.NotificationType != "telegram" {
		return errors.New("通知方式无效")
	}
	if math.IsNaN(options.RateChangePercent) || math.IsInf(options.RateChangePercent, 0) || options.RateChangePercent < 0 || options.RateChangePercent > 10000 || math.IsNaN(options.SubscriptionLowPercent) || options.SubscriptionLowPercent < 1 || options.SubscriptionLowPercent > 100 || options.SubscriptionExpiryHours < 1 || options.SubscriptionExpiryHours > 8760 || options.NotificationRetries < 1 || options.NotificationRetries > 30 || options.PriceSyncHours < 0 || options.PriceSyncHours > 720 || options.ModelSyncHours < 0 || options.ModelSyncHours > 720 {
		return errors.New("通知阈值、重试次数或同步周期无效")
	}
	if len(options.NotificationEvents) > 30 || len(options.NotificationAccounts) > 1000 {
		return errors.New("通知筛选数量过多")
	}
	for _, event := range options.NotificationEvents {
		if !notificationKinds[event] {
			return errors.New("通知事件无效：" + event)
		}
	}
	return nil
}

var notificationKinds = map[string]bool{
	"low_balance": true, "monitor_error": true, "recovered": true, "rates_changed": true,
	"announcement": true, "monitor_warning": true, "monitor_warning_recovered": true,
	"channel_auto_disabled": true, "subscription_expiring": true, "subscription_daily": true,
	"subscription_weekly": true, "subscription_monthly": true,
}

func (a *App) runtimeOptions() RuntimeOptions {
	var setting Setting
	if a.DB.First(&setting, 1).Error == nil {
		return setting.RuntimeOptions
	}
	return RuntimeOptions{FirstOutputSeconds: 60, StreamIdleSeconds: 120, RequestTimeoutSeconds: 600, MaxAttempts: 8, MonitorConcurrency: 4, NotificationType: "webhook", SubscriptionLowPercent: 10, SubscriptionExpiryHours: 72, NotificationRetries: 10, ModelSyncHours: 12}
}
