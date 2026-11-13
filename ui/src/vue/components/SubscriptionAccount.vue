<template>
  <div class="subscription-account" :data-testid="`subscription-account-${provider}`">
    <div class="models-provider-line">
      <span class="models-group-name">{{
        subscriptionProviders.find((p) => p.id === provider)!.label
      }}</span>
      <span class="models-group-count">({{ count }})</span>
      <span class="subscription-status">{{ status?.status }}</span>
      <span class="models-provider-actions">
        <Button
          v-if="status?.logged_in && !connections.follower(provider)"
          size="small"
          severity="secondary"
          text
          :label="t('subscriptionSignOut')"
          :disabled="connections.busy"
          @click="connections.signOut(provider)"
        />
        <Button
          v-else
          size="small"
          severity="secondary"
          text
          :label="t(connections.follower(provider) ? 'subscriptionTakeOver' : 'subscriptionSignIn')"
          :disabled="connections.busy"
          @click="emit('connect', provider)"
        />
      </span>
    </div>
    <div class="subscription-detail">
      <span v-if="status?.fleet">{{ t("subscriptionFleet").replace("{role}", status.fleet) }}</span>
      <span v-else-if="status?.logged_in">{{ t("subscriptionLocal") }}</span>
      <span v-if="connections.follower(provider)">{{
        t("subscriptionFollowing").replace("{owner}", status?.fleet_owner || "")
      }}</span>
      <span v-if="!status?.logged_in && !connections.follower(provider) && status?.fallback">{{
        t("subscriptionFallback").replace("{source}", status.fallback)
      }}</span>
      <span>{{
        t(
          provider === "kimi"
            ? "subscriptionKimiNotice"
            : provider === "openai"
              ? "subscriptionCodexNotice"
              : "subscriptionUnofficial",
        )
      }}</span>
    </div>
  </div>
</template>
<script setup lang="ts">
import { computed } from "vue";
import Button from "primevue/button";
import { useI18n } from "../composables/i18n";
import {
  subscriptionProviders,
  type SubscriptionConnections,
} from "../composables/subscriptionConnections";
import type { SubscriptionProvider } from "../../services/api";
const props = defineProps<{
  provider: SubscriptionProvider;
  connections: SubscriptionConnections;
  count: number;
}>();
const emit = defineEmits<{ (e: "connect", provider: SubscriptionProvider): void }>();
const { t } = useI18n();
const status = computed(() => props.connections.status?.providers[props.provider]);
</script>
<style scoped>
.subscription-account {
  width: 100%;
}
.subscription-status,
.subscription-detail {
  color: var(--text-secondary);
  font-size: 0.8rem;
}
.subscription-detail {
  display: flex;
  flex-direction: column;
  gap: 0.2rem;
  font-weight: normal;
  white-space: normal;
}
</style>
