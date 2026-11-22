package app

import (
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"golang.org/x/crypto/bcrypt"
)

func (a *App) allowPasswordAttempt(c *gin.Context, action string) bool {
	key, now := action+":"+c.ClientIP(), time.Now()
	a.mu.Lock()
	recent := []time.Time{}
	for _, attempt := range a.loginAttempts[key] {
		if now.Sub(attempt) < time.Minute {
			recent = append(recent, attempt)
		}
	}
	if len(a.loginAttempts) > 1000 {
		clear(a.loginAttempts)
	}
	a.loginAttempts[key] = append(recent, now)
	a.mu.Unlock()
	if len(recent) >= 5 {
		operation := "登录"
		if action == "change" {
			operation = "修改密码"
		}
		fail(c, 429, operation+"过于频繁，请稍后重试")
		return false
	}
	return true
}

func (a *App) adminPasswordStatus(c *gin.Context) {
	c.JSON(200, gin.H{"min_bytes": adminPasswordMinBytes, "max_bytes": adminPasswordMaxBytes, "environment_managed": a.envPassword})
}

func (a *App) changeAdminPassword(c *gin.Context) {
	if a.envPassword {
		fail(c, 409, "管理员密码由 LEDGERGATE_ADMIN_PASSWORD 指定，请在部署配置中更改；移除该变量并重启后可在网页修改")
		return
	}
	if !a.allowPasswordAttempt(c, "change") {
		return
	}
	var input struct {
		CurrentPassword string `json:"current_password"`
		NewPassword     string `json:"new_password"`
		ConfirmPassword string `json:"confirm_password"`
	}
	if c.ShouldBindJSON(&input) != nil {
		fail(c, 400, "请输入当前密码、新密码并确认")
		return
	}
	if err := validateAdminPassword(input.NewPassword); err != nil {
		fail(c, 400, err.Error())
		return
	}
	if input.NewPassword != input.ConfirmPassword {
		fail(c, 400, "两次输入的新密码不一致")
		return
	}
	a.authMu.Lock()
	defer a.authMu.Unlock()
	// 登录和改密使用同一把锁，防止旧密码在会话清理后又创建有效会话。
	cookie, _ := c.Cookie("ledgergate_session")
	var session Session
	if a.DB.WithContext(c.Request.Context()).Where("digest=? AND expires_at>?", digest(cookie), time.Now()).First(&session).Error != nil {
		fail(c, 401, "登录已失效，请重新登录")
		return
	}
	if bcrypt.CompareHashAndPassword(a.passwordHash, []byte(input.CurrentPassword)) != nil {
		fail(c, 400, "当前管理员密码不正确")
		return
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(input.NewPassword), bcrypt.DefaultCost)
	if err != nil {
		fail(c, 500, "生成新密码失败")
		return
	}
	path := filepath.Join(a.dataDir, "admin-password")
	stored, err := os.ReadFile(path)
	if err != nil || strings.TrimSpace(string(stored)) != a.adminPassword {
		fail(c, 409, "密码文件已变更或不可读取，请检查数据目录后重启服务")
		return
	}
	if err := a.DB.WithContext(c.Request.Context()).Where("1=1").Delete(&Session{}).Error; err != nil {
		fail(c, 500, "注销原会话失败，密码未更改，请稍后重试")
		return
	}
	if err := saveAdminPassword(path, input.NewPassword+"\n"); err != nil {
		c.AbortWithStatusJSON(500, gin.H{"error": gin.H{"message": "密码未更改，会话已注销，请使用原密码重新登录并检查数据目录权限", "type": "ledgergate_error"}, "reauthenticate": true})
		return
	}
	a.passwordHash, a.adminPassword = hash, input.NewPassword
	c.SetSameSite(http.SameSiteStrictMode)
	c.SetCookie("ledgergate_session", "", -1, "/", "", c.Request.TLS != nil, true)
	c.JSON(200, gin.H{"ok": true})
}
