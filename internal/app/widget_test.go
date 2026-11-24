package app

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestWidgetCredentialAndOverview(t *testing.T) {
	a := testApp(t)
	h := a.Router()
	cookie := loginForTest(t, h)
	if w := call(h, "POST", "/api/widget/credential", "", ""); w.Code != http.StatusUnauthorized {
		t.Fatal("unauthenticated credential creation allowed", w.Code)
	}
	req := httptest.NewRequest("POST", "/api/widget/credential", nil)
	req.Header.Set("Cookie", cookie)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != http.StatusForbidden {
		t.Fatal("credential creation without request protection allowed", w.Code)
	}
	create := func() string {
		t.Helper()
		response := call(h, "POST", "/api/widget/credential", "", cookie)
		var body struct{ Token string }
		if response.Code != 200 || json.Unmarshal(response.Body.Bytes(), &body) != nil || !strings.HasPrefix(body.Token, "lgw_") || response.Header().Get("Cache-Control") != "no-store" {
			t.Fatal("credential creation failed", response.Code, response.Body.String())
		}
		return body.Token
	}
	token := create()
	var saved widgetCredential
	if err := a.DB.First(&saved, 1).Error; err != nil || saved.Digest != digest(token) || strings.Contains(jsonString(saved), token) {
		t.Fatal("credential was not stored as a digest", err)
	}
	today := usageDay(time.Now()).Format("2006-01-02")
	accounts := []Account{
		{Name: "private name", Currency: "USD", Balance: floatPointer(12), TodayUsed: floatPointer(2), TodayDate: today, Enabled: true, Status: "ok", Token: "private-token", Warnings: "private-warning: 今日消费采集失败"},
		{Currency: "CNY", Balance: floatPointer(0), TodayUsed: floatPointer(99), TodayDate: "2000-01-01", Status: "error"},
	}
	if err := a.DB.Create(&accounts).Error; err != nil {
		t.Fatal(err)
	}
	if err := a.DB.Create(&RequestDay{Date: today, Requests: 3, InputTokens: 100, OutputTokens: 20, CostMicros: 456}).Error; err != nil {
		t.Fatal(err)
	}
	if w := call(h, "GET", "/api/widget/overview", "", ""); w.Code != 401 {
		t.Fatal("anonymous overview allowed", w.Code)
	}
	if w := call(h, "GET", "/api/widget/overview", "", "lgw_wrong"); w.Code != 401 {
		t.Fatal("invalid widget token allowed", w.Code)
	}
	w = call(h, "GET", "/api/widget/overview", "", token)
	var overview struct {
		Date         string          `json:"date"`
		Accounts     []widgetAccount `json:"accounts"`
		Requests     int64           `json:"requests"`
		InputTokens  int64           `json:"input_tokens"`
		OutputTokens int64           `json:"output_tokens"`
		Period       widgetPeriod    `json:"period"`
	}
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &overview) != nil || overview.Date != today || overview.Requests != 3 || overview.InputTokens != 100 || overview.OutputTokens != 20 || overview.Period.Days != 1 || overview.Period.Models == nil || overview.Period.Channels == nil || len(overview.Accounts) != 2 || overview.Accounts[0].TodayUsed == nil || !overview.Accounts[0].HasWarning || overview.Accounts[1].HasWarning || overview.Accounts[1].TodayUsed != nil {
		t.Fatal("widget data incorrect", w.Code, w.Body.String())
	}
	if strings.Contains(w.Body.String(), "private") || w.Header().Get("Cache-Control") != "no-store" {
		t.Fatal("widget exposed fields beyond its scope", w.Body.String())
	}
	if w := call(h, "GET", "/api/accounts", "", token); w.Code != 401 {
		t.Fatal("widget token granted administrator access", w.Code)
	}
	if w := call(h, "POST", "/api/widget/credential", "", token); w.Code != 401 {
		t.Fatal("widget token could create credentials", w.Code)
	}
	if w := call(h, "GET", "/v1/models", "", token); w.Code != 401 {
		t.Fatal("widget token granted gateway access", w.Code)
	}
	newToken := create()
	if token == newToken || call(h, "GET", "/api/widget/overview", "", token).Code != 401 || call(h, "GET", "/api/widget/overview", "", newToken).Code != 200 {
		t.Fatal("rotating widget credential did not revoke the old token")
	}
	if call(h, "DELETE", "/api/widget/credential", "", cookie).Code != 204 || call(h, "GET", "/api/widget/overview", "", newToken).Code != 401 {
		t.Fatal("widget credential was not revoked")
	}
}

func TestWidgetPeriodTotalsAndRankings(t *testing.T) {
	a := testApp(t)
	h := a.Router()
	cookie := loginForTest(t, h)
	w := call(h, "POST", "/api/widget/credential", "", cookie)
	var credential struct{ Token string }
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &credential) != nil {
		t.Fatal("credential creation failed")
	}
	today := usageDay(time.Now())
	days := []RequestDay{
		{Date: today.Format("2006-01-02"), Requests: 2, Errors: 1, InputTokens: 100, OutputTokens: 20, CacheReadTokens: 30, CacheWriteTokens: 5, CostMicros: 200000, CostUnknownRequests: 1},
		{Date: today.AddDate(0, 0, -1).Format("2006-01-02"), Requests: 2, InputTokens: 100, OutputTokens: 30, CacheReadTokens: 10, CostMicros: 100000},
		{Date: today.AddDate(0, 0, -8).Format("2006-01-02"), Requests: 1, InputTokens: 80, OutputTokens: 20},
		{Date: today.AddDate(0, 0, -30).Format("2006-01-02"), Requests: 99, InputTokens: 9999},
	}
	if err := a.DB.Create(&days).Error; err != nil {
		t.Fatal(err)
	}
	hours := []RequestHour{
		{Hour: today.Add(time.Hour).Unix(), ChannelID: 1, ChannelName: "主渠道", Model: "alpha", Requests: 2, InputTokens: 100, OutputTokens: 20},
		{Hour: today.AddDate(0, 0, -1).Add(time.Hour).Unix(), ChannelID: 1, ChannelName: "主渠道", Model: "alpha", Requests: 1, InputTokens: 40, OutputTokens: 10},
		{Hour: today.AddDate(0, 0, -1).Add(time.Hour).Unix(), ChannelID: 2, ChannelName: "备用渠道", Model: "beta", Requests: 1, InputTokens: 60, OutputTokens: 20},
		{Hour: today.AddDate(0, 0, -8).Add(time.Hour).Unix(), ChannelID: 2, ChannelName: "备用渠道", Model: "beta", Requests: 1, InputTokens: 80, OutputTokens: 20},
	}
	if err := a.DB.Create(&hours).Error; err != nil {
		t.Fatal(err)
	}
	if err := a.DB.Model(&Setting{}).Where("id = ?", 1).Update("request_summary_retention_days", 7).Error; err != nil {
		t.Fatal(err)
	}
	read := func(query string) widgetPeriod {
		t.Helper()
		response := call(h, "GET", "/api/widget/overview"+query, "", credential.Token)
		var body struct{ Period widgetPeriod }
		if response.Code != 200 || json.Unmarshal(response.Body.Bytes(), &body) != nil {
			t.Fatal("period request failed", response.Code, response.Body.String())
		}
		return body.Period
	}
	week := read("?days=7")
	if week.Days != 7 || week.RetentionDays != 7 || week.Requests != 4 || week.Errors != 1 || week.InputTokens != 200 || week.OutputTokens != 50 || week.CacheReadTokens != 40 || week.CacheWriteTokens != 5 || week.CostMicros != 300000 || week.CostUnknownRequests != 1 {
		t.Fatalf("unexpected weekly totals: %+v", week)
	}
	if len(week.Models) != 2 || week.Models[0].Name != "alpha" || week.Models[0].Tokens != 170 || len(week.Channels) != 2 || week.Channels[0].Name != "主渠道" || week.Channels[0].Requests != 3 {
		t.Fatalf("unexpected weekly rankings: %+v", week)
	}
	if len(week.Trend) != 7 || week.Trend[0].Available || !week.Trend[5].Available || week.Trend[5].Requests != 2 || !week.Trend[6].Available || week.Trend[6].Errors != 1 {
		t.Fatalf("unexpected weekly trend, missing dates must remain unavailable: %+v", week.Trend)
	}
	if current := read(""); current.Days != 1 || current.Requests != 2 || len(current.Models) != 1 {
		t.Fatalf("unexpected daily totals: %+v", current)
	}
	if month := read("?days=30"); month.Requests != 5 || month.InputTokens != 280 || month.RetentionDays != 7 {
		t.Fatalf("unexpected monthly totals: %+v", month)
	}
	if response := call(h, "GET", "/api/widget/overview?days=2", "", credential.Token); response.Code != http.StatusBadRequest {
		t.Fatal("unsupported period allowed", response.Code)
	}
}

func TestWidgetCredentialPersistsAndRestoresWithBackup(t *testing.T) {
	source := testApp(t)
	h := source.Router()
	cookie := loginForTest(t, h)
	w := call(h, "POST", "/api/widget/credential", "", cookie)
	var original struct{ Token string }
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &original) != nil {
		t.Fatal("credential creation failed")
	}
	backup := call(h, "POST", "/api/backup", `{"password":"backup-password-123"}`, cookie)
	if backup.Code != 200 {
		t.Fatal("backup failed", backup.Code)
	}
	reopened, err := Open(Config{DataDir: source.dataDir, AdminPassword: "test-password-12345"})
	if err != nil {
		t.Fatal(err)
	}
	if response := call(reopened.Router(), "GET", "/api/widget/overview", "", original.Token); response.Code != 200 {
		t.Fatal("credential was lost after reopening", response.Code)
	}
	if err := reopened.Close(); err != nil {
		t.Fatal(err)
	}
	target := testApp(t)
	targetHandler := target.Router()
	targetCookie := loginForTest(t, targetHandler)
	if response := restoreUploadForTest(t, targetHandler, targetCookie, backup.Body.Bytes(), restoreTestPassword, "backup.lgb", "恢复", true); response.Code != 200 {
		t.Fatal("restore failed", response.Code, response.Body.String())
	}
	if response := call(targetHandler, "GET", "/api/widget/overview", "", original.Token); response.Code != 200 {
		t.Fatal("backup did not restore widget credential", response.Code)
	}
}
