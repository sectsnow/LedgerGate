package app

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"sync/atomic"
	"testing"
)

func TestChannelModelDiscoveryBaselineAndAcknowledgement(t *testing.T) {
	a := testApp(t)
	h := a.Router()
	cookie := loginForTest(t, h)
	var available atomic.Value
	available.Store([]string{"kept", "unselected"})
	var failed atomic.Bool
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if failed.Load() {
			w.WriteHeader(503)
			return
		}
		data := []object{}
		for _, model := range available.Load().([]string) {
			data = append(data, object{"id": model})
		}
		_ = json.NewEncoder(w).Encode(object{"data": data})
	}))
	defer upstream.Close()
	channel := createChannel(t, h, cookie, "discovery", upstream.URL, "openai")
	if err := a.DB.Model(&channel).Update("models", `["kept","manual-model"]`).Error; err != nil {
		t.Fatal(err)
	}
	group := Group{Name: "kept", Strategy: "priority", Routes: []Route{{ChannelID: channel.ID, Model: "kept", Weight: 1}}}
	if err := a.DB.Create(&group).Error; err != nil {
		t.Fatal(err)
	}
	load := func() Channel {
		t.Helper()
		var current Channel
		if err := a.DB.First(&current, channel.ID).Error; err != nil {
			t.Fatal(err)
		}
		return current
	}
	discover := func(models ...string) Channel {
		t.Helper()
		available.Store(models)
		if err := a.discoverModels(context.Background()); err != nil {
			t.Fatal(err)
		}
		return load()
	}
	path := fmt.Sprintf("/api/channels/%d/models", channel.ID)
	first := discover("kept", "unselected")
	if first.ModelsBaseline != first.DiscoveredModels || first.Models != channel.Models {
		t.Fatal("first discovery should establish a baseline and preserve curated models", first)
	}
	changed := discover("unselected", "new-model", "kept")
	if changed.ModelsBaseline != first.ModelsBaseline || changed.DiscoveredModels == first.DiscoveredModels {
		t.Fatal("discovery should retain the baseline until confirmation")
	}
	if channelModelRevision(first) != channelModelRevision(changed) {
		t.Fatal("model discovery invalidated unrelated model-testing revisions")
	}
	ack := func(revision string, status int) {
		t.Helper()
		w := call(h, "POST", path+"/acknowledge", jsonString(object{"revision": revision}), cookie)
		if w.Code != status {
			t.Fatalf("acknowledgement: want %d, got %d: %s", status, w.Code, w.Body.String())
		}
	}
	ack(channelModelDiscoveryRevision(first), 409)
	var listed []struct {
		Channel  Channel `json:"channel"`
		Revision string  `json:"models_revision"`
	}
	w := call(h, "GET", "/api/channels", "", cookie)
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &listed) != nil || len(listed) != 1 || listed[0].Revision != channelModelDiscoveryRevision(changed) {
		t.Fatal("channel list did not include the current discovery version", w.Body.String())
	}
	ack(listed[0].Revision, 200)
	confirmed := load()
	if confirmed.ModelsBaseline != changed.DiscoveredModels || confirmed.Models != first.Models {
		t.Fatal("keeping the current selection modified models or failed to acknowledge the snapshot")
	}
	reordered := discover("new-model", "kept", "unselected")
	if reordered.ModelsBaseline != reordered.DiscoveredModels {
		t.Fatal("identical or reordered results reintroduced a confirmed change")
	}
	missing := discover("new-model", "unselected")
	if missing.Models != first.Models || missing.ModelsBaseline == missing.DiscoveredModels {
		t.Fatal("an upstream omission removed a selected model or was not detected")
	}
	failed.Store(true)
	if err := a.discoverModels(context.Background()); err == nil {
		t.Fatal("failed discovery was reported as successful")
	}
	broken := load()
	if broken.ModelsSyncError == "" || broken.DiscoveredModels != missing.DiscoveredModels || broken.ModelsBaseline != missing.ModelsBaseline || !broken.ModelsCheckedAt.Equal(*missing.ModelsCheckedAt) {
		t.Fatal("failed discovery overwrote the successful snapshot or timestamp")
	}
	ack(channelModelDiscoveryRevision(broken), 409)
	failed.Store(false)
	preview := previewModelsForTest(t, h, cookie, channel)
	discover("new-model", "unselected", "later-model")
	w = call(h, "PUT", path, jsonString(object{"models": []string{"kept", "manual-model"}, "revision": preview.Revision}), cookie)
	if w.Code != 409 || load().Models != first.Models {
		t.Fatal("a stale preview acknowledged a newer upstream change", w.Body.String())
	}
	preview = previewModelsForTest(t, h, cookie, channel)
	w = call(h, "PUT", path, jsonString(object{"models": []string{"kept", "manual-model"}, "revision": preview.Revision}), cookie)
	if w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	synced := load()
	if synced.Models != first.Models || synced.ModelsBaseline != synced.DiscoveredModels {
		t.Fatal("saving a curated selection did not acknowledge the available list")
	}
	var routes []Route
	if err := a.DB.Where("group_id = ?", group.ID).Find(&routes).Error; err != nil || len(routes) != 1 || routes[0].Model != "kept" {
		t.Fatal("model discovery or confirmation modified a forwarding route")
	}
	if _, err := a.syncChannelModels(context.Background(), synced); err != nil {
		t.Fatal(err)
	}
	if direct := load(); direct.ModelsBaseline != direct.DiscoveredModels {
		t.Fatal("explicit model sync left an unacknowledged result")
	}
}

func TestChannelModelAcknowledgementValidationAndSourceReset(t *testing.T) {
	a := testApp(t)
	h := a.Router()
	cookie := loginForTest(t, h)
	channel := createChannel(t, h, cookie, "reset", "https://upstream.invalid", "openai")
	if err := a.DB.Model(&channel).Updates(map[string]any{"models": `["kept"]`, "discovered_models": `["kept","new"]`, "models_baseline": `["kept"]`}).Error; err != nil {
		t.Fatal(err)
	}
	if err := a.DB.First(&channel, channel.ID).Error; err != nil {
		t.Fatal(err)
	}
	path := fmt.Sprintf("/api/channels/%d/models/acknowledge", channel.ID)
	body := jsonString(object{"revision": channelModelDiscoveryRevision(channel)})
	if w := call(h, "POST", path, body, ""); w.Code != 401 {
		t.Fatal("unauthenticated acknowledgement accepted")
	}
	req := httptest.NewRequest("POST", path, nil)
	req.Header.Set("Cookie", cookie)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != 403 {
		t.Fatal("acknowledgement without request protection accepted")
	}
	if w := call(h, "POST", path, `{}`, cookie); w.Code != 400 {
		t.Fatal("missing revision accepted")
	}
	if w := call(h, "POST", "/api/channels/999999/models/acknowledge", body, cookie); w.Code != 404 {
		t.Fatal("missing channel was not reported")
	}
	input := channelEditInput(channel)
	input["name"] = "renamed"
	w = call(h, "PUT", fmt.Sprintf("/api/channels/%d", channel.ID), jsonString(input), cookie)
	if w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	var renamed Channel
	a.DB.First(&renamed, channel.ID)
	if renamed.ModelsBaseline != channel.ModelsBaseline || renamed.DiscoveredModels != channel.DiscoveredModels {
		t.Fatal("an unrelated edit cleared discovery history")
	}
	input["api_key"] = "replacement-key"
	w = call(h, "PUT", fmt.Sprintf("/api/channels/%d", channel.ID), jsonString(input), cookie)
	if w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	var changed Channel
	a.DB.First(&changed, channel.ID)
	if changed.DiscoveredModels != "" || changed.ModelsBaseline != "" || changed.ModelsCheckedAt != nil || changed.Models != channel.Models {
		t.Fatal("changing the upstream key retained a previous source snapshot or changed selected models")
	}
	if w := call(h, "POST", path, body, cookie); w.Code != 409 {
		t.Fatal("an old-source acknowledgement was accepted")
	}
}

func TestChannelModelDiscoveryUpdates(t *testing.T) {
	for _, test := range []struct {
		name     string
		channel  Channel
		models   []string
		baseline string
	}{
		{"first_subset", Channel{Models: `["a"]`}, []string{"b", "a"}, `["a","b"]`},
		{"legacy_snapshot", Channel{DiscoveredModels: `["a"]`}, []string{"b", "a"}, `["a"]`},
		{"unconfirmed_change", Channel{ModelsBaseline: `["a"]`, DiscoveredModels: `["a","b"]`}, []string{"c", "a"}, `["a"]`},
		{"empty_snapshot", Channel{ModelsBaseline: `[]`}, []string{"b"}, `[]`},
	} {
		t.Run(test.name, func(t *testing.T) {
			before := append([]string{}, test.models...)
			updates := channelModelDiscoveryUpdates(test.channel, test.models)
			if updates["models_baseline"] != test.baseline || !reflect.DeepEqual(before, test.models) {
				t.Fatal("incorrect baseline or mutated model order", updates)
			}
		})
	}
}
