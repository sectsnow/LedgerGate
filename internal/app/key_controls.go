package app

import (
	"errors"
	"slices"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

type keyInput struct {
	Name          string     `json:"name"`
	Enabled       *bool      `json:"enabled"`
	ExpiresAt     *time.Time `json:"expires_at"`
	AllowedModels []string   `json:"allowed_models"`
	BudgetMicros  *int64     `json:"budget_micros"`
}

func (input *keyInput) validate() error {
	input.Name = strings.TrimSpace(input.Name)
	if input.Name == "" || len(input.Name) > 100 || len(input.AllowedModels) > 500 || input.BudgetMicros != nil && *input.BudgetMicros < 0 {
		return errors.New("请检查密钥名称、模型列表和预算")
	}
	models := []string{}
	for _, name := range input.AllowedModels {
		name = strings.TrimSpace(name)
		if name == "" || len(name) > 200 {
			return errors.New("允许使用的模型名称无效")
		}
		if !slices.Contains(models, name) {
			models = append(models, name)
		}
	}
	input.AllowedModels = models
	return nil
}

func (a *App) createAPIKey(c *gin.Context) {
	var input keyInput
	if c.ShouldBindJSON(&input) != nil {
		fail(c, 400, "密钥参数无效")
		return
	}
	if err := input.validate(); err != nil {
		fail(c, 400, err.Error())
		return
	}
	token := "sk-" + randomToken()
	enabled := true
	if input.Enabled != nil {
		enabled = *input.Enabled
	}
	key := APIKey{Name: input.Name, Prefix: token[:11], Digest: digest(token), TokenCipher: a.seal(token), CanCopy: true, Enabled: enabled, ExpiresAt: input.ExpiresAt, AllowedModels: input.AllowedModels, BudgetMicros: input.BudgetMicros}
	if a.DB.Create(&key).Error != nil {
		fail(c, 500, "密钥保存失败")
		return
	}
	c.JSON(201, object{"key": key, "token": token})
}

func (a *App) updateAPIKey(c *gin.Context) {
	id, ok := pathID(c)
	if !ok {
		return
	}
	var input keyInput
	if c.ShouldBindJSON(&input) != nil {
		fail(c, 400, "密钥参数无效")
		return
	}
	if err := input.validate(); err != nil {
		fail(c, 400, err.Error())
		return
	}
	updates := map[string]any{"name": input.Name, "expires_at": input.ExpiresAt, "budget_micros": input.BudgetMicros, "allowed_models": jsonString(input.AllowedModels)}
	if input.Enabled != nil {
		updates["enabled"] = *input.Enabled
	}
	result := a.DB.Model(&APIKey{}).Where("id=?", id).Updates(updates)
	if result.Error != nil {
		fail(c, 500, "密钥保存失败")
		return
	}
	if result.RowsAffected == 0 {
		fail(c, 404, "密钥不存在")
		return
	}
	c.JSON(200, object{"ok": true})
}

func debitKey(tx *gorm.DB, log *RequestLog) error {
	if log.KeyID == 0 || log.CostMicros <= 0 {
		return nil
	}
	const maximum = int64(1<<63 - 1)
	return tx.Model(&APIKey{}).Where("id=?", log.KeyID).Update("used_micros", gorm.Expr("CASE WHEN used_micros > ? THEN ? ELSE used_micros + ? END", maximum-log.CostMicros, maximum, log.CostMicros)).Error
}
