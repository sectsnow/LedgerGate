package app

import (
	"fmt"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
	"testing"

	"gorm.io/gorm"
)

func routeOrderGroupForTest(t *testing.T, a *App, strategy, name string) Group {
	t.Helper()
	group := Group{Name: name, Vendor: "custom", Strategy: strategy, InputPriceMicros: 2_000_000, OutputPriceMicros: 8_000_000, PriceMode: "manual", PriceSource: "manual", PriceModel: "reference-model"}
	for index := range 3 {
		channel := Channel{Name: fmt.Sprintf("%s-channel-%d", name, index), Enabled: true}
		if err := a.DB.Create(&channel).Error; err != nil {
			t.Fatal(err)
		}
		group.Routes = append(group.Routes, Route{ChannelID: channel.ID, Model: "upstream-model", Weight: index + 4, Priority: (index + 1) * 10})
	}
	if err := a.DB.Create(&group).Error; err != nil {
		t.Fatal(err)
	}
	if strategy == "manual" {
		group.ActiveRouteID = group.Routes[1].ID
		if err := a.DB.Model(&group).Update("active_route_id", group.ActiveRouteID).Error; err != nil {
			t.Fatal(err)
		}
	}
	return group
}

func routeOrderBody(group Group, ids []uint) object {
	expected := []routeOrderEntry{}
	for _, route := range group.Routes {
		expected = append(expected, routeOrderEntry{ID: route.ID, Priority: route.Priority, Weight: route.Weight})
	}
	return object{"strategy": group.Strategy, "route_ids": ids, "expected_routes": expected}
}

func loadOrderedGroup(t *testing.T, a *App, id uint) Group {
	t.Helper()
	var group Group
	if err := a.DB.Preload("Routes", func(tx *gorm.DB) *gorm.DB { return tx.Order("priority, id") }).First(&group, id).Error; err != nil {
		t.Fatal(err)
	}
	return group
}

func TestRouteOrderPreservesSettingsAndChangesScheduling(t *testing.T) {
	for _, strategy := range []string{"priority", "weighted", "manual"} {
		t.Run(strategy, func(t *testing.T) {
			a := testApp(t)
			h := a.Router()
			cookie := loginForTest(t, h)
			group := routeOrderGroupForTest(t, a, strategy, "model")
			ids := []uint{group.Routes[2].ID, group.Routes[0].ID, group.Routes[1].ID}
			a.weights[group.Routes[0].ID], a.weights[999] = 100, 77
			response := call(h, "PATCH", fmt.Sprintf("/api/groups/%d/order", group.ID), jsonString(routeOrderBody(group, ids)), cookie)
			if response.Code != 204 {
				t.Fatalf("reorder failed %d %s", response.Code, response.Body.String())
			}
			saved := loadOrderedGroup(t, a, group.ID)
			originalSettings, savedSettings := group, saved
			originalSettings.Routes, savedSettings.Routes = nil, nil
			if !reflect.DeepEqual(originalSettings, savedSettings) {
				t.Fatal("sorting changed model settings, pricing, or manual selection")
			}
			for index, route := range saved.Routes {
				if route.ID != ids[index] || route.Priority != index {
					t.Fatal("route identities or persisted order changed incorrectly")
				}
				for _, original := range group.Routes {
					if original.ID != route.ID {
						continue
					}
					if strategy == "weighted" {
						original.Weight = 3 - index
					}
					original.Priority = index
					if !reflect.DeepEqual(route, original) {
						t.Fatal("sorting changed a route beyond its priority or applicable weight")
					}
				}
				if _, exists := a.weights[route.ID]; exists {
					t.Fatal("old scheduling weights not invalidated")
				}
			}
			if a.weights[999] != 77 {
				t.Fatal("sorting disturbed another model's scheduling state")
			}
			candidates := a.candidates(saved)
			if strategy == "weighted" {
				counts := map[uint]int{}
				for range 60 {
					picked, ok := a.pick(candidates, strategy, map[uint]bool{})
					if !ok {
						t.Fatal("weighted route unavailable")
					}
					counts[picked.Route.ID]++
				}
				if counts[ids[0]] != 30 || counts[ids[1]] != 20 || counts[ids[2]] != 10 {
					t.Fatalf("reordered weights did not change request distribution: %v", counts)
				}
			} else {
				picked, ok := a.pick(candidates, strategy, map[uint]bool{})
				want := ids[0]
				if strategy == "manual" {
					want = group.ActiveRouteID
				}
				if !ok || picked.Route.ID != want {
					t.Fatal("priority or manual routing did not preserve its defined behavior")
				}
			}
		})
	}
}

func TestRouteOrderValidationAndStaleData(t *testing.T) {
	a := testApp(t)
	h := a.Router()
	cookie := loginForTest(t, h)
	group := routeOrderGroupForTest(t, a, "priority", "one")
	other := routeOrderGroupForTest(t, a, "priority", "two")
	ids := []uint{group.Routes[2].ID, group.Routes[0].ID, group.Routes[1].ID}
	path := fmt.Sprintf("/api/groups/%d/order", group.ID)
	if response := call(h, "PATCH", path, jsonString(routeOrderBody(group, ids)), ""); response.Code != 401 {
		t.Fatal("unauthenticated reorder accepted")
	}
	request := httptest.NewRequest("PATCH", path, strings.NewReader(jsonString(routeOrderBody(group, ids))))
	request.Header.Set("Cookie", cookie)
	request.Header.Set("Content-Type", "application/json")
	csrfResponse := httptest.NewRecorder()
	h.ServeHTTP(csrfResponse, request)
	if csrfResponse.Code != 403 {
		t.Fatal("reorder without CSRF protection accepted")
	}
	w := call(h, "PATCH", path, `not-json`, cookie)
	if w.Code != 400 {
		t.Fatal("invalid JSON accepted")
	}
	for _, test := range []struct {
		name   string
		change func(object)
		status int
	}{
		{"missing order", func(body object) { delete(body, "route_ids") }, 400},
		{"duplicates", func(body object) { body["route_ids"] = []uint{ids[0], ids[0], ids[1]} }, 400},
		{"zero", func(body object) { body["route_ids"] = []uint{0, ids[1], ids[2]} }, 400},
		{"unknown strategy", func(body object) { body["strategy"] = "random" }, 400},
		{"changed strategy", func(body object) { body["strategy"] = "weighted" }, 409},
		{"missing expected", func(body object) { delete(body, "expected_routes") }, 400},
		{"missing route", func(body object) {
			body["route_ids"] = ids[:2]
			expected := body["expected_routes"].([]routeOrderEntry)
			body["expected_routes"] = []routeOrderEntry{expected[2], expected[0]}
		}, 409},
		{"changed weight", func(body object) { body["expected_routes"].([]routeOrderEntry)[0].Weight++ }, 409},
		{"changed priority", func(body object) { body["expected_routes"].([]routeOrderEntry)[0].Priority++ }, 409},
		{"foreign route", func(body object) {
			body["expected_routes"].([]routeOrderEntry)[0].ID = other.Routes[0].ID
			body["route_ids"] = []uint{ids[0], other.Routes[0].ID, ids[2]}
		}, 409},
	} {
		t.Run(test.name, func(t *testing.T) {
			body := routeOrderBody(group, ids)
			test.change(body)
			response := call(h, "PATCH", path, jsonString(body), cookie)
			if response.Code != test.status {
				t.Fatalf("got %d, want %d: %s", response.Code, test.status, response.Body.String())
			}
			if saved := loadOrderedGroup(t, a, group.ID); !reflect.DeepEqual(saved.Routes, group.Routes) {
				t.Fatal("rejected reorder changed route data")
			}
		})
	}
	if response := call(h, "PATCH", "/api/groups/999/order", jsonString(routeOrderBody(group, ids)), cookie); response.Code != 404 {
		t.Fatal("unknown model accepted")
	}
}

func TestRouteOrderRollbackAndConcurrentReorders(t *testing.T) {
	a := testApp(t)
	h := a.Router()
	cookie := loginForTest(t, h)
	group := routeOrderGroupForTest(t, a, "weighted", "model")
	ids := []uint{group.Routes[1].ID, group.Routes[0].ID, group.Routes[2].ID}
	trigger := fmt.Sprintf("CREATE TRIGGER reject_order BEFORE UPDATE ON routes WHEN OLD.id = %d BEGIN SELECT RAISE(ABORT, 'forced failure'); END", group.Routes[2].ID)
	if err := a.DB.Exec(trigger).Error; err != nil {
		t.Fatal(err)
	}
	path := fmt.Sprintf("/api/groups/%d/order", group.ID)
	if response := call(h, "PATCH", path, jsonString(routeOrderBody(group, ids)), cookie); response.Code != 500 {
		t.Fatal("forced failure did not fail reorder")
	}
	if saved := loadOrderedGroup(t, a, group.ID); !reflect.DeepEqual(saved.Routes, group.Routes) {
		t.Fatal("failed reorder did not roll back all priorities and weights")
	}
	if err := a.DB.Exec("DROP TRIGGER reject_order").Error; err != nil {
		t.Fatal(err)
	}
	orders := [][]uint{{group.Routes[2].ID, group.Routes[0].ID, group.Routes[1].ID}, {group.Routes[1].ID, group.Routes[2].ID, group.Routes[0].ID}}
	var wg sync.WaitGroup
	statuses := make(chan int, 2)
	for _, ids := range orders {
		wg.Add(1)
		go func(ids []uint) {
			defer wg.Done()
			statuses <- call(h, "PATCH", path, jsonString(routeOrderBody(group, ids)), cookie).Code
		}(ids)
	}
	wg.Wait()
	close(statuses)
	counts := map[int]int{}
	for code := range statuses {
		counts[code]++
	}
	if counts[204] != 1 || counts[409] != 1 {
		t.Fatalf("concurrent stale reorders not protected: %v", counts)
	}
}
