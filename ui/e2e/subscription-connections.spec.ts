import { test, expect, type Page } from "@playwright/test";
import { openModelPicker } from "./helpers";
import type {
  AvailableModel,
  ChatGPTAccount,
  SubscriptionProvider,
  SubscriptionsStatus,
} from "../src/services/api";

const labels: Record<SubscriptionProvider, string> = {
  anthropic: "Claude subscription",
  openai: "ChatGPT (Codex) subscription",
  kimi: "Kimi Code subscription",
};

async function fixture(page: Page) {
  const state = {
    status: {
      legacy_import_available: false,
      providers: {
        anthropic: { logged_in: false, status: "Not logged in" },
        openai: { logged_in: false, status: "Not logged in" },
        kimi: { logged_in: false, status: "Not logged in" },
      },
    } as SubscriptionsStatus,
    models: [
      { id: "fixture-model", display_name: "Gateway model", source: "Gateway", ready: true },
    ] as AvailableModel[],
    providers: [] as Array<{
      provider_id: string;
      provider_type: string;
      display_name: string;
      endpoint: string;
      has_api_key: boolean;
      model_count: number;
    }>,
    starts: [] as string[],
    approvals: 0,
    imports: 0,
    devicePending: true,
    startError: false,
    completeError: false,
    logoutError: false,
    importError: false,
    statusError: false,
    officialStarts: 0,
    officialDone: false,
    officialStartGate: null as Promise<void> | null,
    officialAccounts: [] as ChatGPTAccount[],
  };
  await page.addInitScript((models) => {
    // Every flow exercises the blocked-popup link without opening any provider.
    window.open = () => null;
    let init: Record<string, unknown> | undefined;
    Object.defineProperty(window, "__SHELLEY_INIT__", {
      configurable: true,
      get: () => init,
      set: (value) => {
        init = { ...value, models, default_model: "fixture-model" };
      },
    });
  }, state.models);
  await page.route("**/api/models", (route) => route.fulfill({ json: state.models }));
  await page.route("**/api/model-providers", (route) => route.fulfill({ json: state.providers }));
  await page.route("**/api/custom-models", (route) => route.fulfill({ json: [] }));
  await page.route("**/api/subscriptions", (route) =>
    state.statusError
      ? route.fulfill({ status: 503, body: "subscription status unavailable" })
      : route.fulfill({ json: state.status }),
  );
  const connect = (provider: SubscriptionProvider) => {
    state.status.providers[provider] = {
      logged_in: true,
      status: "Logged in",
      fleet: "owner (epoch 2)",
    };
    state.models.push({
      id: `${provider}-model`,
      display_name: `${provider} model`,
      ready: true,
      source: labels[provider],
      base_url: "https://misleading-api.example.test/v1",
    });
  };
  await page.route("**/api/subscriptions/**", async (route) => {
    const path = new URL(route.request().url()).pathname;
    if (path.endsWith("/import-legacy")) {
      state.imports++;
      state.status.legacy_import_available = false;
      connect("anthropic");
      return state.importError
        ? route.fulfill({ status: 502, body: "import refresh failed" })
        : route.fulfill({ json: { status: "ok" } });
    }
    const provider = path.split("/")[3] as SubscriptionProvider;
    if (path.endsWith("/start")) {
      state.starts.push(provider);
      if (state.startError)
        return route.fulfill({ status: 502, body: "sign-in provider unavailable" });
      return route.fulfill({
        json: {
          session_id: `${provider}-session`,
          ...(provider === "anthropic"
            ? { authorize_url: "https://auth.example.test/claude" }
            : { verification_url: "https://auth.example.test/device", user_code: "TEST-CODE" }),
        },
      });
    }
    if (path.endsWith("/logout")) {
      state.status.providers[provider] = { logged_in: false, status: "Not logged in" };
      state.models = state.models.filter((m) => m.source !== labels[provider]);
      return state.logoutError
        ? route.fulfill({ status: 502, body: "sign-out refresh failed" })
        : route.fulfill({ json: { status: "ok" } });
    }
    state.approvals++;
    if (state.completeError)
      return route.fulfill({ status: 400, body: "invalid authorization code" });
    if (path.endsWith("/poll") && state.devicePending)
      return route.fulfill({ json: { done: false } });
    if (provider === "anthropic")
      expect(route.request().postDataJSON()).toEqual({
        session_id: "anthropic-session",
        code: "test-authorization-code",
      });
    connect(provider);
    return route.fulfill({ json: { done: true } });
  });
  await page.route("**/api/chatgpt/**", async (route) => {
    const path = new URL(route.request().url()).pathname;
    if (path.endsWith("/accounts"))
      return route.fulfill({ json: { enabled: true, accounts: state.officialAccounts } });
    if (
      path.endsWith("/sign-in/complete") ||
      (path.endsWith("/sign-in/official-login") && state.officialDone)
    ) {
      const account = {
        id: "official",
        label: "Official ChatGPT plan",
        signed_in: true,
        plan_usage: true,
      };
      state.officialAccounts = [account];
      state.models.push({
        id: "chatgpt:official:model",
        ready: true,
        source: "Official ChatGPT plan",
        source_id: "chatgpt:official",
        mode: "chatgpt-plan",
      });
      const result = { account, new_registration: false };
      return route.fulfill({ json: path.endsWith("/complete") ? result : { done: true, result } });
    }
    if (path.endsWith("/sign-in")) {
      state.officialStarts++;
      await state.officialStartGate;
      return route.fulfill({
        json: {
          id: "official-login",
          authorize_url: "https://auth.example.test/official",
          redirect_uri: "http://127.0.0.1:1455/auth/callback",
        },
      });
    }
    return route.fulfill({ json: { done: false } });
  });
  return state;
}

async function manage(page: Page) {
  await page.goto("/new");
  await openModelPicker(page);
  await page
    .locator(".model-picker-panel")
    .getByRole("button", { name: "Manage Models", exact: true })
    .click();
  return page.getByRole("dialog", { name: "Manage Models", exact: true });
}

async function choose(page: Page, provider: SubscriptionProvider) {
  await page.getByRole("button", { name: "+ Add Models", exact: true }).click();
  await page.getByTestId(`provider-subscription-${provider}`).click();
  return page.getByRole("dialog", { name: "Add Models", exact: true });
}

for (const provider of ["anthropic", "openai", "kimi"] as const) {
  test(`${labels[provider]} connects through Add Models and signs out`, async ({ page }) => {
    const state = await fixture(page);
    await manage(page);
    const form = await choose(page, provider);
    await expect(form.getByTestId("provider-api-key")).not.toBeVisible();
    await expect(form).toContainText(
      provider === "kimi" ? "third-party OAuth client" : "may violate provider terms",
    );
    if (provider === "openai")
      await expect(form).toContainText("not the official Sign in with ChatGPT");
    if (provider === "anthropic")
      await expect(form).not.toContainText("official Sign in with ChatGPT");
    await page.getByTestId("start-subscription-sign-in").click();
    await expect(page.getByTestId("subscription-sign-in-pending")).toBeVisible();
    await expect(page.getByTestId("subscription-authorize-url")).toHaveAttribute(
      "href",
      provider === "anthropic"
        ? "https://auth.example.test/claude"
        : "https://auth.example.test/device",
    );
    if (provider === "anthropic") {
      state.completeError = true;
      await page.getByTestId("subscription-authorization-code").fill("test-authorization-code");
      await page.getByTestId("complete-subscription-sign-in").click();
      await expect(form.getByRole("alert")).toContainText("invalid authorization code");
      state.completeError = false;
    } else {
      await expect(page.getByTestId("subscription-user-code")).toHaveText("TEST-CODE");
      await page.getByTestId("complete-subscription-sign-in").click();
      await expect(form.getByRole("status")).toContainText("not complete yet");
      state.devicePending = false;
    }
    await page.getByTestId("complete-subscription-sign-in").click();
    await expect(form).not.toBeVisible();
    const account = page.getByTestId(`subscription-account-${provider}`);
    await expect(account).toContainText(labels[provider]);
    await expect(account).toContainText("Fleet: owner (epoch 2)");
    await expect(page.locator(".models-modal")).not.toContainText("misleading-api.example.test");
    expect(state.starts).toEqual([provider]);
    expect(state.officialStarts).toBe(0);
    state.logoutError = true;
    await account.getByRole("button", { name: "Sign out", exact: true }).click();
    await expect(
      page.getByRole("dialog", { name: "Manage Models", exact: true }).getByRole("alert"),
    ).toContainText("sign-out refresh failed");
    await expect(account).not.toBeVisible();
  });
}

test("fleet follower with no local login takes over even when no models are available", async ({
  page,
}) => {
  const state = await fixture(page);
  state.status.providers.openai = {
    logged_in: false,
    status: "Using node-a's login",
    fleet_owner: "node-a",
    fleet: "following node-a",
  };
  await manage(page);
  const account = page.getByTestId("subscription-account-openai");
  await expect(account).toContainText("no local sign-in required");
  await expect(account.getByRole("button", { name: "Sign out", exact: true })).toHaveCount(0);
  await account.getByRole("button", { name: "Take over", exact: true }).click();
  const form = page.getByRole("dialog", { name: "Add Models", exact: true });
  await expect(form).toContainText("moves subscription ownership from node-a");
  await page.getByTestId("start-subscription-sign-in").click();
  state.devicePending = false;
  await page.getByTestId("complete-subscription-sign-in").click();
  await expect(form).not.toBeVisible();
  await expect(account).toContainText("owner (epoch 2)");
  await expect(account.getByRole("button", { name: "Sign out", exact: true })).toBeVisible();
});

test("errors, cancellation and reopening do not retain subscription codes", async ({ page }) => {
  const state = await fixture(page);
  await manage(page);
  let form = await choose(page, "anthropic");
  state.startError = true;
  await page.getByTestId("start-subscription-sign-in").click();
  await expect(form.getByRole("alert")).toContainText("sign-in provider unavailable");
  state.startError = false;
  await page.getByTestId("start-subscription-sign-in").click();
  await page.getByTestId("subscription-authorization-code").fill("discard-this-code");
  await form.getByRole("button", { name: "Close modal", exact: true }).click();
  form = await choose(page, "kimi");
  await expect(page.getByTestId("subscription-sign-in-pending")).not.toBeVisible();
  await expect(form).not.toContainText("discard-this-code");
  await page.getByTestId("start-subscription-sign-in").click();
  await expect(page.getByTestId("subscription-user-code")).toHaveText("TEST-CODE");
  await form.getByRole("button", { name: "Cancel", exact: true }).click();
  await expect(page.getByTestId("subscription-sign-in-pending")).not.toBeVisible();
  expect(state.approvals).toBe(0);
});

test("legacy import is explicit and refreshes after partial success", async ({ page }) => {
  const state = await fixture(page);
  state.status.legacy_import_available = true;
  state.importError = true;
  const modal = await manage(page);
  await expect(page.getByTestId("import-legacy-subscriptions")).toBeVisible();
  expect(state.imports).toBe(0);
  await page.getByTestId("import-legacy-subscriptions").click();
  await expect(modal.getByRole("alert")).toContainText("import refresh failed");
  await expect(page.getByTestId("import-legacy-subscriptions")).not.toBeVisible();
  await expect(page.getByTestId("subscription-account-anthropic")).toBeVisible();
  expect(state.imports).toBe(1);
});

test("subscription status failure leaves official ChatGPT and API-key connections usable", async ({
  page,
}) => {
  const state = await fixture(page);
  state.statusError = true;
  const modal = await manage(page);
  await expect(modal.getByRole("alert")).toContainText("subscription status unavailable");
  await page.getByRole("button", { name: "+ Add Models", exact: true }).click();
  await expect(page.getByTestId("provider-api-key")).toBeEnabled();
  await page.getByTestId("provider-api-key").fill("test-fixture-key");
  await expect(page.getByRole("button", { name: "Connect", exact: true })).toBeEnabled();
  await page.getByTestId("provider-chatgpt").click();
  await page.getByTestId("continue-with-chatgpt").click();
  await expect(page.getByTestId("chatgpt-sign-in-pending")).toBeVisible();
  await page
    .getByTestId("chatgpt-callback-url")
    .fill("http://127.0.0.1:1455/auth/callback?code=fake");
  await page.getByRole("button", { name: "Finish sign-in", exact: true }).click();
  await expect(page.getByTestId("chatgpt-account-official")).toBeVisible();
  await expect(page.getByRole("dialog", { name: "Add Models", exact: true })).not.toBeVisible();
  expect(state.officialStarts).toBe(1);
  expect(state.starts).toEqual([]);
});

test("saved provider with a subscription label keeps its own management actions", async ({
  page,
}) => {
  const state = await fixture(page);
  state.providers.push({
    provider_id: "collision",
    provider_type: "anthropic",
    display_name: "Claude subscription",
    endpoint: "https://api.example.test/v1",
    has_api_key: true,
    model_count: 1,
  });
  state.models.push({
    id: "provider:collision:model",
    source_id: "collision",
    source: "Claude subscription",
    ready: true,
    base_url: "https://api.example.test/v1",
  });
  state.status.providers.anthropic = { logged_in: true, status: "Logged in" };
  await manage(page);
  await expect(page.getByTestId("edit-provider-collision")).toBeVisible();
  await expect(page.getByTestId("remove-provider-collision")).toBeVisible();
  await expect(page.getByTestId("provider-connection")).toContainText(
    "https://api.example.test/v1",
  );
  await expect(page.getByTestId("subscription-account-anthropic")).toContainText("(0)");
});

for (const choice of ["codex", "api"] as const) {
  test(`late official ChatGPT pending cannot override a later ${choice} selection`, async ({
    page,
  }) => {
    const state = await fixture(page);
    let release!: () => void;
    state.officialStartGate = new Promise<void>((resolve) => {
      release = resolve;
    });
    await manage(page);
    await page.getByRole("button", { name: "+ Add Models", exact: true }).click();
    const form = page.getByRole("dialog", { name: "Add Models", exact: true });
    const official = page.getByTestId("provider-chatgpt");
    await official.click();
    const starting = page.waitForRequest(
      (request) => new URL(request.url()).pathname === "/api/chatgpt/sign-in",
    );
    await page.getByTestId("continue-with-chatgpt").click();
    await starting;
    const selected =
      choice === "codex"
        ? page.getByTestId("provider-subscription-openai")
        : form.getByRole("button", { name: "OpenAI (Chat API)", exact: true });
    await selected.click();
    // The first status request proves SIWC processed the deferred start and
    // created its pending session. Wait on the event, never a test sleep.
    const pendingReady = page.waitForRequest(
      (request) => new URL(request.url()).pathname === "/api/chatgpt/sign-in/official-login",
    );
    release();
    await pendingReady;
    await expect(selected).toHaveAttribute("aria-pressed", "true");
    await expect(official).toHaveAttribute("aria-pressed", "false");
    await expect(
      form
        .getByTestId("model-provider-form")
        .locator('.provider-buttons button[aria-pressed="true"]'),
    ).toHaveCount(1);
    if (choice === "codex") {
      await expect(page.getByTestId("subscription-connect")).toBeVisible();
      await expect(page.getByTestId("provider-api-key")).not.toBeVisible();
    } else {
      await expect(page.getByTestId("provider-api-key")).toBeVisible();
      await expect(page.getByTestId("provider-endpoint")).toHaveValue("https://api.openai.com/v1");
    }
    await expect(page.getByTestId("chatgpt-sign-in-pending")).not.toBeVisible();
    await official.click();
    await expect(page.getByTestId("chatgpt-sign-in-pending")).toBeVisible();
    await expect(
      form
        .getByTestId("model-provider-form")
        .locator('.provider-buttons button[aria-pressed="true"]'),
    ).toHaveCount(1);
    await expect(page.getByTestId("chatgpt-sign-in-pending").getByRole("link")).toHaveAttribute(
      "href",
      "https://auth.example.test/official",
    );
    expect(state.officialStarts).toBe(1);
    expect(state.starts).toEqual([]);
  });
}

for (const choice of ["subscription", "api", "single"] as const) {
  test(`official ChatGPT completion preserves a later ${choice} form`, async ({ page }) => {
    const state = await fixture(page);
    await manage(page);
    await page.getByRole("button", { name: "+ Add Models", exact: true }).click();
    const form = page.getByRole("dialog", { name: "Add Models", exact: true });
    await page.getByTestId("provider-chatgpt").click();
    await page.getByTestId("continue-with-chatgpt").click();
    await expect(page.getByTestId("chatgpt-sign-in-pending")).toBeVisible();
    if (choice === "subscription") {
      await page.getByTestId("provider-subscription-anthropic").click();
      await page.getByTestId("start-subscription-sign-in").click();
      await expect(page.getByTestId("subscription-sign-in-pending")).toBeVisible();
      await page.getByTestId("subscription-authorization-code").fill("unfinished-claude-code");
    } else if (choice === "api") {
      await form.getByRole("button", { name: "OpenAI (Chat API)", exact: true }).click();
      await page.getByTestId("provider-api-key").fill("unfinished-api-key");
    } else {
      await page.getByTestId("model-mode-single").click();
      await form
        .getByPlaceholder("Name shown in the model selector", { exact: true })
        .fill("Unfinished model");
    }
    // The upstream status loop consumes a mocked success, without OAuth/network.
    state.officialDone = true;
    await expect(page.getByTestId("chatgpt-account-official")).toBeVisible();
    await expect(form).toBeVisible();
    if (choice === "subscription") {
      await expect(page.getByTestId("subscription-sign-in-pending")).toBeVisible();
      await expect(page.getByTestId("subscription-authorization-code")).toHaveValue(
        "unfinished-claude-code",
      );
      expect(state.starts).toEqual(["anthropic"]);
      expect(state.approvals).toBe(0);
    } else if (choice === "api") {
      await expect(page.getByTestId("provider-api-key")).toHaveValue("unfinished-api-key");
      await expect(
        form.getByRole("button", { name: "OpenAI (Chat API)", exact: true }),
      ).toHaveAttribute("aria-pressed", "true");
    } else {
      await expect(page.getByTestId("model-mode-single")).toHaveAttribute("aria-pressed", "true");
      await expect(
        form.getByPlaceholder("Name shown in the model selector", { exact: true }),
      ).toHaveValue("Unfinished model");
    }
    expect(state.officialStarts).toBe(1);
  });
}
