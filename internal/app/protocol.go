package app

import (
	"errors"
	"fmt"
	"strings"
	"time"
)

func (channel Channel) supportedProtocols() []string {
	if len(channel.Protocols) > 0 {
		return channel.Protocols
	}
	if validProtocol(channel.Protocol) {
		return []string{channel.Protocol}
	}
	return nil
}

func (channel Channel) prepareRequest(body object, incoming string) (object, string, error) {
	protocols := channel.supportedProtocols()
	for _, protocol := range protocols {
		if protocol == incoming {
			return body, protocol, nil
		}
	}
	err := errors.New("渠道未配置可用协议")
	for _, protocol := range protocols {
		converted, convertErr := convertRequest(body, incoming, protocol)
		if convertErr == nil {
			return converted, protocol, nil
		}
		err = convertErr
	}
	return nil, "", err
}

func compatibleCandidates(items []candidate, body object, protocol string) ([]candidate, error) {
	result := make([]candidate, 0, len(items))
	checked := map[uint]error{}
	var firstError error
	for _, item := range items {
		err, exists := checked[item.Channel.ID]
		if !exists {
			_, _, err = item.Channel.prepareRequest(body, protocol)
			checked[item.Channel.ID] = err
		}
		if err == nil {
			result = append(result, item)
		} else if firstError == nil {
			firstError = err
		}
	}
	return result, firstError
}

func validProtocol(protocol string) bool {
	return protocol == "openai" || protocol == "responses" || protocol == "anthropic"
}

func requestPath(protocol string) string {
	switch protocol {
	case "responses":
		return "/v1/responses"
	case "anthropic":
		return "/v1/messages"
	default:
		return "/v1/chat/completions"
	}
}

// 协议间无法表达的字段直接报错，避免请求成功但语义丢失。
func convertRequest(body object, from, to string) (object, error) {
	if from == to {
		return body, nil
	}
	allowed := map[string]bool{}
	for _, field := range []string{"model", "stream", "temperature", "top_p", "tools", "tool_choice", "messages", "max_tokens", "max_completion_tokens", "stop", "parallel_tool_calls", "input", "instructions", "max_output_tokens", "system", "stop_sequences", "stream_options", "metadata", "user", "store", "n"} {
		allowed[field] = true
	}
	formatPair := from != "anthropic" && to != "anthropic"
	if formatPair {
		if from == "openai" {
			allowed["response_format"] = true
		} else {
			allowed["text"] = true
		}
	}
	for field, value := range body {
		if !allowed[field] && value != nil {
			return nil, fmt.Errorf("跨协议转换暂不支持 %s，请使用同协议渠道", field)
		}
	}
	if body["store"] == true {
		return nil, errors.New("跨协议转换不支持保存上游会话，请设置 store=false")
	}
	for _, field := range []string{"previous_response_id", "conversation", "background", "audio", "prediction", "response_format", "reasoning", "reasoning_effort", "thinking", "logprobs", "top_logprobs", "service_tier", "seed", "logit_bias", "presence_penalty", "frequency_penalty", "text", "modalities", "function_call", "functions"} {
		if formatPair && (field == "response_format" || field == "text") {
			continue
		}
		if value, exists := body[field]; exists && value != nil && value != false {
			return nil, fmt.Errorf("跨协议转换暂不支持 %s，请使用同协议渠道", field)
		}
	}
	if value := body["n"]; value != nil {
		if n, ok := number(value); !ok || n != 1 {
			return nil, errors.New("跨协议转换仅支持 n=1")
		}
	}
	if value := body["stream_options"]; value != nil {
		options, ok := value.(map[string]any)
		if from != "openai" || !ok {
			return nil, errors.New("跨协议仅支持 Chat 的 stream_options.include_usage")
		}
		for name, value := range options {
			if value == nil {
				continue
			}
			if name != "include_usage" {
				return nil, fmt.Errorf("跨协议转换暂不支持 stream_options.%s", name)
			}
			if _, ok := value.(bool); !ok {
				return nil, errors.New("stream_options.include_usage 必须为布尔值")
			}
		}
	}
	if user := body["user"]; user != nil {
		if _, ok := user.(string); !ok || from == "anthropic" {
			return nil, errors.New("跨协议 user 须为 Chat 或 Responses 的字符串字段")
		}
	}
	if value := body["metadata"]; value != nil {
		metadata, ok := value.(map[string]any)
		if !ok {
			return nil, errors.New("metadata 必须是对象")
		}
		if from == "anthropic" {
			for name, value := range metadata {
				if name != "user_id" && value != nil {
					return nil, fmt.Errorf("跨协议无法表达 Anthropic metadata.%s", name)
				}
			}
			if value := metadata["user_id"]; value != nil {
				if _, ok := value.(string); !ok {
					return nil, errors.New("metadata.user_id 必须为字符串")
				}
			}
		} else if to == "anthropic" && len(metadata) > 0 {
			return nil, errors.New("Anthropic 无法表达通用 metadata，请使用同协议渠道")
		}
	}
	chat, err := toChat(body, from)
	if err != nil {
		return nil, err
	}
	if to == "openai" {
		return chat, nil
	}
	if to == "responses" {
		return chatToResponses(chat)
	}
	return chatToAnthropic(chat)
}

func toChat(body object, protocol string) (object, error) {
	if protocol == "openai" {
		return body, nil
	}
	stream, _ := body["stream"].(bool)
	chat := object{"model": body["model"], "stream": stream}
	for _, field := range []string{"temperature", "top_p", "stop", "parallel_tool_calls"} {
		if value, ok := body[field]; ok {
			chat[field] = value
		}
	}
	messages := []any{}
	if protocol == "responses" {
		for _, name := range []string{"metadata", "user"} {
			if value, exists := body[name]; exists {
				chat[name] = value
			}
		}
		if text, exists := body["text"]; exists && text != nil {
			options, ok := text.(map[string]any)
			if !ok {
				return nil, errors.New("text 必须是对象")
			}
			for field, value := range options {
				if field != "format" && value != nil {
					return nil, fmt.Errorf("跨协议转换暂不支持 text.%s", field)
				}
			}
			if options["format"] != nil {
				format, err := convertJSONFormat(options["format"], "openai")
				if err != nil {
					return nil, err
				}
				chat["response_format"] = format
			}
		}
		if system := str(body["instructions"]); system != "" {
			messages = append(messages, object{"role": "system", "content": system})
		}
		if input, ok := body["input"].(string); ok {
			messages = append(messages, object{"role": "user", "content": input})
		} else {
			for _, raw := range array(body["input"]) {
				item := obj(raw)
				switch str(item["type"]) {
				case "function_call":
					messages = append(messages, object{"role": "assistant", "content": nil, "tool_calls": []any{object{"id": item["call_id"], "type": "function", "function": object{"name": item["name"], "arguments": item["arguments"]}}}})
				case "function_call_output":
					messages = append(messages, object{"role": "tool", "tool_call_id": item["call_id"], "content": item["output"]})
				case "", "message":
					content, err := contentToChat(item["content"], "responses")
					if err != nil {
						return nil, err
					}
					messages = append(messages, object{"role": item["role"], "content": content})
				default:
					return nil, fmt.Errorf("不支持转换 Responses 输入类型 %s", str(item["type"]))
				}
			}
		}
		if value, ok := body["max_output_tokens"]; ok {
			chat["max_tokens"] = value
		}
	} else {
		if user := obj(body["metadata"])["user_id"]; user != nil {
			chat["user"] = user
		}
		if system := body["system"]; system != nil {
			content, err := contentToChat(system, "anthropic")
			if err != nil {
				return nil, err
			}
			messages = append(messages, object{"role": "system", "content": content})
		}
		for _, raw := range array(body["messages"]) {
			item := obj(raw)
			if content, ok := item["content"].(string); ok {
				messages = append(messages, object{"role": item["role"], "content": content})
				continue
			}
			blocks := []any{}
			calls := []any{}
			results := []any{}
			for _, rawBlock := range array(item["content"]) {
				block := obj(rawBlock)
				switch str(block["type"]) {
				case "tool_use":
					calls = append(calls, object{"id": block["id"], "type": "function", "function": object{"name": block["name"], "arguments": jsonString(block["input"])}})
				case "tool_result":
					content, err := contentToChat(block["content"], "anthropic")
					if err != nil {
						return nil, err
					}
					results = append(results, object{"role": "tool", "tool_call_id": block["tool_use_id"], "content": content})
				default:
					content, err := contentToChat([]any{block}, "anthropic")
					if err != nil {
						return nil, err
					}
					blocks = append(blocks, array(content)...)
				}
			}
			messages = append(messages, results...)
			if len(blocks) > 0 || len(calls) > 0 {
				message := object{"role": item["role"], "content": blocks}
				if len(calls) > 0 {
					message["tool_calls"] = calls
				}
				messages = append(messages, message)
			}
		}
		chat["max_tokens"] = body["max_tokens"]
		if value, ok := body["stop_sequences"]; ok {
			chat["stop"] = value
		}
	}
	if len(messages) == 0 {
		return nil, errors.New("请求中没有可转换的消息")
	}
	chat["messages"] = messages
	tools := []any{}
	for _, raw := range array(body["tools"]) {
		tool := obj(raw)
		if protocol == "responses" && str(tool["type"]) != "function" {
			return nil, errors.New("跨协议仅支持 function 工具")
		}
		parameters := tool["parameters"]
		if protocol == "anthropic" {
			parameters = tool["input_schema"]
		}
		function := object{"name": tool["name"], "description": tool["description"], "parameters": parameters}
		if strict, ok := tool["strict"]; ok {
			function["strict"] = strict
		}
		tools = append(tools, object{"type": "function", "function": function})
	}
	if len(tools) > 0 {
		chat["tools"] = tools
	}
	if choice, exists := body["tool_choice"]; exists {
		if mapped, ok := choice.(map[string]any); ok {
			if disabled, exists := mapped["disable_parallel_tool_use"]; exists && protocol == "anthropic" {
				value, ok := disabled.(bool)
				if !ok {
					return nil, errors.New("disable_parallel_tool_use 必须为布尔值")
				}
				chat["parallel_tool_calls"] = !value
			}
			switch str(mapped["type"]) {
			case "tool", "function":
				chat["tool_choice"] = object{"type": "function", "function": object{"name": mapped["name"]}}
			case "any":
				chat["tool_choice"] = "required"
			case "auto", "none":
				chat["tool_choice"] = mapped["type"]
			default:
				return nil, errors.New("不支持该 tool_choice 转换")
			}
		} else {
			chat["tool_choice"] = choice
		}
	}
	return chat, nil
}

func contentToChat(value any, from string) (any, error) {
	if value == nil {
		return nil, nil
	}
	if text, ok := value.(string); ok {
		return text, nil
	}
	blocks, ok := value.([]any)
	if !ok {
		return nil, errors.New("消息 content 必须是字符串或数组")
	}
	result := []any{}
	for _, raw := range blocks {
		block := obj(raw)
		if block["cache_control"] != nil {
			return nil, errors.New("跨协议转换暂不支持 cache_control")
		}
		switch str(block["type"]) {
		case "text", "input_text", "output_text":
			result = append(result, object{"type": "text", "text": block["text"]})
		case "image_url":
			result = append(result, block)
		case "input_image":
			result = append(result, object{"type": "image_url", "image_url": object{"url": block["image_url"]}})
		case "image":
			source := obj(block["source"])
			imageURL := str(source["url"])
			if str(source["type"]) == "base64" {
				imageURL = "data:" + str(source["media_type"]) + ";base64," + str(source["data"])
			}
			if imageURL == "" {
				return nil, errors.New("图片缺少 URL 或 base64 数据")
			}
			result = append(result, object{"type": "image_url", "image_url": object{"url": imageURL}})
		default:
			return nil, fmt.Errorf("不支持转换 %s 内容类型 %s", from, str(block["type"]))
		}
	}
	return result, nil
}

func chatContent(value any, to string, assistant bool) (any, error) {
	if value == nil {
		return []any{}, nil
	}
	if text, ok := value.(string); ok {
		if to == "anthropic" {
			return []any{object{"type": "text", "text": text}}, nil
		}
		kind := "input_text"
		if assistant {
			kind = "output_text"
		}
		return []any{object{"type": kind, "text": text}}, nil
	}
	result := []any{}
	blocks, ok := value.([]any)
	if !ok {
		return nil, errors.New("消息内容必须是字符串或数组")
	}
	for _, raw := range blocks {
		block := obj(raw)
		if block["cache_control"] != nil {
			return nil, errors.New("跨协议转换暂不支持 cache_control")
		}
		switch str(block["type"]) {
		case "text":
			kind := "input_text"
			if to == "anthropic" {
				kind = "text"
			} else if assistant {
				kind = "output_text"
			}
			result = append(result, object{"type": kind, "text": block["text"]})
		case "image_url":
			imageURL := str(obj(block["image_url"])["url"])
			if imageURL == "" {
				return nil, errors.New("图片缺少 image_url.url")
			}
			if to == "responses" {
				result = append(result, object{"type": "input_image", "image_url": imageURL})
			} else {
				source := object{"type": "url", "url": imageURL}
				if strings.HasPrefix(imageURL, "data:") {
					media, data, found := strings.Cut(strings.TrimPrefix(imageURL, "data:"), ";base64,")
					if !found {
						return nil, errors.New("图片 data URL 格式无效")
					}
					source = object{"type": "base64", "media_type": media, "data": data}
				}
				result = append(result, object{"type": "image", "source": source})
			}
		default:
			return nil, fmt.Errorf("不支持跨协议转换内容类型 %s", str(block["type"]))
		}
	}
	return result, nil
}

func chatToResponses(chat object) (object, error) {
	stream, _ := chat["stream"].(bool)
	result := object{"model": chat["model"], "stream": stream, "store": false}
	if chat["response_format"] != nil {
		format, err := convertJSONFormat(chat["response_format"], "responses")
		if err != nil {
			return nil, err
		}
		result["text"] = object{"format": format}
	}
	for _, field := range []string{"temperature", "top_p", "parallel_tool_calls", "metadata", "user"} {
		if value, ok := chat[field]; ok {
			result[field] = value
		}
	}
	if stop := chat["stop"]; stop != nil {
		return nil, errors.New("Responses 跨协议转换暂不支持 stop")
	}
	if value, ok := chat["max_completion_tokens"]; ok {
		result["max_output_tokens"] = value
	} else if value, ok := chat["max_tokens"]; ok {
		result["max_output_tokens"] = value
	}
	input := []any{}
	for _, raw := range array(chat["messages"]) {
		message := obj(raw)
		role := str(message["role"])
		if role == "tool" {
			content := message["content"]
			if _, ok := content.(string); !ok {
				var text strings.Builder
				blocks, ok := content.([]any)
				if !ok {
					return nil, errors.New("Responses 跨协议工具结果暂仅支持文本")
				}
				for _, raw := range blocks {
					block := obj(raw)
					if str(block["type"]) != "text" {
						return nil, errors.New("Responses 跨协议工具结果暂仅支持文本")
					}
					text.WriteString(str(block["text"]))
				}
				content = text.String()
			}
			input = append(input, object{"type": "function_call_output", "call_id": message["tool_call_id"], "output": content})
			continue
		}
		content, err := chatContent(message["content"], "responses", role == "assistant")
		if err != nil {
			return nil, err
		}
		if len(array(content)) > 0 {
			input = append(input, object{"type": "message", "role": role, "content": content})
		}
		for _, rawCall := range array(message["tool_calls"]) {
			call := obj(rawCall)
			function := obj(call["function"])
			input = append(input, object{"type": "function_call", "call_id": call["id"], "name": function["name"], "arguments": function["arguments"]})
		}
	}
	result["input"] = input
	tools := []any{}
	for _, raw := range array(chat["tools"]) {
		tool := obj(raw)
		if str(tool["type"]) != "function" {
			return nil, errors.New("跨协议仅支持 function 工具")
		}
		function := obj(tool["function"])
		converted := object{"type": "function", "name": function["name"], "description": function["description"], "parameters": function["parameters"]}
		if strict, ok := function["strict"]; ok {
			converted["strict"] = strict
		}
		tools = append(tools, converted)
	}
	if len(tools) > 0 {
		result["tools"] = tools
	}
	if choice, ok := chat["tool_choice"]; ok {
		if mapped, ok := choice.(map[string]any); ok {
			result["tool_choice"] = object{"type": "function", "name": obj(mapped["function"])["name"]}
		} else {
			result["tool_choice"] = choice
		}
	}
	return result, nil
}

func chatToAnthropic(chat object) (object, error) {
	maxTokens := chat["max_completion_tokens"]
	if maxTokens == nil {
		maxTokens = chat["max_tokens"]
	}
	if maxTokens == nil {
		maxTokens = 4096
	}
	stream, _ := chat["stream"].(bool)
	result := object{"model": chat["model"], "stream": stream, "max_tokens": maxTokens}
	if user := chat["user"]; user != nil {
		result["metadata"] = object{"user_id": user}
	}
	for _, field := range []string{"temperature", "top_p"} {
		if value, ok := chat[field]; ok {
			result[field] = value
		}
	}
	if stop := chat["stop"]; stop != nil {
		if text, ok := stop.(string); ok {
			result["stop_sequences"] = []string{text}
		} else {
			result["stop_sequences"] = stop
		}
	}
	messages, system := []any{}, []any{}
	for _, raw := range array(chat["messages"]) {
		message := obj(raw)
		role := str(message["role"])
		content, err := chatContent(message["content"], "anthropic", role == "assistant")
		if err != nil {
			return nil, err
		}
		if role == "system" || role == "developer" {
			system = append(system, array(content)...)
			continue
		}
		if role == "tool" {
			role = "user"
			content = []any{object{"type": "tool_result", "tool_use_id": message["tool_call_id"], "content": content}}
		}
		for _, rawCall := range array(message["tool_calls"]) {
			call := obj(rawCall)
			function := obj(call["function"])
			arguments, err := decodeJSON([]byte(str(function["arguments"])))
			if err != nil {
				return nil, errors.New("工具调用 arguments 必须是合法 JSON")
			}
			if _, ok := arguments.(map[string]any); !ok {
				return nil, errors.New("工具参数必须是 JSON 对象")
			}
			content = append(array(content), object{"type": "tool_use", "id": call["id"], "name": function["name"], "input": arguments})
		}
		if role != "user" && role != "assistant" {
			return nil, fmt.Errorf("不支持转换角色 %s", role)
		}
		if len(messages) > 0 && str(obj(messages[len(messages)-1])["role"]) == role {
			previous := obj(messages[len(messages)-1])
			previous["content"] = append(array(previous["content"]), array(content)...)
		} else {
			messages = append(messages, object{"role": role, "content": content})
		}
	}
	if len(system) > 0 {
		result["system"] = system
	}
	result["messages"] = messages
	tools := []any{}
	for _, raw := range array(chat["tools"]) {
		tool := obj(raw)
		if str(tool["type"]) != "function" {
			return nil, errors.New("跨协议仅支持 function 工具")
		}
		function := obj(tool["function"])
		tools = append(tools, object{"name": function["name"], "description": function["description"], "input_schema": function["parameters"]})
	}
	if len(tools) > 0 {
		result["tools"] = tools
	}
	if choice, exists := chat["tool_choice"]; exists {
		if mapped, ok := choice.(map[string]any); ok {
			result["tool_choice"] = object{"type": "tool", "name": obj(mapped["function"])["name"]}
		} else {
			switch str(choice) {
			case "auto", "none":
				result["tool_choice"] = object{"type": choice}
			case "required":
				result["tool_choice"] = object{"type": "any"}
			default:
				return nil, errors.New("tool_choice 无效")
			}
		}
	}
	if parallel, exists := chat["parallel_tool_calls"]; exists && parallel != nil {
		value, ok := parallel.(bool)
		if !ok {
			return nil, errors.New("parallel_tool_calls 必须为布尔值")
		}
		choice := obj(result["tool_choice"])
		if len(choice) == 0 {
			choice = object{"type": "auto"}
		}
		choice["disable_parallel_tool_use"] = !value
		result["tool_choice"] = choice
	}
	return result, nil
}

func convertJSONFormat(value any, to string) (object, error) {
	format, ok := value.(map[string]any)
	if !ok {
		return nil, errors.New("JSON 输出格式必须是对象")
	}
	switch str(format["type"]) {
	case "text", "json_object":
		return format, nil
	case "json_schema":
		if to == "openai" {
			schema := object{}
			for name, value := range format {
				if name != "type" {
					schema[name] = value
				}
			}
			return object{"type": "json_schema", "json_schema": schema}, nil
		}
		for field, value := range format {
			if field != "type" && field != "json_schema" && value != nil {
				return nil, fmt.Errorf("跨协议转换暂不支持 response_format.%s", field)
			}
		}
		schema, ok := format["json_schema"].(map[string]any)
		if !ok {
			return nil, errors.New("json_schema 必须是对象")
		}
		result := object{}
		for name, value := range schema {
			result[name] = value
		}
		result["type"] = "json_schema"
		return result, nil
	default:
		return nil, errors.New("跨协议暂仅支持 text、json_object 和 json_schema 输出格式")
	}
}

type responseProcessingError string

func (err responseProcessingError) Error() string { return "网关响应处理失败：" + string(err) }

type toolCall struct {
	ID        string
	Name      string
	Arguments string
}
type completion struct {
	usageDetails
	ID               string
	Text             string
	Tools            []toolCall
	Input            int64
	Output           int64
	CacheRead        int64
	CacheWrite       int64
	CacheWrite1h     int64
	UsageSeen        bool
	InputSeen        bool
	OutputSeen       bool
	Finish           string
	IncompleteReason string
}

func validateCompletion(body object, protocol string) error {
	switch protocol {
	case "openai":
		if len(array(body["choices"])) == 0 {
			return errors.New("上游响应缺少 choices")
		}
		for _, raw := range array(body["choices"]) {
			if len(obj(obj(raw)["message"])) == 0 {
				return errors.New("上游响应缺少 message")
			}
		}
	case "anthropic":
		if str(body["type"]) != "message" || array(body["content"]) == nil {
			return errors.New("上游未返回 Anthropic message")
		}
	case "responses":
		if str(body["object"]) != "response" || array(body["output"]) == nil || (str(body["status"]) != "completed" && str(body["status"]) != "incomplete") {
			return errors.New("上游未返回有效 Responses response")
		}
	}
	return nil
}

func usageOf(value object, protocol string) (int64, int64) {
	var result completion
	result.applyUsage(value, protocol)
	return result.Input, result.Output
}

func parseCompletion(body object, protocol string) (completion, error) {
	result := completion{ID: str(body["id"])}
	if err := validateCompletion(body, protocol); err != nil {
		return result, err
	}
	result.applyUsage(body, protocol)
	result.captureEnd(body, protocol)
	if body["error"] != nil {
		return result, errors.New("上游返回错误响应")
	}
	if protocol == "openai" {
		choices := array(body["choices"])
		if len(choices) != 1 {
			return result, responseProcessingError("跨协议响应必须包含一个 choice")
		}
		choice := obj(choices[0])
		message := obj(choice["message"])
		result.Text = str(message["content"])
		if blocks, ok := message["content"].([]any); ok {
			for _, raw := range blocks {
				block := obj(raw)
				if str(block["type"]) != "text" {
					return result, responseProcessingError("响应内容类型暂不支持跨协议转换")
				}
				result.Text += str(block["text"])
			}
		}
		if refusal := str(message["refusal"]); refusal != "" {
			result.Text += refusal
		}
		for _, raw := range array(message["tool_calls"]) {
			call := obj(raw)
			function := obj(call["function"])
			result.Tools = append(result.Tools, toolCall{str(call["id"]), str(function["name"]), str(function["arguments"])})
		}
	} else if protocol == "anthropic" {
		if str(body["type"]) != "message" {
			return result, errors.New("上游未返回 Anthropic message")
		}
		for _, raw := range array(body["content"]) {
			block := obj(raw)
			switch str(block["type"]) {
			case "text":
				result.Text += str(block["text"])
			case "tool_use":
				result.Tools = append(result.Tools, toolCall{str(block["id"]), str(block["name"]), jsonString(block["input"])})
			default:
				return result, responseProcessingError("上游返回的内容类型暂不支持跨协议转换")
			}
		}
	} else {
		if status := str(body["status"]); status == "failed" || status == "cancelled" {
			return result, errors.New("上游 Responses 请求失败或已取消")
		}
		if str(body["object"]) != "response" {
			return result, errors.New("上游未返回 Responses response")
		}
		for _, raw := range array(body["output"]) {
			item := obj(raw)
			switch str(item["type"]) {
			case "message":
				for _, rawBlock := range array(item["content"]) {
					block := obj(rawBlock)
					if str(block["type"]) == "output_text" {
						result.Text += str(block["text"])
					} else if str(block["type"]) == "refusal" {
						result.Text += str(block["refusal"])
					} else {
						return result, responseProcessingError("Responses 输出内容类型暂不支持转换")
					}
				}
			case "function_call":
				result.Tools = append(result.Tools, toolCall{str(item["call_id"]), str(item["name"]), str(item["arguments"])})
			default:
				return result, responseProcessingError("Responses 输出项暂不支持转换")
			}
		}
	}
	return result, nil
}

func completionBody(result completion, protocol, model string) (object, error) {
	if result.Finish == "incomplete" && protocol != "responses" {
		return nil, responseProcessingError("上游未完成原因无法转换到目标协议，请使用同协议渠道")
	}
	if result.ID == "" {
		result.ID = "lg_" + randomToken()[:16]
	}
	if protocol == "openai" {
		message := object{"role": "assistant", "content": result.Text}
		if len(result.Tools) > 0 {
			calls := []any{}
			for _, tool := range result.Tools {
				calls = append(calls, object{"id": tool.ID, "type": "function", "function": object{"name": tool.Name, "arguments": tool.Arguments}})
			}
			message["tool_calls"] = calls
			if result.Text == "" {
				message["content"] = nil
			}
		}
		return object{"id": result.ID, "object": "chat.completion", "created": time.Now().Unix(), "model": model, "choices": []any{object{"index": 0, "message": message, "finish_reason": result.Finish}}, "usage": result.usageBody(protocol)}, nil
	}
	if protocol == "anthropic" {
		content := []any{}
		if result.Text != "" {
			content = append(content, object{"type": "text", "text": result.Text})
		}
		for _, tool := range result.Tools {
			arguments, err := decodeJSON([]byte(tool.Arguments))
			if err != nil {
				return nil, errors.New("上游工具参数 JSON 无效")
			}
			if _, ok := arguments.(map[string]any); !ok {
				return nil, errors.New("上游工具参数必须是对象")
			}
			content = append(content, object{"type": "tool_use", "id": tool.ID, "name": tool.Name, "input": arguments})
		}
		stop := "end_turn"
		if result.Finish == "tool_calls" {
			stop = "tool_use"
		}
		if result.Finish == "length" {
			stop = "max_tokens"
		}
		if result.Finish == "content_filter" {
			stop = "refusal"
		}
		return object{"id": result.ID, "type": "message", "role": "assistant", "model": model, "content": content, "stop_reason": stop, "stop_sequence": nil, "usage": result.usageBody(protocol)}, nil
	}
	output := []any{}
	if result.Text != "" {
		output = append(output, object{"id": "msg_" + result.ID, "type": "message", "role": "assistant", "status": "completed", "content": []any{object{"type": "output_text", "text": result.Text, "annotations": []any{}}}})
	}
	for _, tool := range result.Tools {
		output = append(output, object{"id": "fc_" + tool.ID, "type": "function_call", "status": "completed", "call_id": tool.ID, "name": tool.Name, "arguments": tool.Arguments})
	}
	status := "completed"
	var incomplete any
	if result.Finish == "length" || result.Finish == "content_filter" || result.Finish == "incomplete" {
		status = "incomplete"
		reason := result.IncompleteReason
		if reason == "" && result.Finish == "length" {
			reason = "max_output_tokens"
		}
		if reason == "" && result.Finish == "content_filter" {
			reason = "content_filter"
		}
		if reason != "" {
			incomplete = object{"reason": reason}
		}
	}
	return object{"id": result.ID, "object": "response", "created_at": time.Now().Unix(), "model": model, "status": status, "error": nil, "incomplete_details": incomplete, "output": output, "usage": result.usageBody(protocol)}, nil
}
