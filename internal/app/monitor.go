package app

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

type object = map[string]any

var errAccountRejected = errors.New("账户接口报告业务失败，请检查访问令牌或登录凭据")

func decodeJSON(data []byte) (any, error) {
	var value any
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	if err := decoder.Decode(&value); err != nil {
		return nil, err
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		return nil, errors.New("JSON 包含多段内容")
	}
	return value, nil
}

func obj(value any) object {
	result, _ := value.(map[string]any)
	if result == nil {
		return object{}
	}
	return result
}
func array(value any) []any { result, _ := value.([]any); return result }
func str(value any) string  { result, _ := value.(string); return result }
func number(value any) (float64, bool) {
	switch value := value.(type) {
	case json.Number:
		result, err := value.Float64()
		return result, err == nil
	case float64:
		return value, true
	case int:
		return float64(value), true
	case int64:
		return float64(value), true
	default:
		return 0, false
	}
}
func integer(value any) int64     { result, _ := number(value); return int64(result) }
func jsonString(value any) string { raw, _ := json.Marshal(value); return string(raw) }

func usageDay(now time.Time) time.Time {
	location := time.FixedZone("Asia/Shanghai", 8*60*60)
	local := now.In(location)
	return time.Date(local.Year(), local.Month(), local.Day(), 0, 0, 0, 0, location)
}

func endpoint(base, path string) string {
	base = strings.TrimRight(base, "/")
	if strings.HasSuffix(base, "/v1") && strings.HasPrefix(path, "/v1/") {
		path = strings.TrimPrefix(path, "/v1")
	}
	return base + path
}

func (a *App) doAccountRequest(ctx context.Context, account *Account, method, path string, payload any, responseCookies ...*[]*http.Cookie) (any, int, error) {
	var body io.Reader
	if payload != nil {
		raw, err := json.Marshal(payload)
		if err != nil {
			return nil, 0, err
		}
		body = bytes.NewReader(raw)
	}
	req, err := http.NewRequestWithContext(ctx, method, endpoint(account.BaseURL, path), body)
	if err != nil {
		return nil, 0, errors.New("账户请求地址无效")
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "LedgerGate/0.1")
	authRequest := path == "/api/user/login" || path == "/api/user/auth/refresh" || path == "/api/v1/auth/login" || path == "/api/v1/auth/refresh"
	if account.Kind == "newapi" && authRequest {
		base, _ := url.Parse(account.BaseURL)
		origin := base.Scheme + "://" + base.Host
		req.Header.Set("Origin", origin)
		req.Header.Set("Referer", origin+"/")
		if path == "/api/user/auth/refresh" {
			refresh, err := a.unseal(account.RefreshToken)
			if err != nil {
				return nil, 0, err
			}
			req.AddCookie(&http.Cookie{Name: "new_api_refresh", Value: refresh})
		}
	}
	if !authRequest {
		token, err := a.unseal(account.Token)
		if err != nil {
			return nil, 0, err
		}
		cookie, err := a.unseal(account.Cookie)
		if err != nil {
			return nil, 0, err
		}
		if token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}
		if cookie != "" {
			req.Header.Set("Cookie", cookie)
		}
	}
	if account.UserID != "" {
		req.Header.Set("New-Api-User", account.UserID)
	}
	resp, err := a.doRequest(req, account.UseProxy)
	if err != nil {
		return nil, 0, fmt.Errorf("账户接口：%w", err)
	}
	defer resp.Body.Close()
	for _, cookies := range responseCookies {
		*cookies = resp.Cookies()
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, resp.StatusCode, fmt.Errorf("账户接口返回 HTTP %d", resp.StatusCode)
	}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, (2<<20)+1))
	if err != nil || len(raw) > 2<<20 {
		return nil, resp.StatusCode, errors.New("账户接口响应读取失败或超过 2 MiB")
	}
	value, err := decodeJSON(raw)
	if err != nil {
		return nil, resp.StatusCode, errors.New("账户接口未返回合法 JSON")
	}
	mapped := obj(value)
	if success, ok := mapped["success"].(bool); ok && !success {
		return nil, resp.StatusCode, errAccountRejected
	}
	if code, ok := number(mapped["code"]); ok && code != 0 && code != 200 {
		if code == 401 || code == 403 {
			return nil, int(code), errors.New("账户接口要求重新认证")
		}
		return nil, resp.StatusCode, errAccountRejected
	}
	if data, exists := mapped["data"]; exists {
		return data, resp.StatusCode, nil
	}
	return value, resp.StatusCode, nil
}

func (a *App) loginAccount(ctx context.Context, account *Account) error {
	password, err := a.unseal(account.Password)
	if err != nil {
		return err
	}
	path := "/api/user/login"
	payload := object{"username": account.Username, "password": password}
	if account.Kind == "sub2api" {
		path = "/api/v1/auth/login"
		payload = object{"email": account.Username, "password": password}
	}
	if account.LoginExtra != "" {
		extra, err := a.unseal(account.LoginExtra)
		if err != nil {
			return err
		}
		value, err := decodeJSON([]byte(extra))
		if err != nil {
			return errors.New("登录附加参数无效")
		}
		for key, value := range obj(value) {
			if key != "username" && key != "password" && key != "email" {
				payload[key] = value
			}
		}
	}
	var cookies []*http.Cookie
	value, _, err := a.doAccountRequest(ctx, account, http.MethodPost, path, payload, &cookies)
	if err != nil {
		return err
	}
	data := obj(value)
	if account.Kind == "sub2api" {
		return a.saveSub2APITokens(ctx, account, data, false)
	} else {
		if account.UserID == "" && data["id"] != nil {
			account.UserID = fmt.Sprint(data["id"])
		}
		return a.saveNewAPISession(ctx, account, data, cookies, false)
	}
}

func (a *App) fetchAccount(ctx context.Context, account *Account) (*rateSnapshot, error) {
	now := time.Now()
	day := usageDay(now)
	date := day.Format("2006-01-02")
	if account.TodayDate != date {
		account.TodayUsed, account.TodayDate = nil, date
	}
	path := "/api/user/self"
	if account.Kind == "sub2api" {
		path = "/api/v1/auth/me"
	}
	value, _, err := a.accountRequest(ctx, account, http.MethodGet, path, nil)
	if err != nil {
		return nil, err
	}
	data := obj(value)
	balanceField, usedField := "quota", "used_quota"
	if account.Kind == "sub2api" {
		balanceField, usedField = "balance", "total_used"
	}
	balance, ok := number(data[balanceField])
	if !ok {
		return nil, fmt.Errorf("账户响应缺少数值字段 %s", balanceField)
	}
	if account.Kind == "newapi" {
		balance /= account.QuotaPerUnit
	}
	account.Balance = &balance
	if used, ok := number(data[usedField]); ok {
		if account.Kind == "newapi" {
			used /= account.QuotaPerUnit
		}
		account.Used = &used
	}
	warnings := []string{}
	if account.Kind == "sub2api" {
		stats, _, err := a.accountRequest(ctx, account, http.MethodGet, "/api/v1/usage/dashboard/stats", nil)
		if err != nil {
			warnings = append(warnings, "累计消费采集失败，显示上次可用数据（"+err.Error()+"）")
		} else if used, ok := number(obj(stats)["total_actual_cost"]); ok {
			account.Used = &used
		} else {
			warnings = append(warnings, "上游未返回累计实际消费，显示上次可用数据")
		}
		if err != nil {
			warnings = append(warnings, "今日消费采集失败，显示当日上次可用数据")
		} else if today, ok := number(obj(stats)["today_actual_cost"]); ok {
			account.TodayUsed = &today
		} else {
			warnings = append(warnings, "上游未返回今日实际消费，显示当日上次可用数据")
		}
	} else {
		path := fmt.Sprintf("/api/log/self/stat?type=0&token_name=&model_name=&start_timestamp=%d&end_timestamp=%d&group=", day.Unix(), now.Unix())
		stats, _, err := a.accountRequest(ctx, account, http.MethodGet, path, nil)
		if err != nil {
			warnings = append(warnings, "今日消费采集失败，显示当日上次可用数据（"+err.Error()+"）")
		} else if quota, ok := number(obj(stats)["quota"]); ok {
			today := quota / account.QuotaPerUnit
			account.TodayUsed = &today
		} else {
			warnings = append(warnings, "上游未返回今日消费，显示当日上次可用数据")
		}
	}
	var collectedRates *rateSnapshot
	if rates, err := a.fetchAccountRates(ctx, account); err != nil {
		warnings = append(warnings, "倍率采集失败，显示上次可用数据（"+err.Error()+"）")
	} else {
		account.Rates = jsonString(rates)
		collectedRates = &rates
	}
	if announcements, err := a.fetchAccountAnnouncements(ctx, account); err != nil {
		warnings = append(warnings, "公告采集失败，显示上次可用数据（"+err.Error()+"）")
	} else {
		account.Announcements = announcements
	}
	if account.Kind == "sub2api" {
		value, _, err := a.accountRequest(ctx, account, http.MethodGet, "/api/v1/subscriptions/progress", nil)
		if err != nil {
			warnings = append(warnings, "订阅采集失败，显示上次可用数据（"+err.Error()+"）")
		} else {
			account.Subscriptions = jsonString(value)
		}
	}
	account.Warnings = strings.Join(warnings, "；")
	return collectedRates, nil
}

func (a *App) checkDueAccounts(ctx context.Context) {
	accounts := []Account{}
	if err := a.DB.Where("enabled = ? AND next_check_at <= ?", true, time.Now()).Order("next_check_at").Limit(8).Find(&accounts).Error; err != nil {
		slog.Error("读取采集任务失败", "error", err)
		return
	}
	limit := a.runtimeOptions().MonitorConcurrency
	jobs := make(chan uint, len(accounts))
	for _, account := range accounts {
		jobs <- account.ID
	}
	close(jobs)
	var workers sync.WaitGroup
	for i := 0; i < min(limit, len(accounts)); i++ {
		workers.Go(func() {
			for id := range jobs {
				if ctx.Err() != nil {
					return
				}
				if _, err := a.collectAccount(ctx, id, true); err != nil {
					slog.Warn("账户采集未完成", "account_id", id, "error", err)
				}
			}
		})
	}
	workers.Wait()
}

func (a *App) checkAccountHandler(c *gin.Context) {
	id, ok := pathID(c)
	if !ok {
		return
	}
	account, err := a.checkAccount(c.Request.Context(), id)
	if err != nil {
		fail(c, 409, err.Error())
		return
	}
	c.JSON(200, account)
}

func (a *App) checkAccount(parent context.Context, id uint) (*Account, error) {
	return a.collectAccount(parent, id, false)
}

func (a *App) collectAccount(parent context.Context, id uint, scheduled bool) (*Account, error) {
	if !a.claimAccount(id) {
		return nil, errors.New("账户正在执行采集或密钥操作")
	}
	defer a.releaseAccount(id)
	var account Account
	if a.DB.First(&account, id).Error != nil {
		return nil, errors.New("账户不存在")
	}
	if scheduled && (!account.Enabled || account.NextCheckAt.After(time.Now())) {
		return &account, nil
	}
	previous := account
	ctx, cancel := context.WithTimeout(parent, 30*time.Second)
	defer cancel()
	collectedRates, err := a.fetchAccount(ctx, &account)
	if parent.Err() != nil {
		return nil, parent.Err()
	}
	now := time.Now()
	account.CheckedAt, account.NextCheckAt = &now, now.Add(time.Duration(account.IntervalSeconds)*time.Second)
	account.Status, account.LastError = "ok", ""
	if err != nil {
		account.Status, account.LastError = "error", err.Error()
	} else if account.LowBalance > 0 && *account.Balance < account.LowBalance {
		account.Status = "low"
	}
	var autoDisabled []uint
	err = a.DB.Transaction(func(tx *gorm.DB) error {
		updates := map[string]any{"balance": account.Balance, "used": account.Used, "today_used": account.TodayUsed, "today_date": account.TodayDate, "status": account.Status, "last_error": account.LastError, "rates": account.Rates, "subscriptions": account.Subscriptions, "announcements": account.Announcements, "warnings": account.Warnings, "checked_at": account.CheckedAt, "next_check_at": account.NextCheckAt, "token": account.Token, "refresh_token": account.RefreshToken, "cookie": account.Cookie, "user_id": account.UserID}
		result := tx.Model(&Account{}).Where("id = ? AND revision = ?", account.ID, account.Revision).Updates(updates)
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected == 0 {
			return errors.New("账户配置已更新，请重新采集")
		}
		if err := tx.Create(&Snapshot{AccountID: account.ID, Balance: account.Balance, Used: account.Used, TodayUsed: account.TodayUsed, TodayDate: account.TodayDate, Currency: account.Currency, Status: account.Status}).Error; err != nil {
			return err
		}
		if account.Status == "error" {
			return emitAlert(tx, account, "monitor_error", "账户采集异常", account.LastError)
		}
		if collectedRates != nil {
			var disableErr error
			autoDisabled, disableErr = autoDisableUnavailableChannels(tx, previous, account, *collectedRates)
			if disableErr != nil {
				return disableErr
			}
		}
		if account.Status == "low" {
			if err := emitAlert(tx, account, "low_balance", "账户余额偏低", fmt.Sprintf("余额 %.4f %s，阈值 %.4f", *account.Balance, account.Currency, account.LowBalance)); err != nil {
				return err
			}
		}
		if previous.Status == "error" {
			if err := emitAlert(tx, account, "recovered", "账户采集已恢复", "已重新获取账户余额"); err != nil {
				return err
			}
		}
		if changes := changedRates(previous.Rates, account.Rates); len(changes) > 0 {
			if err := emitAlert(tx, account, "rates_changed", "上游倍率发生变化", rateChangesDetail(changes)); err != nil {
				return err
			}
		}
		if before, after := announcementSignature(previous), announcementSignature(account); before != "" && after != "" && before != after {
			announcements, _ := parseAnnouncements(account)
			titles := make([]string, 0, len(announcements))
			for _, item := range announcements {
				titles = append(titles, item.Title)
			}
			detail := fmt.Sprintf("当前共 %d 条公告", len(announcements))
			if len(titles) > 0 {
				detail += "：" + strings.Join(titles[:min(10, len(titles))], "、")
			}
			if err := emitAlert(tx, account, "announcement", "上游公告更新", detail); err != nil {
				return err
			}
		}
		if account.Warnings != "" {
			if err := emitAlert(tx, account, "monitor_warning", "部分数据采集异常", account.Warnings); err != nil {
				return err
			}
		} else if previous.Warnings != "" {
			if err := emitAlert(tx, account, "monitor_warning_recovered", "扩展数据采集已恢复", "消费、倍率、公告和订阅等本次采集项目已正常返回"); err != nil {
				return err
			}
		}
		if account.Subscriptions != "" {
			if value, err := decodeJSON([]byte(account.Subscriptions)); err == nil {
				var setting Setting
				if err := tx.First(&setting, 1).Error; err != nil {
					return err
				}
				for _, issue := range subscriptionIssues(value, now, setting.RuntimeOptions) {
					if err := emitAlert(tx, account, issue.Kind, issue.Title, issue.Detail); err != nil {
						return err
					}
				}
			}
		}
		return nil
	})
	if err == nil && len(autoDisabled) > 0 {
		a.mu.Lock()
		for _, id := range autoDisabled {
			delete(a.health, id)
		}
		clear(a.weights)
		a.mu.Unlock()
	}
	return &account, err
}

func subscriptionIssues(value any, now time.Time, options ...RuntimeOptions) []Alert {
	threshold, expiryHours := 10.0, 72
	if len(options) > 0 {
		threshold, expiryHours = options[0].SubscriptionLowPercent, options[0].SubscriptionExpiryHours
	}
	issues := []Alert{}
	items := array(value)
	if items == nil {
		items = array(obj(value)["items"])
	}
	for _, raw := range items {
		item := obj(raw)
		progress := obj(item["progress"])
		if len(progress) == 0 {
			progress = item
		}
		expires := str(progress["expires_at"])
		if expires == "" {
			expires = str(item["expires_at"])
		}
		if expiry, err := time.Parse(time.RFC3339, expires); err == nil && expiry.Before(now.Add(time.Duration(expiryHours)*time.Hour)) {
			issues = append(issues, Alert{Kind: "subscription_expiring", Title: "订阅临近到期或已到期", Detail: "到期时间 " + expires})
		}
		for _, window := range []string{"daily", "weekly", "monthly"} {
			usage := obj(progress[window])
			remaining, ok := number(usage["remaining_usd"])
			limit, hasLimit := number(usage["limit_usd"])
			if ok && hasLimit && limit > 0 && remaining/limit <= threshold/100 {
				issues = append(issues, Alert{Kind: "subscription_" + window, Title: "订阅剩余额度偏低", Detail: fmt.Sprintf("%s 剩余 %.1f%%", window, 100*remaining/limit)})
			}
		}
	}
	return issues
}

func emitAlert(tx *gorm.DB, account Account, kind, title, detail string) error {
	var setting Setting
	if err := tx.First(&setting, 1).Error; err != nil {
		return err
	}
	var state AlertState
	err := tx.Where("account_id = ? AND kind = ?", account.ID, kind).First(&state).Error
	if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
		return err
	}
	cooling := time.Since(state.LastEmitted) < time.Duration(setting.AlertCooldownMinutes)*time.Minute
	if cooling && kind != "rates_changed" && kind != "announcement" {
		return nil
	}
	if !cooling {
		state.AccountID, state.Kind, state.LastEmitted = account.ID, kind, time.Now()
		if err := tx.Save(&state).Error; err != nil {
			return err
		}
	}
	channels := []NotificationChannel{}
	if err := tx.Where("enabled = ?", true).Order("id").Find(&channels).Error; err != nil {
		return err
	}
	recipients := []NotificationChannel{}
	for _, channel := range channels {
		filter := setting
		filter.NotificationEvents, filter.NotificationAccounts = channel.Events, channel.Accounts
		if notificationAllowed(filter, account, kind, detail) {
			recipients = append(recipients, channel)
		}
	}
	delivery := "local"
	if len(recipients) > 0 {
		delivery = "pending"
		if cooling {
			delivery = "cooldown"
		}
	}
	alert := Alert{AccountID: account.ID, Kind: kind, Title: account.Name + " · " + title, Detail: detail, Delivery: delivery, NextRetryAt: time.Now()}
	if err := tx.Create(&alert).Error; err != nil {
		return err
	}
	if delivery == "pending" {
		for _, channel := range recipients {
			row := NotificationDelivery{AlertID: &alert.ID, ChannelID: channel.ID, ChannelName: channel.Name, ChannelType: channel.Type, Title: alert.Title, Status: "pending", NextRetryAt: alert.NextRetryAt}
			if err := tx.Create(&row).Error; err != nil {
				return err
			}
		}
	}
	return nil
}

func (a *App) sendWebhook(ctx context.Context, setting Setting, alert Alert) error {
	return a.sendNotification(ctx, a.legacyNotificationChannel(setting), alert)
}

func (a *App) testWebhook(c *gin.Context) {
	var channel NotificationChannel
	if a.DB.Where("enabled = ?", true).Order("id").First(&channel).Error != nil {
		fail(c, 400, "请先添加并启用通知渠道")
		return
	}
	c.Params = append(c.Params, gin.Param{Key: "id", Value: fmt.Sprint(channel.ID)})
	a.testNotificationChannel(c)
}

func (a *App) syncModels(c *gin.Context) {
	id, ok := pathID(c)
	if !ok {
		return
	}
	var channel Channel
	if a.DB.First(&channel, id).Error != nil {
		fail(c, 404, "渠道不存在")
		return
	}
	models, err := a.syncChannelModels(c.Request.Context(), channel)
	if err != nil {
		fail(c, 502, err.Error())
		return
	}
	c.JSON(200, models)
}

func (a *App) availableChannelModels(parent context.Context, channel Channel) ([]string, error) {
	key, err := a.unseal(channel.APIKey)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(parent, 20*time.Second)
	defer cancel()
	models := []string{}
	seenAuth, seenModels := map[string]bool{}, map[string]bool{}
	succeeded := false
	lastError := errors.New("渠道未配置可用协议")
	for _, protocol := range channel.supportedProtocols() {
		if protocol == "responses" {
			protocol = "openai"
		}
		if seenAuth[protocol] {
			continue
		}
		seenAuth[protocol] = true
		items, err := a.fetchModels(ctx, channel, protocol, key)
		if err != nil {
			lastError = err
			continue
		}
		succeeded = true
		for _, model := range items {
			if !seenModels[model] {
				models = append(models, model)
				seenModels[model] = true
			}
		}
	}
	if !succeeded {
		return nil, lastError
	}
	return models, nil
}

func (a *App) syncChannelModels(parent context.Context, channel Channel) ([]string, error) {
	models, err := a.availableChannelModels(parent, channel)
	if err != nil {
		return nil, err
	}
	err = a.DB.WithContext(parent).Transaction(func(tx *gorm.DB) error {
		var current Channel
		if err := tx.First(&current, channel.ID).Error; err != nil {
			return errors.New("渠道已删除或读取失败")
		}
		if current.BaseURL != channel.BaseURL || current.APIKey != channel.APIKey || current.UseProxy != channel.UseProxy || (current.Models != channel.Models && current.Models != jsonString(models)) || !slices.Equal(current.supportedProtocols(), channel.supportedProtocols()) {
			return errors.New("渠道配置或模型列表已更新，请重新同步")
		}
		if tx.Model(&current).Update("models", jsonString(models)).Error != nil {
			return errors.New("保存模型列表失败")
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return models, nil
}

func (a *App) fetchModels(ctx context.Context, channel Channel, protocol, key string) ([]string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint(channel.BaseURL, "/v1/models"), nil)
	if err != nil {
		return nil, errors.New("模型列表地址无效")
	}
	setUpstreamAuth(req, protocol, key)
	resp, err := a.doRequest(req, channel.UseProxy)
	if err != nil {
		return nil, fmt.Errorf("模型列表：%w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("模型列表接口返回 HTTP %d", resp.StatusCode)
	}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, (2<<20)+1))
	if err != nil || len(raw) > 2<<20 {
		return nil, errors.New("模型列表响应过大或读取失败")
	}
	value, err := decodeJSON(raw)
	if err != nil {
		return nil, errors.New("模型列表 JSON 无效")
	}
	items, ok := obj(value)["data"].([]any)
	if !ok {
		return nil, errors.New("模型列表响应缺少有效列表")
	}
	models := []string{}
	for _, item := range items {
		if id := str(obj(item)["id"]); id != "" {
			models = append(models, id)
		}
	}
	return models, nil
}
