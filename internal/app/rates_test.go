package app

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

func TestAccountRatesMatchKeysAndRespectOverrides(t *testing.T) {
	for _, kind := range []string{"sub2api", "newapi"} {
		t.Run(kind, func(t *testing.T) {
			a := testApp(t)
			var emptyOverrides atomic.Bool
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Header.Get("Authorization") != "Bearer account-token" {
					t.Error("incorrect account authentication")
				}
				switch r.URL.Path {
				case "/api/v1/groups/available":
					fmt.Fprint(w, `{"data":[{"id":7,"name":"基础组","rate_multiplier":0.2},{"id":8,"name":"优惠组","rate_multiplier":0.4},{"id":10,"name":"尚未使用的组","rate_multiplier":0.7}]}`)
				case "/api/v1/groups/rates":
					if emptyOverrides.Load() {
						fmt.Fprint(w, `{"data":{}}`)
					} else {
						fmt.Fprint(w, `{"data":{"7":0,"8":0.15}}`)
					}
				case "/api/user/self/groups":
					fmt.Fprint(w, `{"data":{"basic":{"base_ratio":0.2,"ratio":0},"pro":{"base_ratio":0.4,"ratio":0.15},"unused":{"ratio":0.7}}}`)
				case "/api/v1/keys":
					fmt.Fprint(w, `{"data":{"total":3,"items":[{"id":101,"key":"sk-first-secret","group_id":7},{"id":102,"key":"sk-second-secret","group_id":8},{"id":103,"key":"sk-third-secret","group_id":9,"group":null}]}}`)
				case "/api/token/":
					fmt.Fprint(w, `{"data":{"total":3,"items":[{"id":101,"key":"sk-***","group":"basic"},{"id":102,"key":"sk-***","group":"pro"},{"id":103,"key":"sk-***","group":"gone"}]}}`)
				default:
					w.WriteHeader(404)
				}
			}))
			defer server.Close()
			account := Account{Name: kind, Kind: kind, BaseURL: server.URL, Token: a.seal("account-token")}
			if err := a.DB.Create(&account).Error; err != nil {
				t.Fatal(err)
			}
			first, second, third := int64(101), int64(102), int64(103)
			channels := []Channel{
				{Name: "renamed 99x", UpstreamKeyID: &first, APIKey: a.seal("sk-first-secret")},
				{Name: "imported", UpstreamKeyID: &second, APIKey: a.seal("sk-second-secret")},
				{Name: "manual duplicate", APIKey: a.seal("sk-first-secret")},
				{Name: "removed upstream group", UpstreamKeyID: &third, APIKey: a.seal("sk-third-secret")},
				{Name: "renamed 99x", APIKey: a.seal("sk-unrelated-secret")},
				{Name: "different upstream", APIKey: a.seal("sk-first-secret")},
				{Name: "masked secret is not an identity", APIKey: a.seal("sk-***")},
			}
			if kind == "newapi" {
				channels[0].APIKey = a.seal("first-secret")
			}
			for i := range channels {
				channels[i].AccountID, channels[i].BaseURL = &account.ID, server.URL
				if i == 5 {
					channels[i].BaseURL += "/other"
				}
				if err := a.DB.Create(&channels[i]).Error; err != nil {
					t.Fatal(err)
				}
			}
			snapshot, err := a.fetchAccountRates(context.Background(), &account)
			if err != nil {
				t.Fatal(err)
			}
			if len(snapshot.Channels) != 4 {
				t.Fatalf("expected four exact matches, got %d", len(snapshot.Channels))
			}
			if len(snapshot.Groups) != 3 {
				t.Fatal("all available groups must be included, independently of imported keys")
			}
			for i, want := range map[int]float64{0: 0, 1: 0.15, 2: 0} {
				rate := snapshot.Channels[channels[i].ID]
				if rate.Multiplier == nil || *rate.Multiplier != want {
					t.Fatalf("channel %d: incorrect actual rate %#v", i, rate.Multiplier)
				}
			}
			if rate := snapshot.Channels[channels[1].ID]; rate.BaseMultiplier == nil || *rate.BaseMultiplier != 0.4 {
				t.Fatal("base rate lost")
			}
			if snapshot.Channels[channels[3].ID].Multiplier != nil {
				t.Fatal("missing upstream rate must remain unknown")
			}
			account.Rates = jsonString(snapshot)
			if strings.Contains(account.Rates, "sk-") || strings.Contains(account.Rates, "account-token") {
				t.Fatal("credential leaked into rate snapshot")
			}
			if err := a.DB.Model(&account).Updates(map[string]any{"rates": account.Rates, "warnings": "倍率采集失败，显示上次可用数据"}).Error; err != nil {
				t.Fatal(err)
			}
			// Editing a manual key must immediately hide the old cached rate.
			if err := a.DB.Model(&channels[2]).Update("api_key", a.seal("sk-replaced-secret")).Error; err != nil {
				t.Fatal(err)
			}
			h := a.Router()
			cookie := loginForTest(t, h)
			response := call(h, "GET", "/api/channels", "", cookie)
			var rows []struct {
				Channel Channel       `json:"channel"`
				Rate    *upstreamRate `json:"rate"`
				Stale   bool          `json:"rate_stale"`
			}
			if response.Code != 200 || json.Unmarshal(response.Body.Bytes(), &rows) != nil {
				t.Fatalf("channel listing failed %d", response.Code)
			}
			for _, row := range rows {
				if row.Channel.ID == channels[0].ID && (row.Rate == nil || !row.Stale || row.Rate.Multiplier == nil || *row.Rate.Multiplier != 0) {
					t.Fatal("actual zero rate or stale indication missing")
				}
				if row.Channel.ID == channels[2].ID && row.Rate != nil {
					t.Fatal("old rate shown after key replacement")
				}
			}
			if kind == "sub2api" {
				emptyOverrides.Store(true)
				baseOnly, err := a.fetchAccountRates(context.Background(), &account)
				if err != nil {
					t.Fatal(err)
				}
				if rate := baseOnly.Channels[channels[0].ID]; rate.Multiplier == nil || *rate.Multiplier != 0.2 {
					t.Fatal("empty custom rates must fall back to upstream base rate")
				}
			}
		})
	}
}

func TestRatesPaginationFailureAndChangeAlerts(t *testing.T) {
	a := testApp(t)
	var failPage, rename, changed atomic.Bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/user/self":
			fmt.Fprint(w, `{"data":{"quota":10000000,"used_quota":0}}`)
		case "/api/log/self/stat":
			fmt.Fprint(w, `{"data":{"quota":0}}`)
		case "/api/notice":
			fmt.Fprint(w, `{"data":""}`)
		case "/api/user/self/groups":
			description, ratio := "original", 0.1
			if rename.Load() {
				description = "new description"
			}
			if changed.Load() {
				ratio = 0.2
			}
			fmt.Fprintf(w, `{"data":{"first":{"ratio":0.05},"second":{"ratio":%g,"desc":%q}}}`, ratio, description)
		case "/api/token/":
			if r.URL.Query().Get("p") == "1" {
				fmt.Fprint(w, `{"data":{"total":2,"items":[{"id":1,"group":"first"}]}}`)
			} else if failPage.Load() {
				w.WriteHeader(500)
			} else {
				fmt.Fprint(w, `{"data":{"total":2,"items":[{"id":2,"group":"second"}]}}`)
			}
		default:
			w.WriteHeader(404)
		}
	}))
	defer server.Close()
	account := Account{Name: "paginated", Kind: "newapi", AuthMode: "token", Token: a.seal("account-token"), BaseURL: server.URL, QuotaPerUnit: 500000, IntervalSeconds: 300, Currency: "USD", Rates: `{"legacy":{"ratio":1}}`}
	if err := a.DB.Create(&account).Error; err != nil {
		t.Fatal(err)
	}
	id := int64(2)
	channel := Channel{AccountID: &account.ID, UpstreamKeyID: &id, BaseURL: server.URL, APIKey: a.seal("sk-on-second-page")}
	if err := a.DB.Create(&channel).Error; err != nil {
		t.Fatal(err)
	}
	checked, err := a.checkAccount(context.Background(), account.ID)
	if err != nil {
		t.Fatal(err)
	}
	rate := readRateSnapshot(checked.Rates).Channels[channel.ID]
	if rate.Multiplier == nil || *rate.Multiplier != 0.1 {
		t.Fatal("key on second page not matched")
	}
	previous := checked.Rates
	failPage.Store(true)
	checked, err = a.checkAccount(context.Background(), account.ID)
	if err != nil || checked.Rates != previous || !strings.Contains(checked.Warnings, "倍率采集失败") {
		t.Fatal("failed page replaced last usable rate snapshot")
	}
	failPage.Store(false)
	rename.Store(true)
	if _, err := a.checkAccount(context.Background(), account.ID); err != nil {
		t.Fatal(err)
	}
	var count int64
	a.DB.Model(&Alert{}).Where("kind = ?", "rates_changed").Count(&count)
	if count != 0 {
		t.Fatal("migration, failed collection, or description change caused a false rate alert")
	}
	changed.Store(true)
	if _, err := a.checkAccount(context.Background(), account.ID); err != nil {
		t.Fatal(err)
	}
	a.DB.Model(&Alert{}).Where("kind = ?", "rates_changed").Count(&count)
	if count != 1 {
		t.Fatal("actual multiplier change did not emit an alert")
	}
}

func TestChannelEditsDetachChangedUpstreamKey(t *testing.T) {
	a := testApp(t)
	h := a.Router()
	cookie := loginForTest(t, h)
	first := Account{Name: "first", Kind: "newapi", BaseURL: "https://first.example"}
	second := Account{Name: "second", Kind: "newapi", BaseURL: "https://second.example"}
	a.DB.Create(&first)
	a.DB.Create(&second)
	for index, change := range []string{"name", "equivalent_key", "api_key", "account", "base_url"} {
		t.Run(change, func(t *testing.T) {
			id := int64(101 + index)
			channel := Channel{Name: "original", AccountID: &first.ID, UpstreamKeyID: &id, BaseURL: first.BaseURL, APIKey: a.seal("sk-original"), Protocol: "openai", Enabled: true, TimeoutSeconds: 120, CooldownSeconds: 60}
			if err := a.DB.Create(&channel).Error; err != nil {
				t.Fatal(err)
			}
			body := object{"name": "new name", "account_id": first.ID, "base_url": first.BaseURL, "protocol": "openai", "enabled": true, "timeout_seconds": 120, "cooldown_seconds": 60}
			switch change {
			case "equivalent_key":
				body["api_key"] = "original"
			case "api_key":
				body["api_key"] = "sk-replacement"
			case "account":
				body["account_id"] = second.ID
			case "base_url":
				body["base_url"], body["api_key"] = "https://other.example", "sk-replacement"
			}
			response := call(h, "PUT", fmt.Sprintf("/api/channels/%d", channel.ID), jsonString(body), cookie)
			if response.Code != 200 {
				t.Fatalf("edit failed %d %s", response.Code, response.Body.String())
			}
			var saved Channel
			if err := a.DB.First(&saved, channel.ID).Error; err != nil {
				t.Fatal(err)
			}
			if (saved.UpstreamKeyID != nil) != (change == "name" || change == "equivalent_key") {
				t.Fatal("upstream identity retained or cleared incorrectly")
			}
		})
	}
}
