package app

import (
	"encoding/json"
	"math"
	"testing"
	"time"
)

func TestAccountOverviewTotalsAndHistory(t *testing.T) {
	a := testApp(t)
	now := time.Date(2026, 11, 21, 20, 0, 0, 0, usageDay(time.Now()).Location())
	today := usageDay(now)
	yesterday := today.AddDate(0, 0, -1)
	accounts := []Account{
		{Name: "main", Currency: "USD", Enabled: true, Balance: floatPointer(10), Used: floatPointer(100), TodayUsed: floatPointer(3), TodayDate: today.Format("2006-01-02")},
		{Name: "negative-paused", Currency: " usd ", Balance: floatPointer(-2), Used: floatPointer(50), TodayUsed: floatPointer(0), TodayDate: today.Format("2006-01-02")},
		{Name: "other-currency", Currency: "CNY", Balance: floatPointer(50), Used: floatPointer(20), TodayUsed: floatPointer(9), TodayDate: yesterday.Format("2006-01-02")},
		{Name: "unknown", Currency: "USD"},
		{Name: "no-unit", Balance: floatPointer(1)},
	}
	if err := a.DB.Create(&accounts).Error; err != nil {
		t.Fatal(err)
	}
	snapshots := []Snapshot{
		{AccountID: accounts[0].ID, Currency: "USD", Balance: floatPointer(12), TodayUsed: floatPointer(5), TodayDate: yesterday.Format("2006-01-02"), Status: "ok", CreatedAt: yesterday.Add(16 * time.Hour)},
		{AccountID: accounts[0].ID, Currency: "USD", Balance: floatPointer(13), TodayUsed: floatPointer(4), TodayDate: yesterday.Format("2006-01-02"), Status: "ok", CreatedAt: yesterday.Add(15 * time.Hour)},
		{AccountID: accounts[1].ID, Currency: "USD", Balance: floatPointer(-1), TodayUsed: floatPointer(0), TodayDate: yesterday.Format("2006-01-02"), Status: "low", CreatedAt: yesterday.Add(17 * time.Hour)},
		{AccountID: accounts[0].ID, Currency: "USD", Balance: floatPointer(10), TodayUsed: floatPointer(3), TodayDate: today.Format("2006-01-02"), Status: "ok", CreatedAt: now.Add(-time.Minute)},
		{AccountID: accounts[1].ID, Currency: "USD", Balance: floatPointer(-2), TodayUsed: floatPointer(0), TodayDate: today.Format("2006-01-02"), Status: "low", CreatedAt: now.Add(-2 * time.Minute)},
		{AccountID: accounts[0].ID, Currency: "USD", Balance: floatPointer(1000), TodayUsed: floatPointer(999), TodayDate: today.Format("2006-01-02"), Status: "error", CreatedAt: now.Add(-time.Second)},
		{AccountID: accounts[2].ID, Currency: "CNY", Balance: floatPointer(50), TodayUsed: floatPointer(9), TodayDate: yesterday.Format("2006-01-02"), Status: "ok", CreatedAt: now.Add(-time.Minute)},
		{AccountID: accounts[2].ID, Currency: "USD", Balance: floatPointer(999), TodayUsed: floatPointer(999), TodayDate: today.Format("2006-01-02"), Status: "ok", CreatedAt: now.Add(-time.Second)},
		{AccountID: accounts[0].ID, Currency: "USD", Balance: floatPointer(99), Status: "ok", CreatedAt: now.AddDate(0, 0, 1)},
		{AccountID: accounts[0].ID, Currency: "USD", Balance: floatPointer(99), Status: "ok", CreatedAt: now.AddDate(0, 0, -31)},
	}
	if err := a.DB.Create(&snapshots).Error; err != nil {
		t.Fatal(err)
	}
	summaries, err := a.accountOverview(now)
	if err != nil {
		t.Fatal(err)
	}
	if len(summaries) != 3 || summaries[0].Currency != "USD" {
		t.Fatalf("currency grouping failed %+v", summaries)
	}
	groups := map[string]AccountSummary{}
	for _, group := range summaries {
		groups[group.Currency] = group
	}
	usd := groups["USD"]
	if usd.Accounts != 3 || usd.BalanceAccounts != 2 || usd.TodayAccounts != 2 || usd.UsedAccounts != 2 {
		t.Fatalf("unknown amounts counted as collected %+v", usd)
	}
	if usd.Balance == nil || *usd.Balance != 8 || usd.TodayUsed == nil || *usd.TodayUsed != 3 || usd.Used == nil || *usd.Used != 150 {
		t.Fatalf("incorrect totals %+v", usd)
	}
	if usd.Minimum == nil || usd.Minimum.AccountID != accounts[1].ID || usd.Minimum.Name != "negative-paused" || usd.Minimum.Balance != -2 {
		t.Fatal("negative minimum balance missing")
	}
	if len(usd.History) != 30 || usd.History[0].Date != "2026-10-23" || usd.History[29].Date != "2026-11-21" {
		t.Fatal("incorrect thirty-day window")
	}
	for _, point := range usd.History[:28] {
		if point.Balance != nil || point.Consumption != nil {
			t.Fatal("missing historical values fabricated")
		}
	}
	previous, current := usd.History[28], usd.History[29]
	if previous.Balance == nil || *previous.Balance != 11 || previous.Consumption == nil || *previous.Consumption != 5 {
		t.Fatal("daily snapshots counted repeatedly or wrong latest snapshot selected")
	}
	if current.Balance == nil || *current.Balance != 8 || current.Consumption == nil || *current.Consumption != 3 || current.BalanceAccounts != 2 || current.ConsumptionAccounts != 2 {
		t.Fatal("failed or future snapshots affected current day")
	}
	cny := groups["CNY"]
	if cny.Balance == nil || *cny.Balance != 50 || cny.TodayUsed != nil || cny.TodayAccounts != 0 || cny.History[29].Consumption != nil {
		t.Fatal("yesterday's spending or other currency mixed into today's totals")
	}
}

func TestAccountOverviewTimezoneAndZero(t *testing.T) {
	a := testApp(t)
	now := time.Date(2026, 11, 21, 1, 0, 0, 0, time.UTC)
	account := Account{Name: "zero", Currency: "USD", Balance: floatPointer(0), Used: floatPointer(0), TodayUsed: floatPointer(0), TodayDate: "2026-11-21"}
	if err := a.DB.Create(&account).Error; err != nil {
		t.Fatal(err)
	}
	snapshots := []Snapshot{
		{AccountID: account.ID, Currency: "USD", Balance: floatPointer(21), TodayUsed: floatPointer(1), TodayDate: "2026-11-20", Status: "ok", CreatedAt: time.Date(2026, 11, 20, 15, 59, 59, 0, time.UTC)},
		{AccountID: account.ID, Currency: "USD", Balance: floatPointer(22), TodayUsed: floatPointer(2), TodayDate: "2026-11-21", Status: "ok", CreatedAt: time.Date(2026, 11, 20, 16, 0, 0, 0, time.UTC)},
	}
	if err := a.DB.Create(&snapshots).Error; err != nil {
		t.Fatal(err)
	}
	groups, err := a.accountOverview(now)
	if err != nil {
		t.Fatal(err)
	}
	group := groups[0]
	if group.Balance == nil || *group.Balance != 0 || group.TodayUsed == nil || *group.TodayUsed != 0 || group.Used == nil || *group.Used != 0 || group.Minimum == nil {
		t.Fatal("reported zero amounts treated as unknown")
	}
	if group.History[28].Date != "2026-11-20" || group.History[28].Balance == nil || *group.History[28].Balance != 21 || group.History[29].Date != "2026-11-21" || group.History[29].Balance == nil || *group.History[29].Balance != 22 {
		t.Fatal("UTC timestamps grouped into the wrong Shanghai day")
	}
}

func TestOverviewFinancialAPI(t *testing.T) {
	a := testApp(t)
	h := a.Router()
	cookie := loginForTest(t, h)
	response := call(h, "GET", "/api/overview", "", cookie)
	if response.Code != 200 {
		t.Fatal(response.Body.String())
	}
	var empty struct {
		Accounts []AccountSummary `json:"account_summaries"`
		Activity []RequestDay     `json:"activity"`
	}
	if json.Unmarshal(response.Body.Bytes(), &empty) != nil || empty.Accounts == nil || len(empty.Accounts) != 0 || len(empty.Activity) != 365 {
		t.Fatal("empty account summary must be an empty array")
	}
	account := Account{Name: "account", Currency: "USD", Balance: floatPointer(12.34), Used: floatPointer(5.67), TodayUsed: floatPointer(0.89), TodayDate: usageDay(time.Now()).Format("2006-01-02")}
	if err := a.DB.Create(&account).Error; err != nil {
		t.Fatal(err)
	}
	response = call(h, "GET", "/api/overview", "", cookie)
	var result struct {
		Requests int64            `json:"requests"`
		Accounts []AccountSummary `json:"account_summaries"`
	}
	if response.Code != 200 || json.Unmarshal(response.Body.Bytes(), &result) != nil || len(result.Accounts) != 1 {
		t.Fatal(response.Body.String())
	}
	group := result.Accounts[0]
	if len(group.History) != 30 {
		t.Fatal("overview must provide all thirty history days for local switching")
	}
	if group.Balance == nil || math.Abs(*group.Balance-12.34) > 1e-9 || group.TodayUsed == nil || math.Abs(*group.TodayUsed-0.89) > 1e-9 || group.Used == nil || math.Abs(*group.Used-5.67) > 1e-9 || result.Requests != 0 {
		t.Fatal("account money and gateway request statistics are inconsistent")
	}
}

func TestAccountOverviewThirtyDayBounds(t *testing.T) {
	a := testApp(t)
	now := time.Date(2026, 11, 21, 20, 0, 0, 0, usageDay(time.Now()).Location())
	start := usageDay(now).AddDate(0, 0, -29)
	account := Account{Name: "history", Currency: "USD"}
	if err := a.DB.Create(&account).Error; err != nil {
		t.Fatal(err)
	}
	snapshots := []Snapshot{
		{AccountID: account.ID, Currency: "USD", Balance: floatPointer(99), Status: "ok", CreatedAt: start},
		{AccountID: account.ID, Currency: "USD", Balance: floatPointer(999), Status: "ok", CreatedAt: start.Add(-time.Second)},
		{AccountID: account.ID, Currency: "USD", Balance: floatPointer(50), Status: "ok", CreatedAt: now.AddDate(0, 0, -8)},
		{AccountID: account.ID, Currency: "USD", Balance: floatPointer(20), Status: "ok", CreatedAt: now},
	}
	if err := a.DB.Create(&snapshots).Error; err != nil {
		t.Fatal(err)
	}
	groups, err := a.accountOverview(now)
	if err != nil {
		t.Fatal(err)
	}
	for i, point := range groups[0].History {
		want, known := map[int]float64{0: 99, 21: 50, 29: 20}[i]
		if known {
			if point.Balance == nil || *point.Balance != want {
				t.Fatalf("history day %d has wrong retained balance", i)
			}
		} else if point.Balance != nil || point.Consumption != nil {
			t.Fatal("missing history fabricated")
		}
	}
}

func TestRequestActivityCalendarAndTimezone(t *testing.T) {
	a := testApp(t)
	now := time.Date(2026, 11, 21, 20, 0, 0, 0, usageDay(time.Now()).Location())
	today := usageDay(now)
	start := today.AddDate(0, 0, -364)
	empty, err := a.requestActivity(now)
	if err != nil || len(empty) != 365 {
		t.Fatal("empty activity must contain all calendar days")
	}
	for _, day := range empty {
		if day.Requests != 0 {
			t.Fatal("empty activity has fabricated requests")
		}
	}
	logs := []RequestLog{
		{CreatedAt: start.Add(-time.Second)},
		{CreatedAt: start},
		{CreatedAt: today.Add(-time.Second)},
		{CreatedAt: today.In(time.UTC)},
		{CreatedAt: today.Add(time.Minute)},
		{CreatedAt: today.Add(10 * time.Hour), Status: 500, Attempts: 3},
		{CreatedAt: now},
		{CreatedAt: now.Add(time.Hour)},
	}
	requestUsageForTest(t, a, logs[:len(logs)-1])
	days, err := a.requestActivity(now)
	if err != nil || len(days) != 365 {
		t.Fatal(err)
	}
	for i, day := range days {
		want := map[int]int64{0: 1, 363: 1, 364: 4}[i]
		if day.Date != start.AddDate(0, 0, i).Format("2006-01-02") || day.Requests != want {
			t.Fatalf("activity day %d = %+v, want %d requests", i, day, want)
		}
	}
}
