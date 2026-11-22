package app

import (
	"bytes"
	"context"
	"encoding/xml"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"golang.org/x/net/html"
)

const accountIconLimit = 512 << 10

type accountIconCache struct {
	origin  string
	client  *http.Client
	ready   chan struct{}
	body    []byte
	typeOf  string
	expires time.Time
}

func (a *App) accountIcon(c *gin.Context) {
	id, ok := pathID(c)
	if !ok {
		return
	}
	var account Account
	if a.DB.WithContext(c.Request.Context()).Select("id", "base_url", "use_proxy").First(&account, id).Error != nil {
		c.Status(http.StatusNotFound)
		return
	}
	base, err := url.Parse(account.BaseURL)
	if err != nil || base.User != nil || base.Hostname() == "" || (base.Scheme != "http" && base.Scheme != "https") {
		c.Status(http.StatusNotFound)
		return
	}
	origin := (&url.URL{Scheme: base.Scheme, Host: base.Host, Path: "/"}).String()
	client, err := a.httpClient(account.UseProxy)
	if err != nil {
		c.Status(http.StatusNotFound)
		return
	}
	body, typeOf := a.cachedAccountIcon(c.Request.Context(), id, origin, client)
	c.Header("Vary", "Cookie")
	if len(body) == 0 {
		c.Header("Cache-Control", "private, max-age=3600")
		c.Status(http.StatusNotFound)
		return
	}
	c.Header("Cache-Control", "private, max-age=86400")
	// 上游 SVG 仅作为图片显示，直接打开也不能执行脚本或加载外部资源。
	c.Header("Content-Security-Policy", "sandbox; default-src 'none'; style-src 'unsafe-inline'")
	c.Data(http.StatusOK, typeOf, body)
}

func (a *App) cachedAccountIcon(ctx context.Context, id uint, origin string, client *http.Client) ([]byte, string) {
	for {
		a.mu.Lock()
		if a.accountIcons == nil {
			a.accountIcons = map[uint]*accountIconCache{}
		}
		if cached := a.accountIcons[id]; cached != nil && cached.origin == origin && cached.client == client {
			if cached.ready != nil {
				ready := cached.ready
				a.mu.Unlock()
				select {
				case <-ctx.Done():
					return nil, ""
				case <-ready:
					continue
				}
			}
			if time.Now().Before(cached.expires) {
				a.mu.Unlock()
				return cached.body, cached.typeOf
			}
		}
		if len(a.accountIcons) >= 64 {
			for key, cached := range a.accountIcons {
				if cached.ready == nil {
					delete(a.accountIcons, key)
					break
				}
			}
			if len(a.accountIcons) >= 64 {
				a.mu.Unlock()
				return nil, ""
			}
		}
		cached := &accountIconCache{origin: origin, client: client, ready: make(chan struct{})}
		a.accountIcons[id] = cached
		a.mu.Unlock()

		body, typeOf := a.fetchAccountIcon(ctx, origin, client)
		ttl := 24 * time.Hour
		if len(body) == 0 {
			ttl = time.Hour
		}
		if ctx.Err() != nil {
			ttl = 0
		}
		a.mu.Lock()
		cached.body, cached.typeOf, cached.expires = body, typeOf, time.Now().Add(ttl)
		close(cached.ready)
		cached.ready = nil
		a.mu.Unlock()
		return body, typeOf
	}
}

func sameIconOrigin(a, b *url.URL) bool {
	return b.User == nil && strings.EqualFold(a.Scheme, b.Scheme) && strings.EqualFold(a.Host, b.Host)
}

func declaredAccountIcons(raw []byte, base *url.URL) []string {
	icons, touchIcons, logos := []string{}, []string{}, []string{}
	tokens := html.NewTokenizer(bytes.NewReader(raw))
	for {
		switch tokens.Next() {
		case html.ErrorToken:
			return append(append(icons, touchIcons...), logos...)
		case html.StartTagToken, html.SelfClosingTagToken:
			token := tokens.Token()
			if token.Data != "link" && token.Data != "img" {
				continue
			}
			rel, href, label := "", "", ""
			for _, attr := range token.Attr {
				if attr.Key == "rel" {
					rel = strings.ToLower(attr.Val)
				} else if token.Data == "link" && attr.Key == "href" || token.Data == "img" && attr.Key == "src" {
					href = strings.TrimSpace(attr.Val)
				} else if attr.Key == "alt" || attr.Key == "id" || attr.Key == "class" {
					label += " " + strings.ToLower(attr.Val)
				}
			}
			if token.Data == "img" && !strings.Contains(label, "logo") {
				continue
			}
			parsed, err := url.Parse(href)
			if err != nil || href == "" {
				continue
			}
			resolved := base.ResolveReference(parsed)
			if !sameIconOrigin(base, resolved) {
				continue
			}
			if token.Data == "img" {
				logos = append(logos, resolved.String())
			}
			for _, word := range strings.Fields(rel) {
				if word == "icon" {
					icons = append(icons, resolved.String())
					break
				}
				if word == "apple-touch-icon" || word == "apple-touch-icon-precomposed" {
					touchIcons = append(touchIcons, resolved.String())
					break
				}
			}
			if len(icons)+len(touchIcons)+len(logos) >= 8 {
				return append(append(icons, touchIcons...), logos...)
			}
		}
	}
}

func accountIconType(raw []byte) string {
	typeOf := http.DetectContentType(raw)
	switch typeOf {
	case "image/png", "image/jpeg", "image/gif", "image/webp", "image/x-icon":
		return typeOf
	}
	decoder := xml.NewDecoder(bytes.NewReader(raw))
	for {
		token, err := decoder.Token()
		if err != nil {
			return ""
		}
		if start, ok := token.(xml.StartElement); ok {
			if start.Name.Local == "svg" && (start.Name.Space == "" || start.Name.Space == "http://www.w3.org/2000/svg") {
				return "image/svg+xml"
			}
			return ""
		}
	}
}

func (a *App) fetchAccountIcon(parent context.Context, origin string, selected *http.Client) ([]byte, string) {
	ctx, cancel := context.WithTimeout(parent, 8*time.Second)
	defer cancel()
	base, _ := url.Parse(origin)
	client := &http.Client{Transport: selected.Transport, CheckRedirect: func(req *http.Request, via []*http.Request) error {
		if len(via) >= 4 || !sameIconOrigin(base, req.URL) {
			return http.ErrUseLastResponse
		}
		return nil
	}}
	read := func(target string) ([]byte, *url.URL) {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
		if err != nil {
			return nil, nil
		}
		req.Header.Set("User-Agent", "LedgerGate/0.1")
		response, err := client.Do(req)
		if err != nil {
			return nil, nil
		}
		defer response.Body.Close()
		if response.StatusCode != http.StatusOK {
			return nil, nil
		}
		raw, err := io.ReadAll(io.LimitReader(response.Body, accountIconLimit+1))
		if err != nil || len(raw) > accountIconLimit {
			return nil, nil
		}
		return raw, response.Request.URL
	}
	icons := []string{}
	if raw, page := read(origin); page != nil {
		icons = declaredAccountIcons(raw, page)
	}
	icons = append(icons, base.ResolveReference(&url.URL{Path: "/favicon.ico"}).String())
	seen := map[string]bool{}
	for _, target := range icons {
		if seen[target] {
			continue
		}
		seen[target] = true
		raw, _ := read(target)
		if len(raw) > 0 {
			if typeOf := accountIconType(raw); typeOf != "" {
				return raw, typeOf
			}
		}
	}
	return nil, ""
}
