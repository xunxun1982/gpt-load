<script setup lang="ts">
/**
 * CustomModelsTable Component
 * Manages custom model names for aggregate groups.
 * Only aggregate groups can have custom models.
 */
import { CreateOutline, RefreshOutline, SaveOutline } from "@vicons/ionicons5";
import {
  NButton,
  NDataTable,
  NDynamicInput,
  NEmpty,
  NIcon,
  NInput,
  NModal,
  NSpace,
  NSpin,
  NTag,
  NText,
  useMessage,
  type DataTableColumns,
} from "naive-ui";
import { computed, h, onMounted, ref } from "vue";
import { useI18n } from "vue-i18n";
import { hubApi } from "../api/hub";
import type { AggregateGroupCustomModels } from "../types/hub";

const { t } = useI18n();
const message = useMessage();

const loading = ref(false);
const groups = ref<AggregateGroupCustomModels[]>([]);

// Edit modal state
const showEditModal = ref(false);
const editingGroup = ref<AggregateGroupCustomModels | null>(null);
const editingModels = ref<string[]>([]);
const saving = ref(false);

const columns = computed<DataTableColumns<AggregateGroupCustomModels>>(() => [
  {
    title: t("hub.aggregateGroupName"),
    key: "group_name",
    width: 200,
    ellipsis: { tooltip: true },
    titleAlign: "center",
    render: row =>
      h(NSpace, { size: 4, align: "center" }, () => [
        h(NTag, { size: "tiny", type: "warning", bordered: false }, () => t("hub.aggregateGroup")),
        h("span", null, row.group_name),
      ]),
  },
  {
    title: t("hub.customModelNames"),
    key: "custom_model_names",
    minWidth: 300,
    titleAlign: "center",
    render: row => {
      if (!row.custom_model_names || row.custom_model_names.length === 0) {
        return h(NText, { depth: 3 }, () => t("hub.noCustomModels"));
      }
      return h(NSpace, { size: 4, wrap: true }, () =>
        row.custom_model_names.map(model =>
          h(NTag, { size: "tiny", type: "info", bordered: false }, () => model)
        )
      );
    },
  },
  {
    title: t("hub.modelCount"),
    key: "model_count",
    width: 80,
    align: "center",
    titleAlign: "center",
    render: row =>
      h(NText, null, () => t("hub.modelCount", { count: row.custom_model_names?.length || 0 })),
  },
  {
    title: "",
    key: "actions",
    width: 60,
    align: "center",
    render: row =>
      h(
        NButton,
        { size: "tiny", quaternary: true, type: "primary", onClick: () => openEditModal(row) },
        { icon: () => h(NIcon, { component: CreateOutline, size: 14 }) }
      ),
  },
]);

function openEditModal(group: AggregateGroupCustomModels) {
  editingGroup.value = group;
  editingModels.value = [...(group.custom_model_names || [])];
  showEditModal.value = true;
}

async function saveCustomModels() {
  if (!editingGroup.value) {
    return;
  }

  saving.value = true;
  try {
    await hubApi.updateAggregateGroupCustomModels({
      group_id: editingGroup.value.group_id,
      custom_model_names: editingModels.value.filter(m => m.trim() !== ""),
    });

    // Update local data
    if (editingGroup.value) {
      const group = groups.value.find(g => g.group_id === editingGroup.value?.group_id);
      if (group) {
        group.custom_model_names = editingModels.value.filter(m => m.trim() !== "");
      }
    }

    showEditModal.value = false;
    message.success(t("hub.customModelsUpdated"));
  } catch (_) {
    // Error handled by interceptor
  } finally {
    saving.value = false;
  }
}

async function loadCustomModels() {
  loading.value = true;
  try {
    groups.value = await hubApi.getAggregateGroupsCustomModels();
  } catch (_) {
    groups.value = [];
  } finally {
    loading.value = false;
  }
}

defineExpose({ refresh: loadCustomModels });

onMounted(() => {
  loadCustomModels();
});
</script>

<template>
  <div class="custom-models-container">
    <!-- Header -->
    <div class="header-row">
      <n-text depth="3" style="font-size: 12px">
        {{ t("hub.totalModels", { total: groups.length }) }}
      </n-text>
      <n-button size="tiny" :loading="loading" @click="loadCustomModels">
        <template #icon>
          <n-icon :component="RefreshOutline" size="14" />
        </template>
      </n-button>
    </div>

    <!-- Table -->
    <div class="table-wrapper">
      <n-spin :show="loading">
        <n-data-table
          v-if="groups.length || loading"
          :columns="columns"
          :data="groups"
          :bordered="false"
          :single-line="false"
          size="small"
          :max-height="'calc(100vh - 240px)'"
        />
        <n-empty v-else :description="t('hub.noCustomModels')" />
      </n-spin>
    </div>

    <!-- Edit Modal -->
    <n-modal
      v-model:show="showEditModal"
      preset="card"
      :title="t('hub.editCustomModels')"
      style="width: 560px; max-width: 95vw"
      :bordered="false"
      size="small"
    >
      <div v-if="editingGroup" class="edit-content">
        <div class="edit-header">
          <n-space align="center" :size="6">
            <n-tag size="small" type="warning" :bordered="false">
              {{ t("hub.aggregateGroup") }}
            </n-tag>
            <n-text strong>{{ editingGroup.group_name }}</n-text>
          </n-space>
          <n-text depth="3" style="font-size: 12px">
            {{ t("hub.customModelNamesHint") }}
          </n-text>
        </div>

        <n-dynamic-input
          v-model:value="editingModels"
          :min="0"
          :max="100"
          :create-button-props="{ size: 'small' }"
          :on-create="() => ''"
          class="custom-model-input"
        >
          <template #default="{ index }">
            <n-input
              v-model:value="editingModels[index]"
              :placeholder="t('hub.modelName')"
              size="small"
              clearable
            />
          </template>
          <template #create-button-default>
            <n-space align="center" :size="4">
              <n-icon :component="CreateOutline" size="14" />
              <span>{{ t("hub.addCustomModel") }}</span>
            </n-space>
          </template>
        </n-dynamic-input>
      </div>

      <template #footer>
        <n-space justify="end" :size="8">
          <n-button size="small" @click="showEditModal = false">{{ t("common.cancel") }}</n-button>
          <n-button size="small" type="primary" :loading="saving" @click="saveCustomModels">
            <template #icon>
              <n-icon :component="SaveOutline" size="14" />
            </template>
            {{ t("common.save") }}
          </n-button>
        </n-space>
      </template>
    </n-modal>
  </div>
</template>

<style scoped>
.custom-models-container {
  display: flex;
  flex-direction: column;
  height: 100%;
  min-height: 0;
}

.header-row {
  display: flex;
  align-items: center;
  justify-content: space-between;
  margin: 2px 0;
  padding: 3px 6px;
  background: var(--n-color-embedded);
  border-radius: 4px;
  font-size: 12px;
  flex-shrink: 0;
}

.table-wrapper {
  flex: 1;
  min-height: 0;
  overflow: hidden;
}

.edit-content {
  max-height: 55vh;
  overflow-y: auto;
}

.edit-header {
  display: flex;
  flex-direction: column;
  gap: 8px;
  margin-bottom: 16px;
  padding-bottom: 12px;
  border-bottom: 1px solid var(--n-border-color);
}

/* Fix vertical alignment for dynamic input */
.custom-model-input :deep(.n-dynamic-input-item) {
  display: flex;
  align-items: center;
}

.custom-model-input :deep(.n-input) {
  display: flex;
  align-items: center;
}

.custom-model-input :deep(.n-input__input-el) {
  line-height: normal;
}
</style>
