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
	"time"
)

type batchResponse struct {
	Results   []channelBatchItem `json:"results"`
	Succeeded int                `json:"succeeded"`
	Failed    int                `json:"failed"`
}

func runChannelBatch(t *testing.T, handler http.Handler, cookie string, input channelBatchInput) batchResponse {
	t.Helper()
	response := call(handler, "POST", "/api/channels/batch", jsonString(input), cookie)
	var result batchResponse
	if response.Code != http.StatusOK || json.Unmarshal(response.Body.Bytes(), &result) != nil {
		t.Fatalf("batch failed %d %s", response.Code, response.Body.String())
	}
	if len(result.Results) != len(input.IDs) || result.Succeeded+result.Failed != len(input.IDs) {
		t.Fatalf("incomplete results %s", response.Body.String())
	}
	for index, item := range result.Results {
		if item.ID != input.IDs[index] {
			t.Fatal("result order differs from requested channel order")
		}
		if item.Success == (item.Error != "") {
			t.Fatal("ambiguous item result")
		}
	}
	if strings.Contains(response.Body.String(), "private-upstream-key") {
		t.Fatal("batch response exposed upstream credentials")
	}
	return result
}

func TestChannelBatchEnableDisableAndProtocols(t *testing.T) {
	a := testApp(t)
	h := a.Router()
	cookie := loginForTest(t, h)
	channels := []Channel{
		createChannel(t, h, cookie, "first", "https://first.invalid", "openai"),
		createChannel(t, h, cookie, "second", "https://second.invalid", "responses"),
		createChannel(t, h, cookie, "untouched", "https://third.invalid", "anthropic"),
	}
	var stored []Channel
	if a.DB.Order("id").Find(&stored).Error != nil {
		t.Fatal("could not load credentials")
	}
	createGroup(t, h, cookie, channels, "weighted")
	a.health[channels[0].ID] = channelState{Until: time.Now().Add(time.Minute), Error: "previous failure"}
	result := runChannelBatch(t, h, cookie, channelBatchInput{IDs: []uint{channels[0].ID, channels[1].ID, 999}, Action: "disable"})
	if result.Succeeded != 2 || result.Failed != 1 || result.Results[2].Error != "渠道不存在" {
		t.Fatalf("incorrect partial result %+v", result)
	}
	if _, exists := a.health[channels[0].ID]; exists {
		t.Fatal("runtime state not cleared for updated channel")
	}
	var after []Channel
	a.DB.Order("id").Find(&after)
	for index := range after {
		if after[index].Enabled != (index == 2) || after[index].APIKey != stored[index].APIKey || after[index].BaseURL != stored[index].BaseURL || after[index].Name != stored[index].Name {
			t.Fatal("batch modified unexpected fields")
		}
	}
	for range 2 {
		result = runChannelBatch(t, h, cookie, channelBatchInput{IDs: []uint{channels[0].ID, channels[1].ID}, Action: "enable"})
		if result.Succeeded != 2 {
			t.Fatal("repeated enable failed")
		}
	}
	protocols := []string{"anthropic", "responses"}
	result = runChannelBatch(t, h, cookie, channelBatchInput{IDs: []uint{channels[0].ID, channels[1].ID}, Action: "protocols", Protocols: protocols})
	if result.Succeeded != 2 {
		t.Fatal("batch protocol update failed")
	}
	after = nil
	a.DB.Order("id").Find(&after)
	for index, channel := range after {
		if !channel.Enabled || channel.APIKey != stored[index].APIKey || channel.TimeoutSeconds != stored[index].TimeoutSeconds || channel.CooldownSeconds != stored[index].CooldownSeconds {
			t.Fatal("protocol update changed credentials or other settings")
		}
		if index < 2 && (channel.Protocol != "anthropic" || !reflect.DeepEqual(channel.Protocols, protocols)) {
			t.Fatal("selected protocols not persisted")
		}
		if index == 2 && channel.Protocol != stored[index].Protocol {
			t.Fatal("unselected channel changed")
		}
	}
	var routeCount int64
	a.DB.Model(&Route{}).Count(&routeCount)
	if routeCount != 3 {
		t.Fatal("channel updates changed route definitions")
	}
}

func TestChannelBatchAuthorizationAndValidation(t *testing.T) {
	a := testApp(t)
	h := a.Router()
	if response := call(h, "POST", "/api/channels/batch", `{"ids":[1],"action":"disable"}`, ""); response.Code != 401 {
		t.Fatal("batch accepted without admin session")
	}
	cookie := loginForTest(t, h)
	channel := createChannel(t, h, cookie, "channel", "https://example.invalid", "openai")
	for _, body := range []string{
		`{}`, `{"ids":[],"action":"disable"}`, `{"ids":[0],"action":"disable"}`,
		`{"ids":[-1],"action":"disable"}`, `{"ids":[1,1],"action":"disable"}`, `{"ids":[1.5],"action":"disable"}`,
		`{"ids":[1],"action":"unknown"}`, `{"ids":[1],"action":"protocols","protocols":[]}`,
		`{"ids":[1],"action":"protocols","protocols":["openai","openai"]}`, `{"ids":[1],"action":"protocols","protocols":["unknown"]}`,
		`{"ids":[1],"action":"delete","detach_routes":"true"}`,
	} {
		if response := call(h, "POST", "/api/channels/batch", body, cookie); response.Code != 400 {
			t.Fatalf("invalid input accepted %s %d", body, response.Code)
		}
	}
	ids := make([]uint, 101)
	for index := range ids {
		ids[index] = uint(index + 1)
	}
	if response := call(h, "POST", "/api/channels/batch", jsonString(channelBatchInput{IDs: ids, Action: "disable"}), cookie); response.Code != 400 {
		t.Fatal("batch limit ignored")
	}
	req := httptest.NewRequest("POST", "/api/channels/batch", strings.NewReader(`{"ids":[1],"action":"disable"}`))
	req.Header.Set("Cookie", cookie)
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != 403 {
		t.Fatal("batch mutation accepted without CSRF check")
	}
	var saved Channel
	if a.DB.First(&saved, channel.ID).Error != nil || !saved.Enabled {
		t.Fatal("invalid operation changed a channel")
	}
}

func TestChannelBatchDeleteRouteConfirmationAndPartialRollback(t *testing.T) {
	a := testApp(t)
	h := a.Router()
	cookie := loginForTest(t, h)
	target := createChannel(t, h, cookie, "target", "https://first.invalid", "openai")
	kept := createChannel(t, h, cookie, "kept", "https://second.invalid", "openai")
	unused := createChannel(t, h, cookie, "unused", "https://third.invalid", "openai")
	createGroup(t, h, cookie, []Channel{target, kept}, "priority")
	result := runChannelBatch(t, h, cookie, channelBatchInput{IDs: []uint{target.ID, unused.ID}, Action: "delete"})
	if result.Succeeded != 1 || result.Failed != 1 || !strings.Contains(result.Results[0].Error, "路由引用") {
		t.Fatal("referenced channel deleted without confirmation")
	}
	var routes int64
	a.DB.Model(&Route{}).Where("channel_id = ?", target.ID).Count(&routes)
	if routes != 1 {
		t.Fatal("protected channel lost its routes")
	}
	if err := a.DB.Exec(fmt.Sprintf(`CREATE TRIGGER prevent_batch_channel_delete BEFORE DELETE ON channels WHEN OLD.id = %d BEGIN SELECT RAISE(ABORT, 'test failure'); END`, target.ID)).Error; err != nil {
		t.Fatal(err)
	}
	result = runChannelBatch(t, h, cookie, channelBatchInput{IDs: []uint{target.ID, kept.ID}, Action: "delete", DetachRoutes: true})
	if result.Succeeded != 1 || result.Failed != 1 || result.Results[0].Success || !result.Results[1].Success {
		t.Fatal("partial deletion result incorrect")
	}
	a.DB.Model(&Route{}).Where("channel_id = ?", target.ID).Count(&routes)
	if routes != 1 {
		t.Fatal("failed delete did not roll back route removal")
	}
	var targetCount, keptCount int64
	a.DB.Model(&Channel{}).Where("id = ?", target.ID).Count(&targetCount)
	a.DB.Model(&Channel{}).Where("id = ?", kept.ID).Count(&keptCount)
	if targetCount != 1 || keptCount != 0 {
		t.Fatal("partial results differ from persisted channels")
	}
	if a.DB.Exec("DROP TRIGGER prevent_batch_channel_delete").Error != nil {
		t.Fatal("could not remove test trigger")
	}
	result = runChannelBatch(t, h, cookie, channelBatchInput{IDs: []uint{target.ID}, Action: "delete", DetachRoutes: true})
	if result.Succeeded != 1 {
		t.Fatal("failed item could not be retried")
	}
	var group Group
	if a.DB.Preload("Routes").First(&group).Error != nil || len(group.Routes) != 0 {
		t.Fatal("empty model group lost or dangling routes remain")
	}
}

func TestChannelBatchModelSyncPartialResults(t *testing.T) {
	a := testApp(t)
	h := a.Router()
	cookie := loginForTest(t, h)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if strings.HasPrefix(r.URL.Path, "/failed/") {
			w.WriteHeader(503)
			return
		}
		if r.Header.Get("x-api-key") != "" {
			fmt.Fprint(w, `{"data":[{"id":"shared"},{"id":"claude"}]}`)
		} else {
			fmt.Fprint(w, `{"data":[{"id":"shared"},{"id":"chat"}]}`)
		}
	}))
	defer server.Close()
	good := createMultiProtocolChannel(t, h, cookie, server.URL, []string{"openai", "responses", "anthropic"})
	bad := createChannel(t, h, cookie, "failed", server.URL+"/failed", "openai")
	for _, channel := range []Channel{good, bad} {
		if a.DB.Model(&channel).Update("models", `["old-model"]`).Error != nil {
			t.Fatal("could not seed model list")
		}
	}
	result := runChannelBatch(t, h, cookie, channelBatchInput{IDs: []uint{good.ID, bad.ID, 999}, Action: "sync_models"})
	if result.Succeeded != 1 || result.Failed != 2 || !result.Results[0].Success || result.Results[1].Success || !strings.Contains(result.Results[1].Error, "503") {
		t.Fatalf("model sync hid a failure %+v", result)
	}
	var updated, preserved Channel
	a.DB.First(&updated, good.ID)
	a.DB.First(&preserved, bad.ID)
	if updated.Models != `["shared","chat","claude"]` || preserved.Models != `["old-model"]` {
		t.Fatal("model merge or failed-sync preservation regressed")
	}
}

func TestChannelBatchModelSyncBoundsConcurrency(t *testing.T) {
	a := testApp(t)
	h := a.Router()
	cookie := loginForTest(t, h)
	entered, release := make(chan struct{}, 10), make(chan struct{})
	var once sync.Once
	unblock := func() { once.Do(func() { close(release) }) }
	var active, peak atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		current := active.Add(1)
		defer active.Add(-1)
		for old := peak.Load(); current > old; old = peak.Load() {
			if peak.CompareAndSwap(old, current) {
				break
			}
		}
		entered <- struct{}{}
		select {
		case <-release:
		case <-r.Context().Done():
			return
		}
		fmt.Fprint(w, `{"data":[{"id":"model"}]}`)
	}))
	defer server.Close()
	defer unblock()
	ids := []uint{}
	for index := range 6 {
		channel := createChannel(t, h, cookie, fmt.Sprintf("channel-%d", index), server.URL, "openai")
		ids = append(ids, channel.ID)
	}
	done := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		done <- call(h, "POST", "/api/channels/batch", jsonString(channelBatchInput{IDs: ids, Action: "sync_models"}), cookie)
	}()
	for range 4 {
		select {
		case <-entered:
		case <-time.After(3 * time.Second):
			t.Fatal("parallel model requests did not start")
		}
	}
	select {
	case <-entered:
		t.Fatal("more than four upstream requests started concurrently")
	default:
	}
	unblock()
	select {
	case response := <-done:
		var result batchResponse
		if response.Code != 200 || json.Unmarshal(response.Body.Bytes(), &result) != nil || result.Succeeded != 6 {
			t.Fatal(response.Body.String())
		}
	case <-time.After(3 * time.Second):
		t.Fatal("batch did not finish")
	}
	if peak.Load() != 4 {
		t.Fatal("upstream concurrency exceeded its bound")
	}
}

func TestModelSyncDoesNotOverwriteNewChannelConfiguration(t *testing.T) {
	for _, change := range []string{"protocols", "delete"} {
		t.Run(change, func(t *testing.T) {
			a := testApp(t)
			h := a.Router()
			cookie := loginForTest(t, h)
			entered, release := make(chan struct{}), make(chan struct{})
			var once sync.Once
			unblock := func() { once.Do(func() { close(release) }) }
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				close(entered)
				select {
				case <-release:
				case <-r.Context().Done():
					return
				}
				fmt.Fprint(w, `{"data":[{"id":"stale-model"}]}`)
			}))
			defer server.Close()
			defer unblock()
			channel := createChannel(t, h, cookie, "channel", server.URL, "openai")
			a.DB.Model(&channel).Update("models", `["old-model"]`)
			done := make(chan *httptest.ResponseRecorder, 1)
			go func() {
				done <- call(h, "POST", "/api/channels/batch", jsonString(channelBatchInput{IDs: []uint{channel.ID}, Action: "sync_models"}), cookie)
			}()
			select {
			case <-entered:
			case <-time.After(3 * time.Second):
				t.Fatal("model request did not start")
			}
			result := runChannelBatch(t, h, cookie, channelBatchInput{IDs: []uint{channel.ID}, Action: change, Protocols: []string{"anthropic"}})
			if result.Succeeded != 1 {
				t.Fatal("concurrent mutation failed")
			}
			unblock()
			select {
			case response := <-done:
				if response.Code != 200 || json.Unmarshal(response.Body.Bytes(), &result) != nil || result.Failed != 1 {
					t.Fatal("stale sync succeeded", response.Body.String())
				}
			case <-time.After(3 * time.Second):
				t.Fatal("sync did not finish")
			}
			var stored Channel
			err := a.DB.First(&stored, channel.ID).Error
			if change == "delete" && err == nil {
				t.Fatal("deleted channel resurrected")
			}
			if change == "protocols" && (err != nil || stored.Protocol != "anthropic" || stored.Models != `["old-model"]`) {
				t.Fatal("stale sync overwrote updated channel")
			}
		})
	}
}
