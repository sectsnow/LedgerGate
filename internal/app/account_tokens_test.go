package app

import (
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestAccountTokenRevealRequiresAdminAndPreservesCredentials(t *testing.T) {
	a := testApp(t)
	account := tokenTestAccount(t, a, "http://127.0.0.1:1")
	h := a.Router()
	cookie := loginForTest(t, h)
	path := fmt.Sprintf("/api/accounts/%d/tokens/reveal", account.ID)
	body := `{"field":"token"}`
	for _, auth := range []string{"", "sk-not-an-admin-session"} {
		if response := call(h, "POST", path, body, auth); response.Code != 401 {
			t.Fatal("token reveal bypassed administrator authentication")
		}
	}
	req := httptest.NewRequest("POST", path, strings.NewReader(body))
	req.Header.Set("Cookie", cookie)
	req.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	h.ServeHTTP(response, req)
	if response.Code != 403 {
		t.Fatal("token reveal bypassed request protection")
	}
	for _, check := range []struct{ field, want string }{{"token", "access-0"}, {"refresh_token", "refresh-0"}} {
		response := call(h, "POST", path, jsonString(object{"field": check.field}), cookie)
		var actual map[string]string
		if response.Code != 200 || json.Unmarshal(response.Body.Bytes(), &actual) != nil || len(actual) != 1 || actual["token"] != check.want {
			t.Fatal("explicit reveal did not return only the requested token")
		}
		if response.Header().Get("Cache-Control") != "no-store" {
			t.Fatal("revealed token response can be cached")
		}
	}
	assertStoredTokens(t, a, account.ID, "access-0", "refresh-0")
	var current Account
	if err := a.DB.First(&current, account.ID).Error; err != nil || current.Token != account.Token || current.RefreshToken != account.RefreshToken || current.Revision != account.Revision {
		t.Fatal("viewing a token modified account credentials")
	}
	listed := call(h, "GET", "/api/accounts", "", cookie)
	for _, secret := range []string{"access-0", "refresh-0", account.Token, account.RefreshToken} {
		if strings.Contains(listed.Body.String(), secret) {
			t.Fatal("normal account listing exposed credentials")
		}
	}
	for _, input := range []string{`{}`, `{"field":"password"}`, `{"field":"cookie"}`, `{"field":"login_extra"}`, `{"field":123}`, `invalid`} {
		if response := call(h, "POST", path, input, cookie); response.Code != 400 {
			t.Fatal("unexpected credential field accepted")
		}
	}
	if call(h, "POST", "/api/accounts/invalid/tokens/reveal", body, cookie).Code != 400 || call(h, "POST", "/api/accounts/99999/tokens/reveal", body, cookie).Code != 404 {
		t.Fatal("invalid or missing account accepted")
	}
	if err := a.DB.Model(&Account{}).Where("id = ?", account.ID).Update("refresh_token", "").Error; err != nil {
		t.Fatal(err)
	}
	response = call(h, "POST", path, `{"field":"refresh_token"}`, cookie)
	if response.Code != 200 || response.Body.String() != `{"token":""}` {
		t.Fatal("absent optional refresh token was fabricated")
	}
	if err := a.DB.Model(&Account{}).Where("id = ?", account.ID).Update("token", "corrupt-ciphertext").Error; err != nil {
		t.Fatal(err)
	}
	response = call(h, "POST", path, body, cookie)
	if response.Code != 500 || strings.Contains(response.Body.String(), "corrupt-ciphertext") {
		t.Fatal("decryption error hidden or ciphertext exposed")
	}
}
