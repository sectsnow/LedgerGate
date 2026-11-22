package app

import (
	"bufio"
	"context"
	"crypto/x509"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func setTestProxy(t *testing.T, a *App, address string) {
	t.Helper()
	if err := a.DB.Model(&Setting{}).Where("id = 1").Updates(map[string]any{"proxy_enabled": true, "proxy_url": a.seal(address)}).Error; err != nil {
		t.Fatal(err)
	}
}

func TestProxySettingsValidationAndSecrets(t *testing.T) {
	a := testApp(t)
	h := a.Router()
	if response := call(h, "GET", "/api/proxy", "", ""); response.Code != 401 {
		t.Fatal("proxy settings exposed without login")
	}
	cookie := loginForTest(t, h)
	req := httptest.NewRequest("PUT", "/api/proxy", strings.NewReader(`{}`))
	req.Header.Set("Cookie", cookie)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != 403 {
		t.Fatal("proxy mutation accepted without CSRF header")
	}
	input := proxyInput{Enabled: true, Protocol: "http", Host: "127.0.0.1", Port: 7890, Username: "proxy-user", Password: "proxy-secret"}
	response := call(h, "PUT", "/api/proxy", jsonString(input), cookie)
	if response.Code != 200 || strings.Contains(response.Body.String(), input.Password) || !strings.Contains(response.Body.String(), `"has_password":true`) {
		t.Fatal("proxy save or credential redaction failed")
	}
	var saved Setting
	a.DB.First(&saved, 1)
	if saved.ProxyURL == "" || strings.Contains(saved.ProxyURL, input.Password) {
		t.Fatal("proxy secret not encrypted")
	}
	for _, path := range []string{"/api/proxy", "/api/settings"} {
		response = call(h, "GET", path, "", cookie)
		if response.Code != 200 || strings.Contains(response.Body.String(), input.Password) || strings.Contains(response.Body.String(), saved.ProxyURL) {
			t.Fatal("proxy credential exposed in settings response")
		}
	}
	input.Password = ""
	input.Port = 7891
	if call(h, "PUT", "/api/proxy", jsonString(input), cookie).Code != 200 {
		t.Fatal("could not preserve proxy password")
	}
	_, address, err := a.readProxy()
	if password, _ := address.User.Password(); err != nil || password != "proxy-secret" || address.Port() != "7891" {
		t.Fatal("blank password did not preserve stored password")
	}
	response = call(h, "PUT", "/api/settings", `{"alert_cooldown_minutes":60,"retention_days":30}`, cookie)
	_, address, err = a.readProxy()
	if response.Code != 200 || err != nil || address.Port() != "7891" {
		t.Fatal("ordinary settings overwrote proxy settings")
	}
	input.ClearPassword = true
	response = call(h, "PUT", "/api/proxy", jsonString(input), cookie)
	if response.Code != 200 || !strings.Contains(response.Body.String(), `"has_password":false`) {
		t.Fatal("explicit password clear failed")
	}
	for _, invalid := range []proxyInput{
		{Enabled: true, Protocol: "ftp", Host: "localhost", Port: 80},
		{Enabled: true, Protocol: "http", Host: "http://localhost", Port: 80},
		{Enabled: true, Protocol: "http", Host: "localhost", Port: 65536},
		{Enabled: true, Protocol: "socks5", Host: "localhost", Port: 80, Password: "no-user"},
		{Enabled: true, Protocol: "http", Host: "localhost", Port: 80, Username: "bad\nuser"},
	} {
		if call(h, "PUT", "/api/proxy", jsonString(invalid), cookie).Code != 400 {
			t.Fatal("invalid proxy input was accepted")
		}
	}
	for _, raw := range []string{"http://user:pass@127.0.0.1:7890", "https://localhost:443", "socks5://[::1]:1080"} {
		if _, err := parseProxyAddress(raw); err != nil {
			t.Fatal("valid proxy rejected", err)
		}
	}
}

func TestProxyAccountRefreshModelsTestingAndGateway(t *testing.T) {
	a := testApp(t)
	var proxyCalls, refreshCalls atomic.Int32
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		proxyCalls.Add(1)
		if !r.URL.IsAbs() || r.Header.Get("Proxy-Authorization") != "Basic "+base64.StdEncoding.EncodeToString([]byte("proxy-user:proxy-secret")) {
			t.Error("request bypassed HTTP proxy authentication")
		}
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/v1/auth/refresh":
			refreshCalls.Add(1)
			fmt.Fprint(w, `{"data":{"access_token":"access-1","refresh_token":"refresh-1"}}`)
		case "/api/v1/auth/me":
			if r.Header.Get("Authorization") == "Bearer access-0" {
				w.WriteHeader(401)
				return
			}
			fmt.Fprint(w, `{"data":{"balance":25}}`)
		case "/":
			fmt.Fprint(w, `<link rel="icon" href="/favicon.ico">`)
		case "/favicon.ico":
			if r.Header.Get("Authorization") != "" {
				t.Error("account credentials sent to icon endpoint")
			}
			fmt.Fprint(w, `<svg xmlns="http://www.w3.org/2000/svg"></svg>`)
		case "/v1/models":
			fmt.Fprint(w, `{"data":[{"id":"upstream-model"}]}`)
		case "/v1/chat/completions":
			var body object
			_ = json.NewDecoder(r.Body).Decode(&body)
			if body["stream"] == true {
				w.Header().Set("Content-Type", "text/event-stream")
				fmt.Fprint(w, "data: {\"id\":\"proxy-test\",\"choices\":[{\"delta\":{\"content\":\"OK\"},\"finish_reason\":null}]}\n\ndata: {\"choices\":[{\"delta\":{},\"finish_reason\":\"stop\"}],\"usage\":{\"prompt_tokens\":1,\"completion_tokens\":1}}\n\ndata: [DONE]\n\n")
			} else {
				chatResponse(w)
			}
		default:
			t.Error("unexpected proxy endpoint", r.URL.Path)
			w.WriteHeader(404)
		}
	}))
	defer proxy.Close()
	address, _ := url.Parse(proxy.URL)
	address.User = url.UserPassword("proxy-user", "proxy-secret")
	setTestProxy(t, a, address.String())
	account := tokenTestAccount(t, a, "http://provider.invalid")
	if err := a.DB.Model(&account).Update("use_proxy", true).Error; err != nil {
		t.Fatal(err)
	}
	account.UseProxy = true
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, _, err := a.accountRequest(ctx, &account, "GET", "/api/v1/auth/me", nil); err != nil || refreshCalls.Load() != 1 {
		t.Fatal("account refresh did not use proxy", err)
	}
	assertStoredTokens(t, a, account.ID, "access-1", "refresh-1")
	h := a.Router()
	cookie := loginForTest(t, h)
	channel := createChannel(t, h, cookie, "proxied", "http://provider.invalid", "openai")
	channel.UseProxy = true
	if err := a.DB.Model(&channel).Update("use_proxy", true).Error; err != nil {
		t.Fatal(err)
	}
	if err := a.DB.First(&channel, channel.ID).Error; err != nil {
		t.Fatal(err)
	}
	if models, err := a.syncChannelModels(ctx, channel); err != nil || len(models) != 1 {
		t.Fatal("model discovery did not use proxy", err)
	}
	result := runModelTest(t, h, cookie, channel, object{"model": "upstream-model", "protocol": "openai", "prompt": "OK?", "max_tokens": 10, "stream": true})
	if !result.Success || result.Text != "OK" {
		t.Fatal("model test did not use proxy", result.Error)
	}
	key := createGroup(t, h, cookie, []Channel{channel}, "priority")
	response := call(h, "POST", "/v1/chat/completions", `{"model":"my-model","messages":[{"role":"user","content":"OK?"}],"stream":true}`, key)
	if response.Code != 200 || !strings.Contains(response.Body.String(), "[DONE]") || proxyCalls.Load() < 6 {
		t.Fatal("stream gateway did not use proxy", response.Code, response.Body.String())
	}
	response = call(h, "GET", fmt.Sprintf("/api/accounts/%d/icon", account.ID), "", cookie)
	if response.Code != 200 || !strings.Contains(response.Body.String(), "<svg") {
		t.Fatal("account icon did not use proxy")
	}
}

func TestProxySelectionFailureAndClientRotation(t *testing.T) {
	a := testApp(t)
	var directCalls atomic.Int32
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { directCalls.Add(1); fmt.Fprint(w, "direct") }))
	defer target.Close()
	started, finish := make(chan struct{}), make(chan struct{})
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		close(started)
		w.(http.Flusher).Flush()
		<-finish
		fmt.Fprint(w, "proxied")
	}))
	defer proxy.Close()
	setTestProxy(t, a, proxy.URL)
	client, err := a.httpClient(true)
	if err != nil {
		t.Fatal(err)
	}
	response, err := client.Get(target.URL)
	if err != nil {
		t.Fatal(err)
	}
	<-started
	setTestProxy(t, a, "http://127.0.0.1:1")
	next, err := a.httpClient(true)
	close(finish)
	raw, readErr := io.ReadAll(response.Body)
	response.Body.Close()
	if err != nil || next == client || readErr != nil || string(raw) != "proxied" {
		t.Fatal("client rotation interrupted in-flight response")
	}
	req, _ := http.NewRequest("GET", target.URL, nil)
	if _, err := a.doRequest(req, true); err == nil || !strings.Contains(err.Error(), "代理") || directCalls.Load() != 0 {
		t.Fatal("proxy failure fell back to direct")
	}
	resp, err := a.doRequest(req, false)
	if err != nil {
		t.Fatal("direct object did not bypass global proxy")
	}
	resp.Body.Close()
	if err := a.DB.Model(&Setting{}).Where("id = 1").Update("proxy_enabled", false).Error; err != nil {
		t.Fatal(err)
	}
	resp, err = a.doRequest(req, true)
	if err != nil {
		t.Fatal("global off did not select direct transport")
	}
	resp.Body.Close()
	if directCalls.Load() != 2 {
		t.Fatal("unexpected direct requests")
	}
	var wg sync.WaitGroup
	for i := 0; i < 12; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := a.httpClient(true); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
}

func TestProxyHTTPTunnelAndHTTPSProxyTLS(t *testing.T) {
	for _, secureProxy := range []bool{false, true} {
		t.Run(strconv.FormatBool(secureProxy), func(t *testing.T) {
			a := testApp(t)
			target := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Header.Get("Proxy-Authorization") != "" {
					t.Error("proxy authentication leaked to HTTPS target")
				}
				fmt.Fprint(w, "tunnel-ok")
			}))
			defer target.Close()
			targetURL, _ := url.Parse(target.URL)
			var tunnels atomic.Int32
			handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != "CONNECT" || r.Host != targetURL.Host || r.Header.Get("Proxy-Authorization") != "Basic "+base64.StdEncoding.EncodeToString([]byte("u:p")) {
					w.WriteHeader(407)
					return
				}
				upstream, err := net.DialTimeout("tcp", r.Host, time.Second)
				if err != nil {
					t.Error(err)
					w.WriteHeader(502)
					return
				}
				defer upstream.Close()
				conn, rw, err := w.(http.Hijacker).Hijack()
				if err != nil {
					t.Error(err)
					return
				}
				defer conn.Close()
				tunnels.Add(1)
				fmt.Fprint(rw, "HTTP/1.1 200 Connection Established\r\n\r\n")
				_ = rw.Flush()
				go func() { _, _ = io.Copy(upstream, rw); upstream.Close() }()
				_, _ = io.Copy(conn, upstream)
			})
			var proxy *httptest.Server
			if secureProxy {
				proxy = httptest.NewTLSServer(handler)
			} else {
				proxy = httptest.NewServer(handler)
			}
			defer proxy.Close()
			address, _ := url.Parse(proxy.URL)
			address.User = url.UserPassword("u", "p")
			setTestProxy(t, a, address.String())
			req, _ := http.NewRequest("GET", target.URL, nil)
			if secureProxy {
				if _, err := a.doRequest(req, true); err == nil {
					t.Fatal("untrusted HTTPS proxy certificate accepted")
				}
			}
			roots := x509.NewCertPool()
			roots.AddCert(target.Certificate())
			if secureProxy {
				roots.AddCert(proxy.Certificate())
			}
			a.client.Transport.(*http.Transport).TLSClientConfig = target.Client().Transport.(*http.Transport).TLSClientConfig.Clone()
			a.client.Transport.(*http.Transport).TLSClientConfig.InsecureSkipVerify = false
			a.client.Transport.(*http.Transport).TLSClientConfig.RootCAs = roots
			// TLS 信任只注入测试夹具，重建缓存后继续验证完整证书链。
			a.proxyMu.Lock()
			if a.proxyClient != nil {
				a.proxyClient.CloseIdleConnections()
			}
			a.proxyClient = nil
			a.proxyMu.Unlock()
			resp, err := a.doRequest(req, true)
			if err != nil {
				t.Fatal(err)
			}
			raw, err := io.ReadAll(resp.Body)
			resp.Body.Close()
			if err != nil || string(raw) != "tunnel-ok" || tunnels.Load() != 1 {
				t.Fatal("CONNECT tunnel failed")
			}
		})
	}
}

func TestProxySOCKS5AuthenticationAndRemoteDNS(t *testing.T) {
	a := testApp(t)
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	done := make(chan error, 1)
	go func() {
		done <- func() error {
			conn, err := listener.Accept()
			if err != nil {
				return err
			}
			defer conn.Close()
			_ = conn.SetDeadline(time.Now().Add(5 * time.Second))
			r := bufio.NewReader(conn)
			header := make([]byte, 2)
			if _, err := io.ReadFull(r, header); err != nil || header[0] != 5 {
				return fmt.Errorf("bad SOCKS5 greeting")
			}
			methods := make([]byte, int(header[1]))
			if _, err := io.ReadFull(r, methods); err != nil {
				return err
			}
			if _, err := conn.Write([]byte{5, 2}); err != nil {
				return err
			}
			if _, err := io.ReadFull(r, header); err != nil || header[0] != 1 {
				return fmt.Errorf("bad auth version")
			}
			username := make([]byte, int(header[1]))
			if _, err := io.ReadFull(r, username); err != nil {
				return err
			}
			n, err := r.ReadByte()
			if err != nil {
				return err
			}
			password := make([]byte, int(n))
			if _, err := io.ReadFull(r, password); err != nil {
				return err
			}
			if string(username) != "socks-user" || string(password) != "socks-pass" {
				return fmt.Errorf("wrong SOCKS5 credentials")
			}
			if _, err := conn.Write([]byte{1, 0}); err != nil {
				return err
			}
			request := make([]byte, 4)
			if _, err := io.ReadFull(r, request); err != nil || request[1] != 1 || request[3] != 3 {
				return fmt.Errorf("domain was not delegated to proxy")
			}
			n, err = r.ReadByte()
			if err != nil {
				return err
			}
			domain := make([]byte, int(n))
			if _, err := io.ReadFull(r, domain); err != nil {
				return err
			}
			port := make([]byte, 2)
			if _, err := io.ReadFull(r, port); err != nil {
				return err
			}
			if string(domain) != "socks-target.invalid" || binary.BigEndian.Uint16(port) != 80 {
				return fmt.Errorf("wrong SOCKS5 destination")
			}
			if _, err := conn.Write([]byte{5, 0, 0, 1, 127, 0, 0, 1, 0, 80}); err != nil {
				return err
			}
			req, err := http.ReadRequest(r)
			if err != nil {
				return err
			}
			if req.Header.Get("Proxy-Authorization") != "" {
				return fmt.Errorf("proxy auth leaked to SOCKS target")
			}
			_, err = io.WriteString(conn, "HTTP/1.1 200 OK\r\nContent-Length: 8\r\nConnection: close\r\n\r\nsocks-ok")
			return err
		}()
	}()
	setTestProxy(t, a, "socks5://socks-user:socks-pass@"+listener.Addr().String())
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, "GET", "http://socks-target.invalid", nil)
	resp, err := a.doRequest(req, true)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := io.ReadAll(resp.Body)
	resp.Body.Close()
	if err != nil || string(raw) != "socks-ok" {
		t.Fatal("SOCKS5 request failed")
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

func TestProxyDraftTestDoesNotSave(t *testing.T) {
	a := testApp(t)
	h := a.Router()
	cookie := loginForTest(t, h)
	var seen atomic.Int32
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen.Add(1)
		if r.Header.Get("Authorization") != "" || r.Header.Get("Cookie") != "" {
			t.Error("test sent account credentials")
		}
		w.WriteHeader(204)
	}))
	defer proxy.Close()
	u, _ := url.Parse(proxy.URL)
	port, _ := strconv.Atoi(u.Port())
	input := proxyInput{Protocol: "http", Host: u.Hostname(), Port: port, TargetURL: "https://user:secret@example.com"}
	if call(h, "POST", "/api/proxy/test", jsonString(input), cookie).Code != 400 || seen.Load() != 0 {
		t.Fatal("test accepted credentials in target URL")
	}
	input.TargetURL = "http://test-target.invalid"
	response := call(h, "POST", "/api/proxy/test", jsonString(input), cookie)
	if response.Code != 200 || !strings.Contains(response.Body.String(), `"status":204`) || seen.Load() != 1 {
		t.Fatal("draft proxy test failed")
	}
	setting, address, err := a.readProxy()
	if err != nil || setting.ProxyEnabled || address != nil {
		t.Fatal("test persisted draft proxy settings")
	}
}
