package app

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func mockPriceSources(t *testing.T, a *App, bodies map[string]string) {
	t.Helper()
	original := a.client.Transport
	a.client.Transport = catalogTransport(func(request *http.Request) (*http.Response, error) {
		for _, url := range []string{modelPriceCatalogURL, liteLLMPriceURL, openRouterPriceURL} {
			if request.URL.String() != url {
				continue
			}
			if request.Header.Get("Authorization") != "" || request.Header.Get("Cookie") != "" {
				t.Error("upstream credential sent to public pricing source")
			}
			status, body := 503, `{}`
			if value, exists := bodies[url]; exists {
				status, body = 200, value
			}
			return &http.Response{StatusCode: status, Body: io.NopCloser(strings.NewReader(body)), Header: http.Header{}}, nil
		}
		return original.RoundTrip(request)
	})
}

func TestSupplementalCatalogUnitsAndCachePrices(t *testing.T) {
	a := testApp(t)
	mockPriceSources(t, a, map[string]string{
		liteLLMPriceURL: `{
		 "claude-test":{"litellm_provider":"anthropic","mode":"chat","input_cost_per_token":0.000003,"output_cost_per_token":0.000015,"cache_read_input_token_cost":0.0000003,"cache_creation_input_token_cost":0.00000375,"cache_creation_input_token_cost_above_1hr":0.000006},
		 "gpt-image-test":{"litellm_provider":"openai","mode":"image_generation","input_cost_per_token":0.000005,"input_cost_per_image_token":0.000008,"output_cost_per_image_token":0.00003},
		 "xai/grok-video-test":{"litellm_provider":"xai","mode":"video_generation","output_cost_per_second":0.05,"output_cost_per_second_720p":0.08},
		 "reseller/claude-test":{"litellm_provider":"reseller","mode":"chat","input_cost_per_token":0.000001,"output_cost_per_token":0.000002}
		}`,
		openRouterPriceURL: `{"data":[{"id":"tencent/hy3","architecture":{"output_modalities":["text"]},"pricing":{"prompt":"0.000000132","completion":"0.000000528","input_cache_read":"0.000000033"}}]}`,
	})
	lite, err := a.fetchLiteLLMPrices(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	entry, ok := lite.match("claude-test")
	if !ok || entry.Pricing == nil || *entry.Pricing.Input != 3000000 || *entry.Pricing.CacheRead != 300000 || *entry.Pricing.CacheWrite != 3750000 || *entry.Pricing.CacheWrite1h != 6000000 {
		t.Fatal("LiteLLM per-token prices converted incorrectly")
	}
	image, ok := lite.match("gpt-image-test")
	if !ok || image.Pricing.Mode != "image" || image.Pricing.Output != nil || *image.Pricing.ImageOutput != 30000000 {
		t.Fatal("image tokens confused with text output prices")
	}
	video, ok := lite.match("grok-video-test")
	if !ok || *video.Pricing.UnitPrice != 50000 || *video.Pricing.Tiers[0].UnitPrice != 80000 {
		t.Fatal("video per-second pricing converted incorrectly")
	}
	if price, ok := lite.match("litellm/reseller/claude-test"); !ok || *price.Pricing.Input != 1000000 {
		t.Fatal("explicit third-party reference unavailable")
	}
	router, err := a.fetchOpenRouterPrices(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	hy, ok := router.match("hy3")
	if !ok || *hy.Pricing.Input != 132000 || *hy.Pricing.Output != 528000 || *hy.Pricing.CacheRead != 33000 || !hy.Pricing.Reference {
		t.Fatal("OpenRouter units or reference attribution incorrect")
	}
}

func TestBillingExpressionsAndUnknownFunctions(t *testing.T) {
	price, ok := parseBillingExpression(`len <= 272000 ? tier("base", p * 0.2 + c * 1.2 + cr * 0.02 + cw * 0.25) : tier("long", p * 0.4 + c * 1.8 + cr * 0.04 + cw * 0.5)`)
	if !ok || len(price.Tiers) != 2 || *price.ratesFor(272000).Input != 200000 || *price.ratesFor(272001).Input != 400000 || *price.ratesFor(272001).CacheRead != 40000 {
		t.Fatal("safe upstream tier expression not parsed")
	}
	if price, ok := parseBillingExpression(`tier("image", fixed(0.04))`); !ok || price.Mode != "request" || *price.UnitPrice != 40000 {
		t.Fatal("fixed-request expression not parsed")
	}
	for _, expression := range []string{`system("secret")`, `tier("base", p * -2 + c * 3)`, `tier("base", p * 1 + p * 2 + c * 3)`, `len > 99 ? tier("x", p * 1 + c * 2) : tier("y", p * 2 + c * 3)`, `tier("base", unknown * 5)`} {
		if _, ok := parseBillingExpression(expression); ok {
			t.Fatalf("unsupported upstream expression accepted: %s", expression)
		}
	}
}

func TestReferencePriceCompletesOnlyCompatibleCacheRates(t *testing.T) {
	primary := &modelPricing{Mode: "token", Source: "models.dev", priceRates: priceRates{Input: intPointer(2_000_000), Output: intPointer(10_000_000), CacheRead: intPointer(0)}}
	extra := cacheTestPrice()
	makeCatalog := func(provider string, pricing *modelPricing) priceCatalog {
		catalog := priceCatalog{Models: map[string][]catalogPrice{}, Qualified: map[string]catalogPrice{}}
		addCatalogPrice(&catalog, provider, "claude-test", pricing, true)
		return catalog
	}
	catalogs := []catalogResult{{name: "models.dev", catalog: makeCatalog("anthropic", primary)}, {name: "LiteLLM", catalog: makeCatalog("anthropic", extra)}}
	merged := referencePrice(catalogs, "claude-test")
	if merged == nil || merged.CacheRead == nil || *merged.CacheRead != 0 || merged.CacheWrite1h == nil || *merged.CacheWrite1h != 4_000_000 || !strings.Contains(merged.Note, "LiteLLM") || primary.CacheWrite1h != nil {
		t.Fatal("compatible missing cache fields not filled, explicit zero replaced, or original catalog mutated")
	}
	for _, change := range []string{"provider", "price", "tiers"} {
		candidate := *extra
		provider := "anthropic"
		switch change {
		case "provider":
			provider = "reseller"
		case "price":
			candidate.Input = intPointer(1_000_000)
		case "tiers":
			candidate.Tiers = []priceTier{{Label: "long"}}
		}
		catalogs[1].catalog = makeCatalog(provider, &candidate)
		if price := referencePrice(catalogs, "claude-test"); price.CacheWrite1h != nil {
			t.Fatalf("incompatible %s price borrowed for cache", change)
		}
	}
}

func TestNewAPIAccountPricesAndRatios(t *testing.T) {
	a := testApp(t)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/api/pricing" || request.Header.Get("Authorization") != "Bearer site-price-key" {
			t.Error("NewAPI pricing path or account authorization incorrect")
			w.WriteHeader(400)
			return
		}
		fmt.Fprint(w, `{"success":true,"data":[
		 {"model_name":"ratio-model","quota_type":0,"model_ratio":2,"completion_ratio":5,"cache_ratio":0.1,"create_cache_ratio":1.25,"enable_groups":["vip"]},
		 {"model_name":"expr-model","billing_mode":"tiered_expr","billing_expr":"len <= 272000 ? tier(\"base\", p * 0.2 + c * 1.2 + cr * 0.02 + cw * 0.25 + cw1h * 0.4) : tier(\"long\", p * 0.4 + c * 2.4 + cr * 0.04 + cw * 0.5 + cw1h * 0.8)","enable_groups":["vip"]},
		 {"model_name":"image-model","quota_type":1,"model_price":0.04,"enable_groups":["vip"]},
		 {"model_name":"unsupported-model","billing_mode":"tiered_expr","billing_expr":"tier(\"base\", unknown * 5)","enable_groups":["vip"]}
		]}`)
	}))
	defer server.Close()
	account := Account{Name: "NewAPI test", Kind: "newapi", BaseURL: server.URL, AuthMode: "token", Token: a.seal("site-price-key"), Currency: "USD"}
	catalog, err := a.fetchAccountPrices(context.Background(), account)
	if err != nil {
		t.Fatal(err)
	}
	channel := Channel{ID: 1, BaseURL: server.URL, APIKey: a.seal("channel-price-key")}
	factor := 0.1
	account.Rates = jsonString(rateSnapshot{Channels: map[uint]channelRate{1: {upstreamRate: upstreamRate{GroupID: "vip", Group: "VIP", Multiplier: &factor}, Identity: channelRateIdentity(channel)}}})
	price, warning := upstreamRoutePrice(account, channel, "ratio-model", catalog)
	if warning != "" || price == nil || *price.Input != 400_000 || *price.Output != 2_000_000 || *price.CacheRead != 40_000 || *price.CacheWrite != 500_000 || price.CacheWrite1h != nil {
		t.Fatalf("NewAPI ratio or cache conversion incorrect: %+v, %s", price, warning)
	}
	price, warning = upstreamRoutePrice(account, channel, "expr-model", catalog)
	if warning != "" || price == nil || *price.ratesFor(272000).Input != 20_000 || *price.ratesFor(272001).CacheWrite1h != 80_000 {
		t.Fatal("NewAPI context price or cache write TTL incorrect")
	}
	price, warning = upstreamRoutePrice(account, channel, "image-model", catalog)
	if warning != "" || price == nil || price.Mode != "request" || *price.UnitPrice != 4_000 {
		t.Fatal("NewAPI fixed per-request price converted as a token price")
	}
	if catalog["vip"]["unsupported-model"] != nil {
		t.Fatal("unsupported billing expression produced a fabricated price")
	}
}

func TestOverviewReportsUnpricedRequests(t *testing.T) {
	a := testApp(t)
	rows := []RequestLog{{RequestID: "estimated", Status: 200, CostStatus: "estimated", CostMicros: 2220, CreatedAt: time.Now()}, {RequestID: "pending", Status: 200, CostStatus: "unknown", CreatedAt: time.Now()}}
	if err := a.DB.Create(&rows).Error; err != nil {
		t.Fatal(err)
	}
	h := a.Router()
	response := call(h, "GET", "/api/overview", "", loginForTest(t, h))
	var summary struct {
		CostMicros int64 `json:"cost_micros"`
		Unknown    int64 `json:"cost_unknown_requests"`
	}
	if response.Code != 200 || json.Unmarshal(response.Body.Bytes(), &summary) != nil || summary.CostMicros != 2220 || summary.Unknown != 1 {
		t.Fatal("unpriced requests silently included as free in overview: " + response.Body.String())
	}
}

func TestUpstreamRoutePricesAndPublicFallback(t *testing.T) {
	a := testApp(t)
	var failUpstream atomic.Bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/api/v1/model-plaza" {
			w.WriteHeader(404)
			return
		}
		if request.Header.Get("Authorization") != "Bearer account-credential" {
			t.Error("upstream account credential missing")
		}
		if failUpstream.Load() {
			w.WriteHeader(503)
			return
		}
		fmt.Fprint(w, `{"data":{"groups":[
		 {"id":7,"models":[{"name":"codex-auto-review","pricing":{"billing_mode":"token","input_price":0.000002,"output_price":0.00001,"cache_read_price":0.0000002,"cache_write_price":0.0000025,"cache_write_1h_price":0.000004}}]},
		 {"id":8,"models":[{"name":"codex-auto-review","pricing":{"billing_mode":"token","input_price":0.000004,"output_price":0.00002,"cache_read_price":0.0000004,"cache_write_price":0.000005,"cache_write_1h_price":0.000008}}]}
		]}}`)
	}))
	defer server.Close()
	mockPriceSources(t, a, map[string]string{openRouterPriceURL: `{"data":[{"id":"tencent/hy3","pricing":{"prompt":"0.000000132","completion":"0.000000528","input_cache_read":"0.000000033"}}]}`})
	account := Account{Name: "site", Kind: "sub2api", BaseURL: server.URL, AuthMode: "token", Token: a.seal("account-credential"), Currency: "USD"}
	if err := a.DB.Create(&account).Error; err != nil {
		t.Fatal(err)
	}
	h := a.Router()
	cookie := loginForTest(t, h)
	channels := []Channel{}
	snapshot := rateSnapshot{Channels: map[uint]channelRate{}, Groups: []upstreamRate{}}
	for index := 0; index < 2; index++ {
		channel := createChannel(t, h, cookie, fmt.Sprintf("channel-%d", index), server.URL, "openai")
		channel.AccountID = &account.ID
		if err := a.DB.Model(&channel).Update("account_id", account.ID).Error; err != nil {
			t.Fatal(err)
		}
		if err := a.DB.First(&channel, channel.ID).Error; err != nil {
			t.Fatal(err)
		}
		factor := 0.1
		snapshot.Channels[channel.ID] = channelRate{upstreamRate: upstreamRate{GroupID: fmt.Sprint(7 + index), Group: "test group", Multiplier: &factor}, Identity: channelRateIdentity(channel)}
		channels = append(channels, channel)
	}
	if err := a.DB.Model(&account).Update("rates", jsonString(snapshot)).Error; err != nil {
		t.Fatal(err)
	}
	groups := []Group{
		{Name: "codex-auto-review", Strategy: "priority", Routes: []Route{{ChannelID: channels[0].ID, Model: "codex-auto-review", Weight: 1}, {ChannelID: channels[1].ID, Model: "codex-auto-review", Weight: 1}}},
		{Name: "hy3", Strategy: "manual", Routes: []Route{{ChannelID: channels[0].ID, Model: "hy3", Weight: 1}}},
	}
	for i := range groups {
		if err := a.DB.Create(&groups[i]).Error; err != nil {
			t.Fatal(err)
		}
	}
	response := call(h, "POST", "/api/groups/sync-prices", "", cookie)
	var syncResult priceSyncResult
	if response.Code != 200 || json.Unmarshal(response.Body.Bytes(), &syncResult) != nil || syncResult.Synced != 2 || len(syncResult.Unmatched) != 0 {
		t.Fatalf("multi-source sync failed: %s", response.Body.String())
	}
	var saved Group
	if err := a.DB.Preload("Routes").First(&saved, groups[0].ID).Error; err != nil {
		t.Fatal(err)
	}
	if saved.Routes[0].Pricing == nil || saved.Routes[1].Pricing == nil || *saved.Routes[0].Pricing.Input != 200000 || *saved.Routes[1].Pricing.Input != 400000 || saved.Routes[0].Pricing.Reference {
		t.Fatal("channel prices merged or group ratio omitted")
	}
	var usage completion
	usage.applyUsage(cachedUsageFixture("openai"), "openai")
	var record RequestLog
	factor := 0.1
	a.recordRequestCost(&record, usage, saved, saved.Routes[0], channels[0], requestRate{Linked: true, Multiplier: &factor})
	if record.CostMicros != 222 || record.CostDetail == nil || !record.CostDetail.RatesIncludeMultiplier {
		t.Fatalf("site price got its ratio twice: %+v", record)
	}
	saved = Group{}
	if err := a.DB.Preload("Routes").First(&saved, groups[1].ID).Error; err != nil {
		t.Fatal(err)
	}
	if saved.Pricing == nil || saved.Pricing.Source != "OpenRouter" || !saved.Pricing.Reference {
		t.Fatal("public fallback not attributed")
	}
	failUpstream.Store(true)
	response = call(h, "POST", "/api/groups/sync-prices", "", cookie)
	if response.Code != 200 {
		t.Fatal(response.Body.String())
	}
	saved = Group{}
	if err := a.DB.Preload("Routes").First(&saved, groups[0].ID).Error; err != nil {
		t.Fatal(err)
	}
	if !saved.Routes[0].Pricing.Stale || *saved.Routes[0].Pricing.Input != 200000 {
		t.Fatal("temporary upstream outage replaced last usable site price")
	}
}
