package app

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"time"
)

func TestRetentionKeepsSummaryBoundaryAndActiveCache(t *testing.T) {
	a := testApp(t)
	now := usageDay(time.Now()).Add(12 * time.Hour)
	for _, age := range []int{0, 29, 30, 364, 365} {
		at := usageDay(now).AddDate(0, 0, -age)
		requestUsageForTest(t, a, []RequestLog{{CreatedAt: at, Status: 200, InputTokens: 100, CacheReadTokens: 40, CostMicros: 123}})
		if err := a.DB.Create(&Snapshot{CreatedAt: at}).Error; err != nil {
			t.Fatal(err)
		}
	}
	active := RequestLog{RequestID: "still-running"}
	a.beginRequestTrace(&active)
	for range 2 {
		if err := a.cleanupHistory(context.Background(), now); err != nil {
			t.Fatal(err)
		}
	}
	for _, check := range []struct {
		table any
		count int64
	}{{&RequestDay{}, 4}, {&RequestHour{}, 4}, {&Snapshot{}, 2}} {
		var count int64
		if err := a.DB.Model(check.table).Count(&count).Error; err != nil || count != check.count {
			t.Fatalf("%T count=%d want=%d err=%v", check.table, count, check.count, err)
		}
	}
	rows, running := a.requestSummaries()
	if running != 1 || len(rows) != 1 {
		t.Fatal("summary retention removed active cache")
	}
	days, err := a.requestActivity(now)
	if err != nil {
		t.Fatal(err)
	}
	var requests, cost int64
	for _, day := range days {
		requests += day.Requests
		cost += day.CostMicros
	}
	if requests != 4 || cost != 492 {
		t.Fatal("cleanup lost or duplicated summary", requests, cost)
	}
}

func TestRetentionSummaryDeletionRollsBack(t *testing.T) {
	a := testApp(t)
	now := time.Now()
	requestUsageForTest(t, a, []RequestLog{{CreatedAt: now.AddDate(0, 0, -400), Status: 200}})
	if err := a.DB.Exec("CREATE TRIGGER reject_summary BEFORE DELETE ON request_days BEGIN SELECT RAISE(ABORT, 'fixture'); END").Error; err != nil {
		t.Fatal(err)
	}
	if err := a.cleanupHistory(context.Background(), now); err == nil {
		t.Fatal("cleanup failure hidden")
	}
	for _, table := range []any{&RequestDay{}, &RequestHour{}} {
		var count int64
		if err := a.DB.Model(table).Count(&count).Error; err != nil || count != 1 {
			t.Fatal("partial retention committed", count, err)
		}
	}
}

func TestRetentionSettingsValidationAndLegacyClient(t *testing.T) {
	a := testApp(t)
	h := a.Router()
	cookie := loginForTest(t, h)
	for _, days := range []int{0, -1, 366} {
		body := fmt.Sprintf(`{"alert_cooldown_minutes":60,"retention_days":30,"request_summary_retention_days":%d}`, days)
		if response := call(h, "PUT", "/api/settings", body, cookie); response.Code != 400 {
			t.Fatal("invalid retention accepted", response.Body.String())
		}
	}
	response := call(h, "PUT", "/api/settings", `{"alert_cooldown_minutes":60,"retention_days":45,"request_body_retention_days":7,"request_retention_days":90,"request_summary_retention_days":10}`, cookie)
	if response.Code != 200 {
		t.Fatal("legacy client fields rejected", response.Body.String())
	}
	response = call(h, "GET", "/api/settings", "", cookie)
	var saved map[string]any
	if response.Code != 200 || json.Unmarshal(response.Body.Bytes(), &saved) != nil || saved["request_summary_retention_days"] != float64(10) || saved["retention_days"] != float64(45) {
		t.Fatal("summary policy not saved", response.Body.String())
	}
	for _, obsolete := range []string{"request_body_retention_days", "request_retention_days"} {
		if _, exists := saved[obsolete]; exists {
			t.Fatal("obsolete setting exposed", obsolete)
		}
	}
}
