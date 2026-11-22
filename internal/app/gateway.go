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

func (a *App) markFailed(channel Channel, message string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.health[channel.ID] = channelState{Until: time.Now().Add(time.Duration(channel.CooldownSeconds) * time.Second), Error: message}
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
	log := RequestLog{RequestID: requestID, KeyID: c.GetUint("key_id"), KeyName: c.GetString("key_name"), Protocol: protocol, Status: 502, CreatedAt: started, StartedAt: &started, MetricsVersion: 1, CostStatus: "unknown"}
	trace := a.beginRequestTrace(&log)
	c.Set("request_trace", trace)
	c.Writer = &requestCaptureWriter{ResponseWriter: c.Writer, trace: trace}
	defer func() {
		if c.Request.Context().Err() != nil && log.Status < 400 {
			log.Status, log.Error = 499, "客户端已断开"
		}
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
	candidates := a.candidates(group)
	if protocol == "responses" && (body["previous_response_id"] != nil || body["conversation"] != nil) {
		channels := map[uint]bool{}
		for _, item := range candidates {
			channels[item.Channel.ID] = true
		}
		// shortcut: 有状态续接限定单一渠道，需要多渠道时再实现持久化会话亲和。
		if len(channels) > 1 {
			log.Status, log.Error = 400, "有状态 Responses 续接需要配置单一渠道的模型路由"
			fail(c, 400, log.Error)
			return
		}
	}
	excluded := map[uint]bool{}
	for {
		if c.Request.Context().Err() != nil {
			log.Status, log.Error = 499, "客户端已断开"
			return
		}
		if requestCtx.Err() != nil {
			log.Status, log.Error = 504, "请求已达到总时间上限"
			break
		}
		if log.Attempts >= options.MaxAttempts {
			log.Error = "已达到上游尝试次数上限"
			break
		}
		item, ok := a.pick(candidates, group.Strategy, excluded)
		if !ok {
			break
		}
		excluded[item.Channel.ID] = true
		defer a.releaseProbe(item.Channel.ID)
		defer a.releaseRouteProbe(item.Route.ID)
		// 前一次上游等待期间可能停用了渠道或暂停了账户，故障切换前重新核对状态。
		var current Channel
		if enabledChannels(a.DB.WithContext(c.Request.Context())).Select("id").First(&current, item.Channel.ID).Error != nil {
			continue
		}
		converted, upstreamProtocol, err := item.Channel.prepareRequest(body, protocol)
		if err != nil {
			log.Status, log.Error = 400, err.Error()
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
			continue
		}
		attemptCtx, cancel := context.WithTimeout(requestCtx, time.Duration(item.Channel.TimeoutSeconds)*time.Second)
		ctx, cancelCause := context.WithCancelCause(attemptCtx)
		guard := newUpstreamGuard(cancelCause, time.Duration(options.FirstOutputSeconds)*time.Second, time.Duration(options.StreamIdleSeconds)*time.Second)
		guard.stream = log.Stream
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint(item.Channel.BaseURL, requestPath(upstreamProtocol)), bytes.NewBufferString(jsonString(outbound)))
		if err != nil {
			guard.Close()
			cancel()
			log.Error = "上游请求地址无效"
			continue
		}
		setUpstreamAuth(req, upstreamProtocol, key)
		req.Header.Set("Content-Type", "application/json")
		if log.Stream {
			req.Header.Set("Accept", "text/event-stream")
		}
		log.ChannelID, log.Attempts = item.Channel.ID, log.Attempts+1
		rate := a.recordRequestRoute(&log, item.Route, item.Channel, upstreamProtocol)
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
			trace.endAttempt(log.Status, log.Error)
			a.markFailed(item.Channel, log.Error)
			continue
		}
		if resp.StatusCode < 200 || resp.StatusCode >= 300 {
			guard.Close()
			resp.Body.Close()
			cancel()
			log.Status, log.Error = resp.StatusCode, fmt.Sprintf("上游返回 HTTP %d", resp.StatusCode)
			trace.endAttempt(log.Status, log.Error)
			if resp.StatusCode == 400 || resp.StatusCode == 422 {
				fail(c, log.Status, log.Error)
				return
			}
			a.markRouteFailed(item.Route, item.Channel, resp.StatusCode, resp.Header, log.Error)
			continue
		}
		guard.attach(resp.Body)
		resp.Body = guard
		c.Set("upstream_guard", guard)
		var result completion
		if log.Stream {
			result, err = a.pipeStream(c, resp, upstreamProtocol, protocol, log.Model, obj(body["stream_options"])["include_usage"] == true, &timings)
		} else {
			result, err = a.pipeResponse(c, resp, upstreamProtocol, protocol, log.Model)
		}
		if err != nil && context.Cause(ctx) != nil {
			err = context.Cause(ctx)
		}
		timings.Finished = time.Now()
		resp.Body.Close()
		cancel()
		a.recordRequestCost(&log, result, group, item.Route, item.Channel, rate)
		if err == nil {
			log.Status, log.Error = 200, ""
			trace.endAttempt(log.Status, log.Error)
			a.mu.Lock()
			delete(a.health, item.Channel.ID)
			delete(a.routeHealth, item.Route.ID)
			a.mu.Unlock()
			return
		}
		log.Status, log.Error = 502, err.Error()
		if c.Request.Context().Err() != nil {
			log.Status, log.Error = 499, "客户端已断开"
			return
		}
		trace.endAttempt(log.Status, log.Error)
		a.markFailed(item.Channel, log.Error)
		if c.Writer.Written() {
			if log.Stream {
				_ = writeFrame(c.Writer, "error", jsonString(object{"type": "error", "error": object{"type": "upstream_error", "message": log.Error}}))
				c.Writer.Flush()
			}
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
	if err != nil || len(raw) > 32<<20 {
		return result, errors.New("上游响应读取失败或超过 32 MiB")
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
		body["model"] = model
		c.JSON(200, body)
		return result, nil
	}
	result, err = parseCompletion(body, from)
	if err != nil {
		return result, err
	}
	converted, err := completionBody(result, to, model)
	if err != nil {
		return result, err
	}
	c.JSON(200, converted)
	return result, nil
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
		if err != nil {
			if errors.Is(err, io.EOF) {
				return result, errors.New("上游流提前结束，缺少终止事件")
			}
			return result, errors.New("上游流读取失败或事件超过 4 MiB")
		}
		deltas, done, err := inspectEvent(event, from, &result, from != to)
		if err != nil {
			return result, err
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
		if streamEventHasOutput(event, from) || done {
			if value, exists := c.Get("upstream_guard"); exists {
				value.(*upstreamGuard).output()
			}
		} else if !committed {
			pendingBytes += len(event.Data) + len(event.Event)
			if pendingBytes > 1<<20 || len(pending) >= 1024 {
				return result, errors.New("上游首输出之前的事件过多")
			}
			pending = append(pending, event)
			continue
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
			c.Writer.Flush()
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
		for _, timings := range metrics {
			if timings != nil {
				if timings.observe(event, from) && trace != nil {
					trace.recordFirstToken(timings.First.Sub(timings.Started).Milliseconds())
				}
			}
		}
		if done {
			if from != to {
				if err := writer.finish(&result); err != nil {
					return result, err
				}
				c.Writer.Flush()
			}
			return result, nil
		}
	}
}
