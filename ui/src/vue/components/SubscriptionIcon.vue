<!-- Original stylized provider icons; Codex's code glyph is distinct from SIWC. -->
<template>
  <svg
    class="subscription-icon"
    :data-subscription-icon="provider"
    :width="size"
    :height="size"
    viewBox="0 0 24 24"
    fill="none"
    stroke="currentColor"
    stroke-width="2"
    stroke-linecap="round"
    stroke-linejoin="round"
    aria-hidden="true"
    focusable="false"
  >
    <template v-if="provider === 'anthropic'">
      <circle cx="12" cy="12" r="3" />
      <path d="M12 2v4m0 12v4M2 12h4m12 0h4M5 5l3 3m8 8 3 3M5 19l3-3m8-8 3-3" />
    </template>
    <path v-else-if="provider === 'kimi'" d="M5 3v18m14-18L5 14m6-5 8 12" />
    <path v-else d="m7 6-5 6 5 6m10-12 5 6-5 6m-3-15-4 18" />
  </svg>
</template>

<script setup lang="ts">
import type { SubscriptionProvider } from "../../services/api";
defineProps<{ provider: SubscriptionProvider; size: number }>();
</script>

<style scoped>
.subscription-icon {
  flex-shrink: 0;
}
</style>
