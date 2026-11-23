package app

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"
)

var errFirstOutput = errors.New("等待上游首个有效输出超时")
var errStreamIdle = errors.New("上游流长时间无数据")
var errChannelResponseTimeout = errors.New("等待上游完整响应超时")

type upstreamGuard struct {
	mu           sync.Mutex
	first        *time.Timer
	idle         *time.Timer
	closed       bool
	firstDone    bool
	cancel       context.CancelCauseFunc
	body         io.ReadCloser
	idleDuration time.Duration
	stream       bool
}

func newUpstreamGuard(cancel context.CancelCauseFunc, first, idle time.Duration) *upstreamGuard {
	guard := &upstreamGuard{cancel: cancel, idleDuration: idle}
	guard.first = time.AfterFunc(first, func() { guard.expire(errFirstOutput) })
	return guard
}

func (guard *upstreamGuard) expire(reason error) {
	guard.mu.Lock()
	defer guard.mu.Unlock()
	if !guard.closed && !(reason == errFirstOutput && guard.firstDone) {
		guard.cancel(reason)
	}
}

func (guard *upstreamGuard) attach(body io.ReadCloser) {
	guard.mu.Lock()
	defer guard.mu.Unlock()
	guard.body = body
	if guard.stream {
		guard.idle = time.AfterFunc(guard.idleDuration, func() { guard.expire(errStreamIdle) })
	}
}

func (guard *upstreamGuard) Read(data []byte) (int, error) {
	n, err := guard.body.Read(data)
	guard.mu.Lock()
	if !guard.closed && n > 0 {
		if !guard.stream {
			guard.firstDone = true
			guard.first.Stop()
		}
		if guard.idle != nil {
			guard.idle.Reset(guard.idleDuration)
		}
	}
	guard.mu.Unlock()
	return n, err
}

func (guard *upstreamGuard) output() {
	guard.mu.Lock()
	defer guard.mu.Unlock()
	guard.firstDone = true
	if guard.first != nil {
		guard.first.Stop()
	}
}

func (guard *upstreamGuard) Close() error {
	guard.mu.Lock()
	guard.closed = true
	guard.first.Stop()
	if guard.idle != nil {
		guard.idle.Stop()
	}
	body := guard.body
	guard.mu.Unlock()
	if body != nil {
		return body.Close()
	}
	return nil
}

func retryAfter(value string, now time.Time, fallback time.Duration) time.Time {
	value = strings.TrimSpace(value)
	if seconds, err := strconv.ParseInt(value, 10, 64); err == nil && seconds >= 0 {
		return now.Add(time.Duration(min(seconds, 3600)) * time.Second)
	}
	if date, err := http.ParseTime(value); err == nil && date.After(now) {
		if date.After(now.Add(time.Hour)) {
			return now.Add(time.Hour)
		}
		return date
	}
	return now.Add(fallback)
}

func (a *App) reserveRouteProbe(id uint) {
	state := a.routeHealth[id]
	if !state.Until.IsZero() {
		state.Probing = true
		a.routeHealth[id] = state
	}
}

func (a *App) releaseRouteProbe(id uint) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if state, exists := a.routeHealth[id]; exists {
		state.Probing = false
		a.routeHealth[id] = state
	}
}
