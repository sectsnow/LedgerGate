package app

import (
	"encoding/json"
	"path/filepath"
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
)

func TestCacheSummarySamplesExcludeUnknownAndMatchLegacyAggregation(t *testing.T) {
	a := testApp(t)
	base := RequestLog{CreatedAt: time.Now(), Status: 200, MetricsVersion: 2, UsageRecorded: true, InputTokens: 100, CacheReadRecorded: true, CacheReadTokens: 40, CacheWriteRecorded: true, CacheWriteTokens: 20, CacheWrite1hTokens: 5}
	rows := []RequestLog{base, base, base, base, base, base, base}
	rows[1].CacheReadTokens, rows[1].CacheWriteTokens, rows[1].CacheWrite1hTokens = 0, 0, 0
	rows[2].CacheReadRecorded, rows[2].CacheWriteRecorded = false, false
	rows[3].UsageWarning = "invalid usage"
	rows[4].MetricsVersion = 0
	rows[5].InputTokens, rows[5].CacheReadTokens, rows[5].CacheWriteRecorded = 0, 0, false
	rows[6].CacheReadTokens, rows[6].CacheWriteTokens = 120, 120
	requestUsageForTest(t, a, rows)
	var day RequestDay
	if err := a.DB.First(&day).Error; err != nil {
		t.Fatal(err)
	}
	if day.Requests != 7 || day.CacheSampleRequests != 3 || day.CacheSampleInputTokens != 200 || day.CacheSampleReadTokens != 40 || day.CacheHitRequests != 1 || day.CacheWriteSampleRequests != 2 {
		t.Fatalf("unknown, invalid or legacy usage counted as cache samples: %+v", day)
	}
	var hour RequestHour
	if err := a.DB.First(&hour).Error; err != nil || hour.CacheSampleRequests != day.CacheSampleRequests || hour.CacheSampleInputTokens != day.CacheSampleInputTokens || hour.CacheSampleReadTokens != day.CacheSampleReadTokens || hour.CacheHitRequests != day.CacheHitRequests || hour.CacheWriteSampleRequests != day.CacheWriteSampleRequests || hour.CacheReadTokens != day.CacheReadTokens || hour.CacheWriteTokens != day.CacheWriteTokens || hour.CacheWrite1hTokens != day.CacheWrite1hTokens {
		t.Fatalf("daily and hourly cache accounting differs: %+v, %v", hour, err)
	}
	if err := a.DB.Create(&rows).Error; err != nil {
		t.Fatal(err)
	}
	aggregated, err := aggregateRequestDays(a.DB.Model(&RequestLog{}))
	if err != nil || len(aggregated) != 1 || aggregated[0] != day {
		t.Fatalf("legacy and live aggregation differ: %+v, %+v, %v", day, aggregated, err)
	}
}

func TestCacheSummarySchemaUpgradePreservesOldTotals(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "old.db")), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { sqlDB.Close() })
	if err := db.Exec("CREATE TABLE request_days (date TEXT PRIMARY KEY, requests INTEGER, input_tokens INTEGER, cache_read_tokens INTEGER)").Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Exec("INSERT INTO request_days VALUES ('2026-11-25', 12, 1000, 300)").Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Exec("CREATE TABLE request_hours (hour INTEGER, channel_id INTEGER, model TEXT, requests INTEGER, input_tokens INTEGER, PRIMARY KEY (hour, channel_id, model))").Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Exec("INSERT INTO request_hours VALUES (123, 1, 'old', 12, 1000)").Error; err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if err := db.AutoMigrate(&RequestDay{}, &RequestHour{}); err != nil {
			t.Fatal(err)
		}
		var day RequestDay
		if err := db.First(&day).Error; err != nil || day.Requests != 12 || day.InputTokens != 1000 || day.CacheReadTokens != 300 || day.CacheSampleRequests != 0 || day.CacheSampleInputTokens != 0 {
			t.Fatalf("upgrade changed historical usage or invented samples: %+v, %v", day, err)
		}
		var hour RequestHour
		if err := db.First(&hour).Error; err != nil || hour.Requests != 12 || hour.InputTokens != 1000 || hour.CacheSampleRequests != 0 || hour.CacheReadTokens != 0 || hour.Insights != "{}" {
			t.Fatalf("hourly upgrade changed totals or invented cache data: %+v, %v", hour, err)
		}
	}
}

func TestCacheQualityGroupingAndModelFilter(t *testing.T) {
	a := testApp(t)
	base := RequestLog{CreatedAt: time.Now(), Status: 200, Model: "alpha", ChannelID: 1, ChannelName: "A", MetricsVersion: 2, UsageRecorded: true, InputTokens: 1000, CacheReadRecorded: true, CacheReadTokens: 900, CacheWriteRecorded: true, CacheWriteTokens: 40, CacheWrite1hTokens: 10}
	rows := []RequestLog{base, base, base}
	rows[1].Model, rows[1].CacheReadRecorded, rows[1].CacheWriteRecorded = "beta", false, false
	rows[2].ChannelID, rows[2].ChannelName, rows[2].InputTokens, rows[2].CacheReadTokens, rows[2].CacheWriteTokens, rows[2].CacheWrite1hTokens = 2, "B", 100, 0, 0, 0
	requestUsageForTest(t, a, rows)
	h := a.Router()
	cookie := loginForTest(t, h)
	read := func(query string) []qualityRow {
		t.Helper()
		w := call(h, "GET", "/api/request-quality?"+query, "", cookie)
		var result struct{ Items []qualityRow }
		if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &result) != nil {
			t.Fatal("quality request failed", w.Code, w.Body.String())
		}
		return result.Items
	}
	channels := read("group_by=channel")
	if len(channels) != 2 || channels[0].Requests != 2 || channels[0].CacheSampleRequests != 1 || channels[0].CacheSampleInputTokens != 1000 || channels[0].CacheSampleReadTokens != 900 || channels[0].CacheHitRequests != 1 || channels[0].CacheWriteSampleRequests != 1 || channels[0].CacheWrite1hTokens != 20 {
		t.Fatalf("channel samples mixed unknown and known cache usage: %+v", channels)
	}
	models := read("group_by=model&model=alpha")
	if len(models) != 1 || models[0].Requests != 2 || models[0].CacheSampleRequests != 2 || models[0].CacheSampleInputTokens != 1100 || models[0].CacheSampleReadTokens != 900 || models[0].CacheHitRequests != 1 || models[0].CacheWriteSampleRequests != 2 {
		t.Fatalf("model grouping lost weighted sample denominator: %+v", models)
	}
	filtered := read("group_by=channel&model=beta")
	if len(filtered) != 1 || filtered[0].Requests != 1 || filtered[0].CacheSampleRequests != 0 {
		t.Fatalf("model filter invented cache observations: %+v", filtered)
	}
}
