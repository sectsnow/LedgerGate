package app

func anthropicFinish(reason string) string {
	switch reason {
	case "tool_use":
		return "tool_calls"
	case "max_tokens", "model_context_window_exceeded":
		return "length"
	case "refusal":
		return "content_filter"
	default:
		return "stop"
	}
}

func (result *completion) captureResponsesEnd(body object, incomplete bool) {
	if incomplete || str(body["status"]) == "incomplete" {
		result.IncompleteReason = str(obj(body["incomplete_details"])["reason"])
		switch result.IncompleteReason {
		case "max_output_tokens":
			result.Finish = "length"
		case "content_filter":
			result.Finish = "content_filter"
		default:
			result.Finish = "incomplete"
		}
		return
	}
	for _, raw := range array(body["output"]) {
		if str(obj(raw)["type"]) == "function_call" {
			result.Finish = "tool_calls"
			return
		}
	}
	if result.Finish == "" {
		result.Finish = "stop"
	}
}

func (result *completion) captureEnd(body object, protocol string) {
	switch protocol {
	case "openai":
		for _, raw := range array(body["choices"]) {
			reason := str(obj(raw)["finish_reason"])
			if reason == "" {
				continue
			}
			if result.Finish != "" && result.Finish != reason {
				result.Finish = "multiple"
				return
			}
			result.Finish = reason
		}
	case "anthropic":
		if reason := str(body["stop_reason"]); reason != "" {
			result.Finish = anthropicFinish(reason)
		}
	case "responses":
		result.captureResponsesEnd(body, false)
	}
}
