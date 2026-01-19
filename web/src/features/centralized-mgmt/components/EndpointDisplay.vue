<script setup lang="ts">
/**
 * EndpointDisplay Component
 * Displays supported channel types with base URL copy functionality.
 * Channels handle various API endpoints (chat, audio, image, video, etc.)
 * Requests are forwarded to groups/aggregates for processing.
 */
import { copyWithFallback, createManualCopyContent } from "@/utils/clipboard";
import { ChevronDownOutline, CopyOutline } from "@vicons/ionicons5";
import { NButton, NIcon, NPopover, NText, NTooltip, useDialog, useMessage } from "naive-ui";
import { computed, h } from "vue";
import { useI18n } from "vue-i18n";

const { t } = useI18n();
const message = useMessage();
const dialog = useDialog();

// Get base URL for copy only (not displayed)
const baseUrl = computed(() => {
  const { protocol, host } = window.location;
  return `${protocol}//${host}`;
});

// Supported channel types - requests are forwarded to groups/aggregates for processing
// Each channel handles its own endpoints (chat, audio, image, video, etc.)
const channels = computed(() => [
  { key: "openai", label: "OpenAI", type: "success" as const },
  { key: "anthropic", label: "Anthropic", type: "warning" as const },
  { key: "gemini", label: "Gemini", type: "default" as const },
  { key: "codex", label: "Codex", type: "info" as const },
]);

async function copyBaseUrl() {
  const hubBaseUrl = `${baseUrl.value}/hub/v1`;
  await copyWithFallback(hubBaseUrl, {
    onSuccess: () => {
      message.success(t("hub.baseUrlCopied"));
    },
    onError: () => {
      message.error(t("keys.copyFailed"));
    },
    showManualDialog: (text: string) => {
      dialog.create({
        title: t("common.copy"),
        content: () => createManualCopyContent(h, text, t),
        positiveText: t("common.close"),
      });
    },
  });
}
</script>

<template>
  <div class="endpoint-display-inline">
    <n-popover trigger="click" placement="bottom-start" :show-arrow="false">
      <template #trigger>
        <n-button size="tiny" quaternary class="endpoint-trigger">
          <n-icon :component="CopyOutline" :size="12" style="margin-right: 4px" />
          <code class="base-url">/hub/v1</code>
          <n-icon :component="ChevronDownOutline" :size="12" style="margin-left: 4px" />
        </n-button>
      </template>
      <div class="endpoint-popover">
        <div class="endpoint-popover-header">
          <n-text strong>{{ t("hub.supportedChannels") }}</n-text>
          <n-tooltip trigger="hover">
            <template #trigger>
              <n-button size="tiny" quaternary @click="copyBaseUrl">
                <template #icon>
                  <n-icon :component="CopyOutline" :size="12" />
                </template>
                {{ t("hub.copyBaseUrl") }}
              </n-button>
            </template>
            {{ t("hub.copyBaseUrl") }}
          </n-tooltip>
        </div>
        <div class="channel-hint">{{ t("hub.channelHint") }}</div>
        <div class="channel-tags">
          <n-tag v-for="channel in channels" :key="channel.key" :type="channel.type" size="small">
            {{ channel.label }}
          </n-tag>
        </div>
      </div>
    </n-popover>
  </div>
</template>

<style scoped>
.endpoint-display-inline {
  display: inline-flex;
  align-items: center;
}

.endpoint-trigger {
  display: inline-flex;
  align-items: center;
  padding: 2px 8px;
}

.base-url {
  font-size: 12px;
  color: var(--n-text-color-3);
  background: transparent;
  padding: 0;
}

.endpoint-popover {
  min-width: 280px;
  padding: 4px;
}

.endpoint-popover-header {
  display: flex;
  align-items: center;
  justify-content: space-between;
  margin-bottom: 8px;
  padding-bottom: 8px;
  border-bottom: 1px solid var(--n-border-color);
}

.channel-hint {
  font-size: 12px;
  color: var(--n-text-color-3);
  margin-bottom: 8px;
}

.channel-tags {
  display: flex;
  flex-wrap: wrap;
  gap: 6px;
}
</style>
