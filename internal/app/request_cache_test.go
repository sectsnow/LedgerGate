package app

import (
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"gorm.io/gorm"
)

func latestRequestForTest(t *testing.T, a *App) RequestLog {
	t.Helper()
	rows, _ := a.requestSummaries()
	if len(rows) == 0 {
		t.Fatal("request missing from memory")
	}
	return rows[0]
}

func requestUsageForTest(t *testing.T, a *App, rows []RequestLog) {
	t.Helper()
	if err := a.DB.Transaction(func(tx *gorm.DB) error {
		for i := range rows {
			if !activeRequest(rows[i].State) {
				if err := recordRequestUsage(tx, &rows[i]); err != nil {
					return err
				}
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func TestRequestCacheCountBudgetAndActiveProtection(t *testing.T) {
	for _, escaped := range []bool{false, true} {
		t.Run(fmt.Sprint(escaped), func(t *testing.T) {
			a := &App{}
			running := RequestLog{RequestID: "long-running"}
			a.beginRequestTrace(&running)
			text := "x"
			if escaped {
				text = "\x01"
			}
			payload := requestPayload{Available: true, RequestBody: strings.Repeat(text, requestBodyLimit), ResponseBody: strings.Repeat(text, requestBodyLimit), Attempts: []requestAttempt{}}
			for id := uint(2); id <= 61; id++ {
				a.mu.Lock()
				a.cacheFinishedRequest(RequestLog{ID: id, State: "completed", Status: 200}, payload)
				a.mu.Unlock()
			}
			if a.finishedRequestBytes > finishedRequestByteLimit || len(a.finishedRequests) > finishedRequestLimit {
				t.Fatal("cache exceeded limit")
			}
			if !escaped && len(a.finishedRequests) != 50 || escaped && len(a.finishedRequests) >= 50 {
				t.Fatal("count or byte eviction not applied", len(a.finishedRequests))
			}
			if _, _, found := a.cachedRequestDetail(2); found {
				t.Fatal("oldest completion not evicted")
			}
			if log, got, found := a.cachedRequestDetail(61); !found || log.ID != 61 || got.RequestBody != payload.RequestBody {
				t.Fatal("latest completion contents lost")
			}
			rows, active := a.requestSummaries()
			if active != 1 || rows[0].ID != running.ID {
				t.Fatal("completed cache evicted or displaced active request")
			}
			t.Logf("retained=%d serialized_bytes=%d", len(a.finishedRequests), a.finishedRequestBytes)
		})
	}
}

func TestRequestCacheConcurrentFinishKeepsAllUsage(t *testing.T) {
	a := testApp(t)
	key := APIKey{Name: "budget", UsedMicros: 100}
	if err := a.DB.Create(&key).Error; err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	for i := range 65 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			log := RequestLog{RequestID: fmt.Sprint(i), KeyID: key.ID, Status: 200, Model: "cached", CostMicros: 7, InputTokens: 10, OutputTokens: 2, MetricsVersion: 2, UsageRecorded: true, CacheReadRecorded: true, CacheReadTokens: 6}
			trace := a.beginRequestTrace(&log)
			trace.setRequest(log, object{"api_key": "hidden", "input": "hello"})
			trace.appendResponse([]byte(`{"output":"world"}`))
			a.finishRequestTrace(trace, &log)
		}()
	}
	for range 50 {
		a.requestSummaries()
	}
	wg.Wait()
	rows, active := a.requestSummaries()
	if len(rows) != 50 || active != 0 {
		t.Fatal("concurrent cache lost retention bounds", len(rows), active)
	}
	var day RequestDay
	if err := a.DB.First(&day).Error; err != nil || day.Requests != 65 || day.CostMicros != 455 || day.InputTokens != 650 || day.CacheSampleRequests != 65 || day.CacheSampleInputTokens != 650 || day.CacheSampleReadTokens != 390 || day.CacheHitRequests != 65 {
		t.Fatal("eviction lost or duplicated usage", day, err)
	}
	if err := a.DB.First(&key, key.ID).Error; err != nil || key.UsedMicros != 555 {
		t.Fatal("eviction changed key budget", key, err)
	}
	report, err := loadRequestInsights(a.DB, time.Now().Add(-time.Hour), time.Now().Add(time.Hour), nil, nil)
	if err != nil || report.Samples != 65 || report.Duration.Samples != 65 {
		t.Fatal("concurrent completions lost distribution samples", report, err)
	}
	for _, table := range []any{&RequestLog{}, &RequestDetail{}} {
		var count int64
		if err := a.DB.Model(table).Count(&count).Error; err != nil || count != 0 {
			t.Fatal("individual request persisted", count, err)
		}
	}
}

func TestRequestCacheEvictsByFinishTime(t *testing.T) {
	a := &App{}
	now := time.Now()
	for id := uint(1); id <= 51; id++ {
		// 编号越大结束越早，但更晚获得入库和缓存锁。
		finished := now.Add(-time.Duration(id) * time.Second)
		a.cacheFinishedRequest(RequestLog{ID: id, FinishedAt: &finished}, requestPayload{})
	}
	if _, _, found := a.cachedRequestDetail(51); found {
		t.Fatal("late cache insertion displaced a more recently finished request")
	}
	if _, _, found := a.cachedRequestDetail(1); !found {
		t.Fatal("latest completion was evicted")
	}
}

func TestRequestUsageAndDebitRollbackTogether(t *testing.T) {
	a := testApp(t)
	key := APIKey{Name: "budget", UsedMicros: 3}
	if err := a.DB.Create(&key).Error; err != nil {
		t.Fatal(err)
	}
	if err := a.DB.Exec("CREATE TRIGGER reject_debit BEFORE UPDATE ON api_keys BEGIN SELECT RAISE(ABORT, 'fixture'); END").Error; err != nil {
		t.Fatal(err)
	}
	log := RequestLog{KeyID: key.ID, Status: 200, CostMicros: 7, CreatedAt: time.Now()}
	trace := a.beginRequestTrace(&log)
	a.finishRequestTrace(trace, &log)
	for _, table := range []any{&RequestDay{}, &RequestHour{}} {
		var count int64
		if err := a.DB.Model(table).Count(&count).Error; err != nil || count != 0 {
			t.Fatal("failed debit left partial usage", count, err)
		}
	}
	if _, _, found := a.cachedRequestDetail(log.ID); !found {
		t.Fatal("accounting failure hid diagnostic request")
	}
}
