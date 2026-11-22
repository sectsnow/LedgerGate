package app

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"gorm.io/gorm"
)

func manualGroupForTest(t *testing.T, h http.Handler, cookie string, channels ...Channel) Group {
	t.Helper()
	routes := []Route{}
	for index, channel := range channels {
		routes = append(routes, Route{ChannelID: channel.ID, Model: fmt.Sprintf("model-%d", index), Weight: 1, Priority: index})
	}
	response := call(h, "POST", "/api/groups", jsonString(Group{Name: "manual-model", Routes: routes}), cookie)
	var group Group
	if response.Code != 200 || json.Unmarshal(response.Body.Bytes(), &group) != nil {
		t.Fatal("default model creation failed", response.Body.String())
	}
	if group.Strategy != "manual" || group.ActiveRouteID == 0 || group.ActiveRouteID != group.Routes[0].ID {
		t.Fatal("new model does not default to first manual route")
	}
	return group
}

func TestManualRouteSelectionAndEditing(t *testing.T) {
	a := testApp(t)
	h := a.Router()
	cookie := loginForTest(t, h)
	var calls [2]atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body object
		if json.NewDecoder(r.Body).Decode(&body) != nil {
			t.Error("invalid request")
		}
		index, err := strconv.Atoi(strings.TrimPrefix(str(body["model"]), "model-"))
		if err != nil || index < 0 || index > 1 {
			t.Error("incorrect model mapping")
			w.WriteHeader(400)
			return
		}
		calls[index].Add(1)
		if body["stream"] == true {
			w.Header().Set("Content-Type", "text/event-stream")
			fmt.Fprint(w, streamFixture("openai"))
			return
		}
		chatResponse(w)
	}))
	defer upstream.Close()
	channel := createChannel(t, h, cookie, "shared-channel", upstream.URL, "openai")
	// 同一渠道映射到不同上游模型时也必须按路由选择。
	group := manualGroupForTest(t, h, cookie, channel, channel)
	response := call(h, "POST", "/api/keys", `{"name":"manual client"}`, cookie)
	var key struct{ Token string }
	if response.Code != 201 || json.Unmarshal(response.Body.Bytes(), &key) != nil {
		t.Fatal(response.Body.String())
	}
	request := `{"model":"manual-model","messages":[{"role":"user","content":"hi"}]}`
	for range 4 {
		if response := call(h, "POST", "/v1/chat/completions", request, key.Token); response.Code != 200 {
			t.Fatal(response.Body.String())
		}
	}
	if calls[0].Load() != 4 || calls[1].Load() != 0 {
		t.Fatal("manual mode distributed requests")
	}
	path := fmt.Sprintf("/api/groups/%d/selection", group.ID)
	response = call(h, "PATCH", path, jsonString(object{"route_id": group.Routes[1].ID}), cookie)
	if response.Code != 200 {
		t.Fatal(response.Body.String())
	}
	for range 4 {
		if response := call(h, "POST", "/v1/chat/completions", request, key.Token); response.Code != 200 {
			t.Fatal(response.Body.String())
		}
	}
	if calls[0].Load() != 4 || calls[1].Load() != 4 {
		t.Fatal("manual switch did not choose requested route")
	}
	if a.DB.Preload("Routes").First(&group, group.ID).Error != nil {
		t.Fatal("could not load manual model")
	}
	oldActive := group.ActiveRouteID
	group.PriceMode = "manual"
	group.InputPriceMicros = 123456
	response = call(h, "PUT", fmt.Sprintf("/api/groups/%d", group.ID), jsonString(group), cookie)
	if response.Code != 200 || json.Unmarshal(response.Body.Bytes(), &group) != nil {
		t.Fatal(response.Body.String())
	}
	if group.ActiveRouteID == oldActive || group.ActiveRouteID != group.Routes[1].ID || group.InputPriceMicros != 123456 {
		t.Fatal("editing recreated routes without remapping manual selection")
	}
	if response := call(h, "PATCH", path, jsonString(object{"route_id": oldActive}), cookie); response.Code != 404 {
		t.Fatal("stale route selection accepted")
	}
	if response := call(h, "POST", "/v1/chat/completions", `{"model":"manual-model","stream":true,"stream_options":{"include_usage":true},"messages":[{"role":"user","content":"hi"}]}`, key.Token); response.Code != 200 || !strings.Contains(response.Body.String(), "[DONE]") {
		t.Fatal("manual streaming failed")
	}
	if calls[1].Load() != 5 {
		t.Fatal("edited model lost its selected upstream")
	}
	for index := range group.Routes {
		group.Routes[index].ID = 0
	}
	response = call(h, "PUT", fmt.Sprintf("/api/groups/%d", group.ID), jsonString(group), cookie)
	if response.Code != 200 || json.Unmarshal(response.Body.Bytes(), &group) != nil || group.ActiveRouteID != group.Routes[1].ID {
		t.Fatal("selection not preserved by channel/model when caller omitted route IDs")
	}
}

func TestManualRouteUnavailableRequiresExplicitSwitch(t *testing.T) {
	for _, reason := range []string{"failure", "disabled", "empty_balance", "deleted"} {
		t.Run(reason, func(t *testing.T) {
			a := testApp(t)
			h := a.Router()
			cookie := loginForTest(t, h)
			var primaryCalls, backupCalls atomic.Int32
			primary := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { primaryCalls.Add(1); w.WriteHeader(503) }))
			defer primary.Close()
			backup := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { backupCalls.Add(1); chatResponse(w) }))
			defer backup.Close()
			one := createChannel(t, h, cookie, "primary", primary.URL, "openai")
			two := createChannel(t, h, cookie, "backup", backup.URL, "openai")
			group := manualGroupForTest(t, h, cookie, one, two)
			response := call(h, "POST", "/api/keys", `{"name":"manual client"}`, cookie)
			var key struct{ Token string }
			json.Unmarshal(response.Body.Bytes(), &key)
			switch reason {
			case "disabled":
				a.DB.Model(&one).Update("enabled", false)
			case "empty_balance":
				now, balance := time.Now(), 0.0
				account := Account{Name: "empty", Enabled: true, Balance: &balance, BlockEmpty: true, Status: "ok", CheckedAt: &now, IntervalSeconds: 300}
				if a.DB.Create(&account).Error != nil || a.DB.Model(&one).Update("account_id", account.ID).Error != nil {
					t.Fatal("could not seed empty account")
				}
			case "deleted":
				if err := a.DB.Exec("CREATE TRIGGER reject_selected_delete BEFORE DELETE ON channels BEGIN SELECT RAISE(ABORT, 'test failure'); END").Error; err != nil {
					t.Fatal(err)
				}
				if response := call(h, "DELETE", fmt.Sprintf("/api/channels/%d?detach_routes=true", one.ID), "", cookie); response.Code != 500 {
					t.Fatal("delete failure hidden")
				}
				var preserved Group
				if a.DB.First(&preserved, group.ID).Error != nil || preserved.ActiveRouteID != group.ActiveRouteID {
					t.Fatal("failed deletion lost manual selection")
				}
				if a.DB.Exec("DROP TRIGGER reject_selected_delete").Error != nil {
					t.Fatal("could not remove trigger")
				}
				response = call(h, "DELETE", fmt.Sprintf("/api/channels/%d?detach_routes=true", one.ID), "", cookie)
				if response.Code != 204 {
					t.Fatal(response.Body.String())
				}
				var updated Group
				if a.DB.First(&updated, group.ID).Error != nil || updated.ActiveRouteID != 0 {
					t.Fatal("deleted route remained selected")
				}
			}
			request := `{"model":"manual-model","messages":[{"role":"user","content":"hi"}]}`
			for range 2 {
				if response := call(h, "POST", "/v1/chat/completions", request, key.Token); response.Code < 400 {
					t.Fatal("unavailable manual route reported success")
				}
			}
			if backupCalls.Load() != 0 {
				t.Fatal("manual mode unexpectedly failed over")
			}
			if reason == "failure" && primaryCalls.Load() != 1 {
				t.Fatal("manual channel cooling ignored")
			}
			response = call(h, "PATCH", fmt.Sprintf("/api/groups/%d/selection", group.ID), jsonString(object{"route_id": group.Routes[1].ID}), cookie)
			if response.Code != 200 {
				t.Fatal(response.Body.String())
			}
			response = call(h, "POST", "/v1/chat/completions", request, key.Token)
			if response.Code != 200 || backupCalls.Load() != 1 {
				t.Fatal("explicit manual recovery failed", response.Body.String())
			}
		})
	}
}

func TestManualRouteValidationAndLegacyStrategies(t *testing.T) {
	a := testApp(t)
	h := a.Router()
	cookie := loginForTest(t, h)
	channel := createChannel(t, h, cookie, "channel", "http://127.0.0.1:1", "openai")
	group := manualGroupForTest(t, h, cookie, channel)
	path := fmt.Sprintf("/api/groups/%d/selection", group.ID)
	if response := call(h, "PATCH", path, jsonString(object{"route_id": group.ActiveRouteID}), ""); response.Code != 401 {
		t.Fatal("manual selection exposed without admin authentication")
	}
	req := httptest.NewRequest("PATCH", path, strings.NewReader(jsonString(object{"route_id": group.ActiveRouteID})))
	req.Header.Set("Cookie", cookie)
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != 403 {
		t.Fatal("selection changed without CSRF header")
	}
	for _, body := range []string{`{}`, `{"route_id":0}`, `{"route_id":-1}`, `{"route_id":"1"}`} {
		if response := call(h, "PATCH", path, body, cookie); response.Code != 400 {
			t.Fatal("invalid route ID accepted")
		}
	}
	for _, index := range []int{-1, 1} {
		response := call(h, "PUT", fmt.Sprintf("/api/groups/%d", group.ID), jsonString(object{"name": group.Name, "strategy": "manual", "routes": group.Routes, "active_route_index": index}), cookie)
		if response.Code != 400 {
			t.Fatal("invalid route index accepted")
		}
	}
	for _, strategy := range []string{"weighted", "priority"} {
		other := Group{Name: strategy, Strategy: strategy, Routes: []Route{{ChannelID: channel.ID, Model: "model", Weight: 1}}}
		response := call(h, "POST", "/api/groups", jsonString(other), cookie)
		if response.Code != 200 || json.Unmarshal(response.Body.Bytes(), &other) != nil {
			t.Fatal(response.Body.String())
		}
		response = call(h, "PATCH", path, jsonString(object{"route_id": other.Routes[0].ID}), cookie)
		if response.Code != 404 {
			t.Fatal("route from another model accepted")
		}
		if response := call(h, "PATCH", fmt.Sprintf("/api/groups/%d/selection", other.ID), jsonString(object{"route_id": other.Routes[0].ID}), cookie); response.Code != 409 {
			t.Fatal("manual selection changed an automatic model")
		}
		other.Strategy = ""
		response = call(h, "PUT", fmt.Sprintf("/api/groups/%d", other.ID), jsonString(other), cookie)
		if response.Code != 200 || json.Unmarshal(response.Body.Bytes(), &other) != nil || other.Strategy != strategy {
			t.Fatal("existing strategy changed by omitted field")
		}
	}
	var saved Group
	if a.DB.First(&saved, group.ID).Error != nil || saved.ActiveRouteID != group.ActiveRouteID {
		t.Fatal("invalid request changed active route")
	}
}

func TestGeneratedManualSelectionPersistsAcrossRegeneration(t *testing.T) {
	a := testApp(t)
	h := a.Router()
	cookie := loginForTest(t, h)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, `{"data":[{"id":"generated"}]}`) }))
	defer upstream.Close()
	one := createChannel(t, h, cookie, "first", upstream.URL, "openai")
	two := createChannel(t, h, cookie, "second", upstream.URL, "openai")
	generateModelsForTest(t, h, cookie, one.ID, two.ID)
	var group Group
	if a.DB.Preload("Routes", func(tx *gorm.DB) *gorm.DB { return tx.Order("id") }).First(&group).Error != nil || group.Strategy != "manual" || len(group.Routes) != 2 || group.ActiveRouteID != group.Routes[0].ID {
		t.Fatal("generated model does not default to manual selection")
	}
	if response := call(h, "PATCH", fmt.Sprintf("/api/groups/%d/selection", group.ID), jsonString(object{"route_id": group.Routes[1].ID}), cookie); response.Code != 200 {
		t.Fatal(response.Body.String())
	}
	generateModelsForTest(t, h, cookie, one.ID, two.ID)
	var regenerated Group
	if a.DB.First(&regenerated, group.ID).Error != nil || regenerated.ActiveRouteID != group.Routes[1].ID || regenerated.Strategy != "manual" {
		t.Fatal("regeneration overwrote manual selection")
	}
}

func TestManualSelectionMigrationAndRestart(t *testing.T) {
	dir := t.TempDir()
	config := Config{DataDir: dir, AdminPassword: "test-password-12345"}
	a, err := Open(config)
	if err != nil {
		t.Fatal(err)
	}
	h := a.Router()
	cookie := loginForTest(t, h)
	channel := createChannel(t, h, cookie, "channel", "http://127.0.0.1:1", "openai")
	response := call(h, "POST", "/api/groups", jsonString(Group{Name: "legacy", Strategy: "weighted", Routes: []Route{{ChannelID: channel.ID, Model: "model", Weight: 1}}}), cookie)
	if response.Code != 200 {
		a.Close()
		t.Fatal(response.Body.String())
	}
	if a.DB.Exec("ALTER TABLE groups DROP COLUMN active_route_id").Error != nil {
		a.Close()
		t.Fatal("could not prepare old schema")
	}
	if err := a.Close(); err != nil {
		t.Fatal(err)
	}
	a, err = Open(config)
	if err != nil {
		t.Fatal(err)
	}
	h = a.Router()
	cookie = loginForTest(t, h)
	var legacy Group
	if a.DB.First(&legacy).Error != nil || legacy.Strategy != "weighted" || legacy.ActiveRouteID != 0 {
		a.Close()
		t.Fatal("migration changed existing scheduling")
	}
	group := manualGroupForTest(t, h, cookie, channel)
	if err := a.Close(); err != nil {
		t.Fatal(err)
	}
	a, err = Open(config)
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	var saved Group
	if a.DB.Preload("Routes").First(&saved, group.ID).Error != nil || saved.ActiveRouteID != group.ActiveRouteID || saved.Strategy != "manual" {
		t.Fatal("manual route lost after restart")
	}
}
