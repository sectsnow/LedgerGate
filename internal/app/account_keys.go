package app

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type upstreamKey struct {
	ID                int64      `json:"id"`
	Name              string     `json:"name"`
	Group             string     `json:"group"`
	GroupID           string     `json:"-"`
	Status            string     `json:"status"`
	ExpiresAt         *time.Time `json:"expires_at"`
	Importable        bool       `json:"importable"`
	ImportedChannelID uint       `json:"imported_channel_id"`
	Secret            string     `json:"-"`
}

func (a *App) claimAccount(id uint) bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.monitoring[id] {
		return false
	}
	a.monitoring[id] = true
	return true
}

func (a *App) releaseAccount(id uint) {
	a.mu.Lock()
	defer a.mu.Unlock()
	delete(a.monitoring, id)
}

func (a *App) accountForKeys(c *gin.Context) (*Account, bool) {
	id, ok := pathID(c)
	if !ok {
		return nil, false
	}
	if !a.claimAccount(id) {
		fail(c, 409, "账户正在执行采集或密钥操作，请稍后重试")
		return nil, false
	}
	var account Account
	if err := a.DB.First(&account, id).Error; err != nil {
		a.releaseAccount(id)
		if errors.Is(err, gorm.ErrRecordNotFound) {
			fail(c, 404, "账户不存在")
		} else {
			fail(c, 500, "读取账户失败")
		}
		return nil, false
	}
	return &account, true
}

func parseUpstreamKey(data object, kind string, now time.Time) (upstreamKey, error) {
	id := integer(data["id"])
	if id <= 0 {
		return upstreamKey{}, errors.New("上游密钥缺少有效 ID")
	}
	key := upstreamKey{ID: id, Name: str(data["name"]), Secret: str(data["key"]), Status: "unknown"}
	if key.Name == "" {
		key.Name = fmt.Sprintf("密钥 #%d", key.ID)
	}
	if kind == "newapi" {
		key.Group = str(data["group"])
		key.GroupID = key.Group
		switch integer(data["status"]) {
		case 1:
			key.Status = "active"
		case 2:
			key.Status = "disabled"
		case 3:
			key.Status = "expired"
		case 4:
			key.Status = "exhausted"
		}
		if seconds := integer(data["expired_time"]); seconds > 0 {
			expiry := time.Unix(seconds, 0)
			key.ExpiresAt = &expiry
		}
		remaining, hasQuota := number(data["remain_quota"])
		if hasQuota && remaining <= 0 && data["unlimited_quota"] == false {
			key.Status = "exhausted"
		}
	} else {
		key.Group = str(obj(data["group"])["name"])
		key.GroupID = strconv.FormatInt(integer(data["group_id"]), 10)
		if key.Group == "" && integer(data["group_id"]) > 0 {
			key.Group = fmt.Sprintf("分组 #%d", integer(data["group_id"]))
		}
		switch strings.ToLower(str(data["status"])) {
		case "active", "enabled":
			key.Status = "active"
		case "disabled", "inactive", "revoked":
			key.Status = "disabled"
		case "expired":
			key.Status = "expired"
		case "quota_exhausted", "exhausted":
			key.Status = "exhausted"
		}
		if value := str(data["expires_at"]); value != "" {
			expiry, err := time.Parse(time.RFC3339, value)
			if err != nil {
				return key, errors.New("上游密钥到期时间格式无效")
			}
			key.ExpiresAt = &expiry
		}
		limit, hasLimit := number(data["quota"])
		used, hasUsed := number(data["quota_used"])
		if hasLimit && hasUsed && limit > 0 && used >= limit {
			key.Status = "exhausted"
		}
	}
	if key.ExpiresAt != nil && !key.ExpiresAt.After(now) {
		key.Status = "expired"
	}
	key.Importable = key.Status == "active"
	return key, nil
}

func (a *App) listAccountKeys(c *gin.Context) {
	page, err := strconv.Atoi(c.DefaultQuery("page", "1"))
	if err != nil || page < 1 || page > 100000 {
		fail(c, 400, "页码无效")
		return
	}
	account, ok := a.accountForKeys(c)
	if !ok {
		return
	}
	defer a.releaseAccount(account.ID)
	ctx, cancel := context.WithTimeout(c.Request.Context(), 30*time.Second)
	defer cancel()
	items, total, err := a.requestAccountKeys(ctx, account, page, 20)
	if err != nil {
		fail(c, 502, "读取上游密钥列表失败（"+err.Error()+"）")
		return
	}
	var channels []Channel
	if a.DB.Where("account_id = ?", account.ID).Find(&channels).Error != nil {
		fail(c, 500, "读取已有渠道失败")
		return
	}
	imported := map[int64]uint{}
	for _, channel := range channels {
		if channel.UpstreamKeyID != nil {
			imported[*channel.UpstreamKeyID] = channel.ID
		}
	}
	keys := []upstreamKey{}
	for _, item := range items {
		key, err := parseUpstreamKey(obj(item), account.Kind, time.Now())
		if err != nil {
			fail(c, 502, err.Error())
			return
		}
		key.ImportedChannelID = imported[key.ID]
		keys = append(keys, key)
	}
	c.JSON(200, gin.H{"items": keys, "total": total, "page": page, "page_size": 20})
}

func (a *App) requestAccountKeys(ctx context.Context, account *Account, page, size int) ([]any, int, error) {
	path := fmt.Sprintf("/api/v1/keys?page=%d&page_size=%d", page, size)
	if account.Kind == "newapi" {
		path = fmt.Sprintf("/api/token/?p=%d&page_size=%d", page, size)
	}
	value, _, err := a.accountRequest(ctx, account, http.MethodGet, path, nil)
	if err != nil {
		return nil, 0, err
	}
	data := obj(value)
	items, validItems := data["items"].([]any)
	total, validTotal := number(data["total"])
	if !validItems || !validTotal || total < 0 || total > 10_000_000 || total != float64(int(total)) {
		return nil, 0, errors.New("上游密钥列表格式无效")
	}
	return items, int(total), nil
}

func completeUpstreamKey(secret string) bool {
	return secret != "" && !strings.ContainsAny(secret, "*•…\r\n\t ") && !strings.Contains(secret, "...")
}

func upstreamKeyDigest(kind, secret string) string {
	if kind == "newapi" {
		secret = strings.TrimPrefix(secret, "sk-")
	}
	return digest(secret)
}

func (a *App) fetchImportKey(ctx context.Context, account *Account, id int64) (upstreamKey, error) {
	path := fmt.Sprintf("/api/v1/keys/%d", id)
	if account.Kind == "newapi" {
		path = fmt.Sprintf("/api/token/%d", id)
	}
	value, _, err := a.accountRequest(ctx, account, http.MethodGet, path, nil)
	if err != nil {
		return upstreamKey{}, fmt.Errorf("读取密钥 #%d 失败（%w）", id, err)
	}
	key, err := parseUpstreamKey(obj(value), account.Kind, time.Now())
	if err != nil {
		return key, err
	}
	if key.ID != id {
		return key, errors.New("上游返回的密钥 ID 不匹配")
	}
	if !key.Importable {
		return key, fmt.Errorf("密钥“%s”已停用、过期、耗尽或状态未知", key.Name)
	}
	if account.Kind == "newapi" {
		value, _, err := a.accountRequest(ctx, account, http.MethodPost, fmt.Sprintf("/api/token/%d/key", id), nil)
		if err != nil {
			return key, fmt.Errorf("读取密钥 #%d 完整内容失败（%w）", id, err)
		}
		key.Secret = str(obj(value)["key"])
	}
	key.Secret = strings.TrimSpace(key.Secret)
	if !completeUpstreamKey(key.Secret) {
		return key, fmt.Errorf("上游未返回密钥 #%d 的完整内容", id)
	}
	return key, nil
}

func (a *App) importAccountChannels(c *gin.Context) {
	var input struct {
		KeyIDs    []int64  `json:"key_ids"`
		Protocols []string `json:"protocols"`
	}
	if c.ShouldBindJSON(&input) != nil || len(input.KeyIDs) == 0 || len(input.KeyIDs) > 100 || len(input.Protocols) == 0 || len(input.Protocols) > 3 {
		fail(c, 400, "请选择 1–100 把密钥和至少一种上游协议")
		return
	}
	seenProtocols, seenKeys := map[string]bool{}, map[int64]bool{}
	for _, protocol := range input.Protocols {
		if !validProtocol(protocol) || seenProtocols[protocol] {
			fail(c, 400, "上游协议无效或重复")
			return
		}
		seenProtocols[protocol] = true
	}
	for _, id := range input.KeyIDs {
		if id <= 0 || seenKeys[id] {
			fail(c, 400, "密钥 ID 无效或重复")
			return
		}
		seenKeys[id] = true
	}
	account, ok := a.accountForKeys(c)
	if !ok {
		return
	}
	defer a.releaseAccount(account.ID)
	ctx, cancel := context.WithTimeout(c.Request.Context(), 60*time.Second)
	defer cancel()
	var existing []Channel
	if a.DB.Where("account_id = ?", account.ID).Find(&existing).Error != nil {
		fail(c, 500, "读取已有渠道失败")
		return
	}
	known := map[int64]Channel{}
	for _, channel := range existing {
		if channel.UpstreamKeyID != nil {
			known[*channel.UpstreamKeyID] = channel
		}
	}
	keys := []upstreamKey{}
	skipped := []Channel{}
	for _, id := range input.KeyIDs {
		if channel, found := known[id]; found {
			skipped = append(skipped, channel)
			continue
		}
		key, err := a.fetchImportKey(ctx, account, id)
		if err != nil {
			fail(c, 502, err.Error())
			return
		}
		keys = append(keys, key)
	}
	created := []Channel{}
	err := a.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var current Account
		if err := tx.First(&current, account.ID).Error; err != nil {
			return errors.New("账户不存在")
		}
		if current.Revision != account.Revision {
			return errors.New("账户配置已更新，请重新加载密钥列表")
		}
		for _, key := range keys {
			var duplicate *Channel
			for i := range existing {
				plain, err := a.unseal(existing[i].APIKey)
				if err != nil {
					return errors.New("已有渠道密钥解密失败")
				}
				if upstreamKeyDigest(account.Kind, plain) == upstreamKeyDigest(account.Kind, key.Secret) {
					duplicate = &existing[i]
					break
				}
			}
			if duplicate != nil {
				skipped = append(skipped, *duplicate)
				continue
			}
			name := account.Name + " · " + key.Name
			for len(name) > 100 {
				_, size := utf8.DecodeLastRuneInString(name)
				name = name[:len(name)-size]
			}
			id := key.ID
			channel := Channel{Name: name, AccountID: &account.ID, UpstreamKeyID: &id, BaseURL: account.BaseURL, Protocol: input.Protocols[0], Protocols: input.Protocols, APIKey: a.seal(key.Secret), Enabled: true, UseProxy: account.UseProxy, TimeoutSeconds: 120, CooldownSeconds: 60}
			result := tx.Clauses(clause.OnConflict{Columns: []clause.Column{{Name: "account_id"}, {Name: "upstream_key_id"}}, DoNothing: true}).Create(&channel)
			if result.Error != nil {
				return errors.New("保存导入渠道失败")
			}
			if result.RowsAffected == 0 {
				if err := tx.Where("account_id = ? AND upstream_key_id = ?", account.ID, key.ID).First(&channel).Error; err != nil {
					return errors.New("读取已导入渠道失败")
				}
				skipped = append(skipped, channel)
			} else {
				created = append(created, channel)
				existing = append(existing, channel)
			}
		}
		return nil
	})
	if err != nil {
		fail(c, 409, err.Error())
		return
	}
	c.JSON(200, gin.H{"created": created, "existing": skipped})
}
