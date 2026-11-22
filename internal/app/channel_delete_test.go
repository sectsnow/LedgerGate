package app

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"
)

func TestDeleteChannelAndDetachRoutes(t *testing.T) {
	a := testApp(t)
	h := a.Router()
	cookie := loginForTest(t, h)
	target := createChannel(t, h, cookie, "remove", "https://example.invalid", "openai")
	remaining := createChannel(t, h, cookie, "keep", "https://example.invalid", "openai")
	token := createGroup(t, h, cookie, []Channel{target, remaining}, "weighted")
	w := call(h, "POST", "/api/groups", jsonString(Group{Name: "only-channel", Strategy: "priority", Routes: []Route{{ChannelID: target.ID, Model: "model", Weight: 1}}}), cookie)
	if w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	path := fmt.Sprintf("/api/channels/%d", target.ID)
	for _, query := range []string{"", "?detach_routes=false"} {
		w = call(h, "DELETE", path+query, "", cookie)
		if w.Code != http.StatusConflict {
			t.Fatalf("referenced channel deleted without confirmation %d %s", w.Code, w.Body.String())
		}
	}
	if w = call(h, "DELETE", path+"?detach_routes=invalid", "", cookie); w.Code != 400 {
		t.Fatal("invalid deletion option accepted")
	}
	var routeCount int64
	if err := a.DB.Model(&Route{}).Where("channel_id = ?", target.ID).Count(&routeCount).Error; err != nil || routeCount != 2 {
		t.Fatal("blocked deletion changed route references")
	}
	w = call(h, "DELETE", path+"?detach_routes=true", "", cookie)
	if w.Code != 204 {
		t.Fatalf("confirmed deletion failed %d %s", w.Code, w.Body.String())
	}
	var channelCount int64
	a.DB.Model(&Channel{}).Where("id = ?", target.ID).Count(&channelCount)
	a.DB.Model(&Route{}).Where("channel_id = ?", target.ID).Count(&routeCount)
	if channelCount != 0 || routeCount != 0 {
		t.Fatal("channel or its references survived deletion")
	}
	var kept Channel
	if err := a.DB.First(&kept, remaining.ID).Error; err != nil || kept.APIKey == "" {
		t.Fatal("another channel was affected")
	}
	w = call(h, "GET", "/api/groups", "", cookie)
	var groups []Group
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &groups) != nil || len(groups) != 2 {
		t.Fatal("model groups were deleted")
	}
	for _, group := range groups {
		if group.Name == "my-model" && (len(group.Routes) != 1 || group.Routes[0].ChannelID != remaining.ID) {
			t.Fatal("remaining model routes changed")
		}
		if group.Name == "only-channel" && len(group.Routes) != 0 {
			t.Fatal("empty model still references deleted channel")
		}
	}
	w = call(h, "POST", "/v1/chat/completions", `{"model":"only-channel","messages":[{"role":"user","content":"hi"}]}`, token)
	if w.Code != 503 || !strings.Contains(w.Body.String(), "暂无可用渠道") {
		t.Fatal("empty model did not fail cleanly")
	}
	if w = call(h, "DELETE", path+"?detach_routes=true", "", cookie); w.Code != 204 {
		t.Fatal("deletion retry is not idempotent")
	}
}

func TestDeleteChannelRollsBackRoutesOnFailure(t *testing.T) {
	a := testApp(t)
	h := a.Router()
	cookie := loginForTest(t, h)
	channel := createChannel(t, h, cookie, "channel", "https://example.invalid", "openai")
	createGroup(t, h, cookie, []Channel{channel}, "priority")
	if err := a.DB.Exec(`CREATE TRIGGER prevent_channel_delete BEFORE DELETE ON channels BEGIN SELECT RAISE(ABORT, 'test failure'); END`).Error; err != nil {
		t.Fatal(err)
	}
	w := call(h, "DELETE", fmt.Sprintf("/api/channels/%d?detach_routes=true", channel.ID), "", cookie)
	if w.Code != 500 {
		t.Fatalf("deletion failure hidden %d %s", w.Code, w.Body.String())
	}
	var channels, routes int64
	if err := a.DB.Model(&Channel{}).Where("id = ?", channel.ID).Count(&channels).Error; err != nil {
		t.Fatal(err)
	}
	if err := a.DB.Model(&Route{}).Where("channel_id = ?", channel.ID).Count(&routes).Error; err != nil {
		t.Fatal(err)
	}
	if channels != 1 || routes != 1 {
		t.Fatal("failed deletion left partially modified routing")
	}
}
