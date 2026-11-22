package app

import (
	"errors"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

func (a *App) selectGroupRoute(c *gin.Context) {
	id, ok := pathID(c)
	if !ok {
		return
	}
	var input struct {
		RouteID uint `json:"route_id"`
	}
	if c.ShouldBindJSON(&input) != nil || input.RouteID == 0 {
		fail(c, 400, "请选择上游路由")
		return
	}
	var group Group
	err := a.DB.Transaction(func(tx *gorm.DB) error {
		if err := tx.First(&group, id).Error; err != nil {
			return err
		}
		if group.Strategy != "manual" {
			return errors.New("请先将模型调度策略设为手动选择")
		}
		var route Route
		if err := tx.Where("id = ? AND group_id = ?", input.RouteID, id).First(&route).Error; err != nil {
			return err
		}
		group.ActiveRouteID = route.ID
		return tx.Model(&group).Update("active_route_id", route.ID).Error
	})
	if errors.Is(err, gorm.ErrRecordNotFound) {
		fail(c, 404, "模型或上游路由已不存在，请刷新后重试")
		return
	}
	if err != nil {
		if group.ID != 0 && group.Strategy != "manual" {
			fail(c, 409, err.Error())
		} else {
			fail(c, 500, "保存手动选择失败")
		}
		return
	}
	c.JSON(200, gin.H{"active_route_id": group.ActiveRouteID})
}
