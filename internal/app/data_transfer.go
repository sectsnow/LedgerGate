package app

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"slices"
	"time"

	"ledgergate/internal/backup"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

var transferSections = []string{"accounts", "channels", "models", "keys", "settings"}
var transferLabels = map[string]string{"accounts": "上游账户", "channels": "转发渠道", "models": "模型与路由", "keys": "网关密钥", "settings": "系统设置"}

const transferRowLimit = 10000

type transferCredentials struct {
	Token        string `json:"token,omitempty"`
	RefreshToken string `json:"refresh_token,omitempty"`
	Password     string `json:"password,omitempty"`
	Cookie       string `json:"cookie,omitempty"`
	LoginExtra   string `json:"login_extra,omitempty"`
}

type transferAccount struct {
	Account
	Credentials transferCredentials `json:"credentials"`
}

type transferChannel struct {
	Channel
	Key string `json:"api_key,omitempty"`
}

type transferKey struct {
	APIKey
	Hash  string `json:"digest"`
	Token string `json:"token,omitempty"`
}

type transferSetting struct {
	Setting
	WebhookSigningKey    string                         `json:"webhook_secret,omitempty"`
	TelegramBotToken     string                         `json:"telegram_token,omitempty"`
	NotificationChannels *[]transferNotificationChannel `json:"notification_channels,omitempty"`
	ProxyOn              bool                           `json:"proxy_enabled,omitempty"`
	ProxyAddress         string                         `json:"proxy_url,omitempty"`
}

type transferNotificationChannel struct {
	NotificationChannel
	Address       string `json:"url,omitempty"`
	SigningSecret string `json:"secret,omitempty"`
	BotToken      string `json:"token,omitempty"`
	ServerChanKey string `json:"send_key,omitempty"`
	MailPassword  string `json:"smtp_password,omitempty"`
}

type transferDocument struct {
	Format             string            `json:"format"`
	Version            int               `json:"version"`
	ExportedAt         time.Time         `json:"exported_at"`
	Sections           []string          `json:"sections"`
	IncludeCredentials bool              `json:"include_credentials"`
	Accounts           []transferAccount `json:"accounts,omitempty"`
	Channels           []transferChannel `json:"channels,omitempty"`
	Models             []Group           `json:"models,omitempty"`
	Keys               []transferKey     `json:"keys,omitempty"`
	Settings           *transferSetting  `json:"settings,omitempty"`
}

type transferCount struct {
	ID      string `json:"id"`
	Label   string `json:"label"`
	Count   int    `json:"count"`
	Created int    `json:"created"`
	Skipped int    `json:"skipped"`
	Updated int    `json:"updated"`
}

func normalizeTransferSections(requested, available []string) ([]string, error) {
	if len(requested) == 0 || len(requested) > len(transferSections) {
		return nil, errors.New("请至少选择一类数据")
	}
	set := map[string]bool{}
	for _, section := range requested {
		if !slices.Contains(transferSections, section) || !slices.Contains(available, section) || set[section] {
			return nil, errors.New("选择的数据类别无效或文件中不包含该类别")
		}
		set[section] = true
	}
	if set["models"] {
		set["channels"] = true
	}
	if set["channels"] || set["settings"] {
		set["accounts"] = true
	}
	result := []string{}
	for _, section := range transferSections {
		if set[section] {
			if !slices.Contains(available, section) {
				return nil, errors.New("数据包缺少必需的关联账户或渠道")
			}
			result = append(result, section)
		}
	}
	return result, nil
}

func (doc transferDocument) counts() []transferCount {
	counts := map[string]int{"accounts": len(doc.Accounts), "channels": len(doc.Channels), "models": len(doc.Models), "keys": len(doc.Keys)}
	if doc.Settings != nil {
		counts["settings"] = 1
	}
	result := []transferCount{}
	for _, section := range doc.Sections {
		result = append(result, transferCount{ID: section, Label: transferLabels[section], Count: counts[section]})
	}
	return result
}

func (a *App) transferCatalog(c *gin.Context) {
	rows := []transferCount{}
	for i, table := range []any{&Account{}, &Channel{}, &Group{}, &APIKey{}, &Setting{}} {
		var count int64
		if err := a.DB.Model(table).Count(&count).Error; err != nil {
			fail(c, 500, "读取数据数量失败")
			return
		}
		id := transferSections[i]
		rows = append(rows, transferCount{ID: id, Label: transferLabels[id], Count: int(count)})
	}
	c.JSON(200, object{"sections": rows})
}

func (a *App) exportData(c *gin.Context) {
	var input struct {
		Password           string   `json:"password"`
		Sections           []string `json:"sections"`
		IncludeCredentials bool     `json:"include_credentials"`
	}
	if c.ShouldBindJSON(&input) != nil || len(input.Password) < 12 || len(input.Password) > 256 {
		fail(c, 400, "导出密码须为 12–256 字节")
		return
	}
	sections, err := normalizeTransferSections(input.Sections, transferSections)
	if err != nil {
		fail(c, 400, err.Error())
		return
	}
	if !a.backupMu.TryLock() {
		fail(c, 409, "正在处理备份或数据包，请稍后重试")
		return
	}
	defer a.backupMu.Unlock()
	doc := transferDocument{Format: "LedgerGate", Version: 1, ExportedAt: time.Now().UTC(), Sections: sections, IncludeCredentials: input.IncludeCredentials}
	err = a.DB.WithContext(c.Request.Context()).Transaction(func(tx *gorm.DB) error {
		accountIDs, channelIDs := []uint{}, []uint{}
		if slices.Contains(sections, "settings") {
			doc.Settings = &transferSetting{}
			if err := tx.First(&doc.Settings.Setting, 1).Error; err != nil {
				return err
			}
			accountIDs = append(accountIDs, doc.Settings.NotificationAccounts...)
			var channels []NotificationChannel
			if err := tx.Order("id").Find(&channels).Error; err != nil {
				return err
			}
			notifications := make([]transferNotificationChannel, 0, len(channels))
			for _, channel := range channels {
				notifications = append(notifications, transferNotificationChannel{NotificationChannel: channel})
				accountIDs = append(accountIDs, channel.Accounts...)
			}
			doc.Settings.NotificationChannels = &notifications
		}
		if slices.Contains(sections, "models") {
			if err := tx.Preload("Routes", func(db *gorm.DB) *gorm.DB { return db.Order("id") }).Order("id").Limit(transferRowLimit + 1).Find(&doc.Models).Error; err != nil {
				return err
			}
			for _, group := range doc.Models {
				for _, route := range group.Routes {
					channelIDs = append(channelIDs, route.ChannelID)
				}
			}
		}
		if slices.Contains(sections, "channels") {
			query := tx.Order("id").Limit(transferRowLimit + 1)
			if !slices.Contains(input.Sections, "channels") {
				query = query.Where("id IN ?", channelIDs)
			}
			var channels []Channel
			if err := query.Find(&channels).Error; err != nil {
				return err
			}
			for _, channel := range channels {
				if channel.AccountID != nil {
					accountIDs = append(accountIDs, *channel.AccountID)
				}
				doc.Channels = append(doc.Channels, transferChannel{Channel: channel})
			}
		}
		if slices.Contains(sections, "accounts") {
			query := tx.Order("id").Limit(transferRowLimit + 1)
			if !slices.Contains(input.Sections, "accounts") {
				query = query.Where("id IN ?", accountIDs)
			}
			var accounts []Account
			if err := query.Find(&accounts).Error; err != nil {
				return err
			}
			for _, account := range accounts {
				doc.Accounts = append(doc.Accounts, transferAccount{Account: account})
			}
		}
		if slices.Contains(sections, "keys") {
			var keys []APIKey
			if err := tx.Order("id").Limit(transferRowLimit + 1).Find(&keys).Error; err != nil {
				return err
			}
			for _, key := range keys {
				doc.Keys = append(doc.Keys, transferKey{APIKey: key, Hash: key.Digest})
			}
		}
		return nil
	})
	if err != nil {
		fail(c, 500, "读取导出数据失败")
		return
	}
	if err := a.prepareTransferExport(&doc); err != nil {
		fail(c, 500, err.Error())
		return
	}
	if err := doc.validate(); err != nil {
		fail(c, 400, "现有配置不能生成可导入的数据包："+err.Error())
		return
	}
	plain, err := json.Marshal(doc)
	if err != nil {
		fail(c, 500, "生成数据包失败")
		return
	}
	raw, err := backup.EncryptData(plain, input.Password)
	if err != nil {
		fail(c, 400, err.Error())
		return
	}
	c.Header("Content-Disposition", fmt.Sprintf("attachment; filename=\"ledgergate-data-%s.lgd\"", time.Now().UTC().Format("20060102-150405")))
	c.Data(200, "application/octet-stream", raw)
}

func (a *App) prepareTransferExport(doc *transferDocument) error {
	for _, count := range doc.counts() {
		if count.Count > transferRowLimit {
			return errors.New("单类配置超过 10000 条，请使用完整备份")
		}
	}
	decode := func(secret string) (string, error) {
		if !doc.IncludeCredentials {
			return "", nil
		}
		plain, err := a.unseal(secret)
		if err != nil {
			return "", errors.New("已有凭据解密失败，导出已停止")
		}
		return plain, nil
	}
	for i := range doc.Accounts {
		row := &doc.Accounts[i]
		for _, pair := range [][2]*string{{&row.Token, &row.Credentials.Token}, {&row.RefreshToken, &row.Credentials.RefreshToken}, {&row.Password, &row.Credentials.Password}, {&row.Cookie, &row.Credentials.Cookie}, {&row.LoginExtra, &row.Credentials.LoginExtra}} {
			value, err := decode(*pair[0])
			if err != nil {
				return err
			}
			*pair[1] = value
		}
		// 配置迁移不把源站的余额、采集状态和倍率快照当作目标站的实时数据。
		row.Balance, row.Used, row.TodayUsed, row.CheckedAt = nil, nil, nil, nil
		row.TodayDate, row.Status, row.LastError, row.Rates, row.Subscriptions, row.Announcements, row.Warnings = "", "", "", "", "", "", ""
		row.NextCheckAt = time.Time{}
	}
	for i := range doc.Channels {
		row := &doc.Channels[i]
		value, err := decode(row.APIKey)
		if err != nil {
			return err
		}
		row.Key = value
		if row.Models == "" {
			row.Models = "[]"
		}
		row.DiscoveredModels, row.ModelsBaseline, row.ModelsSyncError, row.AutoDisabledReason = "", "", "", ""
		row.ModelsCheckedAt = nil
	}
	for i := range doc.Keys {
		value, err := decode(doc.Keys[i].TokenCipher)
		if err != nil {
			return err
		}
		doc.Keys[i].Token = value
	}
	if doc.Settings != nil {
		var err error
		doc.Settings.WebhookSigningKey, err = decode(doc.Settings.WebhookSecret)
		if doc.Settings.NotificationChannels != nil {
			for i := range *doc.Settings.NotificationChannels {
				row := &(*doc.Settings.NotificationChannels)[i]
				for _, pair := range [][2]*string{{&row.URL, &row.Address}, {&row.Secret, &row.SigningSecret}, {&row.Token, &row.BotToken}, {&row.SendKey, &row.ServerChanKey}, {&row.SMTPPassword, &row.MailPassword}} {
					value, err := decode(*pair[0])
					if err != nil {
						return err
					}
					*pair[1] = value
				}
			}
		}
		if err != nil {
			return err
		}
		doc.Settings.TelegramBotToken, err = decode(doc.Settings.TelegramToken)
		if err != nil {
			return err
		}
		doc.Settings.ProxyAddress, err = decode(doc.Settings.ProxyURL)
		doc.Settings.ProxyOn = doc.IncludeCredentials && doc.Settings.ProxyEnabled
		if !doc.IncludeCredentials {
			// 机器人地址本身通常含有访问令牌。
			doc.Settings.WebhookURL = ""
		}
		return err
	}
	return nil
}

func readTransferUpload(c *gin.Context) (transferDocument, []byte, error) {
	doc := transferDocument{}
	if err := c.Request.ParseMultipartForm(backup.DataLimit + 4096); err != nil {
		return doc, nil, errors.New("读取数据包失败，文件上限 16 MiB")
	}
	if c.Request.MultipartForm != nil {
		defer c.Request.MultipartForm.RemoveAll()
	}
	file, header, err := c.Request.FormFile("file")
	if err != nil {
		return doc, nil, errors.New("请选择 .lgd 数据包")
	}
	defer file.Close()
	if header.Size > backup.DataLimit+48 {
		return doc, nil, errors.New("数据包超过 16 MiB")
	}
	raw, err := io.ReadAll(io.LimitReader(file, backup.DataLimit+49))
	if err != nil {
		return doc, nil, errors.New("读取数据包失败")
	}
	plain, err := backup.DecryptData(raw, c.PostForm("password"))
	if err != nil {
		return doc, nil, err
	}
	decoder := json.NewDecoder(bytes.NewReader(plain))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&doc); err != nil || decoder.Decode(new(any)) != io.EOF {
		return doc, nil, errors.New("数据包内容格式无效或版本不兼容")
	}
	if err := doc.validate(); err != nil {
		return doc, nil, err
	}
	return doc, raw, nil
}

func (a *App) transferBusy(c *gin.Context) bool {
	if !a.backupMu.TryLock() {
		fail(c, http.StatusConflict, "正在处理备份或数据包，请稍后重试")
		return true
	}
	return false
}
