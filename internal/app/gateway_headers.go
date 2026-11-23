package app

import (
	"net/http"
	"strings"
)

func copyNativeRequestHeaders(outbound, incoming *http.Request, from, to string) {
	if from != to {
		return
	}
	if agent := incoming.Header.Get("User-Agent"); agent != "" {
		outbound.Header.Set("User-Agent", agent)
	}
	names := []string{"OpenAI-Beta"}
	if to == "anthropic" {
		names = []string{"Anthropic-Version", "Anthropic-Beta"}
	}
	for _, name := range names {
		if values := incoming.Header.Values(name); len(values) > 0 {
			outbound.Header[http.CanonicalHeaderKey(name)] = append([]string{}, values...)
		}
	}
}

func copyNativeResponseHeaders(outbound, incoming http.Header) {
	for name, values := range incoming {
		lower := strings.ToLower(name)
		if strings.HasPrefix(lower, "x-ratelimit-") || strings.HasPrefix(lower, "anthropic-ratelimit-") || lower == "openai-processing-ms" || lower == "retry-after" {
			outbound[http.CanonicalHeaderKey(name)] = append([]string{}, values...)
		}
	}
	id := upstreamRequestID(incoming)
	if id != "" {
		outbound.Set("X-Upstream-Request-ID", id)
	}
}

func upstreamRequestID(header http.Header) string {
	id := header.Get("X-Request-ID")
	if id == "" {
		id = header.Get("Request-ID")
	}
	id = strings.TrimSpace(id)
	if len(id) > 512 {
		return ""
	}
	for _, char := range id {
		if char < 32 || char == 127 {
			return ""
		}
	}
	return id
}
