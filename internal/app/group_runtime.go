package app

import (
	"fmt"
	"time"

	"github.com/gin-gonic/gin"
)

func temporaryMemberFailure(state channelState) bool {
	return state.Status == 0 || state.Status >= 500 || state.Status == 408 || state.Status == 429
}

func (a *App) groupRetryAt(group Group, candidates []candidate, blockedChannels map[uint]bool) (time.Time, bool) {
	if group.Strategy == "manual" {
		return time.Time{}, false
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	now := time.Now()
	var next time.Time
	state := a.groupRouting[group.ID]
	probeBusy := group.Strategy == "priority" && state != nil && state.Probe != 0 && state.Revision == groupRoutingRevision(group)
	for _, item := range candidates {
		if blockedChannels[item.Channel.ID] {
			continue
		}
		health, route := a.health[item.Channel.ID], a.routeHealth[item.Route.ID]
		if !health.Until.IsZero() && !temporaryMemberFailure(health) || !route.Until.IsZero() && !temporaryMemberFailure(route) {
			continue
		}
		if health.Until.IsZero() && route.Until.IsZero() && !health.Probing && !route.Probing {
			continue
		}
		until := health.Until
		if route.Until.After(until) {
			until = route.Until
		}
		if health.Probing || route.Probing || probeBusy {
			until = maxTime(until, now.Add(time.Second))
		}
		until = maxTime(until, now)
		if next.IsZero() || until.Before(next) {
			next = until
		}
	}
	return next, !next.IsZero()
}

func maxTime(left, right time.Time) time.Time {
	if right.After(left) {
		return right
	}
	return left
}

func (a *App) groupSelectionReason(group Group, item candidate, probe bool, attempt int, previousRoute uint, previousError string) string {
	if attempt > 1 {
		kind := "同成员重试"
		if probe {
			kind = "恢复探测重试"
		}
		return fmt.Sprintf("%s（%d/%d）：%s", kind, attempt, group.relayConfig().MemberMaxAttempts, previousError)
	}
	if probe {
		return "冷却结束，进行恢复探测"
	}
	if previousRoute != 0 && previousRoute != item.Route.ID {
		return "上次尝试失败，切换到此成员：" + previousError
	}
	if group.Strategy == "manual" {
		return "使用手动选定的成员"
	}
	if group.Strategy == "weighted" {
		return "按权重分配到此成员"
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	state := a.groupRouting[group.ID]
	if state != nil && state.Revision == groupRoutingRevision(group) && state.Current == item.Route.ID && time.Now().Before(state.AffinityUntil) {
		return "备用亲和期内继续使用当前成员"
	}
	return "按优先级选择可用成员"
}

type routeRuntime struct {
	RouteID       uint       `json:"route_id"`
	State         string     `json:"state"`
	CooldownUntil *time.Time `json:"cooldown_until,omitempty"`
	Scope         string     `json:"scope,omitempty"`
	Error         string     `json:"error,omitempty"`
}

type groupRuntime struct {
	GroupID       uint           `json:"group_id"`
	CurrentRoute  uint           `json:"current_route_id"`
	ProbeRoute    uint           `json:"probe_route_id"`
	AffinityUntil *time.Time     `json:"affinity_until,omitempty"`
	Routes        []routeRuntime `json:"routes"`
}

func (a *App) groupRuntimeSnapshot(group Group, channels map[uint]Channel) groupRuntime {
	a.mu.Lock()
	defer a.mu.Unlock()
	now := time.Now()
	result := groupRuntime{GroupID: group.ID, Routes: []routeRuntime{}}
	state := a.groupRouting[group.ID]
	if state != nil && state.Revision == groupRoutingRevision(group) {
		result.CurrentRoute, result.ProbeRoute = state.Current, state.Probe
		if now.Before(state.AffinityUntil) {
			until := state.AffinityUntil
			result.AffinityUntil = &until
		}
	}
	for _, route := range group.Routes {
		health, member := a.health[route.ChannelID], a.routeHealth[route.ID]
		row := routeRuntime{RouteID: route.ID, State: "available"}
		cooldown, scope := member, "route"
		if !health.Until.IsZero() && !health.Until.Before(member.Until) {
			cooldown, scope = health, "channel"
		}
		if !cooldown.Until.IsZero() {
			until := cooldown.Until
			row.CooldownUntil, row.Scope, row.Error = &until, scope, cooldown.Error
		}
		_, enabled := channels[route.ChannelID]
		switch {
		case !enabled:
			row.State = "disabled"
			if route.ID == result.CurrentRoute {
				result.CurrentRoute, result.AffinityUntil = 0, nil
			}
		case now.Before(cooldown.Until):
			row.State = "cooling"
		case health.Probing || member.Probing:
			row.State = "probing"
		case group.Strategy == "manual" && route.ID != group.ActiveRouteID:
			row.State = "standby"
		case !cooldown.Until.IsZero():
			row.State = "recovery"
		case group.Strategy == "manual" && route.ID == group.ActiveRouteID || route.ID == result.CurrentRoute:
			row.State = "current"
		}
		result.Routes = append(result.Routes, row)
	}
	return result
}

func (a *App) groupRuntimeStatus(c *gin.Context) {
	groups := []Group{}
	if err := a.DB.Preload("Routes").Find(&groups).Error; err != nil {
		fail(c, 500, "读取路由运行状态失败")
		return
	}
	channels := a.availableChannels()
	result := make([]groupRuntime, 0, len(groups))
	for _, group := range groups {
		result = append(result, a.groupRuntimeSnapshot(group, channels))
	}
	c.JSON(200, result)
}
