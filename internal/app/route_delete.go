package app

import (
	"errors"
	"strconv"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

func (a *App) deleteGroupRoute(c *gin.Context) {
	id, ok := pathID(c)
	if !ok {
		return
	}
	routeID, err := strconv.ParseUint(c.Param("route_id"), 10, 32)
	if err != nil || routeID == 0 {
		fail(c, 400, "路由 ID 无效")
		return
	}
	err = a.DB.WithContext(c.Request.Context()).Transaction(func(tx *gorm.DB) error {
		var route Route
		if err := tx.Where("id = ? AND group_id = ?", routeID, id).First(&route).Error; err != nil {
			return err
		}
		if err := tx.Model(&Group{}).Where("id = ? AND active_route_id = ?", id, routeID).Update("active_route_id", 0).Error; err != nil {
			return err
		}
		return tx.Delete(&route).Error
	})
	if errors.Is(err, gorm.ErrRecordNotFound) {
		fail(c, 404, "模型或上游路由已不存在，请刷新后重试")
		return
	}
	if err != nil {
		fail(c, 500, "移除路由失败，请重试")
		return
	}
	a.mu.Lock()
	delete(a.groupRouting, id)
	delete(a.weights, uint(routeID))
	a.mu.Unlock()
	c.Status(204)
}
