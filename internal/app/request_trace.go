package app

import (
	"bufio"
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

const requestBodyLimit = 256 << 10

func boundedDiagnostic(value string) string {
	if len(value) > 512 {
		return strings.ToValidUTF8(value[:512], "") + "…"
	}
	return value
}

type requestAttempt struct {
	ChannelID         uint      `json:"channel_id"`
	ChannelName       string    `json:"channel_name"`
	Model             string    `json:"model"`
	Protocol          string    `json:"protocol"`
	StartedAt         time.Time `json:"started_at"`
	DurationMS        int64     `json:"duration_ms"`
	Status            int       `json:"status"`
	Error             string    `json:"error"`
	Reason            string    `json:"reason"`
	UpstreamStatus    int       `json:"upstream_status"`
	UpstreamRequestID string    `json:"upstream_request_id"`
	FinishReason      string    `json:"finish_reason"`
	IncompleteReason  string    `json:"incomplete_reason"`
}

type requestPayload struct {
	Available         bool             `json:"available"`
	RequestBody       string           `json:"request_body"`
	ResponseBody      string           `json:"response_body"`
	RequestTruncated  bool             `json:"request_truncated"`
	ResponseTruncated bool             `json:"response_truncated"`
	Attempts          []requestAttempt `json:"attempts"`
}

type requestTrace struct {
	mu                sync.Mutex
	log               RequestLog
	requestBody       string
	requestTruncated  bool
	response          []byte
	responseTruncated bool
	attempts          []requestAttempt
}

func activeRequest(state string) bool { return state == "running" || state == "streaming" }

func requestState(log RequestLog) string {
	if log.State != "" {
		return log.State
	}
	if log.Status == 499 {
		return "cancelled"
	}
	if log.Status >= 400 {
		return "failed"
	}
	return "completed"
}

func (a *App) beginRequestTrace(log *RequestLog) *requestTrace {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.nextRequestID++
	log.ID = a.nextRequestID
	if log.CreatedAt.IsZero() {
		log.CreatedAt = time.Now()
	}
	initial := *log
	initial.State, initial.Status = "running", 0
	t := &requestTrace{log: initial, attempts: []requestAttempt{}}
	if a.activeRequests == nil {
		a.activeRequests = map[uint]*requestTrace{}
	}
	a.activeRequests[initial.ID] = t
	return t
}

func (t *requestTrace) setRequest(log RequestLog, body object) {
	text, truncated := boundedLogJSON(body)
	t.mu.Lock()
	t.log.Model, t.log.Stream = log.Model, log.Stream
	t.requestBody, t.requestTruncated = text, truncated
	t.mu.Unlock()
}

func (a *App) traceAttempt(t *requestTrace, log RequestLog) {
	t.mu.Lock()
	firstByte := t.log.FirstByteMS
	t.log = log
	t.log.State, t.log.Status, t.log.Error, t.log.FirstByteMS = "running", 0, "", firstByte
	t.attempts = append(t.attempts, requestAttempt{ChannelID: log.ChannelID, ChannelName: log.ChannelName, Model: log.UpstreamModel, Protocol: log.UpstreamProtocol, StartedAt: time.Now(), Reason: log.RoutingReason})
	t.mu.Unlock()
}

func (t *requestTrace) waitForRoute(log *RequestLog, reason string, until time.Time) {
	log.RoutingReason, log.RetryAt = reason, &until
	t.mu.Lock()
	t.log.RoutingReason, t.log.RetryAt = reason, &until
	t.log.State, t.log.Status, t.log.Error = "running", 0, ""
	t.mu.Unlock()
}

func (t *requestTrace) endAttempt(status int, message string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if len(t.attempts) == 0 {
		return
	}
	last := &t.attempts[len(t.attempts)-1]
	if last.Status != 0 {
		return
	}
	last.Status, last.Error, last.DurationMS = status, message, time.Since(last.StartedAt).Milliseconds()
}

func (t *requestTrace) attemptResponse(response *http.Response) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if len(t.attempts) == 0 {
		return
	}
	last := &t.attempts[len(t.attempts)-1]
	last.UpstreamStatus, last.UpstreamRequestID = response.StatusCode, upstreamRequestID(response.Header)
}

func (t *requestTrace) attemptOutcome(result completion) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if len(t.attempts) == 0 {
		return
	}
	last := &t.attempts[len(t.attempts)-1]
	last.FinishReason, last.IncompleteReason = boundedDiagnostic(result.Finish), boundedDiagnostic(result.IncompleteReason)
	t.log.FinishReason, t.log.IncompleteReason = last.FinishReason, last.IncompleteReason
}

func (t *requestTrace) usage(result completion) {
	t.mu.Lock()
	result.recordLogUsage(&t.log)
	t.mu.Unlock()
}

func (t *requestTrace) appendResponse(data []byte) {
	if len(data) == 0 {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.log.FirstByteMS == nil {
		ms := time.Since(t.log.CreatedAt).Milliseconds()
		t.log.FirstByteMS = &ms
	}
	if t.log.Stream {
		t.log.State = "streaming"
	}
	remaining := requestBodyLimit - len(t.response)
	if len(data) > remaining {
		t.responseTruncated = true
		data = data[:remaining]
	}
	t.response = append(t.response, data...)
}

func (t *requestTrace) summary() RequestLog {
	t.mu.Lock()
	defer t.mu.Unlock()
	log := t.log
	if activeRequest(log.State) {
		log.DurationMS = time.Since(log.CreatedAt).Milliseconds()
		if log.FirstContentMS != nil {
			log.GenerationMS = intPointer(max(log.DurationMS-*log.FirstContentMS, 0))
		}
	}
	return log
}

func (t *requestTrace) payload() requestPayload {
	t.mu.Lock()
	payload := requestPayload{Available: true, RequestBody: t.requestBody, RequestTruncated: t.requestTruncated, ResponseTruncated: t.responseTruncated, Attempts: append([]requestAttempt{}, t.attempts...)}
	raw, stream := string(t.response), t.log.Stream
	t.mu.Unlock()
	payload.ResponseBody = safeResponseBody(raw, stream)
	if len(payload.ResponseBody) > requestBodyLimit {
		payload.ResponseBody = strings.ToValidUTF8(payload.ResponseBody[:requestBodyLimit], "")
		payload.ResponseTruncated = true
	}
	return payload
}

func (a *App) finishRequestTrace(t *requestTrace, log *RequestLog) error {
	now := time.Now()
	log.DurationMS, log.FinishedAt = now.Sub(log.CreatedAt).Milliseconds(), &now
	log.State = "completed"
	if log.Status >= 400 {
		log.State = "failed"
	}
	if log.Status == 499 {
		log.State = "cancelled"
	}
	t.endAttempt(log.Status, log.Error)
	t.mu.Lock()
	log.FirstByteMS = t.log.FirstByteMS
	t.log = *log
	t.mu.Unlock()
	payload := t.payload()
	options := a.runtimeOptions()
	err := a.DB.Transaction(func(tx *gorm.DB) error {
		if err := recordRequestUsage(tx, log); err != nil {
			return err
		}
		if err := debitKey(tx, log); err != nil {
			return err
		}
		if err := recordKeyUsage(tx, log); err != nil {
			return err
		}
		if err := recordRoutingQuality(tx, log, payload); err != nil {
			return err
		}
		return recordRequestInsights(tx, log, payload)
	})
	if err != nil {
		slog.Error("请求统计保存失败", "request_id", log.RequestID, "error", err)
	} else if summaryErr := a.DB.Transaction(func(tx *gorm.DB) error { return saveRequestSummary(tx, *log, payload, options) }); summaryErr != nil {
		slog.Error("请求摘要保存失败，用量与预算已保存", "request_id", log.RequestID, "error", summaryErr)
	}
	a.mu.Lock()
	delete(a.activeRequests, log.ID)
	a.cacheFinishedRequest(*log, payload)
	a.mu.Unlock()
	return err
}

type requestCaptureWriter struct {
	gin.ResponseWriter
	trace    *requestTrace
	ctx      context.Context
	writeErr error
}

func (w *requestCaptureWriter) Write(data []byte) (int, error) {
	if err := w.ctx.Err(); err != nil {
		return 0, err
	}
	n, err := w.ResponseWriter.Write(data)
	w.writeErr = err
	w.trace.appendResponse(data[:n])
	return n, err
}

func (w *requestCaptureWriter) WriteString(data string) (int, error) {
	if err := w.ctx.Err(); err != nil {
		return 0, err
	}
	n, err := w.ResponseWriter.WriteString(data)
	w.writeErr = err
	w.trace.appendResponse([]byte(data[:n]))
	return n, err
}

func (w *requestCaptureWriter) FlushError() error {
	w.ResponseWriter.WriteHeaderNow()
	// Gin 的 Flush 不返回错误，展开后检查底层连接的刷新结果。
	writer := http.ResponseWriter(w.ResponseWriter)
	if wrapped, ok := writer.(interface{ Unwrap() http.ResponseWriter }); ok {
		writer = wrapped.Unwrap()
	}
	w.writeErr = http.NewResponseController(writer).Flush()
	return w.writeErr
}

func redactLogValue(value any) any {
	switch typed := value.(type) {
	case map[string]any:
		result := object{}
		for key, value := range typed {
			switch strings.ToLower(strings.ReplaceAll(key, "-", "_")) {
			case "authorization", "x_api_key", "api_key", "access_token", "refresh_token", "password", "secret", "client_secret":
				result[key] = "[已隐藏]"
			default:
				result[key] = redactLogValue(value)
			}
		}
		return result
	case []any:
		result := make([]any, len(typed))
		for i, value := range typed {
			result[i] = redactLogValue(value)
		}
		return result
	case string:
		if strings.HasPrefix(typed, "data:") && strings.Contains(typed, ";base64,") {
			return "[已省略 base64 数据]"
		}
	}
	return value
}

func boundedLogJSON(value any) (string, bool) {
	raw, err := json.MarshalIndent(redactLogValue(value), "", "  ")
	if err != nil {
		return "[内容无法记录]", false
	}
	truncated := len(raw) > requestBodyLimit
	if truncated {
		raw = raw[:requestBodyLimit]
	}
	return strings.ToValidUTF8(string(raw), ""), truncated
}

func safeResponseBody(raw string, stream bool) string {
	if raw == "" {
		return ""
	}
	if !stream {
		value, err := decodeJSON([]byte(raw))
		if err != nil {
			return "[响应内容未完整记录，无法显示 JSON 原文]"
		}
		text, _ := boundedLogJSON(value)
		return text
	}
	var result strings.Builder
	scanner := bufio.NewScanner(strings.NewReader(raw))
	scanner.Buffer(make([]byte, 4096), requestBodyLimit+1)
	for scanner.Scan() {
		line := scanner.Text()
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		data := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if data == "[DONE]" {
			result.WriteString("data: [DONE]\n\n")
			continue
		}
		value, err := decodeJSON([]byte(data))
		if err != nil {
			continue
		}
		result.WriteString("data: " + jsonString(redactLogValue(value)) + "\n\n")
	}
	return result.String()
}
