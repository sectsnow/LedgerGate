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
	Currency   string     `json:"currency"`
	Balance    *float64   `json:"balance"`
	TodayUsed  *float64   `json:"today_used"`
	TodayDate  string     `json:"today_date"`
	Status     string     `json:"status"`
	Enabled    bool       `json:"enabled"`
	CheckedAt  *time.Time `json:"checked_at"`
	Warnings   string     `json:"-"`
	HasWarning bool       `json:"has_warning" gorm:"-"`
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
	today := usageDay(time.Now()).Format("2006-01-02")
	accounts := []widgetAccount{}
	day := RequestDay{Date: today}
	err = a.DB.Transaction(func(tx *gorm.DB) error {
		if err := tx.Model(&Account{}).Select("currency", "balance", "today_used", "today_date", "status", "enabled", "checked_at", "warnings").Order("id").Scan(&accounts).Error; err != nil {
			return err
		}
		if err := tx.First(&day, "date = ?", today).Error; err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
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
		if accounts[i].TodayDate != today {
			accounts[i].TodayUsed = nil
		}
	}
	c.JSON(200, object{"date": today, "accounts": accounts, "requests": day.Requests, "input_tokens": day.InputTokens, "output_tokens": day.OutputTokens})
}
