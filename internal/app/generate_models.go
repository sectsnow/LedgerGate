package app

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

type generationFailure struct {
	ChannelID uint   `json:"channel_id"`
	Name      string `json:"name"`
	Message   string `json:"message"`
}

type modelGenerationResult struct {
	CreatedModels  int                 `json:"created_models"`
	AddedRoutes    int                 `json:"added_routes"`
	ExistingRoutes int                 `json:"existing_routes"`
	SyncedChannels int                 `json:"synced_channels"`
	Failures       []generationFailure `json:"failures"`
}

type channelModelResult struct {
	Channel Channel
	Models  []string
	Error   error
	Synced  bool
}

func (a *App) generateModelGroups(c *gin.Context) {
	var input struct {
		ChannelIDs     []uint `json:"channel_ids"`
		UseSavedModels bool   `json:"use_saved_models"`
	}
	if c.ShouldBindJSON(&input) != nil || len(input.ChannelIDs) == 0 || len(input.ChannelIDs) > 100 {
		fail(c, 400, "请选择 1–100 条渠道")
		return
	}
	seen := map[uint]bool{}
	for _, id := range input.ChannelIDs {
		if id == 0 || seen[id] {
			fail(c, 400, "渠道 ID 无效或重复")
			return
		}
		seen[id] = true
	}
	channels := []Channel{}
	if err := enabledChannels(a.DB).Where("channels.id IN ?", input.ChannelIDs).Order("id").Find(&channels).Error; err != nil {
		fail(c, 500, "读取渠道失败")
		return
	}
	if len(channels) != len(input.ChannelIDs) {
		fail(c, 400, "部分渠道不存在或已停用，请刷新后重新选择")
		return
	}

	// 模型同步在路由事务外执行，避免上游等待占用 SQLite 写事务。
	ctx, cancel := context.WithTimeout(c.Request.Context(), time.Minute)
	defer cancel()
	results := make([]channelModelResult, len(channels))
	jobs := make(chan int)
	var workers sync.WaitGroup
	for range min(4, len(channels)) {
		workers.Go(func() {
			for i := range jobs {
				channel := channels[i]
				var models []string
				var err error
				synced := false
				if input.UseSavedModels && channel.Models != "" {
					models, err = readChannelModels(channel.Models)
				} else {
					models, err = a.syncChannelModels(ctx, channel)
					synced = err == nil
					channel.Models = jsonString(models)
				}
				for _, model := range models {
					if strings.TrimSpace(model) == "" || len(model) > 150 {
						err = errors.New("模型名称为空或超过 150 字节，无法生成路由")
						break
					}
				}
				results[i] = channelModelResult{Channel: channel, Models: models, Error: err, Synced: synced}
			}
		})
	}
	for i := range channels {
		jobs <- i
	}
	close(jobs)
	workers.Wait()
	if c.Request.Context().Err() != nil {
		return
	}

	result := modelGenerationResult{Failures: []generationFailure{}}
	err := a.DB.WithContext(c.Request.Context()).Transaction(func(tx *gorm.DB) error {
		groups := []*Group{}
		if err := tx.Preload("Routes").Find(&groups).Error; err != nil {
			return errors.New("读取已有模型路由失败")
		}
		byName := map[string]*Group{}
		for _, group := range groups {
			byName[group.Name] = group
		}
		for _, synced := range results {
			channel := synced.Channel
			message := ""
			if synced.Error != nil {
				message = synced.Error.Error()
			} else {
				var current Channel
				err := enabledChannels(tx).First(&current, channel.ID).Error
				if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
					return errors.New("读取渠道最新配置失败")
				}
				if err != nil || !current.Enabled || current.BaseURL != channel.BaseURL || current.APIKey != channel.APIKey || current.Models != channel.Models || !slices.Equal(current.supportedProtocols(), channel.supportedProtocols()) {
					message = "渠道配置已变更，请重新选择并生成"
				} else {
					if synced.Synced {
						result.SyncedChannels++
					}
					if len(synced.Models) == 0 {
						message = "渠道模型列表为空，未生成路由"
					}
				}
			}
			if message != "" {
				result.Failures = append(result.Failures, generationFailure{ChannelID: channel.ID, Name: channel.Name, Message: message})
				continue
			}
			for _, model := range synced.Models {
				group := byName[model]
				if group == nil {
					group = &Group{Name: model, Strategy: "manual"}
					if err := tx.Omit("Routes").Create(group).Error; err != nil {
						return errors.New("创建模型失败，请重试")
					}
					byName[model] = group
					result.CreatedModels++
				}
				exists, priority := false, 0
				for _, route := range group.Routes {
					if route.ChannelID == channel.ID && route.Model == model {
						exists = true
					}
					priority = max(priority, route.Priority+1)
				}
				if exists {
					result.ExistingRoutes++
					continue
				}
				if len(group.Routes) >= 100 || priority > 10000 {
					return fmt.Errorf("模型“%s”的路由数量或优先级达到上限，请调整后重试", model)
				}
				route := Route{GroupID: group.ID, ChannelID: channel.ID, Model: model, Weight: 1, Priority: priority}
				if err := tx.Create(&route).Error; err != nil {
					return errors.New("保存模型路由失败，请重试")
				}
				group.Routes = append(group.Routes, route)
				if group.Strategy == "manual" && group.ActiveRouteID == 0 && len(group.Routes) == 1 {
					group.ActiveRouteID = route.ID
					if err := tx.Model(group).Update("active_route_id", route.ID).Error; err != nil {
						return errors.New("保存手动路由选择失败，请重试")
					}
				}
				result.AddedRoutes++
			}
		}
		return nil
	})
	if err != nil {
		fail(c, 409, err.Error())
		return
	}
	if result.AddedRoutes > 0 {
		a.mu.Lock()
		clear(a.weights)
		a.mu.Unlock()
	}
	c.JSON(200, result)
}
