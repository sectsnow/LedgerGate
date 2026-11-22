package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func tokenTestAccount(t *testing.T, a *App, baseURL string) Account {
	t.Helper()
	account := Account{Name: "Sub2API", Kind: "sub2api", BaseURL: baseURL, AuthMode: "token", Token: a.seal("access-0"), RefreshToken: a.seal("refresh-0"), Currency: "USD", QuotaPerUnit: 500000, IntervalSeconds: 300, Revision: 1}
	if err := a.DB.Create(&account).Error; err != nil {
		t.Fatal(err)
	}
	return account
}

func assertStoredTokens(t *testing.T, a *App, id uint, access, refresh string) {
	t.Helper()
	var account Account
	if err := a.DB.First(&account, id).Error; err != nil {
		t.Fatal(err)
	}
	for _, pair := range []struct{ ciphertext, want string }{{account.Token, access}, {account.RefreshToken, refresh}} {
		plain, err := a.unseal(pair.ciphertext)
		if err != nil || plain != pair.want {
			t.Fatal("stored credential differs from expected credential")
		}
		if pair.want != "" && pair.ciphertext == pair.want {
			t.Fatal("credential stored as plaintext")
		}
	}
}

func TestAccountTokenPairEditing(t *testing.T) {
	a := testApp(t)
	h := a.Router()
	cookie := loginForTest(t, h)
	input := accountInput{Name: "Sub2API", Kind: "sub2api", BaseURL: "http://127.0.0.1:1", AuthMode: "token", Token: "access-0", RefreshToken: "refresh-0", Currency: "USD", QuotaPerUnit: 500000, IntervalSeconds: 300}
	w := call(h, "POST", "/api/accounts", jsonString(input), cookie)
	if w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	assertStoredTokens(t, a, 1, "access-0", "refresh-0")
	for _, path := range []string{"/api/accounts", "/api/accounts/1/history"} {
		response := call(h, "GET", path, "", cookie)
		if response.Code != 200 || strings.Contains(response.Body.String(), "access-0") || strings.Contains(response.Body.String(), "refresh-0") {
			t.Fatal("credential exposed in admin response")
		}
	}
	w = call(h, "GET", "/api/accounts", "", cookie)
	if !strings.Contains(w.Body.String(), `"has_refresh_token":true`) || strings.Contains(w.Body.String(), `"refresh_token":`) {
		t.Fatal("refresh token storage flag missing or ciphertext exposed")
	}
	input.Name, input.Token, input.RefreshToken = "renamed", "", ""
	if w := call(h, "PUT", "/api/accounts/1", jsonString(input), cookie); w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	assertStoredTokens(t, a, 1, "access-0", "refresh-0")
	input.RefreshToken = "refresh-unpaired"
	if w := call(h, "PUT", "/api/accounts/1", jsonString(input), cookie); w.Code != 400 {
		t.Fatal("unpaired refresh token accepted")
	}
	assertStoredTokens(t, a, 1, "access-0", "refresh-0")
	input.Token, input.RefreshToken = "access-1", "refresh-1"
	if w := call(h, "PUT", "/api/accounts/1", jsonString(input), cookie); w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	assertStoredTokens(t, a, 1, "access-1", "refresh-1")
	input.Token, input.RefreshToken = "access-only", ""
	if w := call(h, "PUT", "/api/accounts/1", jsonString(input), cookie); w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	assertStoredTokens(t, a, 1, "access-only", "")
	input.Kind, input.RefreshToken = "newapi", "refresh-2"
	if w := call(h, "PUT", "/api/accounts/1", jsonString(input), cookie); w.Code != 400 {
		t.Fatal("NewAPI accepted Sub2API refresh credentials")
	}
	assertStoredTokens(t, a, 1, "access-only", "")
}

func TestRefreshRotatesTokensAndRetriesCollection(t *testing.T) {
	for _, expiryPath := range []string{"/api/v1/auth/me", "/api/v1/usage/dashboard/stats"} {
		t.Run(expiryPath, func(t *testing.T) {
			a := testApp(t)
			var generation atomic.Int32
			var expired atomic.Bool
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				if r.URL.Path == "/api/v1/auth/refresh" {
					if r.Method != "POST" || r.Header.Get("Authorization") != "" {
						t.Error("refresh must use its own credential")
					}
					var body object
					if json.NewDecoder(r.Body).Decode(&body) != nil || body["refresh_token"] != fmt.Sprintf("refresh-%d", generation.Load()) {
						t.Error("stale refresh token reused")
					}
					next := generation.Add(1)
					fmt.Fprintf(w, `{"code":0,"data":{"access_token":"access-%d","refresh_token":"refresh-%d"}}`, next, next)
					return
				}
				if r.Header.Get("Authorization") != fmt.Sprintf("Bearer access-%d", generation.Load()) {
					t.Error("wrong access token")
				}
				if r.URL.Path == expiryPath && expired.Swap(false) {
					w.WriteHeader(401)
					return
				}
				switch r.URL.Path {
				case "/api/v1/auth/me":
					fmt.Fprint(w, `{"data":{"balance":25}}`)
				case "/api/v1/usage/dashboard/stats":
					fmt.Fprint(w, `{"data":{"total_actual_cost":12,"today_actual_cost":2}}`)
				case "/api/v1/groups/rates":
					fmt.Fprint(w, `{"data":{}}`)
				default:
					fmt.Fprint(w, `{"data":[]}`)
				}
			}))
			defer server.Close()
			account := tokenTestAccount(t, a, server.URL)
			h := a.Router()
			cookie := loginForTest(t, h)
			for iteration := 1; iteration <= 2; iteration++ {
				expired.Store(true)
				response := call(h, "POST", "/api/accounts/1/check", "", cookie)
				if response.Code != 200 {
					t.Fatal(response.Body.String())
				}
				var collected Account
				if json.Unmarshal(response.Body.Bytes(), &collected) != nil || collected.Status != "ok" || collected.Used == nil || *collected.Used != 12 {
					t.Fatal("account collection did not recover")
				}
				if collected.Warnings != "" {
					t.Fatal(collected.Warnings)
				}
				assertStoredTokens(t, a, account.ID, fmt.Sprintf("access-%d", iteration), fmt.Sprintf("refresh-%d", iteration))
			}
			if generation.Load() != 2 {
				t.Fatal("refresh loop or missing refresh")
			}
		})
	}
}

func TestRefreshFailureAndRetryBound(t *testing.T) {
	for _, test := range []struct {
		name, body                            string
		status                                int
		noRefresh, retryUnauthorized, success bool
	}{
		{name: "revoked_refresh", status: 401, body: `{"message":"secret-upstream-detail"}`},
		{name: "temporary_failure", status: 503, body: `{}`},
		{name: "invalid_json", status: 200, body: `<html>error</html>`},
		{name: "missing_access", status: 200, body: `{"data":{"refresh_token":"unusable-new-refresh"}}`},
		{name: "access_only_account", noRefresh: true},
		{name: "retry_still_unauthorized", status: 200, body: `{"data":{"access_token":"access-1","refresh_token":"refresh-1"}}`, retryUnauthorized: true},
		{name: "refresh_not_rotated", status: 200, body: `{"data":{"access_token":"access-1"}}`, success: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			a := testApp(t)
			var refreshCalls, dataCalls atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				if r.URL.Path == "/api/v1/auth/refresh" {
					refreshCalls.Add(1)
					w.WriteHeader(test.status)
					fmt.Fprint(w, test.body)
					return
				}
				if dataCalls.Add(1) == 1 || test.retryUnauthorized {
					w.WriteHeader(401)
					return
				}
				fmt.Fprint(w, `{"data":{"balance":25}}`)
			}))
			defer server.Close()
			account := tokenTestAccount(t, a, server.URL)
			if test.noRefresh {
				account.RefreshToken = ""
				a.DB.Model(&account).Update("refresh_token", "")
			}
			_, _, err := a.accountRequest(context.Background(), &account, "GET", "/api/v1/auth/me", nil)
			if (err == nil) != test.success {
				t.Fatalf("unexpected refresh outcome %v", err)
			}
			if err != nil && strings.Contains(err.Error(), "secret-upstream-detail") {
				t.Fatal("upstream error leaked secret material")
			}
			if test.noRefresh {
				if refreshCalls.Load() != 0 {
					t.Fatal("missing refresh token was sent")
				}
			} else if refreshCalls.Load() != 1 {
				t.Fatal("refresh was not bounded to one attempt")
			}
			if dataCalls.Load() > 2 {
				t.Fatal("unbounded original request retries")
			}
			access, refresh := "access-0", "refresh-0"
			if test.noRefresh {
				refresh = ""
			}
			if test.retryUnauthorized {
				access, refresh = "access-1", "refresh-1"
			}
			if test.success {
				access = "access-1"
			}
			assertStoredTokens(t, a, account.ID, access, refresh)
		})
	}
}

func TestPasswordLoginStoresRefreshToken(t *testing.T) {
	a := testApp(t)
	var loginCalls, refreshCalls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/v1/auth/login":
			loginCalls.Add(1)
			fmt.Fprint(w, `{"data":{"access_token":"login-access","refresh_token":"login-refresh"}}`)
		case "/api/v1/auth/refresh":
			refreshCalls.Add(1)
			w.WriteHeader(401)
		case "/api/v1/auth/me":
			if r.Header.Get("Authorization") != "Bearer login-access" {
				w.WriteHeader(401)
				return
			}
			fmt.Fprint(w, `{"data":{"balance":25}}`)
		case "/api/v1/usage/dashboard/stats":
			fmt.Fprint(w, `{"data":{"total_actual_cost":12,"today_actual_cost":2}}`)
		default:
			fmt.Fprint(w, `{"data":[]}`)
		}
	}))
	defer server.Close()
	account := tokenTestAccount(t, a, server.URL)
	account.AuthMode, account.Username, account.Password = "password", "user@example.com", a.seal("test-password")
	if err := a.DB.Save(&account).Error; err != nil {
		t.Fatal(err)
	}
	collected, err := a.checkAccount(context.Background(), account.ID)
	if err != nil || collected.Status != "ok" || loginCalls.Load() != 1 || refreshCalls.Load() != 1 {
		t.Fatalf("password fallback failed %v", err)
	}
	assertStoredTokens(t, a, account.ID, "login-access", "login-refresh")
}

func TestRotatedTokensSurviveCancelledCollection(t *testing.T) {
	a := testApp(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/api/v1/auth/refresh" {
			fmt.Fprint(w, `{"data":{"access_token":"access-1","refresh_token":"refresh-1"}}`)
			return
		}
		if r.Header.Get("Authorization") == "Bearer access-0" {
			w.WriteHeader(401)
			return
		}
		cancel()
		fmt.Fprint(w, `{"data":{"balance":25}}`)
	}))
	defer server.Close()
	account := tokenTestAccount(t, a, server.URL)
	if _, err := a.checkAccount(ctx, account.ID); !errors.Is(err, context.Canceled) {
		t.Fatalf("expected cancellation, got %v", err)
	}
	assertStoredTokens(t, a, account.ID, "access-1", "refresh-1")
}

func TestRefreshDoesNotOverwriteConcurrentAccountEdit(t *testing.T) {
	a := testApp(t)
	refreshEntered, releaseRefresh := make(chan struct{}), make(chan struct{})
	var releaseOnce sync.Once
	release := func() { releaseOnce.Do(func() { close(releaseRefresh) }) }
	defer release()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path != "/api/v1/auth/refresh" {
			w.WriteHeader(401)
			return
		}
		close(refreshEntered)
		select {
		case <-releaseRefresh:
		case <-r.Context().Done():
			return
		}
		fmt.Fprint(w, `{"data":{"access_token":"access-rotated","refresh_token":"refresh-rotated"}}`)
	}))
	defer server.Close()
	defer release()
	account := tokenTestAccount(t, a, server.URL)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() { _, err := a.checkAccount(ctx, account.ID); done <- err }()
	select {
	case <-refreshEntered:
	case <-ctx.Done():
		t.Fatal("refresh did not start")
	}
	h := a.Router()
	cookie := loginForTest(t, h)
	input := accountInput{Name: "edited", Kind: "sub2api", BaseURL: server.URL, AuthMode: "token", Token: "access-manual", RefreshToken: "refresh-manual", Currency: "USD", QuotaPerUnit: 500000, IntervalSeconds: 300}
	response := call(h, "PUT", "/api/accounts/1", jsonString(input), cookie)
	if response.Code != 200 {
		t.Fatal(response.Body.String())
	}
	release()
	if err := <-done; err == nil {
		t.Fatal("stale collection overwrote user credentials")
	}
	assertStoredTokens(t, a, account.ID, "access-manual", "refresh-manual")
}

func TestLegacyAccountRefreshMigration(t *testing.T) {
	dir := t.TempDir()
	a, err := Open(Config{DataDir: dir, AdminPassword: "test-password-12345"})
	if err != nil {
		t.Fatal(err)
	}
	account := tokenTestAccount(t, a, "http://127.0.0.1:1")
	if err := a.DB.Exec("ALTER TABLE accounts DROP COLUMN refresh_token").Error; err != nil {
		a.Close()
		t.Fatal(err)
	}
	if err := a.Close(); err != nil {
		t.Fatal(err)
	}
	a, err = Open(Config{DataDir: dir, AdminPassword: "test-password-12345"})
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	assertStoredTokens(t, a, account.ID, "access-0", "")
}
