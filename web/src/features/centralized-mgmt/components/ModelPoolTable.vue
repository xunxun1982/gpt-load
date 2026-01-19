<script setup lang="ts">
/**
 * ModelPoolTable Component
 * Displays aggregated model pool with priority-based group management.
 * Compact single-line layout for each model.
 */
import { CreateOutline, RefreshOutline, Search } from "@vicons/ionicons5";
import {
  NButton,
  NDataTable,
  NEmpty,
  NIcon,
  NInput,
  NInputNumber,
  NModal,
  NPopover,
  NSelect,
  NSpace,
  NSpin,
  NTag,
  NText,
  useMessage,
  type DataTableColumns,
  type SelectOption,
} from "naive-ui";
import { computed, h, onMounted, ref, watch } from "vue";
import { useI18n } from "vue-i18n";
import { hubApi } from "../api/hub";
import type { ModelGroupPriority, ModelPoolEntryV2 } from "../types/hub";

interface Props {
  compact?: boolean;
}

withDefaults(defineProps<Props>(), {
  compact: false,
});

const { t } = useI18n();
const message = useMessage();

const loading = ref(false);
const models = ref<ModelPoolEntryV2[]>([]);
const searchText = ref("");
const currentPage = ref(1);
const pageSize = ref(50);

// Filter state
const filters = ref({
  channelType: "all" as string,
});

// Edit modal state
const showEditModal = ref(false);
const editingModel = ref<ModelPoolEntryV2 | null>(null);
const editingGroups = ref<ModelGroupPriority[]>([]);
const savingPriority = ref(false);

// Get unique channel types for filter
const channelTypeOptions = computed<SelectOption[]>(() => {
  const types = new Set<string>();
  models.value.forEach(model => {
    model.groups.forEach(g => {
      if (g.channel_type) {
        types.add(g.channel_type);
      }
    });
  });
  const options: SelectOption[] = [{ label: t("hub.filterAll"), value: "all" }];
  Array.from(types)
    .sort()
    .forEach(type => {
      options.push({ label: type, value: type });
    });
  return options;
});

/**
 * Get the primary channel type for a model.
 * Prioritizes enabled groups with lowest priority value, falls back to first group's channel type.
 */
function getPrimaryChannelType(model: ModelPoolEntryV2): string {
  // Try to find enabled groups with channel_type, sorted by priority (lowest first)
  const enabledGroup = model.groups
    .filter(g => g.enabled && g.channel_type)
    .sort((a, b) => a.priority - b.priority)[0];
  if (enabledGroup) {
    return enabledGroup.channel_type;
  }
  // Fall back to first group's channel_type
  return model.groups[0]?.channel_type || "";
}

const filteredModels = computed(() => {
  let result = models.value;

  // Filter by channel type
  if (filters.value.channelType !== "all") {
    result = result.filter(model =>
      model.groups.some(g => g.channel_type === filters.value.channelType)
    );
  }

  // Filter by search keyword (matches model name, group name, or channel type)
  if (searchText.value.trim()) {
    const keyword = searchText.value.toLowerCase().trim();
    result = result.filter(model => {
      // Match model name
      if (model.model_name.toLowerCase().includes(keyword)) {
        return true;
      }
      // Match group name or channel type (with null safety)
      return model.groups.some(
        g =>
          (g.group_name ?? "").toLowerCase().includes(keyword) ||
          (g.channel_type ?? "").toLowerCase().includes(keyword)
      );
    });
  }

  // Sort by channel type (alphabetically), then by model name (alphabetically)
  return result.slice().sort((a, b) => {
    const aType = getPrimaryChannelType(a);
    const bType = getPrimaryChannelType(b);
    // Primary sort: channel type
    if (aType !== bType) {
      return aType.localeCompare(bType);
    }
    // Secondary sort: model name
    return a.model_name.localeCompare(b.model_name);
  });
});

const paginatedModels = computed(() => {
  const start = (currentPage.value - 1) * pageSize.value;
  return filteredModels.value.slice(start, start + pageSize.value);
});

const totalPages = computed(() => Math.ceil(filteredModels.value.length / pageSize.value));

function getHealthClass(score: number): string {
  if (score >= 0.8) {
    return "health-good";
  }
  if (score >= 0.5) {
    return "health-warning";
  }
  return "health-critical";
}

function formatPercent(value: number): string {
  return `${(value * 100).toFixed(0)}%`;
}

function getPriorityTagType(
  priority: number
): "default" | "success" | "warning" | "error" | "info" {
  if (priority === 0) {
    return "error";
  }
  if (priority <= 50) {
    return "success";
  }
  if (priority <= 100) {
    return "info";
  }
  return "warning";
}

// Get group type label: standard, sub-group, aggregate
function getGroupTypeLabel(group: ModelGroupPriority): string {
  if (group.group_type === "aggregate") {
    return t("hub.aggregateGroup");
  }
  if (group.is_child_group) {
    return t("hub.subGroup");
  }
  return t("hub.standardGroup");
}

function getGroupTypeTagType(group: ModelGroupPriority): "default" | "warning" | "info" {
  if (group.group_type === "aggregate") {
    return "warning";
  }
  if (group.is_child_group) {
    return "info";
  }
  return "default";
}

// Get group type short label for inline display
function getGroupTypeShort(group: ModelGroupPriority): string {
  if (group.group_type === "aggregate") {
    return `[${t("hub.aggregateGroupShort")}]`;
  }
  if (group.is_child_group) {
    return `[${t("hub.subGroupShort")}]`;
  }
  return "";
}

// Popover editing state for +N tag
const popoverEditingModel = ref<string | null>(null);
const popoverEditingGroups = ref<ModelGroupPriority[]>([]);
const popoverSaving = ref(false);

// Open popover editing for groups
function openPopoverEdit(model: ModelPoolEntryV2, groups: ModelGroupPriority[]) {
  popoverEditingModel.value = model.model_name;
  // Sort by priority for editing
  popoverEditingGroups.value = groups
    .map(g => ({ ...g }))
    .sort((a, b) => {
      if (a.priority === 0 && b.priority !== 0) {
        return 1;
      }
      if (b.priority === 0 && a.priority !== 0) {
        return -1;
      }
      return a.priority - b.priority;
    });
}

// Save popover edited priorities
async function savePopoverPriorities() {
  const modelName = popoverEditingModel.value;
  if (!modelName) {
    return;
  }

  popoverSaving.value = true;
  try {
    const updates = popoverEditingGroups.value.map(g => ({
      model_name: modelName,
      group_id: g.group_id,
      priority: g.priority,
    }));
    await hubApi.batchUpdatePriorities(updates);

    // Update local data
    const model = models.value.find(m => m.model_name === modelName);
    if (model) {
      popoverEditingGroups.value.forEach(eg => {
        const group = model.groups.find(g => g.group_id === eg.group_id);
        if (group) {
          group.priority = eg.priority;
        }
      });
    }

    popoverEditingModel.value = null;
    message.success(t("common.operationSuccess"));
  } catch (_) {
    // Error handled by interceptor
  } finally {
    popoverSaving.value = false;
  }
}

// Render group tags with popover for editing priorities
function renderGroupTags(row: ModelPoolEntryV2) {
  const groups = row.groups.filter(g => g.enabled);
  if (!groups.length) {
    return h(NText, { depth: 3 }, () => "-");
  }

  // Sort by priority for display
  const sortedGroups = [...groups].sort((a, b) => {
    if (a.priority === 0 && b.priority !== 0) {
      return 1;
    }
    if (b.priority === 0 && a.priority !== 0) {
      return -1;
    }
    return a.priority - b.priority;
  });

  const maxVisible = 3;
  const visible = sortedGroups.slice(0, maxVisible);
  const hidden = sortedGroups.slice(maxVisible);

  // Create clickable popover for all groups (visible ones)
  const visibleTagsContent = h(
    NPopover,
    {
      trigger: "click",
      placement: "bottom",
      onUpdateShow: (show: boolean) => {
        if (show) {
          openPopoverEdit(row, sortedGroups);
        } else {
          popoverEditingModel.value = null;
        }
      },
    },
    {
      trigger: () =>
        h(NSpace, { size: 4, wrap: false, style: { cursor: "pointer", flexShrink: 0 } }, () =>
          visible.map(g => {
            const typeShort = getGroupTypeShort(g);
            return h(
              NTag,
              {
                size: "tiny",
                type: getPriorityTagType(g.priority),
                bordered: false,
                round: true,
              },
              () => [
                typeShort
                  ? h("span", { style: { opacity: 0.6, marginRight: "1px" } }, typeShort)
                  : null,
                h("span", null, g.group_name),
                h("span", { style: { opacity: 0.7, marginLeft: "2px" } }, `:${g.priority}`),
              ]
            );
          })
        ),
      default: () =>
        h("div", { class: "hidden-groups-popover" }, [
          h(
            "div",
            { class: "popover-groups" },
            popoverEditingGroups.value.map(g =>
              h("div", { class: "hidden-group-item", key: g.group_id }, [
                h(NTag, { size: "tiny", type: getGroupTypeTagType(g), bordered: false }, () =>
                  getGroupTypeLabel(g)
                ),
                h("span", { class: "group-name" }, g.group_name),
                h(NInputNumber, {
                  value: g.priority,
                  min: 0,
                  max: 999,
                  size: "tiny",
                  showButton: true,
                  buttonPlacement: "both",
                  style: { width: "90px" },
                  onUpdateValue: (v: number | null) => {
                    g.priority = v ?? 0;
                  },
                }),
              ])
            )
          ),
          h("div", { class: "popover-footer" }, [
            h(
              NButton,
              {
                size: "tiny",
                type: "primary",
                loading: popoverSaving.value,
                onClick: savePopoverPriorities,
              },
              () => t("common.save")
            ),
          ]),
        ]),
    }
  );

  // If there are hidden groups, show +N indicator
  if (hidden.length > 0) {
    return h(NSpace, { size: 4, wrap: false, align: "center", style: { flexShrink: 0 } }, () => [
      visibleTagsContent,
      h(
        NTag,
        {
          size: "tiny",
          type: "default",
          bordered: false,
          round: true,
          style: { opacity: 0.7, flexShrink: 0 },
        },
        () => `+${hidden.length}`
      ),
    ]);
  }

  return visibleTagsContent;
}

const columns = computed<DataTableColumns<ModelPoolEntryV2>>(() => [
  {
    title: t("hub.channelType"),
    key: "channel_type",
    width: 90,
    align: "center",
    titleAlign: "center",
    render: row => {
      const type = row.groups[0]?.channel_type;
      return type
        ? h(NTag, { size: "tiny", type: "info", bordered: false }, () => type)
        : h(NText, { depth: 3 }, () => "-");
    },
  },
  {
    title: t("hub.modelName"),
    key: "model_name",
    width: 200,
    ellipsis: { tooltip: true },
    titleAlign: "center",
    render: row => h("code", { class: "model-code" }, row.model_name),
  },
  {
    title: t("hub.sourceGroups"),
    key: "groups",
    minWidth: 360,
    titleAlign: "center",
    render: row => h("div", { class: "groups-cell" }, [renderGroupTags(row)]),
  },
  {
    title: t("hub.healthScore"),
    key: "health_score",
    width: 60,
    align: "center",
    titleAlign: "center",
    render: row => {
      const enabledGroups = row.groups.filter(g => g.enabled && g.priority > 0);
      if (!enabledGroups.length) {
        return h(NText, { depth: 3 }, () => "-");
      }
      const best = Math.max(...enabledGroups.map(g => g.health_score));
      return h(NText, { class: getHealthClass(best) }, () => formatPercent(best));
    },
  },
  {
    title: t("hub.groupCount"),
    key: "group_count",
    width: 65,
    align: "center",
    titleAlign: "center",
    render: row => {
      const enabled = row.groups.filter(g => g.enabled && g.priority > 0).length;
      return h(NText, null, () => `${enabled}/${row.groups.length}`);
    },
  },
  {
    title: "",
    key: "actions",
    width: 36,
    align: "center",
    render: row =>
      h(
        NButton,
        { size: "tiny", quaternary: true, type: "primary", onClick: () => openEditModal(row) },
        { icon: () => h(NIcon, { component: CreateOutline, size: 14 }) }
      ),
  },
]);

function openEditModal(model: ModelPoolEntryV2) {
  editingModel.value = model;
  editingGroups.value = model.groups
    .map(g => ({ ...g }))
    .sort((a, b) => {
      if (a.priority === 0 && b.priority !== 0) {
        return 1;
      }
      if (b.priority === 0 && a.priority !== 0) {
        return -1;
      }
      return a.priority - b.priority;
    });
  showEditModal.value = true;
}

async function saveGroupPriorities() {
  if (!editingModel.value) {
    return;
  }

  const currentModel = editingModel.value;
  savingPriority.value = true;
  try {
    const updates = editingGroups.value.map(g => ({
      model_name: currentModel.model_name,
      group_id: g.group_id,
      priority: g.priority,
    }));
    await hubApi.batchUpdatePriorities(updates);

    const model = models.value.find(m => m.model_name === currentModel.model_name);
    if (model) {
      editingGroups.value.forEach(eg => {
        const group = model.groups.find(g => g.group_id === eg.group_id);
        if (group) {
          group.priority = eg.priority;
        }
      });
    }

    showEditModal.value = false;
    message.success(t("common.operationSuccess"));
  } catch (_) {
    // Error handled by interceptor
  } finally {
    savingPriority.value = false;
  }
}

function resetFilters() {
  searchText.value = "";
  filters.value.channelType = "all";
  currentPage.value = 1;
}

async function loadModelPool() {
  loading.value = true;
  try {
    const res = await hubApi.getModelPoolV2();
    models.value = res.models || [];
  } catch (_) {
    models.value = [];
  } finally {
    loading.value = false;
  }
}

watch([searchText, () => filters.value.channelType], () => {
  currentPage.value = 1;
});

defineExpose({ refresh: loadModelPool });

onMounted(() => {
  loadModelPool();
});
</script>

<template>
  <div class="model-pool-container">
    <!-- Filter row -->
    <div class="filter-row">
      <n-space align="center" :size="6" class="filter-left">
        <n-input
          v-model:value="searchText"
          :placeholder="t('hub.searchModelPlaceholder')"
          size="tiny"
          clearable
          style="width: 160px"
        >
          <template #prefix>
            <n-icon :component="Search" size="14" />
          </template>
        </n-input>
        <span class="filter-item">
          <n-text depth="3" class="filter-label">{{ t("hub.channelType") }}</n-text>
          <n-select
            v-model:value="filters.channelType"
            size="tiny"
            style="width: 90px"
            :options="channelTypeOptions"
            :consistent-menu-width="false"
          />
        </span>
        <n-button size="tiny" quaternary @click="resetFilters">
          <template #icon><n-icon :component="RefreshOutline" size="14" /></template>
        </n-button>
      </n-space>
      <n-text depth="3" style="font-size: 12px">
        {{ t("hub.totalModels", { total: filteredModels.length }) }}
      </n-text>
    </div>

    <!-- Table -->
    <div class="table-wrapper">
      <n-spin :show="loading">
        <n-data-table
          v-if="paginatedModels.length || loading"
          :columns="columns"
          :data="paginatedModels"
          :bordered="false"
          :single-line="false"
          size="small"
          :max-height="'calc(100vh - 240px)'"
          :pagination="{
            page: currentPage,
            pageSize,
            pageCount: totalPages,
            showSizePicker: true,
            pageSizes: [20, 50, 100, 200],
            size: 'small',
            onUpdatePage: (p: number) => {
              currentPage = p;
            },
            onUpdatePageSize: (s: number) => {
              pageSize = s;
              currentPage = 1;
            },
          }"
        />
        <n-empty v-else :description="t('hub.noModels')" />
      </n-spin>
    </div>

    <!-- Edit Priority Modal -->
    <n-modal
      v-model:show="showEditModal"
      preset="card"
      :title="t('hub.editPriority')"
      style="width: 560px; max-width: 95vw"
      :bordered="false"
      size="small"
    >
      <div v-if="editingModel" class="priority-edit-content">
        <div class="edit-header">
          <code class="edit-model-name">{{ editingModel.model_name }}</code>
          <n-text depth="3" style="font-size: 11px">{{ t("hub.priorityHint") }}</n-text>
        </div>

        <div class="priority-table">
          <div class="priority-table-header">
            <span class="col-name">{{ t("hub.sourceGroups") }}</span>
            <span class="col-gtype">{{ t("hub.groupType") }}</span>
            <span class="col-ctype">{{ t("hub.channelType") }}</span>
            <span class="col-health">{{ t("hub.healthScore") }}</span>
            <span class="col-priority">{{ t("hub.priority") }}</span>
          </div>
          <div class="priority-table-body">
            <div
              v-for="group in editingGroups"
              :key="group.group_id"
              class="priority-row"
              :class="{ disabled: group.priority === 0 }"
            >
              <span class="col-name" :title="group.group_name">{{ group.group_name }}</span>
              <span class="col-gtype">
                <n-tag size="tiny" :type="getGroupTypeTagType(group)" :bordered="false">
                  {{ getGroupTypeLabel(group) }}
                </n-tag>
              </span>
              <span class="col-ctype">
                <n-tag v-if="group.channel_type" size="tiny" type="info" :bordered="false">
                  {{ group.channel_type }}
                </n-tag>
                <span v-else class="text-muted">-</span>
              </span>
              <span class="col-health" :class="getHealthClass(group.health_score)">
                {{ formatPercent(group.health_score) }}
              </span>
              <span class="col-priority">
                <n-input-number
                  v-model:value="group.priority"
                  :min="0"
                  :max="999"
                  size="tiny"
                  :show-button="true"
                  button-placement="both"
                  style="width: 95px"
                />
              </span>
            </div>
          </div>
        </div>
      </div>

      <template #footer>
        <n-space justify="end" :size="8">
          <n-button size="small" @click="showEditModal = false">{{ t("common.cancel") }}</n-button>
          <n-button
            size="small"
            type="primary"
            :loading="savingPriority"
            @click="saveGroupPriorities"
          >
            {{ t("common.save") }}
          </n-button>
        </n-space>
      </template>
    </n-modal>
  </div>
</template>

<style scoped>
.model-pool-container {
  display: flex;
  flex-direction: column;
  height: 100%;
  min-height: 0;
}

.filter-row {
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

.filter-left {
  display: flex;
  align-items: center;
  flex: 1;
  min-width: 0;
}

.filter-item {
  display: inline-flex;
  align-items: center;
  gap: 2px;
}

.filter-label {
  font-size: 12px;
  white-space: nowrap;
}

.table-wrapper {
  flex: 1;
  min-height: 0;
  overflow: hidden;
}

/* Ensure table header cells don't wrap */
:deep(.n-data-table-th) {
  white-space: nowrap;
}

.model-code {
  background: transparent;
  padding: 0;
  font-size: 12px;
}

.health-good {
  color: var(--success-color, #18a058);
}
.health-warning {
  color: var(--warning-color, #f0a020);
}
.health-critical {
  color: var(--error-color, #d03050);
}
.text-muted {
  color: var(--n-text-color-3);
}

/* Groups cell - single line with overflow handling */
.groups-cell {
  display: flex;
  align-items: center;
  white-space: nowrap;
  overflow: hidden;
  min-width: 0;
}

/* Edit modal styles */
.priority-edit-content {
  max-height: 55vh;
  overflow-y: auto;
}

.edit-header {
  display: flex;
  justify-content: space-between;
  align-items: center;
  margin-bottom: 8px;
  padding-bottom: 6px;
  border-bottom: 1px solid var(--n-border-color);
}

.edit-model-name {
  font-size: 13px;
  font-weight: 500;
  background: var(--n-color-embedded);
  padding: 2px 8px;
  border-radius: 4px;
}

.priority-table {
  font-size: 12px;
}

.priority-table-header {
  display: flex;
  align-items: center;
  padding: 6px 8px;
  background: var(--n-color-embedded);
  border-radius: 4px;
  font-weight: 500;
  color: var(--n-text-color-2);
  margin-bottom: 4px;
}

.priority-table-body {
  display: flex;
  flex-direction: column;
  gap: 2px;
}

.priority-row {
  display: flex;
  align-items: center;
  padding: 5px 8px;
  border-radius: 4px;
  transition: background 0.15s;
}

.priority-row:hover {
  background: var(--n-color-embedded);
}

.priority-row.disabled {
  opacity: 0.5;
}

.col-name {
  flex: 1;
  min-width: 0;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
  padding-right: 8px;
}

.col-gtype {
  width: 50px;
  text-align: center;
  flex-shrink: 0;
}

.col-ctype {
  width: 60px;
  text-align: center;
  flex-shrink: 0;
}

.col-health {
  width: 45px;
  text-align: center;
  flex-shrink: 0;
}

.col-priority {
  width: 100px;
  text-align: right;
  flex-shrink: 0;
}
</style>

<!-- Global styles for popover -->
<style>
.hidden-groups-popover {
  min-width: 320px;
  max-width: 420px;
  padding: 4px 0;
}

.popover-groups {
  max-height: 280px;
  overflow-y: auto;
}

.hidden-group-item {
  display: flex;
  align-items: center;
  gap: 8px;
  padding: 5px 10px;
  font-size: 12px;
}

.hidden-group-item:hover {
  background: var(--n-color-embedded);
}

.hidden-group-item .group-name {
  flex: 1;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
  min-width: 80px;
}

.popover-footer {
  display: flex;
  justify-content: flex-end;
  padding: 8px 10px 4px;
  border-top: 1px solid var(--n-border-color);
  margin-top: 4px;
}
</style>
