package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"strings"
)

const liteLLMPriceURL = "https://raw.githubusercontent.com/BerriAI/litellm/main/model_prices_and_context_window.json"
const openRouterPriceURL = "https://openrouter.ai/api/v1/models"

func optionalPrice(value *json.Number, perToken bool) *int64 {
	if value == nil {
		return nil
	}
	if !perToken {
		price, ok := priceMicros(value)
		if !ok {
			return nil
		}
		return &price
	}
	if len(value.String()) > 64 {
		return nil
	}
	rate, ok := new(big.Rat).SetString(value.String())
	if !ok || rate.Sign() < 0 || rate.Cmp(big.NewRat(1, 1)) > 0 {
		return nil
	}
	rate.Mul(rate, big.NewRat(1_000_000_000_000, 1))
	whole, remainder := new(big.Int), new(big.Int)
	whole.QuoRem(rate.Num(), rate.Denom(), remainder)
	if remainder.Mul(remainder, big.NewInt(2)).Cmp(rate.Denom()) >= 0 {
		whole.Add(whole, big.NewInt(1))
	}
	if !whole.IsInt64() || whole.Int64() > 1_000_000_000_000 {
		return nil
	}
	price := whole.Int64()
	return &price
}

func sourcePrice(value any, perToken bool) *int64 {
	var number json.Number
	switch value := value.(type) {
	case json.Number:
		number = value
	case string:
		number = json.Number(value)
	case float64:
		number = json.Number(fmt.Sprint(value))
	default:
		return nil
	}
	return optionalPrice(&number, perToken)
}

func tokenSourceRates(data object, keys [7]string) priceRates {
	return priceRates{Input: sourcePrice(data[keys[0]], true), Output: sourcePrice(data[keys[1]], true), CacheRead: sourcePrice(data[keys[2]], true), CacheWrite: sourcePrice(data[keys[3]], true), CacheWrite1h: sourcePrice(data[keys[4]], true), ImageInput: sourcePrice(data[keys[5]], true), ImageOutput: sourcePrice(data[keys[6]], true)}
}

func (a *App) publicPriceJSON(ctx context.Context, url string) (any, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, errors.New("价格接口地址无效")
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "LedgerGate/0.1")
	response, err := a.client.Do(req)
	if err != nil {
		return nil, errors.New("价格接口连接失败或超时")
	}
	defer response.Body.Close()
	if response.StatusCode != 200 {
		return nil, fmt.Errorf("价格接口返回 HTTP %d", response.StatusCode)
	}
	raw, err := io.ReadAll(io.LimitReader(response.Body, (24<<20)+1))
	if err != nil || len(raw) > 24<<20 {
		return nil, errors.New("价格接口读取失败或超过 24 MiB")
	}
	return decodeJSON(raw)
}

func providerVendor(provider string) string {
	for _, vendor := range modelVendors {
		if vendor.Provider == provider {
			return vendor.Name
		}
	}
	return ""
}

func addCatalogPrice(catalog *priceCatalog, provider, model string, price *modelPricing, bare bool) {
	entry := catalogPrice{Provider: provider, Model: model, Vendor: inferredVendor(model), Pricing: price}
	if price.Input != nil {
		entry.Input = *price.Input
	}
	if price.Output != nil {
		entry.Output = *price.Output
	}
	catalog.Qualified[strings.ToLower(provider+"/"+model)] = entry
	if bare {
		catalog.Models[strings.ToLower(model)] = append(catalog.Models[strings.ToLower(model)], entry)
	}
}

func (a *App) fetchLiteLLMPrices(ctx context.Context) (priceCatalog, error) {
	catalog := priceCatalog{Qualified: map[string]catalogPrice{}, Models: map[string][]catalogPrice{}}
	value, err := a.publicPriceJSON(ctx, liteLLMPriceURL)
	if err != nil {
		return catalog, err
	}
	data, ok := value.(map[string]any)
	if !ok {
		return catalog, errors.New("LiteLLM 价格表格式无效")
	}
	keys := [7]string{"input_cost_per_token", "output_cost_per_token", "cache_read_input_token_cost", "cache_creation_input_token_cost", "cache_creation_input_token_cost_above_1hr", "input_cost_per_image_token", "output_cost_per_image_token"}
	for key, raw := range data {
		model := obj(raw)
		provider := str(model["litellm_provider"])
		mode := str(model["mode"])
		price := &modelPricing{Source: "LiteLLM", SourceURL: liteLLMPriceURL, Model: key, Reference: true, priceRates: tokenSourceRates(model, keys)}
		switch mode {
		case "chat", "completion":
			price.Mode = "token"
		case "image_generation":
			price.Mode = "image"
		case "video_generation":
			price.Mode = "video"
		default:
			continue
		}
		if price.Mode == "token" && (price.Input == nil || price.Output == nil) {
			continue
		}
		if price.Mode == "image" {
			price.UnitPrice = sourcePrice(model["output_cost_per_image"], false)
			if price.UnitPrice == nil {
				price.UnitPrice = sourcePrice(model["input_cost_per_image"], false)
			}
			for _, field := range []string{"output_cost_per_image_1K", "output_cost_per_image_2K"} {
				if amount := sourcePrice(model[field], false); amount != nil {
					price.Tiers = append(price.Tiers, priceTier{Label: strings.TrimPrefix(field, "output_cost_per_image_"), UnitPrice: amount})
				}
			}
		}
		if price.Mode == "video" {
			price.UnitPrice = sourcePrice(model["output_cost_per_second"], false)
			for _, field := range []string{"output_cost_per_second_480p", "output_cost_per_second_720p", "output_cost_per_second_1080p"} {
				if amount := sourcePrice(model[field], false); amount != nil {
					price.Tiers = append(price.Tiers, priceTier{Label: strings.TrimPrefix(field, "output_cost_per_second_"), UnitPrice: amount})
				}
			}
		}
		// 原始供应商键可自动匹配，分辨率路径和第三方售价仅接受显式引用。
		modelID := strings.TrimPrefix(key, provider+"/")
		bare := !strings.Contains(modelID, "/") && providerVendor(provider) == inferredVendor(modelID) && providerVendor(provider) != ""
		addCatalogPrice(&catalog, "litellm", key, price, false)
		if bare {
			addCatalogPrice(&catalog, provider, modelID, price, true)
		}
	}
	if len(catalog.Qualified) == 0 {
		return catalog, errors.New("LiteLLM 未返回可用价格")
	}
	return catalog, nil
}

func (a *App) fetchOpenRouterPrices(ctx context.Context) (priceCatalog, error) {
	catalog := priceCatalog{Qualified: map[string]catalogPrice{}, Models: map[string][]catalogPrice{}}
	value, err := a.publicPriceJSON(ctx, openRouterPriceURL)
	if err != nil {
		return catalog, err
	}
	items, ok := obj(value)["data"].([]any)
	if !ok {
		return catalog, errors.New("OpenRouter 模型价格格式无效")
	}
	for _, item := range items {
		model := obj(item)
		outputs := array(obj(model["architecture"])["output_modalities"])
		nonText := false
		for _, output := range outputs {
			if str(output) != "text" {
				nonText = true
			}
		}
		if nonText {
			continue
		}
		id := str(model["id"])
		provider, modelID, ok := strings.Cut(id, "/")
		if !ok {
			continue
		}
		data := obj(model["pricing"])
		price := &modelPricing{Mode: "token", Source: "OpenRouter", SourceURL: openRouterPriceURL, Model: id, Reference: true,
			priceRates: tokenSourceRates(data, [7]string{"prompt", "completion", "input_cache_read", "input_cache_write", "", "", ""})}
		if price.Input == nil || price.Output == nil {
			continue
		}
		if request := sourcePrice(data["request"], false); request != nil && *request > 0 {
			price.Note = "该平台另有按请求收费，Token 价格仅供参考"
		}
		if len(array(data["overrides"])) > 0 {
			price.Note = "参考平台存在分时价格，当前展示默认单价"
		}
		addCatalogPrice(&catalog, "openrouter", id, price, false)
		addCatalogPrice(&catalog, provider, modelID, price, true)
	}
	if len(catalog.Qualified) == 0 {
		return catalog, errors.New("OpenRouter 未返回可用价格")
	}
	return catalog, nil
}
