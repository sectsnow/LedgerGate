package app

import (
	"encoding/json"
	"errors"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

func (a *App) listRequests(c *gin.Context) {
	c.Header("Cache-Control", "no-store")
	page, size := 1, 100
	paged := c.Request.URL.RawQuery != ""
	if paged {
		var err error
		page, err = strconv.Atoi(c.DefaultQuery("page", "1"))
		if err != nil || page < 1 || page > 10000000 {
			fail(c, 400, "页码无效")
			return
		}
		size, err = strconv.Atoi(c.DefaultQuery("page_size", "50"))
		if err != nil || size < 1 || size > 200 {
			fail(c, 400, "每页条数须为 1–200")
			return
		}
	}
	query := a.DB.WithContext(c.Request.Context()).Model(&RequestLog{})
	search := strings.TrimSpace(c.Query("q"))
	if len(search) > 200 {
		fail(c, 400, "搜索内容过长")
		return
	}
	if search != "" {
		like := "%" + search + "%"
		query = query.Where("request_id LIKE ? OR model LIKE ? OR upstream_model LIKE ? OR channel_name LIKE ? OR key_name LIKE ? OR error LIKE ? OR channel_id IN (SELECT id FROM channels WHERE name LIKE ?) OR key_id IN (SELECT id FROM api_keys WHERE name LIKE ?)", like, like, like, like, like, like, like, like)
	}
	switch c.Query("state") {
	case "":
	case "running":
		query = query.Where("state IN ?", []string{"running", "streaming"})
	case "completed":
		query = query.Where("state = 'completed' OR (state = '' AND status < 400)")
	case "failed":
		query = query.Where("state = 'failed' OR (state = '' AND status >= 400 AND status != 499)")
	case "cancelled":
		query = query.Where("state = 'cancelled' OR (state = '' AND status = 499)")
	case "interrupted":
		query = query.Where("state = 'interrupted'")
	default:
		fail(c, 400, "请求状态无效")
		return
	}
	var total int64
	if paged && query.Count(&total).Error != nil {
		fail(c, 500, "读取请求数量失败")
		return
	}
	items := []RequestLog{}
	if query.Order("CASE WHEN state IN ('running', 'streaming') THEN 0 ELSE 1 END").Order("id DESC").Offset((page-1)*size).Limit(size).Find(&items).Error != nil {
		fail(c, 500, "读取请求记录失败")
		return
	}
	a.mu.Lock()
	active := make(map[uint]*requestTrace, len(a.activeRequests))
	for id, trace := range a.activeRequests {
		active[id] = trace
	}
	a.mu.Unlock()
	for i := range items {
		if trace := active[items[i].ID]; trace != nil {
			items[i] = trace.summary()
		}
		items[i].State = requestState(items[i])
	}
	if !paged {
		c.JSON(200, items)
		return
	}
	c.JSON(200, object{"items": items, "total": total, "page": page, "page_size": size, "active": len(active)})
}

func (a *App) requestDetail(c *gin.Context) {
	c.Header("Cache-Control", "no-store")
	id, ok := pathID(c)
	if !ok {
		return
	}
	var log RequestLog
	err := a.DB.WithContext(c.Request.Context()).First(&log, id).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		fail(c, 404, "请求记录不存在或已过保留期")
		return
	}
	if err != nil {
		fail(c, 500, "读取请求记录失败")
		return
	}
	a.mu.Lock()
	trace := a.activeRequests[id]
	a.mu.Unlock()
	payload := requestPayload{Attempts: []requestAttempt{}}
	if trace != nil {
		log, payload = trace.summary(), trace.payload()
	} else {
		var stored RequestDetail
		err := a.DB.WithContext(c.Request.Context()).First(&stored, id).Error
		if err == nil {
			raw, err := a.unseal(stored.Payload)
			if err != nil || json.Unmarshal([]byte(raw), &payload) != nil {
				fail(c, 500, "请求详情解密或读取失败")
				return
			}
		} else if !errors.Is(err, gorm.ErrRecordNotFound) {
			fail(c, 500, "读取请求详情失败")
			return
		}
	}
	log.State = requestState(log)
	if log.ChannelName == "" && log.ChannelID != 0 {
		var channel Channel
		if a.DB.Select("name").First(&channel, log.ChannelID).Error == nil {
			log.ChannelName = channel.Name
		}
	}
	if log.KeyName == "" && log.KeyID != 0 {
		var key APIKey
		if a.DB.Select("name").First(&key, log.KeyID).Error == nil {
			log.KeyName = key.Name
		}
	}
	c.JSON(200, object{"request": log, "detail": payload})
}
