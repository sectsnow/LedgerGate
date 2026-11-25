package app

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestRateCollectionRefreshesOnlyAffectedAutomaticPrices(t *testing.T) {
	a := testApp(t)
	var factor, groupID, priceCalls atomic.Int32
	var failPrices atomic.Bool
	factor.Store(20)
	groupID.Store(7)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/auth/me":
			fmt.Fprint(w, `{"data":{"balance":10}}`)
		case "/api/v1/usage/dashboard/stats":
			fmt.Fprint(w, `{"data":{"total_actual_cost":1,"today_actual_cost":0.1}}`)
		case "/api/v1/groups/available":
			fmt.Fprintf(w, `{"data":[{"id":7,"name":"first","rate_multiplier":%g},{"id":8,"name":"second","rate_multiplier":%g}]}`, float64(factor.Load())/100, float64(factor.Load())/100)
		case "/api/v1/groups/rates":
			fmt.Fprint(w, `{"data":{}}`)
		case "/api/v1/keys":
			fmt.Fprintf(w, `{"data":{"total":1,"items":[{"id":101,"key":"sk-sync-test","group_id":%d}]}}`, groupID.Load())
		case "/api/v1/model-plaza":
			priceCalls.Add(1)
			if failPrices.Load() {
				w.WriteHeader(503)
				return
			}
			fmt.Fprint(w, `{"data":{"groups":[
			 {"id":7,"long_context_pricing_enabled":true,"models":[{"name":"test-model","pricing":{"billing_mode":"token","input_price":0.000002,"output_price":0.00001,"cache_read_price":0.0000002,"cache_write_price":0.0000025,"cache_write_1h_price":0.000004,"intervals":[{"min_tokens":272000,"input_price":0.000004,"output_price":0.00002}]}}]},
			 {"id":8,"models":[{"name":"test-model","pricing":{"billing_mode":"token","input_price":0.000004,"output_price":0.00002,"cache_read_price":0.0000004}}]}
			]}}`)
		default:
			fmt.Fprint(w, `{"data":[]}`)
		}
	}))
	defer server.Close()
	account := Account{Name: "changed", Kind: "sub2api", BaseURL: server.URL, AuthMode: "token", Token: a.seal("account-token"), Currency: "USD", IntervalSeconds: 300}
	otherAccount := Account{Name: "unrelated", Kind: "sub2api", BaseURL: server.URL + "/other"}
	for _, row := range []*Account{&account, &otherAccount} {
		if err := a.DB.Create(row).Error; err != nil {
			t.Fatal(err)
		}
	}
	keyID := int64(101)
	channel := Channel{Name: "changed", AccountID: &account.ID, UpstreamKeyID: &keyID, BaseURL: server.URL, APIKey: a.seal("sk-sync-test")}
	otherChannel := Channel{Name: "unrelated", AccountID: &otherAccount.ID, BaseURL: otherAccount.BaseURL}
	for _, row := range []*Channel{&channel, &otherChannel} {
		if err := a.DB.Create(row).Error; err != nil {
			t.Fatal(err)
		}
	}
	snapshot := rateSnapshot{Groups: []upstreamRate{{GroupID: "7", Multiplier: floatPointer(0.1)}}, Channels: map[uint]channelRate{
		channel.ID: {upstreamRate: upstreamRate{GroupID: "7", Multiplier: floatPointer(0.1)}, Identity: channelRateIdentity(channel)},
	}}
	account.Rates = jsonString(snapshot)
	if err := a.DB.Model(&account).Update("rates", account.Rates).Error; err != nil {
		t.Fatal(err)
	}
	oldPrice := &modelPricing{Mode: "token", Source: account.Name, Model: "test-model", MultiplierIncluded: true, AppliedMultiplier: floatPointer(0.1), ChannelIdentity: pricingIdentity(channel), priceRates: priceRates{Input: intPointer(200000), Output: intPointer(1000000)}}
	otherPrice := *oldPrice
	otherPrice.ChannelIdentity, otherPrice.Input = pricingIdentity(otherChannel), intPointer(777000)
	publicPrice := *oldPrice
	publicPrice.MultiplierIncluded, publicPrice.Reference = false, true
	independentPrice := *oldPrice
	independentPrice.Note, independentPrice.AppliedMultiplier = "使用图片独立倍率", floatPointer(1)
	groups := []Group{
		{Name: "automatic", PriceMode: "auto", Routes: []Route{{ChannelID: channel.ID, Model: "test-model", Pricing: oldPrice}, {ChannelID: otherChannel.ID, Model: "test-model", Pricing: &otherPrice}}},
		{Name: "manual", PriceMode: "manual", InputPriceMicros: 123, Routes: []Route{{ChannelID: channel.ID, Model: "test-model", Pricing: oldPrice}}},
		{Name: "explicit-reference", PriceMode: "auto", PriceModel: "test-model", Routes: []Route{{ChannelID: channel.ID, Model: "test-model", Pricing: oldPrice}}},
		{Name: "public", PriceMode: "auto", Routes: []Route{{ChannelID: channel.ID, Model: "test-model", Pricing: &publicPrice}}},
		{Name: "independent", PriceMode: "auto", Routes: []Route{{ChannelID: channel.ID, Model: "image-model", Pricing: &independentPrice}}},
	}
	for i := range groups {
		if err := a.DB.Create(&groups[i]).Error; err != nil {
			t.Fatal(err)
		}
	}
	history := RequestDay{Date: usageDay(time.Now()).Format("2006-01-02"), Requests: 1, CostMicros: 456789}
	if err := a.DB.Create(&history).Error; err != nil {
		t.Fatal(err)
	}
	read := func() Group {
		t.Helper()
		var saved Group
		if err := a.DB.Preload("Routes").First(&saved, groups[0].ID).Error; err != nil {
			t.Fatal(err)
		}
		return saved
	}
	collect := func() {
		t.Helper()
		checked, err := a.checkAccount(context.Background(), account.ID)
		if err != nil || checked.Status != "ok" {
			t.Fatal("price refresh affected account collection", err)
		}
	}
	collect()
	saved := read()
	price := saved.Routes[0].Pricing
	if priceCalls.Load() != 1 || price == nil || *price.Input != 400000 || *price.Output != 2000000 || *price.CacheRead != 40000 || *price.CacheWrite != 500000 || *price.CacheWrite1h != 800000 || *price.Tiers[0].Input != 800000 || *price.AppliedMultiplier != 0.2 || price.Stale || *saved.Pricing.Input != 400000 {
		t.Fatalf("rate collection did not refresh all price dimensions: %s", jsonString(saved))
	}
	if jsonString(saved.Routes[1].Pricing) != jsonString(&otherPrice) {
		t.Fatal("another account's route pricing changed")
	}
	for _, original := range groups[1:] {
		var current Group
		if err := a.DB.Preload("Routes").First(&current, original.ID).Error; err != nil || jsonString(current.Routes[0].Pricing) != jsonString(original.Routes[0].Pricing) {
			t.Fatal("manual, explicit, public or independent pricing changed", err)
		}
	}
	var record RequestLog
	a.recordRequestCost(&record, completion{Input: 100, Output: 10, UsageSeen: true}, saved, saved.Routes[0], channel, requestRate{Linked: true, Multiplier: floatPointer(0.2)})
	if record.CostMicros != 60 || record.CostWarning != "" {
		t.Fatalf("new request used old rate: %+v", record)
	}
	collect()
	if priceCalls.Load() != 1 {
		t.Fatal("unchanged rates caused unnecessary price fetching")
	}
	factor.Store(5)
	failPrices.Store(true)
	collect()
	saved = read()
	if *saved.Routes[0].Pricing.Input != 400000 || !saved.Routes[0].Pricing.Stale || !strings.Contains(saved.PriceSyncError, "倍率变化后价格同步失败") {
		t.Fatal("failed refresh did not retain and mark the last price")
	}
	failPrices.Store(false)
	collect()
	saved = read()
	if *saved.Routes[0].Pricing.Input != 100000 || saved.Routes[0].Pricing.Stale || saved.PriceSyncError != "" {
		t.Fatal("unchanged new rate did not retry a failed price refresh")
	}
	factor.Store(0)
	func() {
		a.priceSyncMu.Lock()
		defer a.priceSyncMu.Unlock()
		collect()
	}()
	if *read().Routes[0].Pricing.Input != 100000 {
		t.Fatal("account collection bypassed an ongoing price sync")
	}
	collect()
	if *read().Routes[0].Pricing.Input != 0 {
		t.Fatal("price sync skipped during contention was not retried, or zero rate was lost")
	}
	factor.Store(10)
	collect()
	if *read().Routes[0].Pricing.Input != 200000 {
		t.Fatal("cannot recover a nonzero price after a free rate")
	}
	groupID.Store(8)
	func() {
		a.priceSyncMu.Lock()
		defer a.priceSyncMu.Unlock()
		collect()
	}()
	collect()
	if *read().Routes[0].Pricing.Input != 400000 {
		t.Fatal("moving a key to another group with the same multiplier was missed")
	}
	var retained RequestDay
	if err := a.DB.First(&retained, "date = ?", history.Date).Error; err != nil || retained.CostMicros != history.CostMicros {
		t.Fatal("price refresh rewrote historical estimates", err)
	}
}

func TestFullPriceSyncRejectsChangedRateSnapshot(t *testing.T) {
	a := testApp(t)
	mockPriceSources(t, a, nil)
	account := Account{Name: "site", Kind: "sub2api", AuthMode: "token", Currency: "USD"}
	var changedRates string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/model-plaza" {
			w.WriteHeader(404)
			return
		}
		// 模拟全量同步等待上游时，账户采集已经保存了新倍率。
		if err := a.DB.Model(&account).Update("rates", changedRates).Error; err != nil {
			t.Error(err)
		}
		fmt.Fprint(w, `{"data":{"groups":[{"id":7,"models":[{"name":"test-model","pricing":{"billing_mode":"token","input_price":0.000002,"output_price":0.00001}}]}]}}`)
	}))
	defer server.Close()
	account.BaseURL = server.URL
	if err := a.DB.Create(&account).Error; err != nil {
		t.Fatal(err)
	}
	channel := Channel{AccountID: &account.ID, BaseURL: server.URL}
	if err := a.DB.Create(&channel).Error; err != nil {
		t.Fatal(err)
	}
	snapshot := rateSnapshot{Channels: map[uint]channelRate{channel.ID: {upstreamRate: upstreamRate{GroupID: "7", Multiplier: floatPointer(0.1)}, Identity: channelRateIdentity(channel)}}}
	if err := a.DB.Model(&account).Update("rates", jsonString(snapshot)).Error; err != nil {
		t.Fatal(err)
	}
	rate := snapshot.Channels[channel.ID]
	rate.Multiplier = floatPointer(0.2)
	snapshot.Channels[channel.ID] = rate
	changedRates = jsonString(snapshot)
	price := &modelPricing{Mode: "token", MultiplierIncluded: true, AppliedMultiplier: floatPointer(0.1), ChannelIdentity: pricingIdentity(channel), priceRates: priceRates{Input: intPointer(123), Output: intPointer(456)}}
	group := Group{Name: "test-model", PriceMode: "auto", Routes: []Route{{ChannelID: channel.ID, Model: "test-model", Pricing: price}}}
	if err := a.DB.Create(&group).Error; err != nil {
		t.Fatal(err)
	}
	result, err := a.syncPricesContext(context.Background())
	if err != nil || len(result.Unmatched) != 1 {
		t.Fatal("changed rate was not reported as needing a retry", err)
	}
	var saved Group
	if err := a.DB.Preload("Routes").First(&saved, group.ID).Error; err != nil || jsonString(saved.Routes[0].Pricing) != jsonString(price) || !strings.Contains(saved.PriceSyncError, "账户配置或倍率已变化") {
		t.Fatal("full sync overwrote a route with a superseded rate", err)
	}
}
