package app

import (
	"errors"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

var errRouteOrderChanged = errors.New("模型路由已更新，请刷新后重新排序")

type routeOrderEntry struct {
	ID       uint `json:"id"`
	Priority int  `json:"priority"`
	Weight   int  `json:"weight"`
}

func (a *App) reorderGroupRoutes(c *gin.Context) {
	id, ok := pathID(c)
	if !ok {
		return
	}
	var input struct {
		Strategy string            `json:"strategy"`
		RouteIDs []uint            `json:"route_ids"`
		Expected []routeOrderEntry `json:"expected_routes"`
	}
	if c.ShouldBindJSON(&input) != nil || len(input.RouteIDs) < 1 || len(input.RouteIDs) > 100 || len(input.Expected) != len(input.RouteIDs) || (input.Strategy != "priority" && input.Strategy != "weighted" && input.Strategy != "manual") {
		fail(c, 400, "请提供完整路由顺序和当前调度策略")
		return
	}
	expected := map[uint]routeOrderEntry{}
	for _, entry := range input.Expected {
		if _, duplicate := expected[entry.ID]; entry.ID == 0 || duplicate {
			fail(c, 400, "路由 ID 无效或重复")
			return
		}
		expected[entry.ID] = entry
	}
	seen := map[uint]bool{}
	for _, routeID := range input.RouteIDs {
		if _, exists := expected[routeID]; !exists || seen[routeID] {
			fail(c, 400, "排序必须包含全部路由且不能重复")
			return
		}
		seen[routeID] = true
	}
	err := a.DB.WithContext(c.Request.Context()).Transaction(func(tx *gorm.DB) error {
		var group Group
		if err := tx.Preload("Routes").First(&group, id).Error; err != nil {
			return err
		}
		if group.Strategy != input.Strategy || len(group.Routes) != len(input.RouteIDs) {
			return errRouteOrderChanged
		}
		for _, route := range group.Routes {
			entry, exists := expected[route.ID]
			if !exists || entry.Priority != route.Priority || entry.Weight != route.Weight {
				return errRouteOrderChanged
			}
		}
		for index, routeID := range input.RouteIDs {
			updates := map[string]any{"priority": index}
			if group.Strategy == "weighted" {
				updates["weight"] = len(input.RouteIDs) - index
			}
			if err := tx.Model(&Route{}).Where("id = ? AND group_id = ?", routeID, id).Updates(updates).Error; err != nil {
				return err
			}
		}
		return nil
	})
	if errors.Is(err, gorm.ErrRecordNotFound) {
		fail(c, 404, "模型路由不存在")
		return
	}
	if errors.Is(err, errRouteOrderChanged) {
		fail(c, 409, err.Error())
		return
	}
	if err != nil {
		fail(c, 500, "保存路由顺序失败，原顺序已保留")
		return
	}
	a.mu.Lock()
	delete(a.groupRouting, id)
	for routeID := range seen {
		delete(a.weights, routeID)
	}
	a.mu.Unlock()
	c.Status(204)
}
