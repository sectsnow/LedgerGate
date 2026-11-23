package app

import (
	"fmt"
	"net/http/httptest"
	"reflect"
	"testing"
)

func TestDeleteGroupRoutePreservesChannelsAndModelSettings(t *testing.T) {
	for _, strategy := range []string{"weighted", "priority", "manual"} {
		t.Run(strategy, func(t *testing.T) {
			a := testApp(t)
			h := a.Router()
			cookie := loginForTest(t, h)
			group := routeOrderGroupForTest(t, a, strategy, "model")
			shared := Group{Name: "shared", Strategy: "weighted", Routes: []Route{{ChannelID: group.Routes[0].ChannelID, Model: "other", Weight: 1}}}
			if err := a.DB.Create(&shared).Error; err != nil {
				t.Fatal(err)
			}
			log := RequestLog{ChannelID: group.Routes[0].ChannelID, Model: group.Name, Status: 200, CostMicros: 123}
			trace := a.beginRequestTrace(&log)
			a.finishRequestTrace(trace, &log)
			expected := group
			for _, route := range group.Routes {
				a.weights[route.ID] = 7
				path := fmt.Sprintf("/api/groups/%d/routes/%d", group.ID, route.ID)
				response := call(h, "DELETE", path, "", cookie)
				if response.Code != 204 {
					t.Fatalf("delete failed %d %s", response.Code, response.Body.String())
				}
				expected.Routes = expected.Routes[1:]
				if expected.ActiveRouteID == route.ID {
					expected.ActiveRouteID = 0
				}
				saved := loadOrderedGroup(t, a, group.ID)
				if !reflect.DeepEqual(saved, expected) {
					t.Fatal("deleting one route changed model settings, other routes, or removed the model")
				}
				if strategy == "manual" && expected.ActiveRouteID == 0 && len(a.candidates(saved)) != 0 {
					t.Fatal("removing the active route silently selected another upstream")
				}
				if _, exists := a.weights[route.ID]; exists {
					t.Fatal("deleted route retained scheduling state")
				}
			}
			if !reflect.DeepEqual(loadOrderedGroup(t, a, shared.ID), shared) {
				t.Fatal("another model using the same channel was changed")
			}
			var channels int64
			a.DB.Model(&Channel{}).Count(&channels)
			cached, _, found := a.cachedRequestDetail(log.ID)
			var summary RequestDay
			if err := a.DB.First(&summary).Error; err != nil {
				t.Fatal(err)
			}
			if channels != 3 || !found || cached.CostMicros != 123 || summary.Requests != 1 || summary.CostMicros != 123 {
				t.Fatal("route removal deleted an upstream channel or historical usage")
			}
		})
	}
}

func TestDeleteGroupRouteValidationAndRollback(t *testing.T) {
	a := testApp(t)
	h := a.Router()
	cookie := loginForTest(t, h)
	group := routeOrderGroupForTest(t, a, "manual", "one")
	other := routeOrderGroupForTest(t, a, "priority", "two")
	path := fmt.Sprintf("/api/groups/%d/routes/%d", group.ID, group.ActiveRouteID)
	if response := call(h, "DELETE", path, "", ""); response.Code != 401 {
		t.Fatal("unauthenticated route removal accepted")
	}
	request := httptest.NewRequest("DELETE", path, nil)
	request.Header.Set("Cookie", cookie)
	response := httptest.NewRecorder()
	h.ServeHTTP(response, request)
	if response.Code != 403 {
		t.Fatal("route removal without CSRF protection accepted")
	}
	for _, test := range []struct {
		path string
		code int
	}{
		{"/api/groups/invalid/routes/1", 400},
		{"/api/groups/0/routes/1", 400},
		{fmt.Sprintf("/api/groups/%d/routes/0", group.ID), 400},
		{fmt.Sprintf("/api/groups/%d/routes/invalid", group.ID), 400},
		{fmt.Sprintf("/api/groups/%d/routes/4294967296", group.ID), 400},
		{fmt.Sprintf("/api/groups/%d/routes/%d", group.ID, other.Routes[0].ID), 404},
		{fmt.Sprintf("/api/groups/99999/routes/%d", group.ActiveRouteID), 404},
	} {
		if response := call(h, "DELETE", test.path, "", cookie); response.Code != test.code {
			t.Fatalf("%s returned %d, want %d", test.path, response.Code, test.code)
		}
	}
	if err := a.DB.Exec("CREATE TRIGGER prevent_route_delete BEFORE DELETE ON routes BEGIN SELECT RAISE(ABORT, 'test failure'); END").Error; err != nil {
		t.Fatal(err)
	}
	a.weights[group.ActiveRouteID] = 7
	if response := call(h, "DELETE", path, "", cookie); response.Code != 500 {
		t.Fatal("database failure reported success")
	}
	if !reflect.DeepEqual(loadOrderedGroup(t, a, group.ID), group) || a.weights[group.ActiveRouteID] != 7 {
		t.Fatal("failed removal changed selection, route, or scheduling state")
	}
	if err := a.DB.Exec("DROP TRIGGER prevent_route_delete").Error; err != nil {
		t.Fatal(err)
	}
	if response := call(h, "DELETE", path, "", cookie); response.Code != 204 {
		t.Fatal("route removal failed")
	}
	if response := call(h, "DELETE", path, "", cookie); response.Code != 404 {
		t.Fatal("stale route removal reported success")
	}
	if !reflect.DeepEqual(loadOrderedGroup(t, a, other.ID), other) {
		t.Fatal("validation or deletion changed another model")
	}
}
