package app

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestSub2APICumulativeCost(t *testing.T) {
	previous := 42.25
	cases := []struct {
		name          string
		stats         string
		status        int
		previous      *float64
		want          *float64
		previousToday *float64
		previousDate  string
		wantToday     *float64
		warning       bool
	}{
		{name: "historical_actual_cost", stats: `{"code":0,"data":{"total_actual_cost":52.018506675,"total_cost":562.55371663,"today_actual_cost":0.50181156,"today_cost":5.9}}`, status: 200, want: floatPointer(52.018506675), wantToday: floatPointer(0.50181156)},
		{name: "zero_cost", stats: `{"data":{"total_actual_cost":0,"today_actual_cost":0}}`, status: 200, previous: &previous, previousToday: floatPointer(1), want: floatPointer(0), wantToday: floatPointer(0)},
		{name: "failed_collection_preserves_cost", stats: `{}`, status: 503, previous: &previous, want: &previous, previousToday: floatPointer(0.3), wantToday: floatPointer(0.3), warning: true},
		{name: "yesterday_cost_not_reused", stats: `{}`, status: 503, previous: &previous, want: &previous, previousToday: floatPointer(19), previousDate: usageDay(time.Now()).AddDate(0, 0, -1).Format("2006-01-02"), warning: true},
		{name: "missing_cost_stays_unknown", stats: `{"data":{"today_actual_cost":1,"total_cost":562}}`, status: 200, wantToday: floatPointer(1), warning: true},
		{name: "invalid_cost_preserves_previous", stats: `{"data":{"total_actual_cost":"unavailable"}}`, status: 200, previous: &previous, want: &previous, warning: true},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			a := testApp(t)
			h := a.Router()
			cookie := loginForTest(t, h)
			var statsCalls atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Header.Get("Authorization") != "Bearer account-token" {
					t.Error("account credentials not forwarded")
				}
				w.Header().Set("Content-Type", "application/json")
				switch r.URL.Path {
				case "/api/v1/auth/me":
					fmt.Fprint(w, `{"data":{"balance":69.07832404}}`)
				case "/api/v1/usage/dashboard/stats":
					statsCalls.Add(1)
					w.WriteHeader(test.status)
					fmt.Fprint(w, test.stats)
				case "/api/v1/groups/rates":
					fmt.Fprint(w, `{"data":{}}`)
				default:
					fmt.Fprint(w, `{"data":[]}`)
				}
			}))
			defer server.Close()
			response := call(h, "POST", "/api/accounts", jsonString(object{"name": "Sub2API", "kind": "sub2api", "base_url": server.URL, "enabled": true, "auth_mode": "token", "token": "account-token", "currency": "USD", "quota_per_unit": 500000, "low_balance": 1, "interval_seconds": 300}), cookie)
			if response.Code != 200 {
				t.Fatal(response.Body.String())
			}
			if test.previous != nil {
				if err := a.DB.Model(&Account{}).Where("id = ?", 1).Update("used", *test.previous).Error; err != nil {
					t.Fatal(err)
				}
			}
			if test.previousToday != nil {
				date := test.previousDate
				if date == "" {
					date = usageDay(time.Now()).Format("2006-01-02")
				}
				if err := a.DB.Model(&Account{}).Where("id = ?", 1).Updates(map[string]any{"today_used": *test.previousToday, "today_date": date}).Error; err != nil {
					t.Fatal(err)
				}
			}
			response = call(h, "POST", "/api/accounts/1/check", "", cookie)
			if response.Code != 200 {
				t.Fatalf("collection failed: %s", response.Body.String())
			}
			var account Account
			if err := json.Unmarshal(response.Body.Bytes(), &account); err != nil {
				t.Fatal(err)
			}
			if statsCalls.Load() != 1 {
				t.Fatal("missing dedicated cost request")
			}
			if account.Balance == nil || *account.Balance != 69.07832404 || account.Status != "ok" {
				t.Fatalf("balance collection affected: %+v", account)
			}
			if test.want == nil {
				if account.Used != nil {
					t.Fatal("missing cost reported as a value")
				}
			} else if account.Used == nil || *account.Used != *test.want {
				t.Fatalf("cost = %v, want %v", account.Used, *test.want)
			}
			if (account.Warnings != "") != test.warning {
				t.Fatalf("unexpected warning %q", account.Warnings)
			}
			if account.TodayDate != usageDay(time.Now()).Format("2006-01-02") {
				t.Fatalf("wrong consumption date %s", account.TodayDate)
			}
			if test.wantToday == nil {
				if account.TodayUsed != nil {
					t.Fatal("missing daily cost reported as a value")
				}
			} else if account.TodayUsed == nil || *account.TodayUsed != *test.wantToday {
				t.Fatalf("daily cost = %v, want %v", account.TodayUsed, *test.wantToday)
			}
			response = call(h, "GET", "/api/accounts", "", cookie)
			if response.Code != 200 || !strings.Contains(response.Body.String(), `"used":`+jsonString(account.Used)) {
				t.Fatal("account listing lost cumulative cost")
			}
			response = call(h, "GET", "/api/accounts/1/history", "", cookie)
			var history []Snapshot
			if response.Code != 200 || json.Unmarshal(response.Body.Bytes(), &history) != nil || len(history) != 1 {
				t.Fatal("missing cost snapshot")
			}
			if test.want == nil {
				if history[0].Used != nil {
					t.Fatal("unknown historical cost fabricated")
				}
			} else if history[0].Used == nil || *history[0].Used != *test.want {
				t.Fatal("snapshot lost cumulative cost")
			}
			if jsonString(history[0].TodayUsed) != jsonString(account.TodayUsed) || history[0].TodayDate != account.TodayDate {
				t.Fatal("snapshot lost daily cost or date")
			}
		})
	}
}

func floatPointer(value float64) *float64 { return &value }

func TestNewAPIDailyConsumption(t *testing.T) {
	a := testApp(t)
	h := a.Router()
	cookie := loginForTest(t, h)
	var dailyCalls atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer account-token" {
			t.Error("account credentials not forwarded")
		}
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/user/self":
			fmt.Fprint(w, `{"data":{"quota":1000000,"used_quota":500000}}`)
		case "/api/log/self/stat":
			dailyCalls.Add(1)
			if r.URL.Query().Get("type") != "0" || r.URL.Query().Get("start_timestamp") != fmt.Sprint(usageDay(time.Now()).Unix()) {
				t.Error("wrong daily usage window")
			}
			fmt.Fprint(w, `{"data":{"quota":125000}}`)
		case "/api/user/self/groups":
			fmt.Fprint(w, `{"data":{}}`)
		default:
			fmt.Fprint(w, `{"data":[]}`)
		}
	}))
	defer upstream.Close()
	w := call(h, "POST", "/api/accounts", jsonString(object{"name": "NewAPI", "kind": "newapi", "base_url": upstream.URL, "enabled": true, "auth_mode": "token", "token": "account-token", "currency": "USD", "quota_per_unit": 500000, "low_balance": 1, "interval_seconds": 300}), cookie)
	if w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	w = call(h, "POST", "/api/accounts/1/check", "", cookie)
	var account Account
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &account) != nil {
		t.Fatal(w.Body.String())
	}
	if dailyCalls.Load() != 1 || account.TodayUsed == nil || *account.TodayUsed != 0.25 || account.Used == nil || *account.Used != 1 || account.Warnings != "" {
		t.Fatalf("wrong daily consumption %+v", account)
	}
}

func TestConsumptionDayRollover(t *testing.T) {
	before := time.Date(2026, 11, 21, 15, 59, 59, 0, time.UTC)
	after := before.Add(time.Second)
	if usageDay(before).Format("2006-01-02") != "2026-11-21" || usageDay(after).Format("2006-01-02") != "2026-11-22" || usageDay(after).Unix() != after.Unix() {
		t.Fatal("consumption day must switch at midnight in Shanghai")
	}
	a := testApp(t)
	account := Account{Name: "paused account", TodayDate: usageDay(time.Now()).AddDate(0, 0, -1).Format("2006-01-02"), TodayUsed: floatPointer(9)}
	if a.DB.Create(&account).Error != nil {
		t.Fatal("could not create old daily snapshot")
	}
	h := a.Router()
	w := call(h, "GET", "/api/accounts", "", loginForTest(t, h))
	var rows []struct {
		Account Account `json:"account"`
	}
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &rows) != nil || len(rows) != 1 || rows[0].Account.TodayUsed != nil {
		t.Fatal("yesterday's cost displayed as today's cost")
	}
}
