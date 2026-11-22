package app

import (
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestGatewayKeyCopy(t *testing.T) {
	a := testApp(t)
	h := a.Router()
	cookie := loginForTest(t, h)
	response := call(h, "POST", "/api/keys", `{"name":"copyable"}`, cookie)
	var created struct {
		Key   APIKey `json:"key"`
		Token string `json:"token"`
	}
	if response.Code != 201 || json.Unmarshal(response.Body.Bytes(), &created) != nil {
		t.Fatal("key creation failed")
	}
	if !strings.HasPrefix(created.Token, "sk-") || len(created.Token) != 46 || !created.Key.CanCopy {
		t.Fatal("wrong key format or copy status")
	}
	var stored APIKey
	if err := a.DB.First(&stored, created.Key.ID).Error; err != nil {
		t.Fatal(err)
	}
	if stored.TokenCipher == "" || strings.Contains(stored.TokenCipher, created.Token) || stored.Digest != digest(created.Token) {
		t.Fatal("key must be encrypted with a matching authentication digest")
	}

	path := fmt.Sprintf("/api/keys/%d/copy", created.Key.ID)
	for _, auth := range []string{"", created.Token} {
		response = call(h, "POST", path, "", auth)
		if response.Code != 401 || strings.Contains(response.Body.String(), created.Token) {
			t.Fatal("non-admin copied a key")
		}
	}
	req := httptest.NewRequest("POST", path, nil)
	req.Header.Set("Cookie", cookie)
	response = httptest.NewRecorder()
	h.ServeHTTP(response, req)
	if response.Code != 403 {
		t.Fatal("copy endpoint requires CSRF header")
	}
	response = call(h, "POST", path, "", cookie)
	var copied struct {
		Token string `json:"token"`
	}
	if response.Code != 200 || json.Unmarshal(response.Body.Bytes(), &copied) != nil || copied.Token != created.Token {
		t.Fatal("copy did not return the complete key")
	}
	if response.Header().Get("Cache-Control") != "no-store" {
		t.Fatal("copy response must not be cached")
	}
	response = call(h, "GET", "/api/keys", "", cookie)
	if response.Code != 200 || !strings.Contains(response.Body.String(), `"can_copy":true`) || strings.Contains(response.Body.String(), created.Token) || strings.Contains(response.Body.String(), stored.TokenCipher) || strings.Contains(response.Body.String(), stored.Digest) {
		t.Fatal("list must show copy status without leaking key data")
	}
	if response = call(h, "GET", "/v1/models", "", copied.Token); response.Code != 200 {
		t.Fatal("copied key cannot authenticate")
	}
	if response = call(h, "POST", "/api/keys/invalid/copy", "", cookie); response.Code != 400 {
		t.Fatal("invalid id accepted")
	}
	if response = call(h, "POST", "/api/keys/999999/copy", "", cookie); response.Code != 404 {
		t.Fatal("missing key not rejected")
	}
	response = call(h, "DELETE", fmt.Sprintf("/api/keys/%d", created.Key.ID), "", cookie)
	if response.Code != 204 {
		t.Fatal("revocation failed")
	}
	if response = call(h, "POST", path, "", cookie); response.Code != 404 {
		t.Fatal("revoked key can still be copied")
	}
	if response = call(h, "GET", "/v1/models", "", created.Token); response.Code != 401 {
		t.Fatal("revoked key can still authenticate")
	}
}

func TestLegacyGatewayKeyRemainsUsable(t *testing.T) {
	a := testApp(t)
	token := "lg_" + randomToken()
	legacy := APIKey{Name: "legacy", Prefix: token[:11], Digest: digest(token), Enabled: true}
	if err := a.DB.Create(&legacy).Error; err != nil {
		t.Fatal(err)
	}
	h := a.Router()
	cookie := loginForTest(t, h)
	response := call(h, "GET", "/api/keys", "", cookie)
	if response.Code != 200 || !strings.Contains(response.Body.String(), `"can_copy":false`) {
		t.Fatal("legacy key incorrectly marked as copyable")
	}
	response = call(h, "POST", fmt.Sprintf("/api/keys/%d/copy", legacy.ID), "", cookie)
	if response.Code != 409 || strings.Contains(response.Body.String(), token) {
		t.Fatal("legacy key copy should explain that only a digest exists")
	}
	if response = call(h, "GET", "/v1/models", "", token); response.Code != 200 {
		t.Fatal("existing key stopped working")
	}
}

func TestGatewayKeyCopyAfterRestart(t *testing.T) {
	config := Config{DataDir: t.TempDir(), AdminPassword: "test-password-12345"}
	first, err := Open(config)
	if err != nil {
		t.Fatal(err)
	}
	h := first.Router()
	response := call(h, "POST", "/api/keys", `{"name":"persistent"}`, loginForTest(t, h))
	var created struct {
		Key   APIKey `json:"key"`
		Token string `json:"token"`
	}
	if response.Code != 201 || json.Unmarshal(response.Body.Bytes(), &created) != nil {
		first.Close()
		t.Fatal("key creation failed")
	}
	if err := first.Close(); err != nil {
		t.Fatal(err)
	}
	second, err := Open(config)
	if err != nil {
		t.Fatal(err)
	}
	defer second.Close()
	h = second.Router()
	cookie := loginForTest(t, h)
	path := fmt.Sprintf("/api/keys/%d/copy", created.Key.ID)
	response = call(h, "POST", path, "", cookie)
	var copied struct {
		Token string `json:"token"`
	}
	if response.Code != 200 || json.Unmarshal(response.Body.Bytes(), &copied) != nil || copied.Token != created.Token {
		t.Fatal("copied key changed after restart")
	}
	if err := second.DB.Model(&APIKey{}).Where("id = ?", created.Key.ID).Update("token_cipher", second.seal("wrong-key")).Error; err != nil {
		t.Fatal(err)
	}
	response = call(h, "POST", path, "", cookie)
	if response.Code != 500 || strings.Contains(response.Body.String(), "wrong-key") {
		t.Fatal("ciphertext/digest mismatch should fail without exposing data")
	}
}
