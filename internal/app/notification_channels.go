package app

import (
	"context"
	"errors"
	"log/slog"
	"net"
	"net/mail"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

const notificationChannelLimit = 100

func validNotificationType(value string) bool {
	return slices.Contains([]string{"webhook", "feishu", "telegram", "email", "wecom", "dingtalk", "serverchan"}, value)
}

func (a *App) validateNotification(channel NotificationChannel, requireCredentials bool) error {
	for _, field := range []*string{&channel.URL, &channel.Secret, &channel.Token, &channel.SendKey, &channel.SMTPPassword} {
		plain, err := a.unseal(*field)
		if err != nil {
			return err
		}
		*field = plain
	}
	return validateNotificationConfig(channel, requireCredentials)
}

func validateNotificationConfig(channel NotificationChannel, requireCredentials bool) error {
	if !transferName(channel.Name, 100) || !validNotificationType(channel.Type) || channel.Retries < 1 || channel.Retries > 30 {
		return errors.New("通知名称须为 1–100 字节，类型有效，最多尝试次数为 1–30")
	}
	if len(channel.Events) > len(notificationKinds) || len(channel.Accounts) > 1000 {
		return errors.New("通知筛选数量过多")
	}
	for _, event := range channel.Events {
		if !notificationKinds[event] {
			return errors.New("通知事件无效")
		}
	}
	for _, id := range channel.Accounts {
		if id == 0 {
			return errors.New("通知账户无效")
		}
	}
	for _, value := range []string{channel.URL, channel.Secret, channel.Token, channel.SendKey, channel.SMTPPassword} {
		if len(value) > 8192 || strings.ContainsAny(value, "\x00\r\n") {
			return errors.New("通知凭据过长或包含无效字符")
		}
	}
	switch channel.Type {
	case "webhook", "feishu", "wecom", "dingtalk":
		address := channel.URL
		if address == "" && !requireCredentials {
			return nil
		}
		u, err := url.Parse(address)
		if err != nil || u.Hostname() == "" || u.User != nil || u.Fragment != "" || u.Scheme != "http" && u.Scheme != "https" {
			return errors.New("请填写有效的 HTTP 或 HTTPS 通知地址")
		}
	case "telegram":
		token := channel.Token
		if token != "" && !telegramTokenValid(token) || len(channel.ChatID) > 100 || strings.ContainsAny(channel.ChatID, "\x00\r\n") {
			return errors.New("Telegram Bot Token 或 Chat ID 无效")
		}
		if requireCredentials && (token == "" || strings.TrimSpace(channel.ChatID) == "") {
			return errors.New("请填写 Telegram Bot Token 和 Chat ID")
		}
	case "serverchan":
		key := channel.SendKey
		if key == "" && !requireCredentials {
			return nil
		}
		if _, err := serverChanAddress(key); err != nil {
			return err
		}
	case "email":
		if channel.SMTPHost == "" && !requireCredentials {
			return nil
		}
		if len(channel.SMTPHost) > 253 || strings.ContainsAny(channel.SMTPHost, "\x00\r\n /\\@") || channel.SMTPHost == "" || strings.Contains(channel.SMTPHost, ":") && net.ParseIP(channel.SMTPHost) == nil || channel.SMTPPort < 1 || channel.SMTPPort > 65535 || !slices.Contains([]string{"tls", "starttls"}, channel.SMTPTLS) {
			return errors.New("SMTP 主机、端口或加密方式无效（仅支持 TLS / STARTTLS）")
		}
		if len(channel.SMTPUsername) > 250 || strings.ContainsAny(channel.SMTPUsername, "\x00\r\n") || len(channel.SMTPTo) < 1 || len(channel.SMTPTo) > 50 {
			return errors.New("SMTP 用户名无效或收件人数须为 1–50")
		}
		for _, address := range append([]string{channel.SMTPFrom}, channel.SMTPTo...) {
			if len(address) > 320 || strings.ContainsAny(address, "\x00\r\n") {
				return errors.New("邮箱地址无效")
			}
			if _, err := mail.ParseAddress(address); err != nil {
				return errors.New("请填写有效的发件人与收件人邮箱")
			}
		}
		if requireCredentials && channel.SMTPUsername != "" && channel.SMTPPassword == "" {
			return errors.New("请填写 SMTP 密码或授权码")
		}
	}
	return nil
}

func (a *App) migrateNotifications() error {
	return a.DB.Transaction(func(tx *gorm.DB) error {
		var setting Setting
		if err := tx.First(&setting, 1).Error; err != nil {
			return err
		}
		if setting.NotificationsMigrated {
			return nil
		}
		if notificationConfigured(setting) {
			channel := a.legacyNotificationChannel(setting)
			if channel.Type == "" {
				channel.Type = "webhook"
			}
			if err := tx.Create(&channel).Error; err != nil {
				return err
			}
			// 继承原队列的尝试次数和重试时间，历史已送达记录不会再次发送。
			if err := tx.Exec(`INSERT INTO notification_deliveries
				(alert_id, channel_id, channel_name, channel_type, title, status, error, attempts, next_retry_at, created_at, updated_at)
				SELECT id, ?, ?, ?, title, delivery, delivery_error, attempts, next_retry_at, created_at, ?
				FROM alerts WHERE delivery IN ('pending', 'sent', 'failed')`, channel.ID, channel.Name, channel.Type, time.Now()).Error; err != nil {
				return err
			}
		}
		return tx.Model(&setting).Updates(object{"notifications_migrated": true, "webhook_url": "", "webhook_secret": "", "telegram_token": "", "telegram_chat_id": "", "notification_accounts": "[]", "notification_events": "[]"}).Error
	})
}

type notificationChannelView struct {
	NotificationChannel
	HasURL          bool                  `json:"has_url"`
	HasSecret       bool                  `json:"has_secret"`
	HasToken        bool                  `json:"has_token"`
	HasSendKey      bool                  `json:"has_send_key"`
	HasSMTPPassword bool                  `json:"has_smtp_password"`
	LastDelivery    *NotificationDelivery `json:"last_delivery"`
}

func notificationView(channel NotificationChannel) notificationChannelView {
	return notificationChannelView{NotificationChannel: channel, HasURL: channel.URL != "", HasSecret: channel.Secret != "", HasToken: channel.Token != "", HasSendKey: channel.SendKey != "", HasSMTPPassword: channel.SMTPPassword != ""}
}

func (a *App) listNotificationChannels(c *gin.Context) {
	channels, deliveries := []NotificationChannel{}, []NotificationDelivery{}
	if a.DB.Order("id").Find(&channels).Error != nil || a.DB.Where("id IN (?)", a.DB.Model(&NotificationDelivery{}).Select("MAX(id)").Group("channel_id")).Find(&deliveries).Error != nil {
		fail(c, 500, "读取通知渠道失败")
		return
	}
	last := map[uint]*NotificationDelivery{}
	for i := range deliveries {
		last[deliveries[i].ChannelID] = &deliveries[i]
	}
	items := make([]notificationChannelView, 0, len(channels))
	for _, channel := range channels {
		item := notificationView(channel)
		item.LastDelivery = last[channel.ID]
		items = append(items, item)
	}
	c.JSON(200, items)
}

func (a *App) saveNotificationChannel(c *gin.Context) {
	var input struct {
		NotificationChannel
		URL               string `json:"url"`
		Secret            string `json:"secret"`
		Token             string `json:"token"`
		SendKey           string `json:"send_key"`
		SMTPPassword      string `json:"smtp_password"`
		ClearURL          bool   `json:"clear_url"`
		ClearSecret       bool   `json:"clear_secret"`
		ClearToken        bool   `json:"clear_token"`
		ClearSendKey      bool   `json:"clear_send_key"`
		ClearSMTPPassword bool   `json:"clear_smtp_password"`
	}
	if c.ShouldBindJSON(&input) != nil {
		fail(c, 400, "通知渠道格式无效")
		return
	}
	a.notificationMu.Lock()
	defer a.notificationMu.Unlock()
	var current NotificationChannel
	if c.Param("id") != "" {
		id, ok := pathID(c)
		if !ok {
			return
		}
		if a.DB.First(&current, id).Error != nil {
			fail(c, 404, "通知渠道不存在")
			return
		}
	}
	channel := input.NotificationChannel
	channel.ID, channel.CreatedAt, channel.UpdatedAt = current.ID, current.CreatedAt, time.Time{}
	channel.Name, channel.ChatID = strings.TrimSpace(channel.Name), strings.TrimSpace(channel.ChatID)
	channel.SMTPHost, channel.SMTPUsername, channel.SMTPFrom = strings.TrimSpace(channel.SMTPHost), strings.TrimSpace(channel.SMTPUsername), strings.TrimSpace(channel.SMTPFrom)
	channel.Events, channel.Accounts = slices.Compact(slices.Sorted(slices.Values(channel.Events))), slices.Compact(slices.Sorted(slices.Values(channel.Accounts)))
	if channel.ID == 0 && channel.Retries == 0 {
		channel.Retries = a.runtimeOptions().NotificationRetries
	}
	if current.Type != channel.Type {
		current.URL, current.Secret, current.Token, current.SendKey, current.SMTPPassword = "", "", "", "", ""
	}
	for _, pair := range []struct {
		target     *string
		old, value string
		clear      bool
	}{
		{&channel.URL, current.URL, strings.TrimSpace(input.URL), input.ClearURL},
		{&channel.Secret, current.Secret, strings.TrimSpace(input.Secret), input.ClearSecret},
		{&channel.Token, current.Token, strings.TrimSpace(input.Token), input.ClearToken},
		{&channel.SendKey, current.SendKey, strings.TrimSpace(input.SendKey), input.ClearSendKey},
		{&channel.SMTPPassword, current.SMTPPassword, input.SMTPPassword, input.ClearSMTPPassword},
	} {
		*pair.target = pair.old
		if pair.value != "" {
			*pair.target = a.seal(pair.value)
		}
		if pair.clear {
			*pair.target = ""
		}
	}
	if err := a.validateNotification(channel, channel.Enabled); err != nil {
		fail(c, 400, err.Error())
		return
	}
	var accountCount int64
	if a.DB.Model(&Account{}).Where("id IN ?", channel.Accounts).Count(&accountCount).Error != nil || int(accountCount) != len(channel.Accounts) {
		fail(c, 400, "通知订阅包含不存在的账户，请重新选择")
		return
	}
	err := a.DB.Transaction(func(tx *gorm.DB) error {
		if channel.ID == 0 {
			var count int64
			if err := tx.Model(&NotificationChannel{}).Count(&count).Error; err != nil {
				return err
			}
			if count >= notificationChannelLimit {
				return errors.New("通知渠道最多 100 条")
			}
		}
		return tx.Save(&channel).Error
	})
	if err != nil {
		fail(c, 409, "保存通知渠道失败，请检查渠道数量或稍后重试")
		return
	}
	c.JSON(200, notificationView(channel))
}

func (a *App) setNotificationEnabled(c *gin.Context) {
	id, ok := pathID(c)
	if !ok {
		return
	}
	var input struct {
		Enabled bool `json:"enabled"`
	}
	if c.ShouldBindJSON(&input) != nil {
		fail(c, 400, "启停状态无效")
		return
	}
	a.notificationMu.Lock()
	defer a.notificationMu.Unlock()
	var channel NotificationChannel
	if a.DB.First(&channel, id).Error != nil {
		fail(c, 404, "通知渠道不存在")
		return
	}
	if input.Enabled {
		if err := a.validateNotification(channel, true); err != nil {
			fail(c, 400, err.Error())
			return
		}
	}
	if a.DB.Model(&channel).Update("enabled", input.Enabled).Error != nil {
		fail(c, 500, "修改通知渠道状态失败")
		return
	}
	c.JSON(200, object{"ok": true})
}

func (a *App) deleteNotificationChannel(c *gin.Context) {
	id, ok := pathID(c)
	if !ok {
		return
	}
	a.notificationMu.Lock()
	defer a.notificationMu.Unlock()
	err := a.DB.Transaction(func(tx *gorm.DB) error {
		var channel NotificationChannel
		if err := tx.First(&channel, id).Error; err != nil {
			return err
		}
		var ids []uint
		if err := tx.Model(&NotificationDelivery{}).Where("channel_id = ? AND status = ? AND alert_id IS NOT NULL", id, "pending").Pluck("alert_id", &ids).Error; err != nil {
			return err
		}
		if err := tx.Model(&NotificationDelivery{}).Where("channel_id = ? AND status = ?", id, "pending").Updates(object{"status": "cancelled", "error": "通知渠道已删除"}).Error; err != nil {
			return err
		}
		for _, alertID := range ids {
			if err := updateAlertDelivery(tx, alertID); err != nil {
				return err
			}
		}
		return tx.Delete(&channel).Error
	})
	if errors.Is(err, gorm.ErrRecordNotFound) {
		fail(c, 404, "通知渠道不存在")
	} else if err != nil {
		fail(c, 500, "删除通知渠道失败")
	} else {
		c.JSON(200, object{"ok": true})
	}
}

func (a *App) listNotificationDeliveries(c *gin.Context) {
	page, size, ok := activityPage(c)
	if !ok {
		return
	}
	query := a.DB.Model(&NotificationDelivery{})
	if raw := c.Query("channel_id"); raw != "" {
		id, err := strconv.ParseUint(raw, 10, 32)
		if err != nil || id == 0 {
			fail(c, 400, "通知渠道 ID 无效")
			return
		}
		query = query.Where("channel_id = ?", id)
	}
	var total int64
	items := []NotificationDelivery{}
	if query.Count(&total).Error != nil || query.Order("id DESC").Offset((page-1)*size).Limit(size).Find(&items).Error != nil {
		fail(c, 500, "读取通知投递记录失败")
		return
	}
	c.JSON(200, object{"items": items, "total": total, "page": page, "page_size": size})
}

func (a *App) testNotificationChannel(c *gin.Context) {
	id, ok := pathID(c)
	if !ok {
		return
	}
	a.notificationMu.Lock()
	defer a.notificationMu.Unlock()
	var channel NotificationChannel
	if a.DB.First(&channel, id).Error != nil {
		fail(c, 404, "通知渠道不存在")
		return
	}
	if err := a.validateNotification(channel, true); err != nil {
		fail(c, 400, err.Error())
		return
	}
	alert := Alert{Kind: "test", Title: "LedgerGate 测试通知", Detail: "告警通道连接正常", CreatedAt: time.Now()}
	delivery := NotificationDelivery{ChannelID: channel.ID, ChannelName: channel.Name, ChannelType: channel.Type, Title: alert.Title, Status: "pending", NextRetryAt: time.Now()}
	if a.DB.Create(&delivery).Error != nil {
		fail(c, 500, "创建测试投递记录失败")
		return
	}
	result := a.sendNotification(c.Request.Context(), channel, alert)
	recordNotificationResult(&delivery, result, 1)
	if a.DB.Save(&delivery).Error != nil {
		fail(c, 500, "保存测试投递结果失败，请先查看接收端，避免重复测试")
		return
	}
	if result != nil {
		fail(c, 502, result.Error())
		return
	}
	c.JSON(200, object{"ok": true})
}

func recordNotificationResult(delivery *NotificationDelivery, result error, retries int) {
	delivery.Attempts++
	delivery.Status, delivery.Error = "sent", ""
	now := time.Now()
	delivery.SentAt = &now
	if result != nil {
		delivery.Status, delivery.Error, delivery.SentAt = "pending", result.Error(), nil
		delivery.NextRetryAt = now.Add(time.Duration(min(1<<min(delivery.Attempts, 10), 3600)) * time.Second)
		if delivery.Attempts >= retries {
			delivery.Status = "failed"
		}
	}
}

func updateAlertDelivery(tx *gorm.DB, alertID uint) error {
	var rows []NotificationDelivery
	if err := tx.Where("alert_id = ?", alertID).Find(&rows).Error; err != nil {
		return err
	}
	if len(rows) == 0 {
		return nil
	}
	counts := map[string]int{}
	attempts, messages, next := 0, []string{}, time.Time{}
	for _, row := range rows {
		counts[row.Status]++
		attempts = max(attempts, row.Attempts)
		if row.Error != "" && row.Status != "cancelled" {
			messages = append(messages, row.ChannelName+"："+row.Error)
		}
		if row.Status == "pending" && (next.IsZero() || row.NextRetryAt.Before(next)) {
			next = row.NextRetryAt
		}
	}
	status := "cancelled"
	if counts["pending"] > 0 {
		status = "pending"
	} else if counts["sent"] == len(rows) {
		status = "sent"
	} else if counts["sent"] > 0 {
		status = "partial"
	} else if counts["failed"] > 0 {
		status = "failed"
	}
	return tx.Model(&Alert{}).Where("id = ?", alertID).Updates(object{"delivery": status, "delivery_error": notificationText(strings.Join(messages, "；"), 2000), "attempts": attempts, "next_retry_at": next}).Error
}

func (a *App) deliverAlerts(ctx context.Context) {
	rows := []NotificationDelivery{}
	if a.DB.Where("status = ? AND alert_id IS NOT NULL AND next_retry_at <= ? AND channel_id IN (?)", "pending", time.Now(), a.DB.Model(&NotificationChannel{}).Select("id").Where("enabled = ?", true)).Order("next_retry_at, id").Limit(10).Find(&rows).Error != nil {
		return
	}
	for _, row := range rows {
		if ctx.Err() != nil {
			return
		}
		a.deliverNotification(ctx, row.ID)
	}
}

func (a *App) deliverNotification(ctx context.Context, id uint) {
	a.notificationMu.Lock()
	defer a.notificationMu.Unlock()
	var delivery NotificationDelivery
	if a.DB.First(&delivery, id).Error != nil || delivery.Status != "pending" || delivery.AlertID == nil || delivery.NextRetryAt.After(time.Now()) {
		return
	}
	var channel NotificationChannel
	if a.DB.First(&channel, delivery.ChannelID).Error != nil || !channel.Enabled {
		return
	}
	var alert Alert
	if a.DB.First(&alert, *delivery.AlertID).Error != nil {
		return
	}
	result := a.sendNotification(ctx, channel, alert)
	if ctx.Err() != nil {
		return
	}
	recordNotificationResult(&delivery, result, channel.Retries)
	if err := a.DB.Transaction(func(tx *gorm.DB) error {
		if err := tx.Save(&delivery).Error; err != nil {
			return err
		}
		return updateAlertDelivery(tx, *delivery.AlertID)
	}); err != nil {
		slog.Error("通知投递状态保存失败", "delivery_id", delivery.ID, "error", err)
	}
}
