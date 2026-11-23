package app

import (
	"encoding/json"
	"log/slog"
	"sort"
)

const finishedRequestLimit = 50
const finishedRequestByteLimit = 32 << 20

type cachedRequest struct {
	log     RequestLog
	payload string
	bytes   int
}

// 调用方持有 mu；容量按序列化数据计算，Go 对象和临时副本另占内存。
func (a *App) cacheFinishedRequest(log RequestLog, payload requestPayload) {
	body, err := json.Marshal(payload)
	if err != nil {
		slog.Error("请求详情缓存失败", "request_id", log.RequestID, "error", err)
		return
	}
	summary, err := json.Marshal(log)
	if err != nil {
		slog.Error("请求记录缓存失败", "request_id", log.RequestID, "error", err)
		return
	}
	item := cachedRequest{log: log, payload: string(body), bytes: len(summary) + len(body)}
	if item.bytes > finishedRequestByteLimit {
		return
	}
	a.finishedRequests = append(a.finishedRequests, item)
	a.finishedRequestBytes += item.bytes
	// 并发请求的统计入库先后可能不同，仍按实际结束时间淘汰。
	sort.SliceStable(a.finishedRequests, func(i, j int) bool {
		left, right := a.finishedRequests[i].log, a.finishedRequests[j].log
		if left.FinishedAt != nil && right.FinishedAt != nil && !left.FinishedAt.Equal(*right.FinishedAt) {
			return left.FinishedAt.Before(*right.FinishedAt)
		}
		return left.ID < right.ID
	})
	for len(a.finishedRequests) > finishedRequestLimit || a.finishedRequestBytes > finishedRequestByteLimit {
		a.finishedRequestBytes -= a.finishedRequests[0].bytes
		a.finishedRequests[0] = cachedRequest{}
		a.finishedRequests = a.finishedRequests[1:]
	}
}

func (a *App) requestSummaries() ([]RequestLog, int) {
	a.mu.Lock()
	traces := make([]*requestTrace, 0, len(a.activeRequests))
	for _, trace := range a.activeRequests {
		traces = append(traces, trace)
	}
	items := make([]RequestLog, 0, len(traces)+len(a.finishedRequests))
	for _, item := range a.finishedRequests {
		items = append(items, item.log)
	}
	a.mu.Unlock()
	for _, trace := range traces {
		items = append(items, trace.summary())
	}
	// 活跃请求置顶，其余按结束时间排列。
	sort.Slice(items, func(i, j int) bool {
		left, right := items[i], items[j]
		if activeRequest(left.State) != activeRequest(right.State) {
			return activeRequest(left.State)
		}
		if left.FinishedAt != nil && right.FinishedAt != nil && !left.FinishedAt.Equal(*right.FinishedAt) {
			return left.FinishedAt.After(*right.FinishedAt)
		}
		return left.ID > right.ID
	})
	return items, len(traces)
}

func (a *App) cachedRequestDetail(id uint) (RequestLog, requestPayload, bool) {
	a.mu.Lock()
	trace := a.activeRequests[id]
	var item cachedRequest
	for _, cached := range a.finishedRequests {
		if cached.log.ID == id {
			item = cached
			break
		}
	}
	a.mu.Unlock()
	if trace != nil {
		return trace.summary(), trace.payload(), true
	}
	if item.log.ID == 0 {
		return RequestLog{}, requestPayload{}, false
	}
	var payload requestPayload
	if err := json.Unmarshal([]byte(item.payload), &payload); err != nil {
		slog.Error("请求详情缓存读取失败", "request_id", item.log.RequestID, "error", err)
		return RequestLog{}, requestPayload{}, false
	}
	return item.log, payload, true
}
