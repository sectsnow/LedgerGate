package app

import (
	"errors"
	"slices"
	"time"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

var errChannelModelsChanged = errors.New("渠道配置或模型列表已更新，请重新获取可同步模型")

func channelModelRevision(channel Channel) string {
	return digest(jsonString(object{
		"id": channel.ID, "base_url": channel.BaseURL, "api_key": channel.APIKey,
		"protocols": channel.supportedProtocols(), "models": channel.Models, "use_proxy": channel.UseProxy,
	}))
}

func channelModelDiscoveryRevision(channel Channel) string {
	return digest(channelModelRevision(channel) + channel.DiscoveredModels)
}

func channelModelDiscoveryUpdates(channel Channel, models []string) map[string]any {
	models = slices.Clone(models)
	slices.Sort(models)
	snapshot := jsonString(models)
	baseline := channel.ModelsBaseline
	if baseline == "" {
		baseline = channel.DiscoveredModels
		if baseline == "" {
			baseline = snapshot
		}
	}
	return map[string]any{"discovered_models": snapshot, "models_baseline": baseline, "models_checked_at": time.Now(), "models_sync_error": ""}
}

func (a *App) previewChannelModels(c *gin.Context) {
	c.Header("Cache-Control", "no-store")
	id, ok := pathID(c)
	if !ok {
		return
	}
	var channel Channel
	if a.DB.WithContext(c.Request.Context()).First(&channel, id).Error != nil {
		fail(c, 404, "渠道不存在")
		return
	}
	saved := []string{}
	var err error
	if channel.Models != "" {
		saved, err = readChannelModels(channel.Models)
		if err != nil {
			fail(c, 409, err.Error())
			return
		}
	}
	models, err := a.availableChannelModels(c.Request.Context(), channel)
	if err != nil {
		fail(c, 502, err.Error())
		return
	}
	models, err = normalizeChannelModels(models)
	if err != nil {
		fail(c, 502, err.Error())
		return
	}
	err = a.DB.WithContext(c.Request.Context()).Transaction(func(tx *gorm.DB) error {
		var current Channel
		if err := tx.First(&current, id).Error; err != nil {
			return err
		}
		if channelModelDiscoveryRevision(current) != channelModelDiscoveryRevision(channel) {
			return errChannelModelsChanged
		}
		if err := tx.Model(&current).Updates(channelModelDiscoveryUpdates(current, models)).Error; err != nil {
			return err
		}
		return tx.First(&channel, id).Error
	})
	if errors.Is(err, errChannelModelsChanged) {
		fail(c, 409, err.Error())
		return
	}
	if errors.Is(err, gorm.ErrRecordNotFound) {
		fail(c, 404, "渠道已删除，请刷新后重试")
		return
	}
	if err != nil {
		fail(c, 500, "保存模型发现结果失败")
		return
	}
	c.JSON(200, object{"models": models, "saved_models": saved, "revision": channelModelDiscoveryRevision(channel)})
}

func (a *App) saveChannelModelSelection(c *gin.Context) {
	id, ok := pathID(c)
	if !ok {
		return
	}
	var input struct {
		Models   *[]string `json:"models"`
		Revision string    `json:"revision"`
	}
	if c.ShouldBindJSON(&input) != nil || input.Models == nil || len(input.Revision) != 64 {
		fail(c, 400, "请选择模型并提供预览版本")
		return
	}
	models, err := normalizeChannelModels(*input.Models)
	if err != nil {
		fail(c, 400, err.Error())
		return
	}
	err = a.DB.WithContext(c.Request.Context()).Transaction(func(tx *gorm.DB) error {
		var channel Channel
		if err := tx.First(&channel, id).Error; err != nil {
			return err
		}
		if channelModelDiscoveryRevision(channel) != input.Revision {
			return errChannelModelsChanged
		}
		return tx.Model(&channel).Updates(map[string]any{"models": jsonString(models), "models_baseline": channel.DiscoveredModels}).Error
	})
	if errors.Is(err, gorm.ErrRecordNotFound) {
		fail(c, 404, "渠道已删除，请刷新后重试")
		return
	}
	if errors.Is(err, errChannelModelsChanged) {
		fail(c, 409, err.Error())
		return
	}
	if err != nil {
		fail(c, 500, "保存所选模型失败")
		return
	}
	c.JSON(200, models)
}

func (a *App) acknowledgeChannelModels(c *gin.Context) {
	id, ok := pathID(c)
	if !ok {
		return
	}
	var input struct {
		Revision string `json:"revision"`
	}
	if c.ShouldBindJSON(&input) != nil || len(input.Revision) != 64 {
		fail(c, 400, "请提供模型列表版本")
		return
	}
	err := a.DB.WithContext(c.Request.Context()).Transaction(func(tx *gorm.DB) error {
		var channel Channel
		if err := tx.First(&channel, id).Error; err != nil {
			return err
		}
		if channel.DiscoveredModels == "" || channel.ModelsSyncError != "" || channelModelDiscoveryRevision(channel) != input.Revision {
			return errChannelModelsChanged
		}
		return tx.Model(&channel).Update("models_baseline", channel.DiscoveredModels).Error
	})
	if errors.Is(err, gorm.ErrRecordNotFound) {
		fail(c, 404, "渠道已删除，请刷新后重试")
		return
	}
	if errors.Is(err, errChannelModelsChanged) {
		fail(c, 409, "模型发现结果已更新或获取失败，请刷新后确认")
		return
	}
	if err != nil {
		fail(c, 500, "确认模型变化失败")
		return
	}
	c.JSON(200, object{"ok": true})
}
