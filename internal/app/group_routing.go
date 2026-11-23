package app

import (
	"errors"
	"net/http"
	"sort"
	"time"
)

type GroupRelayConfig struct {
	MemberMaxAttempts          int `json:"member_max_attempts"`
	MemberRetryIntervalSeconds int `json:"member_retry_interval_seconds"`
	MemberCooldownSeconds      int `json:"member_cooldown_seconds"`
	MemberAffinitySeconds      int `json:"member_affinity_seconds"`
}

func (group Group) relayConfig() GroupRelayConfig {
	if group.RelayConfig == nil {
		return GroupRelayConfig{MemberMaxAttempts: 2, MemberRetryIntervalSeconds: 3, MemberCooldownSeconds: 60, MemberAffinitySeconds: 300}
	}
	return *group.RelayConfig
}

func (config GroupRelayConfig) validate() error {
	if config.MemberMaxAttempts < 1 || config.MemberMaxAttempts > 32 || config.MemberRetryIntervalSeconds < 1 || config.MemberRetryIntervalSeconds > 3600 || config.MemberCooldownSeconds < 1 || config.MemberCooldownSeconds > 3600 || config.MemberAffinitySeconds < 0 || config.MemberAffinitySeconds > 86400 {
		return errors.New("单成员总尝试须为 1–32 次，重试间隔及冷却为 1–3600 秒，亲和为 0–86400 秒")
	}
	return nil
}

type groupRouteState struct {
	Revision        string
	Current         uint
	Probe           uint
	AffinityUntil   time.Time
	AffinityPending bool
}

func groupRoutingRevision(group Group) string {
	entries := make([]object, 0, len(group.Routes))
	for _, route := range group.Routes {
		entries = append(entries, object{"id": route.ID, "channel": route.ChannelID, "model": route.Model, "priority": route.Priority})
	}
	return digest(jsonString(object{"strategy": group.Strategy, "config": group.relayConfig(), "routes": entries}))
}

// 调用方持有 mu；配置、成员或顺序变化后丢弃旧亲和及探测状态。
func (a *App) groupRouteLocked(group Group) *groupRouteState {
	if a.groupRouting == nil {
		a.groupRouting = map[uint]*groupRouteState{}
	}
	revision := groupRoutingRevision(group)
	state := a.groupRouting[group.ID]
	if state == nil || state.Revision != revision {
		state = &groupRouteState{Revision: revision}
		a.groupRouting[group.ID] = state
	}
	return state
}

func (a *App) pickGroup(group Group, candidates []candidate, excludedRoutes, excludedChannels map[uint]bool) (candidate, bool, bool) {
	eligible := make([]candidate, 0, len(candidates))
	for _, item := range candidates {
		if !excludedRoutes[item.Route.ID] {
			eligible = append(eligible, item)
		}
	}
	if group.Strategy != "priority" {
		item, ok := a.pick(eligible, group.Strategy, excludedChannels)
		return item, ok, false
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	state := a.groupRouteLocked(group)
	now := time.Now()
	available := eligible[:0]
	currentAvailable := false
	for _, item := range eligible {
		health, routeHealth := a.health[item.Channel.ID], a.routeHealth[item.Route.ID]
		if excludedChannels[item.Channel.ID] || now.Before(health.Until) || health.Probing || now.Before(routeHealth.Until) || routeHealth.Probing {
			continue
		}
		available = append(available, item)
		if item.Route.ID == state.Current {
			currentAvailable = true
		}
	}
	if state.Current != 0 && !currentAvailable {
		state.Current, state.AffinityUntil, state.AffinityPending = 0, time.Time{}, true
	}
	sort.SliceStable(available, func(i, j int) bool {
		if available[i].Route.Priority != available[j].Route.Priority {
			return available[i].Route.Priority < available[j].Route.Priority
		}
		return available[i].Route.ID < available[j].Route.ID
	})
	for _, item := range available {
		if now.Before(state.AffinityUntil) && state.Current != item.Route.ID {
			continue
		}
		health, routeHealth := a.health[item.Channel.ID], a.routeHealth[item.Route.ID]
		probe := !health.Until.IsZero() || !routeHealth.Until.IsZero()
		if probe {
			if state.Probe != 0 {
				continue
			}
			state.Probe = item.Route.ID
		} else {
			state.Current = item.Route.ID
		}
		a.reserveProbe(item.Channel.ID)
		a.reserveRouteProbe(item.Route.ID)
		return item, true, probe
	}
	return candidate{}, false, false
}

func (a *App) releaseGroupProbe(group Group, item candidate) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if state := a.groupRouting[group.ID]; state != nil && state.Revision == groupRoutingRevision(group) && state.Probe == item.Route.ID {
		state.Probe = 0
	}
}

func (a *App) memberRetryAvailable(group Group, item candidate, probe bool) bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	health, routeHealth := a.health[item.Channel.ID], a.routeHealth[item.Route.ID]
	if probe {
		// 同一请求重试时保留探测占用，其他请求继续使用备用成员。
		state := a.groupRouting[group.ID]
		now := time.Now()
		return state != nil && state.Revision == groupRoutingRevision(group) && state.Probe == item.Route.ID &&
			!now.Before(health.Until) && !now.Before(routeHealth.Until) &&
			(health.Until.IsZero() || health.Probing) && (routeHealth.Until.IsZero() || routeHealth.Probing)
	}
	return health.Until.IsZero() && routeHealth.Until.IsZero() && !health.Probing && !routeHealth.Probing
}

func (a *App) recordMemberSuccess(group Group, item candidate, probe bool) bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	if group.Strategy != "priority" {
		delete(a.health, item.Channel.ID)
		delete(a.routeHealth, item.Route.ID)
		return true
	}
	state := a.groupRouting[group.ID]
	if state == nil || state.Revision != groupRoutingRevision(group) || probe && state.Probe != item.Route.ID || !probe && (state.Current != item.Route.ID || state.Probe == item.Route.ID) {
		return false
	}
	delete(a.health, item.Channel.ID)
	delete(a.routeHealth, item.Route.ID)
	if probe {
		state.Probe = 0
		if !time.Now().Before(state.AffinityUntil) {
			state.Current = item.Route.ID
		}
	}
	if state.Current == item.Route.ID && state.AffinityPending {
		state.AffinityPending = false
		state.AffinityUntil = time.Now().Add(time.Duration(group.relayConfig().MemberAffinitySeconds) * time.Second)
	}
	return true
}

func (a *App) recordMemberFailure(group Group, item candidate, channelFailure bool, header http.Header, status int, message string) {
	duration := time.Duration(item.Channel.CooldownSeconds) * time.Second
	if group.Strategy == "priority" {
		duration = time.Duration(group.relayConfig().MemberCooldownSeconds) * time.Second
	}
	until := time.Now().Add(duration)
	if status == 429 {
		until = retryAfter(header.Get("Retry-After"), time.Now(), duration)
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	health := channelState{Until: until, Error: message, Status: status}
	if channelFailure {
		health.Probing = a.health[item.Channel.ID].Probing
		a.health[item.Channel.ID] = health
	} else {
		if a.routeHealth == nil {
			a.routeHealth = map[uint]channelState{}
		}
		health.Probing = a.routeHealth[item.Route.ID].Probing
		a.routeHealth[item.Route.ID] = health
	}
	if state := a.groupRouting[group.ID]; state != nil && state.Revision == groupRoutingRevision(group) {
		if state.Current == item.Route.ID {
			state.Current, state.AffinityUntil, state.AffinityPending = 0, time.Time{}, true
		}
	}
}
