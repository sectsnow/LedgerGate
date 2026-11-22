package app

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
)

func TestNewAPIRefreshCookieRotationAndForbiddenDoesNotLogin(t *testing.T) {
	a := testApp(t)
	var generation atomic.Int32
	var forbidden atomic.Bool
	var logins atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/user/login":
			logins.Add(1)
			var body object
			_ = json.NewDecoder(r.Body).Decode(&body)
			if body["extra_field"] != "required" || body["password"] != "password-123" {
				t.Error("login extras or password changed")
			}
			http.SetCookie(w, &http.Cookie{Name: "new_api_refresh", Value: "refresh-0"})
			fmt.Fprint(w, `{"success":true,"data":{"id":7,"access_token":"access-0"}}`)
		case "/api/user/auth/refresh":
			cookie, err := r.Cookie("new_api_refresh")
			if err != nil || cookie.Value != fmt.Sprintf("refresh-%d", generation.Load()) || r.Header.Get("Authorization") != "" || r.Header.Get("Origin") == "" {
				t.Error("invalid refresh authentication")
			}
			next := generation.Add(1)
			http.SetCookie(w, &http.Cookie{Name: "new_api_refresh", Value: fmt.Sprintf("refresh-%d", next)})
			fmt.Fprintf(w, `{"success":true,"data":{"access_token":"access-%d"}}`, next)
		default:
			if forbidden.Load() {
				w.WriteHeader(403)
				return
			}
			if r.Header.Get("Authorization") != fmt.Sprintf("Bearer access-%d", generation.Load()+1) {
				w.WriteHeader(401)
				return
			}
			fmt.Fprint(w, `{"success":true,"data":{"quota":1}}`)
		}
	}))
	defer server.Close()
	account := Account{Name: "new", Kind: "newapi", BaseURL: server.URL, AuthMode: "password", Username: "u", Password: a.seal("password-123"), LoginExtra: a.seal(`{"extra_field":"required","password":"override"}`)}
	if err := a.DB.Create(&account).Error; err != nil {
		t.Fatal(err)
	}
	if err := a.loginAccount(context.Background(), &account); err != nil {
		t.Fatal(err)
	}
	// 首次业务请求触发一次轮换，随后返回 401，不再无限续期。
	_, _, _ = a.accountRequestWithRefresh(context.Background(), &account, "GET", "/api/user/self", nil)
	assertStoredTokens(t, a, account.ID, "access-1", "refresh-1")
	forbidden.Store(true)
	if _, status, err := a.accountRequest(context.Background(), &account, "GET", "/api/user/self", nil); err == nil || status != 403 {
		t.Fatal("403 was not preserved")
	}
	if logins.Load() != 1 {
		t.Fatal("403 caused password login")
	}
}
