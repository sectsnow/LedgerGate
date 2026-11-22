package app

import (
	"errors"
	"reflect"
	"time"

	"gorm.io/gorm"
)

func (a *App) importNotificationChannels(tx *gorm.DB, setting transferSetting, accountIDs map[uint]uint) error {
	rows := []transferNotificationChannel{}
	if setting.NotificationChannels != nil {
		rows = *setting.NotificationChannels
	} else if setting.WebhookURL != "" || setting.TelegramBotToken != "" {
		// 旧版配置包仍可导入，转换为单条渠道并由用户手动启用。
		rows = append(rows, transferNotificationChannel{
			NotificationChannel: NotificationChannel{Name: "原通知渠道", Type: setting.NotificationType, ChatID: setting.TelegramChatID, Events: setting.NotificationEvents, Accounts: setting.NotificationAccounts, Retries: setting.NotificationRetries},
			Address:             setting.WebhookURL, SigningSecret: setting.WebhookSigningKey, BotToken: setting.TelegramBotToken,
		})
	}
	var current []NotificationChannel
	if err := tx.Order("id").Find(&current).Error; err != nil {
		return err
	}
	byName := map[string]NotificationChannel{}
	for _, row := range current {
		byName[row.Name] = row
	}
	keep := map[uint]bool{}
	for _, source := range rows {
		previous := byName[source.Name]
		channel := source.NotificationChannel
		channel.ID, channel.Enabled = previous.ID, false
		channel.CreatedAt, channel.UpdatedAt = previous.CreatedAt, previous.UpdatedAt
		channel.Accounts = []uint{}
		for _, id := range source.Accounts {
			mapped := accountIDs[id]
			if mapped == 0 {
				return errors.New("通知渠道缺少关联账户")
			}
			channel.Accounts = append(channel.Accounts, mapped)
		}
		for _, pair := range []struct {
			target     *string
			old, plain string
		}{
			{&channel.URL, previous.URL, source.Address}, {&channel.Secret, previous.Secret, source.SigningSecret},
			{&channel.Token, previous.Token, source.BotToken}, {&channel.SendKey, previous.SendKey, source.ServerChanKey},
			{&channel.SMTPPassword, previous.SMTPPassword, source.MailPassword},
		} {
			*pair.target = pair.old
			if plain, err := a.unseal(pair.old); err != nil || plain != pair.plain {
				*pair.target = a.seal(pair.plain)
			}
		}
		if !reflect.DeepEqual(channel, previous) {
			if err := tx.Save(&channel).Error; err != nil {
				return errors.New("导入通知渠道失败，全部变更已回滚")
			}
		}
		keep[channel.ID] = true
	}
	for _, row := range current {
		if !keep[row.ID] {
			var deliveries []NotificationDelivery
			if err := tx.Where("channel_id = ? AND status = ?", row.ID, "pending").Find(&deliveries).Error; err != nil {
				return err
			}
			for _, delivery := range deliveries {
				if err := tx.Model(&delivery).Updates(object{"status": "cancelled", "error": "通知配置已被导入替换", "updated_at": time.Now()}).Error; err != nil {
					return err
				}
				if delivery.AlertID != nil {
					if err := updateAlertDelivery(tx, *delivery.AlertID); err != nil {
						return err
					}
				}
			}
			if err := tx.Delete(&row).Error; err != nil {
				return err
			}
		}
	}
	return nil
}
