package app

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
)

type modelPreviewForTest struct {
	Models      []string `json:"models"`
	SavedModels []string `json:"saved_models"`
	Revision    string   `json:"revision"`
}

func previewModelsForTest(t *testing.T, h http.Handler, cookie string, channel Channel) modelPreviewForTest {
	t.Helper()
	w := call(h, "GET", fmt.Sprintf("/api/channels/%d/models/available", channel.ID), "", cookie)
	var preview modelPreviewForTest
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &preview) != nil || len(preview.Revision) != 64 {
		t.Fatal("preview failed", w.Body.String())
	}
	if strings.Contains(w.Body.String(), "private-upstream-key") || channel.APIKey != "" && strings.Contains(w.Body.String(), channel.APIKey) {
		t.Fatal("preview exposed credentials")
	}
	return preview
}

func TestChannelModelPreviewAndSelection(t *testing.T) {
	a := testApp(t)
	h := a.Router()
	cookie := loginForTest(t, h)
	var calls atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.Method != "GET" || r.URL.Path != "/v1/models" || (r.Header.Get("Authorization") != "Bearer private-upstream-key" && r.Header.Get("x-api-key") != "private-upstream-key") {
			t.Error("wrong model-list request or credentials")
		}
		fmt.Fprint(w, `{"data":[{"id":"alpha"},{"id":"beta"},{"id":"alpha"}]}`)
	}))
	defer upstream.Close()
	channel := createMultiProtocolChannel(t, h, cookie, upstream.URL, []string{"openai", "responses", "anthropic"})
	if a.DB.Model(&channel).Update("models", `["alpha","local-model"]`).Error != nil {
		t.Fatal("could not seed model list")
	}
	group := manualGroupForTest(t, h, cookie, channel)
	if a.DB.Preload("Routes").First(&group, group.ID).Error != nil {
		t.Fatal("could not load original model routes")
	}
	preview := previewModelsForTest(t, h, cookie, channel)
	if !reflect.DeepEqual(preview.Models, []string{"alpha", "beta"}) || !reflect.DeepEqual(preview.SavedModels, []string{"alpha", "local-model"}) || calls.Load() != 2 {
		t.Fatal("preview did not merge all protocols or preserve saved models", preview)
	}
	var before, after Channel
	if a.DB.First(&before, channel.ID).Error != nil || before.Models != `["alpha","local-model"]` {
		t.Fatal("preview overwrote saved models")
	}
	path := fmt.Sprintf("/api/channels/%d/models", channel.ID)
	w := call(h, "PUT", path, jsonString(object{"models": []string{" beta ", "beta"}, "revision": preview.Revision}), cookie)
	if w.Code != 200 || w.Body.String() != `["beta"]` {
		t.Fatal("selected-model save failed", w.Body.String())
	}
	if a.DB.First(&after, channel.ID).Error != nil || after.Models != `["beta"]` || after.APIKey != before.APIKey || after.BaseURL != before.BaseURL || after.Enabled != before.Enabled || !reflect.DeepEqual(after.Protocols, before.Protocols) || calls.Load() != 2 {
		t.Fatal("saving selection fetched again or changed channel configuration")
	}
	var current Group
	if a.DB.Preload("Routes").First(&current, group.ID).Error != nil || !reflect.DeepEqual(current, group) {
		t.Fatal("selection changed an existing model or its routes")
	}
	if w := call(h, "PUT", path, jsonString(object{"models": []string{"alpha"}, "revision": preview.Revision}), cookie); w.Code != 409 {
		t.Fatal("stale preview overwrote a newer selection")
	}
	w = call(h, "POST", "/api/groups/generate", jsonString(object{"channel_ids": []uint{channel.ID}, "use_saved_models": true}), cookie)
	var result modelGenerationResult
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &result) != nil || result.CreatedModels != 1 || calls.Load() != 2 {
		t.Fatal("generation did not use the saved selection", w.Body.String())
	}
	var generated []Group
	if a.DB.Where("name IN ?", []string{"alpha", "beta"}).Find(&generated).Error != nil || len(generated) != 1 || generated[0].Name != "beta" || generated[0].Strategy != "manual" {
		t.Fatal("generation included an unselected model or lost manual default")
	}
}

func TestChannelModelSelectionRejectsChangedConfiguration(t *testing.T) {
	for _, change := range []string{"models", "base_url", "api_key", "protocols", "deleted"} {
		t.Run(change, func(t *testing.T) {
			a := testApp(t)
			h := a.Router()
			cookie := loginForTest(t, h)
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, `{"data":[{"id":"alpha"}]}`) }))
			defer upstream.Close()
			channel := createChannel(t, h, cookie, "channel", upstream.URL, "openai")
			preview := previewModelsForTest(t, h, cookie, channel)
			status := 409
			if change == "deleted" {
				if a.DB.Delete(&channel).Error != nil {
					t.Fatal("could not delete channel")
				}
				status = 404
			} else {
				value := map[string]string{"models": `["edited"]`, "base_url": "https://changed.invalid", "api_key": a.seal("different-key"), "protocols": `["anthropic"]`}[change]
				if a.DB.Model(&channel).Update(change, value).Error != nil {
					t.Fatal("could not update channel")
				}
			}
			w := call(h, "PUT", fmt.Sprintf("/api/channels/%d/models", channel.ID), jsonString(object{"models": []string{"alpha"}, "revision": preview.Revision}), cookie)
			if w.Code != status {
				t.Fatal("changed configuration accepted", change, w.Body.String())
			}
			if change == "models" {
				var saved Channel
				if a.DB.First(&saved, channel.ID).Error != nil || saved.Models != `["edited"]` {
					t.Fatal("concurrent edits were overwritten")
				}
			}
		})
	}
}

func TestChannelModelPreviewFailureAndValidation(t *testing.T) {
	a := testApp(t)
	h := a.Router()
	cookie := loginForTest(t, h)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(503) }))
	defer upstream.Close()
	channel := createChannel(t, h, cookie, "channel", upstream.URL, "openai")
	if a.DB.Model(&channel).Update("models", `["saved"]`).Error != nil {
		t.Fatal("could not seed models")
	}
	path := fmt.Sprintf("/api/channels/%d/models", channel.ID)
	if w := call(h, "GET", path+"/available", "", ""); w.Code != 401 {
		t.Fatal("unauthenticated preview allowed")
	}
	if w := call(h, "GET", path+"/available", "", cookie); w.Code != 502 {
		t.Fatal("upstream failure was hidden")
	}
	var saved Channel
	if a.DB.First(&saved, channel.ID).Error != nil || saved.Models != `["saved"]` {
		t.Fatal("failed preview changed saved models")
	}
	body := jsonString(object{"models": []string{"alpha"}, "revision": channelModelRevision(saved)})
	if w := call(h, "PUT", path, body, ""); w.Code != 401 {
		t.Fatal("unauthenticated save allowed")
	}
	req := httptest.NewRequest("PUT", path, strings.NewReader(body))
	req.Header.Set("Cookie", cookie)
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != 403 {
		t.Fatal("save without CSRF header allowed")
	}
	for _, models := range []any{nil, "alpha", []string{""}, []string{strings.Repeat("a", 151)}, []string{"bad\nmodel"}} {
		w := call(h, "PUT", path, jsonString(object{"models": models, "revision": channelModelRevision(saved)}), cookie)
		if w.Code != 400 {
			t.Fatal("invalid selection accepted", w.Body.String())
		}
	}
}
