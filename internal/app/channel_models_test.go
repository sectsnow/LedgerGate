package app

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func channelEditInput(channel Channel) object {
	return object{"name": channel.Name, "account_id": channel.AccountID, "base_url": channel.BaseURL, "protocols": channel.supportedProtocols(), "enabled": channel.Enabled, "timeout_seconds": channel.TimeoutSeconds, "cooldown_seconds": channel.CooldownSeconds}
}

func TestSaveChannelModels(t *testing.T) {
	a := testApp(t)
	h := a.Router()
	cookie := loginForTest(t, h)
	channel := createChannel(t, h, cookie, "editable", "https://example.invalid", "openai")
	var stored Channel
	a.DB.First(&stored, channel.ID)
	originalKey := stored.APIKey
	path := fmt.Sprintf("/api/channels/%d", channel.ID)
	body := channelEditInput(channel)
	body["models"], body["expected_models"] = []string{" kept ", "manual-added", "kept"}, channel.Models
	if response := call(h, "PUT", path, jsonString(body), ""); response.Code != 401 {
		t.Fatal("unauthenticated model editing accepted")
	}
	response := call(h, "PUT", path, jsonString(body), cookie)
	if response.Code != 200 || json.Unmarshal(response.Body.Bytes(), &channel) != nil || channel.Models != `["kept","manual-added"]` {
		t.Fatalf("model normalization or save failed: %d %s", response.Code, response.Body.String())
	}
	body = channelEditInput(channel)
	body["name"] = "config-only"
	response = call(h, "PUT", path, jsonString(body), cookie)
	if response.Code != 200 {
		t.Fatal(response.Body.String())
	}
	a.DB.First(&stored, channel.ID)
	if stored.Models != channel.Models || stored.APIKey != originalKey || stored.Name != "config-only" {
		t.Fatal("config-only edit changed model list or credentials")
	}
	group := Group{Name: "existing-route", Strategy: "priority", Routes: []Route{{ChannelID: channel.ID, Model: "kept", Weight: 1}}}
	if err := a.DB.Create(&group).Error; err != nil {
		t.Fatal(err)
	}
	body = channelEditInput(stored)
	body["models"], body["expected_models"] = []string{}, stored.Models
	response = call(h, "PUT", path, jsonString(body), cookie)
	if response.Code != 200 {
		t.Fatal(response.Body.String())
	}
	a.DB.First(&stored, channel.ID)
	var routes int64
	a.DB.Model(&Route{}).Where("group_id = ?", group.ID).Count(&routes)
	if stored.Models != "[]" || routes != 1 || len(a.candidates(group)) != 1 {
		t.Fatal("clearing model catalog removed existing forwarding routes")
	}
}

func TestChannelModelValidationAndConflict(t *testing.T) {
	a := testApp(t)
	h := a.Router()
	cookie := loginForTest(t, h)
	channel := createChannel(t, h, cookie, "unchanged", "https://example.invalid", "openai")
	tooMany := make([]string, 10001)
	for i := range tooMany {
		tooMany[i] = "model"
	}
	cases := []struct {
		models   any
		expected any
		status   int
	}{
		{[]string{""}, "", 400},
		{[]string{"  "}, "", 400},
		{[]string{strings.Repeat("x", 151)}, "", 400},
		{[]string{strings.Repeat("中", 51)}, "", 400},
		{[]string{"with\nnewline"}, "", 400},
		{[]string{"with\x00null"}, "", 400},
		{[]any{1}, "", 400},
		{"model", "", 400},
		{tooMany, "", 400},
		{[]string{"new"}, nil, 400},
		{[]string{"new"}, "stale", 409},
	}
	for i, test := range cases {
		body := channelEditInput(channel)
		body["name"], body["models"] = "must-not-save", test.models
		if test.expected != nil {
			body["expected_models"] = test.expected
		}
		response := call(h, "PUT", fmt.Sprintf("/api/channels/%d", channel.ID), jsonString(body), cookie)
		if response.Code != test.status {
			t.Fatalf("case %d: got %d: %s", i, response.Code, response.Body.String())
		}
		var stored Channel
		a.DB.First(&stored, channel.ID)
		if stored.Name != channel.Name || stored.Models != channel.Models {
			t.Fatalf("case %d partially saved invalid or stale model edits", i)
		}
	}
}

func TestModelSyncPreservesConcurrentManualEdit(t *testing.T) {
	a := testApp(t)
	h := a.Router()
	cookie := loginForTest(t, h)
	entered, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	unblock := func() { once.Do(func() { close(release) }) }
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		close(entered)
		<-release
		fmt.Fprint(w, `{"data":[{"id":"upstream-only"}]}`)
	}))
	defer upstream.Close()
	defer unblock()
	channel := createChannel(t, h, cookie, "concurrent", upstream.URL, "openai")
	var snapshot Channel
	a.DB.First(&snapshot, channel.ID)
	done := make(chan error, 1)
	go func() { _, err := a.syncChannelModels(context.Background(), snapshot); done <- err }()
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("sync never started")
	}
	body := channelEditInput(channel)
	body["models"], body["expected_models"] = []string{"manual-added"}, channel.Models
	response := call(h, "PUT", fmt.Sprintf("/api/channels/%d", channel.ID), jsonString(body), cookie)
	if response.Code != 200 {
		t.Fatal(response.Body.String())
	}
	unblock()
	select {
	case err := <-done:
		if err == nil || !strings.Contains(err.Error(), "更新") {
			t.Fatal("concurrent sync was allowed to overwrite manual edits")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("sync did not finish")
	}
	var stored Channel
	a.DB.First(&stored, channel.ID)
	if stored.Models != `["manual-added"]` {
		t.Fatal("manual model list lost")
	}
}

func TestGenerateUsesSavedModelLists(t *testing.T) {
	a := testApp(t)
	h := a.Router()
	cookie := loginForTest(t, h)
	var calls atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		fmt.Fprint(w, `{"data":[{"id":"upstream-only"}]}`)
	}))
	defer upstream.Close()
	manual := createChannel(t, h, cookie, "manual", upstream.URL, "openai")
	empty := createChannel(t, h, cookie, "cleared", upstream.URL, "openai")
	fresh := createChannel(t, h, cookie, "not-synced", upstream.URL, "openai")
	for id, models := range map[uint]string{manual.ID: `["kept","manual-added"]`, empty.ID: `[]`} {
		if err := a.DB.Model(&Channel{}).Where("id = ?", id).Update("models", models).Error; err != nil {
			t.Fatal(err)
		}
	}
	response := call(h, "POST", "/api/groups/generate", jsonString(object{"channel_ids": []uint{manual.ID, empty.ID}, "use_saved_models": true}), cookie)
	var result modelGenerationResult
	if response.Code != 200 || json.Unmarshal(response.Body.Bytes(), &result) != nil || result.CreatedModels != 2 || result.AddedRoutes != 2 || result.SyncedChannels != 0 || len(result.Failures) != 1 || calls.Load() != 0 {
		t.Fatalf("saved model generation failed %d %s", response.Code, response.Body.String())
	}
	response = call(h, "POST", "/api/groups/generate", jsonString(object{"channel_ids": []uint{fresh.ID}, "use_saved_models": true}), cookie)
	if response.Code != 200 || json.Unmarshal(response.Body.Bytes(), &result) != nil || result.SyncedChannels != 1 || len(result.Failures) != 0 || calls.Load() != 1 {
		t.Fatal("never-synced channel did not fetch its model list")
	}
	result = generateModelsForTest(t, h, cookie, manual.ID)
	if result.SyncedChannels != 1 || len(result.Failures) != 0 || calls.Load() != 2 {
		t.Fatal("explicit upstream sync no longer works")
	}
	var stored Channel
	a.DB.First(&stored, manual.ID)
	if stored.Models != `["upstream-only"]` {
		t.Fatal("explicit resync did not replace the saved catalog")
	}
}
