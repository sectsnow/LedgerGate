package app

import (
	"bytes"
	"context"
	"encoding/xml"
	"image"
	_ "image/gif"
	_ "image/jpeg"
	"image/png"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"golang.org/x/net/html"
)

const accountIconLimit = 512 << 10
const accountIconDownloadLimit = 4 << 20

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
	origin := accountIconOrigin(account.BaseURL)
	if origin == "" {
		c.Status(http.StatusNotFound)
		return
	}
	stored, err := a.storedAccountIcon(account)
	if err != nil {
		fail(c, 500, "读取网站图标失败")
		return
	}
	body, typeOf := stored.Body, stored.TypeOf
	if len(body) == 0 {
		client, err := a.httpClient(account.UseProxy)
		if err != nil {
			c.Status(http.StatusNotFound)
			return
		}
		if c.Query("refresh") == "1" {
			a.mu.Lock()
			if cached := a.accountIcons[id]; cached != nil && cached.ready == nil && len(cached.body) == 0 {
				cached.expires = time.Time{}
			}
			a.mu.Unlock()
		}
		body, typeOf = a.cachedAccountIcon(c.Request.Context(), id, origin, client)
		if len(body) > 0 {
			if err := a.saveAutomaticAccountIcon(account, body, typeOf); err != nil {
				fail(c, 500, "保存网站图标失败")
				return
			}
			stored, err = a.storedAccountIcon(account)
			if err != nil {
				fail(c, 500, "读取网站图标失败")
				return
			}
			body, typeOf = stored.Body, stored.TypeOf
		}
	}
	c.Header("Vary", "Cookie")
	if len(body) == 0 {
		c.Header("Cache-Control", "no-store")
		c.Status(http.StatusNotFound)
		return
	}
	c.Header("Cache-Control", "private, no-cache")
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

func declaredAccountIcons(raw []byte, base *url.URL, includeExternal bool) []string {
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
			if resolved.User != nil || resolved.Hostname() == "" || (resolved.Scheme != "http" && resolved.Scheme != "https") || !includeExternal && !sameIconOrigin(base, resolved) {
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

func prepareAccountIcon(raw []byte) ([]byte, string) {
	typeOf := accountIconType(raw)
	if typeOf == "" || len(raw) > accountIconDownloadLimit {
		return nil, ""
	}
	if len(raw) <= accountIconLimit && typeOf != "image/png" && typeOf != "image/jpeg" && typeOf != "image/gif" {
		return raw, typeOf
	}
	config, _, err := image.DecodeConfig(bytes.NewReader(raw))
	if err != nil || config.Width <= 0 || config.Height <= 0 || config.Width > (8<<20)/config.Height {
		return nil, ""
	}
	source, _, err := image.Decode(bytes.NewReader(raw))
	if err != nil {
		return nil, ""
	}
	if len(raw) <= accountIconLimit {
		return raw, typeOf
	}
	width, height := config.Width, config.Height
	if width > 128 || height > 128 {
		if width >= height {
			height, width = max(1, height*128/width), 128
		} else {
			width, height = max(1, width*128/height), 128
		}
	}
	thumbnail := image.NewNRGBA(image.Rect(0, 0, width, height))
	bounds := source.Bounds()
	for y := 0; y < height; y++ {
		for x := 0; x < width; x++ {
			thumbnail.Set(x, y, source.At(bounds.Min.X+x*config.Width/width, bounds.Min.Y+y*config.Height/height))
		}
	}
	var output bytes.Buffer
	if png.Encode(&output, thumbnail) != nil || output.Len() > accountIconLimit {
		return nil, ""
	}
	return output.Bytes(), "image/png"
}

func (a *App) fetchAccountIcon(parent context.Context, origin string, selected *http.Client) ([]byte, string) {
	icons := a.fetchAccountIconCandidates(parent, origin, selected, false)
	if len(icons) > 0 {
		return icons[0].Body, icons[0].TypeOf
	}
	return nil, ""
}

func (a *App) fetchAccountIconCandidates(parent context.Context, origin string, selected *http.Client, includeExternal bool) []accountIconCandidate {
	ctx, cancel := context.WithTimeout(parent, 8*time.Second)
	defer cancel()
	base, _ := url.Parse(origin)
	icons := []string{}
	if raw, page := readAccountIconResource(ctx, origin, base, selected, accountIconLimit, false); page != nil {
		icons = declaredAccountIcons(raw, page, includeExternal)
	}
	icons = append(icons, base.ResolveReference(&url.URL{Path: "/favicon.ico"}).String())
	seen := map[string]bool{}
	contents := map[string]bool{}
	result := []accountIconCandidate{}
	for _, target := range icons {
		if seen[target] {
			continue
		}
		seen[target] = true
		raw, _ := readAccountIconResource(ctx, target, base, selected, accountIconDownloadLimit, includeExternal)
		if len(raw) > 0 {
			if body, typeOf := prepareAccountIcon(raw); typeOf != "" {
				hash := digest(string(body))
				if !contents[hash] {
					result = append(result, accountIconCandidate{URL: target, Body: body, TypeOf: typeOf})
					contents[hash] = true
				}
				if !includeExternal {
					return result
				}
			}
		}
	}
	return result
}
