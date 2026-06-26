<script setup lang="ts">
import { NTag } from "naive-ui";
import { computed } from "vue";
import { useI18n } from "vue-i18n";

interface Props {
  channelType?: string;
  codexSupport?: boolean;
  size?: "small" | "medium" | "large";
}

const props = withDefaults(defineProps<Props>(), {
  size: "small",
});

const { t } = useI18n();

const showBadge = computed(() => {
  const { channelType, codexSupport } = props;
  return (channelType === "openai" || channelType === "anthropic") && codexSupport === true;
});

type BadgeType = "default" | "warning" | "info" | "error" | "success" | "primary";

const badgeConfig = computed((): { text: string; type: BadgeType } => {
  const configs: Record<string, { text: string; type: BadgeType }> = {
    openai: { text: t("keys.openaiCodexBadge"), type: "primary" },
    anthropic: { text: t("keys.anthropicCodexBadge"), type: "warning" },
  };
  return configs[props.channelType ?? ""] ?? { text: t("keys.codexSupportBadge"), type: "primary" };
});
</script>

<template>
  <n-tag
    v-if="showBadge"
    :type="badgeConfig.type"
    :size="size"
    :bordered="false"
    round
    class="codex-badge"
  >
    {{ badgeConfig.text }}
  </n-tag>
</template>

<style scoped>
.codex-badge {
  flex-shrink: 0;
  font-size: 11px;
  font-weight: 600;
  padding: 2px 8px;
}
</style>
