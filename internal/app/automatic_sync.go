package app

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type SyncTask struct {
	Name          string     `json:"name" gorm:"primaryKey"`
	StartedAt     *time.Time `json:"started_at"`
	FinishedAt    *time.Time `json:"finished_at"`
	LastSuccessAt *time.Time `json:"last_success_at"`
	Error         string     `json:"error"`
	Summary       string     `json:"summary"`
}

func (a *App) startSyncTask(name string) {
	now := time.Now()
	a.DB.Clauses(clause.OnConflict{Columns: []clause.Column{{Name: "name"}}, DoUpdates: clause.Assignments(map[string]any{"started_at": now, "finished_at": nil, "error": "", "summary": "正在运行"})}).Create(&SyncTask{Name: name, StartedAt: &now, Summary: "正在运行"})
}

func (a *App) finishSyncTask(name string, err error, summary string) {
	now := time.Now()
	updates := map[string]any{"finished_at": now, "summary": summary, "error": ""}
	if err != nil {
		updates["error"] = err.Error()
	} else {
		updates["last_success_at"] = now
	}
	a.DB.Model(&SyncTask{}).Where("name=?", name).Updates(updates)
}

func (a *App) syncDueTasks(ctx context.Context) {
	options := a.runtimeOptions()
	for _, task := range []struct {
		name  string
		hours int
	}{{"prices", options.PriceSyncHours}, {"models", options.ModelSyncHours}} {
		if task.hours == 0 || ctx.Err() != nil {
			continue
		}
		var state SyncTask
		a.DB.First(&state, "name=?", task.name)
		if state.StartedAt != nil && time.Since(*state.StartedAt) < time.Duration(task.hours)*time.Hour {
			continue
		}
		if task.name == "prices" {
			_, _ = a.syncPricesContext(ctx)
		} else {
			_ = a.discoverModels(ctx)
		}
	}
}

func (a *App) discoverModels(parent context.Context) (resultErr error) {
	if !a.modelSyncMu.TryLock() {
		return fmt.Errorf("模型发现正在运行")
	}
	defer a.modelSyncMu.Unlock()
	a.startSyncTask("models")
	var failed atomic.Int32
	defer func() { a.finishSyncTask("models", resultErr, fmt.Sprintf("%d 条渠道发现失败", failed.Load())) }()
	ctx, cancel := context.WithTimeout(parent, 2*time.Minute)
	defer cancel()
	var channels []Channel
	if err := a.DB.WithContext(ctx).Where("enabled=?", true).Find(&channels).Error; err != nil {
		return fmt.Errorf("读取渠道失败")
	}
	jobs := make(chan Channel, len(channels))
	for _, channel := range channels {
		jobs <- channel
	}
	close(jobs)
	var workers sync.WaitGroup
	for i := 0; i < min(4, len(channels)); i++ {
		workers.Go(func() {
			for channel := range jobs {
				if ctx.Err() != nil {
					return
				}
				models, err := a.availableChannelModels(ctx, channel)
				if err == nil {
					models, err = normalizeChannelModels(models)
				}
				now := time.Now()
				updates := map[string]any{"models_checked_at": now, "models_sync_error": ""}
				if err != nil {
					updates["models_sync_error"] = err.Error()
					failed.Add(1)
				} else {
					updates["discovered_models"] = jsonString(models)
				}
				if err := a.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
					var current Channel
					if err := tx.First(&current, channel.ID).Error; err != nil {
						return err
					}
					if channelModelRevision(current) != channelModelRevision(channel) {
						return nil
					}
					return tx.Model(&current).Updates(updates).Error
				}); err != nil {
					failed.Add(1)
				}
			}
		})
	}
	workers.Wait()
	if ctx.Err() != nil {
		return fmt.Errorf("模型发现取消或超时")
	}
	if failed.Load() > 0 {
		return fmt.Errorf("部分渠道发现失败，已保留原模型选择")
	}
	return nil
}

func (a *App) syncTaskList() []SyncTask {
	items := []SyncTask{}
	a.DB.Order("name").Find(&items)
	return items
}
