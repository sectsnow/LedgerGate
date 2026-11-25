package app

import (
	"encoding/json"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

// 与内存正文分表；仅保存白名单元数据和固定分类，不落盘上游错误原文。
type RequestSummary struct {
	ID          uint      `gorm:"primaryKey;autoIncrement:false"`
	RequestID   string    `gorm:"index;size:96"`
	Model       string    `gorm:"index;size:160"`
	ChannelName string    `gorm:"size:160"`
	KeyName     string    `gorm:"size:160"`
	State       string    `gorm:"index"`
	CreatedAt   time.Time `gorm:"index"`
	Data        string    `json:"-"`
}

func shortMetadata(s string) string {
	if len(s) > 160 {
		return strings.ToValidUTF8(s[:160], "")
	}
	return s
}
func routingClass(reason string) string {
	switch {
	case strings.Contains(reason, "探测"):
		return "恢复探测"
	case strings.Contains(reason, "重试"):
		return "成员重试"
	case strings.Contains(reason, "备用") || strings.Contains(reason, "切换"):
		return "切换可用成员"
	case strings.Contains(reason, "等待"):
		return "等待成员可用"
	default:
		return ""
	}
}
func diagnosticClass(status int) string {
	switch {
	case status == 499:
		return "客户端连接中断或取消"
	case status == 429:
		return "请求受限，请检查上游或密钥限制"
	case status == 401 || status == 403:
		return "认证或权限被拒绝"
	case status == 400 || status == 422:
		return "请求参数或能力不匹配"
	case status == 504 || status == 408:
		return "请求等待超时"
	case status >= 500:
		return "上游连接、网关处理或服务异常"
	case status >= 400:
		return "请求失败，请结合状态码排查"
	default:
		return ""
	}
}
func saveRequestSummary(tx *gorm.DB, log RequestLog, payload requestPayload, options RuntimeOptions) error {
	log.CostDetail = nil
	log.SummaryOnly = true
	log.Error = diagnosticClass(log.Status)
	log.RoutingReason = routingClass(log.RoutingReason)
	log.RetryAt = nil
	log.AccountName = shortMetadata(log.AccountName)
	log.Model = shortMetadata(log.Model)
	log.UpstreamModel = shortMetadata(log.UpstreamModel)
	log.ChannelName = shortMetadata(log.ChannelName)
	log.KeyName = shortMetadata(log.KeyName)
	log.RequestID = shortMetadata(log.RequestID)
	log.CostWarning = ""
	log.CostSource = ""
	log.UsageWarning = ""
	log.ReasoningEffort = ""
	log.FinishReason = ""
	log.IncompleteReason = ""
	attempts := make([]requestAttempt, 0, min(len(payload.Attempts), 32))
	for _, item := range payload.Attempts[:min(len(payload.Attempts), 32)] {
		item.Error = diagnosticClass(item.Status)
		item.Reason = routingClass(item.Reason)
		item.IncompleteReason = ""
		item.ChannelName = shortMetadata(item.ChannelName)
		item.Model = shortMetadata(item.Model)
		item.UpstreamRequestID = shortMetadata(item.UpstreamRequestID)
		item.Protocol = shortMetadata(item.Protocol)
		item.FinishReason = ""
		attempts = append(attempts, item)
	}
	body, err := json.Marshal(gin.H{"request": log, "detail": requestPayload{Available: false, Attempts: attempts}})
	if err != nil {
		return err
	}
	row := RequestSummary{ID: log.ID, RequestID: log.RequestID, Model: log.Model, ChannelName: log.ChannelName, KeyName: log.KeyName, State: log.State, CreatedAt: log.CreatedAt, Data: string(body)}
	if err := tx.Create(&row).Error; err != nil {
		return err
	}
	return trimRequestSummaries(tx, time.Now(), options)
}
func trimRequestSummaries(tx *gorm.DB, now time.Time, options RuntimeOptions) error {
	days, limit := options.RequestDigestDays, options.RequestDigestLimit
	if days == 0 {
		days = 7
	}
	if limit == 0 {
		limit = 5000
	}
	if err := tx.Where("created_at < ?", now.AddDate(0, 0, -days)).Delete(&RequestSummary{}).Error; err != nil {
		return err
	}
	return tx.Where("id IN (?)", tx.Model(&RequestSummary{}).Select("id").Order("id DESC").Offset(limit).Limit(-1)).Delete(&RequestSummary{}).Error
}
func (a *App) listRequestSummaries(c *gin.Context) {
	c.Header("Cache-Control", "no-store")
	page, err := strconv.Atoi(c.DefaultQuery("page", "1"))
	if err != nil || page < 1 || page > 100000 {
		fail(c, 400, "页码无效")
		return
	}
	q := strings.TrimSpace(c.Query("q"))
	if len(q) > 200 {
		fail(c, 400, "搜索内容过长")
		return
	}
	state := c.Query("state")
	if state != "" && state != "completed" && state != "failed" && state != "cancelled" {
		fail(c, 400, "摘要状态无效")
		return
	}
	query := a.DB.Model(&RequestSummary{}).Where("created_at >= ?", time.Now().AddDate(0, 0, -a.runtimeOptions().digestDays()))
	if state != "" {
		query = query.Where("state = ?", state)
	}
	if q != "" {
		query = query.Where("instr(lower(request_id), lower(?)) > 0 OR instr(lower(model), lower(?)) > 0 OR instr(lower(channel_name), lower(?)) > 0 OR instr(lower(key_name), lower(?)) > 0", q, q, q, q)
	}
	var total int64
	if query.Count(&total).Error != nil {
		fail(c, 500, "读取请求摘要失败")
		return
	}
	rows := []RequestSummary{}
	if query.Order("id DESC").Offset((page-1)*50).Limit(50).Find(&rows).Error != nil {
		fail(c, 500, "读取请求摘要失败")
		return
	}
	items := []RequestLog{}
	for _, row := range rows {
		var value struct {
			Request RequestLog `json:"request"`
		}
		if json.Unmarshal([]byte(row.Data), &value) != nil {
			fail(c, 500, "请求摘要损坏")
			return
		}
		items = append(items, value.Request)
	}
	c.JSON(200, gin.H{"items": items, "total": total, "page": page, "page_size": 50, "active": 0})
}
func (a *App) requestSummaryDetail(c *gin.Context) {
	value, err := strconv.ParseUint(c.Param("id"), 10, 53)
	if err != nil || value == 0 {
		fail(c, 400, "请求摘要编号无效")
		return
	}
	id := uint(value)
	var row RequestSummary
	if a.DB.Where("id = ? AND created_at >= ?", id, time.Now().AddDate(0, 0, -a.runtimeOptions().digestDays())).First(&row).Error != nil {
		fail(c, 404, "请求摘要不存在或已超过保留期限")
		return
	}
	c.Header("Cache-Control", "no-store")
	c.Data(200, "application/json; charset=utf-8", []byte(row.Data))
}
func (options RuntimeOptions) digestDays() int {
	if options.RequestDigestDays == 0 {
		return 7
	}
	return options.RequestDigestDays
}
