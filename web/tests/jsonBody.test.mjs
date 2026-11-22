import test from "node:test";
import assert from "node:assert/strict";
import { parseJsonBody } from "../src/jsonBody.ts";

test("preserves JSON objects, arrays and primitive values", () => {
  for (const value of [{ messages: [{ role: "user", content: "你好\n第二行" }], empty: null }, [1, false], "text", 0, null]) {
    assert.deepEqual(parseJsonBody(JSON.stringify(value)), { value, format: "json", incomplete: false });
  }
});

test("parses JSON errors even when the request used streaming", () => {
  assert.equal(parseJsonBody('{"error":{"message":"failed"}}', true).format, "json");
});

test("parses SSE events and excludes the completion marker", () => {
  const raw = ': heartbeat\r\nevent: response.created\r\ndata: {"type":"response.created",\r\ndata: "response":{"id":"one"}}\r\n\r\ndata: {"type":"response.output_text.delta","delta":"你好"}\r\n\r\ndata: [DONE]\r\n\r\n';
  assert.deepEqual(parseJsonBody(raw, true), {
    value: [{ type: "response.created", response: { id: "one" } }, { type: "response.output_text.delta", delta: "你好" }],
    format: "sse", incomplete: false,
  });
});

test("reads the final event without a trailing blank line", () => {
  assert.deepEqual(parseJsonBody('data: {"ok":true}', true).value, [{ ok: true }]);
});

test("retains incomplete events and truncated JSON as text", () => {
  const raw = 'data: {"ok":true}\n\ndata: {"partial":';
  assert.deepEqual(parseJsonBody(raw, true), { value: [{ ok: true }, '{"partial":'], format: "sse", incomplete: true });
  assert.deepEqual(parseJsonBody('{"partial":'), { value: '{"partial":', format: "text", incomplete: true });
  assert.equal(parseJsonBody("unstructured error", true).format, "text");
});
