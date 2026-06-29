<!--
  ChatOverflowMenu.vue — the top-right "kebab" overflow menu.

  This is the first piece of the UI rebuilt on real PrimeVue *components*
  (the rest of the Vue world so far only consumes the PrimeVue *theme*). It
  replaces a hand-rolled dropdown — a `v-if` panel with a manual document
  `mousedown` outside-click listener and bespoke segmented-toggle rows — with:

    - <Popover>     the dropdown surface (dismiss-on-outside-click + Esc + focus
                    trap come for free, so we delete the manual handlers)
    - <SelectButton> the theme / notifications / markdown segmented toggles
    - <Select>      the language picker

  The e2e DOM/ARIA contract is preserved so the shared Playwright specs keep
  passing in BOTH worlds:
    - root wrapper:  .chat-overflow-menu-wrapper
    - trigger:       button.btn-icon  (aria-label = t('moreOptions'))
    - action items:  button.overflow-menu-item  (matched by visible text)
  See e2e/agents-md-vim.spec.ts and e2e/diff-viewer-find.spec.ts.

  State the menu reads/writes lives in shared composables/services
  (markdownMode, theme, notifications), so this component owns it directly
  instead of taking a dozen props. Conversation-scoped actions (diffs, git
  graph, archive, export, …) are surfaced as events for ChatInterface to wire
  to its existing handlers.
-->
<template>
  <div class="chat-overflow-menu-wrapper">
    <Button
      class="btn-icon"
      text
      severity="secondary"
      :aria-label="t('moreOptions')"
      aria-haspopup="true"
      :aria-expanded="open"
      @click="toggle"
    >
      <svg fill="none" stroke="currentColor" viewBox="0 0 24 24">
        <path
          stroke-linecap="round"
          stroke-linejoin="round"
          :stroke-width="2"
          d="M12 5v.01M12 12v.01M12 19v.01M12 6a1 1 0 110-2 1 1 0 010 2zm0 7a1 1 0 110-2 1 1 0 010 2zm0 7a1 1 0 110-2 1 1 0 010 2z"
        />
      </svg>
      <span v-if="hasUpdate" class="version-update-dot" />
    </Button>

    <Popover
      ref="popoverRef"
      :pt="{ root: { class: 'chat-overflow-popover' }, content: { class: 'overflow-menu-panel' } }"
      @show="open = true"
      @hide="open = false"
    >
      <!-- Conversation / workspace actions -->
      <button v-if="hasCwd" class="overflow-menu-item" @click="onDiffs">
        <!-- Diffs: two rows of +/- changes -->
        <svg
          fill="none"
          stroke="currentColor"
          stroke-width="2"
          stroke-linecap="round"
          stroke-linejoin="round"
          viewBox="0 0 24 24"
          class="chat-menu-icon"
          aria-hidden="true"
        >
          <path d="M4 7h4M6 5v4" />
          <path d="M14 7h6" />
          <path d="M4 17h6" />
          <path d="M14 17h6M17 15v4" />
        </svg>
        {{ t("diffs") }}
      </button>
      <button v-if="hasCwd" class="overflow-menu-item" @click="onGitGraph">
        <!-- Git graph: commits A (top) and B (top-right) branching from C (bottom) -->
        <svg
          fill="none"
          stroke="currentColor"
          stroke-width="2"
          stroke-linecap="round"
          stroke-linejoin="round"
          viewBox="0 0 24 24"
          class="chat-menu-icon"
          aria-hidden="true"
        >
          <path d="M6 16.6V7.4" />
          <path d="M6 16.6C8 11 12 6 14.6 5" />
          <circle cx="6" cy="5" r="2.4" />
          <circle cx="17" cy="5" r="2.4" />
          <circle cx="6" cy="19" r="2.4" />
        </svg>
        {{ t("gitGraph") }}
      </button>
      <button v-if="terminalUrl" class="overflow-menu-item" @click="onTerminal">
        <i class="pi pi-desktop chat-menu-icon" aria-hidden="true" />
        {{ t("terminal") }}
      </button>

      <!-- Custom server-provided links (icon is a raw SVG path) -->
      <button
        v-for="(link, index) in links"
        :key="index"
        class="overflow-menu-item"
        @click="onExternalLink(link.url)"
      >
        <svg fill="none" stroke="currentColor" viewBox="0 0 24 24" class="chat-menu-icon">
          <path
            stroke-linecap="round"
            stroke-linejoin="round"
            :stroke-width="2"
            :d="
              link.icon_svg ||
              'M10 6H6a2 2 0 00-2 2v10a2 2 0 002 2h10a2 2 0 002-2v-4M14 4h6m0 0v6m0-6L10 14'
            "
          />
        </svg>
        {{ link.title }}
      </button>

      <template v-if="canArchive">
        <div class="overflow-menu-divider" />
        <button class="overflow-menu-item" @click="onArchive">
          <i class="pi pi-inbox chat-menu-icon" aria-hidden="true" />
          {{ t("archiveConversation") }}
        </button>
      </template>

      <template v-if="canExport">
        <div class="overflow-menu-divider" />
        <button class="overflow-menu-item" @click="onExport">
          <i class="pi pi-download chat-menu-icon" aria-hidden="true" />
          {{ t("exportConversation") }}
        </button>
      </template>

      <div class="overflow-menu-divider" />
      <div class="subscriptions-menu-section">
        <button class="overflow-menu-item subscriptions-menu-header" @click="toggleSubscriptions">
          <i class="pi pi-key chat-menu-icon" aria-hidden="true" />
          <span>Subscription accounts</span>
          <span class="subscriptions-menu-chevron">{{ subscriptionsExpanded ? "▾" : "▸" }}</span>
        </button>
        <div v-if="subscriptionsExpanded" class="subscriptions-menu-body">
          <div v-if="subscriptionError" class="subscriptions-menu-error">
            {{ subscriptionError }}
          </div>
          <div v-if="!subscriptions && !subscriptionError" class="subscriptions-menu-muted">
            Loading…
          </div>
          <div
            v-for="provider in subscriptionProviders"
            :key="provider.id"
            class="subscriptions-menu-provider"
          >
            <div class="subscriptions-menu-provider-row">
              <div>
                <div class="subscriptions-menu-provider-name">{{ provider.label }}</div>
                <div
                  :class="[
                    'subscriptions-menu-status',
                    subscriptions?.providers[provider.id]?.logged_in
                      ? 'subscriptions-menu-status-ok'
                      : '',
                  ]"
                >
                  {{ subscriptions?.providers[provider.id]?.status || "Unknown" }}
                </div>
              </div>
              <button
                v-if="subscriptions?.providers[provider.id]?.logged_in"
                class="subscriptions-menu-small-btn"
                :disabled="subscriptionBusy === provider.id"
                @click="logoutSubscription(provider.id)"
              >
                Logout
              </button>
              <button
                v-else
                class="subscriptions-menu-small-btn subscriptions-menu-primary-btn"
                :disabled="subscriptionBusy === provider.id"
                @click="startSubscriptionLogin(provider.id)"
              >
                Login
              </button>
            </div>

            <div
              v-if="subscriptionLogin?.provider === 'openai' && provider.id === 'openai'"
              class="subscriptions-login-box"
            >
              <div>Open this URL and enter the code:</div>
              <a
                :href="subscriptionLogin.session.verification_url"
                target="_blank"
                rel="noopener noreferrer"
              >
                {{ subscriptionLogin.session.verification_url }}
              </a>
              <div class="subscriptions-login-code">{{ subscriptionLogin.session.user_code }}</div>
              <button
                class="subscriptions-menu-small-btn subscriptions-menu-primary-btn"
                :disabled="subscriptionLogin.polling || subscriptionBusy === 'openai'"
                @click="pollOpenAILogin"
              >
                I approved it
              </button>
            </div>

            <div
              v-if="subscriptionLogin?.provider === 'anthropic' && provider.id === 'anthropic'"
              class="subscriptions-login-box"
            >
              <div>Paste the authorization code from Anthropic:</div>
              <textarea
                v-model="subscriptionLogin.code"
                class="subscriptions-code-input"
                rows="2"
              />
              <button
                class="subscriptions-menu-small-btn subscriptions-menu-primary-btn"
                :disabled="!subscriptionLogin.code.trim() || subscriptionBusy === 'anthropic'"
                @click="completeAnthropicLogin"
              >
                Complete login
              </button>
            </div>
          </div>
        </div>
      </div>

      <div class="overflow-menu-divider" />
      <button class="overflow-menu-item" @click="onEditAgentsMd">
        <i class="pi pi-pencil chat-menu-icon" aria-hidden="true" />
        {{ t("editUserAgentsMd") }}
      </button>

      <div class="overflow-menu-divider" />
      <button class="overflow-menu-item" @click="onCheckVersion">
        <i class="pi pi-refresh chat-menu-icon" aria-hidden="true" />
        {{ t("checkForNewVersion") }}
        <span v-if="hasUpdate" class="version-menu-dot" />
      </button>

      <!-- Theme: System / Light / Dark -->
      <div class="overflow-menu-divider" />
      <div class="overflow-menu-control">
        <SelectButton
          v-model="theme"
          :options="themeOptions"
          option-value="value"
          data-key="value"
          :allow-empty="false"
          :aria-label="t('system') + ' / ' + t('light') + ' / ' + t('dark')"
          @update:model-value="onThemeChange"
        >
          <template #option="{ option }">
            <i :class="['pi', option.icon]" :title="option.label" aria-hidden="true" />
            <span class="sr-only-label">{{ option.label }}</span>
          </template>
        </SelectButton>
      </div>

      <!-- Browser notifications: on / off -->
      <template v-if="notificationSupported">
        <div class="overflow-menu-divider" />
        <div class="overflow-menu-control">
          <SelectButton
            v-model="notifEnabled"
            :options="notifOptions"
            option-value="value"
            option-disabled="disabled"
            data-key="value"
            :allow-empty="false"
            :aria-label="t('enableNotifications') + ' / ' + t('disableNotifications')"
            @update:model-value="onNotifChange"
          >
            <template #option="{ option }">
              <i :class="['pi', option.icon]" :title="option.label" aria-hidden="true" />
              <span class="sr-only-label">{{ option.label }}</span>
            </template>
          </SelectButton>
        </div>
      </template>

      <!-- Markdown rendering: Off / Agent / All -->
      <div class="overflow-menu-divider" />
      <div class="overflow-menu-control">
        <div class="md-toggle-label">{{ t("markdown") }}</div>
        <SelectButton
          v-model="markdown"
          :options="markdownOptions"
          option-label="label"
          option-value="value"
          data-key="value"
          :allow-empty="false"
          :aria-label="t('markdown')"
          @update:model-value="onMarkdownChange"
        />
      </div>

      <!-- Language + report-a-bug link -->
      <div class="overflow-menu-divider" />
      <div class="overflow-menu-control">
        <div class="md-toggle-label">
          {{ t("language") }}
          <a
            :href="reportBugHref"
            target="_blank"
            rel="noopener noreferrer"
            class="report-bug-link"
            @click.stop
          >
            [{{ t("reportBug") }}]
          </a>
        </div>
        <Select
          v-model="lang"
          :options="languageOptions"
          option-label="label"
          option-value="locale"
          :aria-label="t('switchLanguage')"
          class="overflow-language-select"
          append-to="self"
          @update:model-value="onLangChange"
        >
          <template #value="{ value }">
            <span class="language-dropdown-flag">{{ languageFor(value).flag }}</span>
            <span>{{ languageFor(value).label }}</span>
          </template>
          <template #option="{ option }">
            <span class="language-dropdown-flag">{{ option.flag }}</span>
            <span>{{ option.label }}</span>
          </template>
        </Select>
      </div>
    </Popover>
  </div>
</template>

<script setup lang="ts">
import { computed, ref } from "vue";
import Popover from "primevue/popover";
import Button from "primevue/button";
import SelectButton from "primevue/selectbutton";
import Select from "primevue/select";
import { api, type SubscriptionLoginStart, type SubscriptionsStatus } from "../../services/api";
import type { Link } from "../../types";
import type { Locale } from "../../i18n/types";
import { useI18n } from "../composables/i18n";
import { useMarkdownMode, type MarkdownMode } from "../composables/markdownMode";
import { type ThemeMode, getStoredTheme, setStoredTheme, applyTheme } from "../../services/theme";
import {
  isChannelEnabled,
  setChannelEnabled,
  getBrowserNotificationState,
  requestBrowserNotificationPermission,
} from "../../services/notifications";

defineProps<{
  hasCwd: boolean;
  terminalUrl: string | null;
  links: Link[];
  canArchive: boolean;
  canExport: boolean;
  hasUpdate: boolean;
}>();

const emit = defineEmits<{
  (e: "open-diffs"): void;
  (e: "open-git-graph"): void;
  (e: "open-terminal"): void;
  (e: "open-external-link", url: string): void;
  (e: "archive"): void;
  (e: "export"): void;
  (e: "edit-agents-md"): void;
  (e: "check-version"): void;
  (e: "models-changed"): void;
}>();

const { t, locale, setLocale } = useI18n();
const { markdownMode, setMarkdownMode } = useMarkdownMode();

const popoverRef = ref<InstanceType<typeof Popover> | null>(null);
const open = ref(false);

function toggle(event: MouseEvent) {
  popoverRef.value?.toggle(event);
}
function hide() {
  popoverRef.value?.hide();
}

// Each action emits its event, then closes the Popover. Kept as explicit
// one-liners (rather than a union-typed helper) so defineEmits' per-event
// overloads type-check cleanly.
const onDiffs = () => (emit("open-diffs"), hide());
const onGitGraph = () => (emit("open-git-graph"), hide());
const onTerminal = () => (emit("open-terminal"), hide());
const onArchive = () => (emit("archive"), hide());
const onExport = () => (emit("export"), hide());
const onEditAgentsMd = () => (emit("edit-agents-md"), hide());
const onCheckVersion = () => (emit("check-version"), hide());
function onExternalLink(url: string) {
  emit("open-external-link", url);
  hide();
}

type SubscriptionProvider = "anthropic" | "openai";
const subscriptionProviders: Array<{ id: SubscriptionProvider; label: string }> = [
  { id: "anthropic", label: "Anthropic" },
  { id: "openai", label: "OpenAI" },
];
const subscriptionsExpanded = ref(false);
const subscriptions = ref<SubscriptionsStatus | null>(null);
const subscriptionBusy = ref<SubscriptionProvider | null>(null);
const subscriptionError = ref<string | null>(null);
const subscriptionLogin = ref<{
  provider: SubscriptionProvider;
  session: SubscriptionLoginStart;
  code: string;
  polling: boolean;
} | null>(null);

async function refreshSubscriptions() {
  subscriptions.value = await api.getSubscriptions();
}

async function toggleSubscriptions() {
  subscriptionsExpanded.value = !subscriptionsExpanded.value;
  if (!subscriptionsExpanded.value || subscriptions.value) return;
  subscriptionError.value = null;
  try {
    await refreshSubscriptions();
  } catch (err) {
    subscriptionError.value = err instanceof Error ? err.message : String(err);
  }
}

async function refreshAfterSubscriptionChange() {
  await refreshSubscriptions();
  emit("models-changed");
}

async function logoutSubscription(provider: SubscriptionProvider) {
  subscriptionBusy.value = provider;
  subscriptionError.value = null;
  try {
    await api.logoutSubscription(provider);
    await refreshAfterSubscriptionChange();
  } catch (err) {
    subscriptionError.value = err instanceof Error ? err.message : String(err);
  } finally {
    subscriptionBusy.value = null;
  }
}

async function startSubscriptionLogin(provider: SubscriptionProvider) {
  subscriptionBusy.value = provider;
  subscriptionError.value = null;
  try {
    const session = await api.startSubscriptionLogin(provider);
    subscriptionLogin.value = { provider, session, code: "", polling: false };
    if (provider === "anthropic" && session.authorize_url) {
      window.open(session.authorize_url, "_blank", "noopener");
    }
  } catch (err) {
    subscriptionError.value = err instanceof Error ? err.message : String(err);
  } finally {
    subscriptionBusy.value = null;
  }
}

async function pollOpenAILogin() {
  if (!subscriptionLogin.value || subscriptionLogin.value.provider !== "openai") return;
  subscriptionLogin.value.polling = true;
  subscriptionBusy.value = "openai";
  subscriptionError.value = null;
  try {
    const result = await api.pollOpenAISubscriptionLogin(
      subscriptionLogin.value.session.session_id,
    );
    if (result.done) {
      subscriptionLogin.value = null;
      await refreshAfterSubscriptionChange();
    } else {
      subscriptionError.value = "Not approved yet. Approve in the browser, then try again.";
    }
  } catch (err) {
    subscriptionError.value = err instanceof Error ? err.message : String(err);
  } finally {
    subscriptionBusy.value = null;
    if (subscriptionLogin.value) subscriptionLogin.value.polling = false;
  }
}

async function completeAnthropicLogin() {
  if (!subscriptionLogin.value || subscriptionLogin.value.provider !== "anthropic") return;
  subscriptionBusy.value = "anthropic";
  subscriptionError.value = null;
  try {
    await api.completeAnthropicSubscriptionLogin(
      subscriptionLogin.value.session.session_id,
      subscriptionLogin.value.code,
    );
    subscriptionLogin.value = null;
    await refreshAfterSubscriptionChange();
  } catch (err) {
    subscriptionError.value = err instanceof Error ? err.message : String(err);
  } finally {
    subscriptionBusy.value = null;
  }
}

const notificationSupported = typeof Notification !== "undefined";

// ---- Theme toggle (System / Light / Dark) ----
// Option labels are computed so they re-translate when the locale changes (the
// language Select lives in this same popover, so a switch is visible live).
const theme = ref<ThemeMode>(getStoredTheme());
const themeOptions = computed(() => [
  { value: "system" as ThemeMode, icon: "pi-desktop", label: t("system") },
  { value: "light" as ThemeMode, icon: "pi-sun", label: t("light") },
  { value: "dark" as ThemeMode, icon: "pi-moon", label: t("dark") },
]);
function onThemeChange(mode: ThemeMode) {
  theme.value = mode;
  setStoredTheme(mode);
  applyTheme(mode);
}

// ---- Browser notifications (on / off) ----
const notifEnabled = ref<boolean>(isChannelEnabled("browser"));
const notifBlocked = getBrowserNotificationState() === "denied";
const notifOptions = computed(() => [
  {
    value: true,
    icon: "pi-bell",
    label: notifBlocked ? t("blockedByBrowser") : t("enableNotifications"),
    disabled: notifBlocked,
  },
  { value: false, icon: "pi-bell-slash", label: t("disableNotifications"), disabled: false },
]);
async function onNotifChange(next: boolean) {
  if (next) {
    // v-model has already flipped notifEnabled to true; confirm via the
    // permission prompt and revert if the user denies.
    const granted = await requestBrowserNotificationPermission();
    notifEnabled.value = granted;
  } else {
    setChannelEnabled("browser", false);
    notifEnabled.value = false;
  }
}

// ---- Markdown rendering (Off / Agent / All) ----
const markdown = ref<MarkdownMode>(markdownMode.value);
const markdownOptions = computed(() => [
  { value: "off" as MarkdownMode, label: t("off") },
  { value: "agent" as MarkdownMode, label: t("agent") },
  { value: "all" as MarkdownMode, label: t("all") },
]);
function onMarkdownChange(mode: MarkdownMode) {
  markdown.value = mode;
  setMarkdownMode(mode);
}

// ---- Language picker ----
interface LanguageOption {
  locale: Locale;
  flag: string;
  label: string;
}
const languageOptions: LanguageOption[] = [
  { locale: "en", flag: "\uD83C\uDDFA\uD83C\uDDF8", label: "English" },
  { locale: "ja", flag: "\uD83C\uDDEF\uD83C\uDDF5", label: "\u65E5\u672C\u8A9E" },
  { locale: "fr", flag: "\uD83C\uDDEB\uD83C\uDDF7", label: "Fran\u00E7ais" },
  {
    locale: "ru",
    flag: "\uD83C\uDDF7\uD83C\uDDFA",
    label: "\u0420\u0443\u0441\u0441\u043A\u0438\u0439",
  },
  { locale: "es", flag: "\uD83C\uDDEA\uD83C\uDDF8", label: "Espa\u00F1ol" },
  { locale: "zh-CN", flag: "\uD83C\uDDE8\uD83C\uDDF3", label: "\u7B80\u4F53\u4E2D\u6587" },
  { locale: "zh-TW", flag: "\uD83C\uDDF9\uD83C\uDDFC", label: "\u7E41\u9AD4\u4E2D\u6587" },
  { locale: "vi", flag: "\uD83C\uDDFB\uD83C\uDDF3", label: "Ti\u1EBFng Vi\u1EC7t" },
  { locale: "upgoer5", flag: "\uD83D\uDE80", label: "Up-Goer Five" },
];
const lang = ref<Locale>(locale.value);
function languageFor(l: Locale): LanguageOption {
  return languageOptions.find((o) => o.locale === l) || languageOptions[0];
}
function onLangChange(l: Locale) {
  lang.value = l;
  setLocale(l);
}

const reportBugHref = `https://github.com/boldsoftware/shelley/issues/new?labels=translation&title=${encodeURIComponent(
  "Translation issue: ",
)}&body=${encodeURIComponent(
  "**Language:** \n**Where in the UI:** \n**Current text:** \n**Suggested text:** \n",
)}`;
</script>
