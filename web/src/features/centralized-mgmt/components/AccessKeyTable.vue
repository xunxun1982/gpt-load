<script setup lang="ts">
/**
 * AccessKeyTable Component
 * Displays Hub access keys with CRUD operations.
 * Compact layout without card wrapper for tab integration.
 */
import { copyWithFallback, createManualCopyContent } from "@/utils/clipboard";
import {
  AddCircleOutline,
  CheckmarkCircleOutline,
  CloseCircleOutline,
  CopyOutline,
  TrashOutline,
} from "@vicons/ionicons5";
import {
  NButton,
  NDataTable,
  NEmpty,
  NIcon,
  NPopover,
  NSpace,
  NSpin,
  NSwitch,
  NTag,
  NText,
  useDialog,
  useMessage,
  type DataTableColumns,
  type DataTableRowKey,
} from "naive-ui";
import { computed, h, onMounted, ref } from "vue";
import { useI18n } from "vue-i18n";
import { hubApi } from "../api/hub";
import type { HubAccessKey } from "../types/hub";

const { t } = useI18n();
const message = useMessage();
const dialog = useDialog();

// Emits
const emit = defineEmits<{
  (e: "create"): void;
  (e: "edit", key: HubAccessKey): void;
}>();

// State
const loading = ref(false);
const accessKeys = ref<HubAccessKey[]>([]);
const togglingIds = ref<Set<number>>(new Set());
const selectedRowKeys = ref<DataTableRowKey[]>([]);
const batchOperating = ref(false);

// Computed
const hasSelection = computed(() => selectedRowKeys.value.length > 0);

const selectedCount = computed(() => selectedRowKeys.value.length);

// Format relative time
function formatRelativeTime(dateStr: string | null): string {
  if (!dateStr) {
    return t("hub.neverUsed");
  }

  const now = new Date();
  const date = new Date(dateStr);

  // Guard against invalid timestamps
  if (Number.isNaN(date.getTime())) {
    return dateStr;
  }

  const diffMs = Math.max(0, now.getTime() - date.getTime()); // Clamp to zero for future timestamps
  const diffMinutes = Math.floor(diffMs / 60000);
  const diffHours = Math.floor(diffMs / 3600000);
  const diffDays = Math.floor(diffMs / 86400000);
  const diffMonths = Math.floor(diffDays / 30);
  const diffYears = Math.floor(diffDays / 365);

  if (diffMinutes < 1) {
    return t("hub.justNow");
  } else if (diffMinutes < 60) {
    return t("hub.minutesAgo", { n: diffMinutes });
  } else if (diffHours < 24) {
    return t("hub.hoursAgo", { n: diffHours });
  } else if (diffDays < 30) {
    return t("hub.daysAgo", { n: diffDays });
  } else if (diffMonths < 12) {
    return t("hub.monthsAgo", { n: diffMonths });
  } else {
    return t("hub.yearsAgo", { n: diffYears });
  }
}

// Mask key value for display (similar to new-api)
function maskKeyValue(key: string): string {
  if (!key || key.length <= 10) {
    return "***";
  }
  // Show first 6 chars and last 4 chars with asterisks in between
  return `${key.slice(0, 6)}**********${key.slice(-4)}`;
}

// Copy full key to clipboard with fallback
async function copyFullKey(key: HubAccessKey) {
  try {
    // Fetch plaintext key value from backend
    const result = await hubApi.getAccessKeyPlaintext(key.id);
    const plaintext = result.key_value;

    await copyWithFallback(plaintext, {
      onSuccess: () => {
        message.success(t("hub.keyCopied"));
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
  } catch (error) {
    console.error("Failed to get plaintext key:", error);
    message.error(t("keys.copyFailed"));
  }
}

// Copy full key name for reference
async function copyKeyName(key: HubAccessKey) {
  await copyWithFallback(key.name, {
    onSuccess: () => {
      message.success(t("hub.keyNameCopied"));
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

// Table columns
const columns = computed<DataTableColumns<HubAccessKey>>(() => [
  {
    type: "selection",
    width: 40,
  },
  {
    title: t("hub.accessKeyName"),
    key: "name",
    width: 150,
    ellipsis: { tooltip: true },
    render: row =>
      h(NSpace, { align: "center", size: 4 }, () => [
        h(NText, { depth: 1 }, () => row.name),
        h(
          NButton,
          {
            size: "tiny",
            quaternary: true,
            circle: true,
            onClick: () => copyKeyName(row),
          },
          {
            icon: () => h(NIcon, { component: CopyOutline, size: 14 }),
          }
        ),
      ]),
  },
  {
    title: t("hub.maskedKey"),
    key: "masked_key",
    width: 240,
    render: row => {
      // Always display masked key format for security
      const displayKey = maskKeyValue(row.masked_key);

      return h(NSpace, { align: "center", size: 4, wrap: false }, () => [
        h("code", { class: "key-display" }, displayKey),
        h(
          NButton,
          {
            size: "tiny",
            quaternary: true,
            circle: true,
            onClick: (e: Event) => {
              e.stopPropagation();
              copyFullKey(row);
            },
          },
          {
            icon: () => h(NIcon, { component: CopyOutline, size: 14 }),
          }
        ),
      ]);
    },
  },
  {
    title: t("hub.allowedModels"),
    key: "allowed_models",
    width: 120,
    render: row => {
      if (row.allowed_models_mode === "all") {
        return h(NTag, { size: "small", type: "success", bordered: false }, () =>
          t("hub.allModels")
        );
      }
      // Guard against null/undefined allowed_models
      const count = row.allowed_models?.length ?? 0;
      return h(NTag, { size: "small", type: "info", bordered: false }, () =>
        t("hub.specificModels", { count })
      );
    },
  },
  {
    title: t("hub.usageCount"),
    key: "usage_count",
    width: 90,
    align: "center",
    render: row => h(NText, { depth: 2 }, () => row.usage_count?.toString() || "0"),
  },
  {
    title: t("hub.lastUsedAt"),
    key: "last_used_at",
    width: 120,
    render: row => {
      const relativeTime = formatRelativeTime(row.last_used_at);
      if (!row.last_used_at) {
        return h(NText, { depth: 3, style: { fontSize: "12px" } }, () => relativeTime);
      }
      // Show relative time with tooltip showing exact time
      return h(
        NPopover,
        { trigger: "hover" },
        {
          trigger: () =>
            h(NText, { depth: 2, style: { fontSize: "12px", cursor: "help" } }, () => relativeTime),
          default: () => new Date(row.last_used_at || "").toLocaleString(),
        }
      );
    },
  },
  {
    title: t("common.status"),
    key: "enabled",
    width: 70,
    align: "center",
    render: row =>
      h(NSwitch, {
        value: row.enabled,
        size: "small",
        loading: togglingIds.value.has(row.id),
        onUpdateValue: (value: boolean) => handleToggle(row, value),
      }),
  },
  {
    title: t("common.actions"),
    key: "actions",
    width: 100,
    align: "center",
    render: row =>
      h(NSpace, { justify: "center", size: 6 }, () => [
        h(
          NButton,
          {
            size: "small",
            secondary: true,
            type: "primary",
            onClick: () => emit("edit", row),
          },
          () => t("common.edit")
        ),
        h(
          NButton,
          {
            size: "small",
            quaternary: true,
            type: "error",
            onClick: () => handleDelete(row),
          },
          {
            icon: () => h(NIcon, { component: TrashOutline, size: 16 }),
          }
        ),
      ]),
  },
]);

// Load access keys
async function loadAccessKeys() {
  loading.value = true;
  try {
    const response = await hubApi.listAccessKeys();
    accessKeys.value = response.access_keys || [];
  } catch (error) {
    console.error("Failed to load access keys:", error);
    accessKeys.value = [];
    // Error already handled by interceptor, no additional message needed
  } finally {
    loading.value = false;
  }
}

// Toggle enabled status
async function handleToggle(key: HubAccessKey, enabled: boolean) {
  togglingIds.value.add(key.id);
  try {
    await hubApi.toggleAccessKey(key.id, enabled);
    key.enabled = enabled;
    message.success(t("hub.accessKeyToggled"));
  } catch (error) {
    console.error("Failed to toggle access key:", error);
    // Error already handled by interceptor, no additional message needed
  } finally {
    togglingIds.value.delete(key.id);
  }
}

// Delete access key
function handleDelete(key: HubAccessKey) {
  dialog.warning({
    title: t("hub.deleteAccessKey"),
    content: t("hub.confirmDeleteAccessKey", { name: key.name }),
    positiveText: t("common.confirm"),
    negativeText: t("common.cancel"),
    onPositiveClick: async () => {
      try {
        await hubApi.deleteAccessKey(key.id);
        message.success(t("hub.accessKeyDeleted"));
        await loadAccessKeys();
      } catch (error) {
        console.error("Failed to delete access key:", error);
        // Error already handled by interceptor, no additional message needed
      }
    },
  });
}

// Batch delete
function handleBatchDelete() {
  if (!hasSelection.value) {
    message.warning(t("hub.selectAtLeastOne"));
    return;
  }

  dialog.warning({
    title: t("hub.batchDelete"),
    content: t("hub.confirmBatchDelete", { count: selectedCount.value }),
    positiveText: t("common.confirm"),
    negativeText: t("common.cancel"),
    onPositiveClick: async () => {
      batchOperating.value = true;
      try {
        const ids = selectedRowKeys.value.map(key => Number(key));
        const result = await hubApi.batchDeleteAccessKeys(ids);
        message.success(t("hub.batchDeleteSuccess", { count: result.deleted_count || 0 }));
        selectedRowKeys.value = [];
        await loadAccessKeys();
      } catch (error) {
        console.error("Failed to batch delete access keys:", error);
        // Error already handled by interceptor
      } finally {
        batchOperating.value = false;
      }
    },
  });
}

// Batch enable
async function handleBatchEnable() {
  if (!hasSelection.value) {
    message.warning(t("hub.selectAtLeastOne"));
    return;
  }

  batchOperating.value = true;
  try {
    const ids = selectedRowKeys.value.map(key => Number(key));
    const result = await hubApi.batchUpdateAccessKeysEnabled(ids, true);
    message.success(t("hub.batchEnableSuccess", { count: result.updated_count || 0 }));
    selectedRowKeys.value = [];
    await loadAccessKeys();
  } catch (error) {
    console.error("Failed to batch enable access keys:", error);
    // Error already handled by interceptor
  } finally {
    batchOperating.value = false;
  }
}

// Batch disable
async function handleBatchDisable() {
  if (!hasSelection.value) {
    message.warning(t("hub.selectAtLeastOne"));
    return;
  }

  batchOperating.value = true;
  try {
    const ids = selectedRowKeys.value.map(key => Number(key));
    const result = await hubApi.batchUpdateAccessKeysEnabled(ids, false);
    message.success(t("hub.batchDisableSuccess", { count: result.updated_count || 0 }));
    selectedRowKeys.value = [];
    await loadAccessKeys();
  } catch (error) {
    console.error("Failed to batch disable access keys:", error);
    // Error already handled by interceptor
  } finally {
    batchOperating.value = false;
  }
}

// Handle row selection
function handleCheck(rowKeys: DataTableRowKey[]) {
  selectedRowKeys.value = rowKeys;
}

// Expose refresh method
defineExpose({
  refresh: loadAccessKeys,
});

onMounted(() => {
  loadAccessKeys();
});
</script>

<template>
  <div class="access-key-container">
    <div class="access-key-header">
      <n-space align="center" :size="12">
        <n-text depth="3" class="key-count">
          {{ t("hub.totalAccessKeys", { total: accessKeys.length }) }}
        </n-text>
        <n-text v-if="hasSelection" depth="2" class="selected-count">
          {{ t("hub.selectedKeys", { count: selectedCount }) }}
        </n-text>
      </n-space>
      <n-space :size="8">
        <n-button
          v-if="hasSelection"
          size="small"
          type="success"
          :loading="batchOperating"
          @click="handleBatchEnable"
        >
          <template #icon>
            <n-icon :component="CheckmarkCircleOutline" />
          </template>
          {{ t("hub.batchEnable") }}
        </n-button>
        <n-button
          v-if="hasSelection"
          size="small"
          type="warning"
          :loading="batchOperating"
          @click="handleBatchDisable"
        >
          <template #icon>
            <n-icon :component="CloseCircleOutline" />
          </template>
          {{ t("hub.batchDisable") }}
        </n-button>
        <n-button
          v-if="hasSelection"
          size="small"
          type="error"
          :loading="batchOperating"
          @click="handleBatchDelete"
        >
          <template #icon>
            <n-icon :component="TrashOutline" />
          </template>
          {{ t("hub.batchDelete") }}
        </n-button>
        <n-button size="small" type="primary" @click="emit('create')">
          <template #icon>
            <n-icon :component="AddCircleOutline" />
          </template>
          {{ t("hub.createAccessKey") }}
        </n-button>
      </n-space>
    </div>

    <n-spin :show="loading" class="table-spin">
      <n-data-table
        v-if="accessKeys.length > 0"
        :columns="columns"
        :data="accessKeys"
        :row-key="(row: HubAccessKey) => row.id"
        :checked-row-keys="selectedRowKeys"
        :bordered="false"
        :single-line="false"
        size="small"
        class="key-table"
        @update:checked-row-keys="handleCheck"
      />
      <n-empty v-else :description="t('hub.noAccessKeys')" class="empty-state" />
    </n-spin>
  </div>
</template>

<style scoped>
.access-key-container {
  display: flex;
  flex-direction: column;
}

.access-key-header {
  display: flex;
  align-items: center;
  justify-content: space-between;
  margin: 2px 0;
  padding: 6px 8px;
  background: var(--n-color-embedded);
  border-radius: 4px;
}

.key-count {
  font-size: 12px;
}

.selected-count {
  font-size: 12px;
  font-weight: 500;
  color: var(--n-color-target);
}

.table-spin {
  margin-top: 4px;
}

.empty-state {
  padding: 40px 0;
}

.masked-key {
  font-size: 12px;
  background: transparent;
  padding: 0;
  color: var(--n-text-color-3);
}

.key-display {
  font-size: 12px;
  background: transparent;
  padding: 0;
  color: var(--n-text-color-3);
  font-family: "SF Mono", "Monaco", "Inconsolata", "Fira Mono", monospace;
  user-select: all;
}
</style>
