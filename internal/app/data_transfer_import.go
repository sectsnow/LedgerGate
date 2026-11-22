package app

import (
	"encoding/json"
	"errors"
	"slices"
	"strconv"
	"time"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

var errTransferPreview = errors.New("只预览，不写入")

type transferPlan struct {
	FileHash  string    `json:"file_hash"`
	Sections  []string  `json:"sections"`
	Revision  string    `json:"revision"`
	Session   string    `json:"session"`
	ExpiresAt time.Time `json:"expires_at"`
}

type transferPreview struct {
	ExportedAt         time.Time       `json:"exported_at"`
	IncludeCredentials bool            `json:"include_credentials"`
	Sections           []transferCount `json:"sections"`
	Selected           []string        `json:"selected"`
	Warnings           []string        `json:"warnings"`
	Token              string          `json:"token,omitempty"`
}

func transferRevision(tx *gorm.DB) (string, error) {
	var accounts []struct {
		ID       uint
		Name     string
		Kind     string
		BaseURL  string
		Username string
		UserID   string
		Revision int
	}
	var channels []Channel
	var groups []Group
	var keys []struct {
		ID     uint
		Digest string
	}
	var setting Setting
	var notifications []NotificationChannel
	for _, query := range []*gorm.DB{
		tx.Model(&Account{}).Select("id,name,kind,base_url,username,user_id,revision").Order("id").Find(&accounts),
		tx.Order("id").Find(&channels),
		tx.Select("id,name").Order("id").Find(&groups),
		tx.Model(&APIKey{}).Select("id,digest").Order("id").Find(&keys),
		tx.First(&setting, 1),
		tx.Order("id").Find(&notifications),
	} {
		if query.Error != nil {
			return "", errors.New("读取当前配置失败")
		}
	}
	secrets := [][]string{}
	for _, row := range notifications {
		secrets = append(secrets, []string{row.URL, row.Secret, row.Token, row.SendKey, row.SMTPPassword})
	}
	return digest(jsonString(object{"accounts": accounts, "channels": channels, "groups": groups, "keys": keys, "setting": setting, "notifications": notifications, "channel_secrets": digest(jsonString(secrets)), "notification_secrets": digest(setting.WebhookSecret + setting.TelegramToken), "proxy_config": digest(setting.ProxyURL + strconv.FormatBool(setting.ProxyEnabled))})), nil
}

func (a *App) previewDataImport(c *gin.Context) { a.processDataImport(c, false) }
func (a *App) importData(c *gin.Context)        { a.processDataImport(c, true) }

func (a *App) processDataImport(c *gin.Context, apply bool) {
	if a.transferBusy(c) {
		return
	}
	defer a.backupMu.Unlock()
	doc, raw, err := readTransferUpload(c)
	if err != nil {
		fail(c, 400, err.Error())
		return
	}
	var requested []string
	if value := c.PostForm("sections"); value != "" {
		if json.Unmarshal([]byte(value), &requested) != nil {
			fail(c, 400, "导入类别格式无效")
			return
		}
	} else {
		requested = doc.Sections
	}
	selected, err := normalizeTransferSections(requested, doc.Sections)
	if err != nil {
		fail(c, 400, err.Error())
		return
	}
	if slices.Contains(selected, "settings") {
		a.notificationMu.Lock()
		defer a.notificationMu.Unlock()
	}
	cookie, _ := c.Cookie("ledgergate_session")
	plan := transferPlan{FileHash: digest(string(raw)), Sections: selected, Session: digest(cookie), ExpiresAt: time.Now().Add(15 * time.Minute)}
	if apply {
		plan = transferPlan{}
		plain, err := a.unseal(c.PostForm("token"))
		if err != nil || json.Unmarshal([]byte(plain), &plan) != nil || plan.FileHash != digest(string(raw)) || !slices.Equal(plan.Sections, selected) || plan.Session != digest(cookie) || !plan.ExpiresAt.After(time.Now()) {
			fail(c, 409, "预览已失效，请重新预览后确认")
			return
		}
	}
	result := transferPreview{ExportedAt: doc.ExportedAt, IncludeCredentials: doc.IncludeCredentials, Sections: doc.counts(), Selected: selected,
		Warnings: []string{"已有账户和渠道按身份匹配后跳过，已有同名统一模型和相同网关密钥跳过。", "新导入的账户、渠道和网关密钥默认停用；自动报价需重新同步。"}}
	if !doc.IncludeCredentials {
		result.Warnings = append(result.Warnings, "文件不含访问凭据，新账户和渠道需补填凭据；通知机器人地址和密钥也需重新配置。")
	}
	if slices.Contains(selected, "settings") {
		result.Warnings = append(result.Warnings, "已选择系统设置：确认导入后将替换当前通知、采集参数与历史保留设置。管理员密码和登录会话保留。")
	}
	err = a.DB.WithContext(c.Request.Context()).Transaction(func(tx *gorm.DB) error {
		revision, err := transferRevision(tx)
		if err != nil {
			return err
		}
		if apply && plan.Revision != revision {
			return errors.New("当前配置已变化，请重新预览后确认")
		}
		if err := a.mergeTransfer(tx, doc, selected, result.Sections); err != nil {
			return err
		}
		if !apply {
			plan.Revision = revision
			result.Token = a.seal(jsonString(plan))
			return errTransferPreview
		}
		return nil
	})
	if err != nil && !errors.Is(err, errTransferPreview) {
		fail(c, 409, err.Error())
		return
	}
	c.JSON(200, result)
}

func (a *App) mergeTransfer(tx *gorm.DB, doc transferDocument, selected []string, counts []transferCount) error {
	count := func(section string, created bool) {
		for i := range counts {
			if counts[i].ID == section {
				if created {
					counts[i].Created++
				} else {
					counts[i].Skipped++
				}
			}
		}
	}
	accountIDs, channelIDs := map[uint]uint{}, map[uint]uint{}
	if slices.Contains(selected, "accounts") {
		for _, source := range doc.Accounts {
			var existing Account
			err := tx.Where("name=? AND kind=? AND base_url=? AND username=? AND user_id=?", source.Name, source.Kind, source.BaseURL, source.Username, source.UserID).Order("id").First(&existing).Error
			if err == nil {
				accountIDs[source.ID] = existing.ID
				count("accounts", false)
				continue
			}
			if !errors.Is(err, gorm.ErrRecordNotFound) {
				return errors.New("读取已有账户失败")
			}
			value := source.Account
			value.ID, value.Enabled, value.Revision, value.Status = 0, false, 1, "pending"
			value.Token, value.RefreshToken, value.Password, value.Cookie, value.LoginExtra = a.seal(source.Credentials.Token), a.seal(source.Credentials.RefreshToken), a.seal(source.Credentials.Password), a.seal(source.Credentials.Cookie), a.seal(source.Credentials.LoginExtra)
			value.Balance, value.Used, value.TodayUsed, value.CheckedAt = nil, nil, nil, nil
			value.TodayDate, value.LastError, value.Rates, value.Subscriptions, value.Announcements, value.Warnings = "", "", "", "", "", ""
			value.NextCheckAt = time.Now()
			if err := tx.Create(&value).Error; err != nil {
				return errors.New("导入账户失败，全部变更已回滚")
			}
			accountIDs[source.ID] = value.ID
			count("accounts", true)
		}
	}
	if slices.Contains(selected, "channels") {
		for _, source := range doc.Channels {
			value := source.Channel
			value.Account, value.ID, value.Enabled, value.APIKey = nil, 0, false, a.seal(source.Key)
			value.AutoDisabledReason, value.DiscoveredModels, value.ModelsSyncError, value.ModelsCheckedAt = "", "", "", nil
			query := tx
			if source.AccountID != nil {
				id := accountIDs[*source.AccountID]
				if id == 0 {
					return errors.New("渠道缺少已导入的关联账户")
				}
				value.AccountID = &id
				query = query.Where("account_id=?", id)
			} else {
				query = query.Where("account_id IS NULL")
			}
			if source.UpstreamKeyID != nil {
				query = query.Where("upstream_key_id=?", *source.UpstreamKeyID)
			} else {
				query = query.Where("name=? AND base_url=? AND protocol=? AND upstream_key_id IS NULL", value.Name, value.BaseURL, value.Protocol)
			}
			var existing Channel
			err := query.Order("id").First(&existing).Error
			if err == nil {
				channelIDs[source.ID] = existing.ID
				count("channels", false)
				continue
			}
			if !errors.Is(err, gorm.ErrRecordNotFound) {
				return errors.New("读取已有渠道失败")
			}
			if err := tx.Omit("Account").Create(&value).Error; err != nil {
				return errors.New("导入渠道失败，全部变更已回滚")
			}
			channelIDs[source.ID] = value.ID
			count("channels", true)
		}
	}
	if slices.Contains(selected, "models") {
		for _, source := range doc.Models {
			var existing Group
			err := tx.Where("name=?", source.Name).First(&existing).Error
			if err == nil {
				count("models", false)
				continue
			}
			if !errors.Is(err, gorm.ErrRecordNotFound) {
				return errors.New("读取已有模型失败")
			}
			value := source
			value.ID, value.ActiveRouteID, value.Routes = 0, 0, nil
			if err := tx.Omit("Routes").Create(&value).Error; err != nil {
				return errors.New("导入模型失败，全部变更已回滚")
			}
			for _, sourceRoute := range source.Routes {
				route := sourceRoute
				route.ID, route.GroupID, route.ChannelID, route.Channel = 0, value.ID, channelIDs[sourceRoute.ChannelID], nil
				if route.ChannelID == 0 {
					return errors.New("模型路由缺少已导入的渠道")
				}
				if route.Pricing != nil {
					price := *route.Pricing
					price.ChannelIdentity, price.Stale = "", true
					route.Pricing = &price
				}
				if err := tx.Create(&route).Error; err != nil {
					return errors.New("导入模型路由失败，全部变更已回滚")
				}
				if source.ActiveRouteID == sourceRoute.ID {
					if err := tx.Model(&value).Update("active_route_id", route.ID).Error; err != nil {
						return errors.New("导入当前手动路由失败，全部变更已回滚")
					}
				}
			}
			count("models", true)
		}
	}
	if slices.Contains(selected, "keys") {
		for _, source := range doc.Keys {
			var existing APIKey
			err := tx.Where("digest=?", source.Hash).First(&existing).Error
			if err == nil {
				count("keys", false)
				continue
			}
			if !errors.Is(err, gorm.ErrRecordNotFound) {
				return errors.New("读取已有网关密钥失败")
			}
			value := source.APIKey
			value.ID, value.Enabled, value.Digest, value.TokenCipher = 0, false, source.Hash, a.seal(source.Token)
			if err := tx.Create(&value).Error; err != nil {
				return errors.New("导入网关密钥失败，全部变更已回滚")
			}
			count("keys", true)
		}
	}
	if slices.Contains(selected, "settings") {
		var current Setting
		if err := tx.First(&current, 1).Error; err != nil {
			return errors.New("读取当前系统设置失败")
		}
		seal := func(cipher, plain string) string {
			if previous, err := a.unseal(cipher); err == nil && previous == plain {
				return cipher
			}
			return a.seal(plain)
		}
		value := doc.Settings.Setting
		value.ID, value.NotificationsMigrated = 1, true
		value.WebhookURL, value.WebhookSecret, value.TelegramToken, value.TelegramChatID = "", "", "", ""
		value.ProxyEnabled, value.ProxyURL = doc.Settings.ProxyOn, seal(current.ProxyURL, doc.Settings.ProxyAddress)
		value.NotificationAccounts = []uint{}
		for _, sourceID := range doc.Settings.NotificationAccounts {
			id := accountIDs[sourceID]
			if id == 0 {
				return errors.New("通知筛选缺少关联账户")
			}
			value.NotificationAccounts = append(value.NotificationAccounts, id)
		}
		if err := a.importNotificationChannels(tx, *doc.Settings, accountIDs); err != nil {
			return err
		}
		if err := tx.Save(&value).Error; err != nil {
			return errors.New("导入系统设置失败，全部变更已回滚")
		}
		for i := range counts {
			if counts[i].ID == "settings" {
				counts[i].Updated = 1
			}
		}
	}
	return nil
}
