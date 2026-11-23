package app

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"

	"ledgergate/internal/backup"
	"ledgergate/web"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

func (a *App) Router() http.Handler {
	gin.SetMode(gin.ReleaseMode)
	r := gin.New()
	r.Use(gin.Recovery())
	_ = r.SetTrustedProxies(nil)
	r.Use(func(c *gin.Context) {
		c.Header("X-Content-Type-Options", "nosniff")
		c.Header("Referrer-Policy", "same-origin")
		c.Header("X-Frame-Options", "DENY")
		bodyLimit := int64(32 << 20)
		if c.Request.URL.Path == "/api/backup/restore" {
			bodyLimit = backup.FileLimit + (1 << 20)
		}
		if c.Request.URL.Path == "/api/login" || c.Request.URL.Path == "/api/setup" || c.Request.URL.Path == "/api/admin/password" {
			bodyLimit = 4096
		}
		c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, bodyLimit)
		if strings.HasPrefix(c.Request.URL.Path, "/api/") {
			c.Header("Cache-Control", "no-store")
		}
		c.Next()
	})
	r.Use(func(c *gin.Context) {
		// 恢复必须等所有请求和后台写入完成，数据库和主密钥一起切换。
		if c.Request.Method == http.MethodPost && c.Request.URL.Path == "/api/backup/restore" {
			a.runtimeMu.RLock()
			authorized := a.requireAdmin(c)
			a.runtimeMu.RUnlock()
			if !authorized {
				return
			}
			a.runtimeMu.Lock()
			defer a.runtimeMu.Unlock()
		} else {
			a.runtimeMu.RLock()
			defer a.runtimeMu.RUnlock()
		}
		c.Next()
	})
	r.GET("/healthz", func(c *gin.Context) {
		c.JSON(200, gin.H{"status": "ok", "app": "LedgerGate", "build": currentBuildInfo()})
	})
	r.POST("/api/login", a.login)
	r.GET("/api/setup", a.setupStatus)
	r.POST("/api/setup", a.setupAdmin)
	api := r.Group("/api", a.admin)
	api.GET("/version", a.versionInfo)
	api.GET("/session", func(c *gin.Context) { c.JSON(200, gin.H{"authenticated": true}) })
	api.GET("/admin/password", a.adminPasswordStatus)
	api.PUT("/admin/password", a.changeAdminPassword)
	api.POST("/logout", func(c *gin.Context) {
		cookie, _ := c.Cookie("ledgergate_session")
		a.DB.Delete(&Session{}, "digest = ?", digest(cookie))
		c.SetCookie("ledgergate_session", "", -1, "/", "", c.Request.TLS != nil, true)
		c.Status(204)
	})
	api.GET("/overview", a.overview)
	api.GET("/proxy", a.getProxy)
	api.PUT("/proxy", a.saveProxy)
	api.POST("/proxy/test", a.testProxy)
	api.POST("/backup", a.createBackup)
	api.POST("/backup/restore", a.restoreBackup)
	api.GET("/data-transfer", a.transferCatalog)
	api.POST("/data-transfer/export", a.exportData)
	api.POST("/data-transfer/preview", a.previewDataImport)
	api.POST("/data-transfer/import", a.importData)
	api.GET("/accounts", a.accounts)
	api.GET("/accounts/:id/icon", a.accountIcon)
	api.POST("/accounts", a.saveAccount)
	api.PUT("/accounts/:id", a.saveAccount)
	api.PATCH("/accounts/:id/monitoring", a.setAccountMonitoring)
	api.DELETE("/accounts/:id", a.deleteAccount)
	api.POST("/accounts/:id/check", a.checkAccountHandler)
	api.GET("/accounts/:id/api-keys", a.listAccountKeys)
	api.GET("/accounts/:id/upstream-groups", a.accountGroups)
	api.POST("/accounts/:id/create-channel", a.createAccountChannel)
	api.POST("/accounts/:id/import-channels", a.importAccountChannels)
	api.GET("/accounts/:id/history", func(c *gin.Context) {
		items := []Snapshot{}
		a.DB.Where("account_id = ?", c.Param("id")).Order("id DESC").Limit(100).Find(&items)
		c.JSON(200, items)
	})
	api.GET("/channels", a.channels)
	api.POST("/channels", a.saveChannel)
	api.POST("/channels/batch", a.batchChannels)
	api.PUT("/channels/:id", a.saveChannel)
	api.DELETE("/channels/:id", a.deleteChannel)
	api.POST("/channels/:id/models", a.syncModels)
	api.GET("/channels/:id/models/available", a.previewChannelModels)
	api.PUT("/channels/:id/models", a.saveChannelModelSelection)
	api.POST("/channels/:id/models/acknowledge", a.acknowledgeChannelModels)
	api.GET("/channels/:id/model-tests/models", a.channelTestModels)
	api.POST("/channels/:id/model-tests/models", a.channelTestModels)
	api.POST("/channels/:id/model-tests", a.testChannelModel)
	api.POST("/channels/:id/models/remove", a.removeChannelTestModels)
	api.GET("/groups/routing", a.groupRuntimeStatus)
	api.GET("/groups", func(c *gin.Context) {
		items := []Group{}
		if err := a.DB.Preload("Routes", func(db *gorm.DB) *gorm.DB { return db.Order("priority, id") }).Order("id").Find(&items).Error; err != nil {
			fail(c, 500, "读取路由失败")
			return
		}
		for i := range items {
			config := items[i].relayConfig()
			items[i].RelayConfig = &config
			items[i].Vendor = items[i].modelVendor()
			items[i].PriceMode = items[i].priceMode()
			if items[i].PriceMode == "manual" {
				items[i].PriceSource = "manual"
			}
		}
		c.JSON(200, items)
	})
	api.POST("/groups", a.saveGroup)
	api.POST("/groups/generate", a.generateModelGroups)
	api.POST("/groups/sync-prices", a.syncModelPrices)
	api.PUT("/groups/:id", a.saveGroup)
	api.PATCH("/groups/:id/selection", a.selectGroupRoute)
	api.PATCH("/groups/:id/order", a.reorderGroupRoutes)
	api.DELETE("/groups/:id/routes/:route_id", a.deleteGroupRoute)
	api.DELETE("/groups/:id", func(c *gin.Context) {
		id, ok := pathID(c)
		if !ok {
			return
		}
		err := a.DB.Transaction(func(tx *gorm.DB) error {
			if err := tx.Where("group_id = ?", id).Delete(&Route{}).Error; err != nil {
				return err
			}
			return tx.Delete(&Group{}, id).Error
		})
		if err != nil {
			fail(c, 500, "删除路由失败")
			return
		}
		a.mu.Lock()
		delete(a.groupRouting, id)
		a.mu.Unlock()
		c.Status(204)
	})
	api.GET("/keys", func(c *gin.Context) {
		keys := []APIKey{}
		if a.DB.Order("id DESC").Find(&keys).Error != nil {
			fail(c, 500, "读取密钥失败")
			return
		}
		for i := range keys {
			keys[i].CanCopy = keys[i].TokenCipher != ""
		}
		c.JSON(200, keys)
	})
	api.POST("/keys", a.createAPIKey)
	api.PUT("/keys/:id", a.updateAPIKey)
	api.POST("/keys/:id/copy", func(c *gin.Context) {
		id, ok := pathID(c)
		if !ok {
			return
		}
		var key APIKey
		err := a.DB.Where("id = ? AND enabled = ?", id, true).First(&key).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			fail(c, 404, "密钥不存在或已撤销")
			return
		}
		if err != nil {
			fail(c, 500, "读取密钥失败")
			return
		}
		if key.TokenCipher == "" {
			fail(c, 409, "旧密钥仅保存摘要，无法恢复完整内容，请创建新密钥")
			return
		}
		token, err := a.unseal(key.TokenCipher)
		if err != nil || digest(token) != key.Digest {
			fail(c, 500, "密钥解密或校验失败")
			return
		}
		c.JSON(200, gin.H{"token": token})
	})
	api.DELETE("/keys/:id", func(c *gin.Context) {
		id, ok := pathID(c)
		if !ok {
			return
		}
		if err := a.DB.Delete(&APIKey{}, id).Error; err != nil {
			fail(c, 500, "撤销密钥失败")
			return
		}
		c.Status(204)
	})
	api.GET("/requests", a.listRequests)
	api.GET("/request-quality", a.requestQuality)
	api.GET("/requests/:id", a.requestDetail)
	api.GET("/alerts", a.listAlerts)
	api.GET("/announcements", a.listAnnouncements)
	api.GET("/settings", func(c *gin.Context) {
		var setting Setting
		if a.DB.First(&setting, 1).Error != nil {
			fail(c, 500, "读取设置失败")
			return
		}
		c.JSON(200, gin.H{"sync_tasks": a.syncTaskList(), "runtime": setting.RuntimeOptions, "telegram_chat_id": setting.TelegramChatID, "has_telegram_token": setting.TelegramToken != "", "webhook_url": setting.WebhookURL, "has_webhook_secret": setting.WebhookSecret != "", "alert_cooldown_minutes": setting.AlertCooldownMinutes, "retention_days": setting.RetentionDays,
			"request_summary_retention_days": setting.RequestSummaryRetentionDays})
	})
	api.PUT("/settings", a.saveSettings)
	api.POST("/settings/test", a.testWebhook)
	api.GET("/notification-channels", a.listNotificationChannels)
	api.POST("/notification-channels", a.saveNotificationChannel)
	api.PUT("/notification-channels/:id", a.saveNotificationChannel)
	api.PATCH("/notification-channels/:id/enabled", a.setNotificationEnabled)
	api.DELETE("/notification-channels/:id", a.deleteNotificationChannel)
	api.POST("/notification-channels/:id/test", a.testNotificationChannel)
	api.GET("/notification-deliveries", a.listNotificationDeliveries)
	gateway := r.Group("/v1", a.gatewayAuth)
	gateway.GET("/models", a.listModels)
	gateway.POST("/chat/completions", func(c *gin.Context) { a.forward(c, "openai") })
	gateway.POST("/responses", func(c *gin.Context) { a.forward(c, "responses") })
	gateway.POST("/messages", func(c *gin.Context) { a.forward(c, "anthropic") })
	assets, _ := fs.Sub(web.Assets, "static/app")
	files := http.FileServer(http.FS(assets))
	r.NoRoute(func(c *gin.Context) {
		if strings.HasPrefix(c.Request.URL.Path, "/api/") || strings.HasPrefix(c.Request.URL.Path, "/v1/") || c.Request.Method != http.MethodGet {
			fail(c, 404, "接口不存在")
			return
		}
		name := strings.TrimPrefix(c.Request.URL.Path, "/")
		if name != "" {
			if info, err := fs.Stat(assets, name); err == nil && !info.IsDir() {
				files.ServeHTTP(c.Writer, c.Request)
				return
			}
		}
		index, err := fs.ReadFile(assets, "index.html")
		if err != nil {
			c.String(503, "请先在 web 目录运行 corepack pnpm install 和 corepack pnpm build，再重新启动服务。")
			return
		}
		c.Data(200, "text/html; charset=utf-8", index)
	})
	return r
}

func pathID(c *gin.Context) (uint, bool) {
	id, err := strconv.ParseUint(c.Param("id"), 10, 32)
	if err != nil || id == 0 {
		fail(c, 400, "ID 无效")
		return 0, false
	}
	return uint(id), true
}

func validURL(raw string) (string, error) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || u.Hostname() == "" || (u.Scheme != "http" && u.Scheme != "https") || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return "", errors.New("请输入完整 HTTP(S) 地址，地址中不能包含凭据、查询参数或片段")
	}
	return strings.TrimRight(u.String(), "/"), nil
}

func (a *App) accounts(c *gin.Context) {
	items := []Account{}
	if a.DB.Order("id").Find(&items).Error != nil {
		fail(c, 500, "读取账户失败")
		return
	}
	result := []gin.H{}
	today := usageDay(time.Now()).Format("2006-01-02")
	for _, item := range items {
		if item.TodayDate != today {
			item.TodayUsed = nil
		}
		result = append(result, gin.H{"account": item, "has_token": item.Token != "", "has_refresh_token": item.RefreshToken != "", "has_password": item.Password != "", "has_login_extra": item.LoginExtra != ""})
	}
	c.JSON(200, result)
}

type accountInput struct {
	UseProxy        *bool   `json:"use_proxy"`
	LoginExtra      string  `json:"login_extra"`
	ClearLoginExtra bool    `json:"clear_login_extra"`
	Name            string  `json:"name"`
	Kind            string  `json:"kind"`
	BaseURL         string  `json:"base_url"`
	Enabled         bool    `json:"enabled"`
	AuthMode        string  `json:"auth_mode"`
	Username        string  `json:"username"`
	Password        string  `json:"password"`
	Token           string  `json:"token"`
	RefreshToken    string  `json:"refresh_token"`
	UserID          string  `json:"user_id"`
	Currency        string  `json:"currency"`
	QuotaPerUnit    float64 `json:"quota_per_unit"`
	LowBalance      float64 `json:"low_balance"`
	BlockEmpty      bool    `json:"block_empty"`
	IntervalSeconds int     `json:"interval_seconds"`
}

func (a *App) saveAccount(c *gin.Context) {
	var input accountInput
	if c.ShouldBindJSON(&input) != nil {
		fail(c, 400, "账户参数无效")
		return
	}
	input.Token = strings.TrimSpace(input.Token)
	input.RefreshToken = strings.TrimSpace(input.RefreshToken)
	if input.RefreshToken != "" && (input.Kind != "sub2api" || input.Token == "") {
		fail(c, 400, "Refresh Token 仅用于 Sub2API，请同时填写对应的 Access Token")
		return
	}
	base, err := validURL(input.BaseURL)
	if err != nil {
		fail(c, 400, err.Error())
		return
	}
	if strings.TrimSpace(input.Name) == "" || len(input.Name) > 100 || (input.Kind != "newapi" && input.Kind != "sub2api") || (input.AuthMode != "token" && input.AuthMode != "password") || input.IntervalSeconds < 30 || input.IntervalSeconds > 86400 || input.QuotaPerUnit <= 0 || input.LowBalance < 0 || len(input.Currency) > 20 {
		fail(c, 400, "请检查名称、站点类型、认证方式、配额换算和采集间隔（30–86400 秒）")
		return
	}
	account := Account{Status: "pending"}
	if c.Param("id") != "" {
		id, ok := pathID(c)
		if !ok {
			return
		}
		if a.DB.First(&account, id).Error != nil {
			fail(c, 404, "账户不存在")
			return
		}
	}
	original := account
	if account.BaseURL != base || account.Kind != input.Kind || account.Username != input.Username || account.AuthMode != input.AuthMode {
		account.Cookie = ""
		account.Token = ""
		account.RefreshToken = ""
		account.Password = ""
		account.LoginExtra = ""
	}
	if account.QuotaPerUnit != input.QuotaPerUnit || account.Currency != input.Currency {
		account.Balance = nil
		account.Used = nil
		account.TodayUsed = nil
		account.TodayDate = ""
		account.Status = "pending"
	}
	account.Name, account.Kind, account.BaseURL, account.Enabled = strings.TrimSpace(input.Name), input.Kind, base, input.Enabled
	account.AuthMode, account.Username, account.UserID = input.AuthMode, input.Username, input.UserID
	account.Currency, account.QuotaPerUnit, account.LowBalance = input.Currency, input.QuotaPerUnit, input.LowBalance
	account.BlockEmpty, account.IntervalSeconds = input.BlockEmpty, input.IntervalSeconds
	if input.UseProxy != nil {
		account.UseProxy = *input.UseProxy
	}
	if input.LoginExtra != "" {
		value, err := decodeJSON([]byte(input.LoginExtra))
		_, isObject := value.(map[string]any)
		if err != nil || !isObject || len(input.LoginExtra) > 8192 {
			fail(c, 400, "登录附加参数须为不超过 8 KiB 的 JSON 对象")
			return
		}
		account.LoginExtra = a.seal(input.LoginExtra)
	}
	if input.ClearLoginExtra {
		account.LoginExtra = ""
	}
	if input.Token != "" {
		account.Token = a.seal(input.Token)
		account.RefreshToken = a.seal(input.RefreshToken)
	}
	if input.Password != "" {
		account.Password = a.seal(input.Password)
		account.Cookie = ""
		if account.AuthMode == "password" {
			account.Token = ""
			account.RefreshToken = ""
		}
	}
	if account.AuthMode == "token" && account.Token == "" {
		fail(c, 400, "请输入账户访问令牌")
		return
	}
	if account.AuthMode == "password" && (account.Username == "" || account.Password == "") {
		fail(c, 400, "请输入登录账号和密码")
		return
	}
	account.NextCheckAt = time.Now()
	account.Revision++
	if account.ID == 0 {
		err = a.DB.Create(&account).Error
	} else {
		result := a.DB.Model(&Account{}).Where("id = ? AND revision = ? AND token = ? AND refresh_token = ? AND enabled = ?", original.ID, original.Revision, original.Token, original.RefreshToken, original.Enabled).Select("*").Omit("id", "created_at").Updates(&account)
		err = result.Error
		if err == nil && result.RowsAffected == 0 {
			fail(c, 409, "账户配置或凭据已更新，请刷新后重试")
			return
		}
	}
	if err != nil {
		fail(c, 500, "保存账户失败")
		return
	}
	c.JSON(200, account)
}

func (a *App) setAccountMonitoring(c *gin.Context) {
	id, ok := pathID(c)
	if !ok {
		return
	}
	var input struct {
		Enabled *bool `json:"enabled"`
	}
	if c.ShouldBindJSON(&input) != nil || input.Enabled == nil {
		fail(c, 400, "请指定是否启用监控")
		return
	}
	var account Account
	if err := a.DB.First(&account, id).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			fail(c, 404, "账户不存在")
		} else {
			fail(c, 500, "读取账户失败")
		}
		return
	}
	updates := map[string]any{"enabled": *input.Enabled}
	if *input.Enabled && !account.Enabled {
		account.NextCheckAt = time.Now()
		updates["next_check_at"] = account.NextCheckAt
	}
	result := a.DB.Model(&Account{}).Where("id = ? AND revision = ? AND enabled = ?", account.ID, account.Revision, account.Enabled).Updates(updates)
	if result.Error != nil {
		fail(c, 500, "更新监控状态失败")
		return
	}
	if result.RowsAffected == 0 {
		fail(c, 409, "账户状态已更新，请刷新后重试")
		return
	}
	account.Enabled = *input.Enabled
	c.JSON(200, account)
}

func (a *App) deleteAccount(c *gin.Context) {
	id, ok := pathID(c)
	if !ok {
		return
	}
	var count int64
	a.DB.Model(&Channel{}).Where("account_id = ?", id).Count(&count)
	if count > 0 {
		fail(c, 409, "请先解除关联渠道")
		return
	}
	a.notificationMu.Lock()
	defer a.notificationMu.Unlock()
	err := a.DB.Transaction(func(tx *gorm.DB) error {
		var notifications []NotificationChannel
		if err := tx.Find(&notifications).Error; err != nil {
			return err
		}
		for _, channel := range notifications {
			if !slices.Contains(channel.Accounts, id) {
				continue
			}
			channel.Accounts = slices.DeleteFunc(channel.Accounts, func(accountID uint) bool { return accountID == id })
			updates := object{"accounts": jsonString(channel.Accounts)}
			if len(channel.Accounts) == 0 {
				// 最后一个订阅账户删除后暂停，避免空筛选变成通知所有账户。
				updates["enabled"] = false
			}
			if err := tx.Model(&channel).Updates(updates).Error; err != nil {
				return err
			}
		}
		for _, model := range []any{&Snapshot{}, &Alert{}, &AlertState{}} {
			if err := tx.Where("account_id = ?", id).Delete(model).Error; err != nil {
				return err
			}
		}
		return tx.Delete(&Account{}, id).Error
	})
	if err != nil {
		fail(c, 500, "删除账户失败")
		return
	}
	c.Status(204)
}

func (a *App) channels(c *gin.Context) {
	items := []Channel{}
	if a.DB.Order("id").Find(&items).Error != nil {
		fail(c, 500, "读取渠道失败")
		return
	}
	var accounts []Account
	if a.DB.Select("id", "enabled", "rates", "warnings", "status").Find(&accounts).Error != nil {
		fail(c, 500, "读取渠道倍率失败")
		return
	}
	rates := map[uint]rateSnapshot{}
	stale := map[uint]bool{}
	active := map[uint]bool{}
	for _, account := range accounts {
		active[account.ID] = account.Enabled
		rates[account.ID] = readRateSnapshot(account.Rates)
		stale[account.ID] = account.Status == "error" || strings.Contains(account.Warnings, "倍率采集失败")
	}
	result := []gin.H{}
	a.mu.Lock()
	defer a.mu.Unlock()
	for _, item := range items {
		state := a.health[item.ID]
		item.Protocols = item.supportedProtocols()
		var rate *upstreamRate
		rateStale := false
		if item.AccountID != nil {
			cached, exists := rates[*item.AccountID].Channels[item.ID]
			if exists && cached.Identity == channelRateIdentity(item) {
				value := cached.upstreamRate
				rate = &value
				rateStale = stale[*item.AccountID]
			}
		}
		paused := item.AccountID != nil && !active[*item.AccountID]
		result = append(result, gin.H{"channel": item, "models_revision": channelModelDiscoveryRevision(item), "monitoring_paused": paused, "cooldown_until": state.Until, "last_error": state.Error, "has_key": item.APIKey != "", "rate": rate, "rate_stale": rateStale})
	}
	c.JSON(200, result)
}

func (a *App) saveChannel(c *gin.Context) {
	var input struct {
		UseProxy        *bool     `json:"use_proxy"`
		Name            string    `json:"name"`
		AccountID       *uint     `json:"account_id"`
		BaseURL         string    `json:"base_url"`
		Protocol        string    `json:"protocol"`
		Protocols       []string  `json:"protocols"`
		Enabled         bool      `json:"enabled"`
		APIKey          string    `json:"api_key"`
		TimeoutSeconds  int       `json:"timeout_seconds"`
		CooldownSeconds int       `json:"cooldown_seconds"`
		Models          *[]string `json:"models"`
		ExpectedModels  *string   `json:"expected_models"`
	}
	if c.ShouldBindJSON(&input) != nil {
		fail(c, 400, "渠道参数无效")
		return
	}
	base, err := validURL(input.BaseURL)
	if err != nil {
		fail(c, 400, err.Error())
		return
	}
	if input.Protocols == nil {
		input.Protocols = []string{input.Protocol}
	}
	if len(input.Protocols) == 0 || len(input.Protocols) > 3 {
		fail(c, 400, "请至少选择一种有效的上游协议")
		return
	}
	seenProtocols := map[string]bool{}
	for _, protocol := range input.Protocols {
		if !validProtocol(protocol) || seenProtocols[protocol] {
			fail(c, 400, "上游协议无效或重复")
			return
		}
		seenProtocols[protocol] = true
	}
	if strings.TrimSpace(input.Name) == "" || len(input.Name) > 100 || input.TimeoutSeconds < 1 || input.TimeoutSeconds > 600 || input.CooldownSeconds < 1 || input.CooldownSeconds > 3600 {
		fail(c, 400, "请检查名称、协议、超时（1–600 秒）和冷却（1–3600 秒）")
		return
	}
	var linkedAccount Account
	if input.AccountID != nil {
		if a.DB.First(&linkedAccount, *input.AccountID).Error != nil {
			fail(c, 400, "关联账户不存在")
			return
		}
	}
	channel := Channel{}
	if c.Param("id") != "" {
		id, ok := pathID(c)
		if !ok {
			return
		}
		if a.DB.First(&channel, id).Error != nil {
			fail(c, 404, "渠道不存在")
			return
		}
	}
	if channel.BaseURL != "" && channel.BaseURL != base && input.APIKey == "" {
		fail(c, 400, "更换上游地址时请重新填写 API Key")
		return
	}
	original := channel
	originalEnabled, originalAutoReason := channel.Enabled, channel.AutoDisabledReason
	sameAccount := channel.AccountID == nil && input.AccountID == nil || channel.AccountID != nil && input.AccountID != nil && *channel.AccountID == *input.AccountID
	if channel.Enabled != input.Enabled || !sameAccount || channel.BaseURL != base || input.APIKey != "" {
		channel.AutoDisabledReason = ""
	}
	if !sameAccount || channel.BaseURL != base {
		channel.UpstreamKeyID = nil
	}
	if input.APIKey != "" && channel.UpstreamKeyID != nil {
		previous, err := a.unseal(channel.APIKey)
		if err != nil {
			fail(c, 500, "已有渠道密钥解密失败")
			return
		}
		if upstreamKeyDigest(linkedAccount.Kind, previous) != upstreamKeyDigest(linkedAccount.Kind, strings.TrimSpace(input.APIKey)) {
			channel.UpstreamKeyID = nil
		}
	}
	channel.Name, channel.AccountID, channel.BaseURL, channel.Enabled = strings.TrimSpace(input.Name), input.AccountID, base, input.Enabled
	channel.Protocols, channel.Protocol = input.Protocols, input.Protocols[0]
	if input.UseProxy != nil {
		channel.UseProxy = *input.UseProxy
	} else if channel.ID == 0 {
		channel.UseProxy = linkedAccount.UseProxy
	}
	channel.TimeoutSeconds, channel.CooldownSeconds = input.TimeoutSeconds, input.CooldownSeconds
	if input.APIKey != "" {
		channel.APIKey = a.seal(strings.TrimSpace(input.APIKey))
	}
	if channel.APIKey == "" {
		fail(c, 400, "请输入上游 API Key")
		return
	}
	sourceChanged := channel.BaseURL != original.BaseURL || channel.APIKey != original.APIKey || channel.UseProxy != original.UseProxy || !slices.Equal(channel.supportedProtocols(), original.supportedProtocols())
	if sourceChanged {
		channel.DiscoveredModels, channel.ModelsBaseline, channel.ModelsSyncError, channel.ModelsCheckedAt = "", "", "", nil
	}
	if input.Models != nil {
		models, err := normalizeChannelModels(*input.Models)
		if err != nil {
			fail(c, 400, err.Error())
			return
		}
		if channel.ID != 0 && input.ExpectedModels == nil {
			fail(c, 400, "保存模型列表时请提供编辑前的列表")
			return
		}
		channel.Models = jsonString(models)
	}
	if channel.ID == 0 {
		err = a.DB.Create(&channel).Error
	} else {
		query := a.DB.Model(&Channel{}).Where("id = ? AND enabled = ? AND COALESCE(auto_disabled_reason, '') = ?", channel.ID, originalEnabled, originalAutoReason).Select("*").Omit("id", "created_at")
		omit := []string{"id", "created_at"}
		if !sourceChanged {
			omit = append(omit, "discovered_models", "models_baseline", "models_sync_error", "models_checked_at")
		}
		if input.Models == nil {
			omit = append(omit, "models")
		} else {
			query = query.Where("COALESCE(models, '') = ?", *input.ExpectedModels)
		}
		query = query.Omit(omit...)
		result := query.Updates(&channel)
		err = result.Error
		if err == nil && result.RowsAffected == 0 {
			fail(c, 409, "渠道或模型列表已更新，请重新打开编辑后再保存")
			return
		}
	}
	if err != nil {
		fail(c, 500, "保存渠道失败")
		return
	}
	a.mu.Lock()
	delete(a.health, channel.ID)
	a.mu.Unlock()
	c.JSON(200, channel)
}

var errChannelInUse = errors.New("该渠道仍被模型路由引用")

func (a *App) deleteChannel(c *gin.Context) {
	id, ok := pathID(c)
	if !ok {
		return
	}
	detach := c.Query("detach_routes")
	if detach != "" && detach != "true" && detach != "false" {
		fail(c, 400, "detach_routes 必须为 true 或 false")
		return
	}
	err := a.deleteChannelRecord(c.Request.Context(), id, detach == "true")
	if errors.Is(err, errChannelInUse) {
		fail(c, 409, err.Error())
		return
	}
	if err != nil {
		fail(c, 500, "删除渠道失败")
		return
	}
	c.Status(204)
}

func (a *App) saveGroup(c *gin.Context) {
	var input struct {
		Group
		ActiveRouteIndex *int `json:"active_route_index"`
	}
	if c.ShouldBindJSON(&input) != nil {
		fail(c, 400, "模型路由参数无效")
		return
	}
	input.Name = strings.TrimSpace(input.Name)
	input.Vendor = strings.TrimSpace(input.Vendor)
	input.PriceModel = strings.TrimSpace(input.PriceModel)
	if len(input.Vendor) > 80 || len(input.PriceModel) > 250 || (input.PriceMode != "" && input.PriceMode != "auto" && input.PriceMode != "manual") {
		fail(c, 400, "厂商、计价模型或计价方式无效")
		return
	}
	input.ID = 0
	var existing Group
	if c.Param("id") != "" {
		id, ok := pathID(c)
		if !ok {
			return
		}
		if a.DB.Preload("Routes").First(&existing, id).Error != nil {
			fail(c, 404, "模型路由不存在")
			return
		}
		input.ID = id
	}
	if input.RelayConfig == nil && input.ID != 0 {
		input.RelayConfig = existing.RelayConfig
	}
	if err := input.Group.relayConfig().validate(); err != nil {
		fail(c, 400, err.Error())
		return
	}
	config := input.Group.relayConfig()
	input.RelayConfig = &config
	if input.Strategy == "" {
		input.Strategy = "manual"
		if input.ID != 0 {
			input.Strategy = existing.Strategy
		}
	}
	if input.PriceMode == "" {
		input.PriceMode = input.Group.priceMode()
		if input.ID != 0 && input.InputPriceMicros == existing.InputPriceMicros && input.OutputPriceMicros == existing.OutputPriceMicros {
			input.PriceMode = existing.priceMode()
		}
	}
	if input.Name == "" || len(input.Name) > 150 || (input.Strategy != "manual" && input.Strategy != "weighted" && input.Strategy != "priority") || len(input.Routes) < 1 || len(input.Routes) > 100 || input.InputPriceMicros < 0 || input.OutputPriceMicros < 0 || input.InputPriceMicros > 1_000_000_000_000 || input.OutputPriceMicros > 1_000_000_000_000 {
		fail(c, 400, "请设置模型名称、调度策略、价格和至少一条路由")
		return
	}
	if input.Pricing != nil && !input.Pricing.priceRates.valid() {
		fail(c, 400, "缓存单价须为 0–1000000 美元/百万 Token，留空表示未提供")
		return
	}
	activeIndex := 0
	if input.Strategy == "manual" {
		if input.ActiveRouteIndex != nil {
			activeIndex = *input.ActiveRouteIndex
		} else if input.ActiveRouteID != 0 || existing.Strategy == "manual" && existing.ActiveRouteID != 0 {
			id := input.ActiveRouteID
			if id == 0 {
				id = existing.ActiveRouteID
			}
			activeIndex = -1
			for index, route := range input.Routes {
				if route.ID == id {
					activeIndex = index
					break
				}
				for _, old := range existing.Routes {
					if old.ID == id && old.ChannelID == route.ChannelID && old.Model == route.Model {
						activeIndex = index
					}
				}
			}
		}
		if activeIndex < 0 || activeIndex >= len(input.Routes) {
			fail(c, 400, "请选择属于该模型的上游路由")
			return
		}
	}
	input.ActiveRouteID = 0
	seen := map[string]bool{}
	for i := range input.Routes {
		route := &input.Routes[i]
		if input.Strategy == "manual" && route.Weight == 0 {
			route.Weight = 1
		}
		key := fmt.Sprintf("%d/%s", route.ChannelID, route.Model)
		var channel Channel
		if route.Model == "" || len(route.Model) > 150 || route.Weight < 1 || route.Weight > 1000 || route.Priority < 0 || route.Priority > 10000 || seen[key] || a.DB.First(&channel, route.ChannelID).Error != nil {
			fail(c, 400, "路由渠道、模型、权重（1–1000）或优先级无效，也请检查重复项")
			return
		}
		seen[key] = true
		route.ID, route.GroupID = 0, input.ID
	}
	err := a.DB.Transaction(func(tx *gorm.DB) error {
		var current Group
		if input.ID != 0 {
			if err := tx.Preload("Routes").First(&current, input.ID).Error; err != nil {
				return err
			}
		}
		prepareGroupPricing(&input.Group, current)
		if err := tx.Omit("Routes").Save(&input.Group).Error; err != nil {
			return err
		}
		if err := tx.Where("group_id = ?", input.ID).Delete(&Route{}).Error; err != nil {
			return err
		}
		for i := range input.Routes {
			input.Routes[i].GroupID = input.ID
		}
		if err := tx.Create(&input.Routes).Error; err != nil {
			return err
		}
		if input.Strategy == "manual" {
			input.ActiveRouteID = input.Routes[activeIndex].ID
			return tx.Model(&input.Group).Update("active_route_id", input.ActiveRouteID).Error
		}
		return nil
	})
	if err != nil {
		fail(c, 409, "模型名称已存在或保存失败")
		return
	}
	a.mu.Lock()
	clear(a.weights)
	delete(a.groupRouting, input.ID)
	a.mu.Unlock()
	c.JSON(200, input.Group)
}

func (a *App) saveSettings(c *gin.Context) {
	var input struct {
		Runtime                     json.RawMessage `json:"runtime"`
		TelegramToken               string          `json:"telegram_token"`
		TelegramChatID              *string         `json:"telegram_chat_id"`
		ClearTelegramToken          bool            `json:"clear_telegram_token"`
		WebhookURL                  string          `json:"webhook_url"`
		WebhookSecret               string          `json:"webhook_secret"`
		ClearWebhookSecret          bool            `json:"clear_webhook_secret"`
		AlertCooldownMinutes        int             `json:"alert_cooldown_minutes"`
		RetentionDays               int             `json:"retention_days"`
		RequestSummaryRetentionDays *int            `json:"request_summary_retention_days"`
	}
	if c.ShouldBindJSON(&input) != nil || input.AlertCooldownMinutes < 1 || input.AlertCooldownMinutes > 10080 || input.RetentionDays < 1 || input.RetentionDays > 365 {
		fail(c, 400, "告警冷却应为 1–10080 分钟，历史保留应为 1–365 天")
		return
	}
	if input.WebhookURL != "" {
		u, err := url.Parse(input.WebhookURL)
		if err != nil || u.Hostname() == "" || u.User != nil || (u.Scheme != "http" && u.Scheme != "https") {
			fail(c, 400, "Webhook 地址无效")
			return
		}
	}
	var setting Setting
	if a.DB.First(&setting, 1).Error != nil {
		fail(c, 500, "读取设置失败")
		return
	}
	setting.WebhookURL, setting.AlertCooldownMinutes, setting.RetentionDays = input.WebhookURL, input.AlertCooldownMinutes, input.RetentionDays
	if len(input.Runtime) > 0 && json.Unmarshal(input.Runtime, &setting.RuntimeOptions) != nil {
		fail(c, 400, "运行设置格式无效")
		return
	}
	if err := setting.RuntimeOptions.validate(); err != nil {
		fail(c, 400, err.Error())
		return
	}
	if input.TelegramChatID != nil {
		if len(*input.TelegramChatID) > 100 {
			fail(c, 400, "Telegram Chat ID 过长")
			return
		}
		setting.TelegramChatID = strings.TrimSpace(*input.TelegramChatID)
	}
	if input.TelegramToken != "" && !telegramTokenValid(strings.TrimSpace(input.TelegramToken)) {
		fail(c, 400, "Telegram Bot Token 格式无效")
		return
	}
	if input.TelegramToken != "" {
		setting.TelegramToken = a.seal(strings.TrimSpace(input.TelegramToken))
	}
	if input.ClearTelegramToken {
		setting.TelegramToken = ""
	}
	if input.RequestSummaryRetentionDays != nil {
		setting.RequestSummaryRetentionDays = *input.RequestSummaryRetentionDays
	}
	if err := validateRetention(setting); err != nil {
		fail(c, 400, err.Error())
		return
	}
	if input.WebhookSecret != "" {
		setting.WebhookSecret = a.seal(input.WebhookSecret)
	}
	if input.ClearWebhookSecret {
		setting.WebhookSecret = ""
	}
	if a.DB.Omit("proxy_enabled", "proxy_url").Save(&setting).Error != nil {
		fail(c, 500, "保存设置失败")
		return
	}
	c.JSON(200, gin.H{"ok": true})
}

func (a *App) overview(c *gin.Context) {
	now := time.Now()
	var result struct {
		Requests            int64 `json:"requests"`
		Errors              int64 `json:"errors"`
		InputTokens         int64 `json:"input_tokens"`
		OutputTokens        int64 `json:"output_tokens"`
		CostMicros          int64 `json:"cost_micros"`
		CostUnknownRequests int64 `json:"cost_unknown_requests"`
	}
	// 按当前小时及之前 23 个小时统计，与请求趋势使用相同的小时桶。
	start := now.Truncate(time.Hour).Add(-23 * time.Hour).Unix()
	err := a.DB.Model(&RequestHour{}).Where("hour >= ? AND hour <= ?", start, now.Unix()).Select("COALESCE(SUM(requests), 0) AS requests, COALESCE(SUM(errors), 0) AS errors, COALESCE(SUM(input_tokens), 0) AS input_tokens, COALESCE(SUM(output_tokens), 0) AS output_tokens, COALESCE(SUM(cost_micros), 0) AS cost_micros, COALESCE(SUM(cost_unknown_requests), 0) AS cost_unknown_requests").Scan(&result).Error
	if err != nil {
		fail(c, 500, "读取统计失败")
		return
	}
	series := []struct {
		Hour     int64 `json:"hour"`
		Requests int64 `json:"requests"`
	}{}
	if err := a.DB.Model(&RequestHour{}).Where("hour >= ? AND hour <= ?", start, now.Unix()).Select("hour, SUM(requests) AS requests").Group("hour").Order("hour").Scan(&series).Error; err != nil {
		fail(c, 500, "读取请求趋势失败")
		return
	}
	activity, err := a.requestActivity(now)
	if err != nil {
		fail(c, 500, "读取请求活跃度失败")
		return
	}
	accountSummaries, err := a.accountOverview(now)
	if err != nil {
		fail(c, 500, "读取账户汇总失败")
		return
	}
	c.JSON(200, gin.H{"requests": result.Requests, "errors": result.Errors, "input_tokens": result.InputTokens, "output_tokens": result.OutputTokens, "cost_micros": result.CostMicros, "cost_unknown_requests": result.CostUnknownRequests, "series": series, "activity": activity, "account_summaries": accountSummaries})
}
