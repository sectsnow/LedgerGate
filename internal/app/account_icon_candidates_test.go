package app

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
)

func TestPublicIconAddress(t *testing.T) {
	for _, value := range []string{"8.8.8.8", "1.1.1.1", "2606:4700:4700::1111"} {
		if !publicIconAddress(net.ParseIP(value)) {
			t.Fatal("public address rejected", value)
		}
	}
	for _, value := range []string{"127.0.0.1", "10.0.0.1", "192.168.1.1", "172.16.0.1", "169.254.169.254", "100.100.100.200", "198.18.0.1", "0.1.2.3", "::1", "::ffff:127.0.0.1", "fd00::1", "fe80::1", "224.0.0.1", "invalid"} {
		if publicIconAddress(net.ParseIP(value)) {
			t.Fatal("private or reserved address accepted", value)
		}
	}
}

func TestAccountIconCandidatesPreviewCDNAndNoReplacement(t *testing.T) {
	a := testApp(t)
	var calls, cdnCalls atomic.Int32
	var privateDial atomic.Bool
	var mode atomic.Int32
	checkHeaders := func(r *http.Request) {
		for _, name := range []string{"Authorization", "Cookie", "New-Api-User", "Referer"} {
			if r.Header.Get(name) != "" {
				t.Error("credentials leaked to icon source", name)
			}
		}
	}
	cdn := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		cdnCalls.Add(1)
		checkHeaders(r)
		if r.Host != "8.8.8.8" || r.URL.Query().Get("sig") != "keep" {
			t.Error("CDN Host or query was lost", r.Host, r.URL)
		}
		fmt.Fprint(w, strings.ReplaceAll(testAccountSVG, "2463eb", "00ff00"))
	}))
	defer cdn.Close()
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		checkHeaders(r)
		if mode.Load() == 2 {
			w.WriteHeader(404)
			return
		}
		switch r.URL.Path {
		case "/":
			if mode.Load() == 1 {
				w.WriteHeader(503)
				return
			}
			fmt.Fprint(w, `<link rel="icon" href="/one.svg?theme=light"><link rel="icon" href="/one.svg?theme=light"><link rel="icon" href="/invalid"><link rel="icon" href="http://127.0.0.2/secret"><link rel="apple-touch-icon" href="/touch.svg"><img alt="Website Logo" src="http://8.8.8.8/cdn.svg?sig=keep">`)
		case "/touch.svg":
			fmt.Fprint(w, strings.ReplaceAll(testAccountSVG, "2463eb", "ffff00"))
		case "/invalid":
			fmt.Fprint(w, "<html>not an image</html>")
		default:
			fmt.Fprint(w, testAccountSVG)
		}
	}))
	defer upstream.Close()
	transport := a.client.Transport.(*http.Transport).Clone()
	dial := transport.DialContext
	transport.DialContext = func(ctx context.Context, network, address string) (net.Conn, error) {
		if strings.HasPrefix(address, "8.8.8.8:") {
			address = cdn.Listener.Addr().String()
		}
		if strings.HasPrefix(address, "127.0.0.2:") {
			privateDial.Store(true)
		}
		return dial(ctx, network, address)
	}
	a.client.Transport = transport
	account := tokenTestAccount(t, a, upstream.URL)
	h := a.Router()
	cookie := loginForTest(t, h)
	manual := strings.ReplaceAll(testAccountSVG, "2463eb", "ff0000")
	if w := uploadIconForTest(t, h, account.ID, cookie, []byte(manual), true); w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	path := fmt.Sprintf("/api/accounts/%d/icon/candidates", account.ID)
	if w := call(h, "POST", path, "", ""); w.Code != 401 || calls.Load() != 0 {
		t.Fatal("candidate endpoint bypassed authentication")
	}
	req := httptest.NewRequest("POST", path, nil)
	req.Header.Set("Cookie", cookie)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != 403 || calls.Load() != 0 {
		t.Fatal("candidate endpoint bypassed CSRF protection")
	}
	before := transferDBState(t, a)
	w = call(h, "POST", path, "", cookie)
	var result struct {
		Items []accountIconCandidate `json:"items"`
	}
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &result) != nil || len(result.Items) != 3 {
		t.Fatal("candidate discovery, deduplication or CDN fetch failed", w.Code, w.Body.String())
	}
	if transferDBState(t, a) != before {
		t.Fatal("preview modified the saved logo or account")
	}
	if cdnCalls.Load() != 1 || privateDial.Load() {
		t.Fatal("CDN was missed or a private address was contacted")
	}
	for _, item := range result.Items {
		if len(item.Body) == 0 || item.TypeOf != "image/svg+xml" {
			t.Fatal("invalid candidate returned")
		}
	}
	if w := call(h, "GET", fmt.Sprintf("/api/accounts/%d/icon", account.ID), "", cookie); w.Body.String() != manual {
		t.Fatal("preview replaced the current logo")
	}
	mode.Store(1)
	w = call(h, "POST", path, "", cookie)
	if json.Unmarshal(w.Body.Bytes(), &result) != nil || len(result.Items) != 1 {
		t.Fatal("favicon fallback failed", w.Body.String())
	}
	mode.Store(2)
	w = call(h, "POST", path, "", cookie)
	if json.Unmarshal(w.Body.Bytes(), &result) != nil || len(result.Items) != 0 || transferDBState(t, a) != before {
		t.Fatal("failed discovery modified the current logo")
	}
}

func TestAccountIconCandidateRedirectValidationAndProxy(t *testing.T) {
	var unexpected atomic.Bool
	cdn := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, "http://127.0.0.2/private", 302) }))
	defer cdn.Close()
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = nil
	dial := (&net.Dialer{}).DialContext
	transport.DialContext = func(ctx context.Context, network, address string) (net.Conn, error) {
		if strings.HasPrefix(address, "8.8.8.8:") {
			return dial(ctx, network, cdn.Listener.Addr().String())
		}
		unexpected.Store(true)
		return dial(ctx, network, address)
	}
	base, _ := url.Parse("https://upstream.example/")
	client := &http.Client{Transport: transport}
	for _, target := range []string{"http://8.8.8.8/redirect", "http://127.0.0.2/private", "http://user:secret@8.8.8.8/logo", "file:///etc/passwd"} {
		if body, _ := readAccountIconResource(context.Background(), target, base, client, accountIconDownloadLimit, true); len(body) > 0 {
			t.Fatal("unsafe icon source accepted", target)
		}
	}
	if unexpected.Load() {
		t.Fatal("redirect escaped address validation")
	}
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "CONNECT" || r.Host != "8.8.4.4:80" {
			t.Error("proxy target was not pinned", r.Method, r.Host)
		}
		conn, _, err := w.(http.Hijacker).Hijack()
		if err != nil {
			t.Error(err)
			return
		}
		defer conn.Close()
		fmt.Fprint(conn, "HTTP/1.1 200 Connection Established\r\n\r\n")
		inner, err := http.ReadRequest(bufio.NewReader(conn))
		if err != nil {
			t.Error(err)
			return
		}
		if inner.Host != "8.8.4.4" || inner.URL.Path != "/logo.svg" || inner.Header.Get("Proxy-Authorization") != "" {
			t.Error("tunneled request lost Host or leaked proxy credentials", inner.Host)
		}
		response := &http.Response{StatusCode: 200, ProtoMajor: 1, ProtoMinor: 1, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(testAccountSVG)), ContentLength: int64(len(testAccountSVG))}
		if err := response.Write(conn); err != nil {
			t.Error(err)
		}
	})
	proxy := httptest.NewServer(handler)
	defer proxy.Close()
	address, _ := url.Parse(proxy.URL)
	proxied := http.DefaultTransport.(*http.Transport).Clone()
	proxied.Proxy = http.ProxyURL(address)
	body, _ := readAccountIconResource(context.Background(), "http://8.8.4.4/logo.svg", base, &http.Client{Transport: proxied}, accountIconDownloadLimit, true)
	if string(body) != testAccountSVG {
		t.Fatal("CDN fetch ignored the configured proxy")
	}
	secureProxy := httptest.NewTLSServer(handler)
	defer secureProxy.Close()
	address, _ = url.Parse(secureProxy.URL)
	secure := secureProxy.Client().Transport.(*http.Transport).Clone()
	secure.Proxy = http.ProxyURL(address)
	body, _ = readAccountIconResource(context.Background(), "http://8.8.4.4/logo.svg", base, &http.Client{Transport: secure}, accountIconDownloadLimit, true)
	if string(body) != testAccountSVG {
		t.Fatal("HTTPS proxy certificate was checked against the CDN name")
	}
}
