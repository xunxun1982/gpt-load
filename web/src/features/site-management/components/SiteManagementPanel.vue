<script setup lang="ts">
import {
  siteManagementApi,
  type AutoCheckinConfig,
  type AutoCheckinStatus,
  type CheckinLogDTO,
  type ManagedSiteDTO,
  type ManagedSiteType,
  type ManagedSiteAuthType,
  type SiteImportData,
} from "@/api/site-management";
import {
  NButton,
  NCard,
  NDataTable,
  NDescriptions,
  NDescriptionsItem,
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
import { computed, h, onMounted, onUnmounted, reactive, ref } from "vue";
import { useI18n } from "vue-i18n";
import {
  OpenOutline,
  RefreshOutline,
  SettingsOutline,
  Close,
  HelpCircleOutline,
  CloudDownloadOutline,
  CloudUploadOutline,
  LogInOutline,
} from "@vicons/ionicons5";
import { askExportMode, askImportMode } from "@/utils/export-import";

const { t } = useI18n();
const message = useMessage();
const dialog = useDialog();

const loading = ref(false);
const sites = ref<ManagedSiteDTO[]>([]);
const showSiteModal = ref(false);
const editingSite = ref<ManagedSiteDTO | null>(null);
const authValueInput = ref("");
const statusRefreshTimer = ref<number | null>(null);
const importLoading = ref(false);
const fileInputRef = ref<HTMLInputElement | null>(null);
const deleteConfirmInput = ref("");

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
  checkin_enabled: false,
  auto_checkin_enabled: false,
  custom_checkin_url: "",
  auth_type: "none" as ManagedSiteAuthType,
});

const showAutoCheckinModal = ref(false);
const autoCheckinLoading = ref(false);
const autoCheckinConfig = reactive<AutoCheckinConfig>({
  global_enabled: false,
  window_start: "09:00",
  window_end: "18:00",
  schedule_mode: "random",
  deterministic_time: "",
  retry_strategy: { enabled: false, interval_minutes: 60, max_attempts_per_day: 2 },
});
const autoCheckinStatus = ref<AutoCheckinStatus | null>(null);

const showLogsModal = ref(false);
const logsLoading = ref(false);
const logs = ref<CheckinLogDTO[]>([]);
const logsSite = ref<ManagedSiteDTO | null>(null);

const siteTypeOptions = computed(() => [
  { label: t("siteManagement.siteTypeOther"), value: "unknown" },
  { label: t("siteManagement.siteTypeVeloera"), value: "Veloera" },
  { label: t("siteManagement.siteTypeWong"), value: "wong-gongyi" },
  { label: t("siteManagement.siteTypeAnyrouter"), value: "anyrouter" },
]);
const authTypeOptions = computed(() => [
  { label: t("siteManagement.authTypeNone"), value: "none" },
  { label: t("siteManagement.authTypeAccessToken"), value: "access_token" },
  { label: t("siteManagement.authTypeCookie"), value: "cookie" },
]);
const scheduleModeOptions = computed(() => [
  { label: t("siteManagement.scheduleModeRandom"), value: "random" },
  { label: t("siteManagement.scheduleModeDeterministic"), value: "deterministic" },
]);

// Site statistics computed from local data (no extra API call needed)
const siteStats = computed(() => {
  const total = sites.value.length;
  const enabled = sites.value.filter(s => s.enabled).length;
  const disabled = total - enabled;
  const autoCheckinEnabled = sites.value.filter(s => s.auto_checkin_enabled).length;
  const checkinEnabled = sites.value.filter(s => s.checkin_enabled).length;
  return { total, enabled, disabled, autoCheckinEnabled, checkinEnabled };
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
    checkin_enabled: false,
    auto_checkin_enabled: false,
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
    checkin_enabled: site.checkin_enabled,
    auto_checkin_enabled: site.auto_checkin_enabled,
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
  const payload = {
    ...siteForm,
    checkin_enabled: siteForm.auto_checkin_enabled ? true : siteForm.checkin_enabled,
    auto_checkin_enabled: siteForm.checkin_enabled ? siteForm.auto_checkin_enabled : false,
  };
  try {
    if (editingSite.value) {
      const updatePayload: Record<string, unknown> = { ...payload };
      if (authValueInput.value.trim()) {
        updatePayload.auth_value = authValueInput.value;
      }
      await siteManagementApi.updateSite(editingSite.value.id, updatePayload);
      message.success(t("siteManagement.siteUpdated"));
    } else {
      await siteManagementApi.createSite({ ...payload, auth_value: authValueInput.value });
      message.success(t("siteManagement.siteCreated"));
    }
    showSiteModal.value = false;
    await loadSites();
  } catch (_) {
    /* handled */
  }
}

function confirmDeleteSite(site: ManagedSiteDTO) {
  dialog.warning({
    title: t("siteManagement.deleteSite"),
    content: t("siteManagement.confirmDeleteSite", { name: site.name }),
    positiveText: t("common.confirm"),
    negativeText: t("common.cancel"),
    onPositiveClick: () => {
      deleteConfirmInput.value = "";
      dialog.create({
        title: t("siteManagement.enterSiteNameToConfirm"),
        content: () =>
          h("div", null, [
            h("p", null, [
              t("siteManagement.dangerousDeleteWarning"),
              h("strong", { style: { color: "#d03050" } }, site.name),
              t("siteManagement.toConfirmDeletion"),
            ]),
            h(NInput, {
              value: deleteConfirmInput.value,
              "onUpdate:value": (v: string) => {
                deleteConfirmInput.value = v;
              },
              placeholder: t("siteManagement.enterSiteName"),
            }),
          ]),
        positiveText: t("siteManagement.confirmDelete"),
        negativeText: t("common.cancel"),
        onPositiveClick: async () => {
          if (deleteConfirmInput.value !== site.name) {
            message.error(t("siteManagement.incorrectSiteName"));
            return false;
          }
          await siteManagementApi.deleteSite(site.id);
          message.success(t("siteManagement.siteDeleted"));
          await loadSites();
        },
      });
    },
  });
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
  return { type: "default" as const, text: "-" };
}

function getSiteTypeLabel(type: string) {
  return siteTypeOptions.value.find(o => o.value === type)?.label || type;
}

async function checkinSite(site: ManagedSiteDTO) {
  try {
    const res = await siteManagementApi.checkinSite(site.id);
    message.info(
      `${site.name}: ${statusTag(res.status).text}${res.message ? ` - ${res.message}` : ""}`
    );
    await loadSites();
  } catch (_) {
    /* handled */
  }
}

function openSiteUrl(site: ManagedSiteDTO) {
  window.open(site.base_url, "_blank");
}

function openCheckinPage(site: ManagedSiteDTO) {
  if (site.checkin_page_url) {
    window.open(site.checkin_page_url, "_blank");
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

async function openAutoCheckin() {
  showAutoCheckinModal.value = true;
  await loadAutoCheckin();
}

async function loadAutoCheckin() {
  autoCheckinLoading.value = true;
  try {
    const [cfg, status] = await Promise.all([
      siteManagementApi.getAutoCheckinConfig(),
      siteManagementApi.getAutoCheckinStatus(),
    ]);
    Object.assign(autoCheckinConfig, cfg);
    autoCheckinStatus.value = status;
  } finally {
    autoCheckinLoading.value = false;
  }
}

async function saveAutoCheckinConfig() {
  try {
    await siteManagementApi.updateAutoCheckinConfig({
      ...autoCheckinConfig,
      deterministic_time: autoCheckinConfig.deterministic_time || "",
    });
    message.success(t("siteManagement.configSaved"));
    await loadAutoCheckin();
  } catch (_) {
    /* handled */
  }
}

async function runAutoCheckinNow() {
  await siteManagementApi.runAutoCheckinNow();
  message.info(t("siteManagement.autoCheckinTriggered"));
  await loadAutoCheckin();
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
    title: t("siteManagement.name"),
    key: "name",
    minWidth: 120,
    ellipsis: { tooltip: true },
    render: row =>
      h("div", { class: "site-name-cell" }, [
        h("span", { class: "site-name" }, row.name),
        row.notes
          ? h(NText, { depth: 3, style: "font-size: 12px; display: block;" }, () => row.notes)
          : null,
      ]),
  },
  {
    title: t("siteManagement.baseUrl"),
    key: "base_url",
    minWidth: 200,
    ellipsis: { tooltip: true },
    render: row =>
      h(
        "a",
        {
          href: row.base_url,
          target: "_blank",
          rel: "noreferrer",
          style: "color: var(--primary-color); text-decoration: none;",
        },
        row.base_url
      ),
  },
  {
    title: t("siteManagement.siteType"),
    key: "site_type",
    width: 100,
    render: row =>
      h(NTag, { size: "small", bordered: false }, () => getSiteTypeLabel(row.site_type)),
  },
  {
    title: t("siteManagement.enabled"),
    key: "enabled",
    width: 70,
    render: row =>
      h(NTag, { size: "small", type: row.enabled ? "success" : "default" }, () =>
        row.enabled ? t("common.yes") : t("common.no")
      ),
  },
  {
    title: t("siteManagement.autoCheckin"),
    key: "auto_checkin_enabled",
    width: 90,
    render: row =>
      h(NTag, { size: "small", type: row.auto_checkin_enabled ? "success" : "default" }, () =>
        row.auto_checkin_enabled ? t("common.yes") : t("common.no")
      ),
  },
  {
    title: t("siteManagement.lastStatus"),
    key: "last_checkin_status",
    width: 100,
    render: row => {
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
    width: 260,
    fixed: "right",
    render: row =>
      h(NSpace, { size: 4, wrap: false, align: "center" }, () => [
        // Icon buttons group (navigation)
        h(
          NTooltip,
          { trigger: "hover" },
          {
            trigger: () =>
              h(
                NButton,
                { size: "tiny", quaternary: true, onClick: () => openSiteUrl(row) },
                { icon: () => h(NIcon, null, () => h(OpenOutline)) }
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
                      onClick: () => openCheckinPage(row),
                    },
                    { icon: () => h(NIcon, null, () => h(LogInOutline)) }
                  ),
                default: () => t("siteManagement.openCheckinPage"),
              }
            )
          : null,
        // Separator
        h("span", { style: "color: var(--n-border-color); margin: 0 2px;" }, "|"),
        // Text buttons group (actions)
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
        h(
          NButton,
          { size: "tiny", secondary: true, type: "error", onClick: () => confirmDeleteSite(row) },
          () => t("common.delete")
        ),
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

onMounted(() => {
  loadSites();
  statusRefreshTimer.value = window.setInterval(() => {
    if (showAutoCheckinModal.value) {
      siteManagementApi
        .getAutoCheckinStatus()
        .then(s => {
          autoCheckinStatus.value = s;
        })
        .catch(() => {});
    }
  }, 30000);
});
onUnmounted(() => {
  if (statusRefreshTimer.value) {
    clearInterval(statusRefreshTimer.value);
  }
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
      <n-text strong style="font-size: 15px">{{ t("siteManagement.title") }}</n-text>
      <n-space size="small">
        <n-button size="small" secondary @click="handleExport">
          <template #icon><n-icon :component="CloudDownloadOutline" /></template>
          {{ t("common.export") }}
        </n-button>
        <n-button size="small" secondary :loading="importLoading" @click="triggerImport">
          <template #icon><n-icon :component="CloudUploadOutline" /></template>
          {{ t("common.import") }}
        </n-button>
        <n-button size="small" secondary @click="openAutoCheckin">
          <template #icon><n-icon :component="SettingsOutline" /></template>
          {{ t("siteManagement.autoCheckin") }}
        </n-button>
        <n-button size="small" secondary @click="loadSites">
          <template #icon><n-icon :component="RefreshOutline" /></template>
          {{ t("common.refresh") }}
        </n-button>
        <n-button size="small" type="primary" @click="openCreateSite">
          {{ t("common.add") }}
        </n-button>
      </n-space>
    </n-space>
    <n-divider style="margin: 8px 0" />
    <n-data-table
      size="small"
      :loading="loading"
      :columns="columns"
      :data="sites"
      :bordered="false"
      :single-line="false"
      :max-height="520"
      :scroll-x="900"
    />

    <!-- Site Statistics -->
    <div class="site-stats" v-if="sites.length > 0">
      <n-space :size="24" align="center">
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
        <span class="stat-item">
          <span class="stat-label">{{ t("siteManagement.statsAutoCheckin") }}:</span>
          <span class="stat-value stat-info">{{ siteStats.autoCheckinEnabled }}</span>
        </span>
      </n-space>
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
                <n-input-number v-model:value="siteForm.sort" :min="0" style="width: 80px" />
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
              <n-form-item :label="t('siteManagement.checkinEnabled')" class="form-item-switch">
                <n-switch v-model:value="siteForm.checkin_enabled" />
              </n-form-item>
              <n-form-item :label="t('siteManagement.autoCheckinEnabled')" class="form-item-switch">
                <n-switch
                  v-model:value="siteForm.auto_checkin_enabled"
                  :disabled="!siteForm.checkin_enabled"
                />
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

    <!-- Auto Check-in Modal -->
    <n-modal v-model:show="showAutoCheckinModal" class="auto-checkin-modal">
      <n-card
        class="auto-checkin-card"
        :title="t('siteManagement.autoCheckinConfig')"
        :bordered="false"
        size="medium"
      >
        <template #header-extra>
          <n-button quaternary circle size="small" @click="showAutoCheckinModal = false">
            <template #icon><n-icon :component="Close" /></template>
          </n-button>
        </template>
        <div class="auto-checkin-content">
          <div class="form-section">
            <div class="section-header">
              <h4 class="section-title">{{ t("siteManagement.status") }}</h4>
              <n-space size="small">
                <n-button
                  size="small"
                  secondary
                  :loading="autoCheckinLoading"
                  @click="loadAutoCheckin"
                >
                  {{ t("common.refresh") }}
                </n-button>
                <n-button
                  size="small"
                  type="primary"
                  :loading="autoCheckinLoading"
                  @click="runAutoCheckinNow"
                >
                  {{ t("siteManagement.runNow") }}
                </n-button>
              </n-space>
            </div>
            <n-descriptions
              v-if="autoCheckinStatus"
              size="small"
              :columns="2"
              bordered
              label-placement="left"
            >
              <n-descriptions-item :label="t('siteManagement.statusRunning')">
                <n-tag :type="autoCheckinStatus.is_running ? 'success' : 'default'" size="small">
                  {{ autoCheckinStatus.is_running ? t("common.yes") : t("common.no") }}
                </n-tag>
              </n-descriptions-item>
              <n-descriptions-item :label="t('siteManagement.statusNext')">
                {{ formatDateTime(autoCheckinStatus.next_scheduled_at) }}
              </n-descriptions-item>
              <n-descriptions-item :label="t('siteManagement.statusLastRun')">
                {{ formatDateTime(autoCheckinStatus.last_run_at) }}
              </n-descriptions-item>
              <n-descriptions-item :label="t('siteManagement.statusLastResult')">
                <n-tag
                  v-if="autoCheckinStatus.last_run_result"
                  :type="
                    autoCheckinStatus.last_run_result === 'success'
                      ? 'success'
                      : autoCheckinStatus.last_run_result === 'partial'
                        ? 'warning'
                        : 'error'
                  "
                  size="small"
                >
                  {{ autoCheckinStatus.last_run_result }}
                </n-tag>
                <span v-else>-</span>
              </n-descriptions-item>
            </n-descriptions>
            <n-descriptions
              v-if="autoCheckinStatus?.summary"
              size="small"
              :columns="3"
              bordered
              label-placement="left"
              style="margin-top: 8px"
            >
              <n-descriptions-item :label="t('siteManagement.summaryTotal')">
                {{ autoCheckinStatus.summary.total_eligible }}
              </n-descriptions-item>
              <n-descriptions-item :label="t('siteManagement.summarySuccess')">
                {{ autoCheckinStatus.summary.success_count }}
              </n-descriptions-item>
              <n-descriptions-item :label="t('siteManagement.summaryFailed')">
                {{ autoCheckinStatus.summary.failed_count }}
              </n-descriptions-item>
            </n-descriptions>
          </div>
          <div class="form-section">
            <h4 class="section-title">{{ t("siteManagement.config") }}</h4>
            <n-form
              label-placement="left"
              label-width="auto"
              size="small"
              class="auto-checkin-form"
            >
              <div class="form-row">
                <n-form-item class="form-item-half">
                  <template #label>
                    <span class="form-label-with-tooltip">
                      {{ t("siteManagement.globalEnabled") }}
                      <n-tooltip trigger="hover">
                        <template #trigger>
                          <n-icon :component="HelpCircleOutline" class="help-icon" />
                        </template>
                        {{ t("siteManagement.globalEnabledTooltip") }}
                      </n-tooltip>
                    </span>
                  </template>
                  <n-switch v-model:value="autoCheckinConfig.global_enabled" />
                </n-form-item>
                <n-form-item class="form-item-half">
                  <template #label>
                    <span class="form-label-with-tooltip">
                      {{ t("siteManagement.scheduleMode") }}
                      <n-tooltip trigger="hover">
                        <template #trigger>
                          <n-icon :component="HelpCircleOutline" class="help-icon" />
                        </template>
                        {{ t("siteManagement.scheduleModeTooltip") }}
                      </n-tooltip>
                    </span>
                  </template>
                  <n-select
                    v-model:value="autoCheckinConfig.schedule_mode"
                    :options="scheduleModeOptions"
                    style="width: 140px"
                  />
                </n-form-item>
              </div>
              <div class="form-row">
                <n-form-item class="form-item-third">
                  <template #label>
                    <span class="form-label-with-tooltip">
                      {{ t("siteManagement.windowStart") }}
                      <n-tooltip trigger="hover">
                        <template #trigger>
                          <n-icon :component="HelpCircleOutline" class="help-icon" />
                        </template>
                        {{ t("siteManagement.windowTooltip") }}
                      </n-tooltip>
                    </span>
                  </template>
                  <n-input
                    v-model:value="autoCheckinConfig.window_start"
                    placeholder="09:00"
                    style="width: 100px"
                  />
                </n-form-item>
                <n-form-item :label="t('siteManagement.windowEnd')" class="form-item-third">
                  <n-input
                    v-model:value="autoCheckinConfig.window_end"
                    placeholder="18:00"
                    style="width: 100px"
                  />
                </n-form-item>
                <n-form-item
                  v-if="autoCheckinConfig.schedule_mode === 'deterministic'"
                  class="form-item-third"
                >
                  <template #label>
                    <span class="form-label-with-tooltip">
                      {{ t("siteManagement.deterministicTime") }}
                      <n-tooltip trigger="hover">
                        <template #trigger>
                          <n-icon :component="HelpCircleOutline" class="help-icon" />
                        </template>
                        {{ t("siteManagement.deterministicTimeTooltip") }}
                      </n-tooltip>
                    </span>
                  </template>
                  <n-input
                    v-model:value="autoCheckinConfig.deterministic_time"
                    placeholder="10:30"
                    style="width: 100px"
                  />
                </n-form-item>
              </div>
            </n-form>
          </div>
          <div class="form-section">
            <h4 class="section-title">{{ t("siteManagement.retryStrategy") }}</h4>
            <n-form
              label-placement="left"
              label-width="auto"
              size="small"
              class="auto-checkin-form"
            >
              <div class="form-row">
                <n-form-item class="form-item-third">
                  <template #label>
                    <span class="form-label-with-tooltip">
                      {{ t("siteManagement.retryEnabled") }}
                      <n-tooltip trigger="hover">
                        <template #trigger>
                          <n-icon :component="HelpCircleOutline" class="help-icon" />
                        </template>
                        {{ t("siteManagement.retryEnabledTooltip") }}
                      </n-tooltip>
                    </span>
                  </template>
                  <n-switch v-model:value="autoCheckinConfig.retry_strategy.enabled" />
                </n-form-item>
                <n-form-item class="form-item-third">
                  <template #label>
                    <span class="form-label-with-tooltip">
                      {{ t("siteManagement.retryInterval") }}
                      <n-tooltip trigger="hover">
                        <template #trigger>
                          <n-icon :component="HelpCircleOutline" class="help-icon" />
                        </template>
                        {{ t("siteManagement.retryIntervalTooltip") }}
                      </n-tooltip>
                    </span>
                  </template>
                  <n-input-number
                    v-model:value="autoCheckinConfig.retry_strategy.interval_minutes"
                    :min="1"
                    :max="1440"
                    style="width: 100px"
                  />
                </n-form-item>
                <n-form-item class="form-item-third">
                  <template #label>
                    <span class="form-label-with-tooltip">
                      {{ t("siteManagement.retryMaxAttempts") }}
                      <n-tooltip trigger="hover">
                        <template #trigger>
                          <n-icon :component="HelpCircleOutline" class="help-icon" />
                        </template>
                        {{ t("siteManagement.retryMaxAttemptsTooltip") }}
                      </n-tooltip>
                    </span>
                  </template>
                  <n-input-number
                    v-model:value="autoCheckinConfig.retry_strategy.max_attempts_per_day"
                    :min="1"
                    :max="10"
                    style="width: 100px"
                  />
                </n-form-item>
              </div>
            </n-form>
          </div>
          <n-space justify="end" size="small" style="margin-top: 16px">
            <n-button size="small" secondary @click="showAutoCheckinModal = false">
              {{ t("common.close") }}
            </n-button>
            <n-button
              size="small"
              type="primary"
              :loading="autoCheckinLoading"
              @click="saveAutoCheckinConfig"
            >
              {{ t("common.save") }}
            </n-button>
          </n-space>
        </div>
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
.site-stats {
  margin-top: 12px;
  padding: 8px 12px;
  background: var(--n-color-embedded);
  border-radius: 6px;
  font-size: 13px;
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
.auto-checkin-modal,
.logs-modal {
  width: 720px;
}
.site-form-card,
.auto-checkin-card,
.logs-card {
  max-height: 85vh;
  overflow: hidden;
  display: flex;
  flex-direction: column;
}
.site-form-card :deep(.n-card__content),
.auto-checkin-card :deep(.n-card__content),
.logs-card :deep(.n-card__content) {
  overflow-y: auto;
  overflow-x: hidden;
  max-height: calc(85vh - 60px);
  padding-right: 16px;
}
.site-form-card :deep(.n-card__content)::-webkit-scrollbar,
.auto-checkin-card :deep(.n-card__content)::-webkit-scrollbar {
  width: 5px;
}
.site-form-card :deep(.n-card__content)::-webkit-scrollbar-thumb,
.auto-checkin-card :deep(.n-card__content)::-webkit-scrollbar-thumb {
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
.site-form :deep(.n-form-item),
.auto-checkin-form :deep(.n-form-item) {
  margin-bottom: 6px !important;
  --n-feedback-height: 0 !important;
}
.site-form :deep(.n-form-item-label),
.auto-checkin-form :deep(.n-form-item-label) {
  font-weight: 500;
  font-size: 13px;
  color: var(--text-primary);
  display: flex;
  align-items: center;
  height: 28px;
  line-height: 28px;
}
.site-form :deep(.n-input),
.auto-checkin-form :deep(.n-input) {
  --n-border-radius: 6px;
  --n-height: 28px;
}
.site-form :deep(.n-select),
.auto-checkin-form :deep(.n-select) {
  --n-border-radius: 6px;
}
.site-form :deep(.n-input-number),
.auto-checkin-form :deep(.n-input-number) {
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
.auto-checkin-content {
  display: flex;
  flex-direction: column;
  gap: 12px;
}
.auto-checkin-content :deep(.n-descriptions) {
  --n-th-padding: 8px 12px;
  --n-td-padding: 8px 12px;
}
@media (max-width: 720px) {
  .site-form-modal,
  .auto-checkin-modal,
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
