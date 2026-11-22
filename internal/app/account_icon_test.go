package app

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

const testAccountSVG = `<svg xmlns="http://www.w3.org/2000/svg" width="32" height="32"><rect width="32" height="32" fill="#2463eb"/></svg>`

func TestAccountIconDeclaredCacheAndURLChange(t *testing.T) {
	a := testApp(t)
	var calls atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.Header.Get("Authorization") != "" || r.Header.Get("Cookie") != "" || r.Header.Get("New-Api-User") != "" || r.Header.Get("Referer") != "" {
			t.Error("credentials sent with public icon request")
		}
		switch r.URL.Path {
		case "/":
			fmt.Fprint(w, `<link rel="apple-touch-icon" href="/touch.png"><link rel="shortcut ICON" href="/brand.svg?a=1&amp;b=2">`)
		case "/brand.svg":
			if r.URL.Query().Get("b") != "2" {
				t.Error("HTML icon URL was not decoded")
			}
			fmt.Fprint(w, testAccountSVG)
		default:
			t.Error("declared favicon should take precedence", r.URL)
			w.WriteHeader(404)
		}
	}))
	defer upstream.Close()
	account := tokenTestAccount(t, a, upstream.URL+"/v1")
	h := a.Router()
	path := fmt.Sprintf("/api/accounts/%d/icon", account.ID)
	if response := call(h, "GET", path, "", ""); response.Code != 401 || calls.Load() != 0 {
		t.Fatal("unauthenticated icon request accepted")
	}
	cookie := loginForTest(t, h)
	for i := 0; i < 2; i++ {
		response := call(h, "GET", path, "", cookie)
		if response.Code != 200 || response.Body.String() != testAccountSVG || response.Header().Get("Content-Type") != "image/svg+xml" || response.Header().Get("Cache-Control") != "private, max-age=86400" || !strings.Contains(response.Header().Get("Content-Security-Policy"), "sandbox") {
			t.Fatal("declared icon response incorrect", response.Code, response.Body.String())
		}
	}
	if calls.Load() != 2 {
		t.Fatal("cache repeated upstream requests", calls.Load())
	}
	a.mu.Lock()
	a.accountIcons[account.ID].expires = time.Now().Add(-time.Second)
	a.mu.Unlock()
	if response := call(h, "GET", path, "", cookie); response.Code != 200 || calls.Load() != 4 {
		t.Fatal("expired icon was not refreshed")
	}
	other := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/" {
			fmt.Fprint(w, "no declared icon")
		} else {
			fmt.Fprint(w, strings.ReplaceAll(testAccountSVG, "2463eb", "ff0000"))
		}
	}))
	defer other.Close()
	if err := a.DB.Model(&account).Update("base_url", other.URL).Error; err != nil {
		t.Fatal(err)
	}
	if response := call(h, "GET", path, "", cookie); response.Code != 200 || !strings.Contains(response.Body.String(), "ff0000") {
		t.Fatal("changed upstream reused old icon", response.Body.String())
	}
	if response := call(h, "GET", "/api/accounts/999999/icon", "", cookie); response.Code != 404 {
		t.Fatal("unknown account accepted")
	}
}

func TestAccountIconFallbackAndNegativeCache(t *testing.T) {
	for _, mode := range []string{"homepage-error", "missing", "html", "oversized", "none"} {
		t.Run(mode, func(t *testing.T) {
			a := testApp(t)
			var calls atomic.Int32
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				switch r.URL.Path {
				case "/":
					if mode == "homepage-error" {
						w.WriteHeader(500)
					} else {
						fmt.Fprint(w, `<link rel="icon" href="/logo">`)
					}
				case "/logo":
					switch mode {
					case "html":
						w.Header().Set("Content-Type", "image/png")
						fmt.Fprint(w, `<html><body>login required</body></html>`)
					case "oversized":
						fmt.Fprint(w, testAccountSVG+strings.Repeat(" ", accountIconLimit))
					default:
						w.WriteHeader(404)
					}
				case "/favicon.ico":
					if mode == "none" {
						w.WriteHeader(404)
					} else {
						fmt.Fprint(w, testAccountSVG)
					}
				}
			}))
			defer upstream.Close()
			account := tokenTestAccount(t, a, upstream.URL)
			h := a.Router()
			cookie := loginForTest(t, h)
			response := call(h, "GET", fmt.Sprintf("/api/accounts/%d/icon", account.ID), "", cookie)
			if mode == "none" {
				if response.Code != 404 || response.Header().Get("Cache-Control") != "private, max-age=3600" {
					t.Fatal("missing icon was not negatively cached")
				}
			} else if response.Code != 200 || response.Body.String() != testAccountSVG {
				t.Fatal("favicon fallback failed", response.Code, response.Body.String())
			}
			before := calls.Load()
			call(h, "GET", fmt.Sprintf("/api/accounts/%d/icon", account.ID), "", cookie)
			if calls.Load() != before {
				t.Fatal("cached result fetched again")
			}
		})
	}
}

func TestAccountIconHomeLogo(t *testing.T) {
	a := testApp(t)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/":
			fmt.Fprint(w, `<img src="/qr.png" alt="contact QR"><img src="/shop/logo.svg" alt="上游 Logo">`)
		case "/shop/logo.svg":
			fmt.Fprint(w, testAccountSVG)
		default:
			t.Error("non-logo image fetched", r.URL)
			w.WriteHeader(404)
		}
	}))
	defer upstream.Close()
	body, _ := a.cachedAccountIcon(context.Background(), 1, upstream.URL+"/", a.client)
	if string(body) != testAccountSVG {
		t.Fatal("explicit homepage logo was not discovered")
	}
}

func TestAccountIconRejectsExternalLinksAndRedirects(t *testing.T) {
	a := testApp(t)
	var externalCalls atomic.Int32
	external := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		externalCalls.Add(1)
		fmt.Fprint(w, testAccountSVG)
	}))
	defer external.Close()
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/":
			fmt.Fprintf(w, `<link rel="icon" href="%s/private"><link rel="icon" href="/redirect">`, external.URL)
		case "/redirect":
			http.Redirect(w, r, external.URL+"/private", 302)
		case "/favicon.ico":
			fmt.Fprint(w, testAccountSVG)
		}
	}))
	defer upstream.Close()
	body, _ := a.cachedAccountIcon(context.Background(), 1, upstream.URL+"/", a.client)
	if string(body) != testAccountSVG || externalCalls.Load() != 0 {
		t.Fatal("icon discovery followed an external URL")
	}
}

func TestAccountIconConcurrentCacheAndCancellation(t *testing.T) {
	a := testApp(t)
	var calls atomic.Int32
	started, release := make(chan struct{}), make(chan struct{})
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if calls.Add(1) == 1 {
			close(started)
			select {
			case <-release:
			case <-r.Context().Done():
				return
			}
		}
		if r.URL.Path == "/favicon.ico" {
			fmt.Fprint(w, testAccountSVG)
		}
	}))
	defer upstream.Close()
	var workers sync.WaitGroup
	for i := 0; i < 12; i++ {
		workers.Go(func() {
			body, _ := a.cachedAccountIcon(context.Background(), 1, upstream.URL+"/", a.client)
			if string(body) != testAccountSVG {
				t.Error("concurrent icon response missing")
			}
		})
	}
	select {
	case <-started:
	case <-time.After(3 * time.Second):
		t.Fatal("icon request did not start")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if body, _ := a.cachedAccountIcon(ctx, 1, upstream.URL+"/", a.client); len(body) != 0 {
		t.Error("cancelled cache wait did not stop")
	}
	close(release)
	workers.Wait()
	if calls.Load() != 2 {
		t.Fatal("concurrent requests duplicated icon fetching", calls.Load())
	}
}
