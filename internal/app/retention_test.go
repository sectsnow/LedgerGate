package app

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"testing"
	"time"
)

func retentionRecord(t *testing.T, a *App, at time.Time, state string, cost int64) RequestLog {
	t.Helper()
	log := RequestLog{RequestID: fmt.Sprintf("retention-%d-%s", at.UnixNano(), state), Model: "retention-model", State: state, Status: 200, InputTokens: 100, OutputTokens: 20, CacheReadTokens: 40, CacheWriteTokens: 10, CacheWrite1hTokens: 2, CostMicros: cost, CostStatus: "estimated", CreatedAt: at}
	if activeRequest(state) {
		log.Status, log.CostStatus = 0, "unknown"
	}
	if err := a.DB.Create(&log).Error; err != nil {
		t.Fatal(err)
	}
	payload := requestPayload{Available: true, RequestBody: `{"input":"fixture"}`, ResponseBody: `{"output":"fixture"}`, Attempts: []requestAttempt{}}
	if err := a.DB.Create(&RequestDetail{RequestLogID: log.ID, Payload: a.seal(jsonString(payload))}).Error; err != nil {
		t.Fatal(err)
	}
	return log
}

func TestRetentionSeparatesBodiesRequestsAndSummaries(t *testing.T) {
	a := testApp(t)
	today := usageDay(time.Now())
	now := today.Add(12 * time.Hour)
	ages := []int{0, 6, 7, 89, 90, 364, 365}
	logs := []RequestLog{}
	for _, age := range ages {
		logs = append(logs, retentionRecord(t, a, today.AddDate(0, 0, -age).In(time.UTC), "completed", 125))
	}
	if err := a.DB.Model(&logs[4]).Updates(map[string]any{"cost_status": "unknown", "cost_micros": 999}).Error; err != nil {
		t.Fatal(err)
	}
	if err := a.DB.Create(&RequestDay{Date: today.AddDate(0, 0, -400).Format("2006-01-02"), Requests: 99}).Error; err != nil {
		t.Fatal(err)
	}
	for _, age := range []int{29, 31} {
		at := today.AddDate(0, 0, -age)
		if err := a.DB.Create(&Snapshot{CreatedAt: at}).Error; err != nil {
			t.Fatal(err)
		}
		if err := a.DB.Create(&Alert{CreatedAt: at}).Error; err != nil {
			t.Fatal(err)
		}
	}
	for _, offset := range []time.Duration{-time.Hour, time.Hour} {
		if err := a.DB.Create(&Session{Digest: fmt.Sprint(offset), ExpiresAt: now.Add(offset)}).Error; err != nil {
			t.Fatal(err)
		}
	}
	before, err := a.requestActivity(now)
	if err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if err := a.cleanupHistory(context.Background(), now); err != nil {
			t.Fatal(err)
		}
	}
	for index, log := range logs {
		var count int64
		if err := a.DB.Model(&RequestLog{}).Where("id = ?", log.ID).Count(&count).Error; err != nil {
			t.Fatal(err)
		}
		if (count == 1) != (index < 4) {
			t.Fatalf("request age %d retained incorrectly", ages[index])
		}
		if err := a.DB.Model(&RequestDetail{}).Where("request_log_id = ?", log.ID).Count(&count).Error; err != nil {
			t.Fatal(err)
		}
		if (count == 1) != (index < 2) {
			t.Fatalf("body age %d retained incorrectly", ages[index])
		}
	}
	for _, test := range []struct {
		table any
		want  int64
	}{{&RequestDay{}, 6}, {&Snapshot{}, 1}, {&Alert{}, 1}, {&Session{}, 1}} {
		var count int64
		if err := a.DB.Model(test.table).Count(&count).Error; err != nil || count != test.want {
			t.Fatalf("unexpected retained rows %T: %d, %v", test.table, count, err)
		}
	}
	after, err := a.requestActivity(now)
	if err != nil || !reflect.DeepEqual(before, after) {
		t.Fatalf("cleanup lost or duplicated daily trends: %v", err)
	}
	for _, day := range after {
		if day.Date == today.AddDate(0, 0, -90).Format("2006-01-02") && (day.Requests != 1 || day.InputTokens != 100 || day.CacheReadTokens != 40 || day.CostMicros != 0 || day.CostUnknownRequests != 1) {
			t.Fatal("unknown costs treated as free or archived token details lost", day)
		}
	}
	h := a.Router()
	cookie := loginForTest(t, h)
	response := call(h, "GET", fmt.Sprintf("/api/requests/%d", logs[2].ID), "", cookie)
	var detail struct {
		Detail  requestPayload
		Request RequestLog
	}
	if response.Code != 200 || json.Unmarshal(response.Body.Bytes(), &detail) != nil || detail.Detail.Available || detail.Request.CostMicros != 125 {
		t.Fatal("expired body removed request statistics", response.Body.String())
	}
}

func TestRetentionProtectsActiveDaysAndRefreshesFinalCost(t *testing.T) {
	a := testApp(t)
	now := usageDay(time.Now()).Add(12 * time.Hour)
	day := usageDay(now).AddDate(0, 0, -100)
	retentionRecord(t, a, day, "completed", 123)
	active := retentionRecord(t, a, day.Add(time.Hour), "streaming", 0)
	if err := a.cleanupHistory(context.Background(), now); err != nil {
		t.Fatal(err)
	}
	var count int64
	a.DB.Model(&RequestLog{}).Count(&count)
	if count != 2 {
		t.Fatal("active day's source records partially deleted")
	}
	if err := a.DB.Model(&active).Updates(map[string]any{"state": "completed", "status": 200, "cost_status": "estimated", "cost_micros": 456}).Error; err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if err := a.cleanupHistory(context.Background(), now); err != nil {
			t.Fatal(err)
		}
	}
	a.DB.Model(&RequestLog{}).Count(&count)
	if count != 0 {
		t.Fatal("completed old requests were not archived")
	}
	var summary RequestDay
	if err := a.DB.First(&summary, "date = ?", day.Format("2006-01-02")).Error; err != nil || summary.Requests != 2 || summary.CostMicros != 579 || summary.CostUnknownRequests != 0 {
		t.Fatalf("final fees lost or counted twice: %+v, %v", summary, err)
	}
}

func TestRetentionBackfillsAFullYearWithoutDoubleCounting(t *testing.T) {
	a := testApp(t)
	now := usageDay(time.Now()).Add(12 * time.Hour)
	logs := make([]RequestLog, 366)
	for index := range logs {
		logs[index] = RequestLog{RequestID: fmt.Sprintf("year-%d", index), State: "completed", Status: 200, CostStatus: "estimated", CostMicros: int64(index + 1), CreatedAt: now.AddDate(0, 0, -index)}
	}
	if err := a.DB.CreateInBatches(&logs, 50).Error; err != nil { t.Fatal(err) }
	for range 2 {
		if err := a.cleanupHistory(context.Background(), now); err != nil { t.Fatal(err) }
	}
	var raw, archived int64
	a.DB.Model(&RequestLog{}).Count(&raw)
	a.DB.Model(&RequestDay{}).Count(&archived)
	if raw != 90 || archived != 365 { t.Fatalf("wrong year retention: raw=%d archived=%d", raw, archived) }
	days, err := a.requestActivity(now)
	if err != nil || len(days) != 365 { t.Fatal("year activity missing", err) }
	var total int64
	for _, day := range days {
		if day.Requests != 1 { t.Fatal("archived and raw day counted twice", day) }
		total += day.CostMicros
	}
	if total != 365*366/2 { t.Fatal("year fee totals lost or duplicated", total) }
}

func TestRetentionCleanupRollsBackBeforeDataLoss(t *testing.T) {
	a := testApp(t)
	now := usageDay(time.Now()).Add(12 * time.Hour)
	log := retentionRecord(t, a, now.AddDate(0, 0, -100), "completed", 123)
	for _, test := range []struct{ event, table string }{{"INSERT", "request_days"}, {"DELETE", "request_details"}, {"DELETE", "request_logs"}} {
		trigger := fmt.Sprintf("CREATE TRIGGER reject_retention BEFORE %s ON %s BEGIN SELECT RAISE(ABORT, 'forced failure'); END", test.event, test.table)
		if err := a.DB.Exec(trigger).Error; err != nil {
			t.Fatal(err)
		}
		if err := a.cleanupHistory(context.Background(), now); err == nil {
			t.Fatal("cleanup failure hidden")
		}
		var requests, bodies, summaries int64
		a.DB.Model(&RequestLog{}).Where("id = ?", log.ID).Count(&requests)
		a.DB.Model(&RequestDetail{}).Where("request_log_id = ?", log.ID).Count(&bodies)
		a.DB.Model(&RequestDay{}).Count(&summaries)
		if requests != 1 || bodies != 1 || summaries != 0 {
			t.Fatalf("failed cleanup lost data: %d %d %d", requests, bodies, summaries)
		}
		if err := a.DB.Exec("DROP TRIGGER reject_retention").Error; err != nil {
			t.Fatal(err)
		}
	}
	if err := a.DB.Model(&Setting{}).Where("id = 1").Update("request_body_retention_days", 0).Error; err != nil {
		t.Fatal(err)
	}
	if err := a.cleanupHistory(context.Background(), now); err == nil {
		t.Fatal("invalid retention can purge data")
	}
}

func TestRetentionSettingsValidationAndLegacyClient(t *testing.T) {
	a := testApp(t)
	h := a.Router()
	cookie := loginForTest(t, h)
	var setting Setting
	if err := a.DB.First(&setting, 1).Error; err != nil || setting.RequestBodyRetentionDays != 7 || setting.RequestRetentionDays != 90 || setting.RequestSummaryRetentionDays != 365 {
		t.Fatal("incorrect defaults", setting, err)
	}
	for _, change := range []object{{"request_body_retention_days": 0}, {"request_body_retention_days": -1}, {"request_body_retention_days": 366}, {"request_body_retention_days": 91}, {"request_retention_days": 6}, {"request_summary_retention_days": 89}, {"request_summary_retention_days": 366}} {
		body := object{"webhook_url": "", "alert_cooldown_minutes": 60, "retention_days": 30}
		for key, value := range change {
			body[key] = value
		}
		response := call(h, "PUT", "/api/settings", jsonString(body), cookie)
		if response.Code != 400 {
			t.Fatal("invalid retention accepted", body, response.Body.String())
		}
	}
	var unchanged Setting
	if err := a.DB.First(&unchanged, 1).Error; err != nil || !reflect.DeepEqual(setting, unchanged) {
		t.Fatal("invalid input changed settings")
	}
	response := call(h, "PUT", "/api/settings", `{"webhook_url":"","alert_cooldown_minutes":60,"retention_days":30,"request_body_retention_days":3,"request_retention_days":60,"request_summary_retention_days":180}`, cookie)
	if response.Code != 200 {
		t.Fatal(response.Body.String())
	}
	response = call(h, "PUT", "/api/settings", `{"webhook_url":"","alert_cooldown_minutes":60,"retention_days":45}`, cookie)
	if response.Code != 200 {
		t.Fatal("legacy client rejected", response.Body.String())
	}
	response = call(h, "GET", "/api/settings", "", cookie)
	var saved Setting
	if response.Code != 200 || json.Unmarshal(response.Body.Bytes(), &saved) != nil || saved.RequestBodyRetentionDays != 3 || saved.RequestRetentionDays != 60 || saved.RequestSummaryRetentionDays != 180 || saved.RetentionDays != 45 {
		t.Fatal("legacy client overwrote independent request policies", response.Body.String())
	}
}

func TestRetentionMigrationPreservesExistingPreferences(t *testing.T) {
	dir := t.TempDir()
	config := Config{DataDir: dir, AdminPassword: "test-password-12345"}
	a, err := Open(config)
	if err != nil {
		t.Fatal(err)
	}
	if err := a.DB.Model(&Setting{}).Where("id = 1").Update("retention_days", 45).Error; err != nil {
		t.Fatal(err)
	}
	log := retentionRecord(t, a, time.Now(), "completed", 123)
	for _, column := range []string{"request_body_retention_days", "request_retention_days", "request_summary_retention_days"} {
		if err := a.DB.Migrator().DropColumn(&Setting{}, column); err != nil {
			t.Fatal(err)
		}
	}
	if err := a.DB.Migrator().DropTable(&RequestDay{}); err != nil {
		t.Fatal(err)
	}
	if err := a.Close(); err != nil {
		t.Fatal(err)
	}
	a, err = Open(config)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { a.Close() })
	var setting Setting
	if err := a.DB.First(&setting, 1).Error; err != nil || setting.RetentionDays != 45 || setting.RequestBodyRetentionDays != 7 || setting.RequestRetentionDays != 90 || setting.RequestSummaryRetentionDays != 365 {
		t.Fatal("migration lost preference or failed to set defaults", setting, err)
	}
	var saved RequestLog
	if err := a.DB.First(&saved, log.ID).Error; err != nil || saved.CostMicros != 123 {
		t.Fatal("migration altered existing requests")
	}
	if !a.DB.Migrator().HasTable(&RequestDay{}) {
		t.Fatal("daily summary table not created")
	}
}
