package app

import (
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"
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
	search := strings.ToLower(strings.TrimSpace(c.Query("q")))
	if len(search) > 200 {
		fail(c, 400, "搜索内容过长")
		return
	}
	state := c.Query("state")
	switch state {
	case "", "running", "completed", "failed", "cancelled":
	default:
		fail(c, 400, "请求状态无效")
		return
	}
	all, active := a.requestSummaries()
	items := []RequestLog{}
	for _, log := range all {
		log.State = requestState(log)
		if state != "" && !(state == "running" && activeRequest(log.State)) && state != log.State {
			continue
		}
		if search != "" && !strings.Contains(strings.ToLower(strings.Join([]string{log.RequestID, log.Model, log.UpstreamModel, log.ChannelName, log.KeyName, log.Error}, "\n")), search) {
			continue
		}
		items = append(items, log)
	}
	total := len(items)
	start := min((page-1)*size, total)
	items = items[start:min(start+size, total)]
	if !paged {
		c.JSON(200, items)
		return
	}
	c.JSON(200, object{"items": items, "total": total, "page": page, "page_size": size, "active": active})
}

func (a *App) requestDetail(c *gin.Context) {
	c.Header("Cache-Control", "no-store")
	value, err := strconv.ParseUint(c.Param("id"), 10, 53)
	if err != nil || value == 0 {
		fail(c, 400, "请求编号无效")
		return
	}
	log, payload, found := a.cachedRequestDetail(uint(value))
	if !found {
		fail(c, 404, "请求已移出内存缓存或服务已重启，历史用量统计仍保留")
		return
	}
	log.State = requestState(log)
	c.JSON(200, object{"request": log, "detail": payload})
}
