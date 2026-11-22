package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
)

const modelPriceCatalogURL = "https://models.dev/api.json"

var modelVendors = []struct {
	Name     string
	Provider string
	Prefixes []string
}{
	{"OpenAI", "openai", []string{"gpt-", "chatgpt-", "codex-", "o1", "o3", "o4", "o5"}},
	{"Anthropic", "anthropic", []string{"claude-"}},
	{"Google", "google", []string{"gemini-", "gemma-"}},
	{"DeepSeek", "deepseek", []string{"deepseek-"}},
	{"xAI", "xai", []string{"grok-"}},
	{"Moonshot AI", "moonshotai", []string{"kimi-", "moonshot-"}},
	{"智谱", "zai", []string{"glm-", "zhipu-"}},
	{"MiniMax", "minimax", []string{"minimax-", "abab"}},
	{"阿里云", "alibaba", []string{"qwen", "qwq-", "wan-"}},
	{"Mistral", "mistral", []string{"mistral-", "mixtral-", "ministral-", "codestral-", "devstral-", "pixtral-", "magistral-"}},
	{"Meta", "meta", []string{"llama-", "llama3", "llama4", "muse-"}},
	{"Cohere", "cohere", []string{"command-", "embed-"}},
	{"字节跳动", "volcengine", []string{"doubao-", "seed-", "seedance-", "seedream-"}},
	{"百度", "baidu", []string{"ernie-"}},
	{"腾讯", "tencent", []string{"hunyuan-", "hy3", "hy4"}},
	{"阶跃星辰", "stepfun", []string{"step-", "step3", "step4"}},
}

func inferredVendor(model string) string {
	model = strings.ToLower(model)
	if _, suffix, ok := strings.Cut(model, "/"); ok {
		model = suffix
	}
	for _, vendor := range modelVendors {
		for _, prefix := range vendor.Prefixes {
			if strings.HasPrefix(model, prefix) {
				return vendor.Name
			}
		}
	}
	return "其他"
}

func (group Group) modelVendor() string {
	if group.Vendor != "" {
		return group.Vendor
	}
	if vendor := inferredVendor(group.Name); vendor != "其他" {
		return vendor
	}
	matched := ""
	for _, route := range group.Routes {
		vendor := inferredVendor(route.Model)
		if vendor == "其他" || (matched != "" && matched != vendor) {
			return "其他"
		}
		matched = vendor
	}
	if matched == "" {
		return "其他"
	}
	return matched
}

func (group Group) priceMode() string {
	if group.PriceMode != "" {
		return group.PriceMode
	}
	// 旧版手填过非零价格的模型继续使用手动计价。
	if group.InputPriceMicros != 0 || group.OutputPriceMicros != 0 {
		return "manual"
	}
	return "auto"
}

func (group Group) priceReference() string {
	if group.PriceModel != "" {
		return group.PriceModel
	}
	models := []string{}
	for _, route := range group.Routes {
		key := fmt.Sprintf("%d/%s", route.ChannelID, route.Model)
		if !slices.Contains(models, key) {
			models = append(models, key)
		}
	}
	sort.Strings(models)
	return group.Name + "\n" + strings.Join(models, "\n")
}

func prepareGroupPricing(group *Group, current Group) {
	for index := range group.Routes {
		group.Routes[index].Pricing = nil
	}
	group.PriceSource, group.PriceSourceModel, group.PriceSyncError = "", "", ""
	group.PriceUpdatedAt = nil
	if group.PriceMode == "manual" {
		if group.Pricing == nil && current.priceMode() == "manual" {
			group.Pricing = current.Pricing
		}
		group.Pricing = manualPricing(*group)
		group.PriceSource = "manual"
		now := time.Now()
		group.PriceUpdatedAt = &now
		if current.priceMode() == "manual" && jsonString(manualPricing(current).priceRates) == jsonString(group.Pricing.priceRates) && current.PriceUpdatedAt != nil {
			group.PriceUpdatedAt = current.PriceUpdatedAt
		}
		return
	}
	group.InputPriceMicros, group.OutputPriceMicros = 0, 0
	group.Pricing = nil
	if current.ID != 0 && current.priceMode() == "auto" && current.priceReference() == group.priceReference() {
		group.InputPriceMicros, group.OutputPriceMicros = current.InputPriceMicros, current.OutputPriceMicros
		group.PriceSource, group.PriceSourceModel, group.PriceSyncError = current.PriceSource, current.PriceSourceModel, current.PriceSyncError
		group.PriceUpdatedAt = current.PriceUpdatedAt
		group.Pricing = current.Pricing
		for index := range group.Routes {
			for _, route := range current.Routes {
				if route.ChannelID == group.Routes[index].ChannelID && route.Model == group.Routes[index].Model {
					group.Routes[index].Pricing = route.Pricing
				}
			}
		}
	}
}

type catalogModel struct {
	Type string `json:"type"`
	Cost struct {
		Input      *json.Number `json:"input"`
		Output     *json.Number `json:"output"`
		CacheRead  *json.Number `json:"cache_read"`
		CacheWrite *json.Number `json:"cache_write"`
	} `json:"cost"`
}

type catalogProvider struct {
	Models map[string]catalogModel `json:"models"`
}

type catalogPrice struct {
	Provider string
	Model    string
	Vendor   string
	Input    int64
	Output   int64
	Pricing  *modelPricing
}

type priceCatalog struct {
	Qualified map[string]catalogPrice
	Models    map[string][]catalogPrice
}

func priceMicros(value *json.Number) (int64, bool) {
	if value == nil {
		return 0, false
	}
	if len(value.String()) > 64 || strings.HasPrefix(value.String(), "-") {
		return 0, false
	}
	approximate, err := value.Float64()
	if err != nil || approximate < 0 || approximate > 1_000_000 {
		return 0, false
	}
	if approximate < 0.0000005 {
		return 0, true
	}
	price, ok := new(big.Rat).SetString(value.String())
	if !ok || price.Sign() < 0 {
		return 0, false
	}
	price.Mul(price, big.NewRat(1_000_000, 1))
	whole, remainder := new(big.Int), new(big.Int)
	whole.QuoRem(price.Num(), price.Denom(), remainder)
	if remainder.Mul(remainder, big.NewInt(2)).Cmp(price.Denom()) >= 0 {
		whole.Add(whole, big.NewInt(1))
	}
	if !whole.IsInt64() || whole.Int64() > 1_000_000_000_000 {
		return 0, false
	}
	return whole.Int64(), true
}

func (a *App) fetchPriceCatalog(ctx context.Context) (priceCatalog, error) {
	result := priceCatalog{Qualified: map[string]catalogPrice{}, Models: map[string][]catalogPrice{}}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, modelPriceCatalogURL, nil)
	if err != nil {
		return result, err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "LedgerGate/0.1")
	response, err := a.client.Do(req)
	if err != nil {
		return result, errors.New("Models.dev 价格目录连接失败，请稍后重试")
	}
	defer response.Body.Close()
	if response.StatusCode != 200 {
		return result, errors.New("Models.dev 价格目录返回异常状态")
	}
	raw, err := io.ReadAll(io.LimitReader(response.Body, (16<<20)+1))
	if err != nil || len(raw) > 16<<20 {
		return result, errors.New("价格目录读取失败或超过 16 MiB")
	}
	var providers map[string]catalogProvider
	if err := json.Unmarshal(raw, &providers); err != nil {
		return result, errors.New("价格目录格式无效")
	}
	primary := map[string]string{}
	for _, vendor := range modelVendors {
		primary[vendor.Provider] = vendor.Name
	}
	for providerID, provider := range providers {
		for id, model := range provider.Models {
			if model.Type != "" && model.Type != "chat" && model.Type != "image" && model.Type != "video" {
				continue
			}
			input, validInput := priceMicros(model.Cost.Input)
			output, validOutput := priceMicros(model.Cost.Output)
			if !validInput || !validOutput {
				continue
			}
			price := catalogPrice{Provider: providerID, Model: id, Vendor: primary[providerID], Input: input, Output: output}
			mode := model.Type
			if mode == "" || mode == "chat" {
				mode = "token"
			}
			price.Pricing = &modelPricing{Mode: mode, Source: "models.dev", SourceURL: modelPriceCatalogURL, Model: providerID + "/" + id, Reference: true,
				priceRates: priceRates{Input: intPointer(input), Output: intPointer(output), CacheRead: optionalPrice(model.Cost.CacheRead, false), CacheWrite: optionalPrice(model.Cost.CacheWrite, false)}}
			result.Qualified[strings.ToLower(providerID+"/"+id)] = price
			if price.Vendor != "" {
				key := strings.ToLower(id)
				result.Models[key] = append(result.Models[key], price)
			}
		}
	}
	if len(result.Models) == 0 {
		return result, errors.New("价格目录中没有可用的模型输入和输出价格")
	}
	return result, nil
}

func (catalog priceCatalog) match(model string) (catalogPrice, bool) {
	if price, ok := catalog.Qualified[strings.ToLower(model)]; ok {
		return price, true
	}
	prices := catalog.Models[strings.ToLower(model)]
	vendor := inferredVendor(model)
	matched := []catalogPrice{}
	for _, price := range prices {
		if vendor == "其他" || price.Vendor == vendor {
			matched = append(matched, price)
		}
	}
	if len(matched) != 1 {
		return catalogPrice{}, false
	}
	return matched[0], true
}

func (catalog priceCatalog) priceFor(group Group) (catalogPrice, string) {
	if group.PriceModel != "" {
		if price, ok := catalog.match(group.PriceModel); ok {
			return price, ""
		}
		return catalogPrice{}, "指定的计价模型未匹配到唯一价格"
	}
	if price, ok := catalog.match(group.Name); ok {
		return price, ""
	}
	var matched *catalogPrice
	for _, route := range group.Routes {
		price, ok := catalog.match(route.Model)
		if !ok {
			return catalogPrice{}, "部分上游模型未匹配，请指定计价模型或手动设置价格"
		}
		if matched != nil && (matched.Provider != price.Provider || matched.Model != price.Model) {
			return catalogPrice{}, "上游模型对应多个价格，请指定计价模型"
		}
		matched = &price
	}
	if matched != nil {
		return *matched, ""
	}
	return catalogPrice{}, "未匹配到模型价格"
}

type priceUnmatched struct {
	ID     uint   `json:"id"`
	Name   string `json:"name"`
	Reason string `json:"reason"`
}

type priceSyncResult struct {
	Synced    int              `json:"synced"`
	Manual    int              `json:"manual"`
	Source    string           `json:"source"`
	SyncedAt  time.Time        `json:"synced_at"`
	Unmatched []priceUnmatched `json:"unmatched"`
	Warnings  []string         `json:"warnings"`
}

func (a *App) syncModelPrices(c *gin.Context) {
	a.syncAllModelPrices(c)
}
