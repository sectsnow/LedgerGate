package app

import (
	"encoding/json"
	"fmt"
	"reflect"
	"testing"
	"time"
)

func TestConfiguredRecentModels(t *testing.T) {
	a := testApp(t)
	h := a.Router()
	cookie := loginForTest(t, h)
	groups := []Group{{Name: "a"}, {Name: "b"}, {Name: "c"}, {Name: "d"}, {Name: "e"}, {Name: "f"}}
	if err := a.DB.Create(&groups).Error; err != nil {
		t.Fatal(err)
	}
	hour := time.Now().Truncate(time.Hour).Unix() - 3600
	rows := []RequestHour{
		{Hour: hour, ChannelID: 1, Model: "a", Requests: 3, LastRequestUnix: 100},
		{Hour: hour, ChannelID: 2, Model: "a", Requests: 4, LastRequestUnix: 200},
		{Hour: hour, ChannelID: 1, Model: "b", Requests: 7, LastRequestUnix: 200},
		{Hour: hour, ChannelID: 1, Model: "c", Requests: 9, LastRequestUnix: 100},
		{Hour: hour, ChannelID: 1, Model: "d", Requests: 7, LastRequestUnix: 300},
		{Hour: hour, ChannelID: 1, Model: "e", Requests: 2, LastRequestUnix: 400},
		{Hour: hour, ChannelID: 1, Model: "f", Requests: 1, LastRequestUnix: 500},
		{Hour: hour - 24*3600, ChannelID: 1, Model: "e", Requests: 1000},
		{Hour: hour + 2*3600, ChannelID: 1, Model: "f", Requests: 1000},
	}
	for i := range 505 {
		rows = append(rows, RequestHour{Hour: hour, ChannelID: 1, Model: fmt.Sprintf("old-%03d", i), Requests: 100})
	}
	if err := a.DB.CreateInBatches(&rows, 100).Error; err != nil {
		t.Fatal(err)
	}
	response := call(h, "GET", "/api/request-quality?group_by=model&configured=1", "", cookie)
	var result struct{ Items []qualityRow }
	if response.Code != 200 || json.Unmarshal(response.Body.Bytes(), &result) != nil {
		t.Fatalf("读取最近使用模型失败：%d %s", response.Code, response.Body.String())
	}
	var names []string
	for _, item := range result.Items {
		names = append(names, item.Name)
	}
	if want := []string{"c", "d", "a", "b", "e"}; !reflect.DeepEqual(names, want) || result.Items[2].Requests != 7 {
		t.Fatalf("最近模型排序或汇总错误：%+v", result.Items)
	}
	for _, query := range []string{"?configured=1", "?group_by=model&configured=invalid"} {
		if response := call(h, "GET", "/api/request-quality"+query, "", cookie); response.Code != 400 {
			t.Fatalf("无效参数 %q 返回 %d", query, response.Code)
		}
	}
}
