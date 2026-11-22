package app

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func clearRecord(t *testing.T, a *App, at time.Time, version int, state string) RequestLog {
	t.Helper()
	log := retentionRecord(t, a, at, state, 125)
	updates := map[string]any{"metrics_version": version}
	if !activeRequest(state) && version > 0 {
		updates["finished_at"] = at
	}
	if err := a.DB.Model(&log).Updates(updates).Error; err != nil {
		t.Fatal(err)
	}
	return log
}

func clearPreview(t *testing.T, h http.Handler, cookie string, filter requestClearFilter) requestClearPreview {
	t.Helper()
	response := call(h, "POST", "/api/requests/clear/preview", jsonString(filter), cookie)
	var preview requestClearPreview
	if response.Code != 200 || json.Unmarshal(response.Body.Bytes(), &preview) != nil || preview.Token == "" {
		t.Fatalf("preview failed: %d %s", response.Code, response.Body.String())
	}
	return preview
}

func clearExecute(h http.Handler, cookie string, preview requestClearPreview) *httptest.ResponseRecorder {
	return call(h, "POST", "/api/requests/clear", jsonString(object{"token": preview.Token, "confirmation": "清空"}), cookie)
}

func TestRequestClearModesKeepAccountingAndRebuildSummaries(t *testing.T) {
	for _, mode := range []string{"range", "all"} {
		t.Run(mode, func(t *testing.T) {
			a := testApp(t)
			h := a.Router()
			cookie := loginForTest(t, h)
			today := usageDay(time.Now())
			yesterday := today.AddDate(0, 0, -1)
			clearRecord(t, a, yesterday.Add(10*time.Hour), 0, "")
			clearRecord(t, a, yesterday.Add(11*time.Hour), 1, "completed")
			clearRecord(t, a, today, 1, "completed")
			active := clearRecord(t, a, yesterday.Add(12*time.Hour), 1, "streaming")
			archiveDate := today.AddDate(0, 0, -2).Format("2006-01-02")
			if err := a.DB.Create(&RequestDay{Date: archiveDate, Requests: 9}).Error; err != nil {
				t.Fatal(err)
			}
			rawDays, err := requestDays(a.DB, yesterday, time.Now())
			if err != nil || saveRequestDays(a.DB, rawDays) != nil {
				t.Fatal("could not seed summaries")
			}
			key := APIKey{Name: "budget", UsedMicros: 98765}
			account := Account{Name: "upstream", Enabled: true, Balance: floatPointer(42), Used: floatPointer(10)}
			channel := Channel{Name: "saved", Enabled: true, APIKey: a.seal("secret")}
			group := Group{Name: "configured", Strategy: "manual"}
			for _, value := range []any{&key, &account, &channel, &group, &Snapshot{}, &Alert{}} {
				if err := a.DB.Create(value).Error; err != nil {
					t.Fatal(err)
				}
			}
			filter := requestClearFilter{Mode: mode}
			if mode == "range" {
				filter.Start, filter.End = yesterday.Format("2006-01-02"), yesterday.Format("2006-01-02")
			}
			preview := clearPreview(t, h, cookie, filter)
			requests, details, days := int64(2), int64(2), 1
			if mode == "all" {
				requests, details, days = 3, 3, 3
			}
			if preview.Requests != requests || preview.Details != details || preview.SummaryDays != days || preview.KeptRequests != 4-requests || preview.ActiveRequests != 1 {
				t.Fatalf("unexpected preview counts: %+v", preview)
			}
			response := clearExecute(h, cookie, preview)
			if response.Code != 200 {
				t.Fatal(response.Body.String())
			}
			var count int64
			a.DB.Model(&RequestLog{}).Count(&count)
			if count != 4-requests {
				t.Fatal("wrong remaining request count")
			}
			a.DB.Model(&RequestDetail{}).Count(&count)
			if count != 4-details {
				t.Fatal("details not deleted together with requests")
			}
			var protected RequestLog
			if a.DB.First(&protected, active.ID).Error != nil || protected.State != "streaming" {
				t.Fatal("cleanup touched a running request")
			}
			var summary RequestDay
			if a.DB.First(&summary, "date = ?", yesterday.Format("2006-01-02")).Error != nil {
				t.Fatal("active request's daily summary was lost")
			}
			if summary.Requests != 1 || summary.CostUnknownRequests != 1 || summary.CostMicros != 0 {
				t.Fatal("remaining day's counts, unknown costs or fees are inconsistent", summary)
			}
			a.DB.Model(&RequestDay{}).Where("date = ?", archiveDate).Count(&count)
			if (count == 0) != (mode == "all") {
				t.Fatal("archived-only summary cleaned outside selected scope")
			}
			if err := a.cleanupHistory(context.Background(), time.Now()); err != nil {
				t.Fatal(err)
			}
			a.DB.Model(&RequestLog{}).Count(&count)
			if count != 4-requests {
				t.Fatal("automatic retention resurrected deleted requests")
			}
			var savedKey APIKey
			var savedAccount Account
			var savedChannel Channel
			if a.DB.First(&savedKey, key.ID).Error != nil || savedKey.UsedMicros != 98765 || a.DB.First(&savedAccount, account.ID).Error != nil || *savedAccount.Balance != 42 || *savedAccount.Used != 10 || a.DB.First(&savedChannel, channel.ID).Error != nil || savedChannel.APIKey != channel.APIKey {
				t.Fatal("request cleanup reset financial counters or configuration")
			}
			for _, table := range []any{&Group{}, &Snapshot{}, &Alert{}} {
				a.DB.Model(table).Count(&count)
				if count != 1 {
					t.Fatal("request cleanup removed unrelated data")
				}
			}
		})
	}
}

func TestRequestClearFreezesScopeWhileRequestsFinishAndArrive(t *testing.T) {
	a := testApp(t)
	h := a.Router()
	cookie := loginForTest(t, h)
	at := time.Now().Add(-time.Hour)
	old := clearRecord(t, a, at, 0, "completed")
	key := APIKey{Name: "budget", UsedMicros: 50}
	if err := a.DB.Create(&key).Error; err != nil {
		t.Fatal(err)
	}
	ongoing := RequestLog{RequestID: "ongoing", KeyID: key.ID, MetricsVersion: 1, CreatedAt: at.Add(time.Minute)}
	trace := a.beginRequestTrace(&ongoing)
	preview := clearPreview(t, h, cookie, requestClearFilter{Mode: "all"})
	if preview.Requests != 1 || preview.ActiveRequests != 1 {
		t.Fatal("preview included active requests")
	}
	arrived := clearRecord(t, a, time.Now(), 1, "completed")
	ongoing.Status, ongoing.CostMicros = 200, 75
	a.finishRequestTrace(trace, &ongoing)
	if response := clearExecute(h, cookie, preview); response.Code != 200 {
		t.Fatal(response.Body.String())
	}
	for _, id := range []uint{ongoing.ID, arrived.ID} {
		var log RequestLog
		if a.DB.First(&log, id).Error != nil {
			t.Fatal("cleanup expanded to a new or previously running request")
		}
	}
	var count int64
	a.DB.Model(&RequestLog{}).Where("id = ?", old.ID).Count(&count)
	if count != 0 {
		t.Fatal("confirmed old request was not cleared")
	}
	var saved APIKey
	if a.DB.First(&saved, key.ID).Error != nil || saved.UsedMicros != 125 {
		t.Fatal("cleanup reset or duplicated an in-flight debit")
	}
}

func TestRequestClearValidatesPreviewSessionConfirmationAndChanges(t *testing.T) {
	a := testApp(t)
	h := a.Router()
	cookie := loginForTest(t, h)
	log := clearRecord(t, a, time.Now().Add(-time.Hour), 0, "completed")
	for _, path := range []string{"/api/requests/clear/preview", "/api/requests/clear"} {
		if response := call(h, "POST", path, `{}`, ""); response.Code != 401 {
			t.Fatal("unauthenticated cleanup accepted")
		}
		req := httptest.NewRequest("POST", path, strings.NewReader(`{}`))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Cookie", cookie)
		response := httptest.NewRecorder()
		h.ServeHTTP(response, req)
		if response.Code != 403 {
			t.Fatal("cleanup accepted without CSRF protection")
		}
	}
	for _, body := range []string{`{}`, `[]`, `{"mode":"invalid"}`, `{"mode":"legacy"}`, `{"mode":"all","start":"2026-01-01"}`, `{"mode":"range","start":"invalid","end":"2026-01-01"}`, `{"mode":"range","start":"2026-01-02","end":"2026-01-01"}`, `{"mode":"range","start":"2099-01-01","end":"2099-01-02"}`} {
		if response := call(h, "POST", "/api/requests/clear/preview", body, cookie); response.Code != 400 {
			t.Fatal("invalid cleanup scope accepted", body)
		}
	}
	preview := clearPreview(t, h, cookie, requestClearFilter{Mode: "all"})
	if response := call(h, "POST", "/api/requests/clear", jsonString(object{"token": preview.Token}), cookie); response.Code != 400 {
		t.Fatal("full cleanup accepted without typed confirmation")
	}
	if response := call(h, "POST", "/api/requests/clear", jsonString(object{"token": preview.Token + "invalid", "confirmation": "清空"}), cookie); response.Code != 400 {
		t.Fatal("tampered cleanup token accepted")
	}
	otherSession := loginForTest(t, h)
	if response := clearExecute(h, otherSession, preview); response.Code != 403 {
		t.Fatal("cleanup token accepted from a different session")
	}
	raw, _ := a.unseal(preview.Token)
	var plan requestClearPlan
	if json.Unmarshal([]byte(raw), &plan) != nil {
		t.Fatal("could not decode fixture plan")
	}
	plan.ExpiresAt = time.Now().Add(-time.Minute)
	expired := requestClearPreview{Token: a.seal(jsonString(plan))}
	if response := clearExecute(h, cookie, expired); response.Code != 400 {
		t.Fatal("expired preview accepted")
	}
	if err := a.DB.Delete(&log).Error; err != nil {
		t.Fatal(err)
	}
	clearRecord(t, a, time.Now(), 1, "completed")
	if response := clearExecute(h, cookie, preview); response.Code != 409 {
		t.Fatal("stale preview expanded or silently changed scope")
	}
}

func TestRequestClearRollsBackDetailsRequestsAndSummaries(t *testing.T) {
	a := testApp(t)
	h := a.Router()
	cookie := loginForTest(t, h)
	at := time.Now().Add(-time.Hour)
	log := clearRecord(t, a, at, 0, "completed")
	summary := RequestDay{Date: usageDay(at).Format("2006-01-02"), Requests: 1, CostMicros: 125}
	if a.DB.Create(&summary).Error != nil || a.DB.Exec("CREATE TRIGGER reject_summary_clear BEFORE DELETE ON request_days BEGIN SELECT RAISE(ABORT, 'fixture failure'); END").Error != nil {
		t.Fatal("could not seed rollback fixture")
	}
	preview := clearPreview(t, h, cookie, requestClearFilter{Mode: "all"})
	if response := clearExecute(h, cookie, preview); response.Code != 500 {
		t.Fatal("database failure was hidden")
	}
	for _, table := range []any{&RequestLog{}, &RequestDetail{}, &RequestDay{}} {
		var count int64
		a.DB.Model(table).Count(&count)
		if count != 1 {
			t.Fatal("partial cleanup survived rollback")
		}
	}
	if a.DB.Exec("DROP TRIGGER reject_summary_clear").Error != nil || clearExecute(h, cookie, preview).Code != 200 {
		t.Fatal("rolled-back cleanup could not be retried")
	}
	if response := call(h, "GET", fmt.Sprintf("/api/requests/%d", log.ID), "", cookie); response.Code != 404 {
		t.Fatal("deleted request detail remained accessible")
	}
}

func TestRequestClearDateBoundariesAndArchivedOnlyData(t *testing.T) {
	a := testApp(t)
	h := a.Router()
	cookie := loginForTest(t, h)
	day := usageDay(time.Now()).AddDate(0, 0, -3)
	for _, at := range []time.Time{day.Add(-time.Second), day, day.Add(24*time.Hour - time.Second), day.Add(24 * time.Hour)} {
		clearRecord(t, a, at.In(time.UTC), 1, "completed")
	}
	preview := clearPreview(t, h, cookie, requestClearFilter{Mode: "range", Start: day.Format("2006-01-02"), End: day.Format("2006-01-02")})
	if preview.Requests != 2 || clearExecute(h, cookie, preview).Code != 200 {
		t.Fatal("cleanup did not use inclusive Beijing calendar dates")
	}
	if err := a.DB.Create(&RequestDay{Date: day.AddDate(0, 0, -10).Format("2006-01-02"), Requests: 9}).Error; err != nil {
		t.Fatal(err)
	}
	preview = clearPreview(t, h, cookie, requestClearFilter{Mode: "all"})
	if clearExecute(h, cookie, preview).Code != 200 {
		t.Fatal("cleanup failed")
	}
	if err := a.DB.Create(&RequestDay{Date: day.Format("2006-01-02"), Requests: 4}).Error; err != nil {
		t.Fatal(err)
	}
	preview = clearPreview(t, h, cookie, requestClearFilter{Mode: "all"})
	if preview.Requests != 0 || preview.SummaryDays != 1 || clearExecute(h, cookie, preview).Code != 200 {
		t.Fatal("could not clear archived-only history")
	}
	preview = clearPreview(t, h, cookie, requestClearFilter{Mode: "all"})
	if clearExecute(h, cookie, preview).Code != 400 {
		t.Fatal("empty cleanup was accepted")
	}
}

func TestRequestClearCoversAllPagesAndSummaryBatches(t *testing.T) {
	a := testApp(t)
	h := a.Router()
	cookie := loginForTest(t, h)
	now := time.Now().Add(-time.Hour)
	rows := make([]RequestLog, 405)
	for i := range rows {
		rows[i] = RequestLog{RequestID: fmt.Sprintf("clear-page-%d", i), CreatedAt: now.AddDate(0, 0, -(i % 205)), Status: 200, State: "completed"}
	}
	if err := a.DB.CreateInBatches(&rows, 50).Error; err != nil {
		t.Fatal(err)
	}
	preview := clearPreview(t, h, cookie, requestClearFilter{Mode: "all"})
	if preview.Requests != 405 || preview.SummaryDays != 205 || clearExecute(h, cookie, preview).Code != 200 {
		t.Fatal("cleanup only covered one page or exceeded a SQL parameter limit")
	}
}
