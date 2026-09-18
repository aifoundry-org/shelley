import assert from "node:assert/strict";
import { test } from "node:test";
import { api } from "./api";

test("subscription API routes Kimi login, polling, and logout", async (t) => {
  const requests: { input: string; init?: RequestInit }[] = [];
  t.mock.method(globalThis, "fetch", async (input: string, init?: RequestInit) => {
    requests.push({ input, init });
    return Response.json({ session_id: "kimi-session", done: true });
  });
  const session = await api.startSubscriptionLogin("kimi");
  const result = await api.pollDeviceSubscriptionLogin("kimi", session.session_id);
  await api.logoutSubscription("kimi");
  assert.equal(result.done, true);
  assert.deepEqual(
    requests.map(({ input }) => input),
    [
      "/api/subscriptions/kimi/login/start",
      "/api/subscriptions/kimi/login/poll",
      "/api/subscriptions/kimi/logout",
    ],
  );
  assert.ok(requests.every(({ init }) => init?.method === "POST"));
  assert.deepEqual(JSON.parse(requests[1].init?.body as string), {
    session_id: "kimi-session",
  });
});

test("device polling still routes OpenAI sessions", async (t) => {
  t.mock.method(globalThis, "fetch", async (input: string, init?: RequestInit) => {
    assert.equal(input, "/api/subscriptions/openai/login/poll");
    assert.deepEqual(JSON.parse(init?.body as string), { session_id: "openai-session" });
    return Response.json({ done: false });
  });
  assert.deepEqual(await api.pollDeviceSubscriptionLogin("openai", "openai-session"), {
    done: false,
  });
});

test("Kimi polling surfaces server errors", async (t) => {
  t.mock.method(
    globalThis,
    "fetch",
    async () => new Response("login session not found", { status: 404 }),
  );
  await assert.rejects(
    api.pollDeviceSubscriptionLogin("kimi", "missing"),
    /Failed to poll login: login session not found/,
  );
});
