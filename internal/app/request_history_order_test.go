package app

import (
	"encoding/json"
	"slices"
	"testing"
)

func TestRequestHistoryActiveFirstBeforePagination(t *testing.T) {
	a := testApp(t)
	h := a.Router()
	cookie := loginForTest(t, h)
	rows := []RequestLog{
		{RequestID: "old-running", State: "running"},
		{RequestID: "old-completed", State: "completed", Status: 200},
		{RequestID: "old-streaming", State: "streaming"},
		{RequestID: "failed", State: "failed", Status: 502},
		{RequestID: "cancelled", State: "cancelled", Status: 499},
		{RequestID: "another-failed", State: "failed", Status: 502},
		{RequestID: "legacy-completed", Status: 200},
		{RequestID: "newest-completed", State: "completed", Status: 200},
	}
	for i := range rows {
		rows[i].ID = uint(i + 1)
		if activeRequest(rows[i].State) {
			if a.activeRequests == nil {
				a.activeRequests = map[uint]*requestTrace{}
			}
			a.activeRequests[rows[i].ID] = &requestTrace{log: rows[i]}
		} else {
			a.cacheFinishedRequest(rows[i], requestPayload{Attempts: []requestAttempt{}})
		}
	}
	for _, test := range []struct {
		name, path string
		order      []int
		total      int64
	}{
		{"first page", "/api/requests?page=1&page_size=3", []int{2, 0, 7}, 8},
		{"second page", "/api/requests?page=2&page_size=3", []int{6, 5, 4}, 8},
		{"third page", "/api/requests?page=3&page_size=3", []int{3, 1}, 8},
		{"active filter", "/api/requests?page=1&state=running", []int{2, 0}, 2},
		{"completed filter", "/api/requests?page=1&state=completed", []int{7, 6, 1}, 3},
		{"search filter", "/api/requests?page=1&q=old-", []int{2, 0, 1}, 3},
		{"dashboard", "/api/requests", []int{2, 0, 7, 6, 5, 4, 3, 1}, 8},
	} {
		t.Run(test.name, func(t *testing.T) {
			response := call(h, "GET", test.path, "", cookie)
			var page struct {
				Items []RequestLog `json:"items"`
				Total int64        `json:"total"`
			}
			var err error
			if test.path == "/api/requests" {
				err = json.Unmarshal(response.Body.Bytes(), &page.Items)
				page.Total = int64(len(page.Items))
			} else {
				err = json.Unmarshal(response.Body.Bytes(), &page)
			}
			if response.Code != 200 || err != nil || page.Total != test.total {
				t.Fatalf("unexpected history response: %d %s", response.Code, response.Body.String())
			}
			got, want := []uint{}, []uint{}
			for _, row := range page.Items {
				got = append(got, row.ID)
			}
			for _, index := range test.order {
				want = append(want, rows[index].ID)
			}
			if !slices.Equal(got, want) {
				t.Fatalf("request order = %v, want %v", got, want)
			}
		})
	}
	for _, index := range []int{0, 2} {
		trace := a.activeRequests[rows[index].ID]
		trace.log.State, trace.log.Status = "completed", 200
		a.cacheFinishedRequest(trace.log, requestPayload{Attempts: []requestAttempt{}})
		delete(a.activeRequests, rows[index].ID)
	}
	response := call(h, "GET", "/api/requests", "", cookie)
	var completed []RequestLog
	if response.Code != 200 || json.Unmarshal(response.Body.Bytes(), &completed) != nil || len(completed) != len(rows) {
		t.Fatalf("unexpected completed history: %d %s", response.Code, response.Body.String())
	}
	for i, row := range completed {
		if row.ID != rows[len(rows)-1-i].ID {
			t.Fatal("completed requests did not return to chronological order")
		}
	}
}
