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

// Show badge when channelType is OpenAI, Codex, or Gemini and ccSupport is explicitly true
const showBadge = computed(() => {
  const { channelType, ccSupport } = props;
  return (
    (channelType === "openai" || channelType === "codex" || channelType === "gemini") &&
    ccSupport === true
  );
});

// Get badge text based on channel type
const badgeText = computed(() => {
  switch (props.channelType) {
    case "openai":
      return t("keys.openaiCCBadge");
    case "codex":
      return t("keys.codexCCBadge");
    case "gemini":
      return t("keys.geminiCCBadge");
    default:
      return t("keys.ccSupportBadge");
  }
});

// Get badge type (color) based on channel type
const badgeType = computed(() => {
  switch (props.channelType) {
    case "openai":
      return "warning"; // Orange/Yellow
    case "codex":
      return "info"; // Blue
    case "gemini":
      return "success"; // Green
    default:
      return "warning";
  }
});
</script>

<template>
  <n-tag
    v-if="showBadge"
    :type="badgeType"
    :size="size"
    :bordered="false"
    round
    class="cc-badge"
  >
    {{ badgeText }}
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
