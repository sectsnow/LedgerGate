package app

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

func notificationConfigured(setting Setting) bool {
	if setting.NotificationType == "telegram" {
		return setting.TelegramToken != "" && setting.TelegramChatID != ""
	}
	return setting.WebhookURL != ""
}

func notificationAllowed(setting Setting, account Account, kind, detail string) bool {
	if len(setting.NotificationAccounts) > 0 && !slices.Contains(setting.NotificationAccounts, account.ID) {
		return false
	}
	if len(setting.NotificationEvents) > 0 && !slices.Contains(setting.NotificationEvents, kind) {
		return false
	}
	if kind == "rates_changed" && setting.RateChangePercent > 0 {
		var changes rateAlertDetail
		if json.Unmarshal([]byte(detail), &changes) == nil && len(changes.Changes) > 0 {
			for _, change := range changes.Changes {
				if change.Kind != "changed" || change.Percent == nil || math.Abs(*change.Percent) >= setting.RateChangePercent {
					return true
				}
			}
			return false
		}
	}
	return true
}

func (a *App) legacyNotificationChannel(setting Setting) NotificationChannel {
	return NotificationChannel{Name: "原通知渠道", Type: setting.NotificationType, Enabled: true,
		URL: a.seal(setting.WebhookURL), Secret: setting.WebhookSecret, Token: setting.TelegramToken, ChatID: setting.TelegramChatID,
		Events: setting.NotificationEvents, Accounts: setting.NotificationAccounts, Retries: setting.NotificationRetries}
}

func notificationText(value string, limit int) string {
	if len(value) <= limit {
		return value
	}
	value = value[:limit-3]
	for !utf8.ValidString(value) {
		value = value[:len(value)-1]
	}
	return value + "..."
}

func serverChanAddress(key string) (string, error) {
	if len(key) < 5 || len(key) > 512 {
		return "", errors.New("Server酱 SendKey 格式无效")
	}
	for _, r := range key {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '_' || r == '-') {
			return "", errors.New("Server酱 SendKey 格式无效")
		}
	}
	if strings.HasPrefix(key, "sctp") {
		id, _, ok := strings.Cut(key[4:], "t")
		if _, err := strconv.ParseUint(id, 10, 64); !ok || err != nil {
			return "", errors.New("Server酱³ SendKey 格式无效")
		}
		return "https://" + id + ".push.ft07.com/send/" + key + ".send", nil
	}
	if !strings.HasPrefix(key, "SCT") {
		return "", errors.New("请填写 SCT 或 sctp 开头的 SendKey")
	}
	return "https://sctapi.ftqq.com/" + key + ".send", nil
}

func (a *App) notificationPayload(channel NotificationChannel, alert Alert) (string, []byte, error) {
	text := "LedgerGate\n" + alert.Title + "\n" + describeAlert(alert).Detail
	address, err := a.unseal(channel.URL)
	if err != nil {
		return "", nil, err
	}
	switch channel.Type {
	case "telegram":
		token, err := a.unseal(channel.Token)
		if err != nil {
			return "", nil, err
		}
		body, err := json.Marshal(object{"chat_id": channel.ChatID, "text": notificationText(text, 3800), "disable_web_page_preview": true})
		return "https://api.telegram.org/bot" + token + "/sendMessage", body, err
	case "feishu":
		body := object{"msg_type": "text", "content": object{"text": notificationText(text, 20000)}}
		if channel.Secret != "" {
			secret, err := a.unseal(channel.Secret)
			if err != nil {
				return "", nil, err
			}
			stamp := strconv.FormatInt(time.Now().Unix(), 10)
			mac := hmac.New(sha256.New, []byte(stamp+"\n"+secret))
			body["timestamp"], body["sign"] = stamp, base64.StdEncoding.EncodeToString(mac.Sum(nil))
		}
		raw, err := json.Marshal(body)
		return address, raw, err
	case "wecom":
		raw, err := json.Marshal(object{"msgtype": "text", "text": object{"content": notificationText(text, 2048)}})
		return address, raw, err
	case "dingtalk":
		if channel.Secret != "" {
			secret, err := a.unseal(channel.Secret)
			if err != nil {
				return "", nil, err
			}
			stamp := strconv.FormatInt(time.Now().UnixMilli(), 10)
			mac := hmac.New(sha256.New, []byte(secret))
			mac.Write([]byte(stamp + "\n" + secret))
			u, err := url.Parse(address)
			if err != nil {
				return "", nil, errors.New("钉钉机器人地址无效")
			}
			query := u.Query()
			query.Set("timestamp", stamp)
			query.Set("sign", base64.StdEncoding.EncodeToString(mac.Sum(nil)))
			u.RawQuery, address = query.Encode(), ""
			address = u.String()
		}
		raw, err := json.Marshal(object{"msgtype": "text", "text": object{"content": notificationText(text, 20000)}})
		return address, raw, err
	case "serverchan":
		key, err := a.unseal(channel.SendKey)
		if err != nil {
			return "", nil, err
		}
		address, err := serverChanAddress(key)
		if err != nil {
			return "", nil, err
		}
		title := []rune(strings.NewReplacer("\r", " ", "\n", " ").Replace(alert.Title))
		raw, err := json.Marshal(object{"title": string(title[:min(len(title), 32)]), "desp": describeAlert(alert).Detail})
		return address, raw, err
	case "webhook":
		raw, err := json.Marshal(object{"event": alert.Kind, "title": alert.Title, "detail": describeAlert(alert).Detail, "account_id": alert.AccountID, "alert_id": alert.ID, "created_at": alert.CreatedAt})
		return address, raw, err
	}
	return "", nil, errors.New("通知类型无效")
}

func (a *App) sendNotification(ctx context.Context, channel NotificationChannel, alert Alert) error {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	if channel.Type == "email" {
		return a.sendNotificationEmail(ctx, channel, alert)
	}
	address, raw, err := a.notificationPayload(channel, alert)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, address, bytes.NewReader(raw))
	if err != nil {
		return errors.New("通知地址无效")
	}
	req.Header.Set("Content-Type", "application/json")
	if channel.Type == "webhook" && channel.Secret != "" {
		secret, err := a.unseal(channel.Secret)
		if err != nil {
			return err
		}
		mac := hmac.New(sha256.New, []byte(secret))
		mac.Write(raw)
		req.Header.Set("X-LedgerGate-Signature", "sha256="+hex.EncodeToString(mac.Sum(nil)))
	}
	resp, err := a.doRequest(req, channel.UseProxy)
	if err != nil {
		return errors.New("通知连接失败或超时，请检查网络和通知地址")
	}
	defer resp.Body.Close()
	responseBody, readErr := io.ReadAll(io.LimitReader(resp.Body, 4097))
	if readErr != nil {
		return errors.New("通知响应读取失败")
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("通知服务返回 HTTP %d", resp.StatusCode)
	}
	if channel.Type == "webhook" {
		return nil
	}
	var result object
	if len(responseBody) > 4096 || json.Unmarshal(responseBody, &result) != nil {
		return errors.New("通知服务未返回有效结果")
	}
	if channel.Type == "telegram" {
		if result["ok"] == true {
			return nil
		}
		return errors.New("Telegram 报告发送失败，请检查 Bot Token 和 Chat ID")
	}
	key := "code"
	if channel.Type == "wecom" || channel.Type == "dingtalk" {
		key = "errcode"
	}
	code, exists := result[key]
	if !exists && channel.Type == "feishu" {
		code, exists = result["StatusCode"]
	}
	value, valid := number(code)
	if !exists || !valid || value != 0 {
		return fmt.Errorf("通知平台报告发送失败（%s=%v），请检查凭据、安全设置或发送限额", key, value)
	}
	return nil
}

func telegramTokenValid(value string) bool {
	left, right, ok := strings.Cut(value, ":")
	if !ok || len(right) < 10 || len(right) > 150 {
		return false
	}
	if _, err := strconv.ParseUint(left, 10, 64); err != nil {
		return false
	}
	for _, r := range right {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '_' || r == '-') {
			return false
		}
	}
	return true
}
