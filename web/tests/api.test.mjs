import assert from "node:assert/strict";
import test from "node:test";
import { api } from "../src/api.ts";

test("API errors keep HTTP status, server messages and session expiry behavior", async () => {
  const originalFetch = globalThis.fetch;
  const originalWindow = globalThis.window;
  const events = [];
  globalThis.window = { dispatchEvent: event => events.push(event.type) };
  try {
    for (const status of [404, 503, 401]) {
      globalThis.fetch = async () => new Response(JSON.stringify({ error: { message: "server message" } }), { status });
      await assert.rejects(api("/requests/1"), error => error instanceof Error && error.status === status && error.message === "server message");
    }
    assert.deepEqual(events, ["session-expired"]);
    await assert.rejects(api("/login"), error => error.status === 401);
    assert.deepEqual(events, ["session-expired"]);
    globalThis.fetch = async () => new Response("unavailable", { status: 502 });
    await assert.rejects(api("/requests/1"), error => error.status === 502 && error.message === "请求失败（502）");
    globalThis.fetch = async () => new Response('{"ok":true}', { status: 200 });
    assert.deepEqual(await api("/requests/1"), { ok: true });
  } finally {
    globalThis.fetch = originalFetch;
    if (originalWindow === undefined) delete globalThis.window;
    else globalThis.window = originalWindow;
  }
});
