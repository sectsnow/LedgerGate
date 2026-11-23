package app

import (
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"slices"
	"strings"
)

func transferName(name string, limit int) bool {
	return name != "" && name == strings.TrimSpace(name) && len(name) <= limit && !strings.ContainsAny(name, "\x00\r\n")
}

func transferPricing(price *modelPricing) bool {
	if price == nil {
		return true
	}
	if !slices.Contains([]string{"token", "image", "video", "request"}, price.Mode) || !price.priceRates.valid() || !validPrice(price.UnitPrice) || len(price.Tiers) > 100 {
		return false
	}
	for _, tier := range price.Tiers {
		if tier.Min < 0 || tier.Max != nil && *tier.Max <= tier.Min || !tier.priceRates.valid() || !validPrice(tier.UnitPrice) {
			return false
		}
	}
	return true
}

func (doc transferDocument) validate() error {
	if doc.Format != "LedgerGate" || doc.Version != 1 || doc.ExportedAt.IsZero() {
		return errors.New("只支持 LedgerGate v1 配置数据包")
	}
	if _, err := normalizeTransferSections(doc.Sections, doc.Sections); err != nil {
		return err
	}
	for _, row := range doc.counts() {
		if row.Count > transferRowLimit {
			return errors.New("单类配置超过 10000 条")
		}
	}
	if len(doc.Accounts) > 0 && !slices.Contains(doc.Sections, "accounts") || len(doc.Channels) > 0 && !slices.Contains(doc.Sections, "channels") || len(doc.Models) > 0 && !slices.Contains(doc.Sections, "models") || len(doc.Keys) > 0 && !slices.Contains(doc.Sections, "keys") || (doc.Settings != nil) != slices.Contains(doc.Sections, "settings") {
		return errors.New("数据包内容与所列类别不一致")
	}
	accountIDs, channelIDs, modelIDs, keyIDs := map[uint]bool{}, map[uint]bool{}, map[uint]bool{}, map[uint]bool{}
	addID := func(ids map[uint]bool, id uint) bool {
		if id == 0 || ids[id] {
			return false
		}
		ids[id] = true
		return true
	}
	for _, row := range doc.Accounts {
		base, err := validURL(row.BaseURL)
		if !addID(accountIDs, row.ID) || !transferName(row.Name, 100) || !slices.Contains([]string{"newapi", "sub2api"}, row.Kind) || !slices.Contains([]string{"token", "password"}, row.AuthMode) || err != nil || base != row.BaseURL || row.IntervalSeconds < 30 || row.IntervalSeconds > 86400 || row.QuotaPerUnit <= 0 || row.LowBalance < 0 || len(row.Currency) > 20 || len(row.Username) > 250 || len(row.UserID) > 100 {
			return errors.New("账户配置无效，请检查名称、地址、认证方式和采集间隔")
		}
		for _, secret := range []string{row.Credentials.Token, row.Credentials.RefreshToken, row.Credentials.Password, row.Credentials.Cookie} {
			if len(secret) > 8192 || !doc.IncludeCredentials && secret != "" {
				return errors.New("账户凭据格式无效")
			}
		}
		if extra := row.Credentials.LoginExtra; extra != "" {
			var value map[string]any
			if !doc.IncludeCredentials || len(extra) > 16384 || json.Unmarshal([]byte(extra), &value) != nil || value == nil {
				return errors.New("附加登录参数须为 JSON 对象")
			}
			for _, key := range []string{"username", "email", "password"} {
				if _, exists := value[key]; exists {
					return errors.New("附加登录参数不能覆盖账号或密码")
				}
			}
		}
	}
	for _, row := range doc.Channels {
		base, err := validURL(row.BaseURL)
		if !addID(channelIDs, row.ID) || !transferName(row.Name, 100) || err != nil || base != row.BaseURL || row.TimeoutSeconds < 1 || row.TimeoutSeconds > 600 || row.CooldownSeconds < 1 || row.CooldownSeconds > 3600 || row.AccountID != nil && !accountIDs[*row.AccountID] || row.UpstreamKeyID != nil && (*row.UpstreamKeyID <= 0 || row.AccountID == nil) {
			return errors.New("渠道配置或关联账户无效")
		}
		if len(row.Protocols) < 1 || len(row.Protocols) > 3 || row.Protocol != row.Protocols[0] {
			return errors.New("渠道协议配置无效")
		}
		protocols := map[string]bool{}
		for _, protocol := range row.Protocols {
			if !validProtocol(protocol) || protocols[protocol] {
				return errors.New("渠道协议无效或重复")
			}
			protocols[protocol] = true
		}
		if _, err := readChannelModels(row.Models); err != nil {
			return errors.New("渠道模型列表无效")
		}
		if len(row.Key) > 8192 || strings.ContainsAny(row.Key, "\x00\r\n") || !doc.IncludeCredentials && row.Key != "" {
			return errors.New("渠道密钥格式无效")
		}
	}
	modelNames := map[string]bool{}
	for _, group := range doc.Models {
		if err := group.relayConfig().validate(); err != nil {
			return err
		}
		if !addID(modelIDs, group.ID) || !transferName(group.Name, 150) || modelNames[group.Name] || len(group.Vendor) > 80 || len(group.PriceModel) > 250 || !slices.Contains([]string{"manual", "weighted", "priority"}, group.Strategy) || !slices.Contains([]string{"", "auto", "manual"}, group.PriceMode) || !validPrice(&group.InputPriceMicros) || !validPrice(&group.OutputPriceMicros) || !transferPricing(group.Pricing) || len(group.Routes) > 100 {
			return errors.New("统一模型、价格或调度策略无效")
		}
		modelNames[group.Name] = true
		routeIDs, routes := map[uint]bool{}, map[string]bool{}
		for _, route := range group.Routes {
			key := fmt.Sprintf("%d/%s", route.ChannelID, route.Model)
			if !addID(routeIDs, route.ID) || route.GroupID != group.ID || !channelIDs[route.ChannelID] || !transferName(route.Model, 150) || route.Weight < 1 || route.Weight > 1000 || route.Priority < 0 || route.Priority > 10000 || routes[key] || !transferPricing(route.Pricing) {
				return errors.New("模型路由或关联渠道无效")
			}
			routes[key] = true
		}
		if group.ActiveRouteID != 0 && !routeIDs[group.ActiveRouteID] {
			return errors.New("当前手动路由不属于该模型")
		}
	}
	keyHashes := map[string]bool{}
	for _, row := range doc.Keys {
		input := keyInput{Name: row.Name, AllowedModels: row.AllowedModels, BudgetMicros: row.BudgetMicros}
		hash, err := hex.DecodeString(row.Hash)
		if !addID(keyIDs, row.ID) || input.validate() != nil || row.UsedMicros < 0 || len(row.Prefix) > 30 || err != nil || len(hash) != 32 || keyHashes[row.Hash] || row.Token != "" && (len(row.Token) > 8192 || digest(row.Token) != row.Hash) || !doc.IncludeCredentials && row.Token != "" {
			return errors.New("网关密钥配置、摘要或累计费用无效")
		}
		keyHashes[row.Hash] = true
	}
	if row := doc.Settings; row != nil {
		if row.NotificationChannels != nil {
			if len(*row.NotificationChannels) > notificationChannelLimit {
				return errors.New("通知渠道最多 100 条")
			}
			ids, names := map[uint]bool{}, map[string]bool{}
			for _, source := range *row.NotificationChannels {
				value := source.NotificationChannel
				value.URL, value.Secret, value.Token, value.SendKey, value.SMTPPassword = source.Address, source.SigningSecret, source.BotToken, source.ServerChanKey, source.MailPassword
				if !addID(ids, source.ID) || names[source.Name] || validateNotificationConfig(value, false) != nil {
					return errors.New("通知渠道配置无效或名称重复")
				}
				names[source.Name] = true
				for _, id := range source.Accounts {
					if !accountIDs[id] {
						return errors.New("通知渠道缺少关联账户")
					}
				}
				if !doc.IncludeCredentials && (source.Address != "" || source.SigningSecret != "" || source.BotToken != "" || source.ServerChanKey != "" || source.MailPassword != "") {
					return errors.New("数据包的凭据标记与通知内容不一致")
				}
			}
		}
		if row.ProxyAddress != "" {
			if _, err := parseProxyAddress(row.ProxyAddress); err != nil {
				return errors.New("系统设置中的代理配置无效")
			}
		}
		if row.ProxyOn && row.ProxyAddress == "" || !doc.IncludeCredentials && (row.ProxyAddress != "" || row.ProxyOn) {
			return errors.New("数据包的代理配置与凭据标记不一致")
		}
		if row.RuntimeOptions.validate() != nil || validateRetention(row.Setting) != nil || row.AlertCooldownMinutes < 1 || row.AlertCooldownMinutes > 10080 || len(row.TelegramChatID) > 100 || len(row.TelegramBotToken) > 8192 || row.TelegramBotToken != "" && !telegramTokenValid(row.TelegramBotToken) || len(row.WebhookSigningKey) > 8192 {
			return errors.New("系统设置中的运行参数、通知或保留周期无效")
		}
		for _, id := range row.NotificationAccounts {
			if !accountIDs[id] {
				return errors.New("通知账户筛选缺少关联账户")
			}
		}
		if row.WebhookURL != "" {
			u, err := url.Parse(row.WebhookURL)
			if err != nil || len(row.WebhookURL) > 8192 || u.Hostname() == "" || u.User != nil || u.Scheme != "http" && u.Scheme != "https" {
				return errors.New("通知地址无效")
			}
		}
		if !doc.IncludeCredentials && (row.WebhookURL != "" || row.WebhookSigningKey != "" || row.TelegramBotToken != "") {
			return errors.New("数据包的凭据标记与通知内容不一致")
		}
	}
	return nil
}
