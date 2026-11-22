package app

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"
)

func (a *App) accountRequest(ctx context.Context, account *Account, method, path string, payload any) (any, int, error) {
	if account.AuthMode == "password" && account.Token == "" && account.Cookie == "" && method == http.MethodGet {
		if err := a.loginAccount(ctx, account); err != nil {
			return nil, 0, err
		}
	}
	value, status, err := a.accountRequestWithRefresh(ctx, account, method, path, payload)
	if err != nil && status == http.StatusUnauthorized && account.AuthMode == "password" && method == http.MethodGet {
		if err := a.loginAccount(ctx, account); err != nil {
			return nil, 0, err
		}
		return a.doAccountRequest(ctx, account, method, path, payload)
	}
	return value, status, err
}

func (a *App) accountRequestWithRefresh(ctx context.Context, account *Account, method, path string, payload any) (any, int, error) {
	value, status, err := a.doAccountRequest(ctx, account, method, path, payload)
	if err == nil || status != http.StatusUnauthorized || account.RefreshToken == "" || path == "/api/v1/auth/login" || path == "/api/v1/auth/refresh" || path == "/api/user/login" || path == "/api/user/auth/refresh" {
		return value, status, err
	}
	refreshToken, err := a.unseal(account.RefreshToken)
	if err != nil {
		return nil, http.StatusInternalServerError, err
	}
	if account.Kind == "newapi" {
		var cookies []*http.Cookie
		value, status, err = a.doAccountRequest(ctx, account, http.MethodPost, "/api/user/auth/refresh", nil, &cookies)
		if err != nil {
			return nil, status, fmt.Errorf("NewAPI 自动续期失败（%w）", err)
		}
		if err := a.saveNewAPISession(ctx, account, obj(value), cookies, true); err != nil {
			return nil, 500, err
		}
		return a.doAccountRequest(ctx, account, method, path, payload)
	}
	value, status, err = a.doAccountRequest(ctx, account, http.MethodPost, "/api/v1/auth/refresh", object{"refresh_token": refreshToken})
	if err != nil {
		if status == http.StatusBadRequest {
			status = http.StatusUnauthorized
		}
		return nil, status, fmt.Errorf("Access Token 自动续期失败（%w）", err)
	}
	if err := a.saveSub2APITokens(ctx, account, obj(value), true); err != nil {
		return nil, http.StatusInternalServerError, err
	}
	return a.doAccountRequest(ctx, account, method, path, payload)
}

func (a *App) saveNewAPISession(ctx context.Context, account *Account, data object, cookies []*http.Cookie, refreshing bool) error {
	access := strings.TrimSpace(str(data["access_token"]))
	refresh := ""
	var sessionCookies []string
	for _, cookie := range cookies {
		if cookie.Name == "new_api_refresh" {
			refresh = cookie.Value
		} else if cookie.MaxAge >= 0 {
			sessionCookies = append(sessionCookies, cookie.Name+"="+cookie.Value)
		}
	}
	if refreshing && access == "" {
		return errors.New("NewAPI 续期响应缺少 access_token")
	}
	if access == "" && len(sessionCookies) == 0 {
		return errors.New("登录响应缺少会话信息，可能需要二步验证")
	}
	newAccess, newRefresh, newCookie := a.seal(access), a.seal(refresh), a.seal(strings.Join(sessionCookies, "; "))
	if refreshing && refresh == "" {
		newRefresh = account.RefreshToken
	}
	if access != "" {
		newCookie = ""
	}
	if account.ID != 0 {
		saveCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		result := a.DB.WithContext(saveCtx).Model(&Account{}).Where("id=? AND revision=? AND token=? AND refresh_token=? AND cookie=?", account.ID, account.Revision, account.Token, account.RefreshToken, account.Cookie).Updates(map[string]any{"token": newAccess, "refresh_token": newRefresh, "cookie": newCookie, "user_id": account.UserID})
		if result.Error != nil {
			return errors.New("NewAPI 会话保存失败")
		}
		if result.RowsAffected == 0 {
			return errors.New("账户配置或会话已更新，请重新采集")
		}
	}
	account.Token, account.RefreshToken, account.Cookie = newAccess, newRefresh, newCookie
	return nil
}

func (a *App) saveSub2APITokens(ctx context.Context, account *Account, data object, preserveRefresh bool) error {
	accessToken := strings.TrimSpace(str(data["access_token"]))
	if accessToken == "" {
		return errors.New("认证响应缺少有效 access_token")
	}
	refreshToken := strings.TrimSpace(str(data["refresh_token"]))
	newToken, newRefresh := a.seal(accessToken), a.seal(refreshToken)
	if refreshToken == "" && preserveRefresh {
		newRefresh = account.RefreshToken
	}
	if account.ID != 0 {
		// 轮换后的令牌先成对落库，采集中断或后续统计失败也能继续续期。
		saveCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		result := a.DB.WithContext(saveCtx).Model(&Account{}).Where("id = ? AND revision = ? AND token = ? AND refresh_token = ?", account.ID, account.Revision, account.Token, account.RefreshToken).Updates(map[string]any{"token": newToken, "refresh_token": newRefresh})
		if result.Error != nil {
			return errors.New("认证令牌保存失败，请检查数据库")
		}
		if result.RowsAffected == 0 {
			return errors.New("账户配置或凭据已更新，请重新采集")
		}
	}
	account.Token, account.RefreshToken = newToken, newRefresh
	return nil
}
