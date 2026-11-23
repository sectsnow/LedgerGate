package app

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

const testModelCatalog = `{
  "openai":{"models":{
    "gpt-known":{"cost":{"input":2.5,"output":10}},
    "gpt-missing":{"cost":{"input":1}},
    "gpt-negative":{"cost":{"input":-1,"output":2}},
    "gpt-image":{"type":"image","cost":{"input":0.1,"output":0.2}}
  }},
  "deepseek":{"models":{"deepseek-v4-pro":{"cost":{"input":0.66,"output":1.98,"cache_read":0.022}}}},
  "alibaba":{"models":{"deepseek-v4-pro":{"cost":{"input":2,"output":6}}}},
  "zai":{"models":{"glm-free":{"cost":{"input":0,"output":0}}}},
  "minimax":{"models":{"MiniMax-M3":{"cost":{"input":0.3,"output":1.2}}}},
  "reseller":{"models":{"gpt-known":{"cost":{"input":0.01,"output":0.02}}}}
}`

type catalogTransport func(*http.Request) (*http.Response, error)

func (transport catalogTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	return transport(request)
}

func mockCatalog(t *testing.T, a *App, handler http.Handler) {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	target, _ := url.Parse(server.URL)
	original := a.client.Transport
	a.client.Transport = catalogTransport(func(request *http.Request) (*http.Response, error) {
		if request.URL.String() == liteLLMPriceURL || request.URL.String() == openRouterPriceURL {
			return &http.Response{StatusCode: 503, Body: io.NopCloser(strings.NewReader(`{}`)), Header: http.Header{}}, nil
		}
		if request.URL.String() != modelPriceCatalogURL {
			return original.RoundTrip(request)
		}
		if request.Header.Get("Authorization") != "" || request.Header.Get("Cookie") != "" {
			t.Error("credentials were sent to the public price catalog")
		}
		copy := request.Clone(request.Context())
		newURL := *request.URL
		newURL.Scheme, newURL.Host = target.Scheme, target.Host
		copy.URL = &newURL
		return original.RoundTrip(copy)
	})
	t.Cleanup(func() { original.(*http.Transport).CloseIdleConnections() })
}

func TestModelVendorClassification(t *testing.T) {
	cases := map[string]string{
		"gpt-6.1-sol": "OpenAI", "claude-opus-5": "Anthropic", "gemini-4": "Google",
		"deepseek-v4.1-flash": "DeepSeek", "grok-4.6": "xAI", "kimi-k3": "Moonshot AI",
		"glm-5.3": "智谱", "MiniMax-M3": "MiniMax", "qwen3": "阿里云", "llama-4": "Meta",
		"codestral-latest": "Mistral", "openai/gpt-known": "OpenAI", "doubao-seed": "字节跳动",
		"hy4-preview": "腾讯", "unknown-model": "其他",
	}
	for model, want := range cases {
		if got := (Group{Name: model}).modelVendor(); got != want {
			t.Errorf("%s classified as %s, want %s", model, got, want)
		}
	}
	if got := (Group{Name: "alias", Routes: []Route{{Model: "deepseek-v4-pro"}}}).modelVendor(); got != "DeepSeek" {
		t.Fatal("alias did not inherit the upstream model vendor")
	}
	if got := (Group{Name: "gpt-known", Vendor: "自定义"}).modelVendor(); got != "自定义" {
		t.Fatal("manual vendor override ignored")
	}
	if got := (Group{Name: "alias", Routes: []Route{{Model: "deepseek-v4-pro"}, {Model: "gpt-known"}}}).modelVendor(); got != "其他" {
		t.Fatal("mixed vendor alias was assigned one vendor")
	}
}

func TestPriceMicros(t *testing.T) {
	for _, test := range []struct {
		value string
		want  int64
		valid bool
	}{
		{"0", 0, true}, {"0.66", 660000, true}, {"2.5", 2500000, true},
		{"0.0000005", 1, true}, {"0.00000049", 0, true}, {"1e-6", 1, true},
		{"1000000", 1_000_000_000_000, true}, {"1000000.1", 0, false},
		{"-1", 0, false}, {"-1e-1000000", 0, false}, {"1e-1000000", 0, true},
		{"1e1000000", 0, false}, {"invalid", 0, false},
	} {
		value := json.Number(test.value)
		got, valid := priceMicros(&value)
		if got != test.want || valid != test.valid {
			t.Fatalf("%s converted to %d %v, want %d %v", test.value, got, valid, test.want, test.valid)
		}
	}
	if _, valid := priceMicros(nil); valid {
		t.Fatal("missing price accepted as free")
	}
}

func TestPublicPriceMatching(t *testing.T) {
	a := testApp(t)
	mockCatalog(t, a, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, testModelCatalog) }))
	catalog, err := a.fetchPriceCatalog(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	price, ok := catalog.match("deepseek-v4-pro")
	if !ok || price.Provider != "deepseek" || price.Input != 660000 || price.Output != 1980000 {
		t.Fatal("model host price confused with original vendor")
	}
	price, ok = catalog.match("gpt-known")
	if !ok || price.Provider != "openai" || price.Input != 2500000 {
		t.Fatal("reseller price used as original vendor price")
	}
	if price, ok = catalog.match("reseller/gpt-known"); !ok || price.Input != 10000 {
		t.Fatal("explicit provider/model reference not supported")
	}
	if price, ok = catalog.match("minimax-m3"); !ok || price.Provider != "minimax" {
		t.Fatal("case-insensitive model match failed")
	}
	for _, name := range []string{"gpt-known-new-version", "gpt-missing", "gpt-negative"} {
		if _, ok := catalog.match(name); ok {
			t.Fatalf("invalid or inexact reference matched %s", name)
		}
	}
	if price, ok := catalog.match("gpt-image"); !ok || price.Pricing == nil || price.Pricing.Mode != "image" {
		t.Fatal("image pricing must retain its billing type")
	}
	if price, ok = catalog.match("glm-free"); !ok || price.Input != 0 || price.Output != 0 {
		t.Fatal("explicit zero price lost")
	}
	if _, reason := catalog.priceFor(Group{Name: "alias", Routes: []Route{{Model: "gpt-known"}, {Model: "deepseek-v4-pro"}}}); reason == "" {
		t.Fatal("mixed-price alias silently assigned a single price")
	}
	if price, reason := catalog.priceFor(Group{Name: "alias", Routes: []Route{{Model: "gpt-known"}, {Model: "gpt-known"}}}); reason != "" || price.Provider != "openai" {
		t.Fatal("same upstream model alias not resolved")
	}
	if price, reason := catalog.priceFor(Group{Name: "gpt-known", PriceModel: "deepseek/deepseek-v4-pro"}); reason != "" || price.Provider != "deepseek" {
		t.Fatal("explicit price model did not take precedence")
	}
}

func TestSyncModelPricesAndPreserveManualSettings(t *testing.T) {
	a := testApp(t)
	mockCatalog(t, a, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, testModelCatalog) }))
	h := a.Router()
	cookie := loginForTest(t, h)
	channel := createChannel(t, h, cookie, "upstream", "https://example.invalid", "openai")
	groups := []Group{
		{Name: "gpt-known", Strategy: "priority", Routes: []Route{{ChannelID: channel.ID, Model: "gpt-known", Weight: 7, Priority: 9}}},
		{Name: "deepseek-v4-pro", Strategy: "weighted"},
		{Name: "alias", Strategy: "weighted", Routes: []Route{{ChannelID: channel.ID, Model: "gpt-known", Weight: 3}}},
		{Name: "legacy-manual", InputPriceMicros: 123, OutputPriceMicros: 456},
		{Name: "manual-free", PriceMode: "manual"},
		{Name: "unknown"},
		{Name: "glm-free"},
	}
	for i := range groups {
		if err := a.DB.Create(&groups[i]).Error; err != nil {
			t.Fatal(err)
		}
	}
	response := call(h, "POST", "/api/groups/sync-prices", "", cookie)
	var result priceSyncResult
	if response.Code != 200 || json.Unmarshal(response.Body.Bytes(), &result) != nil || result.Synced != 4 || result.Manual != 2 || len(result.Unmatched) != 1 {
		t.Fatalf("bad sync result %d %s", response.Code, response.Body.String())
	}
	var saved Group
	if err := a.DB.Preload("Routes").First(&saved, groups[0].ID).Error; err != nil {
		t.Fatal(err)
	}
	if saved.Routes[0].Pricing == nil {
		t.Fatal("route pricing not saved")
	}
	savedRoutes := append([]Route(nil), saved.Routes...)
	for index := range savedRoutes {
		savedRoutes[index].Pricing = nil
	}
	if saved.InputPriceMicros != 2500000 || saved.OutputPriceMicros != 10000000 || saved.PriceSource != "models.dev" || saved.PriceSourceModel != "openai/gpt-known" || saved.PriceUpdatedAt == nil || saved.Strategy != "priority" || !reflect.DeepEqual(savedRoutes, groups[0].Routes) {
		t.Fatalf("pricing or routing incorrect %+v", saved)
	}
	manual := Group{}
	if err := a.DB.First(&manual, groups[3].ID).Error; err != nil || manual.InputPriceMicros != 123 || manual.OutputPriceMicros != 456 || manual.PriceUpdatedAt != nil {
		t.Fatal("legacy manual prices overwritten")
	}
	unknown := Group{}
	if err := a.DB.First(&unknown, groups[5].ID).Error; err != nil || unknown.PriceSyncError == "" || unknown.PriceSource != "" {
		t.Fatal("unmatched price not recorded")
	}
	response = call(h, "GET", "/api/groups", "", cookie)
	var listed []Group
	if response.Code != 200 || json.Unmarshal(response.Body.Bytes(), &listed) != nil || listed[0].Vendor != "OpenAI" || listed[2].Vendor != "OpenAI" || listed[3].PriceMode != "manual" || listed[3].PriceSource != "manual" {
		t.Fatal("list does not expose vendor or effective pricing mode")
	}
	// 编辑路由时客户端可能持有同步前的价格，自动模式沿用事务中的最新价格。
	input := groups[0]
	input.PriceMode, input.Vendor = "auto", "自定义分类"
	response = call(h, "PUT", fmt.Sprintf("/api/groups/%d", input.ID), jsonString(input), cookie)
	if response.Code != 200 {
		t.Fatal(response.Body.String())
	}
	if err := a.DB.First(&saved, input.ID).Error; err != nil || saved.InputPriceMicros != 2500000 || saved.PriceSource != "models.dev" || saved.Vendor != "自定义分类" {
		t.Fatal("editing vendor lost synchronized price")
	}
	input.PriceModel = "deepseek/deepseek-v4-pro"
	response = call(h, "PUT", fmt.Sprintf("/api/groups/%d", input.ID), jsonString(input), cookie)
	if response.Code != 200 {
		t.Fatal(response.Body.String())
	}
	saved = Group{}
	if err := a.DB.First(&saved, input.ID).Error; err != nil || saved.PriceSource != "" || saved.InputPriceMicros != 0 || saved.PriceUpdatedAt != nil {
		t.Fatal("price reference change retained unrelated price")
	}
	response = call(h, "POST", "/api/groups/sync-prices", "", cookie)
	if response.Code != 200 {
		t.Fatal(response.Body.String())
	}
	if err := a.DB.First(&saved, input.ID).Error; err != nil || saved.InputPriceMicros != 660000 || saved.PriceSourceModel != "deepseek/deepseek-v4-pro" || saved.Vendor != "自定义分类" {
		t.Fatal("reference sync or custom vendor failed")
	}
	input.PriceMode, input.InputPriceMicros, input.OutputPriceMicros = "manual", 0, 0
	response = call(h, "PUT", fmt.Sprintf("/api/groups/%d", input.ID), jsonString(input), cookie)
	if response.Code != 200 {
		t.Fatal(response.Body.String())
	}
	if err := a.DB.First(&saved, input.ID).Error; err != nil || saved.PriceSource != "manual" || saved.InputPriceMicros != 0 {
		t.Fatal("manual zero price not retained")
	}
}

func TestPriceSyncFailuresAndAccounting(t *testing.T) {
	a := testApp(t)
	var unavailable atomic.Bool
	mockCatalog(t, a, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if unavailable.Load() {
			w.WriteHeader(503)
			return
		}
		fmt.Fprint(w, testModelCatalog)
	}))
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { chatResponse(w) }))
	defer upstream.Close()
	h := a.Router()
	cookie := loginForTest(t, h)
	channel := createChannel(t, h, cookie, "upstream", upstream.URL, "openai")
	response := call(h, "POST", "/api/groups", jsonString(Group{Name: "gpt-known", Strategy: "priority", PriceMode: "auto", Routes: []Route{{ChannelID: channel.ID, Model: "gpt-known", Weight: 1}}}), cookie)
	if response.Code != 200 {
		t.Fatal(response.Body.String())
	}
	var group Group
	if err := json.Unmarshal(response.Body.Bytes(), &group); err != nil {
		t.Fatal(err)
	}
	response = call(h, "POST", "/api/groups/sync-prices", "", cookie)
	if response.Code != 200 {
		t.Fatal(response.Body.String())
	}
	response = call(h, "POST", "/api/keys", `{"name":"pricing test"}`, cookie)
	var key struct{ Token string }
	if json.Unmarshal(response.Body.Bytes(), &key) != nil {
		t.Fatal("failed to parse key")
	}
	response = call(h, "POST", "/v1/chat/completions", `{"model":"gpt-known","messages":[{"role":"user","content":"hi"}]}`, key.Token)
	if response.Code != 200 {
		t.Fatal(response.Body.String())
	}
	record := latestRequestForTest(t, a)
	if record.CostMicros != 750 {
		t.Fatalf("synchronized price not used for accounting %+v", record)
	}
	unavailable.Store(true)
	response = call(h, "POST", "/api/groups/sync-prices", "", cookie)
	if response.Code != 502 {
		t.Fatal("catalog outage not reported")
	}
	if err := a.DB.First(&group, group.ID).Error; err != nil || group.InputPriceMicros != 2500000 || group.PriceSource != "models.dev" {
		t.Fatal("catalog outage erased saved price")
	}
	if response = call(h, "POST", "/api/groups/sync-prices", "", ""); response.Code != 401 {
		t.Fatal("price sync exposed without admin auth")
	}
	req := httptest.NewRequest("POST", "/api/groups/sync-prices", nil)
	req.Header.Set("Cookie", cookie)
	response = httptest.NewRecorder()
	h.ServeHTTP(response, req)
	if response.Code != 403 {
		t.Fatal("price sync allowed without CSRF validation")
	}
}

func TestPriceSyncTransactionRollback(t *testing.T) {
	a := testApp(t)
	mockCatalog(t, a, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, testModelCatalog) }))
	for _, group := range []Group{{Name: "gpt-known"}, {Name: "deepseek-v4-pro"}} {
		if err := a.DB.Create(&group).Error; err != nil {
			t.Fatal(err)
		}
	}
	if err := a.DB.Exec(`CREATE TRIGGER reject_price_sync BEFORE UPDATE ON groups WHEN OLD.name = 'deepseek-v4-pro' BEGIN SELECT RAISE(ABORT, 'test failure'); END`).Error; err != nil {
		t.Fatal(err)
	}
	h := a.Router()
	response := call(h, "POST", "/api/groups/sync-prices", "", loginForTest(t, h))
	if response.Code != 500 {
		t.Fatal("database price update failure hidden")
	}
	var groups []Group
	if err := a.DB.Find(&groups).Error; err != nil {
		t.Fatal(err)
	}
	for _, group := range groups {
		if group.InputPriceMicros != 0 || group.OutputPriceMicros != 0 || group.PriceUpdatedAt != nil {
			t.Fatal("failed sync left partially updated prices")
		}
	}
}

func TestMalformedPriceCatalog(t *testing.T) {
	for _, body := range []string{`{}`, `[]`, `{"invalid":`, `{"openai":{"models":{"invalid":{"cost":{"input":-1,"output":2}}}}}`} {
		t.Run(body, func(t *testing.T) {
			a := testApp(t)
			mockCatalog(t, a, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, body) }))
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			if _, err := a.fetchPriceCatalog(ctx); err == nil {
				t.Fatal("invalid price catalog accepted")
			}
		})
	}
	if !strings.HasPrefix(modelPriceCatalogURL, "https://") {
		t.Fatal("price catalog should use HTTPS")
	}
}
