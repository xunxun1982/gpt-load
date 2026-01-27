<script setup lang="ts">
import { NTag } from "naive-ui";
import { computed } from "vue";
import { useI18n } from "vue-i18n";

interface Props {
  channelType?: string;
  ccSupport?: boolean;
  size?: "small" | "medium" | "large";
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

// Badge configuration based on channel type
type BadgeType = "default" | "warning" | "info" | "error" | "success" | "primary";

const badgeConfig = computed(() => {
  const configs: Record<string, { text: string; type: BadgeType }> = {
    openai: { text: t("keys.openaiCCBadge"), type: "warning" }, // Orange/Yellow
    codex: { text: t("keys.codexCCBadge"), type: "info" }, // Blue
    gemini: { text: t("keys.geminiCCBadge"), type: "success" }, // Green
  };
  return configs[props.channelType ?? ""] ?? { text: t("keys.ccSupportBadge"), type: "warning" };
});

const badgeText = computed(() => badgeConfig.value.text);
const badgeType = computed(() => badgeConfig.value.type);
</script>

<template>
  <n-tag v-if="showBadge" :type="badgeType" :size="size" :bordered="false" round class="cc-badge">
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
