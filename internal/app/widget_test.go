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
	}
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &overview) != nil || overview.Date != today || overview.Requests != 3 || overview.InputTokens != 100 || overview.OutputTokens != 20 || len(overview.Accounts) != 2 || overview.Accounts[0].TodayUsed == nil || !overview.Accounts[0].HasWarning || overview.Accounts[1].HasWarning || overview.Accounts[1].TodayUsed != nil {
		t.Fatal("widget data incorrect", w.Code, w.Body.String())
	}
	if strings.Contains(w.Body.String(), "private") || strings.Contains(w.Body.String(), "cost_micros") || w.Header().Get("Cache-Control") != "no-store" {
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
