package app

import (
	"context"
	"errors"
	"net/http"
	"slices"
	"sync"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

type channelBatchInput struct {
	IDs          []uint   `json:"ids"`
	Action       string   `json:"action"`
	Protocols    []string `json:"protocols"`
	DetachRoutes bool     `json:"detach_routes"`
}

type channelBatchItem struct {
	ID      uint   `json:"id"`
	Name    string `json:"name"`
	Success bool   `json:"success"`
	Error   string `json:"error,omitempty"`
}

func (a *App) batchChannels(c *gin.Context) {
	var input channelBatchInput
	if c.ShouldBindJSON(&input) != nil || len(input.IDs) == 0 || len(input.IDs) > 100 {
		fail(c, 400, "请选择 1–100 条渠道")
		return
	}
	seen := map[uint]bool{}
	for _, id := range input.IDs {
		if id == 0 || seen[id] {
			fail(c, 400, "渠道 ID 无效或重复")
			return
		}
		seen[id] = true
	}
	switch input.Action {
	case "enable", "disable", "sync_models", "delete":
	case "protocols":
		if len(input.Protocols) == 0 || len(input.Protocols) > 3 {
			fail(c, 400, "请至少选择一种上游协议")
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
	default:
		fail(c, 400, "批量操作无效")
		return
	}
	results := make([]channelBatchItem, len(input.IDs))
	jobs := make(chan int)
	var workers sync.WaitGroup
	for range min(4, len(input.IDs)) {
		workers.Go(func() {
			for index := range jobs {
				results[index] = a.applyChannelBatch(c.Request.Context(), input.IDs[index], input)
			}
		})
	}
	for index := range input.IDs {
		jobs <- index
	}
	close(jobs)
	workers.Wait()
	succeeded := 0
	for _, result := range results {
		if result.Success {
			succeeded++
		}
	}
	c.JSON(http.StatusOK, gin.H{"results": results, "succeeded": succeeded, "failed": len(results) - succeeded})
}

func (a *App) applyChannelBatch(ctx context.Context, id uint, input channelBatchInput) channelBatchItem {
	result := channelBatchItem{ID: id}
	var channel Channel
	if err := a.DB.WithContext(ctx).First(&channel, id).Error; err != nil {
		result.Error = "读取渠道失败"
		if errors.Is(err, gorm.ErrRecordNotFound) {
			result.Error = "渠道不存在"
		}
		if ctx.Err() != nil {
			result.Error = "操作已取消"
		}
		return result
	}
	result.Name = channel.Name
	var err error
	switch input.Action {
	case "sync_models":
		_, err = a.syncChannelModels(ctx, channel)
	case "delete":
		err = a.deleteChannelRecord(ctx, id, input.DetachRoutes)
		if err != nil && !errors.Is(err, errChannelInUse) {
			err = errors.New("删除渠道失败")
		}
	default:
		updates := map[string]any{"enabled": input.Action == "enable", "auto_disabled_reason": ""}
		if input.Action == "protocols" {
			updates = map[string]any{"protocol": input.Protocols[0], "protocols": jsonString(input.Protocols)}
			if !slices.Equal(channel.supportedProtocols(), input.Protocols) {
				updates["discovered_models"], updates["models_baseline"], updates["models_sync_error"], updates["models_checked_at"] = "", "", "", nil
			}
		}
		saved := a.DB.WithContext(ctx).Model(&Channel{}).Where("id = ?", id).Updates(updates)
		if saved.Error != nil {
			err = errors.New("保存渠道失败")
		} else if saved.RowsAffected == 0 {
			err = errors.New("渠道已删除")
		}
		if err == nil {
			a.mu.Lock()
			delete(a.health, id)
			clear(a.weights)
			a.mu.Unlock()
		}
	}
	if err != nil {
		result.Error = err.Error()
		if ctx.Err() != nil {
			result.Error = "操作已取消"
		}
		return result
	}
	result.Success = true
	return result
}

func (a *App) deleteChannelRecord(ctx context.Context, id uint, detachRoutes bool) error {
	err := a.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var count int64
		if err := tx.Model(&Route{}).Where("channel_id = ?", id).Count(&count).Error; err != nil {
			return err
		}
		if count > 0 && !detachRoutes {
			return errChannelInUse
		}
		if detachRoutes {
			routes := tx.Model(&Route{}).Select("id").Where("channel_id = ?", id)
			if err := tx.Model(&Group{}).Where("active_route_id IN (?)", routes).Update("active_route_id", 0).Error; err != nil {
				return err
			}
			if err := tx.Where("channel_id = ?", id).Delete(&Route{}).Error; err != nil {
				return err
			}
		}
		return tx.Delete(&Channel{}, id).Error
	})
	if err != nil {
		return err
	}
	a.mu.Lock()
	delete(a.health, id)
	clear(a.weights)
	a.mu.Unlock()
	return nil
}
