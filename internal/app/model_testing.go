package app

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

type modelTestInput struct {
	KeyChannelID uint   `json:"key_channel_id"`
	APIKey       string `json:"api_key"`
	Protocol     string `json:"protocol"`
	Model        string `json:"model"`
	Prompt       string `json:"prompt"`
	Stream       bool   `json:"stream"`
	MaxTokens    int    `json:"max_tokens"`
	LimitField   string `json:"limit_field"`
}

type modelTestResult struct {
	Success        bool   `json:"success"`
	Status         int    `json:"status"`
	DurationMS     int64  `json:"duration_ms"`
	FirstTokenMS   *int64 `json:"first_token_ms"`
	FirstContentMS *int64 `json:"first_content_ms"`
	GenerationMS   *int64 `json:"generation_ms"`
	InputTokens    *int64 `json:"input_tokens"`
	OutputTokens   *int64 `json:"output_tokens"`
	CacheRead      *int64 `json:"cache_read_tokens"`
	CacheWrite     *int64 `json:"cache_write_tokens"`
	UsageWarning   string `json:"usage_warning"`
	Text           string `json:"text"`
	Error          string `json:"error"`
}

type modelTestModels struct {
	Models   []string `json:"models"`
	Revision string   `json:"revision"`
	saved    []string
	routes   []Route
}

func loadModelTestModels(db *gorm.DB, channel Channel) (modelTestModels, error) {
	result := modelTestModels{Models: []string{}, saved: []string{}}
	var err error
	if channel.Models != "" {
		result.saved, err = readChannelModels(channel.Models)
		if err != nil {
			return result, err
		}
	}
	if err := db.Select("id", "group_id", "model").Where("channel_id = ?", channel.ID).Order("id").Find(&result.routes).Error; err != nil {
		return result, errors.New("读取渠道模型路由失败")
	}
	result.Models = append(result.Models, result.saved...)
	for _, route := range result.routes {
		if !slices.Contains(result.Models, route.Model) {
			result.Models = append(result.Models, route.Model)
		}
	}
	result.Revision = digest(channelModelRevision(channel) + jsonString(result.routes))
	return result, nil
}

func (a *App) modelTestChannel(c *gin.Context) (Channel, bool) {
	id, ok := pathID(c)
	if !ok {
		return Channel{}, false
	}
	var channel Channel
	if err := a.DB.WithContext(c.Request.Context()).First(&channel, id).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			fail(c, 404, "渠道不存在，请刷新后重试")
		} else {
			fail(c, 500, "读取渠道失败")
		}
		return channel, false
	}
	return channel, true
}

func (a *App) modelTestKey(c *gin.Context, channel Channel, input modelTestInput) (string, bool) {
	if !validProtocol(input.Protocol) {
		fail(c, 400, "请选择有效的测试协议")
		return "", false
	}
	if input.APIKey != "" {
		if input.KeyChannelID != 0 || len(input.APIKey) > 8192 || !completeUpstreamKey(input.APIKey) {
			fail(c, 400, "请提供完整的临时 Key，且不能同时选择已保存的 Key")
			return "", false
		}
		return input.APIKey, true
	}
	if input.KeyChannelID != 0 && input.KeyChannelID != channel.ID {
		var source Channel
		if a.DB.WithContext(c.Request.Context()).First(&source, input.KeyChannelID).Error != nil || source.BaseURL != channel.BaseURL {
			fail(c, 400, "只能选择相同上游地址的渠道 Key")
			return "", false
		}
		channel = source
	}
	key, err := a.unseal(channel.APIKey)
	if err != nil || !completeUpstreamKey(key) {
		fail(c, 400, "渠道未保存有效 Key，请重新配置或输入临时 Key")
		return "", false
	}
	return key, true
}

func (a *App) channelTestModels(c *gin.Context) {
	channel, ok := a.modelTestChannel(c)
	if !ok {
		return
	}
	models, err := loadModelTestModels(a.DB.WithContext(c.Request.Context()), channel)
	if err != nil {
		fail(c, 409, err.Error())
		return
	}
	if c.Request.Method == http.MethodPost {
		var input modelTestInput
		c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, 64<<10)
		if c.ShouldBindJSON(&input) != nil {
			fail(c, 400, "测试参数无效")
			return
		}
		key, ok := a.modelTestKey(c, channel, input)
		if !ok {
			return
		}
		ctx, cancel := context.WithTimeout(c.Request.Context(), 30*time.Second)
		defer cancel()
		available, err := a.fetchModels(ctx, channel, input.Protocol, key)
		if err == nil {
			available, err = normalizeChannelModels(available)
		}
		if err != nil {
			fail(c, 502, err.Error())
			return
		}
		for _, model := range available {
			if !slices.Contains(models.Models, model) {
				models.Models = append(models.Models, model)
			}
		}
	}
	c.JSON(200, models)
}

func (a *App) testChannelModel(c *gin.Context) {
	var input modelTestInput
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, 64<<10)
	if c.ShouldBindJSON(&input) != nil {
		fail(c, 400, "测试参数无效")
		return
	}
	models, err := normalizeChannelModels([]string{input.Model})
	if err != nil || strings.TrimSpace(input.Prompt) == "" || len(input.Prompt) > 4096 || input.MaxTokens < 1 || input.MaxTokens > 8192 || (input.LimitField != "" && input.LimitField != "max_tokens" && input.LimitField != "max_completion_tokens") {
		fail(c, 400, "请提供有效模型、1–4096 字节的测试内容和 1–8192 的输出上限")
		return
	}
	input.Model = models[0]
	channel, ok := a.modelTestChannel(c)
	if !ok {
		return
	}
	key, ok := a.modelTestKey(c, channel, input)
	if !ok {
		return
	}
	a.mu.Lock()
	if a.modelTests >= 10 {
		a.mu.Unlock()
		fail(c, 429, "同时最多测试 10 个模型，请稍后重试")
		return
	}
	a.modelTests++
	a.mu.Unlock()
	defer func() { a.mu.Lock(); a.modelTests--; a.mu.Unlock() }()

	body := object{"model": input.Model, "messages": []any{object{"role": "user", "content": input.Prompt}}, "stream": input.Stream, "max_tokens": input.MaxTokens}
	if input.Protocol == "openai" && input.LimitField == "max_completion_tokens" {
		delete(body, "max_tokens")
		body["max_completion_tokens"] = input.MaxTokens
	}
	if input.Protocol != "openai" {
		body, err = convertRequest(body, "openai", input.Protocol)
		if err != nil {
			fail(c, 400, err.Error())
			return
		}
	} else if input.Stream {
		body["stream_options"] = object{"include_usage": true}
	}
	ctx, cancel := context.WithTimeout(c.Request.Context(), time.Duration(min(max(channel.TimeoutSeconds, 5), 300))*time.Second)
	defer cancel()
	started := time.Now()
	timing := requestTimings{Started: started}
	result := modelTestResult{}
	usage := completion{}
	defer func() {
		result.DurationMS = time.Since(started).Milliseconds()
		if !timing.First.IsZero() {
			result.FirstTokenMS = intPointer(timing.First.Sub(started).Milliseconds())
		}
		if !timing.FirstContent.IsZero() {
			result.FirstContentMS = intPointer(timing.FirstContent.Sub(started).Milliseconds())
			result.GenerationMS = intPointer(max(result.DurationMS-*result.FirstContentMS, 0))
		}
		if usage.UsageSeen && usage.UsageWarning == "" {
			result.InputTokens, result.OutputTokens = intPointer(usage.Input), intPointer(usage.Output)
		}
		if usage.CacheReadSeen {
			result.CacheRead = intPointer(usage.CacheRead)
		}
		if usage.CacheWriteSeen {
			result.CacheWrite = intPointer(usage.CacheWrite)
		}
		result.UsageWarning = usage.UsageWarning
		result.Text = modelTestPreview(result.Text, key, 16384)
		result.Error = modelTestPreview(result.Error, key, 1024)
		c.JSON(200, result)
	}()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint(channel.BaseURL, requestPath(input.Protocol)), bytes.NewBufferString(jsonString(body)))
	if err != nil {
		result.Error = "上游请求地址无效"
		return
	}
	setUpstreamAuth(req, input.Protocol, key)
	req.Header.Set("Content-Type", "application/json")
	if input.Stream {
		req.Header.Set("Accept", "text/event-stream")
	}
	// 单次请求，不使用网关重试，也不改变渠道的启用或冷却状态。
	resp, err := a.doRequest(req, channel.UseProxy)
	if err != nil {
		result.Error = err.Error()
		return
	}
	defer resp.Body.Close()
	result.Status = resp.StatusCode
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		raw, _ := io.ReadAll(io.LimitReader(resp.Body, 16<<10))
		message := modelTestUpstreamError(raw)
		result.Error = fmt.Sprintf("HTTP %d", resp.StatusCode)
		if message != "" {
			result.Error += "：" + message
		}
		return
	}
	if input.Stream {
		scanner := bufio.NewScanner(io.LimitReader(resp.Body, (8<<20)+1))
		scanner.Buffer(make([]byte, 4096), 1<<20)
		for {
			event, readErr := readEvent(scanner)
			received := time.Now()
			if readErr != nil {
				result.Error = "上游流未正常结束、读取失败或响应过大"
				return
			}
			_, done, inspectErr := inspectEvent(event, input.Protocol, &usage, false)
			if inspectErr != nil {
				result.Error = inspectErr.Error()
				if detail := modelTestUpstreamError([]byte(event.Data)); detail != "" {
					result.Error += "：" + detail
				}
				return
			}
			timing.observe(event, input.Protocol, received)
			if value, decodeErr := decodeJSON([]byte(event.Data)); decodeErr == nil {
				text := modelTestText(obj(value), input.Protocol, true)
				if len(result.Text) < 16384 {
					result.Text += modelTestPreview(text, "", 16384-len(result.Text))
				}
			}
			if done {
				break
			}
		}
		if timing.FirstContent.IsZero() && result.Text == "" {
			result.Error = "上游未返回文本；请检查模型类型或提高输出上限后重试"
			return
		}
	} else {
		raw, readErr := io.ReadAll(io.LimitReader(resp.Body, (8<<20)+1))
		if readErr != nil || len(raw) > 8<<20 {
			result.Error = "上游响应读取失败或超过 8 MiB"
			return
		}
		value, decodeErr := decodeJSON(raw)
		response := obj(value)
		if decodeErr != nil {
			result.Error = "上游未返回合法 JSON"
			return
		}
		if response["error"] != nil {
			result.Error = "上游报告错误：" + modelTestUpstreamError(raw)
			return
		}
		if err := validateCompletion(response, input.Protocol); err != nil {
			result.Error = err.Error()
			return
		}
		usage.applyUsage(response, input.Protocol)
		result.Text = modelTestText(response, input.Protocol, false)
		if strings.TrimSpace(result.Text) == "" {
			result.Error = "上游未返回文本；请检查模型类型或提高输出上限后重试"
			return
		}
	}
	result.Success = true
}

func modelTestPreview(text, key string, limit int) string {
	if key != "" {
		text = strings.ReplaceAll(text, key, "[已隐藏 Key]")
	}
	if len(text) <= limit {
		return text
	}
	text = text[:limit]
	for !utf8.ValidString(text) {
		text = text[:len(text)-1]
	}
	return text
}

func modelTestUpstreamError(raw []byte) string {
	value, err := decodeJSON(raw)
	if err != nil {
		return ""
	}
	body := obj(value)
	if message := str(obj(body["error"])["message"]); message != "" {
		return message
	}
	if message := str(body["error"]); message != "" {
		return message
	}
	if response := obj(body["response"]); response["error"] != nil {
		return str(obj(response["error"])["message"])
	}
	return str(body["message"])
}

func modelTestText(body object, protocol string, stream bool) string {
	var text strings.Builder
	content := func(value any) {
		if value, ok := value.(string); ok {
			text.WriteString(value)
			return
		}
		for _, raw := range array(value) {
			block := obj(raw)
			for _, field := range []string{"text", "thinking", "refusal"} {
				text.WriteString(str(block[field]))
			}
		}
	}
	if protocol == "openai" {
		for _, raw := range array(body["choices"]) {
			field := "message"
			if stream {
				field = "delta"
			}
			message := obj(obj(raw)[field])
			content(message["content"])
			for _, field := range []string{"reasoning_content", "reasoning", "refusal"} {
				text.WriteString(str(message[field]))
			}
		}
	} else if protocol == "anthropic" {
		if !stream {
			content(body["content"])
		} else if str(body["type"]) == "content_block_start" {
			content([]any{body["content_block"]})
		} else if str(body["type"]) == "content_block_delta" {
			content([]any{body["delta"]})
		}
	} else if stream {
		switch str(body["type"]) {
		case "response.output_text.delta", "response.refusal.delta", "response.reasoning_text.delta", "response.reasoning_summary_text.delta":
			text.WriteString(str(body["delta"]))
		}
	} else {
		for _, raw := range array(body["output"]) {
			item := obj(raw)
			content(item["content"])
			content(item["summary"])
		}
	}
	return text.String()
}

func (a *App) removeChannelTestModels(c *gin.Context) {
	id, ok := pathID(c)
	if !ok {
		return
	}
	var input struct {
		Models   []string `json:"models"`
		Revision string   `json:"revision"`
	}
	if c.ShouldBindJSON(&input) != nil || len(input.Models) == 0 || len(input.Revision) != 64 {
		fail(c, 400, "请选择要移除的模型，并重新读取模型列表")
		return
	}
	selected, err := normalizeChannelModels(input.Models)
	if err != nil {
		fail(c, 400, err.Error())
		return
	}
	var result modelTestModels
	routeIDs := []uint{}
	err = a.DB.WithContext(c.Request.Context()).Transaction(func(tx *gorm.DB) error {
		var channel Channel
		if err := tx.First(&channel, id).Error; err != nil {
			return err
		}
		inventory, err := loadModelTestModels(tx, channel)
		if err != nil {
			return err
		}
		if inventory.Revision != input.Revision {
			return errChannelModelsChanged
		}
		remaining := []string{}
		for _, model := range inventory.saved {
			if !slices.Contains(selected, model) {
				remaining = append(remaining, model)
			}
		}
		for _, route := range inventory.routes {
			if slices.Contains(selected, route.Model) {
				routeIDs = append(routeIDs, route.ID)
			}
		}
		if len(routeIDs) > 0 {
			if err := tx.Model(&Group{}).Where("active_route_id IN ?", routeIDs).Update("active_route_id", 0).Error; err != nil {
				return err
			}
			if err := tx.Where("id IN ?", routeIDs).Delete(&Route{}).Error; err != nil {
				return err
			}
		}
		channel.Models = jsonString(remaining)
		if err := tx.Model(&channel).Update("models", channel.Models).Error; err != nil {
			return err
		}
		result, err = loadModelTestModels(tx, channel)
		return err
	})
	if errors.Is(err, gorm.ErrRecordNotFound) {
		fail(c, 404, "渠道已删除，请刷新后重试")
		return
	}
	if errors.Is(err, errChannelModelsChanged) {
		fail(c, 409, "渠道模型或路由已更新，请重新读取列表后再移除")
		return
	}
	if err != nil {
		fail(c, 500, "移除模型失败，原配置已保留")
		return
	}
	a.mu.Lock()
	for _, id := range routeIDs {
		delete(a.weights, id)
	}
	a.mu.Unlock()
	c.JSON(200, object{"models": result.Models, "revision": result.Revision, "removed_routes": len(routeIDs)})
}
