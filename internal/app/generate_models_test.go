package app

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"gorm.io/gorm"
)

func generateModelsForTest(t *testing.T, handler http.Handler, cookie string, ids ...uint) modelGenerationResult {
	t.Helper()
	response := call(handler, "POST", "/api/groups/generate", jsonString(object{"channel_ids": ids}), cookie)
	var result modelGenerationResult
	if response.Code != 200 || json.Unmarshal(response.Body.Bytes(), &result) != nil {
		t.Fatalf("generation failed %d %s", response.Code, response.Body.String())
	}
	return result
}

func TestGenerateModelsFromImportedChannels(t *testing.T) {
	a := testApp(t)
	h := a.Router()
	cookie := loginForTest(t, h)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer private-upstream-key" {
			t.Error("upstream credential missing")
		}
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/one/v1/models":
			fmt.Fprint(w, `{"data":[{"id":"shared"},{"id":"alpha"},{"id":"shared"}]}`)
		case "/two/v1/models":
			fmt.Fprint(w, `{"data":[{"id":"shared"},{"id":"beta"}]}`)
		case "/one/v1/chat/completions", "/two/v1/chat/completions":
			chatResponse(w)
		default:
			t.Errorf("unexpected endpoint %s", r.URL.Path)
			w.WriteHeader(404)
		}
	}))
	defer upstream.Close()
	one := createChannel(t, h, cookie, "imported", upstream.URL+"/one", "openai")
	two := createChannel(t, h, cookie, "direct", upstream.URL+"/two", "openai")
	account := Account{Name: "account", Kind: "sub2api", BaseURL: upstream.URL, Enabled: true}
	if err := a.DB.Create(&account).Error; err != nil {
		t.Fatal(err)
	}
	if err := a.DB.Model(&one).Updates(map[string]any{"account_id": account.ID, "upstream_key_id": 100}).Error; err != nil {
		t.Fatal(err)
	}
	shared := Group{Name: "shared", Strategy: "priority", InputPriceMicros: 123456, OutputPriceMicros: 4567890, Routes: []Route{{ChannelID: one.ID, Model: "shared", Weight: 7, Priority: 6}}}
	alias := Group{Name: "personal-alias", Strategy: "weighted", Routes: []Route{{ChannelID: two.ID, Model: "custom-upstream", Weight: 9, Priority: 2}}}
	for _, group := range []*Group{&shared, &alias} {
		response := call(h, "POST", "/api/groups", jsonString(group), cookie)
		if response.Code != 200 || json.Unmarshal(response.Body.Bytes(), group) != nil {
			t.Fatal("could not prepare existing route")
		}
	}
	result := generateModelsForTest(t, h, cookie, one.ID, two.ID)
	if result.CreatedModels != 2 || result.AddedRoutes != 3 || result.ExistingRoutes != 1 || result.SyncedChannels != 2 || len(result.Failures) != 0 {
		t.Fatalf("unexpected generation summary %+v", result)
	}
	var saved Group
	if err := a.DB.Preload("Routes", func(tx *gorm.DB) *gorm.DB { return tx.Order("priority, id") }).First(&saved, shared.ID).Error; err != nil {
		t.Fatal(err)
	}
	if saved.Strategy != shared.Strategy || saved.InputPriceMicros != shared.InputPriceMicros || saved.OutputPriceMicros != shared.OutputPriceMicros || len(saved.Routes) != 2 || saved.Routes[0] != shared.Routes[0] || saved.Routes[1].ChannelID != two.ID || saved.Routes[1].Weight != 1 || saved.Routes[1].Priority != 7 {
		t.Fatalf("existing model settings changed %+v", saved)
	}
	var savedAlias Group
	if err := a.DB.Preload("Routes").First(&savedAlias, alias.ID).Error; err != nil || !reflect.DeepEqual(savedAlias, alias) {
		t.Fatalf("manual alias changed %+v %v", savedAlias, err)
	}
	var alpha Group
	if err := a.DB.Preload("Routes").Where("name = ?", "alpha").First(&alpha).Error; err != nil || alpha.Strategy != "manual" || alpha.InputPriceMicros != 0 || len(alpha.Routes) != 1 || alpha.Routes[0].ChannelID != one.ID || alpha.Routes[0].Weight != 1 || alpha.ActiveRouteID != alpha.Routes[0].ID {
		t.Fatal("new model defaults or route mapping incorrect")
	}
	result = generateModelsForTest(t, h, cookie, one.ID, two.ID)
	if result.CreatedModels != 0 || result.AddedRoutes != 0 || result.ExistingRoutes != 4 {
		t.Fatalf("repeated generation duplicated routes %+v", result)
	}
	var synced Channel
	if err := a.DB.First(&synced, one.ID).Error; err != nil || synced.Models != `["shared","alpha"]` {
		t.Fatal("imported channel models not synchronized and deduplicated")
	}
	response := call(h, "POST", "/api/keys", `{"name":"generated route client"}`, cookie)
	var key struct{ Token string }
	if response.Code != 201 || json.Unmarshal(response.Body.Bytes(), &key) != nil {
		t.Fatal("could not create gateway key")
	}
	response = call(h, "GET", "/v1/models", "", key.Token)
	if response.Code != 200 || !strings.Contains(response.Body.String(), `"id":"alpha"`) || !strings.Contains(response.Body.String(), `"id":"beta"`) {
		t.Fatal("generated models not exposed to gateway clients")
	}
	response = call(h, "POST", "/v1/chat/completions", `{"model":"alpha","messages":[{"role":"user","content":"hi"}]}`, key.Token)
	if response.Code != 200 || !strings.Contains(response.Body.String(), `"model":"alpha"`) {
		t.Fatal("generated model could not forward requests")
	}
}

func TestGenerateModelsPartialFailures(t *testing.T) {
	a := testApp(t)
	h := a.Router()
	cookie := loginForTest(t, h)
	var recovered atomic.Bool
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/failed/v1/models":
			if !recovered.Load() {
				w.WriteHeader(401)
				return
			}
			fmt.Fprint(w, `{"data":[{"id":"recovered"}]}`)
		case "/empty/v1/models":
			fmt.Fprint(w, `{"data":[]}`)
		case "/invalid/v1/models":
			fmt.Fprint(w, jsonString(object{"data": []any{object{"id": strings.Repeat("x", 151)}}}))
		default:
			fmt.Fprint(w, `{"data":[{"id":"available"}]}`)
		}
	}))
	defer upstream.Close()
	good := createChannel(t, h, cookie, "good", upstream.URL+"/good", "openai")
	failed := createChannel(t, h, cookie, "failed", upstream.URL+"/failed", "openai")
	empty := createChannel(t, h, cookie, "empty", upstream.URL+"/empty", "openai")
	invalid := createChannel(t, h, cookie, "invalid", upstream.URL+"/invalid", "openai")
	unselected := createChannel(t, h, cookie, "unselected", upstream.URL+"/other", "openai")
	if err := a.DB.Model(&failed).Update("models", `["stale"]`).Error; err != nil {
		t.Fatal(err)
	}
	result := generateModelsForTest(t, h, cookie, good.ID, failed.ID, empty.ID, invalid.ID)
	if result.CreatedModels != 1 || result.AddedRoutes != 1 || len(result.Failures) != 3 {
		t.Fatalf("partial failures not reported %+v", result)
	}
	var groups []Group
	if err := a.DB.Preload("Routes").Find(&groups).Error; err != nil || len(groups) != 1 || groups[0].Name != "available" || len(groups[0].Routes) != 1 || groups[0].Routes[0].ChannelID != good.ID {
		t.Fatal("failed, empty, invalid or unselected channel produced routes")
	}
	var stored Channel
	if err := a.DB.First(&stored, failed.ID).Error; err != nil || stored.Models != `["stale"]` {
		t.Fatal("failed synchronization destroyed existing model cache")
	}
	stored = Channel{}
	if err := a.DB.First(&stored, unselected.ID).Error; err != nil || stored.Models != "" {
		t.Fatal("unselected channel was synchronized")
	}
	recovered.Store(true)
	result = generateModelsForTest(t, h, cookie, good.ID, failed.ID)
	if result.CreatedModels != 1 || result.AddedRoutes != 1 || result.ExistingRoutes != 1 || len(result.Failures) != 0 {
		t.Fatalf("partial failure retry did not converge %+v", result)
	}
}

func TestGenerateModelsValidationAndAuthentication(t *testing.T) {
	a := testApp(t)
	h := a.Router()
	cookie := loginForTest(t, h)
	channel := createChannel(t, h, cookie, "disabled", "https://example.invalid", "openai")
	if err := a.DB.Model(&channel).Update("enabled", false).Error; err != nil {
		t.Fatal(err)
	}
	for _, ids := range [][]uint{{}, {0}, {99999}, {channel.ID}, {1, 1}, make([]uint, 101)} {
		response := call(h, "POST", "/api/groups/generate", jsonString(object{"channel_ids": ids}), cookie)
		if response.Code != 400 {
			t.Fatalf("invalid selection accepted %v %d", ids, response.Code)
		}
	}
	if response := call(h, "POST", "/api/groups/generate", `{"channel_ids":[1]}`, ""); response.Code != 401 {
		t.Fatal("generation exposed without admin authentication")
	}
	req := httptest.NewRequest("POST", "/api/groups/generate", strings.NewReader(`{"channel_ids":[1]}`))
	req.Header.Set("Cookie", cookie)
	req.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	h.ServeHTTP(response, req)
	if response.Code != 403 {
		t.Fatal("generation allowed without CSRF validation")
	}
}

func TestGenerateModelsRollsBackRoutingOnFailure(t *testing.T) {
	a := testApp(t)
	h := a.Router()
	cookie := loginForTest(t, h)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"data":[{"id":"model-a"},{"id":"model-b"}]}`)
	}))
	defer upstream.Close()
	channel := createChannel(t, h, cookie, "channel", upstream.URL, "openai")
	if err := a.DB.Exec(`CREATE TRIGGER reject_second_generated_route BEFORE INSERT ON routes WHEN NEW.model = 'model-b' BEGIN SELECT RAISE(ABORT, 'test failure'); END`).Error; err != nil {
		t.Fatal(err)
	}
	response := call(h, "POST", "/api/groups/generate", jsonString(object{"channel_ids": []uint{channel.ID}}), cookie)
	if response.Code != 409 {
		t.Fatal("routing save failure hidden")
	}
	var models, routes int64
	if err := a.DB.Model(&Group{}).Count(&models).Error; err != nil {
		t.Fatal(err)
	}
	if err := a.DB.Model(&Route{}).Count(&routes).Error; err != nil || models != 0 || routes != 0 {
		t.Fatal("failed generation left partial routing configuration")
	}
}

func TestConcurrentModelGeneration(t *testing.T) {
	a := testApp(t)
	h := a.Router()
	cookie := loginForTest(t, h)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"data":[{"id":"shared"}]}`)
	}))
	defer upstream.Close()
	channel := createChannel(t, h, cookie, "channel", upstream.URL, "openai")
	responses := make([]*httptest.ResponseRecorder, 2)
	var workers sync.WaitGroup
	for i := range responses {
		workers.Go(func() {
			responses[i] = call(h, "POST", "/api/groups/generate", jsonString(object{"channel_ids": []uint{channel.ID}}), cookie)
		})
	}
	workers.Wait()
	for _, response := range responses {
		if response.Code != 200 {
			t.Fatalf("concurrent generation failed %s", response.Body.String())
		}
	}
	var groups []Group
	if err := a.DB.Preload("Routes").Find(&groups).Error; err != nil || len(groups) != 1 || len(groups[0].Routes) != 1 {
		t.Fatal("concurrent generation created duplicate models or routes")
	}
}

func TestGenerateModelsRechecksChannelAvailability(t *testing.T) {
	a := testApp(t)
	h := a.Router()
	cookie := loginForTest(t, h)
	var channelID atomic.Uint32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := a.DB.Model(&Channel{}).Where("id = ?", channelID.Load()).Update("enabled", false).Error; err != nil {
			t.Error(err)
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"data":[{"id":"must-not-generate"}]}`)
	}))
	defer upstream.Close()
	channel := createChannel(t, h, cookie, "channel", upstream.URL, "openai")
	channelID.Store(uint32(channel.ID))
	result := generateModelsForTest(t, h, cookie, channel.ID)
	if result.CreatedModels != 0 || result.AddedRoutes != 0 || len(result.Failures) != 1 {
		t.Fatalf("disabled channel generated routing %+v", result)
	}
}
