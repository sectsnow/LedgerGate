package app

import "testing"

func TestConversionPreservesMetadataAndUser(t *testing.T) {
	chat := object{"model": "m", "messages": []any{object{"role": "user", "content": "hello"}}, "metadata": object{"task": "audit", "client": "test"}, "user": "end-user", "n": 1, "stream_options": object{"include_usage": true}}
	before := jsonString(chat)
	responses, err := convertRequest(chat, "openai", "responses")
	if err != nil || jsonString(responses["metadata"]) != jsonString(chat["metadata"]) || responses["user"] != "end-user" {
		t.Fatalf("Chat tags lost: %s %v", jsonString(responses), err)
	}
	restored, err := convertRequest(responses, "responses", "openai")
	if err != nil || jsonString(restored["metadata"]) != jsonString(chat["metadata"]) || restored["user"] != "end-user" || jsonString(chat) != before {
		t.Fatalf("Responses tags lost or input mutated: %s %v", jsonString(restored), err)
	}
	if _, err := convertRequest(chat, "openai", "anthropic"); err == nil {
		t.Fatal("generic metadata silently discarded for Anthropic")
	}
	delete(chat, "metadata")
	anthropic, err := convertRequest(chat, "openai", "anthropic")
	if err != nil || obj(anthropic["metadata"])["user_id"] != "end-user" {
		t.Fatalf("end-user identity not mapped: %s %v", jsonString(anthropic), err)
	}
	for _, target := range []string{"openai", "responses"} {
		converted, err := convertRequest(anthropic, "anthropic", target)
		if err != nil || converted["user"] != "end-user" {
			t.Fatalf("Anthropic identity lost for %s: %s %v", target, jsonString(converted), err)
		}
	}
}

func TestConversionRejectsUnrepresentableOptions(t *testing.T) {
	for _, field := range []object{
		{"metadata": "invalid"}, {"user": object{"id": "u"}}, {"n": 0}, {"n": 2}, {"n": 0.5}, {"n": "1"},
		{"stream_options": object{"vendor_flag": true}}, {"stream_options": object{"include_usage": "yes"}}, {"stream_options": true},
	} {
		body := object{"model": "m", "messages": []any{object{"role": "user", "content": "hello"}}}
		for name, value := range field {
			body[name] = value
		}
		if _, err := convertRequest(body, "openai", "responses"); err == nil {
			t.Fatalf("invalid option silently accepted: %s", jsonString(field))
		}
	}
	for _, metadata := range []any{object{"vendor_tag": "cannot-map"}, object{"user_id": 123}} {
		body := object{"model": "m", "max_tokens": 100, "messages": []any{object{"role": "user", "content": "hello"}}, "metadata": metadata}
		if _, err := convertRequest(body, "anthropic", "openai"); err == nil {
			t.Fatal("unrepresentable Anthropic metadata silently accepted")
		}
	}
	native := object{"model": "m", "thinking": object{"type": "adaptive"}, "metadata": object{"vendor_tag": "native"}}
	if converted, err := convertRequest(native, "anthropic", "anthropic"); err != nil || jsonString(converted) != jsonString(native) {
		t.Fatal("native extensions were restricted")
	}
}
