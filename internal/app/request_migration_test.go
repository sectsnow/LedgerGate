package app

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"ledgergate/internal/backup"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
)

func seedLegacyRequest(t *testing.T, a *App, at time.Time) RequestLog {
	t.Helper()
	log := RequestLog{RequestID: "legacy", Model: "old-model", ChannelID: 2, ChannelName: "old-channel", State: "completed", Status: 200,
		InputTokens: 100, OutputTokens: 20, CacheReadTokens: 40, CacheWriteTokens: 10, CacheWrite1hTokens: 2,
		CostMicros: 123, DurationMS: 1000, FirstTokenMS: intPointer(200), GenerationMS: intPointer(800), CreatedAt: at}
	if err := a.DB.Create(&log).Error; err != nil {
		t.Fatal(err)
	}
	if err := a.DB.Create(&RequestDetail{RequestLogID: log.ID, Payload: a.seal(`{"request_body":"old secret body"}`)}).Error; err != nil {
		t.Fatal(err)
	}
	if err := a.DB.Model(&Setting{}).Where("id=1").Update("request_history_migrated", false).Error; err != nil {
		t.Fatal(err)
	}
	return log
}

func TestRequestMigrationArchivesLegacyAndKeepsAccounting(t *testing.T) {
	a := testApp(t)
	now := time.Now()
	known := seedLegacyRequest(t, a, now)
	if err := a.DB.Model(&known).Updates(map[string]any{"metrics_version": 2, "usage_recorded": true, "cache_read_recorded": true, "cache_write_recorded": true}).Error; err != nil {
		t.Fatal(err)
	}
	seedLegacyRequest(t, a, now.Add(-time.Minute))
	date := usageDay(now).Format("2006-01-02")
	if err := a.DB.Create(&RequestDay{Date: date, Requests: 2, CostMicros: 246}).Error; err != nil {
		t.Fatal(err)
	}
	oldDate := usageDay(now).AddDate(0, 0, -100).Format("2006-01-02")
	if err := a.DB.Create(&RequestDay{Date: oldDate, Requests: 9, CostMicros: 987}).Error; err != nil {
		t.Fatal(err)
	}
	key := APIKey{Name: "already-billed", UsedMicros: 456}
	if err := a.DB.Create(&key).Error; err != nil {
		t.Fatal(err)
	}
	config := Config{DataDir: a.dataDir, AdminPassword: "test-password-12345"}
	if err := a.Close(); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		reopened, err := Open(config)
		if err != nil {
			t.Fatal(err)
		}
		var day RequestDay
		if err := reopened.DB.First(&day, "date = ?", date).Error; err != nil || day.Requests != 2 || day.CostMicros != 246 || day.CacheWrite1hTokens != 4 || day.CacheSampleRequests != 1 || day.CacheSampleInputTokens != 100 || day.CacheSampleReadTokens != 40 {
			t.Fatal("legacy overlap doubled or lost totals", day, err)
		}
		day = RequestDay{}
		if err := reopened.DB.First(&day, "date = ?", oldDate).Error; err != nil || day.Requests != 9 || day.CostMicros != 987 {
			t.Fatal("older archived day lost", day, err)
		}
		var hourly struct{ Requests, FirstTokenCount, SemanticFirstTokenCount, DurationMS, GenerationMS, CacheSampleRequests, CacheSampleInputTokens, CacheSampleReadTokens int64 }
		if err := reopened.DB.Model(&RequestHour{}).Select("SUM(requests) AS requests, SUM(first_token_count) AS first_token_count, SUM(semantic_first_token_count) AS semantic_first_token_count, SUM(duration_ms) AS duration_ms, SUM(generation_ms) AS generation_ms, SUM(cache_sample_requests) AS cache_sample_requests, SUM(cache_sample_input_tokens) AS cache_sample_input_tokens, SUM(cache_sample_read_tokens) AS cache_sample_read_tokens").Scan(&hourly).Error; err != nil || hourly.Requests != 2 || hourly.FirstTokenCount != 2 || hourly.SemanticFirstTokenCount != 1 || hourly.DurationMS != 2000 || hourly.GenerationMS != 1600 || hourly.CacheSampleRequests != 1 || hourly.CacheSampleInputTokens != 100 || hourly.CacheSampleReadTokens != 40 {
			t.Fatal("performance aggregates lost", hourly, err)
		}
		for _, model := range []any{&RequestLog{}, &RequestDetail{}} {
			var count int64
			if err := reopened.DB.Model(model).Count(&count).Error; err != nil || count != 0 {
				t.Fatal("old rows remain in live DB", count, err)
			}
		}
		if err := reopened.DB.First(&key, key.ID).Error; err != nil || key.UsedMicros != 456 {
			t.Fatal("migration billed historical key again", err)
		}
		if err := reopened.Close(); err != nil {
			t.Fatal(err)
		}
	}
	archives, err := filepath.Glob(filepath.Join(a.dataDir, "backups", "request-history-*"))
	if err != nil || len(archives) != 1 {
		t.Fatal("archive missing or repeated after restart", archives, err)
	}
	for _, name := range restoreFiles {
		info, err := os.Stat(filepath.Join(archives[0], name))
		if err != nil || info.Mode().Perm() != 0600 {
			t.Fatal("archive incomplete or insufficiently protected", name, err)
		}
	}
	db, err := gorm.Open(sqlite.Open(filepath.Join(archives[0], "ledgergate.db")), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	sqlDB, _ := db.DB()
	defer sqlDB.Close()
	var detail RequestDetail
	if err := db.First(&detail).Error; err != nil {
		t.Fatal("original encrypted contents not archived", err)
	}
	if plain, err := a.unseal(detail.Payload); err != nil || !strings.Contains(plain, "old secret body") {
		t.Fatal("archive body cannot be recovered", err)
	}
}

func TestRequestMigrationFailurePreservesAllRows(t *testing.T) {
	for _, failAt := range []string{"archive", "summary", "details", "requests", "marker"} {
		t.Run(failAt, func(t *testing.T) {
			a := testApp(t)
			seedLegacyRequest(t, a, time.Now())
			if failAt == "archive" {
				if err := os.WriteFile(filepath.Join(a.dataDir, "backups"), []byte("fixture"), 0600); err != nil {
					t.Fatal(err)
				}
			} else {
				operations := map[string]string{"summary": "INSERT ON request_hours", "details": "DELETE ON request_details", "requests": "DELETE ON request_logs", "marker": "UPDATE ON settings"}
				if err := a.DB.Exec("CREATE TRIGGER reject_migration BEFORE " + operations[failAt] + " BEGIN SELECT RAISE(ABORT, 'fixture'); END").Error; err != nil {
					t.Fatal(err)
				}
			}
			if err := a.migrateRequestHistory(); err == nil {
				t.Fatal("migration failure hidden")
			}
			for _, check := range []struct {
				model any
				count int64
			}{{&RequestLog{}, 1}, {&RequestDetail{}, 1}, {&RequestDay{}, 0}, {&RequestHour{}, 0}} {
				var count int64
				if err := a.DB.Model(check.model).Count(&count).Error; err != nil || count != check.count {
					t.Fatal("failed migration partially committed", check.model, count, err)
				}
			}
		})
	}
}

func TestRequestMigrationReclaimsOldDatabase(t *testing.T) {
	for _, legacy := range []bool{false, true} {
		t.Run(map[bool]string{false: "incremental", true: "legacy"}[legacy], func(t *testing.T) {
			a := testApp(t)
			if legacy {
				if err := a.DB.Exec("PRAGMA auto_vacuum=NONE").Error; err != nil {
					t.Fatal(err)
				}
				if err := a.DB.Exec("VACUUM").Error; err != nil {
					t.Fatal(err)
				}
			}
			for range 24 {
				row := seedLegacyRequest(t, a, time.Now())
				if err := a.DB.Model(&RequestDetail{}).Where("request_log_id = ?", row.ID).Update("payload", a.seal(strings.Repeat("old context ", 50000))).Error; err != nil {
					t.Fatal(err)
				}
			}
			if err := a.DB.Exec("PRAGMA wal_checkpoint(TRUNCATE)").Error; err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(a.dataDir, "ledgergate.db")
			before, err := os.Stat(path)
			if err != nil {
				t.Fatal(err)
			}
			if err := a.migrateRequestHistory(); err != nil {
				t.Fatal(err)
			}
			if err := a.reclaimDatabase(context.Background()); err != nil {
				t.Fatal(err)
			}
			after, err := os.Stat(path)
			if err != nil || after.Size()*2 >= before.Size() {
				t.Fatalf("live database did not shrink: before=%d after=%v err=%v", before.Size(), after, err)
			}
			t.Logf("live database bytes: %d -> %d; original archive retained separately", before.Size(), after.Size())
		})
	}
}

func TestRequestBackupKeepsUsageAndExcludesMemoryDetails(t *testing.T) {
	a := testApp(t)
	log := RequestLog{Status: 200, InputTokens: 123, CostMicros: 789, MetricsVersion: 2, UsageRecorded: true, CacheReadRecorded: true, CacheReadTokens: 40}
	trace := a.beginRequestTrace(&log)
	trace.setRequest(log, object{"input": "do-not-back-up-body"})
	a.finishRequestTrace(trace, &log)
	h := a.Router()
	w := call(h, "POST", "/api/backup", `{"password":"request-backup-password"}`, loginForTest(t, h))
	if w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	destination := filepath.Join(t.TempDir(), "restored")
	if err := backup.RestoreReader(bytes.NewReader(w.Body.Bytes()), "request-backup-password", destination); err != nil {
		t.Fatal(err)
	}
	restored, err := Open(Config{DataDir: destination})
	if err != nil {
		t.Fatal(err)
	}
	defer restored.Close()
	if rows, _ := restored.requestSummaries(); len(rows) != 0 {
		t.Fatal("memory requests included in backup")
	}
	var day RequestDay
	if err := restored.DB.First(&day).Error; err != nil || day.InputTokens != 123 || day.CostMicros != 789 || day.CacheSampleRequests != 1 || day.CacheSampleInputTokens != 123 || day.CacheSampleReadTokens != 40 || day.CacheHitRequests != 1 {
		t.Fatal("backup lost persistent usage", day, err)
	}
}
