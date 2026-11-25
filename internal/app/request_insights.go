package app

import (
	"encoding/json"
	"fmt"
	"math"
	"math/bits"
	"sort"
	"strconv"
	"time"

	"gorm.io/gorm"
)

type latencyHistogram map[int64]int64

func (h latencyHistogram) add(value int64) {
	if value < 0 {
		return
	}
	step := int64(1) << max(0, bits.Len64(uint64(value))-5)
	upper := value
	if value > 0 {
		if value > math.MaxInt64-step {
			upper = math.MaxInt64
		} else {
			upper = ((value + step - 1) / step) * step
		}
	}
	h[upper]++
}

type latencyPercentiles struct {
	Samples int64  `json:"samples"`
	P50MS   *int64 `json:"p50_ms"`
	P95MS   *int64 `json:"p95_ms"`
}

func (h latencyHistogram) percentiles() latencyPercentiles {
	result := latencyPercentiles{}
	keys := make([]int64, 0, len(h))
	for key, count := range h {
		result.Samples += count
		keys = append(keys, key)
	}
	if result.Samples == 0 {
		return result
	}
	sort.Slice(keys, func(i, j int) bool { return keys[i] < keys[j] })
	var seen int64
	for _, key := range keys {
		seen += h[key]
		if result.P50MS == nil && seen >= (result.Samples+1)/2 {
			result.P50MS = intPointer(key)
		}
		if result.P95MS == nil && seen >= result.Samples-(result.Samples/20) {
			result.P95MS = intPointer(key)
		}
	}
	return result
}

type cacheLatency struct {
	Samples      int64 `json:"samples"`
	DurationMS   int64 `json:"duration_ms"`
	FirstSamples int64 `json:"first_samples"`
	FirstMS      int64 `json:"first_ms"`
}

// 每个小时、渠道和模型只保存合并后的分布、固定分类和估算合计。
type requestInsights struct {
	Samples        int64            `json:"samples"`
	Duration       latencyHistogram `json:"duration"`
	First          latencyHistogram `json:"first"`
	Errors         map[string]int64 `json:"errors"`
	RetryEligible  int64            `json:"retry_eligible"`
	RetryRescued   int64            `json:"retry_rescued"`
	WaitSamples    int64            `json:"wait_samples"`
	WaitMS         int64            `json:"wait_ms"`
	SavingsSamples int64            `json:"savings_samples"`
	BaselineMicros int64            `json:"baseline_micros"`
	ActualMicros   int64            `json:"actual_micros"`
	Cohorts        [4]cacheLatency  `json:"cohorts"`
}

func cacheSavings(log *RequestLog) (int64, int64, bool) {
	detail := log.CostDetail
	if detail == nil || detail.Mode != "token" || log.CostStatus != "estimated" || log.CostWarning != "" || !log.UsageRecorded || log.UsageWarning != "" || !log.CacheReadRecorded || !log.CacheWriteRecorded || log.InputTokens < 0 || log.CacheReadTokens < 0 || log.CacheWriteTokens < 0 || log.CacheReadTokens > log.InputTokens || log.CacheWriteTokens > log.InputTokens-log.CacheReadTokens || log.CacheWrite1hTokens < 0 || log.CacheWrite1hTokens > log.CacheWriteTokens || !detail.Rates.valid() || detail.MultiplierStale {
		return 0, 0, false
	}
	if detail.Rates.Input == nil || log.CacheReadTokens > 0 && detail.Rates.CacheRead == nil || log.CacheWriteTokens > log.CacheWrite1hTokens && detail.Rates.CacheWrite == nil || log.CacheWrite1hTokens > 0 && detail.Rates.CacheWrite1h == nil {
		return 0, 0, false
	}
	var multiplier *float64
	if detail.MultiplierApplied && !detail.RatesIncludeMultiplier {
		multiplier = detail.Multiplier
		if multiplier == nil || math.IsNaN(*multiplier) || math.IsInf(*multiplier, 0) || *multiplier < 0 {
			return 0, 0, false
		}
	}
	price := &modelPricing{Mode: "token", priceRates: detail.Rates}
	price.Output = intPointer(0)
	usage := completion{Input: log.InputTokens, CacheRead: log.CacheReadTokens, CacheWrite: log.CacheWriteTokens, CacheWrite1h: log.CacheWrite1hTokens, UsageSeen: true}
	actual, _, warning := pricedCost(usage, price, multiplier)
	if warning != "" {
		return 0, 0, false
	}
	usage.CacheRead, usage.CacheWrite, usage.CacheWrite1h = 0, 0, 0
	baseline, _, warning := pricedCost(usage, price, multiplier)
	return baseline, actual, warning == ""
}

func recordRequestInsights(tx *gorm.DB, log *RequestLog, payload requestPayload) error {
	query := tx.Model(&RequestHour{}).Where("hour = ? AND channel_id = ? AND model = ?", log.CreatedAt.Unix()/3600*3600, log.ChannelID, log.Model)
	var row RequestHour
	if err := query.Select("insights").Take(&row).Error; err != nil {
		return err
	}
	var value requestInsights
	if err := json.Unmarshal([]byte(row.Insights), &value); err != nil {
		return fmt.Errorf("读取请求分布失败: %w", err)
	}
	if value.Duration == nil {
		value.Duration = latencyHistogram{}
	}
	if value.First == nil {
		value.First = latencyHistogram{}
	}
	if value.Errors == nil {
		value.Errors = map[string]int64{}
	}
	value.Samples++
	value.Duration.add(log.DurationMS)
	if log.MetricsVersion >= 2 && log.FirstTokenMS != nil {
		value.First.add(*log.FirstTokenMS)
	}
	if label := diagnosticClass(log.Status); label != "" {
		value.Errors[label]++
	}
	if len(payload.Attempts) > 0 {
		first := payload.Attempts[0]
		if log.Status != 499 && first.Status != 499 && (first.Status >= 400 || first.Status == 0 && first.Error != "") {
			value.RetryEligible++
			if log.Status >= 200 && log.Status < 400 && len(payload.Attempts) > 1 {
				value.RetryRescued++
			}
		}
		value.WaitSamples++
		if len(payload.Attempts) > 1 {
			value.WaitMS += max(0, payload.Attempts[len(payload.Attempts)-1].StartedAt.Sub(log.CreatedAt).Milliseconds())
		}
	}
	if baseline, actual, ok := cacheSavings(log); ok {
		value.SavingsSamples++
		value.BaselineMicros += baseline
		value.ActualMicros += actual
	}
	if log.Status >= 200 && log.Status < 400 && log.MetricsVersion > 0 && log.UsageRecorded && log.UsageWarning == "" && log.CacheReadRecorded && log.CacheReadTokens >= 0 && log.CacheReadTokens <= log.InputTokens && log.DurationMS >= 0 {
		index := 0
		if log.Stream {
			index += 2
		}
		if log.CacheReadTokens > 0 {
			index++
		}
		cohort := &value.Cohorts[index]
		cohort.Samples++
		cohort.DurationMS += log.DurationMS
		if log.MetricsVersion >= 2 && log.FirstTokenMS != nil && *log.FirstTokenMS >= 0 {
			cohort.FirstSamples++
			cohort.FirstMS += *log.FirstTokenMS
		}
	}
	body, err := json.Marshal(value)
	if err != nil {
		return err
	}
	return tx.Model(&RequestHour{}).Where("hour = ? AND channel_id = ? AND model = ?", log.CreatedAt.Unix()/3600*3600, log.ChannelID, log.Model).Update("insights", string(body)).Error
}

type cacheComparison struct {
	ChannelID uint         `json:"channel_id"`
	Channel   string       `json:"channel"`
	Model     string       `json:"model"`
	Stream    bool         `json:"stream"`
	Hit       cacheLatency `json:"hit"`
	Miss      cacheLatency `json:"miss"`
}

type insightTrend struct {
	Date        string `json:"date"`
	Samples     int64  `json:"samples"`
	InputTokens int64  `json:"input_tokens"`
	ReadTokens  int64  `json:"read_tokens"`
}

type errorCount struct {
	Name  string `json:"name"`
	Count int64  `json:"count"`
}

type advancedReport struct {
	Requests        int64              `json:"requests"`
	Samples         int64              `json:"samples"`
	Duration        latencyPercentiles `json:"duration"`
	First           latencyPercentiles `json:"first"`
	RetryEligible   int64              `json:"retry_eligible"`
	RetryRescued    int64              `json:"retry_rescued"`
	WaitSamples     int64              `json:"wait_samples"`
	WaitMS          int64              `json:"wait_ms"`
	SavingsSamples  int64              `json:"savings_samples"`
	BaselineMicros  int64              `json:"baseline_micros"`
	ActualMicros    int64              `json:"actual_micros"`
	Errors          []errorCount       `json:"errors"`
	Comparisons     []cacheComparison  `json:"comparisons"`
	ComparisonTotal int                `json:"comparison_total"`
	Trend           []insightTrend     `json:"trend"`
}

func loadRequestInsights(tx *gorm.DB, start, end time.Time, channel *uint, model *string) (*advancedReport, error) {
	query := tx.Model(&RequestHour{}).Where("hour >= ? AND hour < ?", start.Unix()/3600*3600, end.Unix())
	if channel != nil {
		query = query.Where("channel_id = ?", *channel)
	}
	if model != nil {
		query = query.Where("model = ?", *model)
	}
	rows, err := query.Select("hour, channel_id, channel_name, model, requests, insights, cache_sample_requests, cache_sample_input_tokens, cache_sample_read_tokens").Rows()
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	report := &advancedReport{Errors: []errorCount{}, Comparisons: []cacheComparison{}, Trend: []insightTrend{}}
	durations, firsts := latencyHistogram{}, latencyHistogram{}
	errors := map[string]int64{}
	comparisons := map[string]*cacheComparison{}
	trend := map[string]*insightTrend{}
	for rows.Next() {
		var row RequestHour
		if err := tx.ScanRows(rows, &row); err != nil {
			return nil, err
		}
		var value requestInsights
		if err := json.Unmarshal([]byte(row.Insights), &value); err != nil {
			return nil, fmt.Errorf("读取请求分布失败: %w", err)
		}
		report.Requests += row.Requests
		report.Samples += value.Samples
		report.RetryEligible += value.RetryEligible
		report.RetryRescued += value.RetryRescued
		report.WaitSamples += value.WaitSamples
		report.WaitMS += value.WaitMS
		report.SavingsSamples += value.SavingsSamples
		report.BaselineMicros += value.BaselineMicros
		report.ActualMicros += value.ActualMicros
		for bucket, count := range value.Duration {
			durations[bucket] += count
		}
		for bucket, count := range value.First {
			firsts[bucket] += count
		}
		for name, count := range value.Errors {
			errors[name] += count
		}
		for i, cohort := range value.Cohorts {
			if cohort.Samples == 0 {
				continue
			}
			key := strconv.FormatUint(uint64(row.ChannelID), 10) + "\x00" + row.Model + "\x00" + strconv.FormatBool(i >= 2)
			pair := comparisons[key]
			if pair == nil {
				pair = &cacheComparison{ChannelID: row.ChannelID, Channel: row.ChannelName, Model: row.Model, Stream: i >= 2}
				comparisons[key] = pair
			}
			dest := &pair.Miss
			if i%2 == 1 {
				dest = &pair.Hit
			}
			dest.Samples += cohort.Samples
			dest.DurationMS += cohort.DurationMS
			dest.FirstSamples += cohort.FirstSamples
			dest.FirstMS += cohort.FirstMS
		}
		date := time.Unix(row.Hour, 0).In(usageDay(start).Location()).Format("2006-01-02")
		if end.Sub(start) <= 24*time.Hour {
			date = time.Unix(row.Hour, 0).In(usageDay(start).Location()).Format("2006-01-02 15:00")
		}
		point := trend[date]
		if point == nil {
			point = &insightTrend{Date: date}
			trend[date] = point
		}
		point.Samples += row.CacheSampleRequests
		point.InputTokens += row.CacheSampleInputTokens
		point.ReadTokens += row.CacheSampleReadTokens
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	report.Duration, report.First = durations.percentiles(), firsts.percentiles()
	for name, count := range errors {
		report.Errors = append(report.Errors, errorCount{Name: name, Count: count})
	}
	sort.Slice(report.Errors, func(i, j int) bool {
		if report.Errors[i].Count != report.Errors[j].Count {
			return report.Errors[i].Count > report.Errors[j].Count
		}
		return report.Errors[i].Name < report.Errors[j].Name
	})
	for _, pair := range comparisons {
		report.Comparisons = append(report.Comparisons, *pair)
	}
	sort.Slice(report.Comparisons, func(i, j int) bool {
		a, b := report.Comparisons[i], report.Comparisons[j]
		if a.Hit.Samples+a.Miss.Samples != b.Hit.Samples+b.Miss.Samples {
			return a.Hit.Samples+a.Miss.Samples > b.Hit.Samples+b.Miss.Samples
		}
		return a.Channel+"\x00"+a.Model+strconv.FormatBool(a.Stream) < b.Channel+"\x00"+b.Model+strconv.FormatBool(b.Stream)
	})
	report.ComparisonTotal = len(report.Comparisons)
	if len(report.Comparisons) > 20 {
		report.Comparisons = report.Comparisons[:20]
	}
	for tick := usageDay(start); tick.Before(end); tick = tick.AddDate(0, 0, 1) {
		if end.Sub(start) <= 24*time.Hour {
			break
		}
		date := tick.Format("2006-01-02")
		if trend[date] == nil {
			trend[date] = &insightTrend{Date: date}
		}
	}
	if end.Sub(start) <= 24*time.Hour {
		for tick := start.Truncate(time.Hour); tick.Before(end); tick = tick.Add(time.Hour) {
			date := tick.In(usageDay(start).Location()).Format("2006-01-02 15:00")
			if trend[date] == nil {
				trend[date] = &insightTrend{Date: date}
			}
		}
	}
	for _, point := range trend {
		report.Trend = append(report.Trend, *point)
	}
	sort.Slice(report.Trend, func(i, j int) bool { return report.Trend[i].Date < report.Trend[j].Date })
	return report, nil
}
