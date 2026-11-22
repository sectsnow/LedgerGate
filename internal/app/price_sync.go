package app

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

type catalogResult struct {
	name    string
	catalog priceCatalog
	err     error
}

func referencePrice(catalogs []catalogResult, model string) *modelPricing {
	var partial *modelPricing
	for _, source := range catalogs {
		if source.err != nil {
			continue
		}
		if price, ok := source.catalog.match(model); ok && price.Pricing != nil {
			copy := *price.Pricing
			if copy.Mode == "token" {
				for _, other := range catalogs {
					extra, matched := other.catalog.match(model)
					if other.err != nil || !matched || extra.Pricing == nil || extra.Pricing.Mode != "token" || extra.Provider != price.Provider || extra.Model != price.Model || extra.Input != price.Input || extra.Output != price.Output || len(copy.Tiers) != 0 || len(extra.Pricing.Tiers) != 0 {
						continue
					}
					before := jsonString(copy.priceRates)
					// 同一供应商和基础价才补缺项，保留来源明确返回的零价格。
					copy.priceRates = overlayRates(extra.Pricing.priceRates, copy.priceRates)
					if before != jsonString(copy.priceRates) {
						copy.Note = strings.Trim(copy.Note+"；缓存缺项由 "+other.name+" 补充（同模型且输入输出价一致）", "；")
					}
				}
				return &copy
			}
			if source.name == "LiteLLM" {
				return &copy
			}
			partial = &copy
		}
	}
	return partial
}

func resolveRoutePricing(group Group, route Route, channel Channel, accounts map[uint]Account, upstream map[uint]accountPrices, accountErrors map[uint]error, catalogs []catalogResult, now time.Time) (*modelPricing, string) {
	if group.PriceModel != "" {
		if price := referencePrice(catalogs, group.PriceModel); price != nil {
			price.ChannelIdentity, price.UpdatedAt = pricingIdentity(channel), &now
			return price, ""
		}
		return nil, "指定的计价模型未匹配到价格"
	}
	if channel.AccountID != nil {
		if account, ok := accounts[*channel.AccountID]; ok {
			if accountErrors[account.ID] != nil && route.Pricing != nil && !route.Pricing.Reference && route.Pricing.ChannelIdentity == pricingIdentity(channel) {
				price := *route.Pricing
				price.Stale = true
				return &price, ""
			}
			if price, _ := upstreamRoutePrice(account, channel, route.Model, upstream[account.ID]); price != nil {
				price.UpdatedAt = &now
				return price, ""
			}
		}
	}
	price := referencePrice(catalogs, route.Model)
	if price == nil {
		price = referencePrice(catalogs, group.Name)
	}
	if price == nil {
		return nil, "该上游模型没有可用价格，请指定计价模型或手动设置价格"
	}
	price.ChannelIdentity, price.UpdatedAt = pricingIdentity(channel), &now
	return price, ""
}

func (a *App) syncAllModelPrices(c *gin.Context) {
	result, err := a.syncPricesContext(c.Request.Context())
	if err != nil {
		status := 502
		if strings.Contains(err.Error(), "保存失败") || strings.Contains(err.Error(), "配置失败") {
			status = 500
		}
		if strings.Contains(err.Error(), "正在运行") {
			status = 409
		}
		fail(c, status, err.Error())
		return
	}
	c.JSON(200, result)
}

func (a *App) syncPricesContext(parent context.Context) (result priceSyncResult, resultErr error) {
	if !a.priceSyncMu.TryLock() {
		return result, fmt.Errorf("价格同步正在运行")
	}
	defer a.priceSyncMu.Unlock()
	a.startSyncTask("prices")
	defer func() {
		a.finishSyncTask("prices", resultErr, fmt.Sprintf("同步 %d 个模型，%d 个待匹配", result.Synced, len(result.Unmatched)))
	}()
	ctx, cancel := context.WithTimeout(parent, 50*time.Second)
	defer cancel()
	var groups []Group
	var channelRows []Channel
	var accountRows []Account
	if a.DB.Preload("Routes").Order("id").Find(&groups).Error != nil || a.DB.Find(&channelRows).Error != nil || a.DB.Find(&accountRows).Error != nil {
		return result, fmt.Errorf("读取模型价格配置失败")
	}
	accounts, channels := map[uint]Account{}, map[uint]Channel{}
	for _, account := range accountRows {
		accounts[account.ID] = account
	}
	for _, channel := range channelRows {
		channels[channel.ID] = channel
	}
	catalogs := make([]catalogResult, 3)
	loaders := []struct {
		name  string
		fetch func(context.Context) (priceCatalog, error)
	}{
		{"models.dev", a.fetchPriceCatalog}, {"LiteLLM", a.fetchLiteLLMPrices}, {"OpenRouter", a.fetchOpenRouterPrices},
	}
	var workers sync.WaitGroup
	for i, loader := range loaders {
		workers.Go(func() {
			request, stop := context.WithTimeout(ctx, 20*time.Second)
			defer stop()
			catalog, err := loader.fetch(request)
			catalogs[i] = catalogResult{name: loader.name, catalog: catalog, err: err}
		})
	}
	usedAccounts := map[uint]bool{}
	for _, group := range groups {
		if group.priceMode() == "manual" {
			continue
		}
		for _, route := range group.Routes {
			if channel := channels[route.ChannelID]; channel.AccountID != nil {
				usedAccounts[*channel.AccountID] = true
			}
		}
	}
	upstream, accountErrors := map[uint]accountPrices{}, map[uint]error{}
	for id := range usedAccounts {
		account, exists := accounts[id]
		if !exists {
			continue
		}
		if !a.claimAccount(id) {
			accountErrors[id] = fmt.Errorf("账户正在采集或执行密钥操作，请稍后重试")
			continue
		}
		request, stop := context.WithTimeout(ctx, 15*time.Second)
		upstream[id], accountErrors[id] = a.fetchAccountPrices(request, account)
		stop()
		a.releaseAccount(id)
	}
	workers.Wait()
	result = priceSyncResult{Source: "多来源", SyncedAt: time.Now(), Unmatched: []priceUnmatched{}, Warnings: []string{}}
	sources := []string{}
	for _, source := range catalogs {
		if source.err != nil {
			result.Warnings = append(result.Warnings, source.name+"："+source.err.Error())
		} else {
			sources = append(sources, source.name)
		}
	}
	for id, err := range accountErrors {
		if err != nil {
			result.Warnings = append(result.Warnings, accounts[id].Name+"："+err.Error())
		} else {
			sources = append(sources, accounts[id].Name)
		}
	}
	if len(sources) == 0 {
		return result, fmt.Errorf("价格来源均不可用，已保留现有价格")
	}
	result.Source = strings.Join(sources, " / ")
	err := a.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		for _, previous := range groups {
			var group Group
			if err := tx.Preload("Routes").First(&group, previous.ID).Error; err != nil {
				return err
			}
			if group.priceMode() == "manual" {
				result.Manual++
				continue
			}
			if group.priceReference() != previous.priceReference() {
				result.Unmatched = append(result.Unmatched, priceUnmatched{group.ID, group.Name, "同步期间路由已变更，请重试"})
				continue
			}
			var representative *modelPricing
			reason := ""
			for _, route := range group.Routes {
				channel, exists := channels[route.ChannelID]
				if !exists {
					reason = "路由渠道不存在"
					continue
				}
				var currentChannel Channel
				if tx.First(&currentChannel, channel.ID).Error != nil || pricingIdentity(currentChannel) != pricingIdentity(channel) {
					reason = "同步期间渠道配置已变化，请重试"
					continue
				}
				price, problem := resolveRoutePricing(group, route, channel, accounts, upstream, accountErrors, catalogs, result.SyncedAt)
				if problem != "" {
					reason = strings.Trim(reason+"；"+channel.Name+"（"+route.Model+"）："+problem, "；")
				}
				if err := tx.Model(&Route{}).Where("id = ?", route.ID).Update("pricing", jsonString(price)).Error; err != nil {
					return err
				}
				if price != nil && (representative == nil || route.ID == group.ActiveRouteID) {
					representative = price
				}
			}
			if len(group.Routes) == 0 {
				model := group.PriceModel
				if model == "" {
					model = group.Name
				}
				representative = referencePrice(catalogs, model)
				if representative != nil {
					representative.UpdatedAt = &result.SyncedAt
				} else {
					reason = "没有匹配到公开参考价格"
				}
			}
			updates := map[string]any{"price_sync_error": reason}
			if representative != nil {
				input, output := int64(0), int64(0)
				if representative.Input != nil {
					input = *representative.Input
				}
				if representative.Output != nil {
					output = *representative.Output
				}
				updates["pricing"], updates["input_price_micros"], updates["output_price_micros"] = jsonString(representative), input, output
				updates["price_mode"], updates["price_source"], updates["price_source_model"], updates["price_updated_at"] = "auto", representative.Source, representative.Model, representative.UpdatedAt
				if group.Vendor == "" && inferredVendor(group.Name) != "其他" {
					updates["vendor"] = inferredVendor(group.Name)
				}
				result.Synced++
			}
			if reason != "" {
				result.Unmatched = append(result.Unmatched, priceUnmatched{group.ID, group.Name, reason})
			}
			if err := tx.Model(&group).Updates(updates).Error; err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return result, fmt.Errorf("同步价格保存失败，原价格已保留")
	}
	return result, nil
}
