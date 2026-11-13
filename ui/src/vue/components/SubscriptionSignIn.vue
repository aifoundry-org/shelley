<!-- Fork subscription OAuth, deliberately separate from upstream OpenAI SIWC. -->
<template>
  <div data-testid="subscription-connect">
    <p class="form-hint">
      {{
        t(
          provider === "kimi"
            ? "subscriptionKimiNotice"
            : provider === "openai"
              ? "subscriptionCodexNotice"
              : "subscriptionUnofficial",
        )
      }}
    </p>
    <div v-if="connections.follower(provider)" class="form-hint">
      {{ t("subscriptionFollowing").replace("{owner}", status?.fleet_owner || "") }}
      {{ t("subscriptionTakeOverHint").replace("{owner}", status?.fleet_owner || "") }}
    </div>
    <div v-if="connections.error" class="models-error" role="alert">{{ connections.error }}</div>
    <div v-if="pending" class="subscription-pending" data-testid="subscription-sign-in-pending">
      <p>
        {{ t("subscriptionWaiting") }}
        <a
          :href="
            provider === 'anthropic'
              ? pending.session.authorize_url
              : pending.session.verification_url
          "
          target="_blank"
          rel="noopener noreferrer"
          data-testid="subscription-authorize-url"
          >{{ t("subscriptionOpenBrowser") }}</a
        >
      </p>
      <template v-if="provider !== 'anthropic'">
        <p>{{ t("subscriptionDeviceCode") }}</p>
        <code class="subscription-device-code" data-testid="subscription-user-code">{{
          pending.session.user_code
        }}</code>
      </template>
      <div v-else class="form-group">
        <label for="subscription-auth-code">{{ t("subscriptionPasteCode") }}</label>
        <input
          id="subscription-auth-code"
          v-model="code"
          type="password"
          autocomplete="off"
          data-testid="subscription-authorization-code"
          :disabled="connections.busy"
        />
      </div>
      <p v-if="connections.pendingMessage" role="status">{{ connections.pendingMessage }}</p>
      <div class="form-actions">
        <Button
          type="button"
          size="small"
          severity="secondary"
          :label="t('cancel')"
          @click="connections.cancel()"
        />
        <Button
          type="button"
          size="small"
          :label="t(provider === 'anthropic' ? 'subscriptionComplete' : 'subscriptionApproved')"
          :disabled="connections.busy || (provider === 'anthropic' && !code.trim())"
          data-testid="complete-subscription-sign-in"
          @click="connections.complete(code)"
        />
      </div>
    </div>
    <div v-else class="form-actions">
      <Button type="button" severity="secondary" :label="t('cancel')" @click="emit('close')" />
      <Button
        type="button"
        :label="t(connections.follower(provider) ? 'subscriptionTakeOver' : 'subscriptionSignIn')"
        :disabled="connections.busy || !!connections.pending"
        data-testid="start-subscription-sign-in"
        @click="connections.start(provider)"
      />
    </div>
  </div>
</template>

<script setup lang="ts">
import { computed, ref, watch } from "vue";
import Button from "primevue/button";
import { useI18n } from "../composables/i18n";
import type { SubscriptionConnections } from "../composables/subscriptionConnections";
import type { SubscriptionProvider } from "../../services/api";
const props = defineProps<{
  provider: SubscriptionProvider;
  connections: SubscriptionConnections;
}>();
const emit = defineEmits<{ (e: "close"): void }>();
const { t } = useI18n();
const code = ref("");
const status = computed(() => props.connections.status?.providers[props.provider]);
const pending = computed(() =>
  props.connections.pending?.provider === props.provider ? props.connections.pending : null,
);
watch(
  () => pending.value?.session.session_id,
  () => {
    code.value = "";
  },
);
</script>

<style scoped>
.subscription-pending {
  display: flex;
  flex-direction: column;
  gap: 0.4rem;
  padding: 0.6rem 0.75rem;
  border: 1px solid var(--blue-border);
  border-radius: 6px;
  background: var(--blue-bg);
}
.subscription-pending p {
  margin: 0;
  font-size: 0.875rem;
}
.subscription-pending a {
  color: var(--link-color);
}
.subscription-device-code {
  font-size: 1.2rem;
  font-weight: 600;
  letter-spacing: 0.1em;
}
.subscription-pending input {
  padding: 0.35rem 0.5rem;
  border: 1px solid var(--border);
  border-radius: 4px;
  background: var(--bg-base);
  color: var(--text-primary);
  width: 100%;
}
</style>
