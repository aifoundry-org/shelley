import { reactive } from "vue";
import {
  api,
  type SubscriptionLoginStart,
  type SubscriptionProvider,
  type SubscriptionsStatus,
} from "../../services/api";
import type { I18n } from "./i18n";

// Source keys are stable backend identifiers, separate from display labels.
// None of these are OpenAI SIWC accounts.
export const subscriptionProviders = [
  { id: "anthropic", source: "Claude subscription", label: "Claude" },
  { id: "openai", source: "ChatGPT (Codex) subscription", label: "Codex" },
  { id: "kimi", source: "Kimi Code subscription", label: "Kimi" },
] as const;

export function subscriptionForSource(
  source: string,
  sourceID?: string,
): SubscriptionProvider | undefined {
  return sourceID ? undefined : subscriptionProviders.find((p) => p.source === source)?.id;
}

export function subscriptionSourceLabel(source: string, sourceID?: string): string {
  const provider = subscriptionForSource(source, sourceID);
  return provider ? subscriptionProviders.find((p) => p.id === provider)!.label : source;
}

export function useSubscriptionConnections(
  t: I18n["t"],
  modelsChanged: () => Promise<void>,
  signedIn: () => void,
) {
  const state = reactive({
    status: null as SubscriptionsStatus | null,
    loading: false,
    busy: false,
    error: null as string | null,
    pendingMessage: null as string | null,
    pending: null as { provider: SubscriptionProvider; session: SubscriptionLoginStart } | null,
  });
  let generation = 0;
  const describe = (err: unknown) => (err instanceof Error ? err.message : String(err));
  const follower = (provider: SubscriptionProvider) =>
    !!state.status?.providers[provider]?.fleet_owner;
  const connected = (provider: SubscriptionProvider) =>
    !!state.status?.providers[provider]?.logged_in ||
    follower(provider) ||
    !!state.status?.providers[provider]?.fleet;

  async function load() {
    state.loading = true;
    try {
      state.status = await api.getSubscriptions();
      state.error = null;
    } catch (err) {
      state.error = describe(err);
    } finally {
      state.loading = false;
    }
  }

  function cancel() {
    generation++;
    state.pending = null;
    state.pendingMessage = null;
    state.error = null;
  }

  async function changed() {
    // Refresh both even if one fails: credentials may already have changed.
    const results = await Promise.allSettled([api.getSubscriptions(), modelsChanged()]);
    if (results[0].status === "fulfilled") state.status = results[0].value;
    const failure = results.find((r) => r.status === "rejected");
    if (failure?.status === "rejected") throw failure.reason;
  }

  async function start(provider: SubscriptionProvider) {
    if (state.busy || state.pending) return;
    state.busy = true;
    state.error = null;
    state.pendingMessage = null;
    const operation = ++generation;
    // Open synchronously inside the click, not after the API request.
    const tab = window.open("about:blank", "_blank");
    if (tab) tab.opener = null;
    try {
      const session = await api.startSubscriptionLogin(provider);
      if (operation !== generation) {
        tab?.close();
        return;
      }
      const url = provider === "anthropic" ? session.authorize_url : session.verification_url;
      if (!session.session_id || !url || (provider !== "anthropic" && !session.user_code)) {
        throw new Error(t("subscriptionInvalidSession"));
      }
      state.pending = { provider, session };
      if (tab) tab.location.href = url;
    } catch (err) {
      tab?.close();
      if (operation === generation) state.error = describe(err);
    } finally {
      state.busy = false;
    }
  }

  async function complete(code = "") {
    const pending = state.pending;
    if (!pending || state.busy || (pending.provider === "anthropic" && !code.trim())) return;
    state.busy = true;
    state.error = null;
    state.pendingMessage = null;
    const operation = generation;
    const before = state.status?.providers[pending.provider];
    try {
      if (pending.provider === "anthropic") {
        await api.completeAnthropicSubscriptionLogin(pending.session.session_id, code.trim());
      } else {
        const result = await api.pollDeviceSubscriptionLogin(
          pending.provider,
          pending.session.session_id,
        );
        if (!result.done) {
          if (operation === generation) state.pendingMessage = t("subscriptionNotApproved");
          return;
        }
      }
      if (operation === generation) state.pending = null;
      await changed();
      if (operation === generation) signedIn();
    } catch (err) {
      // The server may have saved credentials and consumed the session before
      // failing to refresh models. Always reconcile, even after cancellation.
      const message = describe(err);
      try {
        await changed();
      } catch {
        // Preserve the original completion error, not a secondary refresh error.
      }
      if (operation === generation) {
        const after = state.status?.providers[pending.provider];
        const saved =
          after?.logged_in &&
          !after.fleet_owner &&
          (!before?.logged_in ||
            before.fleet_owner ||
            before.expires_at !== after.expires_at ||
            before.fleet !== after.fleet);
        if (saved) state.pending = null;
        state.error = saved ? `${message} ${t("subscriptionConnectedAfterError")}` : message;
      }
    } finally {
      state.busy = false;
    }
  }

  async function mutate(action: () => Promise<void>) {
    state.busy = true;
    state.error = null;
    try {
      await action();
    } catch (err) {
      state.error = describe(err);
    } finally {
      // Persistence may have succeeded before a server-side refresh failed.
      try {
        await changed();
      } catch (err) {
        if (!state.error) state.error = describe(err);
      }
      state.busy = false;
    }
  }

  async function signOut(provider: SubscriptionProvider) {
    if (state.busy || follower(provider) || !state.status?.providers[provider]?.logged_in) return;
    await mutate(() => api.logoutSubscription(provider));
  }

  async function importLegacy() {
    if (state.busy || !state.status?.legacy_import_available) return;
    await mutate(() => api.importLegacySubscriptions());
  }

  return Object.assign(state, {
    load,
    cancel,
    start,
    complete,
    signOut,
    follower,
    connected,
    importLegacy,
  });
}

export type SubscriptionConnections = ReturnType<typeof useSubscriptionConnections>;
