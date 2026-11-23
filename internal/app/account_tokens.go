package app

import (
	"errors"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

func (a *App) revealAccountToken(c *gin.Context) {
	id, ok := pathID(c)
	if !ok {
		return
	}
	var input struct {
		Field string `json:"field"`
	}
	if c.ShouldBindJSON(&input) != nil || (input.Field != "token" && input.Field != "refresh_token") {
		fail(c, 400, "请选择 Access Token 或 Refresh Token")
		return
	}
	var account Account
	if err := a.DB.Select("id", "token", "refresh_token").First(&account, id).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			fail(c, 404, "账户不存在")
		} else {
			fail(c, 500, "读取账户令牌失败")
		}
		return
	}
	ciphertext := account.Token
	if input.Field == "refresh_token" {
		ciphertext = account.RefreshToken
	}
	token, err := a.unseal(ciphertext)
	if err != nil {
		fail(c, 500, "令牌解密失败，请检查主密钥")
		return
	}
	c.JSON(200, gin.H{"token": token})
}
