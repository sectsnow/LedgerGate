package app

import (
	"bufio"
	"context"
	"crypto/tls"
	"encoding/base64"
	"errors"
	"io"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"time"

	"github.com/gin-gonic/gin"
)

type accountIconCandidate struct {
	URL    string `json:"url"`
	Body   []byte `json:"data"`
	TypeOf string `json:"mime"`
}

func publicIconAddress(ip net.IP) bool {
	address, ok := netip.AddrFromSlice(ip)
	if !ok {
		return false
	}
	address = address.Unmap()
	return address.IsGlobalUnicast() && !address.IsPrivate() &&
		!netip.MustParsePrefix("100.64.0.0/10").Contains(address) &&
		!netip.MustParsePrefix("0.0.0.0/8").Contains(address) &&
		!netip.MustParsePrefix("198.18.0.0/15").Contains(address)
}

func readAccountIconResource(ctx context.Context, raw string, base *url.URL, selected *http.Client, limit int64, includeExternal bool) ([]byte, *url.URL) {
	target, err := url.Parse(raw)
	if err != nil {
		return nil, nil
	}
	for hop := 0; hop < 4; hop++ {
		if target.User != nil || target.Hostname() == "" || (target.Scheme != "http" && target.Scheme != "https") {
			return nil, nil
		}
		outbound := *target
		transport := selected.Transport
		if !sameIconOrigin(base, target) {
			if !includeExternal {
				return nil, nil
			}
			addresses, err := net.DefaultResolver.LookupIPAddr(ctx, target.Hostname())
			if err != nil || len(addresses) == 0 {
				return nil, nil
			}
			for _, address := range addresses {
				if !publicIconAddress(address.IP) {
					return nil, nil
				}
			}
			original, ok := selected.Transport.(*http.Transport)
			if !ok {
				return nil, nil
			}
			pinned := original.Clone()
			if pinned.TLSClientConfig == nil {
				pinned.TLSClientConfig = &tls.Config{}
			} else {
				pinned.TLSClientConfig = pinned.TLSClientConfig.Clone()
			}
			pinned.TLSClientConfig.ServerName = target.Hostname()
			defer pinned.CloseIdleConnections()
			transport = pinned
			port := target.Port()
			if port == "" {
				if target.Scheme == "https" {
					port = "443"
				} else {
					port = "80"
				}
			}
			// 固定校验过的公网 IP；直连及代理均保留原 Host 和 TLS 站点名。
			outbound.Host = net.JoinHostPort(addresses[0].IP.String(), port)
			if pinned.Proxy != nil {
				proxy, err := pinned.Proxy(&http.Request{URL: &outbound})
				if err != nil {
					return nil, nil
				}
				if proxy != nil && (proxy.Scheme == "http" || proxy.Scheme == "https") {
					pinned.Proxy, pinned.DialTLSContext = nil, nil
					pinned.DialContext = func(ctx context.Context, network, destination string) (net.Conn, error) {
						return dialAccountIconProxy(ctx, proxy, destination, original)
					}
				}
			}
		}
		request, err := http.NewRequestWithContext(ctx, http.MethodGet, outbound.String(), nil)
		if err != nil {
			return nil, nil
		}
		request.Host = target.Host
		request.Header.Set("User-Agent", "LedgerGate/0.1")
		client := &http.Client{Transport: transport, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
		response, err := client.Do(request)
		if err != nil {
			return nil, nil
		}
		if response.StatusCode >= 300 && response.StatusCode < 400 {
			location := response.Header.Get("Location")
			response.Body.Close()
			if location == "" {
				return nil, nil
			}
			next, err := url.Parse(location)
			if err != nil {
				return nil, nil
			}
			target = target.ResolveReference(next)
			continue
		}
		if response.StatusCode != http.StatusOK {
			response.Body.Close()
			return nil, nil
		}
		body, err := io.ReadAll(io.LimitReader(response.Body, limit+1))
		response.Body.Close()
		if err != nil || int64(len(body)) > limit {
			return nil, nil
		}
		return body, target
	}
	return nil, nil
}

func dialAccountIconProxy(ctx context.Context, proxy *url.URL, destination string, original *http.Transport) (net.Conn, error) {
	dial := original.DialContext
	if dial == nil {
		dial = (&net.Dialer{}).DialContext
	}
	port := proxy.Port()
	if port == "" {
		if proxy.Scheme == "https" {
			port = "443"
		} else {
			port = "80"
		}
	}
	conn, err := dial(ctx, "tcp", net.JoinHostPort(proxy.Hostname(), port))
	if err != nil {
		return nil, err
	}
	connected := false
	defer func() {
		if !connected {
			conn.Close()
		}
	}()
	rawConnection := conn
	stop := context.AfterFunc(ctx, func() { rawConnection.Close() })
	defer stop()
	if deadline, ok := ctx.Deadline(); ok {
		conn.SetDeadline(deadline)
	}
	if proxy.Scheme == "https" {
		config := &tls.Config{}
		if original.TLSClientConfig != nil {
			config = original.TLSClientConfig.Clone()
		}
		config.ServerName, config.NextProtos = proxy.Hostname(), []string{"http/1.1"}
		secured := tls.Client(conn, config)
		if err := secured.HandshakeContext(ctx); err != nil {
			return nil, err
		}
		conn = secured
	}
	// CONNECT 固定目标 IP，隧道内保留源站 Host，避免代理再次解析 CDN 域名。
	request := &http.Request{Method: http.MethodConnect, URL: &url.URL{Opaque: destination}, Host: destination, Header: make(http.Header)}
	if proxy.User != nil {
		password, _ := proxy.User.Password()
		request.Header.Set("Proxy-Authorization", "Basic "+base64.StdEncoding.EncodeToString([]byte(proxy.User.Username()+":"+password)))
	}
	if err := request.Write(conn); err != nil {
		return nil, err
	}
	response, err := http.ReadResponse(bufio.NewReader(io.LimitReader(conn, 64<<10)), request)
	if err != nil {
		return nil, err
	}
	if response.StatusCode != http.StatusOK {
		return nil, errors.New("icon proxy CONNECT failed")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	conn.SetDeadline(time.Time{})
	connected = true
	return conn, nil
}

func (a *App) accountIconCandidates(c *gin.Context) {
	id, ok := pathID(c)
	if !ok {
		return
	}
	var account Account
	if a.DB.WithContext(c.Request.Context()).Select("id", "base_url", "use_proxy").First(&account, id).Error != nil {
		fail(c, 404, "账户不存在")
		return
	}
	origin := accountIconOrigin(account.BaseURL)
	if origin == "" {
		fail(c, 400, "站点地址无效")
		return
	}
	client, err := a.httpClient(account.UseProxy)
	if err != nil {
		fail(c, 400, err.Error())
		return
	}
	items := a.fetchAccountIconCandidates(c.Request.Context(), origin, client, true)
	c.JSON(200, gin.H{"items": items})
}
