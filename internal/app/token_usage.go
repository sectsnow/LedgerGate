package app

func usageCount(value any) int64 {
	return min(max(integer(value), 0), 1_000_000_000_000_000)
}

func (result *completion) applyUsage(body object, protocol string) {
	usage, exists := body["usage"].(map[string]any)
	if !exists {
		return
	}
	result.recordUsageDetails(usage, protocol)
	inputField, outputField := "input_tokens", "output_tokens"
	if protocol == "openai" {
		inputField, outputField = "prompt_tokens", "completion_tokens"
	}
	regular := max(result.Input-result.CacheRead-result.CacheWrite, 0)
	if input, ok := usage[inputField]; ok {
		result.UsageSeen, result.InputSeen = true, true
		result.Input = usageCount(input)
		regular = result.Input
	}
	if output, ok := usage[outputField]; ok {
		result.UsageSeen, result.OutputSeen = true, true
		result.Output = usageCount(output)
	}
	if protocol == "anthropic" {
		if value, ok := usage["cache_read_input_tokens"]; ok {
			result.CacheRead = usageCount(value)
		}
		if value, ok := usage["cache_creation_input_tokens"]; ok {
			result.CacheWrite = usageCount(value)
		}
		if creation, ok := usage["cache_creation"].(map[string]any); ok {
			if value, ok := creation["ephemeral_1h_input_tokens"]; ok {
				result.CacheWrite1h = usageCount(value)
			}
			if usage["cache_creation_input_tokens"] == nil {
				result.CacheWrite = usageCount(creation["ephemeral_5m_input_tokens"]) + result.CacheWrite1h
			}
		}
		result.Input = regular + result.CacheRead + result.CacheWrite
		return
	}
	detailsField := "input_tokens_details"
	if protocol == "openai" {
		detailsField = "prompt_tokens_details"
	}
	details := obj(usage[detailsField])
	if value, ok := details["cached_tokens"]; ok {
		result.CacheRead = usageCount(value)
	}
	if value, ok := usage["prompt_cache_hit_tokens"]; ok && details["cached_tokens"] == nil {
		result.CacheRead = usageCount(value)
	}
	if value, ok := details["cache_creation_tokens"]; ok {
		result.CacheWrite = usageCount(value)
	}
	if value, ok := details["cache_creation_1h_tokens"]; ok {
		result.CacheWrite1h = usageCount(value)
	}
}

func (result completion) usageBody(protocol string) object {
	usage := object{"input_tokens": result.Input, "output_tokens": result.Output}
	if protocol == "anthropic" {
		usage["input_tokens"] = max(result.Input-result.CacheRead-result.CacheWrite, 0)
		if result.CacheRead > 0 {
			usage["cache_read_input_tokens"] = result.CacheRead
		}
		if result.CacheWrite > 0 {
			usage["cache_creation_input_tokens"] = result.CacheWrite
			usage["cache_creation"] = object{"ephemeral_5m_input_tokens": max(result.CacheWrite-result.CacheWrite1h, 0), "ephemeral_1h_input_tokens": result.CacheWrite1h}
		}
		return usage
	}
	details := object{}
	if result.CacheRead > 0 {
		details["cached_tokens"] = result.CacheRead
	}
	if result.CacheWrite > 0 {
		details["cache_creation_tokens"] = result.CacheWrite
	}
	if result.CacheWrite1h > 0 {
		details["cache_creation_1h_tokens"] = result.CacheWrite1h
	}
	if protocol == "openai" {
		usage = object{"prompt_tokens": result.Input, "completion_tokens": result.Output}
		if len(details) > 0 {
			usage["prompt_tokens_details"] = details
		}
	} else if len(details) > 0 {
		usage["input_tokens_details"] = details
	}
	usage["total_tokens"] = result.Input + result.Output
	return usage
}
