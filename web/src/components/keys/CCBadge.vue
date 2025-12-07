<script setup lang="ts">
import { NTag } from "naive-ui";
import { computed } from "vue";
import { useI18n } from "vue-i18n";

interface Props {
  channelType?: string;
  ccSupport?: boolean;
  size?: "small" | "medium" | "large";
  name?: string;
  displayName?: string;
}

const props = withDefaults(defineProps<Props>(), {
  size: "small",
});

const { t } = useI18n();

// For accuracy, use config as the single source of truth:
// show badge ONLY when channelType is OpenAI and ccSupport is explicitly true.
// We do NOT infer from name/displayName to avoid false positives.
const showBadge = computed(() => {
  const { channelType, ccSupport } = props;
  return channelType === "openai" && ccSupport === true;
});
</script>

<template>
  <n-tag v-if="showBadge" type="warning" :size="size" :bordered="false" round class="cc-badge">
    {{ t("keys.ccSupportBadge") }}
  </n-tag>
</template>

<style scoped>
.cc-badge {
  flex-shrink: 0;
  font-size: 11px;
  font-weight: 600;
  padding: 2px 8px;
  animation: subtle-pulse 2s ease-in-out infinite;
}

@keyframes subtle-pulse {
  0%,
  100% {
    transform: scale(1);
    opacity: 1;
  }
  50% {
    transform: scale(1.02);
    opacity: 0.95;
  }
}
</style>
