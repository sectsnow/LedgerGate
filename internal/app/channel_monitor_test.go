package app

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"gorm.io/gorm"
)

func TestAutomaticChannelDisableRequiresConfirmedMissingGroup(t *testing.T) {
	for _, kind := range []string{"newapi", "sub2api"} {
		t.Run(kind, func(t *testing.T) {
			a := testApp(t)
			var missing atomic.Bool
			var calls atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				switch r.URL.Path {
				case "/v1/chat/completions":
					calls.Add(1)
					chatResponse(w)
				case "/api/user/self":
					fmt.Fprint(w, `{"data":{"quota":5000000,"used_quota":0}}`)
				case "/api/v1/auth/me":
					fmt.Fprint(w, `{"data":{"balance":10}}`)
				case "/api/v1/usage/dashboard/stats":
					fmt.Fprint(w, `{"data":{"total_actual_cost":0,"today_actual_cost":0}}`)
				case "/api/log/self/stat":
					fmt.Fprint(w, `{"data":{"quota":0}}`)
				case "/api/user/self/groups":
					if missing.Load() {
						fmt.Fprint(w, `{"data":{"active":{"ratio":0.1}}}`)
					} else {
						fmt.Fprint(w, `{"data":{"gone":{"ratio":0.2},"active":{"ratio":0.1}}}`)
					}
				case "/api/v1/groups/available":
					if missing.Load() {
						fmt.Fprint(w, `{"data":[{"id":8,"name":"active","rate_multiplier":0.1}]}`)
					} else {
						fmt.Fprint(w, `{"data":[{"id":7,"name":"gone","rate_multiplier":0.2},{"id":8,"name":"active","rate_multiplier":0.1}]}`)
					}
				case "/api/v1/groups/rates":
					fmt.Fprint(w, `{"data":{}}`)
				case "/api/token/":
					fmt.Fprint(w, `{"data":{"total":2,"items":[{"id":11,"group":"gone","key":"sk-***"},{"id":12,"group":"active","key":"sk-***"}]}}`)
				case "/api/v1/keys":
					fmt.Fprint(w, `{"data":{"total":2,"items":[{"id":11,"group_id":7,"group":null,"key":"sk-gone"},{"id":12,"group_id":8,"key":"sk-active"}]}}`)
				default:
					fmt.Fprint(w, `{"data":[]}`)
				}
			}))
			defer server.Close()
			account := Account{Name: kind, Kind: kind, BaseURL: server.URL, AuthMode: "token", Token: a.seal("account-token"), Enabled: true, QuotaPerUnit: 500000, IntervalSeconds: 300, Revision: 1}
			if err := a.DB.Create(&account).Error; err != nil {
				t.Fatal(err)
			}
			first, second := int64(11), int64(12)
			gone := Channel{Name: "gone channel", AccountID: &account.ID, UpstreamKeyID: &first, BaseURL: server.URL, APIKey: a.seal("sk-gone"), Enabled: true, Protocol: "openai", TimeoutSeconds: 5, CooldownSeconds: 60}
			active := Channel{Name: "active channel", AccountID: &account.ID, UpstreamKeyID: &second, BaseURL: server.URL, APIKey: a.seal("sk-active"), Enabled: true}
			if a.DB.Create(&gone).Error != nil || a.DB.Create(&active).Error != nil {
				t.Fatal("could not create channels")
			}
			h := a.Router()
			cookie := loginForTest(t, h)
			key := createGroup(t, h, cookie, []Channel{gone}, "priority")
			collect := func() {
				t.Helper()
				checked, err := a.checkAccount(context.Background(), account.ID)
				if err != nil || checked.Status != "ok" || checked.Warnings != "" {
					t.Fatalf("collection failed: %v %+v", err, checked)
				}
			}
			collect()
			missing.Store(true)
			collect()
			var stored Channel
			a.DB.First(&stored, gone.ID)
			if !stored.Enabled || stored.AutoDisabledReason != "" {
				t.Fatal("one missing group response disabled channel")
			}
			a.mu.Lock()
			a.health[gone.ID] = channelState{Until: time.Now().Add(time.Hour)}
			a.mu.Unlock()
			collect()
			a.DB.First(&stored, gone.ID)
			if stored.Enabled || !strings.Contains(stored.AutoDisabledReason, "连续两次") || stored.APIKey != gone.APIKey {
				t.Fatal("confirmed missing group did not disable only the channel")
			}
			var kept Channel
			a.DB.First(&kept, active.ID)
			if !kept.Enabled {
				t.Fatal("unrelated active channel disabled")
			}
			a.mu.Lock()
			_, hasHealth := a.health[gone.ID]
			a.mu.Unlock()
			if hasHealth {
				t.Fatal("automatic disable did not clear cooldown")
			}
			collect()
			var alerts int64
			a.DB.Model(&Alert{}).Where("kind = ?", "channel_auto_disabled").Count(&alerts)
			if alerts != 1 {
				t.Fatal("automatic disable alert missing or duplicated")
			}
			request := `{"model":"my-model","messages":[{"role":"user","content":"hello"}]}`
			if response := call(h, "POST", "/v1/chat/completions", request, key); response.Code != 503 || calls.Load() != 0 {
				t.Fatal("automatically disabled channel still forwarded")
			}
			missing.Store(false)
			collect()
			a.DB.First(&stored, gone.ID)
			if stored.Enabled {
				t.Fatal("upstream recovery should not silently re-enable channel")
			}
			result := runChannelBatch(t, h, cookie, channelBatchInput{IDs: []uint{gone.ID}, Action: "enable"})
			if result.Succeeded != 1 {
				t.Fatal("manual recovery failed")
			}
			a.DB.First(&stored, gone.ID)
			if !stored.Enabled || stored.AutoDisabledReason != "" {
				t.Fatal("manual enable did not clear automatic reason")
			}
			if response := call(h, "POST", "/v1/chat/completions", request, key); response.Code != 200 || calls.Load() != 1 {
				t.Fatal("re-enabled channel cannot forward")
			}
			var routes int64
			a.DB.Model(&Route{}).Where("channel_id = ?", gone.ID).Count(&routes)
			if routes != 1 {
				t.Fatal("automatic disable changed routes")
			}
		})
	}
}

func TestAutoDisableDoesNotTreatUnknownDataAsUnavailable(t *testing.T) {
	for _, scenario := range []string{"first_collection", "previous_error", "previous_rates_failure", "group_still_available", "zero_rate", "orphan_with_rate", "auto_group", "empty_group", "unassigned_group", "unmatched_key", "changed_key", "changed_group", "manual_disable"} {
		t.Run(scenario, func(t *testing.T) {
			a := testApp(t)
			account := Account{Name: "test", Kind: "newapi", Status: "ok"}
			if err := a.DB.Create(&account).Error; err != nil {
				t.Fatal(err)
			}
			channel := Channel{AccountID: &account.ID, Enabled: scenario != "manual_disable", BaseURL: "https://example.invalid", APIKey: a.seal("sk-key")}
			if err := a.DB.Create(&channel).Error; err != nil {
				t.Fatal(err)
			}
			rate := channelRate{upstreamRate: upstreamRate{GroupID: "gone", Group: "gone"}, Identity: channelRateIdentity(channel)}
			old := rateSnapshot{Groups: []upstreamRate{}, Channels: map[uint]channelRate{channel.ID: rate}}
			current := rateSnapshot{Groups: []upstreamRate{}, Channels: map[uint]channelRate{channel.ID: rate}}
			switch scenario {
			case "first_collection":
				old.Groups = nil
			case "previous_error":
				account.Status = "error"
			case "previous_rates_failure":
				account.Warnings = "倍率采集失败，显示上次可用数据"
			case "group_still_available":
				current.Groups = []upstreamRate{rate.upstreamRate}
			case "zero_rate":
				rate.Multiplier = floatPointer(0)
				current.Channels[channel.ID] = rate
			case "orphan_with_rate":
				rate.Multiplier = floatPointer(0.2)
				current.Channels[channel.ID] = rate
			case "auto_group":
				rate.GroupID = "auto"
				old.Channels[channel.ID], current.Channels[channel.ID] = rate, rate
			case "empty_group":
				rate.GroupID = ""
				old.Channels[channel.ID], current.Channels[channel.ID] = rate, rate
			case "unassigned_group":
				rate.GroupID = "0"
				old.Channels[channel.ID], current.Channels[channel.ID] = rate, rate
			case "unmatched_key":
				delete(current.Channels, channel.ID)
			case "changed_key":
				rate.Identity = "old-identity"
				old.Channels[channel.ID] = rate
			case "changed_group":
				rate.GroupID = "other"
				old.Channels[channel.ID] = rate
			}
			account.Rates = jsonString(old)
			if err := a.DB.Transaction(func(tx *gorm.DB) error {
				disabled, err := autoDisableUnavailableChannels(tx, account, account, current)
				if len(disabled) != 0 {
					t.Error("unknown or available data disabled a channel")
				}
				return err
			}); err != nil {
				t.Fatal(err)
			}
			var saved Channel
			a.DB.First(&saved, channel.ID)
			if saved.Enabled != channel.Enabled || saved.AutoDisabledReason != "" {
				t.Fatal("manual channel state overwritten")
			}
		})
	}
}

func TestFailedRateCollectionDoesNotAutoDisable(t *testing.T) {
	for _, failure := range []string{"groups", "keys", "partial_keys"} {
		t.Run(failure, func(t *testing.T) {
			a := testApp(t)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/api/user/self":
					fmt.Fprint(w, `{"data":{"quota":5000000,"used_quota":0}}`)
				case "/api/log/self/stat":
					fmt.Fprint(w, `{"data":{"quota":0}}`)
				case "/api/user/self/groups":
					if failure == "groups" {
						w.WriteHeader(503)
					} else {
						fmt.Fprint(w, `{"data":{}}`)
					}
				case "/api/token/":
					if failure == "keys" {
						w.WriteHeader(503)
					} else {
						fmt.Fprint(w, `{"data":{"total":1,"items":[]}}`)
					}
				default:
					fmt.Fprint(w, `{"data":[]}`)
				}
			}))
			defer server.Close()
			account := Account{Name: "test", Kind: "newapi", BaseURL: server.URL, AuthMode: "token", Token: a.seal("account-token"), Status: "ok", QuotaPerUnit: 500000, IntervalSeconds: 300}
			if err := a.DB.Create(&account).Error; err != nil {
				t.Fatal(err)
			}
			id := int64(11)
			channel := Channel{AccountID: &account.ID, UpstreamKeyID: &id, BaseURL: server.URL, Enabled: true, APIKey: a.seal("sk-key")}
			if err := a.DB.Create(&channel).Error; err != nil {
				t.Fatal(err)
			}
			old := rateSnapshot{Groups: []upstreamRate{}, Channels: map[uint]channelRate{channel.ID: {upstreamRate: upstreamRate{GroupID: "gone", Group: "gone"}, Identity: channelRateIdentity(channel)}}}
			if err := a.DB.Model(&account).Update("rates", jsonString(old)).Error; err != nil {
				t.Fatal(err)
			}
			checked, err := a.checkAccount(context.Background(), account.ID)
			if err != nil || !strings.Contains(checked.Warnings, "倍率采集失败") {
				t.Fatal("expected failed fresh collection")
			}
			var saved Channel
			a.DB.First(&saved, channel.ID)
			if !saved.Enabled || saved.AutoDisabledReason != "" {
				t.Fatal("failed collection auto-disabled channel using old snapshot")
			}
		})
	}
}

func TestAutomaticDisableRollsBackWithAlertFailure(t *testing.T) {
	a := testApp(t)
	account := Account{Name: "test", Status: "ok"}
	a.DB.Create(&account)
	channel := Channel{AccountID: &account.ID, Enabled: true, APIKey: a.seal("sk-key")}
	a.DB.Create(&channel)
	snapshot := rateSnapshot{Groups: []upstreamRate{}, Channels: map[uint]channelRate{channel.ID: {upstreamRate: upstreamRate{GroupID: "gone", Group: "gone"}, Identity: channelRateIdentity(channel)}}}
	account.Rates = jsonString(snapshot)
	if err := a.DB.Callback().Create().Before("gorm:create").Register("test:auto_alert_failure", func(tx *gorm.DB) {
		if _, ok := tx.Statement.Dest.(*Alert); ok {
			tx.AddError(errors.New("injected alert storage failure"))
		}
	}); err != nil {
		t.Fatal(err)
	}
	defer a.DB.Callback().Create().Remove("test:auto_alert_failure")
	if err := a.DB.Transaction(func(tx *gorm.DB) error {
		_, err := autoDisableUnavailableChannels(tx, account, account, snapshot)
		return err
	}); err == nil {
		t.Fatal("alert failure ignored")
	}
	var saved Channel
	a.DB.First(&saved, channel.ID)
	if !saved.Enabled || saved.AutoDisabledReason != "" {
		t.Fatal("failed monitor transaction left a partially disabled channel")
	}
}

func TestChannelEditCannotOverwriteConcurrentAutomaticDisable(t *testing.T) {
	a := testApp(t)
	h := a.Router()
	cookie := loginForTest(t, h)
	channel := createChannel(t, h, cookie, "original", "https://example.invalid", "openai")
	var injected atomic.Bool
	if err := a.DB.Callback().Update().Before("gorm:update").Register("test:concurrent_auto_disable", func(tx *gorm.DB) {
		if tx.Statement.Schema != nil && tx.Statement.Schema.Name == "Channel" && !injected.Swap(true) {
			tx.AddError(tx.Exec("UPDATE channels SET enabled = ?, auto_disabled_reason = ? WHERE id = ?", false, "自动检测到分组缺失", channel.ID).Error)
		}
	}); err != nil {
		t.Fatal(err)
	}
	defer a.DB.Callback().Update().Remove("test:concurrent_auto_disable")
	input := object{"name": "edited", "base_url": channel.BaseURL, "protocol": "openai", "enabled": true, "timeout_seconds": 120, "cooldown_seconds": 60}
	response := call(h, "PUT", fmt.Sprintf("/api/channels/%d", channel.ID), jsonString(input), cookie)
	if response.Code != 409 {
		t.Fatalf("concurrent automatic disable was ignored: %d %s", response.Code, response.Body.String())
	}
	var saved Channel
	if err := a.DB.First(&saved, channel.ID).Error; err != nil {
		t.Fatal(err)
	}
	if saved.Enabled || saved.AutoDisabledReason == "" || saved.Name != channel.Name {
		t.Fatal("stale edit reversed automatic disable")
	}
}
