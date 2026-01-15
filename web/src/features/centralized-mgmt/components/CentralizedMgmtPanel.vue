<script setup lang="ts">
/**
 * CentralizedMgmtPanel Component
 * Main panel with tab-based navigation for Model Pool and Access Keys.
 * Uses maximized layout for better space utilization.
 */
import { KeyOutline, LayersOutline, RefreshOutline, SettingsOutline } from "@vicons/ionicons5";
import {
  NButton,
  NIcon,
  NInputNumber,
  NModal,
  NSpace,
  NSpin,
  NTabPane,
  NTabs,
  NText,
  useMessage,
} from "naive-ui";
import { h, ref } from "vue";
import { useI18n } from "vue-i18n";
import { hubApi } from "../api/hub";
import type { HubAccessKey, HubSettings } from "../types/hub";
import AccessKeyModal from "./AccessKeyModal.vue";
import AccessKeyTable from "./AccessKeyTable.vue";
import EndpointDisplay from "./EndpointDisplay.vue";
import ModelPoolTable from "./ModelPoolTable.vue";

const { t } = useI18n();
const message = useMessage();

// Tab state
const activeTab = ref("model-pool");

// Refs to child components
const modelPoolTableRef = ref<InstanceType<typeof ModelPoolTable> | null>(null);
const accessKeyTableRef = ref<InstanceType<typeof AccessKeyTable> | null>(null);

// Modal state
const showAccessKeyModal = ref(false);
const editingAccessKey = ref<HubAccessKey | null>(null);
const refreshing = ref(false);

// Settings modal
const showSettingsModal = ref(false);
const settingsLoading = ref(false);
const hubSettings = ref<HubSettings>({
  max_retries: 3,
  retry_delay: 100,
  health_threshold: 0.5,
  enable_priority: true,
});

// Tab render functions
const renderModelPoolTab = () =>
  h(NSpace, { align: "center", size: 6 }, () => [
    h(NIcon, { component: LayersOutline, size: 16 }),
    t("hub.modelPool"),
  ]);

const renderAccessKeysTab = () =>
  h(NSpace, { align: "center", size: 6 }, () => [
    h(NIcon, { component: KeyOutline, size: 16 }),
    t("hub.accessKeys"),
  ]);

function handleCreateAccessKey() {
  editingAccessKey.value = null;
  showAccessKeyModal.value = true;
}

function handleEditAccessKey(key: HubAccessKey) {
  editingAccessKey.value = key;
  showAccessKeyModal.value = true;
}

function handleAccessKeySuccess() {
  accessKeyTableRef.value?.refresh();
}

async function handleRefresh() {
  refreshing.value = true;
  try {
    if (activeTab.value === "model-pool") {
      await modelPoolTableRef.value?.refresh();
    } else {
      await accessKeyTableRef.value?.refresh();
    }
    message.success(t("common.operationSuccess"));
  } catch (error) {
    console.error("Failed to refresh:", error);
    // Error already handled by interceptor, no additional message needed
  } finally {
    refreshing.value = false;
  }
}

async function loadSettings() {
  settingsLoading.value = true;
  try {
    hubSettings.value = await hubApi.getSettings();
  } catch (e) {
    console.error("Failed to load settings:", e);
    message.error(t("common.loadFailed"));
  } finally {
    settingsLoading.value = false;
  }
}

async function saveSettings() {
  settingsLoading.value = true;
  try {
    await hubApi.updateSettings(hubSettings.value);
    showSettingsModal.value = false;
    message.success(t("common.operationSuccess"));
  } catch (e) {
    console.error("Failed to save settings:", e);
    message.error(t("common.saveFailed"));
  } finally {
    settingsLoading.value = false;
  }
}

async function openSettings() {
  await loadSettings();
  showSettingsModal.value = true;
}
</script>

<template>
  <div class="centralized-mgmt-panel">
    <!-- Compact header with endpoint -->
    <div class="panel-header">
      <div class="header-left">
        <h2 class="panel-title">{{ t("hub.centralizedManagement") }}</h2>
        <endpoint-display />
      </div>
      <n-space align="center" :size="8">
        <n-button size="tiny" quaternary @click="openSettings">
          <template #icon>
            <n-icon :component="SettingsOutline" size="16" />
          </template>
        </n-button>
        <n-button size="tiny" :loading="refreshing" @click="handleRefresh">
          <template #icon>
            <n-icon :component="RefreshOutline" size="14" />
          </template>
        </n-button>
      </n-space>
    </div>

    <!-- Tab navigation -->
    <n-tabs v-model:value="activeTab" type="line" animated class="main-tabs">
      <n-tab-pane name="model-pool" :tab="renderModelPoolTab">
        <model-pool-table ref="modelPoolTableRef" :compact="true" />
      </n-tab-pane>
      <n-tab-pane name="access-keys" :tab="renderAccessKeysTab">
        <access-key-table
          ref="accessKeyTableRef"
          @create="handleCreateAccessKey"
          @edit="handleEditAccessKey"
        />
      </n-tab-pane>
    </n-tabs>

    <!-- Access Key Modal -->
    <access-key-modal
      v-model:show="showAccessKeyModal"
      :edit-key="editingAccessKey"
      @success="handleAccessKeySuccess"
    />

    <!-- Settings Modal -->
    <n-modal
      v-model:show="showSettingsModal"
      preset="card"
      :title="t('hub.hubSettings')"
      style="width: 400px"
      :bordered="false"
    >
      <n-spin :show="settingsLoading">
        <n-space vertical :size="16">
          <div class="setting-row">
            <span class="setting-label">{{ t("hub.maxRetries") }}</span>
            <n-input-number
              v-model:value="hubSettings.max_retries"
              :min="0"
              :max="10"
              size="small"
              style="width: 120px"
            />
          </div>
          <n-text depth="3" style="font-size: 12px; margin-top: -8px">
            {{ t("hub.maxRetriesHint") }}
          </n-text>

          <div class="setting-row">
            <span class="setting-label">{{ t("hub.retryDelay") }}</span>
            <n-input-number
              v-model:value="hubSettings.retry_delay"
              :min="0"
              :max="5000"
              :step="100"
              size="small"
              style="width: 120px"
            />
            <span class="setting-unit">ms</span>
          </div>

          <div class="setting-row">
            <span class="setting-label">{{ t("hub.healthThreshold") }}</span>
            <n-input-number
              v-model:value="hubSettings.health_threshold"
              :min="0"
              :max="1"
              :step="0.1"
              size="small"
              style="width: 120px"
            />
          </div>
          <n-text depth="3" style="font-size: 12px; margin-top: -8px">
            {{ t("hub.healthThresholdHint") }}
          </n-text>

          <div class="setting-row">
            <span class="setting-label">{{ t("hub.enablePriority") }}</span>
            <n-switch v-model:value="hubSettings.enable_priority" />
          </div>
        </n-space>
      </n-spin>
      <template #footer>
        <n-space justify="end">
          <n-button @click="showSettingsModal = false">{{ t("common.cancel") }}</n-button>
          <n-button type="primary" :loading="settingsLoading" @click="saveSettings">
            {{ t("common.save") }}
          </n-button>
        </n-space>
      </template>
    </n-modal>
  </div>
</template>

<style scoped>
.centralized-mgmt-panel {
  display: flex;
  flex-direction: column;
  height: 100%;
  padding: 4px 8px;
}

.panel-header {
  display: flex;
  justify-content: space-between;
  align-items: center;
  margin-bottom: 2px;
  flex-shrink: 0;
}

.header-left {
  display: flex;
  align-items: center;
  gap: 12px;
  flex: 1;
  min-width: 0;
}

.panel-title {
  font-size: 15px;
  font-weight: 600;
  margin: 0;
  color: var(--n-text-color);
  white-space: nowrap;
}

.main-tabs {
  flex: 1;
  display: flex;
  flex-direction: column;
  min-height: 0;
}

.main-tabs :deep(.n-tabs-nav) {
  flex-shrink: 0;
  margin-bottom: 2px;
}

.main-tabs :deep(.n-tabs-pane-wrapper) {
  flex: 1;
  min-height: 0;
  overflow: hidden;
}

.main-tabs :deep(.n-tab-pane) {
  height: 100%;
  padding-top: 0;
}

.setting-row {
  display: flex;
  align-items: center;
  gap: 12px;
}

.setting-label {
  min-width: 120px;
}

.setting-unit {
  color: var(--n-text-color-3);
  font-size: 12px;
}
</style>
