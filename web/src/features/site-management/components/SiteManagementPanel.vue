<script setup lang="ts">
/**
 * Site Management Panel Component
 *
 * Error Handling Convention:
 * Empty catch blocks with "catch (_) { ... }" rely on centralized error handling
 * in the HTTP utility (@/utils/http). The interceptor automatically displays error toasts
 * to users, so local error handling is intentionally omitted to avoid duplicate messages.
 */
import {
  siteManagementApi,
  type CheckinLogDTO,
  type ManagedSiteDTO,
  type ManagedSiteType,
  type ManagedSiteAuthType,
  type SiteImportData,
} from "@/api/site-management";
import { appState } from "@/utils/app-state";
import {
  NButton,
  NCard,
  NCheckbox,
  NDataTable,
  NDivider,
  NForm,
  NFormItem,
  NIcon,
  NInput,
  NInputNumber,
  NModal,
  NSelect,
  NSpace,
  NSwitch,
  NTag,
  NText,
  NTooltip,
  useDialog,
  useMessage,
  type DataTableColumns,
} from "naive-ui";
import { computed, h, onMounted, reactive, ref, watch } from "vue";
import { useI18n } from "vue-i18n";
import {
  OpenOutline,
  Close,
  CloudDownloadOutline,
  CloudUploadOutline,
  LogInOutline,
  Search,
  LinkOutline,
} from "@vicons/ionicons5";
import { askExportMode, askImportMode } from "@/utils/export-import";

const { t } = useI18n();
const message = useMessage();
const dialog = useDialog();

// Emit for navigation to group
interface Emits {
  (e: "navigate-to-group", groupId: number): void;
}
const emit = defineEmits<Emits>();

const loading = ref(false);
const sites = ref<ManagedSiteDTO[]>([]);
const showOnlyCheckinAvailable = ref(false);
const searchText = ref("");
const showSiteModal = ref(false);
const editingSite = ref<ManagedSiteDTO | null>(null);
const authValueInput = ref("");
const importLoading = ref(false);
const fileInputRef = ref<HTMLInputElement | null>(null);
const deleteConfirmInput = ref("");
const deleteAllConfirmInput = ref("");
const deleteAllLoading = ref(false);

const siteForm = reactive({
  name: "",
  notes: "",
  description: "",
  sort: 0,
  enabled: true,
  base_url: "",
  site_type: "unknown" as ManagedSiteType,
  user_id: "",
  checkin_page_url: "",
  checkin_available: false,
  checkin_enabled: false,
  custom_checkin_url: "",
  auth_type: "none" as ManagedSiteAuthType,
});

const showLogsModal = ref(false);
const logsLoading = ref(false);
const logs = ref<CheckinLogDTO[]>([]);
const logsSite = ref<ManagedSiteDTO | null>(null);

const siteTypeOptions = computed(() => [
  { label: t("siteManagement.siteTypeNewApi"), value: "new-api" },
  { label: t("siteManagement.siteTypeVeloera"), value: "Veloera" },
  { label: t("siteManagement.siteTypeOneHub"), value: "one-hub" },
  { label: t("siteManagement.siteTypeDoneHub"), value: "done-hub" },
  { label: t("siteManagement.siteTypeWong"), value: "wong-gongyi" },
  { label: t("siteManagement.siteTypeBrand"), value: "brand" },
  { label: t("siteManagement.siteTypeOther"), value: "unknown" },
]);
const authTypeOptions = computed(() => [
  { label: t("siteManagement.authTypeNone"), value: "none" },
  { label: t("siteManagement.authTypeAccessToken"), value: "access_token" },
]);

// Site statistics computed from local data (no extra API call needed)
const siteStats = computed(() => {
  const total = sites.value.length;
  const enabled = sites.value.filter(s => s.enabled).length;
  const disabled = total - enabled;
  const checkinEnabled = sites.value.filter(s => s.checkin_enabled).length;
  return { total, enabled, disabled, checkinEnabled };
});

// Filtered sites based on filter options and search text
const filteredSites = computed(() => {
  let result = sites.value;

  // Filter by checkin available
  if (showOnlyCheckinAvailable.value) {
    result = result.filter(s => s.checkin_available);
  }

  // Filter by search text (fuzzy search on name, base_url, notes, description)
  const query = searchText.value.trim().toLowerCase();
  if (query) {
    result = result.filter(s => {
      const name = (s.name || "").toLowerCase();
      const baseUrl = (s.base_url || "").toLowerCase();
      const notes = (s.notes || "").toLowerCase();
      const description = (s.description || "").toLowerCase();
      return (
        name.includes(query) ||
        baseUrl.includes(query) ||
        notes.includes(query) ||
        description.includes(query)
      );
    });
  }

  return result;
});

async function loadSites() {
  loading.value = true;
  try {
    sites.value = await siteManagementApi.listSites();
  } finally {
    loading.value = false;
  }
}

function resetSiteForm() {
  Object.assign(siteForm, {
    name: "",
    notes: "",
    description: "",
    sort: 0,
    enabled: true,
    base_url: "",
    site_type: "unknown",
    user_id: "",
    checkin_page_url: "",
    checkin_available: false,
    checkin_enabled: false,
    custom_checkin_url: "",
    auth_type: "none",
  });
  authValueInput.value = "";
}

function openCreateSite() {
  editingSite.value = null;
  resetSiteForm();
  showSiteModal.value = true;
}

function openEditSite(site: ManagedSiteDTO) {
  editingSite.value = site;
  Object.assign(siteForm, {
    name: site.name,
    notes: site.notes,
    description: site.description,
    sort: site.sort,
    enabled: site.enabled,
    base_url: site.base_url,
    site_type: site.site_type,
    user_id: site.user_id,
    checkin_page_url: site.checkin_page_url,
    checkin_available: site.checkin_available,
    checkin_enabled: site.checkin_enabled,
    custom_checkin_url: site.custom_checkin_url,
    auth_type: site.auth_type,
  });
  authValueInput.value = "";
  showSiteModal.value = true;
}

async function submitSite() {
  if (!siteForm.name.trim()) {
    message.warning(t("siteManagement.nameRequired"));
    return;
  }
  if (!siteForm.base_url.trim()) {
    message.warning(t("siteManagement.baseUrlRequired"));
    return;
  }
  const payload = { ...siteForm };
  try {
    if (editingSite.value) {
      const updatePayload: Record<string, unknown> = { ...payload };
      if (authValueInput.value.trim()) {
        updatePayload.auth_value = authValueInput.value;
      }
      await siteManagementApi.updateSite(editingSite.value.id, updatePayload);
      message.success(t("siteManagement.siteUpdated"));
    } else {
      await siteManagementApi.createSite({
        ...payload,
        auth_value: authValueInput.value,
      });
      message.success(t("siteManagement.siteCreated"));
    }
    showSiteModal.value = false;

    await loadSites();
  } catch (_) {
    /* handled */
  }
}

function confirmDeleteSite(site: ManagedSiteDTO) {
  // Step 1: Check if site has binding
  if (site.bound_group_id) {
    dialog.warning({
      title: t("siteManagement.deleteSite"),
      content: t("siteManagement.siteHasBinding", {
        name: site.name,
        groupName: site.bound_group_name || `#${site.bound_group_id}`,
      }),
      positiveText: t("siteManagement.mustUnbindFirst"),
      negativeText: t("common.cancel"),
      onPositiveClick: () => {
        // Navigate to the bound group for unbinding
        if (site.bound_group_id) {
          handleNavigateToGroup(site.bound_group_id);
        }
      },
    });
    return;
  }

  // Step 2: Require name input for confirmation
  deleteConfirmInput.value = "";
  dialog.create({
    title: t("siteManagement.deleteSite"),
    content: () =>
      h("div", null, [
        h("p", null, [
          t("siteManagement.dangerousDeleteWarning"),
          h(
            "strong",
            { style: { color: "#d03050", userSelect: "all", cursor: "pointer" } },
            site.name
          ),
          t("siteManagement.toConfirmDeletion"),
        ]),
        h(
          "div",
          { style: "margin-top: 12px;" },
          h(NInput, {
            value: deleteConfirmInput.value,
            "onUpdate:value": (v: string) => {
              deleteConfirmInput.value = v;
            },
            placeholder: t("siteManagement.enterSiteName"),
          })
        ),
      ]),
    positiveText: t("siteManagement.confirmDelete"),
    negativeText: t("common.cancel"),
    onPositiveClick: async () => {
      // Validate name
      if (deleteConfirmInput.value !== site.name) {
        message.error(t("siteManagement.incorrectSiteName"));
        return false;
      }
      try {
        await siteManagementApi.deleteSite(site.id);
        message.success(t("siteManagement.siteDeleted"));
        await loadSites();
      } catch (_) {
        /* handled by centralized error handler */
        return false;
      }
    },
  });
}

// Copy a site with unique name
async function copySite(site: ManagedSiteDTO) {
  try {
    await siteManagementApi.copySite(site.id);
    message.success(t("siteManagement.siteCopied"));
    await loadSites();
  } catch (_) {
    /* handled by centralized error handler */
  }
}

function statusTag(status: ManagedSiteDTO["last_checkin_status"]) {
  if (status === "success") {
    return { type: "success" as const, text: t("siteManagement.statusSuccess") };
  }
  if (status === "already_checked") {
    return { type: "info" as const, text: t("siteManagement.statusAlreadyChecked") };
  }
  if (status === "failed") {
    return { type: "error" as const, text: t("siteManagement.statusFailed") };
  }
  if (status === "skipped") {
    return { type: "warning" as const, text: t("siteManagement.statusSkipped") };
  }
  // Empty or unknown status
  return { type: "default" as const, text: t("siteManagement.statusNone") };
}

function getSiteTypeLabel(type: string) {
  return siteTypeOptions.value.find(o => o.value === type)?.label || type;
}

async function checkinSite(site: ManagedSiteDTO) {
  try {
    const res = await siteManagementApi.checkinSite(site.id);
    const statusText = statusTag(res.status).text;
    // Build message: "SiteName: Status" or "SiteName: Status - Message" if message exists
    const displayMsg = res.message?.trim()
      ? `${site.name}: ${statusText} - ${res.message}`
      : `${site.name}: ${statusText}`;
    message.info(displayMsg);

    await loadSites();
  } catch (_) {
    /* handled */
  }
}

function openSiteUrl(site: ManagedSiteDTO) {
  // Note: win.opener = null is kept as defense-in-depth for older browsers
  // that may not fully support noopener. AI suggested removing it as redundant,
  // but we prefer the extra safety with negligible overhead.
  const win = window.open(site.base_url, "_blank", "noopener,noreferrer");
  if (win) {
    win.opener = null;
  }
}

function openCheckinPage(site: ManagedSiteDTO) {
  if (site.checkin_page_url) {
    const win = window.open(site.checkin_page_url, "_blank", "noopener,noreferrer");
    if (win) {
      win.opener = null;
    }
  }
}

async function openLogs(site: ManagedSiteDTO) {
  logsSite.value = site;
  showLogsModal.value = true;
  await loadLogs();
}

async function loadLogs() {
  if (!logsSite.value) {
    return;
  }
  logsLoading.value = true;
  try {
    logs.value = await siteManagementApi.listCheckinLogs(logsSite.value.id, 100);
  } finally {
    logsLoading.value = false;
  }
}

function formatDateTime(dateStr?: string) {
  if (!dateStr) {
    return "-";
  }
  try {
    return new Date(dateStr).toLocaleString();
  } catch {
    return dateStr;
  }
}

const columns = computed<DataTableColumns<ManagedSiteDTO>>(() => [
  {
    title: "#",
    key: "sort",
    width: 45,
    align: "center",
    titleAlign: "center",
    render: row => h(NText, { depth: 3 }, () => row.sort),
  },
  {
    title: t("siteManagement.name"),
    key: "name",
    width: 140,
    titleAlign: "center",
    ellipsis: { tooltip: true },
    render: row =>
      h("div", { class: "site-name-cell" }, [
        h("div", { style: "display: flex; align-items: center; gap: 4px;" }, [
          // Show bound group icon before site name if bound to a group
          row.bound_group_id
            ? h(
                NTooltip,
                { trigger: "hover" },
                {
                  trigger: () =>
                    h(
                      NIcon,
                      {
                        size: 14,
                        color: "var(--success-color)",
                        style: "cursor: pointer; flex-shrink: 0;",
                        onClick: (e: Event) => {
                          e.stopPropagation();
                          if (row.bound_group_id) {
                            handleNavigateToGroup(row.bound_group_id);
                          }
                        },
                      },
                      () => h(LinkOutline)
                    ),
                  default: () =>
                    `${t("binding.navigateToGroup")}: ${row.bound_group_name || `#${row.bound_group_id}`}`,
                }
              )
            : null,
          h("span", { class: "site-name" }, row.name),
        ]),
        row.notes
          ? h(NText, { depth: 3, style: "font-size: 12px; display: block;" }, () => row.notes)
          : null,
      ]),
  },
  {
    title: t("siteManagement.baseUrl"),
    key: "base_url",
    minWidth: 60,
    titleAlign: "center",
    ellipsis: { tooltip: true },
    render: row =>
      h(
        "a",
        {
          href: row.base_url,
          target: "_blank",
          rel: "noopener noreferrer",
          style: "color: var(--primary-color); text-decoration: none;",
        },
        row.base_url
      ),
  },
  {
    title: t("siteManagement.siteType"),
    key: "site_type",
    width: 80,
    align: "center",
    titleAlign: "center",
    render: row =>
      h(NTag, { size: "small", bordered: false }, () => getSiteTypeLabel(row.site_type)),
  },
  {
    title: t("siteManagement.enabled"),
    key: "enabled",
    width: 50,
    align: "center",
    titleAlign: "center",
    render: row =>
      h(NTag, { size: "small", type: row.enabled ? "success" : "default" }, () =>
        row.enabled ? t("common.yes") : t("common.no")
      ),
  },
  {
    title: t("siteManagement.checkinAvailable"),
    key: "checkin_available",
    width: 70,
    align: "center",
    titleAlign: "center",
    render: row =>
      h(NTag, { size: "small", type: row.checkin_available ? "success" : "default" }, () =>
        row.checkin_available ? t("common.yes") : t("common.no")
      ),
  },
  {
    title: t("siteManagement.lastStatus"),
    key: "last_checkin_status",
    width: 80,
    align: "center",
    titleAlign: "center",
    render: row => {
      // Show "-" if check-in is not enabled for this site
      if (!row.checkin_enabled) {
        return h("span", { style: "color: #999" }, "-");
      }
      const tag = statusTag(row.last_checkin_status);
      return h(
        NTooltip,
        { trigger: "hover" },
        {
          trigger: () => h(NTag, { size: "small", type: tag.type }, () => tag.text),
          default: () => row.last_checkin_message || "-",
        }
      );
    },
  },
  {
    title: t("common.actions"),
    key: "actions",
    width: 295,
    fixed: "right",
    titleAlign: "center",
    render: row =>
      h("div", { style: "display: flex; align-items: center; gap: 4px;" }, [
        // Text action buttons first
        h(NButton, { size: "tiny", secondary: true, onClick: () => openEditSite(row) }, () =>
          t("common.edit")
        ),
        h(
          NButton,
          {
            size: "tiny",
            secondary: true,
            onClick: () => checkinSite(row),
            disabled: !row.checkin_enabled,
          },
          () => t("siteManagement.checkin")
        ),
        h(NButton, { size: "tiny", secondary: true, onClick: () => openLogs(row) }, () =>
          t("siteManagement.logs")
        ),
        h(NButton, { size: "tiny", secondary: true, onClick: () => copySite(row) }, () =>
          t("common.copy")
        ),
        h(
          NButton,
          { size: "tiny", secondary: true, type: "error", onClick: () => confirmDeleteSite(row) },
          () => t("common.delete")
        ),
        // Separator
        h("span", { style: "color: var(--n-border-color); margin: 0 4px;" }, "|"),
        // Icon buttons group (navigation) - closer together
        h(
          NTooltip,
          { trigger: "hover" },
          {
            trigger: () =>
              h(
                NButton,
                {
                  size: "tiny",
                  quaternary: true,
                  style: "padding: 0 6px;",
                  onClick: () => openSiteUrl(row),
                },
                { icon: () => h(NIcon, { size: 16 }, () => h(OpenOutline)) }
              ),
            default: () => t("siteManagement.openSite"),
          }
        ),
        // Show checkin page button only when checkin_page_url is set
        row.checkin_page_url
          ? h(
              NTooltip,
              { trigger: "hover" },
              {
                trigger: () =>
                  h(
                    NButton,
                    {
                      size: "tiny",
                      quaternary: true,
                      type: "info",
                      style: "padding: 0 6px;",
                      onClick: () => openCheckinPage(row),
                    },
                    { icon: () => h(NIcon, { size: 16 }, () => h(LogInOutline)) }
                  ),
                default: () => t("siteManagement.openCheckinPage"),
              }
            )
          : null,
      ]),
  },
]);

const logsColumns = computed<DataTableColumns<CheckinLogDTO>>(() => [
  {
    title: t("siteManagement.logTime"),
    key: "created_at",
    width: 180,
    render: row => formatDateTime(row.created_at),
  },
  {
    title: t("siteManagement.logStatus"),
    key: "status",
    width: 120,
    render: row => {
      const tag = statusTag(row.status);
      return h(NTag, { size: "small", type: tag.type }, () => tag.text);
    },
  },
  {
    title: t("siteManagement.logMessage"),
    key: "message",
    minWidth: 200,
    ellipsis: { tooltip: true },
  },
]);

// Handle export with mode selection dialog
async function handleExport() {
  try {
    const mode = await askExportMode(dialog, t);
    const blob = await siteManagementApi.exportSites(mode, true);
    const url = URL.createObjectURL(blob);
    const a = document.createElement("a");
    a.href = url;
    // Generate filename like standard-group: managed-sites_count_timestamp-mode.json
    const siteCount = sites.value.length;
    const timestamp = new Date().toISOString().replace(/[:.]/g, "-").slice(0, 19);
    const suffix = mode === "plain" ? "plain" : "enc";
    a.download = `managed-sites_${siteCount}_${timestamp}-${suffix}.json`;
    document.body.appendChild(a);
    a.click();
    document.body.removeChild(a);
    URL.revokeObjectURL(url);
    message.success(t("siteManagement.exportSuccess"));
  } catch (_) {
    /* handled */
  }
}

// Handle import with mode selection dialog
function triggerImport() {
  fileInputRef.value?.click();
}

async function handleFileChange(event: Event) {
  const input = event.target as HTMLInputElement;
  const file = input.files?.[0];
  if (!file) {
    return;
  }

  try {
    const text = await file.text();
    const data = JSON.parse(text) as SiteImportData;

    if (!data.sites || !Array.isArray(data.sites)) {
      message.error(t("siteManagement.importInvalidFormat"));
      input.value = "";
      return;
    }

    // Ask user for import mode
    const mode = await askImportMode(dialog, t);

    importLoading.value = true;
    const result = await siteManagementApi.importSites(data, mode === "auto" ? undefined : mode);
    message.success(
      t("siteManagement.importSuccess", { imported: result.imported, total: result.total })
    );
    await loadSites();
  } catch (e) {
    if (e instanceof SyntaxError) {
      message.error(t("siteManagement.importInvalidJSON"));
    }
  } finally {
    importLoading.value = false;
    input.value = "";
  }
}

// Navigate to bound group
function handleNavigateToGroup(groupId: number) {
  emit("navigate-to-group", groupId);
}

// Delete all unbound sites with confirmation
async function confirmDeleteAllUnbound() {
  // First get the count of unbound sites
  let unboundCount = 0;
  try {
    unboundCount = await siteManagementApi.getUnboundCount();
  } catch (_) {
    return;
  }

  if (unboundCount === 0) {
    message.info(t("siteManagement.noUnboundSites"));
    return;
  }

  // Show confirmation dialog with input
  deleteAllConfirmInput.value = "";
  dialog.create({
    title: t("siteManagement.confirmDeleteAllUnbound"),
    content: () =>
      h("div", null, [
        h("p", null, [
          t("siteManagement.deleteAllUnboundWarning", { count: unboundCount }),
          h(
            "strong",
            { style: { color: "#d03050", userSelect: "all", cursor: "pointer" } },
            t("siteManagement.deleteAllUnboundConfirmText")
          ),
          h("span", null, " "),
          t("siteManagement.toConfirmDeletion"),
        ]),
        h(
          "div",
          { style: "margin-top: 12px;" },
          h(NInput, {
            value: deleteAllConfirmInput.value,
            "onUpdate:value": (v: string) => {
              deleteAllConfirmInput.value = v;
            },
            placeholder: t("siteManagement.deleteAllUnboundPlaceholder"),
          })
        ),
      ]),
    positiveText: t("siteManagement.confirmDelete"),
    negativeText: t("common.cancel"),
    onPositiveClick: async () => {
      if (deleteAllConfirmInput.value !== t("siteManagement.deleteAllUnboundConfirmText")) {
        message.error(t("siteManagement.incorrectConfirmText"));
        return false;
      }
      deleteAllLoading.value = true;
      try {
        await siteManagementApi.deleteAllUnboundSites();
        await loadSites();
      } catch (_) {
        /* handled by centralized error handler */
        return false;
      } finally {
        deleteAllLoading.value = false;
      }
    },
  });
}

// Watch for site binding changes from other components (e.g., Keys page)
watch(
  () => appState.siteBindingTrigger,
  () => {
    loadSites();
  }
);

onMounted(() => {
  loadSites();
});
</script>

<template>
  <div class="site-management">
    <input
      ref="fileInputRef"
      type="file"
      accept=".json"
      style="display: none"
      @change="handleFileChange"
    />
    <n-space justify="space-between" align="center" :wrap="false" size="small">
      <n-space align="center" size="small">
        <n-text strong style="font-size: 15px">{{ t("siteManagement.title") }}</n-text>
        <n-checkbox v-model:checked="showOnlyCheckinAvailable" size="small">
          {{ t("siteManagement.filterCheckinAvailable") }}
        </n-checkbox>
      </n-space>
      <n-space size="small">
        <n-tooltip trigger="hover">
          <template #trigger>
            <n-button
              size="small"
              secondary
              type="error"
              :loading="deleteAllLoading"
              @click="confirmDeleteAllUnbound"
            >
              {{ t("siteManagement.deleteAllUnbound") }}
            </n-button>
          </template>
          {{ t("siteManagement.deleteAllUnboundTooltip") }}
        </n-tooltip>
        <n-button size="small" secondary @click="handleExport">
          <template #icon><n-icon :component="CloudDownloadOutline" /></template>
          {{ t("common.export") }}
        </n-button>
        <n-button size="small" secondary :loading="importLoading" @click="triggerImport">
          <template #icon><n-icon :component="CloudUploadOutline" /></template>
          {{ t("common.import") }}
        </n-button>
        <n-button size="small" type="primary" @click="openCreateSite">
          {{ t("common.add") }}
        </n-button>
      </n-space>
    </n-space>
    <n-divider style="margin: 8px 0" />
    <!-- Site Statistics and Search - placed above table for visibility when scrolling -->
    <div class="site-stats-row" v-if="sites.length > 0">
      <n-space :size="24" align="center" class="stats-left">
        <span class="stat-item">
          <span class="stat-label">{{ t("siteManagement.statsTotal") }}:</span>
          <span class="stat-value">{{ siteStats.total }}</span>
        </span>
        <span class="stat-item">
          <span class="stat-label">{{ t("siteManagement.statsEnabled") }}:</span>
          <span class="stat-value stat-success">{{ siteStats.enabled }}</span>
        </span>
        <span class="stat-item">
          <span class="stat-label">{{ t("siteManagement.statsDisabled") }}:</span>
          <span class="stat-value stat-warning">{{ siteStats.disabled }}</span>
        </span>
      </n-space>
      <n-input
        v-model:value="searchText"
        :placeholder="t('siteManagement.searchPlaceholder')"
        size="small"
        clearable
        class="search-input"
      >
        <template #prefix>
          <n-icon :component="Search" />
        </template>
      </n-input>
    </div>
    <!-- Table wrapper with tabindex for keyboard navigation (arrow keys scroll) -->
    <div class="site-table-wrapper" tabindex="0">
      <n-data-table
        size="small"
        :loading="loading"
        :columns="columns"
        :data="filteredSites"
        :bordered="false"
        :single-line="false"
        :max-height="'calc(100vh - 295px)'"
        :scroll-x="900"
      />
    </div>

    <!-- Site Modal -->
    <n-modal v-model:show="showSiteModal" class="site-form-modal">
      <n-card
        class="site-form-card"
        :title="editingSite ? t('common.edit') : t('common.create')"
        :bordered="false"
        size="medium"
      >
        <template #header-extra>
          <n-button quaternary circle size="small" @click="showSiteModal = false">
            <template #icon><n-icon :component="Close" /></template>
          </n-button>
        </template>
        <n-form label-placement="left" label-width="80" size="small" class="site-form">
          <div class="form-section">
            <h4 class="section-title">{{ t("siteManagement.basicInfo") }}</h4>
            <div class="form-row">
              <n-form-item :label="t('siteManagement.name')" class="form-item-half" required>
                <n-input
                  v-model:value="siteForm.name"
                  :placeholder="t('siteManagement.namePlaceholder')"
                />
              </n-form-item>
              <n-form-item :label="t('siteManagement.siteType')" class="form-item-half">
                <n-select v-model:value="siteForm.site_type" :options="siteTypeOptions" />
              </n-form-item>
            </div>
            <n-form-item :label="t('siteManagement.baseUrl')" required>
              <n-input v-model:value="siteForm.base_url" placeholder="https://example.com" />
            </n-form-item>
            <div class="form-row">
              <n-form-item :label="t('siteManagement.sort')" class="form-item-sort">
                <n-input-number v-model:value="siteForm.sort" :min="0" style="width: 100px" />
              </n-form-item>
              <n-form-item :label="t('siteManagement.enabled')" class="form-item-switch">
                <n-switch v-model:value="siteForm.enabled" />
              </n-form-item>
            </div>
            <n-form-item :label="t('siteManagement.notes')">
              <n-input
                v-model:value="siteForm.notes"
                :placeholder="t('siteManagement.notesPlaceholder')"
              />
            </n-form-item>
            <n-form-item :label="t('common.description')">
              <n-input
                v-model:value="siteForm.description"
                type="textarea"
                :autosize="{ minRows: 2, maxRows: 3 }"
              />
            </n-form-item>
          </div>
          <div class="form-section">
            <h4 class="section-title">{{ t("siteManagement.checkinSettings") }}</h4>
            <div class="form-row">
              <n-form-item :label="t('siteManagement.userId')" class="form-item-half">
                <n-input
                  v-model:value="siteForm.user_id"
                  :placeholder="t('siteManagement.userIdPlaceholder')"
                />
              </n-form-item>
              <n-form-item :label="t('siteManagement.checkinPageUrl')" class="form-item-half">
                <n-input
                  v-model:value="siteForm.checkin_page_url"
                  :placeholder="t('siteManagement.checkinPageUrlPlaceholder')"
                />
              </n-form-item>
            </div>
            <n-form-item :label="t('siteManagement.customCheckinUrl')">
              <n-input
                v-model:value="siteForm.custom_checkin_url"
                :placeholder="t('siteManagement.customCheckinUrlPlaceholder')"
              />
            </n-form-item>
            <div class="form-row form-row-switches">
              <n-form-item :label="t('siteManagement.checkinAvailable')" class="form-item-switch">
                <n-switch v-model:value="siteForm.checkin_available" />
              </n-form-item>
              <n-form-item :label="t('siteManagement.checkinEnabled')" class="form-item-switch">
                <n-switch v-model:value="siteForm.checkin_enabled" />
              </n-form-item>
            </div>
          </div>
          <div class="form-section">
            <h4 class="section-title">{{ t("siteManagement.authSettings") }}</h4>
            <div class="form-row form-row-auth">
              <n-form-item :label="t('siteManagement.authType')" class="form-item-auth-type">
                <n-select
                  v-model:value="siteForm.auth_type"
                  :options="authTypeOptions"
                  style="width: 150px"
                  placement="top-start"
                />
              </n-form-item>
              <n-form-item :label="t('siteManagement.authValue')" class="form-item-auth-value">
                <n-input
                  v-model:value="authValueInput"
                  type="password"
                  show-password-on="click"
                  :placeholder="
                    editingSite
                      ? t('siteManagement.authValueEditHint')
                      : t('siteManagement.authValuePlaceholder')
                  "
                />
              </n-form-item>
            </div>
          </div>
          <n-space justify="end" size="small" style="margin-top: 12px">
            <n-button size="small" secondary @click="showSiteModal = false">
              {{ t("common.cancel") }}
            </n-button>
            <n-button size="small" type="primary" @click="submitSite">
              {{ t("common.save") }}
            </n-button>
          </n-space>
        </n-form>
      </n-card>
    </n-modal>

    <!-- Logs Modal -->
    <n-modal v-model:show="showLogsModal" class="logs-modal">
      <n-card class="logs-card" :title="t('siteManagement.logs')" :bordered="false" size="medium">
        <template #header-extra>
          <n-button quaternary circle size="small" @click="showLogsModal = false">
            <template #icon><n-icon :component="Close" /></template>
          </n-button>
        </template>
        <n-space vertical size="small">
          <n-space justify="space-between" align="center" :wrap="false" size="small">
            <n-text strong>{{ logsSite?.name || "" }}</n-text>
            <n-button size="small" secondary :loading="logsLoading" @click="loadLogs">
              {{ t("common.refresh") }}
            </n-button>
          </n-space>
          <n-data-table
            size="small"
            :loading="logsLoading"
            :columns="logsColumns"
            :data="logs"
            :bordered="false"
            :single-line="false"
            :max-height="420"
          />
        </n-space>
      </n-card>
    </n-modal>
  </div>
</template>

<style scoped>
.site-management {
  display: flex;
  flex-direction: column;
  gap: 6px;
}
/* Table wrapper for keyboard navigation support */
.site-table-wrapper {
  outline: none;
}
/* Focus style for accessibility - subtle indicator when focused via keyboard */
.site-table-wrapper:focus-visible {
  outline: 2px solid var(--n-primary-color);
  outline-offset: 2px;
  border-radius: 4px;
}

/*
 * Force scrollbar always visible in naive-ui DataTable
 * naive-ui uses custom scrollbar component with fade effect
 * We override the scrollbar rail and bar opacity to always show
 */
/* Target the scrollbar container - always show vertical scrollbar rail */
.site-table-wrapper :deep(.n-scrollbar-rail--vertical) {
  opacity: 1 !important;
  right: 2px;
}
/* Always show the scrollbar thumb/bar */
.site-table-wrapper :deep(.n-scrollbar-rail--vertical .n-scrollbar-rail__scrollbar) {
  opacity: 1 !important;
  background-color: rgba(128, 128, 128, 0.35) !important;
  border-radius: 4px;
  width: 6px !important;
}
.site-table-wrapper :deep(.n-scrollbar-rail--vertical .n-scrollbar-rail__scrollbar:hover) {
  background-color: rgba(128, 128, 128, 0.5) !important;
}
/* Ensure scrollbar rail background is visible */
.site-table-wrapper :deep(.n-scrollbar-rail--vertical::before) {
  content: "";
  position: absolute;
  top: 0;
  right: 0;
  bottom: 0;
  width: 6px;
  background-color: rgba(128, 128, 128, 0.1);
  border-radius: 4px;
}

/* Stats row with search input */
.site-stats-row {
  display: flex;
  justify-content: space-between;
  align-items: center;
  margin-bottom: 8px;
  padding: 8px 12px;
  background: var(--n-color-embedded);
  border-radius: 6px;
  font-size: 13px;
  gap: 16px;
}
.stats-left {
  flex: 1;
  min-width: 0;
}
.search-input {
  width: 200px;
  flex-shrink: 0;
}
.stat-item {
  display: inline-flex;
  align-items: center;
  gap: 4px;
}
.stat-label {
  color: var(--n-text-color-3);
}
.stat-value {
  font-weight: 600;
  color: var(--n-text-color-1);
}
.stat-success {
  color: var(--n-success-color);
}
.stat-warning {
  color: var(--n-warning-color);
}
.stat-info {
  color: var(--n-info-color);
}
.site-name-cell {
  display: flex;
  flex-direction: column;
  gap: 2px;
}
.site-name {
  font-weight: 500;
}
.site-form-modal,
.logs-modal {
  width: 720px;
}
.site-form-card,
.logs-card {
  max-height: 85vh;
  overflow: hidden;
  display: flex;
  flex-direction: column;
}
.site-form-card :deep(.n-card__content),
.logs-card :deep(.n-card__content) {
  overflow-y: auto;
  overflow-x: hidden;
  max-height: calc(85vh - 60px);
  padding-right: 16px;
}
.site-form-card :deep(.n-card__content)::-webkit-scrollbar {
  width: 5px;
}
.site-form-card :deep(.n-card__content)::-webkit-scrollbar-thumb {
  background: rgba(0, 0, 0, 0.15);
  border-radius: 3px;
}
.form-section {
  margin-bottom: 12px;
}
.form-section:last-of-type {
  margin-bottom: 0;
}
.section-title {
  font-size: 13px;
  font-weight: 600;
  color: var(--text-primary);
  margin: 0 0 8px 0;
  padding-bottom: 4px;
  border-bottom: 1px solid var(--border-color);
}
.section-header {
  display: flex;
  justify-content: space-between;
  align-items: center;
  margin-bottom: 8px;
}
.section-header .section-title {
  margin: 0;
  padding: 0;
  border: none;
}
.form-row {
  display: flex;
  gap: 12px;
  align-items: flex-start;
}
.form-item-half {
  flex: 1;
  min-width: 0;
}
.form-item-third {
  flex: 1;
  min-width: 0;
}
.form-item-quarter {
  flex: 0 0 120px;
}
.form-item-two-thirds {
  flex: 2;
  min-width: 0;
}
.form-item-sort {
  flex: 0 0 auto;
}
.form-item-switch {
  flex: 0 0 auto;
}
.form-row-switches {
  gap: 24px;
}
.form-row-auth {
  gap: 12px;
}
.form-item-auth-type {
  flex: 0 0 auto;
}
.form-item-auth-type :deep(.n-form-item-label) {
  width: 70px !important;
  flex-shrink: 0;
}
.form-item-auth-value {
  flex: 1;
  min-width: 0;
}
.form-item-auth-value :deep(.n-form-item-label) {
  width: 70px !important;
  flex-shrink: 0;
}
.site-form :deep(.n-form-item) {
  margin-bottom: 6px !important;
  --n-feedback-height: 0 !important;
}
.site-form :deep(.n-form-item-label) {
  font-weight: 500;
  font-size: 13px;
  color: var(--text-primary);
  display: flex;
  align-items: center;
  height: 28px;
  line-height: 28px;
}
.site-form :deep(.n-input) {
  --n-border-radius: 6px;
  --n-height: 28px;
}
.site-form :deep(.n-select) {
  --n-border-radius: 6px;
}
.site-form :deep(.n-input-number) {
  --n-border-radius: 6px;
  --n-height: 28px;
}
.site-form :deep(.n-base-selection) {
  --n-height: 28px;
}
.form-label-with-tooltip {
  display: flex;
  align-items: center;
  gap: 4px;
}
.help-icon {
  color: var(--text-tertiary);
  font-size: 13px;
  cursor: help;
  transition: color 0.2s ease;
}
.help-icon:hover {
  color: var(--primary-color);
}
@media (max-width: 720px) {
  .site-form-modal,
  .logs-modal {
    width: 95vw !important;
  }
  .form-row {
    flex-direction: column;
    gap: 0;
  }
  .form-row-auth {
    flex-direction: column;
    gap: 0;
  }
  .form-item-half,
  .form-item-third,
  .form-item-quarter,
  .form-item-two-thirds,
  .form-item-sort,
  .form-item-switch,
  .form-item-auth-type,
  .form-item-auth-value {
    width: 100%;
    flex: none;
  }
  .form-item-auth-type :deep(.n-form-item-label),
  .form-item-auth-value :deep(.n-form-item-label) {
    width: 60px !important;
  }
  .form-item-auth-type :deep(.n-select) {
    width: 100% !important;
  }
}
</style>
