package app

import (
	"encoding/json"
	"reflect"
	"testing"
	"time"
)

func TestOverviewHourlyTrendMetrics(t *testing.T) {
	a := testApp(t)
	h := a.Router()
	cookie := loginForTest(t, h)
	hour := time.Now().Truncate(time.Hour).Unix()
	start := hour - 23*3600
	rows := []RequestHour{
		{Hour: start, ChannelID: 1, Model: "a", Requests: 2, Errors: 1, InputTokens: 120, OutputTokens: 12, CostMicros: 100, CostUnknownRequests: 1},
		{Hour: start, ChannelID: 2, Model: "a", Requests: 3, Errors: 1, InputTokens: 180, OutputTokens: 18, CostMicros: 200},
		{Hour: start, ChannelID: 1, Model: "b", Requests: 1, Errors: 1, InputTokens: 10, CostUnknownRequests: 1},
		{Hour: hour, ChannelID: 1, Model: "a", Requests: 2, CostUnknownRequests: 2},
		{Hour: start - 3600, ChannelID: 1, Model: "a", Requests: 999, Errors: 999, InputTokens: 999, OutputTokens: 999, CostMicros: 999, CostUnknownRequests: 999},
		{Hour: hour + 3600, ChannelID: 1, Model: "a", Requests: 999, Errors: 999, InputTokens: 999, OutputTokens: 999, CostMicros: 999, CostUnknownRequests: 999},
	}
	if err := a.DB.Create(&rows).Error; err != nil {
		t.Fatal(err)
	}
	response := call(h, "GET", "/api/overview", "", cookie)
	if response.Code != 200 {
		t.Fatalf("overview %d: %s", response.Code, response.Body.String())
	}
	var result struct {
		Series []map[string]int64 `json:"series"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	want := []map[string]int64{
		{"hour": start, "requests": 6, "errors": 3, "input_tokens": 310, "output_tokens": 30, "cost_micros": 300, "cost_unknown_requests": 2},
		{"hour": hour, "requests": 2, "errors": 0, "input_tokens": 0, "output_tokens": 0, "cost_micros": 0, "cost_unknown_requests": 2},
	}
	if !reflect.DeepEqual(result.Series, want) {
		t.Fatalf("hourly metrics must sum channels/models within the 24-hour window, including zeros and unknown costs\ngot:  %+v\nwant: %+v", result.Series, want)
	}
}
