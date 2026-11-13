import assert from "node:assert/strict";
import { api, type SubscriptionLoginStart, type SubscriptionsStatus } from "../../services/api";
import { en } from "../../i18n/en";
import { subscriptionForSource, useSubscriptionConnections } from "./subscriptionConnections";

function deferred<T>() {
  let resolve!: (value: T) => void;
  const promise = new Promise<T>((done) => {
    resolve = done;
  });
  return { promise, resolve };
}

const original = {
  getSubscriptions: api.getSubscriptions,
  startSubscriptionLogin: api.startSubscriptionLogin,
  completeAnthropicSubscriptionLogin: api.completeAnthropicSubscriptionLogin,
  pollDeviceSubscriptionLogin: api.pollDeviceSubscriptionLogin,
  logoutSubscription: api.logoutSubscription,
  importLegacySubscriptions: api.importLegacySubscriptions,
};
const fakeWindow = {
  open: () => null as { opener: unknown; location: { href: string }; close: () => void } | null,
};
Object.defineProperty(globalThis, "window", { value: fakeWindow, configurable: true });
let status!: SubscriptionsStatus;
let changed = 0;
let signedIn = 0;
let starts = 0;
let polls = 0;
let logouts = 0;
let imports = 0;
let claudeCode = "";
const controller = () =>
  useSubscriptionConnections(
    (key) => en[key],
    async () => {
      changed++;
    },
    () => {
      signedIn++;
    },
  );
function reset() {
  status = {
    legacy_import_available: false,
    providers: {
      anthropic: { logged_in: false, status: "Not logged in" },
      openai: { logged_in: false, status: "Not logged in" },
      kimi: { logged_in: false, status: "Not logged in" },
    },
  };
  changed = signedIn = starts = polls = logouts = imports = 0;
  api.getSubscriptions = async () => structuredClone(status);
  api.startSubscriptionLogin = async (provider) => {
    starts++;
    return provider === "anthropic"
      ? { session_id: "claude-session", authorize_url: "https://auth.example.test/claude" }
      : {
          session_id: `${provider}-session`,
          verification_url: "https://auth.example.test/device",
          user_code: "TEST-CODE",
        };
  };
  api.completeAnthropicSubscriptionLogin = async (session, code) => {
    assert.equal(session, "claude-session");
    claudeCode = code;
    status.providers.anthropic = { logged_in: true, status: "Logged in" };
  };
  api.pollDeviceSubscriptionLogin = async (provider, session) => {
    polls++;
    assert.equal(session, `${provider}-session`);
    status.providers[provider] = { logged_in: true, status: "Logged in" };
    return { done: true };
  };
  api.logoutSubscription = async (provider) => {
    logouts++;
    status.providers[provider] = { logged_in: false, status: "Not logged in" };
  };
  api.importLegacySubscriptions = async () => {
    imports++;
    status.legacy_import_available = false;
  };
  fakeWindow.open = () => null;
}

try {
  for (const provider of ["anthropic", "openai", "kimi"] as const) {
    reset();
    const c = controller();
    await c.load();
    await c.start(provider);
    assert.equal(c.pending?.provider, provider, "popup blockers preserve a visible login session");
    await c.start(provider);
    assert.equal(starts, 1, "a pending sign-in cannot be started twice");
    if (provider === "anthropic") {
      await c.complete();
      assert.equal(changed, 0, "Claude requires a pasted code");
      await c.complete(" test-code ");
      assert.equal(claudeCode, "test-code");
      assert.equal(polls, 0, "Claude never uses device polling");
    } else {
      api.pollDeviceSubscriptionLogin = async () => ({ done: false });
      await c.complete();
      assert.equal(c.pendingMessage, en.subscriptionNotApproved);
      assert.equal(changed, 0);
      api.pollDeviceSubscriptionLogin = async (p) => {
        status.providers[p] = { logged_in: true, status: "Logged in" };
        return { done: true };
      };
      await c.complete();
    }
    assert.equal(c.pending, null);
    assert.equal(c.connected(provider), true);
    assert.equal(changed, 1, "login refreshes models");
    assert.equal(signedIn, 1, "login closes only its active add dialog");
    await c.signOut(provider);
    assert.equal(logouts, 1);
    assert.equal(c.connected(provider), false);
    assert.equal(changed, 2, "sign out refreshes models and statuses");
  }

  reset();
  const race = controller();
  const start = deferred<SubscriptionLoginStart>();
  api.startSubscriptionLogin = () => {
    starts++;
    return start.promise;
  };
  const first = race.start("anthropic");
  await race.start("anthropic");
  assert.equal(starts, 1, "concurrent duplicate presses are ignored");
  race.cancel();
  start.resolve({ session_id: "cancelled", authorize_url: "https://auth.example.test/" });
  await first;
  assert.equal(race.pending, null, "cancel invalidates a late start response");
  assert.equal(race.busy, false);

  reset();
  let opened = false;
  let closed = false;
  const tab = {
    opener: {} as unknown,
    location: { href: "" },
    close: () => {
      closed = true;
    },
  };
  fakeWindow.open = () => {
    opened = true;
    return tab;
  };
  api.startSubscriptionLogin = async () => {
    assert.equal(opened, true, "tab opens before the asynchronous request");
    throw new Error("provider unavailable");
  };
  const failure = controller();
  await failure.start("kimi");
  assert.equal(failure.error, "provider unavailable");
  assert.equal(closed, true, "failed starts close their blank tab");
  assert.equal(tab.opener, null);
  assert.equal(failure.busy, false);

  reset();
  const c = controller();
  await c.start("anthropic");
  api.completeAnthropicSubscriptionLogin = async () => {
    throw new Error("invalid code");
  };
  await c.complete("bad");
  assert.equal(c.error, "invalid code");
  assert.notEqual(c.pending, null, "completion errors allow retry");
  c.cancel();
  assert.equal(c.error, null);
  assert.equal(c.pending, null);
  await c.start("kimi");
  assert.equal(
    (c.pending as { provider: string } | null)?.provider,
    "kimi",
    "cancel allows another provider",
  );

  reset();
  status.providers.openai = {
    logged_in: false,
    status: "Using fleet token",
    fleet_owner: "node-a",
    fleet: "following node-a",
  };
  const fleet = controller();
  await fleet.load();
  assert.equal(fleet.connected("openai"), true, "follower needs no local login");
  assert.equal(fleet.follower("openai"), true);
  await fleet.signOut("openai");
  assert.equal(logouts, 0, "followers cannot sign out the owner's account");
  await fleet.start("openai");
  await fleet.complete();
  assert.equal(fleet.follower("openai"), false, "takeover refreshes the fleet role");

  reset();
  const importing = controller();
  await importing.load();
  await importing.importLegacy();
  assert.equal(imports, 0, "unavailable imports are not requested");
  status.legacy_import_available = true;
  await importing.load();
  assert.equal(imports, 0, "loading never implicitly imports credentials");
  api.importLegacySubscriptions = async () => {
    imports++;
    throw new Error("import refused");
  };
  await importing.importLegacy();
  assert.equal(importing.error, "import refused");
  api.importLegacySubscriptions = async () => {
    imports++;
    status.legacy_import_available = false;
  };
  await importing.importLegacy();
  assert.equal(imports, 2);
  assert.equal(importing.status?.legacy_import_available, false);
  assert.equal(changed, 2, "import refreshes models on success and error");

  reset();
  const cancelledComplete = controller();
  await cancelledComplete.start("kimi");
  const completing = deferred<{ done: boolean }>();
  api.pollDeviceSubscriptionLogin = () => completing.promise;
  const completion = cancelledComplete.complete();
  cancelledComplete.cancel();
  completing.resolve({ done: true });
  await completion;
  assert.equal(changed, 1, "successful in-flight completion still refreshes after cancellation");
  assert.equal(signedIn, 0, "late completion cannot close a reopened dialog");

  for (const provider of ["anthropic", "openai", "kimi"] as const) {
    reset();
    const partial = controller();
    await partial.load();
    await partial.start(provider);
    const failAfterSaving = async () => {
      status.providers[provider] = {
        logged_in: true,
        status: "Logged in",
        fleet: "owner (epoch 2)",
      };
      throw new Error("model refresh failed after login");
    };
    api.completeAnthropicSubscriptionLogin = failAfterSaving;
    api.pollDeviceSubscriptionLogin = failAfterSaving;
    await partial.complete("test-code");
    assert.equal(
      partial.connected(provider),
      true,
      "completion errors detect persisted credentials",
    );
    assert.equal(partial.pending, null, "a consumed session is not offered for retry");
    assert.match(partial.error || "", /model refresh failed after login/);
    assert.match(partial.error || "", /session has finished/);
    assert.equal(signedIn, 0, "partial success keeps the original error visible");
    assert.equal(changed, 1);
  }

  reset();
  const lateError = controller();
  await lateError.start("kimi");
  const lateFailure = deferred<{ done: boolean }>();
  api.pollDeviceSubscriptionLogin = async () => {
    await lateFailure.promise;
    status.providers.kimi = { logged_in: true, status: "Logged in" };
    throw new Error("late refresh error");
  };
  const late = lateError.complete();
  lateError.cancel();
  lateFailure.resolve({ done: true });
  await late;
  assert.equal(lateError.connected("kimi"), true);
  assert.equal(lateError.error, null, "cancelled errors cannot reopen an error panel");
  assert.equal(changed, 1);
  assert.equal(signedIn, 0);

  assert.equal(
    subscriptionForSource("Claude subscription", "saved-provider"),
    undefined,
    "saved provider labels cannot impersonate subscription routes",
  );
  assert.equal(subscriptionForSource("ChatGPT"), undefined, "official ChatGPT is never Codex");
  assert.equal(subscriptionForSource("ChatGPT (Codex) subscription"), "openai");
  console.log("subscriptionConnections: all flow, race, fleet, import and error assertions passed");
} finally {
  Object.assign(api, original);
}
