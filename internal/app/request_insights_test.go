package app

import (
	"bytes"
	"context"
	"encoding/json"
	"math"
	"path/filepath"
	"testing"
	"time"

	"gorm.io/gorm"
	"ledgergate/internal/backup"
)

func TestLatencyHistogramQuantilesAndBounds(t *testing.T) {
	h := latencyHistogram{}
	if h.percentiles().P50MS != nil {
		t.Fatal("empty histogram invented a percentile")
	}
	for i := int64(1); i <= 1000; i++ {
		h.add(i)
	}
	result := h.percentiles()
	if result.Samples != 1000 || result.P50MS == nil || result.P95MS == nil || *result.P50MS < 500 || *result.P50MS > 531 || *result.P95MS < 950 || *result.P95MS > 1009 {
		t.Fatalf("incorrect nearest-rank upper bounds: %+v", result)
	}
	h.add(-1)
	if h.percentiles().Samples != 1000 {
		t.Fatal("negative duration became a sample")
	}
	h = latencyHistogram{}
	h.add(0)
	if value := h.percentiles(); value.P50MS == nil || *value.P50MS != 0 {
		t.Fatal("zero timing treated as missing")
	}
	h.add(math.MaxInt64)
	if h[math.MaxInt64] != 1 {
		t.Fatal("last histogram bucket overflowed")
	}
}

func savingsLogForTest() RequestLog {
	return RequestLog{Status: 200, MetricsVersion: 2, UsageRecorded: true, CacheReadRecorded: true, CacheWriteRecorded: true, InputTokens: 1000, CacheReadTokens: 600, CacheWriteTokens: 200, CacheWrite1hTokens: 100, CostStatus: "estimated", CostDetail: &requestCostDetail{Mode: "token", Rates: priceRates{Input: intPointer(1_000_000), CacheRead: intPointer(100_000), CacheWrite: intPointer(1_250_000), CacheWrite1h: intPointer(2_000_000)}}}
}

func TestCacheSavingsUsesFrozenRatesAndIncludesWriteCosts(t *testing.T) {
	log := savingsLogForTest()
	baseline, actual, ok := cacheSavings(&log)
	if !ok || baseline != 1000 || actual != 585 {
		t.Fatal("write costs missing from net saving", baseline, actual, ok)
	}
	log.CostDetail.MultiplierApplied = true
	factor := 2.0
	log.CostDetail.Multiplier = &factor
	baseline, actual, ok = cacheSavings(&log)
	if !ok || baseline != 2000 || actual != 1170 {
		t.Fatal("multiplier not applied", baseline, actual)
	}
	log.CostDetail.RatesIncludeMultiplier = true
	baseline, actual, ok = cacheSavings(&log)
	if !ok || baseline != 1000 || actual != 585 {
		t.Fatal("included multiplier applied twice", baseline, actual)
	}
	log = savingsLogForTest()
	log.CacheReadTokens = 0
	log.CacheWriteTokens = 1000
	log.CacheWrite1hTokens = 1000
	baseline, actual, ok = cacheSavings(&log)
	if !ok || actual <= baseline {
		t.Fatal("negative net savings were hidden", baseline, actual)
	}
	log = savingsLogForTest()
	log.CostDetail.Rates.CacheRead = nil
	if _, _, ok := cacheSavings(&log); ok {
		t.Fatal("missing cache price invented saving")
	}
	log = savingsLogForTest()
	log.CacheWriteRecorded = false
	if _, _, ok := cacheSavings(&log); ok {
		t.Fatal("missing write usage assumed zero")
	}
	log = savingsLogForTest()
	log.CostDetail.MultiplierStale = true
	if _, _, ok := cacheSavings(&log); ok {
		t.Fatal("stale multiplier treated as certain")
	}
	log = savingsLogForTest()
	log.CostDetail.Mode = "request"
	if _, _, ok := cacheSavings(&log); ok {
		t.Fatal("per-request billing treated as token savings")
	}
}

func TestRequestInsightsPersistFilterBackupAndRetention(t *testing.T) {
	a := testApp(t)
	now := time.Now().Add(-time.Minute)
	for i := 0; i < 7; i++ {
		log := savingsLogForTest()
		log.CreatedAt = now
		log.Model = "alpha"
		log.ChannelID = 1
		log.ChannelName = "A"
		log.Stream = true
		log.DurationMS = int64(i+1) * 1000
		log.FirstTokenMS = intPointer(30)
		log.Attempts = 1
		payload := requestPayload{Attempts: []requestAttempt{{Status: 200, StartedAt: now}}}
		switch i {
		case 0:
			log.Attempts = 2
			payload.Attempts = []requestAttempt{{Status: 502, StartedAt: now}, {Status: 200, StartedAt: now.Add(200 * time.Millisecond)}}
		case 1:
			log.Status = 429
			log.CostStatus = "unknown"
			log.FirstTokenMS = nil
			payload.Attempts[0].Status = 429
		case 2:
			log.MetricsVersion = 0
			log.UsageRecorded = false
			log.CacheReadRecorded = false
		case 3:
			log.CacheReadTokens = 0
			log.CacheWriteTokens = 0
			log.CacheWrite1hTokens = 0
			log.FirstTokenMS = intPointer(80)
		case 4:
			log.Model = "beta"
		case 5:
			log.Stream = false
		case 6:
			log.Status, log.CostStatus, log.FirstTokenMS, log.Attempts = 499, "unknown", nil, 2
			payload.Attempts = []requestAttempt{{Status: 502, StartedAt: now}, {Status: 499, StartedAt: now.Add(300 * time.Millisecond)}}
		}
		if err := a.DB.Transaction(func(tx *gorm.DB) error {
			if err := recordRequestUsage(tx, &log); err != nil {
				return err
			}
			if err := recordRoutingQuality(tx, &log, payload); err != nil {
				return err
			}
			return recordRequestInsights(tx, &log, payload)
		}); err != nil {
			t.Fatal(err)
		}
	}
	start := usageDay(now)
	end := start.AddDate(0, 0, 1)
	channel := uint(1)
	model := "alpha"
	report, err := loadRequestInsights(a.DB, start, end, &channel, &model)
	if err != nil || report.Requests != 6 || report.Samples != 6 || report.Duration.Samples != 6 || report.First.Samples != 3 || report.RetryEligible != 2 || report.RetryRescued != 1 || report.WaitMS != 500 || report.WaitSamples != 6 || len(report.Errors) != 2 || report.Errors[0].Count != 1 || report.ComparisonTotal != 2 || report.SavingsSamples != 3 {
		t.Fatalf("incorrect report: %+v, %v", report, err)
	}
	for _, pair := range report.Comparisons {
		if pair.Model != "alpha" || pair.ChannelID != 1 {
			t.Fatal("comparison mixed channels/models", pair)
		}
		if pair.Stream && (pair.Hit.Samples != 1 || pair.Miss.Samples != 1 || pair.Hit.DurationMS != 1000 || pair.Miss.DurationMS != 4000) {
			t.Fatal("cache cohorts mixed modes or errors", pair)
		}
	}
	if len(report.Trend) != 24 {
		t.Fatal("missing hourly gap points", len(report.Trend))
	}
	var before string
	var row RequestHour
	if err := a.DB.First(&row).Error; err != nil {
		t.Fatal(err)
	}
	before = row.Insights
	changed := savingsLogForTest()
	*changed.CostDetail.Rates.Input = 9_000_000
	after, err := loadRequestInsights(a.DB, start, end, &channel, &model)
	if err != nil || after.BaselineMicros != report.BaselineMicros {
		t.Fatal("current prices changed stored savings")
	}
	h := a.Router()
	cookie := loginForTest(t, h)
	response := call(h, "GET", "/api/request-quality?group_by=channel&model=alpha&detail=1", "", cookie)
	var body struct {
		Insights *advancedReport `json:"insights"`
	}
	if response.Code != 200 || json.Unmarshal(response.Body.Bytes(), &body) != nil || body.Insights == nil || body.Insights.Samples != 6 {
		t.Fatal("detail endpoint missing insights", response.Code, response.Body.String())
	}
	if call(h, "GET", "/api/request-quality?detail=1", "", "").Code != 401 {
		t.Fatal("unauthenticated metrics access")
	}
	if call(h, "GET", "/api/request-quality?detail=bad", "", cookie).Code != 400 {
		t.Fatal("invalid channel accepted")
	}
	response = call(h, "POST", "/api/widget/credential", "", cookie)
	var credential struct{ Token string }
	json.Unmarshal(response.Body.Bytes(), &credential)
	response = call(h, "GET", "/api/widget/overview", "", credential.Token)
	var widget struct{ Period widgetPeriod }
	json.Unmarshal(response.Body.Bytes(), &widget)
	if response.Code != 200 || widget.Period.Insights == nil || widget.Period.Insights.Samples != 7 || widget.Period.Quality.AttemptOverheadMS != 500 {
		t.Fatal("widget missing aggregate insights", response.Body.String())
	}
	response = call(h, "POST", "/api/backup", `{"password":"insights-backup-password"}`, cookie)
	destination := filepath.Join(t.TempDir(), "restored")
	if response.Code != 200 {
		t.Fatal(response.Body.String())
	}
	if err := backup.RestoreReader(bytes.NewReader(response.Body.Bytes()), "insights-backup-password", destination); err != nil {
		t.Fatal(err)
	}
	restored, err := Open(Config{DataDir: destination})
	if err != nil {
		t.Fatal(err)
	}
	defer restored.Close()
	got, err := loadRequestInsights(restored.DB, start, end, &channel, &model)
	if err != nil || got.Samples != report.Samples || got.ActualMicros != report.ActualMicros || got.Duration.Samples != report.Duration.Samples {
		t.Fatal("backup/reopen lost insight aggregates", got, err)
	}
	if err := restored.DB.First(&row).Error; err != nil || row.Insights != before {
		t.Fatal("backup changed histogram", err)
	}
	if err := restored.cleanupHistory(context.Background(), now.AddDate(0, 0, 367)); err != nil {
		t.Fatal(err)
	}
	got, err = loadRequestInsights(restored.DB, start, end, &channel, &model)
	if err != nil || got.Samples != 0 || got.Duration.P50MS != nil {
		t.Fatal("expired distributions retained", got, err)
	}
}
