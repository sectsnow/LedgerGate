package app

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/gin-gonic/gin"
)

type proxySettings struct {
	Enabled     bool   `json:"enabled"`
	Protocol    string `json:"protocol"`
	Host        string `json:"host"`
	Port        int    `json:"port"`
	Username    string `json:"username"`
	HasPassword bool   `json:"has_password"`
}

type upstreamConnectionError string

func (err upstreamConnectionError) Error() string { return string(err) }

type proxyInput struct {
	Enabled       bool   `json:"enabled"`
	Protocol      string `json:"protocol"`
	Host          string `json:"host"`
	Port          int    `json:"port"`
	Username      string `json:"username"`
	Password      string `json:"password"`
	ClearPassword bool   `json:"clear_password"`
	TargetURL     string `json:"target_url"`
}

func proxyAddress(input proxyInput, password string) (*url.URL, error) {
	if input.Protocol != "http" && input.Protocol != "https" && input.Protocol != "socks5" {
		return nil, errors.New("代理协议须为 HTTP、HTTPS 或 SOCKS5")
	}
	host := strings.TrimSpace(input.Host)
	if strings.HasPrefix(host, "[") && strings.HasSuffix(host, "]") {
		host = host[1 : len(host)-1]
	}
	if host == "" || len(host) > 253 || input.Port < 1 || input.Port > 65535 {
		return nil, errors.New("请填写代理主机和有效端口（1–65535）")
	}
	if net.ParseIP(host) == nil {
		for _, ch := range host {
			if !(ch >= 'a' && ch <= 'z' || ch >= 'A' && ch <= 'Z' || ch >= '0' && ch <= '9' || ch == '.' || ch == '-' || ch == '_') {
				return nil, errors.New("代理主机只能填写 IP 或主机名，请勿包含协议、端口或路径")
			}
		}
	}
	if len(input.Username) > 255 || len(password) > 255 || strings.Contains(input.Username, ":") || strings.ContainsFunc(input.Username+password, unicode.IsControl) || password != "" && input.Username == "" {
		return nil, errors.New("代理认证信息无效：用户名和密码各不超过 255 字节，密码需要对应用户名，且不能含控制字符或在用户名中使用冒号")
	}
	address := &url.URL{Scheme: input.Protocol, Host: net.JoinHostPort(host, strconv.Itoa(input.Port))}
	if input.Username != "" {
		address.User = url.UserPassword(input.Username, password)
	}
	return address, nil
}

func parseProxyAddress(raw string) (*url.URL, error) {
	u, err := url.Parse(raw)
	if err != nil || u.Opaque != "" || u.Path != "" || u.RawQuery != "" || u.Fragment != "" || u.ForceQuery {
		return nil, errors.New("代理配置格式无效，请重新保存")
	}
	port, _ := strconv.Atoi(u.Port())
	input := proxyInput{Protocol: u.Scheme, Host: u.Hostname(), Port: port}
	password := ""
	if u.User != nil {
		input.Username = u.User.Username()
		password, _ = u.User.Password()
	}
	return proxyAddress(input, password)
}

func (a *App) readProxy() (Setting, *url.URL, error) {
	var setting Setting
	if a.DB.Select("id", "proxy_enabled", "proxy_url").First(&setting, 1).Error != nil {
		return setting, nil, errors.New("读取代理设置失败")
	}
	if setting.ProxyURL == "" {
		return setting, nil, nil
	}
	raw, err := a.unseal(setting.ProxyURL)
	if err != nil {
		return setting, nil, errors.New("代理凭据解密失败，请检查主密钥或重新保存")
	}
	u, err := parseProxyAddress(raw)
	return setting, u, err
}

func publicProxy(setting Setting, address *url.URL) proxySettings {
	result := proxySettings{Enabled: setting.ProxyEnabled, Protocol: "http", Port: 7890}
	if address != nil {
		result.Protocol, result.Host = address.Scheme, address.Hostname()
		result.Port, _ = strconv.Atoi(address.Port())
		if address.User != nil {
			result.Username = address.User.Username()
			password, _ := address.User.Password()
			result.HasPassword = password != ""
		}
	}
	return result
}

func proxyDraft(input proxyInput, previous *url.URL) (*url.URL, error) {
	password := input.Password
	if input.ClearPassword && password != "" {
		return nil, errors.New("请在填写新密码和清除原密码之间选择一项")
	}
	if password == "" && !input.ClearPassword && previous != nil && previous.User != nil {
		password, _ = previous.User.Password()
	}
	if !input.Enabled && strings.TrimSpace(input.Host) == "" && input.Username == "" && password == "" {
		return nil, nil
	}
	return proxyAddress(input, password)
}

func (a *App) getProxy(c *gin.Context) {
	setting, address, err := a.readProxy()
	if err != nil {
		fail(c, 500, err.Error())
		return
	}
	c.JSON(200, publicProxy(setting, address))
}

func (a *App) saveProxy(c *gin.Context) {
	var input proxyInput
	if c.ShouldBindJSON(&input) != nil {
		fail(c, 400, "代理设置格式无效")
		return
	}
	setting, previous, err := a.readProxy()
	if err != nil {
		fail(c, 500, err.Error())
		return
	}
	address, err := proxyDraft(input, previous)
	if err != nil {
		fail(c, 400, err.Error())
		return
	}
	cipher := ""
	if address != nil {
		cipher = a.seal(address.String())
	}
	result := a.DB.Model(&Setting{}).Where("id = 1 AND proxy_url = ? AND proxy_enabled = ?", setting.ProxyURL, setting.ProxyEnabled).Updates(map[string]any{"proxy_enabled": input.Enabled, "proxy_url": cipher})
	if result.Error != nil {
		fail(c, 500, "保存代理设置失败")
		return
	}
	if result.RowsAffected == 0 {
		fail(c, 409, "代理设置已更新，请重新加载后重试")
		return
	}
	setting.ProxyEnabled = input.Enabled
	c.JSON(200, publicProxy(setting, address))
}

func (a *App) newProxyClient(address *url.URL) (*http.Client, error) {
	base, ok := a.client.Transport.(*http.Transport)
	if !ok {
		return nil, errors.New("当前 HTTP 客户端不支持网络代理")
	}
	transport := base.Clone()
	transport.Proxy = http.ProxyURL(address)
	return &http.Client{Transport: transport, CheckRedirect: a.client.CheckRedirect}, nil
}

func (a *App) httpClient(useProxy bool) (*http.Client, error) {
	if !useProxy {
		return a.client, nil
	}
	a.proxyMu.Lock()
	defer a.proxyMu.Unlock()
	setting, address, err := a.readProxy()
	if err != nil {
		return nil, err
	}
	if !setting.ProxyEnabled {
		return a.client, nil
	}
	if address == nil {
		return nil, errors.New("全局代理已开启但未配置地址")
	}
	if a.proxyClient == nil || a.proxyIdentity != setting.ProxyURL {
		client, err := a.newProxyClient(address)
		if err != nil {
			return nil, err
		}
		if a.proxyClient != nil {
			a.proxyClient.CloseIdleConnections()
		}
		a.proxyClient, a.proxyIdentity = client, setting.ProxyURL
	}
	return a.proxyClient, nil
}

func (a *App) doRequest(req *http.Request, useProxy bool) (*http.Response, error) {
	client, err := a.httpClient(useProxy)
	if err != nil {
		return nil, err
	}
	resp, err := client.Do(req)
	if err != nil {
		if client != a.client {
			return nil, upstreamConnectionError("代理连接失败或超时，请检查代理地址、认证及目标连通性")
		}
		return nil, upstreamConnectionError("上游连接失败或超时")
	}
	if client != a.client && resp.StatusCode == http.StatusProxyAuthRequired {
		resp.Body.Close()
		return nil, errors.New("代理认证失败，请检查代理用户名和密码")
	}
	return resp, nil
}

func (a *App) testProxy(c *gin.Context) {
	var input proxyInput
	if c.ShouldBindJSON(&input) != nil {
		fail(c, 400, "代理测试参数无效")
		return
	}
	_, previous, err := a.readProxy()
	if err != nil {
		fail(c, 500, err.Error())
		return
	}
	input.Enabled = true
	address, err := proxyDraft(input, previous)
	if err != nil {
		fail(c, 400, err.Error())
		return
	}
	target, err := url.Parse(input.TargetURL)
	if err != nil || len(input.TargetURL) > 2048 || target.Hostname() == "" || target.User != nil || target.Fragment != "" || target.Scheme != "http" && target.Scheme != "https" {
		fail(c, 400, "测试目标须为不含认证信息的 HTTP 或 HTTPS 地址")
		return
	}
	client, err := a.newProxyClient(address)
	if err != nil {
		fail(c, 500, err.Error())
		return
	}
	defer client.CloseIdleConnections()
	ctx, cancel := context.WithTimeout(c.Request.Context(), 12*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target.String(), nil)
	if err != nil {
		fail(c, 400, "测试目标地址无效")
		return
	}
	start := time.Now()
	resp, err := client.Do(req)
	if err != nil {
		fail(c, 502, "代理测试失败，请检查代理地址、认证、TLS 证书及目标连通性")
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusProxyAuthRequired {
		fail(c, 502, "代理认证失败，请检查用户名和密码")
		return
	}
	_, _ = io.CopyN(io.Discard, resp.Body, 4096)
	c.JSON(200, gin.H{"status": resp.StatusCode, "duration_ms": time.Since(start).Milliseconds()})
}
