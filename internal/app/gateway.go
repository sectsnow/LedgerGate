package app

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"slices"
	"sort"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/gin-gonic/gin/render"
)

type candidate struct {
	Route   Route
	Channel Channel
}

func (a *App) availableChannels() map[uint]Channel {
	channels := []Channel{}
	accounts := []Account{}
	enabledChannels(a.DB).Find(&channels)
	a.DB.Find(&accounts)
	blocked := map[uint]bool{}
	for _, account := range accounts {
		fresh := account.CheckedAt != nil && time.Since(*account.CheckedAt) < time.Duration(account.IntervalSeconds*2)*time.Second
		blocked[account.ID] = account.BlockEmpty && fresh && (account.Status == "ok" || account.Status == "low") && account.Balance != nil && *account.Balance <= 0
	}
	byID := map[uint]Channel{}
	for _, channel := range channels {
		if channel.AccountID != nil && blocked[*channel.AccountID] {
			continue
		}
		byID[channel.ID] = channel
	}
	return byID
}

func groupCandidates(group Group, channels map[uint]Channel) []candidate {
	result := []candidate{}
	for _, route := range group.Routes {
		if group.Strategy == "manual" && route.ID != group.ActiveRouteID {
			continue
		}
		if channel, ok := channels[route.ChannelID]; ok {
			result = append(result, candidate{route, channel})
		}
	}
	return result
}

func (a *App) candidates(group Group) []candidate {
	return groupCandidates(group, a.availableChannels())
}

func (a *App) pick(candidates []candidate, strategy string, excluded map[uint]bool) (candidate, bool) {
	a.mu.Lock()
	defer a.mu.Unlock()
	eligible := []candidate{}
	for _, item := range candidates {
		state := a.health[item.Channel.ID]
		routeState := a.routeHealth[item.Route.ID]
		if excluded[item.Channel.ID] || time.Now().Before(state.Until) || state.Probing || time.Now().Before(routeState.Until) || routeState.Probing {
			continue
		}
		eligible = append(eligible, item)
	}
	if len(eligible) == 0 {
		return candidate{}, false
	}
	if strategy == "priority" || strategy == "manual" {
		sort.SliceStable(eligible, func(i, j int) bool {
			if eligible[i].Route.Priority != eligible[j].Route.Priority {
				return eligible[i].Route.Priority < eligible[j].Route.Priority
			}
			return eligible[i].Route.ID < eligible[j].Route.ID
		})
		a.reserveProbe(eligible[0].Channel.ID)
		a.reserveRouteProbe(eligible[0].Route.ID)
		return eligible[0], true
	}
	selected, total := eligible[0], 0
	for _, item := range eligible {
		a.weights[item.Route.ID] += item.Route.Weight
		total += item.Route.Weight
		if a.weights[item.Route.ID] > a.weights[selected.Route.ID] {
			selected = item
		}
	}
	a.weights[selected.Route.ID] -= total
	a.reserveProbe(selected.Channel.ID)
	a.reserveRouteProbe(selected.Route.ID)
	return selected, true
}

// 调用方已持有 mu，冷却结束时仅放行一个恢复探测。
func (a *App) reserveProbe(id uint) {
	state := a.health[id]
	if !state.Until.IsZero() {
		state.Probing = true
		a.health[id] = state
	}
}

func (a *App) releaseProbe(id uint) {
	a.mu.Lock()
	defer a.mu.Unlock()
	state, ok := a.health[id]
	if ok {
		state.Probing = false
		a.health[id] = state
	}
}

func setUpstreamAuth(req *http.Request, protocol, key string) {
	if protocol == "anthropic" {
		req.Header.Set("x-api-key", key)
		req.Header.Set("anthropic-version", "2023-06-01")
	} else {
		req.Header.Set("Authorization", "Bearer "+key)
	}
	req.Header.Set("User-Agent", "LedgerGate/0.1")
}

func (a *App) listModels(c *gin.Context) {
	groups := []Group{}
	if a.DB.Preload("Routes").Order("name").Find(&groups).Error != nil {
		fail(c, 500, "读取模型失败")
		return
	}
	models := []any{}
	allowed, _ := c.Get("allowed_models")
	allowedModels, _ := allowed.([]string)
	channels := a.availableChannels()
	for _, group := range groups {
		if len(allowedModels) > 0 && !slices.Contains(allowedModels, group.Name) {
			continue
		}
		if len(groupCandidates(group, channels)) == 0 {
			continue
		}
		models = append(models, object{"id": group.Name, "object": "model", "created": 0, "owned_by": "ledgergate"})
	}
	c.JSON(200, object{"object": "list", "data": models})
}

func estimateCost(input, output, inputPrice, outputPrice int64) int64 {
	if input < 0 || output < 0 || inputPrice < 0 || outputPrice < 0 {
		return 0
	}
	left := new(big.Int).Mul(big.NewInt(input), big.NewInt(inputPrice))
	right := new(big.Int).Mul(big.NewInt(output), big.NewInt(outputPrice))
	left.Add(left, right).Add(left, big.NewInt(500000)).Div(left, big.NewInt(1000000))
	if !left.IsInt64() {
		return int64(^uint64(0) >> 1)
	}
	return left.Int64()
}

func (a *App) forward(c *gin.Context, protocol string) {
	started := time.Now()
	options := a.runtimeOptions()
	requestCtx, stopRequest := context.WithTimeout(c.Request.Context(), time.Duration(options.RequestTimeoutSeconds)*time.Second)
	defer stopRequest()
	timings := requestTimings{Started: started}
	requestID := "req_" + randomToken()[:20]
	c.Header("X-Request-ID", requestID)
	log := RequestLog{RequestID: requestID, KeyID: c.GetUint("key_id"), KeyName: c.GetString("key_name"), Protocol: protocol, Status: 502, CreatedAt: started, StartedAt: &started, MetricsVersion: 2, CostStatus: "unknown"}
	trace := a.beginRequestTrace(&log)
	c.Set("request_trace", trace)
	responseWriter := &requestCaptureWriter{ResponseWriter: c.Writer, trace: trace, ctx: c.Request.Context()}
	c.Writer = responseWriter
	defer func() {
		log.RetryAt = nil
		timings.apply(&log)
		a.finishRequestTrace(trace, &log)
	}()
	raw, err := io.ReadAll(c.Request.Body)
	if err != nil {
		log.Status, log.Error = 413, "请求体读取失败或超过 32 MiB"
		fail(c, log.Status, log.Error)
		return
	}
	value, err := decodeJSON(raw)
	if err != nil {
		log.Status, log.Error = 400, "请求体必须为合法 JSON"
		fail(c, log.Status, log.Error)
		return
	}
	body, ok := value.(map[string]any)
	if !ok || str(body["model"]) == "" {
		log.Status, log.Error = 400, "请指定 model"
		fail(c, log.Status, log.Error)
		return
	}
	if stream, exists := body["stream"]; exists {
		if _, ok := stream.(bool); !ok {
			log.Status, log.Error = 400, "stream 必须为布尔值"
			fail(c, log.Status, log.Error)
			return
		}
	}
	log.Model, _ = body["model"].(string)
	if value, ok := c.Get("allowed_models"); ok {
		if allowed, _ := value.([]string); len(allowed) > 0 && !slices.Contains(allowed, log.Model) {
			log.Status, log.Error = 403, "此密钥不允许使用该模型"
			fail(c, 403, log.Error)
			return
		}
	}
	log.Stream, _ = body["stream"].(bool)
	log.ReasoningEffort = reasoningEffort(body)
	trace.setRequest(log, body)
	var group Group
	if a.DB.Preload("Routes").Where("name = ?", log.Model).First(&group).Error != nil {
		log.Status, log.Error = 404, "模型路由不存在"
		fail(c, log.Status, log.Error)
		return
	}
	var candidates []candidate
	excludedRoutes, excludedChannels := map[uint]bool{}, map[uint]bool{}
	blockedChannels := map[uint]bool{}
	memberAttempts := map[uint]int{}
	var previousRoute uint
	var retry *candidate
	retryProbe := false
	retryRevision := ""
	var releasePick func()
	defer func() {
		if releasePick != nil {
			releasePick()
		}
	}()
	// 上游暂时失败先留在当前成员重试；等待同样受整次请求时间预算约束。
	failure := func(item candidate, probe, retryable, channelFailure bool, header http.Header) {
		if requestCtx.Err() != nil {
			return
		}
		config := group.relayConfig()
		if group.Strategy == "priority" && retryable && memberAttempts[item.Route.ID] < config.MemberMaxAttempts {
			if log.Attempts < options.MaxAttempts {
				interval := time.Duration(config.MemberRetryIntervalSeconds) * time.Second
				trace.waitForRoute(&log, "等待同成员重试："+log.Error, time.Now().Add(interval))
				timer := time.NewTimer(interval)
				defer timer.Stop()
				select {
				case <-requestCtx.Done():
				case <-timer.C:
					retry, retryProbe, retryRevision = &item, probe, groupRoutingRevision(group)
				}
			}
			return
		}
		a.recordMemberFailure(group, item, channelFailure, header, log.Status, log.Error)
	}
	for {
		if releasePick != nil && retry == nil {
			releasePick()
			releasePick = nil
		}
		if c.Request.Context().Err() != nil {
			log.Status, log.Error = 499, "客户端已断开"
			return
		}
		if requestCtx.Err() != nil {
			log.Status, log.Error = 504, "请求已达到总时间上限"
			break
		}
		if log.Attempts >= options.MaxAttempts {
			if log.Error != "" {
				log.Error += "；"
			}
			log.Error += "已达到上游尝试次数上限"
			break
		}
		if err := a.DB.WithContext(requestCtx).Preload("Routes").First(&group, group.ID).Error; err != nil {
			if c.Request.Context().Err() != nil {
				log.Status, log.Error = 499, "客户端已断开"
				return
			}
			if requestCtx.Err() != nil {
				log.Status, log.Error = 504, "请求已达到总时间上限"
				break
			}
			log.Status, log.Error = 404, "模型路由已不存在或无法读取"
			fail(c, log.Status, log.Error)
			return
		}
		candidates = a.candidates(group)
		if protocol == "responses" && (body["previous_response_id"] != nil || body["conversation"] != nil) {
			// shortcut: 有状态续接限定单一渠道，多渠道续接需持久化响应归属。
			channels := map[uint]bool{}
			for _, candidate := range candidates {
				channels[candidate.Channel.ID] = true
			}
			if len(channels) > 1 {
				log.Status, log.Error = 400, "有状态 Responses 续接需要配置单一渠道的模型路由"
				fail(c, log.Status, log.Error)
				return
			}
		}
		compatible, compatibilityError := compatibleCandidates(candidates, body, protocol)
		if len(compatible) == 0 && compatibilityError != nil {
			log.Status, log.Error = 400, "所有可选成员均无法处理本次请求："+compatibilityError.Error()
			fail(c, log.Status, log.Error)
			return
		}
		skippedIncompatible := len(candidates) - len(compatible)
		candidates = compatible
		var item candidate
		ok, probe := false, false
		if retry != nil && retryRevision == groupRoutingRevision(group) {
			for _, current := range candidates {
				if current.Route.ID == retry.Route.ID && memberAttempts[current.Route.ID] < group.relayConfig().MemberMaxAttempts && a.memberRetryAvailable(group, current, retryProbe) {
					item, ok, probe = current, true, retryProbe
					break
				}
			}
		}
		retry = nil
		if !ok {
			if releasePick != nil {
				releasePick()
				releasePick = nil
			}
			item, ok, probe = a.pickGroup(group, candidates, excludedRoutes, excludedChannels)
		}
		if !ok {
			until, waitable := a.groupRetryAt(group, candidates, blockedChannels)
			if !waitable {
				break
			}
			trace.waitForRoute(&log, "成员暂不可用，等待冷却或恢复探测结束", until)
			// 短间隔重读配置，让停用、改路由及其他请求的恢复结果及时生效。
			timer := time.NewTimer(min(max(time.Until(until), 0), time.Second))
			select {
			case <-requestCtx.Done():
			case <-timer.C:
			}
			timer.Stop()
			excludedRoutes, excludedChannels, memberAttempts = map[uint]bool{}, map[uint]bool{}, map[uint]int{}
			for id := range blockedChannels {
				excludedChannels[id] = true
			}
			continue
		}
		excludedRoutes[item.Route.ID] = true
		if group.Strategy != "priority" {
			excludedChannels[item.Channel.ID] = true
		}
		pickedGroup := group
		releasePick = func() {
			if pickedGroup.Strategy != "priority" || probe {
				a.releaseProbe(item.Channel.ID)
				a.releaseRouteProbe(item.Route.ID)
				a.releaseGroupProbe(pickedGroup, item)
			}
		}
		// 前一次上游等待期间可能停用了渠道或暂停了账户，故障切换前重新核对状态。
		var current Channel
		if enabledChannels(a.DB.WithContext(c.Request.Context())).Select("id").First(&current, item.Channel.ID).Error != nil {
			continue
		}
		converted, upstreamProtocol, err := item.Channel.prepareRequest(body, protocol)
		if err != nil {
			log.Status, log.Error = 400, err.Error()
			if group.Strategy == "priority" {
				fail(c, log.Status, log.Error)
				return
			}
			continue
		}
		// 每次尝试使用独立副本，保留客户端模型名供后续路由使用。
		outbound := object{}
		for key, value := range converted {
			outbound[key] = value
		}
		outbound["model"] = item.Route.Model
		if log.Stream && upstreamProtocol == "openai" {
			options := object{}
			for key, value := range obj(outbound["stream_options"]) {
				options[key] = value
			}
			options["include_usage"] = true
			outbound["stream_options"] = options
		}
		key, err := a.unseal(item.Channel.APIKey)
		if err != nil {
			log.Error = err.Error()
			if group.Strategy == "priority" {
				log.Status = 500
				fail(c, log.Status, log.Error)
				return
			}
			continue
		}
		var attemptCtx context.Context
		var cancel context.CancelFunc
		firstOutputTimeout := time.Duration(options.FirstOutputSeconds) * time.Second
		if log.Stream {
			// 流式渠道期限只约束首输出，生成阶段由空闲和整次请求预算约束。
			attemptCtx, cancel = context.WithCancel(requestCtx)
			firstOutputTimeout = min(firstOutputTimeout, time.Duration(item.Channel.TimeoutSeconds)*time.Second)
		} else {
			attemptCtx, cancel = context.WithTimeoutCause(requestCtx, time.Duration(item.Channel.TimeoutSeconds)*time.Second, errChannelResponseTimeout)
		}
		ctx, cancelCause := context.WithCancelCause(attemptCtx)
		guard := newUpstreamGuard(cancelCause, firstOutputTimeout, time.Duration(options.StreamIdleSeconds)*time.Second)
		guard.stream = log.Stream
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint(item.Channel.BaseURL, requestPath(upstreamProtocol)), bytes.NewBufferString(jsonString(outbound)))
		if err != nil {
			guard.Close()
			cancel()
			log.Error = "上游请求地址无效"
			if group.Strategy == "priority" {
				log.Status = 500
				fail(c, log.Status, log.Error)
				return
			}
			continue
		}
		setUpstreamAuth(req, upstreamProtocol, key)
		copyNativeRequestHeaders(req, c.Request, protocol, upstreamProtocol)
		req.Header.Set("Content-Type", "application/json")
		if log.Stream {
			req.Header.Set("Accept", "text/event-stream")
		}
		log.ChannelID, log.Attempts = item.Channel.ID, log.Attempts+1
		memberAttempts[item.Route.ID]++
		log.RoutingReason = a.groupSelectionReason(group, item, probe, memberAttempts[item.Route.ID], previousRoute, log.Error)
		if skippedIncompatible > 0 {
			log.RoutingReason = fmt.Sprintf("跳过 %d 个不兼容成员；%s", skippedIncompatible, log.RoutingReason)
		}
		log.RetryAt = nil
		log.FinishReason, log.IncompleteReason = "", ""
		previousRoute = item.Route.ID
		rate := a.recordRequestRoute(&log, item.Route, item.Channel, upstreamProtocol)
		timings.First, timings.FirstContent, timings.Finished = time.Time{}, time.Time{}, time.Time{}
		a.traceAttempt(trace, log)
		resp, err := a.doRequest(req, item.Channel.UseProxy)
		if err != nil {
			cause := context.Cause(ctx)
			guard.Close()
			cancel()
			if c.Request.Context().Err() != nil {
				log.Status, log.Error = 499, "客户端已断开"
				return
			}
			log.Status, log.Error = 502, err.Error()
			if cause != nil {
				log.Error = cause.Error()
			}
			if requestCtx.Err() != nil {
				log.Status, log.Error = 504, "请求已达到总时间上限"
				trace.endAttempt(log.Status, log.Error)
				break
			}
			var connectionErr upstreamConnectionError
			connectionFailure := errors.As(err, &connectionErr)
			if cause == nil && !connectionFailure {
				log.Status = 500
				trace.endAttempt(log.Status, log.Error)
				fail(c, log.Status, log.Error)
				return
			}
			trace.endAttempt(log.Status, log.Error)
			failure(item, probe, true, connectionFailure && cause == nil, nil)
			continue
		}
		trace.attemptResponse(resp)
		if resp.StatusCode < 200 || resp.StatusCode >= 300 {
			guard.Close()
			resp.Body.Close()
			cancel()
			log.Status, log.Error = resp.StatusCode, fmt.Sprintf("上游返回 HTTP %d", resp.StatusCode)
			trace.endAttempt(log.Status, log.Error)
			if resp.StatusCode == 400 || resp.StatusCode == 422 || resp.StatusCode >= 400 && resp.StatusCode < 500 && resp.StatusCode != 401 && resp.StatusCode != 403 && resp.StatusCode != 404 && resp.StatusCode != 408 && resp.StatusCode != 429 {
				fail(c, log.Status, log.Error)
				return
			}
			channelFailure := resp.StatusCode == 401 || resp.StatusCode == 403
			if channelFailure {
				excludedChannels[item.Channel.ID] = true
				blockedChannels[item.Channel.ID] = true
			}
			failure(item, probe, resp.StatusCode >= 500 || resp.StatusCode == 408, channelFailure, resp.Header)
			continue
		}
		guard.attach(resp.Body)
		resp.Body = guard
		c.Set("upstream_guard", guard)
		if group.Strategy == "priority" {
			c.Set("upstream_committed", func() {
				if !a.recordMemberSuccess(pickedGroup, item, probe) && releasePick != nil {
					releasePick()
				}
				releasePick = nil
			})
		} else {
			c.Set("upstream_committed", nil)
		}
		var result completion
		if log.Stream {
			result, err = a.pipeStream(c, resp, upstreamProtocol, protocol, log.Model, obj(body["stream_options"])["include_usage"] == true, &timings)
		} else {
			result, err = a.pipeResponse(c, resp, upstreamProtocol, protocol, log.Model)
		}
		if err != nil && context.Cause(ctx) != nil {
			err = context.Cause(ctx)
		}
		log.FinishReason, log.IncompleteReason = boundedDiagnostic(result.Finish), boundedDiagnostic(result.IncompleteReason)
		trace.attemptOutcome(result)
		timings.Finished = time.Now()
		resp.Body.Close()
		cancel()
		a.recordRequestCost(&log, result, group, item.Route, item.Channel, rate)
		if err == nil {
			// 完成响应已成功写出，之后的连接关闭不再改变结果。
			log.Status, log.Error = 200, ""
			trace.endAttempt(log.Status, log.Error)
			if !log.Stream || group.Strategy != "priority" {
				if a.recordMemberSuccess(group, item, probe) {
					releasePick = nil
				}
			}
			return
		}
		log.Status, log.Error = 502, err.Error()
		if c.Request.Context().Err() != nil || responseWriter.writeErr != nil {
			log.Status, log.Error = 499, "客户端已断开"
			return
		}
		var processingErr responseProcessingError
		localError := errors.As(err, &processingErr)
		if localError {
			log.Status = 500
		}
		if requestCtx.Err() != nil {
			log.Status, log.Error = 504, "请求已达到总时间上限"
		}
		trace.endAttempt(log.Status, log.Error)
		// 已出流的失败只影响本次请求，不重放，也不据此冷却成员。
		if !localError && requestCtx.Err() == nil && !c.Writer.Written() {
			failure(item, probe, true, false, nil)
		}
		if c.Writer.Written() {
			if log.Stream {
				errorType := "upstream_error"
				if localError || requestCtx.Err() != nil {
					errorType = "ledgergate_error"
				}
				_ = writeFrame(c.Writer, "error", jsonString(object{"type": "error", "error": object{"type": errorType, "message": log.Error}}))
				c.Writer.Flush()
			}
			return
		}
		if localError {
			fail(c, log.Status, log.Error)
			return
		}
	}
	if log.Error == "" {
		log.Status, log.Error = 503, "暂无可用渠道，请检查启用状态、余额限制与冷却时间"
	}
	if log.Status < 400 || log.Status == 401 || log.Status == 403 || log.Status == 404 || log.Status == 429 {
		log.Status = 502
	}
	fail(c, log.Status, log.Error)
}

func (a *App) pipeResponse(c *gin.Context, resp *http.Response, from, to, model string) (completion, error) {
	result := completion{}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, (32<<20)+1))
	if err != nil {
		return result, errors.New("上游响应读取失败")
	}
	if len(raw) > 32<<20 {
		return result, responseProcessingError("上游响应超过 32 MiB")
	}
	value, err := decodeJSON(raw)
	if err != nil {
		return result, errors.New("上游未返回合法 JSON")
	}
	body := obj(value)
	if body["error"] != nil {
		return result, errors.New("上游返回错误响应")
	}
	if from == to {
		if err := validateCompletion(body, from); err != nil {
			return result, err
		}
		result.applyUsage(body, from)
		result.captureEnd(body, from)
		body["model"] = model
		copyNativeResponseHeaders(c.Writer.Header(), resp.Header)
		c.Status(200)
		return result, (render.JSON{Data: body}).Render(c.Writer)
	}
	result, err = parseCompletion(body, from)
	if err != nil {
		return result, err
	}
	converted, err := completionBody(result, to, model)
	if err != nil {
		return result, err
	}
	c.Status(200)
	return result, (render.JSON{Data: converted}).Render(c.Writer)
}

func (a *App) pipeStream(c *gin.Context, resp *http.Response, from, to, model string, includeUsage bool, metrics ...*requestTimings) (completion, error) {
	result := completion{}
	traceValue, _ := c.Get("request_trace")
	trace, _ := traceValue.(*requestTrace)
	scanner := bufio.NewScanner(resp.Body)
	scanner.Buffer(make([]byte, 4096), 4<<20)
	writer := streamWriter{writer: c.Writer, protocol: to, model: model, includeUsage: includeUsage}
	var pending []sseEvent
	pendingBytes := 0
	committed := false
	for {
		event, err := readEvent(scanner)
		received := time.Now()
		if err != nil {
			if errors.Is(err, bufio.ErrTooLong) {
				return result, responseProcessingError("单行 SSE 数据超过 4 MiB")
			}
			if errors.Is(err, io.EOF) {
				return result, errors.New("上游流提前结束，缺少终止事件")
			}
			return result, errors.New("上游流读取失败")
		}
		deltas, done, err := inspectEvent(event, from, &result, from != to)
		if err != nil {
			return result, err
		}
		for _, timing := range metrics {
			if timing != nil && timing.observe(event, from, received) && trace != nil {
				trace.recordTimings(*timing)
			}
		}
		if trace != nil {
			trace.mu.Lock()
			if trace.log.FirstByteMS == nil {
				ms := time.Since(trace.log.CreatedAt).Milliseconds()
				trace.log.FirstByteMS = &ms
			}
			trace.mu.Unlock()
			trace.usage(result)
		}
		output := streamEventHasOutput(event, from)
		if from == to {
			output = streamEventStartsOutput(event, from)
		}
		if output || done {
			if value, exists := c.Get("upstream_guard"); exists {
				value.(*upstreamGuard).output()
			}
		} else if !committed {
			if from != to {
				// 转换所需的 ID 和用量已提取，无须缓存不会转发的前置事件。
				continue
			}
			pendingBytes += len(event.Data) + len(event.Event)
			if pendingBytes <= 1<<20 && len(pending) < 1024 {
				pending = append(pending, event)
				continue
			}
			// 同协议缓冲达到上限后按序透传，继续保留首输出和空闲超时保护。
		}
		if !committed && from == to {
			copyNativeResponseHeaders(c.Writer.Header(), resp.Header)
		}
		committed = true
		c.Header("Content-Type", "text/event-stream; charset=utf-8")
		c.Header("Cache-Control", "no-cache")
		c.Header("X-Accel-Buffering", "no")
		if from == to {
			for _, previous := range pending {
				value, _ := decodeJSON([]byte(previous.Data))
				mapped := obj(value)
				for _, target := range []object{mapped, obj(mapped["message"]), obj(mapped["response"])} {
					if _, ok := target["model"]; ok {
						target["model"] = model
					}
				}
				if err := writeFrame(c.Writer, previous.Event, jsonString(mapped)); err != nil {
					return result, err
				}
			}
			pending = nil
			if event.Data != "[DONE]" {
				value, _ := decodeJSON([]byte(event.Data))
				mapped := obj(value)
				if to == "openai" && !includeUsage && len(array(mapped["choices"])) == 0 && mapped["usage"] != nil {
					continue
				}
				for _, target := range []object{mapped, obj(mapped["message"]), obj(mapped["response"])} {
					if _, ok := target["model"]; ok {
						target["model"] = model
					}
				}
				event.Data = jsonString(mapped)
			}
			if err := writeFrame(c.Writer, event.Event, event.Data); err != nil {
				return result, err
			}
			if err := http.NewResponseController(c.Writer).Flush(); err != nil {
				return result, err
			}
		} else {
			pending = nil
			if err := writer.start(result); err != nil {
				return result, err
			}
			for _, delta := range deltas {
				if err := writer.delta(delta); err != nil {
					return result, err
				}
			}
		}
		if callback, ok := c.Get("upstream_committed"); ok && callback != nil {
			callback.(func())()
			c.Set("upstream_committed", nil)
		}
		if done {
			if from != to {
				if err := writer.finish(&result); err != nil {
					return result, err
				}
			}
			return result, nil
		}
	}
}
