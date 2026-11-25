package app

import (
	"encoding/json"
	"log/slog"
	"time"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

type collectionResult struct {
	Status      string     `json:"status"`
	AttemptedAt *time.Time `json:"attempted_at"`
	SucceededAt *time.Time `json:"succeeded_at"`
	Message     string     `json:"message"`
}
type CollectionEvent struct {
	ID        uint      `json:"id" gorm:"primaryKey"`
	AccountID uint      `json:"account_id" gorm:"index"`
	Kind      string    `json:"kind"`
	Status    string    `json:"status"`
	Message   string    `json:"message"`
	CreatedAt time.Time `json:"created_at" gorm:"index"`
}

func collectionState(account Account) map[string]collectionResult {
	rows := map[string]collectionResult{}
	_ = json.Unmarshal([]byte(account.CollectionState), &rows)
	if rows == nil {
		rows = map[string]collectionResult{}
	}
	return rows
}
func updateCollection(rows map[string]collectionResult, kind, status, message string) {
	now := time.Now()
	row := rows[kind]
	row.Status, row.Message, row.AttemptedAt = status, message, &now
	if status == "ok" {
		row.SucceededAt = &now
	}
	rows[kind] = row
}
func (a *App) accountCollection(c *gin.Context) {
	id, ok := pathID(c)
	if !ok {
		return
	}
	var account Account
	if a.DB.First(&account, id).Error != nil {
		fail(c, 404, "账户不存在")
		return
	}
	events := []CollectionEvent{}
	if a.DB.Where("account_id = ? AND created_at >= ?", id, time.Now().AddDate(0, 0, -30)).Order("id DESC").Limit(50).Find(&events).Error != nil {
		fail(c, 500, "读取采集时间线失败")
		return
	}
	rows := collectionState(account)
	for _, kind := range []string{"balance", "used", "today", "rates", "subscriptions", "announcements", "prices"} {
		if _, ok := rows[kind]; !ok {
			rows[kind] = collectionResult{Status: "unknown", Message: "尚无分项采集记录"}
		}
	}
	if account.Kind != "sub2api" {
		rows["subscriptions"] = collectionResult{Status: "unsupported", Message: "此类型账户未接入订阅采集"}
	}
	c.Header("Cache-Control", "no-store")
	c.JSON(200, gin.H{"items": rows, "events": events, "enabled": account.Enabled, "interval_seconds": account.IntervalSeconds, "checked_at": account.CheckedAt})
}
func appendCollectionEvent(tx *gorm.DB, accountID uint, kind, status, message string) error {
	row := CollectionEvent{AccountID: accountID, Kind: kind, Status: status, Message: message, CreatedAt: time.Now()}
	if err := tx.Create(&row).Error; err != nil {
		return err
	}
	return tx.Where("account_id = ? AND id NOT IN (?)", accountID, tx.Model(&CollectionEvent{}).Select("id").Where("account_id = ?", accountID).Order("id DESC").Limit(100)).Delete(&CollectionEvent{}).Error
}
func (a *App) priceCollectionOutcome(account Account, status, message string, event bool) {
	err := a.DB.Transaction(func(tx *gorm.DB) error {
		var current Account
		if err := tx.First(&current, account.ID).Error; err != nil {
			return err
		}
		if current.Revision != account.Revision {
			return nil
		}
		rows := collectionState(current)
		updateCollection(rows, "prices", status, message)
		if err := tx.Model(&current).Update("collection_state", jsonString(rows)).Error; err != nil {
			return err
		}
		if event {
			return appendCollectionEvent(tx, account.ID, "prices", status, message)
		}
		return nil
	})
	if err != nil {
		slog.Warn("报价采集状态保存失败", "account_id", account.ID, "error", err)
	}
}
