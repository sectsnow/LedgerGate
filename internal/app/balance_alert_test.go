package app

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestBalanceAlertThreshold(t *testing.T) {
	for _, kind := range []string{"newapi", "sub2api"} {
		for _, test := range []struct {
			name      string
			threshold float64
			balance   float64
			wantLow   bool
		}{
			{name: "disabled_positive_balance", balance: 1},
			{name: "disabled_zero_balance"},
			{name: "disabled_negative_balance", balance: -1},
			{name: "enabled_zero_balance", threshold: 5, wantLow: true},
			{name: "enabled_negative_balance", threshold: 5, balance: -1, wantLow: true},
			{name: "below_threshold", threshold: 5, balance: 4.99, wantLow: true},
			{name: "equal_threshold", threshold: 5, balance: 5},
			{name: "above_threshold", threshold: 5, balance: 6},
			{name: "decimal_threshold", threshold: 0.1, balance: 0.05, wantLow: true},
		} {
			t.Run(kind+"/"+test.name, func(t *testing.T) {
				a := testApp(t)
				upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					w.Header().Set("Content-Type", "application/json")
					switch r.URL.Path {
					case "/api/user/self":
						fmt.Fprintf(w, `{"success":true,"data":{"quota":%g,"used_quota":0}}`, test.balance*500000)
					case "/api/v1/auth/me":
						fmt.Fprintf(w, `{"data":{"balance":%g}}`, test.balance)
					case "/api/log/self/stat":
						fmt.Fprint(w, `{"success":true,"data":{"quota":0}}`)
					case "/api/v1/usage/dashboard/stats":
						fmt.Fprint(w, `{"data":{"total_actual_cost":0,"today_actual_cost":0}}`)
					case "/api/user/self/groups":
						fmt.Fprint(w, `{"success":true,"data":{"default":{"ratio":1}}}`)
					case "/api/v1/groups/rates":
						fmt.Fprint(w, `{"data":{}}`)
					default:
						fmt.Fprint(w, `{"success":true,"data":[]}`)
					}
				}))
				defer upstream.Close()
				h := a.Router()
				cookie := loginForTest(t, h)
				input := object{"name": "threshold test", "kind": kind, "base_url": upstream.URL, "enabled": true, "auth_mode": "token", "token": "test-account-token", "currency": "USD", "quota_per_unit": 500000, "interval_seconds": 300, "block_empty": true}
				if test.threshold > 0 {
					input["low_balance"] = test.threshold
				}
				response := call(h, "POST", "/api/accounts", jsonString(input), cookie)
				var saved Account
				if response.Code != 200 || json.Unmarshal(response.Body.Bytes(), &saved) != nil || saved.LowBalance != test.threshold {
					t.Fatal("threshold default or saving failed")
				}
				account, err := a.checkAccount(context.Background(), saved.ID)
				if err != nil {
					t.Fatal(err)
				}
				wantStatus, wantAlerts := "ok", int64(0)
				if test.wantLow {
					wantStatus, wantAlerts = "low", 1
				}
				var alerts int64
				if err := a.DB.Model(&Alert{}).Where("account_id = ? AND kind = ?", account.ID, "low_balance").Count(&alerts).Error; err != nil {
					t.Fatal(err)
				}
				if account.Status != wantStatus || alerts != wantAlerts {
					t.Fatalf("status=%s alerts=%d, want status=%s alerts=%d", account.Status, alerts, wantStatus, wantAlerts)
				}
				channel := Channel{Name: "linked", BaseURL: upstream.URL, AccountID: &account.ID, Enabled: true}
				if err := a.DB.Create(&channel).Error; err != nil {
					t.Fatal(err)
				}
				candidates := a.candidates(Group{Routes: []Route{{ChannelID: channel.ID}}})
				if (len(candidates) == 0) != (test.balance <= 0) {
					t.Fatal("balance alert threshold changed the empty-balance forwarding gate")
				}
			})
		}
	}
}
