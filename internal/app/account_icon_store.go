package app

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type accountIconRecord struct {
	AccountID uint     `gorm:"primaryKey;autoIncrement:false"`
	Account   *Account `gorm:"foreignKey:AccountID;constraint:OnDelete:CASCADE"`
	Origin    string
	Body      []byte
	TypeOf    string
	Manual    bool
	UpdatedAt time.Time
}

type transferAccountIcon struct {
	Body   []byte `json:"data"`
	Manual bool   `json:"manual,omitempty"`
}

func accountIconOrigin(raw string) string {
	base, err := url.Parse(raw)
	if err != nil || base.User != nil || base.Hostname() == "" || (base.Scheme != "http" && base.Scheme != "https") {
		return ""
	}
	return (&url.URL{Scheme: base.Scheme, Host: base.Host, Path: "/"}).String()
}

func (a *App) storedAccountIcon(account Account) (accountIconRecord, error) {
	var icon accountIconRecord
	err := a.DB.First(&icon, "account_id = ?", account.ID).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return accountIconRecord{}, nil
	}
	if err == nil && !icon.Manual && icon.Origin != accountIconOrigin(account.BaseURL) {
		return accountIconRecord{}, nil
	}
	return icon, err
}

func (a *App) saveAutomaticAccountIcon(account Account, body []byte, typeOf string) error {
	// 在同一条语句中确认站点未变，且自动抓取不能覆盖并发上传的手动图标。
	return a.DB.Exec(`INSERT INTO account_icon_records (account_id, origin, body, type_of, manual, updated_at)
		SELECT ?, ?, ?, ?, false, ? WHERE EXISTS (SELECT 1 FROM accounts WHERE id = ? AND base_url = ?)
		ON CONFLICT(account_id) DO UPDATE SET origin=excluded.origin, body=excluded.body,
		type_of=excluded.type_of, updated_at=excluded.updated_at WHERE account_icon_records.manual = false`,
		account.ID, accountIconOrigin(account.BaseURL), body, typeOf, time.Now(), account.ID, account.BaseURL).Error
}

func (a *App) uploadAccountIcon(c *gin.Context) {
	id, ok := pathID(c)
	if !ok {
		return
	}
	var account Account
	if err := a.DB.Select("id", "base_url", "use_proxy").First(&account, id).Error; err != nil {
		fail(c, 404, "账户不存在")
		return
	}
	var raw []byte
	if c.ContentType() == "application/json" {
		c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, 8<<10)
		var input struct {
			URL string `json:"url"`
		}
		if c.ShouldBindJSON(&input) != nil {
			fail(c, 400, "请输入有效的 HTTP(S) 图片地址")
			return
		}
		input.URL = strings.TrimSpace(input.URL)
		target, err := url.Parse(input.URL)
		if err != nil || len(input.URL) > 4096 || target.User != nil || target.Hostname() == "" || (target.Scheme != "http" && target.Scheme != "https") {
			fail(c, 400, "请输入有效的 HTTP(S) 图片地址，不支持带账号密码的地址")
			return
		}
		client, err := a.httpClient(account.UseProxy)
		if err != nil {
			fail(c, 400, err.Error())
			return
		}
		ctx, cancel := context.WithTimeout(c.Request.Context(), 8*time.Second)
		defer cancel()
		// 空源站要求每个直链和重定向目标都通过公网地址校验。
		raw, _ = readAccountIconResource(ctx, input.URL, &url.URL{}, client, accountIconDownloadLimit, true)
		if len(raw) == 0 {
			fail(c, 400, "图片下载失败；请使用可公开访问的公网图片直链，检查访问限制和 4 MiB 上限。当前 Logo 保留")
			return
		}
	} else {
		c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, accountIconDownloadLimit+(64<<10))
		file, _, err := c.Request.FormFile("file")
		if c.Request.MultipartForm != nil {
			defer c.Request.MultipartForm.RemoveAll()
		}
		if err != nil {
			fail(c, 400, "请选择不超过 4 MiB 的图片")
			return
		}
		defer file.Close()
		raw, err = io.ReadAll(io.LimitReader(file, accountIconDownloadLimit+1))
		if err != nil || len(raw) > accountIconDownloadLimit {
			fail(c, 400, "图片读取失败或超过 4 MiB 上限")
			return
		}
	}
	body, typeOf := prepareAccountIcon(raw)
	if len(body) == 0 {
		fail(c, 400, "图片无效或尺寸过大；支持 PNG、JPEG、GIF、SVG、WebP 和 ICO，超过 512 KiB 的位图请使用 PNG、JPEG 或 GIF")
		return
	}
	icon := accountIconRecord{AccountID: id, Origin: accountIconOrigin(account.BaseURL), Body: body, TypeOf: typeOf, Manual: true}
	if err := a.DB.Clauses(clause.OnConflict{Columns: []clause.Column{{Name: "account_id"}}, DoUpdates: clause.AssignmentColumns([]string{"origin", "body", "type_of", "manual", "updated_at"})}).Create(&icon).Error; err != nil {
		fail(c, 500, "保存网站图标失败")
		return
	}
	a.mu.Lock()
	delete(a.accountIcons, id)
	a.mu.Unlock()
	c.JSON(200, gin.H{"ok": true})
}

func (a *App) resetAccountIcon(c *gin.Context) {
	id, ok := pathID(c)
	if !ok {
		return
	}
	var account Account
	if a.DB.Select("id").First(&account, id).Error != nil {
		fail(c, 404, "账户不存在")
		return
	}
	if err := a.DB.Delete(&accountIconRecord{}, "account_id = ?", id).Error; err != nil {
		fail(c, 500, "恢复自动图标失败")
		return
	}
	a.mu.Lock()
	delete(a.accountIcons, id)
	a.mu.Unlock()
	c.Status(204)
}

func importAccountIcon(tx *gorm.DB, account Account, icon *transferAccountIcon) error {
	if icon == nil {
		return nil
	}
	value := accountIconRecord{AccountID: account.ID, Origin: accountIconOrigin(account.BaseURL), Body: icon.Body, TypeOf: accountIconType(icon.Body), Manual: icon.Manual}
	return tx.Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "account_id"}},
		DoUpdates: clause.AssignmentColumns([]string{"origin", "body", "type_of", "manual", "updated_at"}),
		Where:     clause.Where{Exprs: []clause.Expression{clause.Expr{SQL: "account_icon_records.manual = false AND account_icon_records.origin <> excluded.origin"}}},
	}).Create(&value).Error
}
