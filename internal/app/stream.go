package app

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"
	"time"
)

type sseEvent struct {
	Event string
	Data  string
}

func readEvent(scanner *bufio.Scanner) (sseEvent, error) {
	event := sseEvent{}
	data := []string{}
	for scanner.Scan() {
		line := scanner.Text()
		if line == "" {
			if len(data) > 0 {
				event.Data = strings.Join(data, "\n")
				return event, nil
			}
			event = sseEvent{}
			continue
		}
		if strings.HasPrefix(line, "data:") {
			data = append(data, strings.TrimPrefix(strings.TrimPrefix(line, "data:"), " "))
		}
		if strings.HasPrefix(line, "event:") {
			event.Event = strings.TrimSpace(strings.TrimPrefix(line, "event:"))
		}
	}
	if err := scanner.Err(); err != nil {
		return event, err
	}
	if len(data) > 0 {
		event.Data = strings.Join(data, "\n")
		return event, nil
	}
	return event, io.EOF
}

type streamDelta struct {
	Text  string
	Tool  *toolCall
	Index int
}

func inspectEvent(event sseEvent, protocol string, result *completion, convert bool) ([]streamDelta, bool, error) {
	if event.Data == "[DONE]" {
		if protocol != "openai" {
			return nil, false, errors.New("上游协议与 SSE 终止事件不符")
		}
		return nil, true, nil
	}
	value, err := decodeJSON([]byte(event.Data))
	if err != nil {
		return nil, false, errors.New("上游 SSE 数据不是合法 JSON")
	}
	body, ok := value.(map[string]any)
	if !ok {
		return nil, false, errors.New("上游 SSE 数据必须是对象")
	}
	kind := str(body["type"])
	if event.Event == "error" || body["error"] != nil || kind == "error" || kind == "response.failed" || kind == "response.cancelled" {
		return nil, false, errors.New("上游在流中报告错误")
	}
	if id := str(body["id"]); id != "" {
		result.ID = id
	}
	if body["usage"] != nil {
		result.applyUsage(body, protocol)
	}
	deltas := []streamDelta{}
	if protocol == "openai" {
		for _, raw := range array(body["choices"]) {
			choice := obj(raw)
			if convert && integer(choice["index"]) != 0 {
				return nil, false, errors.New("跨协议流仅支持一个 choice")
			}
			if finish := str(choice["finish_reason"]); finish != "" {
				result.Finish = finish
			}
			if !convert {
				continue
			}
			delta := obj(choice["delta"])
			if text := str(delta["content"]); text != "" {
				deltas = append(deltas, streamDelta{Text: text})
			}
			if text := str(delta["refusal"]); text != "" {
				deltas = append(deltas, streamDelta{Text: text})
			}
			if delta["reasoning_content"] != nil || delta["audio"] != nil {
				return nil, false, errors.New("流中内容类型暂不支持跨协议转换")
			}
			for _, rawCall := range array(delta["tool_calls"]) {
				call := obj(rawCall)
				function := obj(call["function"])
				deltas = append(deltas, streamDelta{Index: int(integer(call["index"])), Tool: &toolCall{str(call["id"]), str(function["name"]), str(function["arguments"])}})
			}
		}
		return deltas, false, nil
	}
	if protocol == "anthropic" {
		switch kind {
		case "message_start":
			message := obj(body["message"])
			result.ID = str(message["id"])
			result.applyUsage(message, protocol)
		case "message_delta":
			switch str(obj(body["delta"])["stop_reason"]) {
			case "tool_use":
				result.Finish = "tool_calls"
			case "max_tokens":
				result.Finish = "length"
			default:
				result.Finish = "stop"
			}
		case "message_stop":
			return nil, true, nil
		case "content_block_start":
			if !convert {
				break
			}
			block := obj(body["content_block"])
			switch str(block["type"]) {
			case "tool_use":
				arguments := ""
				if len(obj(block["input"])) > 0 {
					arguments = jsonString(block["input"])
				}
				deltas = append(deltas, streamDelta{Index: int(integer(body["index"])), Tool: &toolCall{ID: str(block["id"]), Name: str(block["name"]), Arguments: arguments}})
			case "text":
				if text := str(block["text"]); text != "" {
					deltas = append(deltas, streamDelta{Text: text})
				}
			default:
				return nil, false, errors.New("Anthropic 流内容类型暂不支持转换")
			}
		case "content_block_delta":
			if !convert {
				break
			}
			delta := obj(body["delta"])
			switch str(delta["type"]) {
			case "text_delta":
				deltas = append(deltas, streamDelta{Text: str(delta["text"])})
			case "input_json_delta":
				deltas = append(deltas, streamDelta{Index: int(integer(body["index"])), Tool: &toolCall{Arguments: str(delta["partial_json"])}})
			default:
				return nil, false, errors.New("Anthropic 增量类型暂不支持转换")
			}
		}
		return deltas, false, nil
	}
	switch kind {
	case "response.created":
		result.ID = str(obj(body["response"])["id"])
	case "response.output_text.delta":
		if convert {
			deltas = append(deltas, streamDelta{Text: str(body["delta"])})
		}
	case "response.output_item.added":
		if !convert {
			break
		}
		item := obj(body["item"])
		switch str(item["type"]) {
		case "function_call":
			deltas = append(deltas, streamDelta{Index: int(integer(body["output_index"])), Tool: &toolCall{ID: str(item["call_id"]), Name: str(item["name"]), Arguments: str(item["arguments"])}})
		case "message":
		default:
			return nil, false, errors.New("Responses 输出类型暂不支持跨协议转换")
		}
	case "response.function_call_arguments.delta":
		if convert {
			deltas = append(deltas, streamDelta{Index: int(integer(body["output_index"])), Tool: &toolCall{Arguments: str(body["delta"])}})
		}
	case "response.completed", "response.incomplete":
		response := obj(body["response"])
		result.applyUsage(response, protocol)
		if str(response["status"]) == "failed" || response["error"] != nil {
			return nil, false, errors.New("Responses 完成事件报告错误")
		}
		if kind == "response.incomplete" {
			result.Finish = "length"
		}
		return deltas, true, nil
	}
	return deltas, false, nil
}

func writeFrame(writer io.Writer, event, data string) error {
	if event != "" {
		if _, err := fmt.Fprintf(writer, "event: %s\n", event); err != nil {
			return err
		}
	}
	_, err := fmt.Fprintf(writer, "data: %s\n\n", data)
	return err
}

type outputTool struct {
	toolCall
	Index       int
	SourceIndex int
}

type streamWriter struct {
	writer       http.ResponseWriter
	protocol     string
	model        string
	id           string
	sequence     int
	text         strings.Builder
	textIndex    int
	tools        map[int]*outputTool
	nextIndex    int
	started      bool
	created      int64
	includeUsage bool
}

func (s *streamWriter) send(kind string, body object) error {
	if s.protocol == "responses" {
		body["sequence_number"] = s.sequence
		s.sequence++
	}
	if s.protocol != "openai" {
		body["type"] = kind
	} else {
		kind = ""
	}
	if err := writeFrame(s.writer, kind, jsonString(body)); err != nil {
		return err
	}
	if flusher, ok := s.writer.(http.Flusher); ok {
		flusher.Flush()
	}
	return nil
}

func (s *streamWriter) chunk(delta object, finish any) object {
	return object{"id": s.id, "object": "chat.completion.chunk", "created": s.created, "model": s.model, "choices": []any{object{"index": 0, "delta": delta, "finish_reason": finish}}}
}

func (s *streamWriter) start(result completion) error {
	if s.started {
		return nil
	}
	s.started, s.created = true, time.Now().Unix()
	s.id = result.ID
	if s.id == "" {
		s.id = "lg_" + randomToken()[:16]
	}
	s.tools, s.textIndex = map[int]*outputTool{}, -1
	switch s.protocol {
	case "openai":
		return s.send("", s.chunk(object{"role": "assistant", "content": ""}, nil))
	case "anthropic":
		usage := result.usageBody("anthropic")
		usage["output_tokens"] = 0
		return s.send("message_start", object{"message": object{"id": s.id, "type": "message", "role": "assistant", "model": s.model, "content": []any{}, "stop_reason": nil, "stop_sequence": nil, "usage": usage}})
	default:
		response, _ := completionBody(completion{ID: s.id}, "responses", s.model)
		response["status"] = "in_progress"
		response["created_at"] = s.created
		response["usage"] = nil
		return s.send("response.created", object{"response": response})
	}
}

func (s *streamWriter) delta(delta streamDelta) error {
	if s.text.Len() > 16<<20 {
		return errors.New("跨协议流累计内容超过 16 MiB")
	}
	if delta.Text != "" {
		if s.textIndex < 0 {
			s.textIndex = s.nextIndex
			s.nextIndex++
			if s.protocol == "anthropic" {
				if err := s.send("content_block_start", object{"index": s.textIndex, "content_block": object{"type": "text", "text": ""}}); err != nil {
					return err
				}
			}
			if s.protocol == "responses" {
				if err := s.send("response.output_item.added", object{"output_index": s.textIndex, "item": object{"id": "msg_" + s.id, "type": "message", "role": "assistant", "status": "in_progress", "content": []any{}}}); err != nil {
					return err
				}
				if err := s.send("response.content_part.added", object{"item_id": "msg_" + s.id, "output_index": s.textIndex, "content_index": 0, "part": object{"type": "output_text", "text": "", "annotations": []any{}}}); err != nil {
					return err
				}
			}
		}
		s.text.WriteString(delta.Text)
		switch s.protocol {
		case "openai":
			return s.send("", s.chunk(object{"content": delta.Text}, nil))
		case "anthropic":
			return s.send("content_block_delta", object{"index": s.textIndex, "delta": object{"type": "text_delta", "text": delta.Text}})
		default:
			return s.send("response.output_text.delta", object{"item_id": "msg_" + s.id, "output_index": s.textIndex, "content_index": 0, "delta": delta.Text})
		}
	}
	if delta.Tool == nil {
		return nil
	}
	tool, exists := s.tools[delta.Index]
	if !exists {
		if delta.Tool.ID == "" || delta.Tool.Name == "" {
			return errors.New("上游工具调用首个事件缺少 ID 或名称")
		}
		tool = &outputTool{toolCall: toolCall{ID: delta.Tool.ID, Name: delta.Tool.Name}, Index: s.nextIndex, SourceIndex: len(s.tools)}
		s.tools[delta.Index] = tool
		s.nextIndex++
		if s.protocol == "anthropic" {
			if err := s.send("content_block_start", object{"index": tool.Index, "content_block": object{"type": "tool_use", "id": tool.ID, "name": tool.Name, "input": object{}}}); err != nil {
				return err
			}
		}
		if s.protocol == "responses" {
			if err := s.send("response.output_item.added", object{"output_index": tool.Index, "item": object{"id": "fc_" + tool.ID, "type": "function_call", "status": "in_progress", "call_id": tool.ID, "name": tool.Name, "arguments": ""}}); err != nil {
				return err
			}
		}
	}
	if len(tool.Arguments)+len(delta.Tool.Arguments) > 4<<20 {
		return errors.New("工具参数超过 4 MiB")
	}
	tool.Arguments += delta.Tool.Arguments
	switch s.protocol {
	case "openai":
		call := object{"index": tool.SourceIndex, "function": object{"arguments": delta.Tool.Arguments}}
		if !exists {
			call["id"], call["type"] = tool.ID, "function"
			obj(call["function"])["name"] = tool.Name
		}
		return s.send("", s.chunk(object{"tool_calls": []any{call}}, nil))
	case "anthropic":
		if delta.Tool.Arguments != "" {
			return s.send("content_block_delta", object{"index": tool.Index, "delta": object{"type": "input_json_delta", "partial_json": delta.Tool.Arguments}})
		}
	case "responses":
		if delta.Tool.Arguments != "" {
			return s.send("response.function_call_arguments.delta", object{"item_id": "fc_" + tool.ID, "output_index": tool.Index, "delta": delta.Tool.Arguments})
		}
	}
	return nil
}

func (s *streamWriter) finish(result *completion) error {
	result.ID, result.Text = s.id, s.text.String()
	tools := []*outputTool{}
	for _, tool := range s.tools {
		tools = append(tools, tool)
	}
	sort.Slice(tools, func(i, j int) bool { return tools[i].Index < tools[j].Index })
	for _, tool := range tools {
		if tool.Arguments == "" {
			tool.Arguments = "{}"
		}
		if _, err := decodeJSON([]byte(tool.Arguments)); err != nil {
			return errors.New("上游流工具参数 JSON 不完整")
		}
		result.Tools = append(result.Tools, tool.toolCall)
	}
	if result.Finish == "" {
		if len(tools) > 0 {
			result.Finish = "tool_calls"
		} else {
			result.Finish = "stop"
		}
	}
	if s.protocol == "openai" {
		if err := s.send("", s.chunk(object{}, result.Finish)); err != nil {
			return err
		}
		if s.includeUsage {
			if err := s.send("", object{"id": s.id, "object": "chat.completion.chunk", "created": s.created, "model": s.model, "choices": []any{}, "usage": result.usageBody("openai")}); err != nil {
				return err
			}
		}
		return writeFrame(s.writer, "", "[DONE]")
	}
	if s.protocol == "anthropic" {
		for index := 0; index < s.nextIndex; index++ {
			if err := s.send("content_block_stop", object{"index": index}); err != nil {
				return err
			}
		}
		stop := "end_turn"
		if result.Finish == "tool_calls" {
			stop = "tool_use"
		}
		if result.Finish == "length" {
			stop = "max_tokens"
		}
		if err := s.send("message_delta", object{"delta": object{"stop_reason": stop, "stop_sequence": nil}, "usage": result.usageBody("anthropic")}); err != nil {
			return err
		}
		return s.send("message_stop", object{})
	}
	body, err := completionBody(*result, "responses", s.model)
	if err != nil {
		return err
	}
	// 输出项的完成顺序沿用流中分配的索引。
	output := make([]any, s.nextIndex)
	if s.textIndex >= 0 {
		message := object{"id": "msg_" + s.id, "type": "message", "role": "assistant", "status": "completed", "content": []any{object{"type": "output_text", "text": result.Text, "annotations": []any{}}}}
		output[s.textIndex] = message
		if err := s.send("response.output_text.done", object{"item_id": "msg_" + s.id, "output_index": s.textIndex, "content_index": 0, "text": result.Text}); err != nil {
			return err
		}
		if err := s.send("response.content_part.done", object{"item_id": "msg_" + s.id, "output_index": s.textIndex, "content_index": 0, "part": array(message["content"])[0]}); err != nil {
			return err
		}
		if err := s.send("response.output_item.done", object{"output_index": s.textIndex, "item": message}); err != nil {
			return err
		}
	}
	for _, tool := range tools {
		item := object{"id": "fc_" + tool.ID, "type": "function_call", "status": "completed", "call_id": tool.ID, "name": tool.Name, "arguments": tool.Arguments}
		output[tool.Index] = item
		if err := s.send("response.function_call_arguments.done", object{"item_id": "fc_" + tool.ID, "output_index": tool.Index, "name": tool.Name, "arguments": tool.Arguments}); err != nil {
			return err
		}
		if err := s.send("response.output_item.done", object{"output_index": tool.Index, "item": item}); err != nil {
			return err
		}
	}
	body["output"], body["created_at"] = output, s.created
	kind := "response.completed"
	if result.Finish == "length" {
		kind = "response.incomplete"
	}
	return s.send(kind, object{"response": body})
}
