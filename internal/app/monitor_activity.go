package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
)

type rateChange struct {
	GroupID string   `json:"group_id"`
	Group   string   `json:"group"`
	Before  *float64 `json:"before"`
	After   *float64 `json:"after"`
	Kind    string   `json:"kind"`
	Percent *float64 `json:"percent"`
}

type rateAlertDetail struct {
	Summary string       `json:"summary"`
	Changes []rateChange `json:"rate_changes"`
}

type activityAlert struct {
	Alert
	Changes    []rateChange           `json:"rate_changes,omitempty"`
	Deliveries []NotificationDelivery `json:"deliveries,omitempty"`
}

func describeAlert(alert Alert) activityAlert {
	item := activityAlert{Alert: alert}
	if alert.Kind == "rates_changed" {
		var detail rateAlertDetail
		if json.Unmarshal([]byte(alert.Detail), &detail) == nil && detail.Summary != "" {
			item.Detail, item.Changes = detail.Summary, detail.Changes
		}
	}
	return item
}

func changedRates(beforeRaw, afterRaw string) []rateChange {
	before, after := readRateSnapshot(beforeRaw), readRateSnapshot(afterRaw)
	changes := []rateChange{}
	if before.Groups == nil || after.Groups == nil {
		return changes
	}
	old := map[string]upstreamRate{}
	for _, rate := range before.Groups {
		old[rate.GroupID] = rate
	}
	for _, rate := range after.Groups {
		previous, exists := old[rate.GroupID]
		delete(old, rate.GroupID)
		change := rateChange{GroupID: rate.GroupID, Group: rate.Group, Before: previous.Multiplier, After: rate.Multiplier, Kind: "changed"}
		if !exists {
			change.Kind = "added"
		} else if previous.Multiplier == nil || rate.Multiplier == nil || *previous.Multiplier == *rate.Multiplier {
			continue
		} else if *previous.Multiplier > 0 {
			percent := (*rate.Multiplier / *previous.Multiplier - 1) * 100
			change.Percent = &percent
		}
		changes = append(changes, change)
	}
	for _, rate := range old {
		changes = append(changes, rateChange{GroupID: rate.GroupID, Group: rate.Group, Before: rate.Multiplier, Kind: "removed"})
	}
	sort.Slice(changes, func(i, j int) bool { return changes[i].GroupID < changes[j].GroupID })
	return changes
}

func rateChangesDetail(changes []rateChange) string {
	value := func(rate *float64) string {
		if rate == nil {
			return "未提供"
		}
		return strconv.FormatFloat(*rate, 'f', -1, 64) + "×"
	}
	lines := make([]string, 0, len(changes))
	for _, change := range changes {
		line := change.Group + "：" + value(change.Before) + " → " + value(change.After)
		if change.Kind == "added" {
			line = change.Group + "：新增分组，倍率 " + value(change.After)
		} else if change.Kind == "removed" {
			line = change.Group + "：分组已移除，原倍率 " + value(change.Before)
		}
		lines = append(lines, line)
	}
	return jsonString(rateAlertDetail{Summary: strings.Join(lines, "\n"), Changes: changes})
}

func activityPage(c *gin.Context) (page, size int, ok bool) {
	page, err := strconv.Atoi(c.DefaultQuery("page", "1"))
	if err != nil || page < 1 || page > 10000000 {
		fail(c, 400, "页码无效")
		return 0, 0, false
	}
	size, err = strconv.Atoi(c.DefaultQuery("page_size", "50"))
	if err != nil || size < 1 || size > 200 {
		fail(c, 400, "每页条数须为 1–200")
		return 0, 0, false
	}
	return page, size, true
}

func activityAccount(c *gin.Context) (uint64, bool) {
	raw := c.Query("account_id")
	if raw == "" {
		return 0, true
	}
	id, err := strconv.ParseUint(raw, 10, 32)
	if err != nil || id == 0 {
		fail(c, 400, "账户 ID 无效")
		return 0, false
	}
	return id, true
}

func (a *App) listAlerts(c *gin.Context) {
	page, size, ok := activityPage(c)
	if !ok {
		return
	}
	accountID, ok := activityAccount(c)
	if !ok {
		return
	}
	search := strings.TrimSpace(c.Query("q"))
	if len(search) > 200 {
		fail(c, 400, "搜索内容过长")
		return
	}
	query := a.DB.WithContext(c.Request.Context()).Model(&Alert{})
	if accountID != 0 {
		query = query.Where("account_id = ?", accountID)
	}
	if kind := c.Query("kind"); len(kind) > 64 {
		fail(c, 400, "告警类型无效")
		return
	} else if kind != "" {
		query = query.Where("kind = ?", kind)
	}
	if search != "" {
		query = query.Where("title LIKE ? OR detail LIKE ?", "%"+search+"%", "%"+search+"%")
	}
	var total, pending int64
	if query.Count(&total).Error != nil || a.DB.Model(&Alert{}).Where("delivery IN ?", []string{"pending", "failed", "partial"}).Count(&pending).Error != nil {
		fail(c, 500, "读取告警数量失败")
		return
	}
	legacy := c.Request.URL.RawQuery == ""
	if legacy {
		size = 100
	}
	alerts := []Alert{}
	if query.Order("id DESC").Offset((page-1)*size).Limit(size).Find(&alerts).Error != nil {
		fail(c, 500, "读取告警记录失败")
		return
	}
	items := make([]activityAlert, 0, len(alerts))
	ids := make([]uint, 0, len(alerts))
	for _, alert := range alerts {
		ids = append(ids, alert.ID)
	}
	rows := []NotificationDelivery{}
	if len(ids) > 0 && a.DB.Where("alert_id IN ?", ids).Order("id").Find(&rows).Error != nil {
		fail(c, 500, "读取通知投递状态失败")
		return
	}
	deliveries := map[uint][]NotificationDelivery{}
	for _, row := range rows {
		if row.AlertID != nil {
			deliveries[*row.AlertID] = append(deliveries[*row.AlertID], row)
		}
	}
	for _, alert := range alerts {
		item := describeAlert(alert)
		item.Deliveries = deliveries[alert.ID]
		items = append(items, item)
	}
	if legacy {
		c.JSON(200, items)
		return
	}
	c.JSON(200, object{"items": items, "total": total, "pending": pending, "page": page, "page_size": size})
}

type announcement struct {
	ID          string     `json:"id"`
	AccountID   uint       `json:"account_id"`
	AccountName string     `json:"account_name"`
	Kind        string     `json:"kind"`
	Title       string     `json:"title"`
	Content     string     `json:"content"`
	Mode        string     `json:"mode"`
	PublishedAt *time.Time `json:"published_at"`
	UpdatedAt   *time.Time `json:"updated_at"`
	CollectedAt *time.Time `json:"collected_at"`
	Stale       bool       `json:"stale"`
}

func announcementTime(value any) *time.Time {
	if text := str(value); text != "" {
		if parsed, err := time.Parse(time.RFC3339Nano, text); err == nil {
			return &parsed
		}
	}
	if seconds, ok := number(value); ok && seconds > 0 {
		if seconds > 1e12 {
			seconds /= 1000
		}
		parsed := time.Unix(int64(seconds), 0)
		return &parsed
	}
	return nil
}

func announcementItems(value any) ([]any, error) {
	if value == nil {
		return []any{}, nil
	}
	if text, ok := value.(string); ok {
		if strings.TrimSpace(text) == "" {
			return []any{}, nil
		}
		return []any{object{"id": "notice", "title": "站点公告", "content": text}}, nil
	}
	if items, ok := value.([]any); ok {
		return items, nil
	}
	data := obj(value)
	for _, key := range []string{"items", "announcements"} {
		if items, ok := data[key].([]any); ok {
			return items, nil
		}
	}
	if _, ok := data["content"].(string); ok {
		return []any{data}, nil
	}
	return nil, errors.New("上游公告列表格式无效")
}

func parseAnnouncements(account Account) ([]announcement, error) {
	value, err := decodeJSON([]byte(account.Announcements))
	if err != nil {
		return nil, err
	}
	items, err := announcementItems(value)
	if err != nil {
		return nil, err
	}
	result := []announcement{}
	seen := map[string]bool{}
	for _, raw := range items {
		item, ok := raw.(map[string]any)
		if !ok {
			return nil, errors.New("上游公告条目格式无效")
		}
		content, title := str(item["content"]), strings.TrimSpace(str(item["title"]))
		if title == "" && strings.TrimSpace(content) == "" {
			continue
		}
		if title == "" {
			title = "站点公告"
		}
		id := str(item["id"])
		if numeric, ok := number(item["id"]); ok {
			id = strconv.FormatFloat(numeric, 'f', -1, 64)
		}
		if id == "" {
			id = digest(title + "\x00" + content)
		}
		if seen[id] {
			continue
		}
		seen[id] = true
		result = append(result, announcement{ID: fmt.Sprintf("%d/%s", account.ID, id), AccountID: account.ID, AccountName: account.Name, Kind: account.Kind, Title: title, Content: content, Mode: str(item["notify_mode"]), PublishedAt: announcementTime(item["created_at"]), UpdatedAt: announcementTime(item["updated_at"]), CollectedAt: account.CheckedAt, Stale: account.Status == "error" || strings.Contains(account.Warnings, "公告采集失败")})
	}
	return result, nil
}

func announcementSignature(account Account) string {
	items, err := parseAnnouncements(account)
	if err != nil {
		return ""
	}
	values := map[string]any{}
	for _, item := range items {
		values[item.ID] = []string{item.Title, item.Content, item.Mode}
	}
	return jsonString(values)
}

func (a *App) fetchAccountAnnouncements(ctx context.Context, account *Account) (string, error) {
	if account.Kind == "newapi" {
		value, _, err := a.accountRequest(ctx, account, http.MethodGet, "/api/notice", nil)
		if err != nil {
			return "", err
		}
		copy := *account
		copy.Announcements = jsonString(value)
		if _, err := parseAnnouncements(copy); err != nil {
			return "", err
		}
		return copy.Announcements, nil
	}
	items := []any{}
	seen := map[string]bool{}
	for page := 1; page <= 100; page++ {
		path := fmt.Sprintf("/api/v1/announcements?page=%d&page_size=100", page)
		value, _, err := a.accountRequest(ctx, account, http.MethodGet, path, nil)
		if err != nil {
			return "", err
		}
		batch, err := announcementItems(value)
		if err != nil {
			return "", err
		}
		previousCount := len(items)
		for _, item := range batch {
			if _, ok := item.(map[string]any); !ok {
				return "", errors.New("上游公告条目格式无效")
			}
			key := jsonString(obj(item)["id"])
			if key == "null" {
				key = jsonString(item)
			}
			if !seen[key] {
				seen[key] = true
				items = append(items, item)
			}
		}
		total := integer(obj(value)["total"])
		if total <= int64(len(items)) {
			return jsonString(items), nil
		}
		if len(items) == previousCount {
			return "", errors.New("上游公告分页不完整")
		}
	}
	return "", errors.New("上游公告超过本次采集页数限制")
}

func (a *App) listAnnouncements(c *gin.Context) {
	page, size, ok := activityPage(c)
	if !ok {
		return
	}
	accountID, ok := activityAccount(c)
	if !ok {
		return
	}
	search := strings.ToLower(strings.TrimSpace(c.Query("q")))
	if len(search) > 200 {
		fail(c, 400, "搜索内容过长")
		return
	}
	accounts := []Account{}
	query := a.DB.WithContext(c.Request.Context()).Select("id", "name", "kind", "announcements", "checked_at", "status", "warnings").Order("id")
	if accountID != 0 {
		query = query.Where("id = ?", accountID)
	}
	if query.Find(&accounts).Error != nil {
		fail(c, 500, "读取公告失败")
		return
	}
	items, warnings := []announcement{}, []string{}
	for _, account := range accounts {
		if account.Announcements == "" {
			warnings = append(warnings, account.Name+"：尚无公告快照，请采集账户后重试")
			continue
		}
		announcements, err := parseAnnouncements(account)
		if err != nil {
			warnings = append(warnings, account.Name+"：已保存的公告格式无法识别，请重新采集")
			continue
		}
		if account.Status == "error" || strings.Contains(account.Warnings, "公告采集失败") || strings.Contains(account.Warnings, "/api/notice 采集失败") || strings.Contains(account.Warnings, "/api/v1/announcements 采集失败") {
			warnings = append(warnings, account.Name+"：采集异常，显示上次可用公告")
			for i := range announcements {
				announcements[i].Stale = true
			}
		}
		for _, item := range announcements {
			if search == "" || strings.Contains(strings.ToLower(item.AccountName+" "+item.Title+" "+item.Content), search) {
				items = append(items, item)
			}
		}
	}
	sort.SliceStable(items, func(i, j int) bool {
		left, right := items[i].PublishedAt, items[j].PublishedAt
		if left == nil || right == nil {
			return left != nil
		}
		return left.After(*right)
	})
	total := len(items)
	start := min((page-1)*size, total)
	c.JSON(200, object{"items": items[start:min(start+size, total)], "total": total, "page": page, "page_size": size, "warnings": warnings})
}
