package app

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// 创建结果先落库，重试只恢复同一把上游密钥，避免网络中断后重复生成。
type channelCreation struct {
	RequestID     string   `gorm:"primaryKey"`
	AccountID     uint     `gorm:"index"`
	Account       *Account `gorm:"foreignKey:AccountID;constraint:OnDelete:CASCADE"`
	Fingerprint   string
	UpstreamName  string
	Rate          string
	Attempted     bool
	UpstreamKeyID int64
	Secret        string
	ChannelID     uint
	CreatedAt     time.Time
}

type channelCreateInput struct {
	RequestID       string   `json:"request_id"`
	Name            string   `json:"name"`
	GroupID         string   `json:"group_id"`
	Protocols       []string `json:"protocols"`
	Enabled         bool     `json:"enabled"`
	UseProxy        *bool    `json:"use_proxy,omitempty"`
	TimeoutSeconds  int      `json:"timeout_seconds"`
	CooldownSeconds int      `json:"cooldown_seconds"`
}

func (a *App) accountGroups(c *gin.Context) {
	account, ok := a.accountForKeys(c)
	if !ok {
		return
	}
	defer a.releaseAccount(account.ID)
	ctx, cancel := context.WithTimeout(c.Request.Context(), 30*time.Second)
	defer cancel()
	groups, _, err := a.fetchUpstreamGroups(ctx, account)
	if err != nil {
		fail(c, 502, "读取上游分组失败（"+err.Error()+"）")
		return
	}
	items := make([]upstreamRate, 0, len(groups))
	for _, group := range groups {
		items = append(items, group)
	}
	sort.Slice(items, func(i, j int) bool {
		if items[i].Multiplier == nil {
			return false
		}
		if items[j].Multiplier == nil {
			return true
		}
		if *items[i].Multiplier != *items[j].Multiplier {
			return *items[i].Multiplier < *items[j].Multiplier
		}
		return items[i].Group < items[j].Group
	})
	c.JSON(200, items)
}

func (a *App) findCreatedKey(ctx context.Context, account *Account, name string) (int64, error) {
	seen := 0
	var found int64
	for page := 1; page <= 100; page++ {
		items, total, err := a.requestAccountKeys(ctx, account, page, 100)
		if err != nil {
			return 0, err
		}
		for _, item := range items {
			key := obj(item)
			if str(key["name"]) == name {
				id := integer(key["id"])
				if id <= 0 || found != 0 && found != id {
					return 0, errors.New("上游返回了重复或无效的密钥记录，请在上游核对后导入")
				}
				found = id
			}
		}
		seen += len(items)
		if seen >= total {
			return found, nil
		}
		if len(items) == 0 {
			return 0, errors.New("上游密钥列表不完整")
		}
	}
	return 0, errors.New("上游密钥超过本次查询页数限制")
}

func (a *App) createAccountChannel(c *gin.Context) {
	var input channelCreateInput
	if c.ShouldBindJSON(&input) != nil {
		fail(c, 400, "渠道参数无效")
		return
	}
	input.Name = strings.TrimSpace(input.Name)
	requestBytes, err := hex.DecodeString(input.RequestID)
	if err != nil || len(requestBytes) != 16 || len(input.RequestID) != 32 || input.Name == "" || len(input.Name) > 100 || !utf8.ValidString(input.Name) || len(input.GroupID) == 0 || len(input.GroupID) > 300 || len(input.Protocols) == 0 || len(input.Protocols) > 3 || input.TimeoutSeconds < 1 || input.TimeoutSeconds > 600 || input.CooldownSeconds < 1 || input.CooldownSeconds > 3600 {
		fail(c, 400, "请检查渠道名称、上游分组、协议、超时和冷却时间")
		return
	}
	seen := map[string]bool{}
	for _, protocol := range input.Protocols {
		if !validProtocol(protocol) || seen[protocol] {
			fail(c, 400, "上游协议无效或重复")
			return
		}
		seen[protocol] = true
	}
	account, ok := a.accountForKeys(c)
	if !ok {
		return
	}
	defer a.releaseAccount(account.ID)
	ctx, cancel := context.WithTimeout(context.WithoutCancel(c.Request.Context()), 60*time.Second)
	defer cancel()
	fingerprint := digest(jsonString(object{"input": input, "account_id": account.ID, "revision": account.Revision}))
	var job channelCreation
	var available map[string]upstreamRate
	err = a.DB.WithContext(ctx).First(&job, "request_id = ?", input.RequestID).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		groups, _, groupErr := a.fetchUpstreamGroups(ctx, account)
		if groupErr != nil {
			fail(c, 502, "读取上游分组失败（"+groupErr.Error()+"）")
			return
		}
		available = groups
		rate, exists := groups[input.GroupID]
		if !exists || input.GroupID == "auto" && account.Kind == "newapi" {
			fail(c, 400, "请选择上游当前可用的具体分组")
			return
		}
		if account.Kind == "sub2api" {
			id, err := strconv.ParseInt(input.GroupID, 10, 64)
			if err != nil || id <= 0 {
				fail(c, 400, "上游分组 ID 无效")
				return
			}
		}
		prefix := input.Name
		for len(prefix) > 28 {
			_, size := utf8.DecodeLastRuneInString(prefix)
			prefix = prefix[:len(prefix)-size]
		}
		job = channelCreation{RequestID: input.RequestID, AccountID: account.ID, Fingerprint: fingerprint, UpstreamName: prefix + " [LG-" + input.RequestID[:16] + "]", Rate: jsonString(rate)}
		err = a.DB.WithContext(ctx).Create(&job).Error
	}
	if err != nil {
		fail(c, 500, "保存创建进度失败，请稍后重试")
		return
	}
	if job.AccountID != account.ID || job.Fingerprint != fingerprint {
		fail(c, 409, "本次创建的参数已固定，请保持原参数重试，或重新打开添加窗口")
		return
	}
	if job.ChannelID != 0 {
		var channel Channel
		if a.DB.First(&channel, job.ChannelID).Error != nil {
			fail(c, 409, "该次创建的本地渠道已不存在，请从上游已有密钥中重新导入")
			return
		}
		c.JSON(200, channel)
		return
	}
	if job.Attempted && job.UpstreamKeyID == 0 {
		id, err := a.findCreatedKey(ctx, account, job.UpstreamName)
		if err != nil || id == 0 {
			fail(c, 409, "上游创建结果尚未确认，请稍后重试查询，或在上游查找密钥“"+job.UpstreamName+"”；不会自动重复生成")
			return
		}
		job.UpstreamKeyID = id
		if a.DB.WithContext(ctx).Model(&job).Update("upstream_key_id", id).Error != nil {
			fail(c, 500, "上游密钥已创建，保存进度失败，请使用相同参数重试")
			return
		}
	}
	if !job.Attempted {
		if available == nil {
			available, _, err = a.fetchUpstreamGroups(ctx, account)
			if err != nil {
				fail(c, 502, "重新读取上游分组失败，请稍后重试")
				return
			}
		}
		rate, exists := available[input.GroupID]
		if !exists {
			fail(c, 400, "该分组已不再可用，请重新选择")
			return
		}
		job.Rate = jsonString(rate)
		if a.DB.WithContext(ctx).Model(&job).Updates(map[string]any{"attempted": true, "rate": job.Rate}).Error != nil {
			fail(c, 500, "保存创建进度失败，请稍后重试")
			return
		}
		job.Attempted = true
		path := "/api/v1/keys"
		payload := object{"name": job.UpstreamName}
		if account.Kind == "sub2api" {
			payload["group_id"], _ = strconv.ParseInt(input.GroupID, 10, 64)
		} else {
			path = "/api/token/"
			payload["group"], payload["expired_time"], payload["unlimited_quota"], payload["remain_quota"] = input.GroupID, -1, true, 0
			payload["model_limits_enabled"] = false
		}
		value, status, createErr := a.accountRequest(ctx, account, http.MethodPost, path, payload)
		if createErr != nil {
			if status >= 400 && status < 500 && status != http.StatusRequestTimeout || errors.Is(createErr, errAccountRejected) {
				_ = a.DB.Model(&job).Update("attempted", false).Error
				fail(c, 502, "上游拒绝创建密钥（"+createErr.Error()+"），请检查账户权限与分组后重试")
			} else {
				fail(c, 502, "上游创建结果待确认，请保持原参数重试查询；不会自动重复生成密钥")
			}
			return
		}
		job.UpstreamKeyID = integer(obj(value)["id"])
		if job.UpstreamKeyID <= 0 {
			job.UpstreamKeyID, err = a.findCreatedKey(ctx, account, job.UpstreamName)
		}
		if err != nil || job.UpstreamKeyID <= 0 {
			fail(c, 502, "上游已接受创建请求，但尚未查到密钥，请保持原参数重试查询")
			return
		}
		if secret := str(obj(value)["key"]); completeUpstreamKey(secret) {
			job.Secret = a.seal(secret)
		}
		if a.DB.WithContext(ctx).Model(&job).Updates(map[string]any{"upstream_key_id": job.UpstreamKeyID, "secret": job.Secret}).Error != nil {
			fail(c, 500, "上游密钥已创建，保存进度失败，请使用相同参数重试")
			return
		}
	}
	key, err := a.fetchImportKey(ctx, account, job.UpstreamKeyID)
	if err != nil && job.Secret != "" && key.ID == job.UpstreamKeyID && key.Name == job.UpstreamName && key.Importable {
		if secret, decodeErr := a.unseal(job.Secret); decodeErr == nil && completeUpstreamKey(secret) {
			key.Secret, err = secret, nil
		}
	}
	if err != nil {
		fail(c, 502, fmt.Sprintf("上游密钥 #%d 已创建，读取完整密钥失败（%s）。可保持原参数重试或使用导入已有密钥", job.UpstreamKeyID, err.Error()))
		return
	}
	if key.Name != job.UpstreamName || key.GroupID != input.GroupID {
		fail(c, 409, "上游密钥名称或所属分组与本次创建不符，请在上游核对后导入")
		return
	}
	var rate upstreamRate
	if json.Unmarshal([]byte(job.Rate), &rate) != nil {
		fail(c, 500, "读取创建记录失败，请使用导入已有密钥")
		return
	}
	id := job.UpstreamKeyID
	channel := Channel{Name: input.Name, AccountID: &account.ID, UpstreamKeyID: &id, BaseURL: account.BaseURL, APIKey: a.seal(key.Secret), Protocol: input.Protocols[0], Protocols: input.Protocols, Enabled: input.Enabled, UseProxy: account.UseProxy, TimeoutSeconds: input.TimeoutSeconds, CooldownSeconds: input.CooldownSeconds}
	if input.UseProxy != nil {
		channel.UseProxy = *input.UseProxy
	}
	err = a.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var current Account
		if err := tx.First(&current, account.ID).Error; err != nil {
			return err
		}
		if current.Revision != account.Revision {
			return errors.New("账户配置已更新")
		}
		result := tx.Clauses(clause.OnConflict{Columns: []clause.Column{{Name: "account_id"}, {Name: "upstream_key_id"}}, DoNothing: true}).Create(&channel)
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected == 0 {
			if err := tx.Where("account_id = ? AND upstream_key_id = ?", account.ID, id).First(&channel).Error; err != nil {
				return err
			}
		}
		snapshot := readRateSnapshot(current.Rates)
		if snapshot.Channels == nil {
			snapshot.Channels = map[uint]channelRate{}
		}
		snapshot.Channels[channel.ID] = channelRate{upstreamRate: rate, Identity: channelRateIdentity(channel)}
		if err := tx.Model(&current).Update("rates", jsonString(snapshot)).Error; err != nil {
			return err
		}
		return tx.Model(&job).Updates(map[string]any{"channel_id": channel.ID, "secret": ""}).Error
	})
	if err != nil {
		fail(c, 500, fmt.Sprintf("上游密钥 #%d 已创建，但本地保存失败，请保持原参数重试或导入已有密钥", job.UpstreamKeyID))
		return
	}
	c.JSON(201, channel)
}
