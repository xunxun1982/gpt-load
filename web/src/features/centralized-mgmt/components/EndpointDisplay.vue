<script setup lang="ts">
/**
 * EndpointDisplay Component
 * Displays Hub endpoint paths with copy functionality.
 * Supports collapsed mode for compact header display.
 */
import { copy } from "@/utils/clipboard";
import { ChevronDownOutline, CopyOutline } from "@vicons/ionicons5";
import { NButton, NIcon, NPopover, NSpace, NText, NTooltip, useMessage } from "naive-ui";
import { computed } from "vue";
import { useI18n } from "vue-i18n";

const { t } = useI18n();
const message = useMessage();

// Get base URL for copy only (not displayed)
const baseUrl = computed(() => {
  const { protocol, host } = window.location;
  return `${protocol}//${host}`;
});

// Hub endpoint paths - compact format
const endpoints = computed(() => [
  { key: "chat", label: "Chat", path: "/hub/v1/chat/completions" },
  { key: "models", label: "Models", path: "/hub/v1/models" },
  { key: "claude", label: "Claude", path: "/hub/v1/messages" },
  { key: "codex", label: "Codex", path: "/hub/v1/responses" },
]);

async function copyEndpoint(path: string) {
  const fullUrl = `${baseUrl.value}${path}`;
  const success = await copy(fullUrl);
  if (success) {
    message.success(t("hub.endpointCopied"));
  } else {
    message.error(t("keys.copyFailed"));
  }
}

async function copyBaseUrl() {
  const hubBaseUrl = `${baseUrl.value}/hub/v1`;
  const success = await copy(hubBaseUrl);
  if (success) {
    message.success(t("hub.baseUrlCopied"));
  } else {
    message.error(t("keys.copyFailed"));
  }
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
          <n-text strong>{{ t("hub.unifiedEndpoint") }}</n-text>
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
        <n-space :size="6" wrap>
          <div
            v-for="endpoint in endpoints"
            :key="endpoint.key"
            class="endpoint-chip"
            @click="copyEndpoint(endpoint.path)"
          >
            <span class="endpoint-label">{{ endpoint.label }}</span>
            <code class="endpoint-path">{{ endpoint.path }}</code>
            <n-icon :component="CopyOutline" :size="12" class="copy-icon" />
          </div>
        </n-space>
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
  min-width: 320px;
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

.endpoint-chip {
  display: inline-flex;
  align-items: center;
  gap: 6px;
  padding: 4px 8px;
  background: var(--n-color-embedded);
  border-radius: 4px;
  border: 1px solid var(--n-border-color);
  cursor: pointer;
  transition: all 0.2s;
  font-size: 12px;
}

.endpoint-chip:hover {
  border-color: var(--n-primary-color);
  background: var(--n-color-hover);
}

.endpoint-chip:hover .copy-icon {
  opacity: 1;
}

.endpoint-label {
  font-weight: 500;
  color: var(--n-text-color);
}

.endpoint-path {
  color: var(--n-text-color-3);
  background: transparent;
  padding: 0;
  font-size: 12px;
}

.copy-icon {
  opacity: 0.4;
  transition: opacity 0.2s;
}
</style>
