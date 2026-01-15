<script setup lang="ts">
/**
 * AccessKeyTable Component
 * Displays Hub access keys with CRUD operations.
 * Compact layout without card wrapper for tab integration.
 */
import { AddCircleOutline, TrashOutline } from "@vicons/ionicons5";
import {
  NButton,
  NDataTable,
  NEmpty,
  NIcon,
  NSpace,
  NSpin,
  NSwitch,
  NTag,
  NText,
  useDialog,
  useMessage,
  type DataTableColumns,
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

// Table columns
const columns = computed<DataTableColumns<HubAccessKey>>(() => [
  {
    title: t("hub.accessKeyName"),
    key: "name",
    width: 180,
    ellipsis: { tooltip: true },
  },
  {
    title: t("hub.maskedKey"),
    key: "masked_key",
    width: 220,
    render: row => h("code", { class: "masked-key" }, row.masked_key || "-"),
  },
  {
    title: t("hub.allowedModels"),
    key: "allowed_models",
    width: 150,
    render: row => {
      if (row.allowed_models_mode === "all") {
        return h(NTag, { size: "small", type: "success", bordered: false }, () =>
          t("hub.allModels")
        );
      }
      return h(NTag, { size: "small", type: "info", bordered: false }, () =>
        t("hub.specificModels", { count: row.allowed_models.length })
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
      <n-text depth="3" class="key-count">
        {{ t("hub.totalAccessKeys", { total: accessKeys.length }) }}
      </n-text>
      <n-button size="small" type="primary" @click="emit('create')">
        <template #icon>
          <n-icon :component="AddCircleOutline" />
        </template>
        {{ t("hub.createAccessKey") }}
      </n-button>
    </div>

    <n-spin :show="loading" class="table-spin">
      <n-data-table
        v-if="accessKeys.length > 0"
        :columns="columns"
        :data="accessKeys"
        :bordered="false"
        :single-line="false"
        size="small"
        class="key-table"
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
  padding: 3px 6px;
  background: var(--n-color-embedded);
  border-radius: 4px;
}

.key-count {
  font-size: 12px;
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
</style>
