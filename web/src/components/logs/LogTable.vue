<script setup lang="ts">
import { logApi } from "@/api/logs";
import type { LogFilter, RequestLog } from "@/types/models";
import { copyWithFallback, createManualCopyContent } from "@/utils/clipboard";
import { formatTokenCount, maskKey } from "@/utils/display";
import {
  CheckmarkDoneOutline,
  CloseCircleOutline,
  CopyOutline,
  DocumentTextOutline,
  DownloadOutline,
  EyeOffOutline,
  EyeOutline,
  ReloadOutline,
  Search,
  SettingsOutline,
} from "@vicons/ionicons5";
import {
  NButton,
  NButtonGroup,
  NCard,
  NCheckbox,
  NCheckboxGroup,
  NDataTable,
  NDatePicker,
  NEllipsis,
  NIcon,
  NInput,
  NModal,
  NPopover,
  NSelect,
  NSpace,
  NSpin,
  NTag,
  NTooltip,
  useDialog,
  useMessage,
} from "naive-ui";
import { computed, h, onMounted, reactive, ref, watch, type VNodeChild } from "vue";
import { useI18n } from "vue-i18n";

const { t } = useI18n();

// Message instance
const message = useMessage();
const dialog = useDialog();

interface LogRow extends RequestLog {
  is_key_visible: boolean;
}

// Column configuration
interface ColumnConfig {
  key: string;
  title: string;
  width: number;
  defaultVisible: boolean;
  alwaysVisible?: boolean; // Some columns cannot be hidden
  required?: boolean; // Required field, cannot be unchecked
  fixed?: "left" | "right"; // Fixed column position
  render?: (row: LogRow) => VNodeChild;
}

// Data
const loading = ref(false);
const logs = ref<LogRow[]>([]);
const currentPage = ref(1);
const pageSize = ref(15);
const total = ref(0);
const hasMore = ref(false);
const totalPages = computed(() => {
  if (total.value < 0) {
    return -1;
  }
  return Math.ceil(total.value / pageSize.value);
});

// Display text for pagination info
const totalRecordsText = computed(() => {
  if (total.value < 0) {
    // Fast log pagination intentionally skips COUNT(*) on large tables.
    return t("logs.calculatingTotal");
  }
  return t("logs.totalRecords", { total: total.value });
});

const pageInfoText = computed(() => {
  if (totalPages.value < 0) {
    return t("logs.pageInfoUnknown", { current: currentPage.value });
  }
  return t("logs.pageInfo", { current: currentPage.value, total: totalPages.value });
});

// Check if next page button should be disabled
const isNextPageDisabled = computed(() => {
  // If we know the total pages and we're at or beyond it, disable
  if (totalPages.value > 0 && currentPage.value >= totalPages.value) {
    return true;
  }
  if (totalPages.value < 0) {
    return !hasMore.value;
  }
  // If we have no data on current page, also disable
  if (logs.value.length === 0) {
    return true;
  }
  // Otherwise, allow user to try (backend will handle if there's no more data)
  return false;
});

// Modal for viewing request/response details
const showDetailModal = ref(false);
const selectedLog = ref<LogRow | null>(null);

// Filters
const filters = reactive({
  parent_group_name: "",
  group_name: "",
  key_value: "",
  model: "",
  is_success: ref(null),
  status_code: "",
  source_ip: "",
  error_contains: "",
  start_time: null as number | null,
  end_time: null as number | null,
  request_type: ref(null),
});

const successOptions = [
  { label: t("common.success"), value: "true" },
  { label: t("common.error"), value: "false" },
];

const requestTypeOptions = [
  { label: t("logs.retryRequest"), value: "retry" },
  { label: t("logs.finalRequest"), value: "final" },
  { label: t("logs.validationRequest"), value: "validation" },
];

const getRequestTypeLabel = (requestType: RequestLog["request_type"]) => {
  if (requestType === "retry") {
    return t("logs.retryRequest");
  }
  if (requestType === "validation") {
    return t("logs.validationRequest");
  }
  return t("logs.finalRequest");
};

const getRequestTypeTagType = (requestType: RequestLog["request_type"]) => {
  if (requestType === "retry") {
    return "warning";
  }
  if (requestType === "validation") {
    return "info";
  }
  return "default";
};

// Fetch data
const loadLogs = async () => {
  loading.value = true;
  try {
    const params: LogFilter = {
      page: currentPage.value,
      page_size: pageSize.value,
      parent_group_name: filters.parent_group_name || undefined,
      group_name: filters.group_name || undefined,
      key_value: filters.key_value || undefined,
      model: filters.model || undefined,
      is_success:
        filters.is_success === "" || filters.is_success === null
          ? undefined
          : filters.is_success === "true",
      status_code: filters.status_code ? parseInt(filters.status_code, 10) : undefined,
      source_ip: filters.source_ip || undefined,
      error_contains: filters.error_contains || undefined,
      start_time: filters.start_time ? new Date(filters.start_time).toISOString() : undefined,
      end_time: filters.end_time ? new Date(filters.end_time).toISOString() : undefined,
      request_type: filters.request_type || undefined,
    };

    const res = await logApi.getLogs(params);
    if (res.code === 0 && res.data) {
      logs.value = res.data.items.map(log => ({ ...log, is_key_visible: false }));
      total.value = res.data.pagination.total_items;
      hasMore.value =
        res.data.pagination.has_more ??
        (res.data.pagination.total_items < 0 && logs.value.length >= pageSize.value);
    } else {
      logs.value = [];
      total.value = 0;
      hasMore.value = false;
      window.$message.error(res.message || t("logs.loadFailed"), {
        keepAliveOnHover: true,
        duration: 5000,
        closable: true,
      });
    }
  } catch (_error) {
    hasMore.value = false;
    window.$message.error(t("logs.requestFailed"));
  } finally {
    loading.value = false;
  }
};

const formatDateTime = (timestamp: string) => {
  if (!timestamp) {
    return "-";
  }
  const date = new Date(timestamp);
  return date.toLocaleString("zh-CN", { hour12: false }).replace(/\//g, "-");
};

const toggleKeyVisibility = (row: LogRow) => {
  row.is_key_visible = !row.is_key_visible;
};

const viewLogDetails = (row: LogRow) => {
  selectedLog.value = row;
  showDetailModal.value = true;
};

const closeDetailModal = () => {
  showDetailModal.value = false;
  selectedLog.value = null;
};

const formatJsonString = (jsonStr: string) => {
  if (!jsonStr) {
    return "";
  }
  try {
    return JSON.stringify(JSON.parse(jsonStr), null, 2);
  } catch {
    return jsonStr;
  }
};

const getTokenMetrics = (log: RequestLog) => {
  return [
    { label: t("logs.inputTokens"), value: log.input_tokens },
    { label: t("logs.outputTokens"), value: log.output_tokens },
    { label: t("logs.totalTokens"), value: log.total_tokens },
    { label: t("logs.cacheReadTokens"), value: log.cache_read_tokens },
    { label: t("logs.cacheWriteTokens"), value: log.cache_write_tokens },
    { label: t("logs.thinkingTokens"), value: log.thinking_tokens },
  ].filter(
    (metric): metric is { label: string; value: number } => typeof metric.value === "number"
  );
};

const hasTokenUsage = (log: RequestLog) => {
  return getTokenMetrics(log).some(metric => metric.value > 0) || !!log.token_usage_source;
};

const formatTokenUsageSource = (source: RequestLog["token_usage_source"]) => {
  if (source === "upstream") {
    return t("logs.tokenSourceUpstream");
  }
  if (source === "estimated") {
    return t("logs.tokenSourceEstimated");
  }
  return "-";
};

const formatTokenUsageInline = (log: RequestLog) => {
  const metrics = getTokenMetrics(log).map(
    metric => `${metric.label}: ${formatTokenCount(metric.value || 0)}`
  );
  if (log.token_usage_source) {
    metrics.push(
      `${t("logs.tokenUsageSource")}: ${formatTokenUsageSource(log.token_usage_source)}`
    );
  }
  return metrics.length > 0 ? metrics.join(" · ") : "-";
};

const shouldShowUserAgentChange = computed(() => {
  const log = selectedLog.value;
  return !!log?.simulated_client_enabled && (!!log?.user_agent || !!log?.upstream_user_agent);
});

const selectedUserAgent = computed(() => {
  const log = selectedLog.value;
  return log?.user_agent || log?.upstream_user_agent || "";
});

// Copy helper with fallback dialog
const copyContent = async (content: string, type: string) => {
  await copyWithFallback(content, {
    onSuccess: () => {
      message.success(t("logs.copiedToClipboard", { type }));
    },
    onError: () => {
      message.error(t("logs.copyFailed", { type }));
    },
    showManualDialog: (text: string) => {
      dialog.create({
        title: t("common.copy"),
        content: () => createManualCopyContent(h, text, t),
        positiveText: t("common.close"),
      });
    },
  });
};

// Column visibility management
const visibleColumns = ref<string[]>([]);
const STORAGE_KEY = "log-table-visible-columns";

// Columns that should always be included by default (even if not in saved preferences)
const ALWAYS_DEFAULT_COLUMNS = ["parent_group_name", "token_usage"];

// Load column preferences from localStorage
const loadColumnPreferences = () => {
  const saved = localStorage.getItem(STORAGE_KEY);
  if (saved) {
    try {
      const parsed = JSON.parse(saved) as string[];
      // Ensure always-default columns are included
      const merged = [...new Set([...parsed, ...ALWAYS_DEFAULT_COLUMNS])];
      visibleColumns.value = merged;
    } catch {
      // If parse fails, use defaults
      setDefaultColumns();
    }
  } else {
    setDefaultColumns();
  }
};

// Set default visible columns (all columns selected by default)
const setDefaultColumns = () => {
  visibleColumns.value = allColumnConfigs.filter(col => !col.alwaysVisible).map(col => col.key);
};

// Save column preferences to localStorage
const saveColumnPreferences = () => {
  localStorage.setItem(STORAGE_KEY, JSON.stringify(visibleColumns.value));
};

// Watch for changes and save
watch(visibleColumns, saveColumnPreferences, { deep: true });

// All available columns configuration
const allColumnConfigs: ColumnConfig[] = [
  {
    key: "timestamp",
    title: t("logs.time"),
    width: 160,
    defaultVisible: true,
    required: true, // Required field
    render: (row: LogRow) => formatDateTime(row.timestamp),
  },
  {
    key: "is_success",
    title: t("common.status"),
    width: 90,
    defaultVisible: true,
    required: true, // Required field
    render: (row: LogRow) =>
      h(
        NTag,
        { type: row.is_success ? "success" : "error", size: "small", round: true },
        { default: () => (row.is_success ? t("common.success") : t("common.error")) }
      ),
  },
  {
    key: "request_type",
    title: t("logs.requestType"),
    width: 130,
    defaultVisible: true,
    render: (row: LogRow) => {
      return h(
        NTag,
        { type: getRequestTypeTagType(row.request_type), size: "small", round: true },
        { default: () => getRequestTypeLabel(row.request_type) }
      );
    },
  },
  {
    key: "is_stream",
    title: t("logs.responseType"),
    width: 140,
    defaultVisible: true,
    render: (row: LogRow) =>
      h(
        NTag,
        { type: row.is_stream ? "info" : "default", size: "small", round: true },
        { default: () => (row.is_stream ? t("logs.stream") : t("logs.nonStream")) }
      ),
  },
  {
    key: "status_code",
    title: t("logs.statusCode"),
    width: 130,
    defaultVisible: true,
  },
  {
    key: "duration_ms",
    title: t("logs.duration"),
    width: 110,
    defaultVisible: true,
  },
  {
    key: "parent_group_name",
    title: t("logs.parentGroup"),
    width: 120,
    defaultVisible: true,
    render: (row: LogRow) => row.parent_group_name || "-",
  },
  {
    key: "group_name",
    title: t("logs.group"),
    width: 120,
    defaultVisible: true,
  },
  {
    key: "model",
    title: t("logs.model"),
    width: 240,
    defaultVisible: true,
    required: true, // Required field
    render: (row: LogRow) => {
      // Show mapped_model → model if mapping exists
      if (row.mapped_model && row.mapped_model !== row.model) {
        return h("span", {}, [
          h("span", { style: "color: var(--text-tertiary)" }, row.mapped_model),
          h("span", { style: "color: var(--text-tertiary); margin: 0 4px" }, "→"),
          h("span", {}, row.model),
        ]);
      }
      return row.model || "-";
    },
  },
  {
    key: "token_usage",
    title: t("logs.tokenUsage"),
    width: 280,
    defaultVisible: true,
    render: (row: LogRow) =>
      h(
        NEllipsis,
        { style: "max-width: 260px", tooltip: true },
        { default: () => formatTokenUsageInline(row) }
      ),
  },
  {
    key: "key_value",
    title: "Key",
    width: 200,
    defaultVisible: true,
    render: (row: LogRow) =>
      h(NSpace, { align: "center", wrap: false }, () => [
        h(
          NEllipsis,
          { style: "max-width: 150px" },
          { default: () => (row.is_key_visible ? row.key_value : maskKey(row.key_value || "")) }
        ),
        h(
          NButton,
          { size: "tiny", text: true, onClick: () => toggleKeyVisibility(row) },
          {
            icon: () =>
              h(NIcon, null, { default: () => h(row.is_key_visible ? EyeOffOutline : EyeOutline) }),
          }
        ),
      ]),
  },
  {
    key: "source_ip",
    title: t("logs.sourceIP"),
    width: 260,
    defaultVisible: true,
  },
  {
    key: "request_path",
    title: t("logs.requestPath"),
    width: 550,
    defaultVisible: true,
    render: (row: LogRow) =>
      h(NEllipsis, { style: "max-width: 530px" }, { default: () => row.request_path || "-" }),
  },
  {
    key: "upstream_addr",
    title: t("logs.upstreamAddress"),
    width: 600,
    defaultVisible: true,
    render: (row: LogRow) =>
      h(NEllipsis, { style: "max-width: 580px" }, { default: () => row.upstream_addr || "-" }),
  },
  {
    key: "error_message",
    title: t("logs.errorMessage"),
    width: 600,
    defaultVisible: true,
    render: (row: LogRow) => {
      if (!row.error_message) {
        return "-";
      }
      return h(
        NEllipsis,
        {
          style: "max-width: 580px; color: var(--error-color)",
          tooltip: true,
        },
        { default: () => row.error_message }
      );
    },
  },
  {
    key: "actions",
    title: t("common.actions"),
    width: 100,
    defaultVisible: true,
    alwaysVisible: true, // Actions column cannot be hidden
    fixed: "right" as const,
    render: (row: LogRow) =>
      h(
        NButton,
        {
          size: "small",
          type: "primary",
          ghost: true,
          onClick: () => viewLogDetails(row),
        },
        {
          icon: () => h(NIcon, null, { default: () => h(DocumentTextOutline) }),
          default: () => t("common.detail"),
        }
      ),
  },
];

// Generate displayed columns based on visibility settings
const createColumns = () => {
  return allColumnConfigs
    .filter(col => col.alwaysVisible || col.required || visibleColumns.value.includes(col.key))
    .map(col => ({
      title: col.title,
      key: col.key,
      width: col.width,
      fixed: col.fixed,
      render: col.render,
    }));
};

// Computed columns that react to visibility changes
const columns = computed(() => createColumns());

const scrollX = computed(() => {
  const visibleColumns = createColumns();
  return visibleColumns.reduce((total, col) => total + (col.width || 100), 0);
});

// Lifecycle and Watchers
onMounted(() => {
  loadColumnPreferences();
  loadLogs();
});
watch([currentPage, pageSize], loadLogs);

const handleSearch = () => {
  currentPage.value = 1;
  loadLogs();
};

const resetFilters = () => {
  filters.parent_group_name = "";
  filters.group_name = "";
  filters.key_value = "";
  filters.model = "";
  filters.is_success = null;
  filters.status_code = "";
  filters.source_ip = "";
  filters.error_contains = "";
  filters.start_time = null;
  filters.end_time = null;
  filters.request_type = null;
  handleSearch();
};

const exportLogs = () => {
  const params: Omit<LogFilter, "page" | "page_size"> = {
    parent_group_name: filters.parent_group_name || undefined,
    group_name: filters.group_name || undefined,
    key_value: filters.key_value || undefined,
    model: filters.model || undefined,
    is_success:
      filters.is_success === "" || filters.is_success === null
        ? undefined
        : filters.is_success === "true",
    status_code: filters.status_code ? parseInt(filters.status_code, 10) : undefined,
    source_ip: filters.source_ip || undefined,
    error_contains: filters.error_contains || undefined,
    start_time: filters.start_time ? new Date(filters.start_time).toISOString() : undefined,
    end_time: filters.end_time ? new Date(filters.end_time).toISOString() : undefined,
    request_type: filters.request_type || undefined,
  };
  logApi.exportLogs(params);
};

function changePage(page: number) {
  currentPage.value = page;
}

function changePageSize(size: number) {
  pageSize.value = size;
  currentPage.value = 1;
}

// Column selector functions
const selectAllColumns = () => {
  visibleColumns.value = allColumnConfigs.filter(col => !col.alwaysVisible).map(col => col.key);
};

const deselectAllColumns = () => {
  // Keep required columns and always-default columns selected for consistent behavior
  const required = allColumnConfigs.filter(col => col.required).map(col => col.key);
  visibleColumns.value = [...new Set([...required, ...ALWAYS_DEFAULT_COLUMNS])];
};
</script>

<template>
  <div class="log-table-container">
    <n-space vertical>
      <!-- Toolbar -->
      <div class="toolbar">
        <div class="filter-section">
          <div class="filter-row">
            <div class="filter-grid">
              <div class="filter-item">
                <n-select
                  v-model:value="filters.is_success"
                  :options="successOptions"
                  size="small"
                  :placeholder="t('common.status')"
                  clearable
                  @update:value="handleSearch"
                />
              </div>
              <div class="filter-item">
                <n-select
                  v-model:value="filters.request_type"
                  :options="requestTypeOptions"
                  size="small"
                  clearable
                  :placeholder="t('logs.requestType')"
                  @update:value="handleSearch"
                />
              </div>
              <div class="filter-item">
                <n-input
                  v-model:value="filters.status_code"
                  :placeholder="t('logs.statusCode')"
                  size="small"
                  clearable
                  @keyup.enter="handleSearch"
                />
              </div>
              <div class="filter-item">
                <n-input
                  v-model:value="filters.parent_group_name"
                  :placeholder="t('logs.parentGroupName')"
                  size="small"
                  clearable
                  @keyup.enter="handleSearch"
                />
              </div>
              <div class="filter-item">
                <n-input
                  v-model:value="filters.group_name"
                  :placeholder="t('logs.groupName')"
                  size="small"
                  clearable
                  @keyup.enter="handleSearch"
                />
              </div>
              <div class="filter-item">
                <n-input
                  v-model:value="filters.model"
                  :placeholder="t('logs.model')"
                  size="small"
                  clearable
                  @keyup.enter="handleSearch"
                />
              </div>
              <div class="filter-item">
                <n-date-picker
                  v-model:value="filters.start_time"
                  type="datetime"
                  clearable
                  size="small"
                  :placeholder="t('common.startTime')"
                />
              </div>
              <div class="filter-item">
                <n-date-picker
                  v-model:value="filters.end_time"
                  type="datetime"
                  clearable
                  size="small"
                  :placeholder="t('common.endTime')"
                />
              </div>
              <div class="filter-item">
                <n-input
                  v-model:value="filters.key_value"
                  :placeholder="t('logs.key')"
                  size="small"
                  clearable
                  @keyup.enter="handleSearch"
                />
              </div>
              <div class="filter-item">
                <n-input
                  v-model:value="filters.error_contains"
                  :placeholder="t('logs.errorMessage')"
                  size="small"
                  clearable
                  @keyup.enter="handleSearch"
                />
              </div>
              <div class="filter-actions">
                <n-button-group size="small">
                  <n-tooltip trigger="hover">
                    <template #trigger>
                      <n-button ghost :disabled="loading" @click="handleSearch">
                        <template #icon>
                          <n-icon :component="Search" />
                        </template>
                      </n-button>
                    </template>
                    {{ t("common.search") }}
                  </n-tooltip>
                  <n-tooltip trigger="hover">
                    <template #trigger>
                      <n-button @click="resetFilters">
                        <template #icon>
                          <n-icon :component="ReloadOutline" />
                        </template>
                      </n-button>
                    </template>
                    {{ t("common.reset") }}
                  </n-tooltip>
                  <n-tooltip trigger="hover">
                    <template #trigger>
                      <n-button ghost @click="exportLogs">
                        <template #icon>
                          <n-icon :component="DownloadOutline" />
                        </template>
                      </n-button>
                    </template>
                    {{ t("logs.exportLogs") }}
                  </n-tooltip>
                  <n-popover trigger="click" placement="bottom-end">
                    <template #trigger>
                      <n-tooltip trigger="hover">
                        <template #trigger>
                          <n-button ghost>
                            <template #icon>
                              <n-icon :component="SettingsOutline" />
                            </template>
                          </n-button>
                        </template>
                        {{ t("logs.customColumns") }}
                      </n-tooltip>
                    </template>
                    <div class="column-selector">
                      <n-space vertical size="medium">
                        <n-space>
                          <n-tooltip trigger="hover">
                            <template #trigger>
                              <n-button size="tiny" @click="selectAllColumns">
                                <template #icon>
                                  <n-icon :component="CheckmarkDoneOutline" />
                                </template>
                              </n-button>
                            </template>
                            {{ t("common.selectAll") }}
                          </n-tooltip>
                          <n-tooltip trigger="hover">
                            <template #trigger>
                              <n-button size="tiny" @click="deselectAllColumns">
                                <template #icon>
                                  <n-icon :component="CloseCircleOutline" />
                                </template>
                              </n-button>
                            </template>
                            {{ t("common.deselectAll") }}
                          </n-tooltip>
                        </n-space>
                        <n-checkbox-group v-model:value="visibleColumns">
                          <n-space vertical size="small">
                            <n-checkbox
                              v-for="col in allColumnConfigs.filter(c => !c.alwaysVisible)"
                              :key="col.key"
                              :value="col.key"
                              :label="col.title"
                              :disabled="col.required"
                            />
                          </n-space>
                        </n-checkbox-group>
                      </n-space>
                    </div>
                  </n-popover>
                </n-button-group>
              </div>
            </div>
          </div>
        </div>
      </div>
      <div class="table-main">
        <!-- Table -->
        <div class="table-container">
          <n-spin :show="loading">
            <n-data-table
              :columns="columns"
              :data="logs"
              :bordered="false"
              remote
              size="small"
              :scroll-x="scrollX"
            />
          </n-spin>
        </div>

        <!-- Pagination -->
        <div class="pagination-container">
          <div class="pagination-info">
            <span>{{ totalRecordsText }}</span>
            <n-select
              v-model:value="pageSize"
              :options="[
                { label: t('logs.recordsPerPage', { count: 15 }), value: 15 },
                { label: t('logs.recordsPerPage', { count: 30 }), value: 30 },
                { label: t('logs.recordsPerPage', { count: 50 }), value: 50 },
                { label: t('logs.recordsPerPage', { count: 100 }), value: 100 },
              ]"
              size="small"
              style="width: 100px; margin-left: 12px"
              @update:value="changePageSize"
            />
          </div>
          <div class="pagination-controls">
            <n-button
              size="small"
              :disabled="currentPage <= 1"
              @click="changePage(currentPage - 1)"
            >
              {{ t("logs.previousPage") }}
            </n-button>
            <span class="page-info">
              {{ pageInfoText }}
            </span>
            <n-button
              size="small"
              :disabled="isNextPageDisabled"
              @click="changePage(currentPage + 1)"
            >
              {{ t("logs.nextPage") }}
            </n-button>
          </div>
        </div>
      </div>
    </n-space>

    <!-- Detail modal -->
    <n-modal
      v-model:show="showDetailModal"
      preset="card"
      style="width: 1000px"
      :title="t('logs.requestDetails')"
    >
      <div v-if="selectedLog" style="max-height: 65vh; overflow-y: auto">
        <n-space vertical size="small">
          <!-- Basic information -->
          <n-card
            :title="t('logs.basicInfo')"
            size="small"
            :header-style="{ padding: '8px 12px', fontSize: '13px' }"
          >
            <div class="detail-grid-compact">
              <div class="detail-item-compact">
                <span class="detail-label-compact">{{ t("logs.time") }}:</span>
                <span class="detail-value-compact">
                  {{ formatDateTime(selectedLog.timestamp) }}
                </span>
              </div>
              <div class="detail-item-compact">
                <span class="detail-label-compact">{{ t("common.status") }}:</span>
                <n-tag :type="selectedLog.is_success ? 'success' : 'error'" size="small">
                  {{ selectedLog.is_success ? t("common.success") : t("common.error") }} -
                  {{ selectedLog.status_code }}
                </n-tag>
              </div>
              <div class="detail-item-compact">
                <span class="detail-label-compact">{{ t("logs.duration") }}:</span>
                <span class="detail-value-compact">{{ selectedLog.duration_ms }}ms</span>
              </div>
              <div class="detail-item-compact">
                <span class="detail-label-compact">{{ t("logs.parentGroup") }}:</span>
                <span class="detail-value-compact">{{ selectedLog.parent_group_name || "-" }}</span>
              </div>
              <div class="detail-item-compact">
                <span class="detail-label-compact">{{ t("logs.group") }}:</span>
                <span class="detail-value-compact">{{ selectedLog.group_name }}</span>
              </div>
              <div class="detail-item-compact">
                <span class="detail-label-compact">{{ t("logs.model") }}:</span>
                <span class="detail-value-compact">{{ selectedLog.model }}</span>
              </div>
              <div class="detail-item-compact" v-if="selectedLog.mapped_model">
                <span class="detail-label-compact">{{ t("logs.requestedModel") }}:</span>
                <span class="detail-value-compact">{{ selectedLog.mapped_model }}</span>
              </div>
              <div class="detail-item-compact">
                <span class="detail-label-compact">{{ t("logs.requestType") }}:</span>
                <n-tag :type="getRequestTypeTagType(selectedLog.request_type)" size="small">
                  {{ getRequestTypeLabel(selectedLog.request_type) }}
                </n-tag>
              </div>
              <div class="detail-item-compact">
                <span class="detail-label-compact">{{ t("logs.responseType") }}:</span>
                <n-tag :type="selectedLog.is_stream ? 'info' : 'default'" size="small">
                  {{ selectedLog.is_stream ? t("logs.stream") : t("logs.nonStream") }}
                </n-tag>
              </div>
              <div class="detail-item-compact">
                <span class="detail-label-compact">{{ t("logs.sourceIP") }}:</span>
                <span class="detail-value-compact">{{ selectedLog.source_ip || "-" }}</span>
              </div>
              <div class="detail-item-compact key-item">
                <span class="detail-label-compact">{{ t("logs.key") }}:</span>
                <div class="key-display-compact">
                  <span class="key-value-compact">
                    {{
                      selectedLog.is_key_visible
                        ? selectedLog.key_value || "-"
                        : maskKey(selectedLog.key_value || "")
                    }}
                  </span>
                  <div class="key-actions-compact">
                    <n-button size="tiny" text @click="toggleKeyVisibility(selectedLog)">
                      <template #icon>
                        <n-icon
                          :component="selectedLog.is_key_visible ? EyeOffOutline : EyeOutline"
                        />
                      </template>
                    </n-button>
                    <n-button
                      v-if="selectedLog.key_value"
                      size="tiny"
                      text
                      @click="copyContent(selectedLog.key_value, 'API Key')"
                    >
                      <template #icon>
                        <n-icon :component="CopyOutline" />
                      </template>
                    </n-button>
                  </div>
                </div>
              </div>
            </div>
          </n-card>

          <!-- Token information -->
          <n-card
            v-if="hasTokenUsage(selectedLog)"
            :title="t('logs.tokenUsage')"
            size="small"
            :header-style="{ padding: '8px 12px', fontSize: '13px' }"
          >
            <div class="token-inline-compact">
              <span
                v-for="metric in getTokenMetrics(selectedLog)"
                :key="metric.label"
                class="token-pill-compact"
              >
                <span class="token-label-compact">{{ metric.label }}:</span>
                <span class="token-value-compact">{{ formatTokenCount(metric.value || 0) }}</span>
              </span>
              <span class="token-pill-compact" v-if="selectedLog.token_usage_source">
                <span class="token-label-compact">{{ t("logs.tokenUsageSource") }}:</span>
                <n-tag
                  :type="selectedLog.token_usage_source === 'estimated' ? 'warning' : 'success'"
                  size="small"
                >
                  {{ formatTokenUsageSource(selectedLog.token_usage_source) }}
                </n-tag>
              </span>
            </div>
          </n-card>

          <!-- Request information (compact layout) -->
          <n-card
            :title="t('logs.requestInfo')"
            size="small"
            :header-style="{ padding: '8px 12px', fontSize: '13px' }"
          >
            <div class="compact-fields">
              <div class="compact-field" v-if="selectedLog.request_path">
                <div class="compact-field-header">
                  <span class="compact-field-title">{{ t("logs.requestPath") }}</span>
                  <n-button
                    size="tiny"
                    text
                    @click="copyContent(selectedLog.request_path, t('logs.requestPath'))"
                  >
                    <template #icon>
                      <n-icon :component="CopyOutline" />
                    </template>
                  </n-button>
                </div>
                <div class="compact-field-content">
                  {{ selectedLog.request_path }}
                </div>
              </div>

              <div class="compact-field" v-if="selectedLog.upstream_addr">
                <div class="compact-field-header">
                  <span class="compact-field-title">{{ t("logs.upstreamAddress") }}</span>
                  <n-button
                    size="tiny"
                    text
                    @click="copyContent(selectedLog.upstream_addr, t('logs.upstreamAddress'))"
                  >
                    <template #icon>
                      <n-icon :component="CopyOutline" />
                    </template>
                  </n-button>
                </div>
                <div class="compact-field-content">
                  {{ selectedLog.upstream_addr }}
                </div>
              </div>

              <div class="compact-field" v-if="selectedLog.proxy_url">
                <div class="compact-field-header">
                  <span class="compact-field-title">{{ t("logs.proxyServer") }}</span>
                  <n-button
                    size="tiny"
                    text
                    @click="copyContent(selectedLog.proxy_url, t('logs.proxyServer'))"
                  >
                    <template #icon>
                      <n-icon :component="CopyOutline" />
                    </template>
                  </n-button>
                </div>
                <div class="compact-field-content">
                  {{ selectedLog.proxy_url }}
                </div>
              </div>

              <div class="compact-field" v-if="shouldShowUserAgentChange">
                <div class="user-agent-compare">
                  <div class="user-agent-compare-item">
                    <span class="compact-field-title user-agent-label">
                      {{ t("logs.userAgentBefore") }}
                    </span>
                    <span class="compact-field-content user-agent-value">
                      {{ selectedLog.user_agent }}
                    </span>
                    <n-button
                      size="tiny"
                      text
                      @click="copyContent(selectedLog.user_agent, t('logs.userAgentBefore'))"
                    >
                      <template #icon>
                        <n-icon :component="CopyOutline" />
                      </template>
                    </n-button>
                  </div>
                  <div class="user-agent-compare-item">
                    <span class="compact-field-title user-agent-label">
                      {{ t("logs.userAgentAfter") }}
                    </span>
                    <span class="compact-field-content user-agent-value">
                      {{ selectedLog.upstream_user_agent }}
                    </span>
                    <n-button
                      size="tiny"
                      text
                      @click="
                        copyContent(selectedLog.upstream_user_agent || '', t('logs.userAgentAfter'))
                      "
                    >
                      <template #icon>
                        <n-icon :component="CopyOutline" />
                      </template>
                    </n-button>
                  </div>
                </div>
              </div>

              <div class="compact-field" v-else-if="selectedUserAgent">
                <div class="compact-field-header">
                  <span class="compact-field-title">{{ t("logs.userAgent") }}</span>
                  <n-button
                    size="tiny"
                    text
                    @click="copyContent(selectedUserAgent, t('logs.userAgent'))"
                  >
                    <template #icon>
                      <n-icon :component="CopyOutline" />
                    </template>
                  </n-button>
                </div>
                <div class="compact-field-content">
                  {{ selectedUserAgent }}
                </div>
              </div>

              <div class="compact-field" v-if="selectedLog.request_body">
                <div class="compact-field-header">
                  <span class="compact-field-title">{{ t("logs.requestContent") }}</span>
                  <n-button
                    size="tiny"
                    text
                    @click="
                      copyContent(
                        formatJsonString(selectedLog.request_body),
                        t('logs.requestContent')
                      )
                    "
                  >
                    <template #icon>
                      <n-icon :component="CopyOutline" />
                    </template>
                  </n-button>
                </div>
                <div class="compact-field-content">
                  {{ formatJsonString(selectedLog.request_body) }}
                </div>
              </div>

              <div class="compact-field" v-if="selectedLog.response_body">
                <div class="compact-field-header">
                  <span class="compact-field-title">{{ t("logs.responseContent") }}</span>
                  <n-button
                    size="tiny"
                    text
                    @click="
                      copyContent(
                        formatJsonString(selectedLog.response_body),
                        t('logs.responseContent')
                      )
                    "
                  >
                    <template #icon>
                      <n-icon :component="CopyOutline" />
                    </template>
                  </n-button>
                </div>
                <div class="compact-field-content">
                  {{ formatJsonString(selectedLog.response_body) }}
                </div>
              </div>
            </div>
          </n-card>

          <!-- Error information -->
          <n-card
            v-if="selectedLog.error_message"
            :title="t('logs.errorInfo')"
            size="small"
            :header-style="{ padding: '8px 12px', fontSize: '13px' }"
          >
            <template #header-extra>
              <n-button
                size="tiny"
                text
                ghost
                @click="copyContent(selectedLog.error_message, t('logs.errorMessage'))"
              >
                <template #icon>
                  <n-icon :component="CopyOutline" />
                </template>
              </n-button>
            </template>
            <div class="compact-field compact-field-error">
              <div class="compact-field-content">
                {{ selectedLog.error_message }}
              </div>
            </div>
          </n-card>
        </n-space>
      </div>
      <template #footer>
        <n-space justify="end">
          <n-button @click="closeDetailModal">{{ t("common.close") }}</n-button>
        </n-space>
      </template>
    </n-modal>
  </div>
</template>

<style scoped>
.log-table-container {
  /* background: white; */
  /* border-radius: 8px; */
  box-shadow: 0 1px 2px rgba(0, 0, 0, 0.05);
  display: flex;
  flex-direction: column;
  /* height: 100%; */
}
.toolbar {
  background: var(--card-bg-solid);
  border-radius: 8px;
  padding: 16px;
  border-bottom: 1px solid var(--border-color);
}

.filter-section {
  display: flex;
  flex-direction: column;
  gap: 12px;
}

.filter-row {
  display: flex;
  flex-wrap: wrap;
  align-items: flex-end; /* Aligns buttons with the bottom of the filter items */
  gap: 16px;
}

.filter-grid {
  display: flex;
  flex-wrap: wrap;
  gap: 12px;
  flex: 1 1 auto; /* Let it take available space and wrap */
}

.filter-item {
  flex: 1 1 180px; /* Each item will have a base width of 180px and can grow */
  min-width: 180px; /* Prevent from becoming too narrow */
}

.filter-actions {
  display: flex;
  align-items: center;
  gap: 8px;
}

@media (max-width: 768px) {
  .pagination-container {
    flex-direction: column;
    gap: 12px;
  }

  .filter-grid {
    width: 100%;
  }

  .filter-item {
    flex: 1 1 100%;
    min-width: 100%;
  }

  .filter-actions {
    width: 100%;
    justify-content: flex-start;
    flex-wrap: wrap;
  }
}

@media (max-width: 480px) {
  .filter-actions {
    width: 100%;
  }

  .filter-actions :deep(.n-button-group) {
    display: flex;
    flex-wrap: wrap;
    width: 100%;
  }

  .filter-actions :deep(.n-button) {
    flex: 1;
    min-width: 40px;
  }
}

.table-main {
  background: var(--card-bg-solid);
  border-radius: 8px;
  overflow: hidden;
}
.table-container {
  /* background: white;
  border-radius: 8px; */
  flex: 1;
  overflow: auto;
  position: relative;
}
.empty-container {
  position: absolute;
  top: 50%;
  left: 50%;
  transform: translate(-50%, -50%);
}
.pagination-container {
  display: flex;
  justify-content: space-between;
  align-items: center;
  padding: 12px;
  border-top: 1px solid var(--border-color);
}
.pagination-info {
  display: flex;
  align-items: center;
  gap: 12px;
  font-size: 13px;
  color: var(--text-secondary);
}
.pagination-controls {
  display: flex;
  align-items: center;
  gap: 12px;
}
.page-info {
  font-size: 13px;
  color: var(--text-secondary);
}

.detail-grid {
  display: grid;
  grid-template-columns: repeat(auto-fit, minmax(220px, 1fr));
  gap: 16px;
}

.detail-item {
  display: flex;
  align-items: center;
  gap: 8px;
}

.detail-label {
  font-weight: 500;
  color: var(--text-secondary);
  min-width: 70px;
  flex-shrink: 0;
}

/* Compact layout styles */
.detail-grid-compact {
  display: grid;
  grid-template-columns: repeat(auto-fit, minmax(160px, 1fr));
  gap: 4px 10px;
  font-size: 12px;
}

.detail-item-compact {
  display: flex;
  align-items: center;
  gap: 4px;
  padding: 1px 0;
  line-height: 1.2;
}

.detail-label-compact {
  font-weight: 500;
  color: var(--text-secondary);
  min-width: 55px;
  flex-shrink: 0;
  font-size: 11px;
}

.detail-value-compact {
  font-size: 11px;
  color: var(--text-primary);
}

.key-item {
  grid-column: 1 / -1;
}

.key-display-compact {
  display: flex;
  align-items: flex-start;
  gap: 6px;
  flex: 1;
  min-width: 0;
}

.key-value-compact {
  font-family: monospace;
  font-size: 11px;
  color: var(--text-primary);
  background: var(--bg-tertiary);
  border: 1px solid var(--border-color);
  border-radius: 3px;
  padding: 4px 6px;
  flex: 1;
  min-width: 0;
  word-break: break-all;
  line-height: 1.3;
  max-height: 60px;
  overflow-y: auto;
}

.key-actions-compact {
  display: flex;
  gap: 2px;
  flex-shrink: 0;
  line-height: 24px;
  height: 24px;
}

.token-inline-compact {
  display: flex;
  align-items: center;
  flex-wrap: nowrap;
  gap: 6px;
  overflow-x: auto;
  padding-bottom: 2px;
}

.token-pill-compact {
  display: flex;
  align-items: center;
  flex: 0 0 auto;
  gap: 4px;
  min-width: 0;
  padding: 4px 8px;
  border: 1px solid var(--border-color);
  border-radius: 3px;
  background: var(--bg-tertiary);
}

.token-label-compact {
  color: var(--text-secondary);
  font-size: 11px;
  min-width: 0;
  white-space: nowrap;
}

.token-value-compact {
  color: var(--text-primary);
  font-family: monospace;
  font-size: 12px;
  font-weight: 600;
  white-space: nowrap;
}

.compact-fields {
  display: flex;
  flex-direction: column;
  gap: 6px;
}

.compact-field {
  border: 1px solid var(--border-color);
  border-radius: 3px;
  padding: 6px;
  background: var(--bg-tertiary);
}

.compact-field-error {
  border: 1px solid var(--error-border-color, #f5c6cb);
  background: var(--error-bg-color, #f8d7da);
}

.compact-field-error .compact-field-content {
  color: var(--error-text-color, #721c24);
}

/* Error field styles in dark mode */
:global(.dark) .compact-field-error {
  --error-border-color: rgba(248, 113, 113, 0.3);
  --error-bg-color: rgba(239, 68, 68, 0.1);
  --error-text-color: #fca5a5;
}

.compact-field-header {
  display: flex;
  justify-content: space-between;
  align-items: center;
  margin-bottom: 3px;
}

.compact-field-title {
  font-weight: 500;
  color: var(--text-secondary);
  font-size: 11px;
}

.compact-field-content {
  font-family: monospace;
  font-size: 10px;
  line-height: 1.3;
  word-break: break-all;
  white-space: pre-wrap;
  color: var(--text-primary);
  max-height: 100px;
  overflow-y: auto;
}

.user-agent-compare {
  display: flex;
  align-items: center;
  gap: 8px;
  overflow-x: auto;
  white-space: nowrap;
}

.user-agent-compare-item {
  display: inline-flex;
  align-items: center;
  flex: 0 0 auto;
  gap: 4px;
  min-width: 260px;
  max-width: 50%;
}

.user-agent-label {
  flex: 0 0 auto;
}

.user-agent-value {
  display: inline-block;
  flex: 1 1 auto;
  min-width: 0;
  max-height: none;
  overflow-x: auto;
  overflow-y: hidden;
  white-space: nowrap;
}

.detail-field {
  margin-bottom: 8px;
}

.field-header {
  display: flex;
  justify-content: space-between;
  align-items: center;
  margin-bottom: 8px;
}

.field-title {
  font-weight: 500;
  color: var(--text-secondary);
  font-size: 14px;
}

.field-actions {
  display: flex;
  gap: 8px;
}

.field-content {
  background: var(--bg-tertiary);
  border: 1px solid var(--border-color);
  border-radius: 6px;
  padding: 12px;
  font-family: monospace;
  font-size: 13px;
  line-height: 1.5;
  word-break: break-all;
  color: #495057;
}

.key-display {
  display: flex;
  align-items: center;
  gap: 8px;
}

.key-value {
  font-family: monospace;
  font-size: 12px;
  color: #856404;
  background: #fff3cd;
  border: 1px solid #ffeaa7;
  border-radius: 4px;
  padding: 4px 8px;
}

.key-actions {
  display: flex;
  gap: 4px;
}

.empty-content {
  text-align: center;
  color: #6c757d;
  padding: 24px;
  background: #f8f9fa;
  border-radius: 6px;
  font-style: italic;
}

.code-block {
  max-height: 400px;
  overflow-y: auto;
  border-radius: 6px;
}

.error-block {
  max-height: 200px;
}

/* Column selector styles */
.column-selector {
  min-width: 100px;
  max-height: 400px;
  overflow-y: auto;
}
</style>
