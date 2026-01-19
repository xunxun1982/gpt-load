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
  autoCheckinApi,
  siteManagementApi,
  type AutoCheckinConfig,
  type AutoCheckinStatus,
  type CheckinLogDTO,
  type ManagedSiteAuthType,
  type ManagedSiteBypassMethod,
  type ManagedSiteDTO,
  type ManagedSiteType,
  type SiteImportData,
  type SiteListParams,
} from "@/api/site-management";
import { appState } from "@/utils/app-state";
import { askExportMode, askImportMode } from "@/utils/export-import";
import {
  Close,
  CloudDownloadOutline,
  CloudUploadOutline,
  LinkOutline,
  LogInOutline,
  OpenOutline,
  PlayOutline,
  RefreshOutline,
  Search,
  SettingsOutline,
} from "@vicons/ionicons5";
import { debounce } from "lodash-es";
import {
  NButton,
  NCard,
  NCollapseTransition,
  NDataTable,
  NForm,
  NFormItem,
  NIcon,
  NInput,
  NInputNumber,
  NModal,
  NPagination,
  NPopover,
  NSelect,
  NSpace,
  NSwitch,
  NTag,
  NText,
  NTooltip,
  useDialog,
  useMessage,
  type DataTableColumns,
  type SelectOption,
} from "naive-ui";
import { computed, h, onMounted, onUnmounted, reactive, ref, watch } from "vue";
import { useI18n } from "vue-i18n";

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
const showSiteModal = ref(false);
const editingSite = ref<ManagedSiteDTO | null>(null);
const authValueInput = ref("");
const importLoading = ref(false);
const fileInputRef = ref<HTMLInputElement | null>(null);
const deleteConfirmInput = ref("");
const deleteAllConfirmInput = ref("");
const deleteAllLoading = ref(false);

// Balance state
const balances = ref<Record<number, string | null>>({});
const balanceLoading = ref(false);

// Auto check-in configuration state
const autoCheckinConfig = ref<AutoCheckinConfig | null>(null);
const autoCheckinStatus = ref<AutoCheckinStatus | null>(null);
const autoCheckinLoading = ref(false);
const autoCheckinRunning = ref(false);
const showAutoCheckinConfig = ref(false);

// Pagination state
const pagination = reactive({
  page: 1,
  pageSize: 20,
  total: 0,
  totalPages: 0,
});

// Filter state (use string values for naive-ui select compatibility)
const filters = reactive({
  search: "",
  enabled: "all" as string,
  checkinAvailable: "all" as string,
});

const siteForm = reactive({
  name: "",
  notes: "",
  description: "",
  sort: 0,
  enabled: true,
  base_url: "",
  site_type: "new-api" as ManagedSiteType,
  user_id: "",
  checkin_page_url: "",
  checkin_available: false,
  checkin_enabled: false,
  custom_checkin_url: "",
  use_proxy: false,
  proxy_url: "",
  bypass_method: "" as ManagedSiteBypassMethod,
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
  { label: t("siteManagement.siteTypeAnyrouter"), value: "anyrouter" },
  { label: t("siteManagement.siteTypeBrand"), value: "brand" },
  { label: t("siteManagement.siteTypeOther"), value: "unknown" },
]);
const authTypeOptions = computed(() => [
  { label: t("siteManagement.authTypeNone"), value: "none" },
  { label: t("siteManagement.authTypeAccessToken"), value: "access_token" },
  { label: t("siteManagement.authTypeCookie"), value: "cookie" },
]);

const bypassMethodOptions = computed(() => [
  { label: t("siteManagement.bypassMethodNone"), value: "" },
  { label: t("siteManagement.bypassMethodStealth"), value: "stealth" },
]);

// Filter options for enabled status (use string values for naive-ui select compatibility)
const enabledFilterOptions = computed<SelectOption[]>(() => [
  { label: t("siteManagement.filterEnabledAll"), value: "all" },
  { label: t("siteManagement.filterEnabledYes"), value: "true" },
  { label: t("siteManagement.filterEnabledNo"), value: "false" },
]);

// Filter options for checkin available
const checkinFilterOptions = computed<SelectOption[]>(() => [
  { label: t("siteManagement.filterCheckinAll"), value: "all" },
  { label: t("siteManagement.filterCheckinYes"), value: "true" },
  { label: t("siteManagement.filterCheckinNo"), value: "false" },
]);

// Current check-in day based on Beijing time (UTC+8) with 05:00 reset
// Computed once and shared across all table rows for performance
const currentCheckinDay = computed(() => {
  const now = new Date();
  const BEIJING_OFFSET_MS = 8 * 60 * 60 * 1000; // UTC+8 in milliseconds
  const CHECKIN_RESET_HOUR = 5; // Check-in day resets at 05:00 Beijing time
  const beijingTime = new Date(now.getTime() + BEIJING_OFFSET_MS);
  // If before 05:00 Beijing time, consider it as previous day
  if (beijingTime.getUTCHours() < CHECKIN_RESET_HOUR) {
    beijingTime.setUTCDate(beijingTime.getUTCDate() - 1);
  }
  return beijingTime.toISOString().slice(0, 10); // YYYY-MM-DD format
});

// Load sites with pagination
async function loadSites() {
  loading.value = true;
  try {
    const params: SiteListParams = {
      page: pagination.page,
      page_size: pagination.pageSize,
      search: filters.search || undefined,
      enabled: filters.enabled === "all" ? null : filters.enabled === "true",
      checkin_available:
        filters.checkinAvailable === "all" ? null : filters.checkinAvailable === "true",
    };

    const result = await siteManagementApi.listSitesPaginated(params);
    sites.value = result.sites;
    pagination.total = result.total;
    pagination.totalPages = result.total_pages;
  } finally {
    loading.value = false;
  }
}

// Debounced search handler
const debouncedSearch = debounce(() => {
  pagination.page = 1; // Reset to first page on search
  loadSites();
}, 300);

// Cleanup debounced search on component unmount to prevent memory leaks
onUnmounted(() => {
  debouncedSearch.cancel();
});

// Watch search input changes
watch(() => filters.search, debouncedSearch);

// Watch filter changes (immediate, no debounce)
watch(
  () => [filters.enabled, filters.checkinAvailable],
  () => {
    pagination.page = 1;
    loadSites();
  }
);

// Pagination handlers
function handlePageChange(page: number) {
  pagination.page = page;
  loadSites();
}

function handlePageSizeChange(pageSize: number) {
  pagination.pageSize = pageSize;
  pagination.page = 1;
  loadSites();
}

// Reset filters
function resetFilters() {
  filters.search = "";
  filters.enabled = "all";
  filters.checkinAvailable = "all";
  pagination.page = 1;
  loadSites();
}

function resetSiteForm() {
  Object.assign(siteForm, {
    name: "",
    notes: "",
    description: "",
    sort: 0,
    enabled: true,
    base_url: "",
    site_type: "new-api",
    user_id: "",
    checkin_page_url: "",
    checkin_available: false,
    checkin_enabled: false,
    custom_checkin_url: "",
    use_proxy: false,
    proxy_url: "",
    bypass_method: "",
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
    use_proxy: site.use_proxy,
    proxy_url: site.proxy_url,
    bypass_method: site.bypass_method,
    auth_type: site.auth_type,
  });
  authValueInput.value = "";
  showSiteModal.value = true;
}

// Known WAF/Cloudflare cookie names that indicate bypass capability.
// IMPORTANT: This list is duplicated from backend (auto_checkin_service.go).
// Keep both lists in sync when adding/removing cookie names.
const knownWAFCookieNames = [
  "cf_clearance", // Cloudflare clearance cookie (most important)
  "acw_tc", // Alibaba Cloud WAF cookie
  "cdn_sec_tc", // CDN security cookie
  "acw_sc__v2", // Alibaba Cloud WAF v2 cookie
  "__cf_bm", // Cloudflare bot management
  "_cfuvid", // Cloudflare unique visitor ID
];

// Parse cookie string into a map
function parseCookieString(cookieStr: string): Map<string, string> {
  const result = new Map<string, string>();
  for (const part of cookieStr.split(";")) {
    const trimmed = part.trim();
    if (!trimmed) {
      continue;
    }
    const idx = trimmed.indexOf("=");
    if (idx > 0) {
      const key = trimmed.substring(0, idx).trim();
      const val = trimmed.substring(idx + 1).trim();
      result.set(key, val);
    }
  }
  return result;
}

// Validate CF cookies for stealth bypass mode
// Returns list of missing cookie names if validation fails
function validateCFCookies(cookieStr: string): string[] {
  if (!cookieStr) {
    return [...knownWAFCookieNames];
  }
  const cookieMap = parseCookieString(cookieStr);
  // Check if at least one WAF cookie is present
  for (const name of knownWAFCookieNames) {
    if (cookieMap.has(name)) {
      return []; // Found at least one WAF cookie
    }
  }
  // No WAF cookies found, return all as missing (user needs at least one)
  return [...knownWAFCookieNames];
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

  // Validate stealth bypass requirements
  if (siteForm.bypass_method === "stealth") {
    // Stealth bypass requires cookie auth type
    if (siteForm.auth_type !== "cookie") {
      message.error(t("siteManagement.stealthRequiresCookieAuth"));
      return;
    }

    // Validate CF cookies for stealth mode
    const cookieValue = authValueInput.value.trim();
    const prev = editingSite.value;
    // Need cookie validation when:
    // 1. Creating new site (!prev)
    // 2. User entered new cookie value (cookieValue is not empty)
    // 3. Switching from non-cookie auth or non-stealth bypass to stealth/cookie
    const needsCookie =
      !prev || !!cookieValue || prev.auth_type !== "cookie" || prev.bypass_method !== "stealth";
    if (needsCookie) {
      if (!cookieValue) {
        message.error(t("siteManagement.stealthRequiresCookieValue"));
        return;
      }
      const missingCookies = validateCFCookies(cookieValue);
      if (missingCookies.length > 0) {
        message.error(t("siteManagement.missingCFCookies", { cookies: missingCookies.join(", ") }));
        return;
      }
    }
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
  // Step 1: Check if site has binding (many-to-one: multiple groups can bind to one site)
  const boundCount = site.bound_group_count || site.bound_groups?.length || 0;
  if (boundCount > 0) {
    const groupNames = site.bound_groups?.map(g => g.name).join(", ") || "";
    dialog.warning({
      title: t("siteManagement.deleteSite"),
      content: t("siteManagement.siteHasBindings", {
        name: site.name,
        count: boundCount,
        groupNames: groupNames || t("siteManagement.unknownGroups"),
      }),
      positiveText: t("common.ok"),
      negativeText: t("common.cancel"),
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

/**
 * Truncate notes to specified number of display characters.
 * Counts CJK characters as 1, ASCII characters as 0.5 for display width calculation.
 * Note: This implementation handles common CJK text well. For emoji support,
 * a library like string-width could be used, but it's overkill for typical site notes.
 * The tooltip always shows full content, so minor width miscalculation is acceptable.
 * @param text - The text to truncate
 * @param maxChars - Maximum number of CJK-equivalent characters to display
 * @returns Truncated text with ellipsis if needed
 */
function truncateNotes(text: string, maxChars: number): string {
  if (!text || maxChars <= 0) {
    return "";
  }
  let displayWidth = 0;
  let endIndex = 0;
  for (let i = 0; i < text.length; i++) {
    const charCode = text.charCodeAt(i);
    // CJK characters (Chinese, Japanese, Korean) and full-width characters
    const isCJK =
      (charCode >= 0x4e00 && charCode <= 0x9fff) || // CJK Unified Ideographs
      (charCode >= 0x3000 && charCode <= 0x303f) || // CJK Punctuation
      (charCode >= 0xff00 && charCode <= 0xffef) || // Full-width forms
      (charCode >= 0x3040 && charCode <= 0x309f) || // Hiragana
      (charCode >= 0x30a0 && charCode <= 0x30ff) || // Katakana
      (charCode >= 0xac00 && charCode <= 0xd7af); // Hangul Syllables (Korean)
    displayWidth += isCJK ? 1 : 0.5;
    if (displayWidth > maxChars) {
      return `${text.slice(0, endIndex)}...`;
    }
    endIndex = i + 1;
  }
  return text;
}

// Row class name for disabled sites (grayed out style)
function rowClassName(row: ManagedSiteDTO) {
  return row.enabled ? "" : "site-row-disabled";
}

// Backend message to i18n key mapping
const backendMsgMap: Record<string, string> = {
  "check-in failed": "siteManagement.backendMsg_checkInFailed",
  "check-in disabled": "siteManagement.backendMsg_checkInDisabled",
  "missing credentials": "siteManagement.backendMsg_missingCredentials",
  "missing user_id": "siteManagement.backendMsg_missingUserId",
  "unsupported auth type": "siteManagement.backendMsg_unsupportedAuthType",
  "anyrouter requires cookie auth": "siteManagement.backendMsg_anyrouterRequiresCookie",
  "cloudflare challenge, update cookies from browser":
    "siteManagement.backendMsg_cloudflareChallenge",
  "already checked in": "siteManagement.backendMsg_alreadyCheckedIn",
  "stealth bypass requires cookie auth": "siteManagement.backendMsg_stealthRequiresCookie",
};

// Translate backend check-in messages to localized text
function translateCheckinMessage(msg: string): string {
  if (!msg) {
    return "";
  }
  // Check for exact match first
  const key = backendMsgMap[msg];
  if (key) {
    return t(key);
  }
  // Check for dynamic messages (e.g., "missing cf cookies, need one of: ...")
  if (msg.startsWith("missing cf cookies")) {
    // Preserve the cookie list detail for user reference
    const detail = msg.replace(/^missing cf cookies[:,]?\s*/i, "");
    const base = t("siteManagement.backendMsg_missingCfCookies");
    return detail ? `${base}: ${detail}` : base;
  }
  return msg;
}

async function checkinSite(site: ManagedSiteDTO) {
  try {
    const res = await siteManagementApi.checkinSite(site.id);
    const statusText = statusTag(res.status).text;
    // Translate backend message if possible
    const translatedMsg = translateCheckinMessage(res.message?.trim() || "");
    // Only show message if it's different from status (avoid "签到失败 - 签到失败")
    // Also skip if message is empty
    const showMsg = translatedMsg && translatedMsg !== statusText;
    const displayMsg = showMsg
      ? `${site.name}: ${statusText} - ${translatedMsg}`
      : `${site.name}: ${statusText}`;
    message.info(displayMsg);

    await loadSites();
  } catch (_) {
    /* handled */
  }
}

// Check if site was opened today (based on Beijing time with 05:00 reset)
function isSiteOpenedToday(site: ManagedSiteDTO): boolean {
  return site.last_site_opened_date === currentCheckinDay.value;
}

// Check if check-in page was opened today (based on Beijing time with 05:00 reset)
function isCheckinPageOpenedToday(site: ManagedSiteDTO): boolean {
  return site.last_checkin_page_opened_date === currentCheckinDay.value;
}

async function openSiteUrl(site: ManagedSiteDTO) {
  // Note: win.opener = null is kept as defense-in-depth for older browsers
  // that may not fully support noopener. AI suggested removing it as redundant,
  // but we prefer the extra safety with negligible overhead.
  const win = window.open(site.base_url, "_blank", "noopener,noreferrer");
  if (win) {
    win.opener = null;
  }
  // Record the click event (fire-and-forget, don't block UI)
  try {
    await siteManagementApi.recordSiteOpened(site.id);
    // Update local state to show clicked status immediately
    site.last_site_opened_date = currentCheckinDay.value;
  } catch (_) {
    /* handled by centralized error handler */
  }
}

async function openCheckinPage(site: ManagedSiteDTO) {
  if (site.checkin_page_url) {
    const win = window.open(site.checkin_page_url, "_blank", "noopener,noreferrer");
    if (win) {
      win.opener = null;
    }
    // Record the click event (fire-and-forget, don't block UI)
    try {
      await siteManagementApi.recordCheckinPageOpened(site.id);
      // Update local state to show clicked status immediately
      site.last_checkin_page_opened_date = currentCheckinDay.value;
    } catch (_) {
      /* handled by centralized error handler */
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

// Calculate first column width based on max sort number digits
const sortColumnWidth = computed(() => {
  const maxSort = Math.max(0, ...sites.value.map(s => s.sort));
  const digits = maxSort > 0 ? Math.floor(Math.log10(maxSort)) + 1 : 1;
  // Base width 28px + 9px per digit, min 40px
  return Math.max(40, 28 + digits * 9);
});

const columns = computed<DataTableColumns<ManagedSiteDTO>>(() => [
  {
    title: "#",
    key: "sort",
    width: sortColumnWidth.value,
    align: "center",
    titleAlign: "center",
    render: row => h(NText, { depth: 3 }, () => row.sort),
  },
  {
    title: t("siteManagement.name"),
    key: "name",
    width: 140,
    titleAlign: "center",
    render: row => {
      // Get bound groups count (many-to-one relationship)
      const boundCount = row.bound_group_count || row.bound_groups?.length || 0;
      const boundGroups = row.bound_groups || [];

      // Build bound groups icon with click behavior
      const buildBoundGroupsIcon = () => {
        if (boundCount === 0) {
          return null;
        }

        // Single group: direct navigation on click with tooltip
        if (boundCount === 1) {
          const g = boundGroups[0];
          const groupId = g?.id || row.bound_group_id;
          const groupName = g?.display_name || g?.name || row.bound_group_name || `#${groupId}`;
          return h(
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
                      if (groupId) {
                        handleNavigateToGroup(groupId);
                      }
                    },
                  },
                  () => h(LinkOutline)
                ),
              default: () => `${t("binding.navigateToGroup")}: ${groupName}`,
            }
          );
        }

        // Multiple groups: show popover with clickable list
        return h(
          NPopover,
          {
            trigger: "click",
            placement: "bottom-start",
            style: { padding: "4px 0" },
          },
          {
            trigger: () =>
              h(
                "div",
                {
                  style:
                    "display: inline-flex; align-items: center; cursor: pointer; flex-shrink: 0;",
                },
                [
                  h(NIcon, { size: 14, color: "var(--success-color)" }, () => h(LinkOutline)),
                  h(
                    "span",
                    {
                      style: "font-size: 10px; color: var(--success-color); margin-left: 1px;",
                    },
                    boundCount
                  ),
                ]
              ),
            default: () =>
              h(
                "div",
                { class: "bound-groups-list" },
                boundGroups.map(g =>
                  h(
                    "div",
                    {
                      class: "bound-group-item",
                      onClick: () => {
                        if (g.id) {
                          handleNavigateToGroup(g.id);
                        }
                      },
                    },
                    [
                      h(NIcon, { size: 12, style: "margin-right: 6px;" }, () => h(LinkOutline)),
                      h(
                        "span",
                        { class: "bound-group-name" },
                        g.display_name || g.name || `#${g.id}`
                      ),
                      !g.enabled
                        ? h(
                            NTag,
                            { size: "tiny", type: "default", style: "margin-left: 6px;" },
                            () => t("common.disabled")
                          )
                        : null,
                    ]
                  )
                )
              ),
          }
        );
      };

      return h("div", { class: "site-name-row" }, [
        buildBoundGroupsIcon(),
        // Site name with tooltip on hover
        h(
          NTooltip,
          {
            trigger: "hover",
            placement: "top-end",
            style: { maxWidth: "300px" },
          },
          {
            trigger: () => h("span", { class: "site-name-text" }, row.name),
            default: () => row.name,
          }
        ),
      ]);
    },
  },
  {
    title: t("siteManagement.notes"),
    key: "notes",
    width: 90,
    align: "center",
    titleAlign: "center",
    render: row => {
      if (!row.notes) {
        return h("span", { style: "color: var(--n-text-color-disabled)" }, "-");
      }
      // Truncate to 4 Chinese characters (approximately 8 chars for mixed content)
      const truncated = truncateNotes(row.notes, 4);
      // Always show tooltip with full notes content
      return h(
        NTooltip,
        {
          trigger: "hover",
          placement: "top-end",
          style: { maxWidth: "300px" },
        },
        {
          trigger: () => h("span", { class: "site-notes-cell" }, truncated),
          default: () => row.notes,
        }
      );
    },
  },
  {
    title: t("siteManagement.baseUrl"),
    key: "base_url",
    // minWidth reduced to make room for notes column; column auto-expands as needed
    // Single line with ellipsis and tooltip on hover for full URL
    minWidth: 40,
    titleAlign: "center",
    render: row =>
      h(
        NTooltip,
        {
          trigger: "hover",
          placement: "top-end",
          style: { maxWidth: "400px" },
        },
        {
          trigger: () =>
            h(
              "a",
              {
                href: row.base_url,
                target: "_blank",
                rel: "noopener noreferrer",
                class: "site-url-cell",
              },
              row.base_url
            ),
          default: () => row.base_url,
        }
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
    title: t("siteManagement.balance"),
    key: "balance",
    width: 75,
    align: "center",
    titleAlign: "center",
    render: row => {
      // Show "-" for unsupported site types
      if (!supportsBalance(row.site_type)) {
        return h("span", { style: "color: #999" }, "-");
      }
      const balanceDisplay = getBalanceDisplay(row);
      // Clickable balance cell with tooltip showing full balance and click hint
      return h(
        NTooltip,
        { trigger: "hover" },
        {
          trigger: () =>
            h(
              "span",
              {
                class: "balance-cell",
                onClick: () => fetchSiteBalance(row.id),
              },
              balanceDisplay
            ),
          default: () =>
            balanceDisplay === "-"
              ? t("siteManagement.balanceTooltip")
              : `${balanceDisplay} (${t("siteManagement.balanceTooltip")})`,
        }
      );
    },
  },
  {
    title: t("siteManagement.enabled"),
    key: "enabled",
    width: 60,
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
      // Only show status if it's from the current check-in day, otherwise show "-"
      // Check-in day resets at 05:00 Beijing time (UTC+8), not midnight
      // This prevents stale "success" status from showing after the reset time
      if (!row.last_checkin_date || row.last_checkin_date !== currentCheckinDay.value) {
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
        // "Open Site" button - shows success type if clicked today
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
                  type: isSiteOpenedToday(row) ? "success" : "default",
                  style: "padding: 0 6px;",
                  onClick: () => openSiteUrl(row),
                },
                { icon: () => h(NIcon, { size: 16 }, () => h(OpenOutline)) }
              ),
            default: () =>
              isSiteOpenedToday(row)
                ? t("siteManagement.openSiteVisited")
                : t("siteManagement.openSite"),
          }
        ),
        // Show checkin page button only when checkin_page_url is set
        // Shows success type if clicked today
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
                      type: isCheckinPageOpenedToday(row) ? "success" : "info",
                      style: "padding: 0 6px;",
                      onClick: () => openCheckinPage(row),
                    },
                    { icon: () => h(NIcon, { size: 16 }, () => h(LogInOutline)) }
                  ),
                default: () =>
                  isCheckinPageOpenedToday(row)
                    ? t("siteManagement.openCheckinPageVisited")
                    : t("siteManagement.openCheckinPage"),
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
    render: row => translateCheckinMessage(row.message || ""),
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
    const siteCount = pagination.total;
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

// Balance functions
// Fetch balance for a single site
async function fetchSiteBalance(siteId: number) {
  try {
    const result = await siteManagementApi.fetchSiteBalance(siteId);
    balances.value[siteId] = result.balance;
    // Also update the site in the list to reflect the new balance
    const site = sites.value.find(s => s.id === siteId);
    if (site && result.balance) {
      site.last_balance = result.balance;
    }
  } catch (_) {
    // Keep existing value or set to null on error
  }
}

// Refresh balances for all enabled sites (manual trigger only)
async function refreshAllBalances() {
  balanceLoading.value = true;
  message.info(t("siteManagement.refreshingBalance"));
  try {
    const results = await siteManagementApi.refreshAllBalances();
    // Update balances map with results
    for (const [siteIdStr, info] of Object.entries(results)) {
      const siteId = parseInt(siteIdStr, 10);
      if (!isNaN(siteId)) {
        balances.value[siteId] = info.balance;
        // Also update the site in the list
        const site = sites.value.find(s => s.id === siteId);
        if (site && info.balance) {
          site.last_balance = info.balance;
        }
      }
    }
    message.success(t("siteManagement.balanceRefreshed"));
  } catch (_) {
    // Error handled by centralized error handler
  } finally {
    balanceLoading.value = false;
  }
}

// Get display balance for a site
// Priority: 1. Local state (from manual refresh) 2. Database cache (last_balance)
function getBalanceDisplay(site: ManagedSiteDTO): string {
  // Check local state first (updated by manual refresh)
  const localBalance = balances.value[site.id];
  if (localBalance !== undefined && localBalance !== null) {
    return localBalance;
  }
  // Fall back to database cached balance
  if (site.last_balance && site.last_balance !== "") {
    return site.last_balance;
  }
  return "-";
}

// Check if site type supports balance fetching
function supportsBalance(siteType: string): boolean {
  return ["new-api", "Veloera", "one-hub", "done-hub", "wong-gongyi"].includes(siteType);
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

// Auto check-in configuration functions
async function loadAutoCheckinConfig() {
  autoCheckinLoading.value = true;
  try {
    const [config, status] = await Promise.all([
      autoCheckinApi.getConfig(),
      autoCheckinApi.getStatus(),
    ]);
    autoCheckinConfig.value = config;
    autoCheckinStatus.value = status;
  } catch (_) {
    /* handled by centralized error handler */
  } finally {
    autoCheckinLoading.value = false;
  }
}

async function saveAutoCheckinConfig() {
  if (!autoCheckinConfig.value) {
    return;
  }
  autoCheckinLoading.value = true;
  try {
    const updated = await autoCheckinApi.updateConfig(autoCheckinConfig.value);
    autoCheckinConfig.value = updated;
    message.success(t("common.saveSuccess"));
  } catch (_) {
    /* handled by centralized error handler */
  } finally {
    autoCheckinLoading.value = false;
  }
}

async function runAutoCheckinNow() {
  autoCheckinRunning.value = true;
  try {
    await autoCheckinApi.runNow();
    message.success(t("siteManagement.autoCheckinTriggered"));
    // Refresh status and site list after a short delay to allow backend processing
    setTimeout(async () => {
      await Promise.all([loadAutoCheckinConfig(), loadSites()]);
    }, 2000);
  } catch (_) {
    /* handled by centralized error handler */
  } finally {
    autoCheckinRunning.value = false;
  }
}

// Schedule mode options
const scheduleModeOptions = computed<SelectOption[]>(() => [
  { label: t("siteManagement.scheduleModeMultiple"), value: "multiple" },
  { label: t("siteManagement.scheduleModeRandom"), value: "random" },
  { label: t("siteManagement.scheduleModeDeterministic"), value: "deterministic" },
]);

// New schedule time input
const newScheduleTime = ref("");

// Add a new schedule time
function addScheduleTime() {
  if (!autoCheckinConfig.value || !newScheduleTime.value) {
    return;
  }
  const time = newScheduleTime.value.trim();
  // Validate HH:MM format
  if (!/^([01]?[0-9]|2[0-3]):[0-5][0-9]$/.test(time)) {
    message.warning(t("siteManagement.invalidTimeFormat"));
    return;
  }
  // Normalize to HH:MM format
  const parts = time.split(":");
  const hours = parts[0] ?? "00";
  const mins = parts[1] ?? "00";
  const normalized = `${hours.padStart(2, "0")}:${mins}`;
  // Check for duplicates
  if (autoCheckinConfig.value.schedule_times.includes(normalized)) {
    message.warning(t("siteManagement.duplicateTime"));
    return;
  }
  autoCheckinConfig.value.schedule_times.push(normalized);
  // Sort times
  autoCheckinConfig.value.schedule_times.sort();
  newScheduleTime.value = "";
}

// Remove a schedule time
function removeScheduleTime(index: number) {
  if (!autoCheckinConfig.value) {
    return;
  }
  autoCheckinConfig.value.schedule_times.splice(index, 1);
}

// Format next scheduled time for display (convert UTC to Beijing time)
const nextScheduledDisplay = computed(() => {
  if (!autoCheckinStatus.value?.next_scheduled_at) {
    return "";
  }
  try {
    const utcDate = new Date(autoCheckinStatus.value.next_scheduled_at);
    // Convert to Beijing time (UTC+8)
    return utcDate.toLocaleString("zh-CN", { timeZone: "Asia/Shanghai" });
  } catch {
    return autoCheckinStatus.value.next_scheduled_at;
  }
});

// Format last run time for display
const lastRunDisplay = computed(() => {
  if (!autoCheckinStatus.value?.last_run_at) {
    return "";
  }
  try {
    const utcDate = new Date(autoCheckinStatus.value.last_run_at);
    return utcDate.toLocaleString("zh-CN", { timeZone: "Asia/Shanghai" });
  } catch {
    return autoCheckinStatus.value.last_run_at;
  }
});

onMounted(() => {
  loadSites();
  loadAutoCheckinConfig();
  // Balance is loaded from database cache (last_balance field) via loadSites()
  // Manual refresh button is available for users to update balances on demand
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
      </n-space>
      <n-space size="small">
        <!-- Auto check-in config button -->
        <n-tooltip trigger="hover">
          <template #trigger>
            <n-button
              size="small"
              secondary
              @click="showAutoCheckinConfig = !showAutoCheckinConfig"
            >
              <template #icon><n-icon :component="SettingsOutline" /></template>
              {{ t("siteManagement.autoCheckin") }}
            </n-button>
          </template>
          {{ t("siteManagement.autoCheckinConfigTooltip") }}
        </n-tooltip>
        <!-- Refresh balance button -->
        <n-tooltip trigger="hover">
          <template #trigger>
            <n-button size="small" secondary :loading="balanceLoading" @click="refreshAllBalances">
              <template #icon><n-icon :component="RefreshOutline" /></template>
              {{ t("siteManagement.refreshBalance") }}
            </n-button>
          </template>
          {{ t("siteManagement.refreshBalanceTooltip") }}
        </n-tooltip>
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

    <!-- Auto Check-in Configuration Panel -->
    <n-collapse-transition :show="showAutoCheckinConfig">
      <div class="auto-checkin-panel">
        <!-- Header row with title and action buttons -->
        <div class="auto-checkin-header">
          <n-space align="center" size="small">
            <n-text strong style="font-size: 13px">
              {{ t("siteManagement.autoCheckinConfig") }}
            </n-text>
            <n-tag :type="autoCheckinStatus?.is_running ? 'warning' : 'default'" size="tiny">
              {{
                autoCheckinStatus?.is_running
                  ? t("siteManagement.statusRunning")
                  : t("siteManagement.statusIdle")
              }}
            </n-tag>
            <n-text v-if="nextScheduledDisplay" depth="3" style="font-size: 11px">
              {{ t("siteManagement.nextScheduled") }}: {{ nextScheduledDisplay }}
            </n-text>
          </n-space>
          <n-space size="small">
            <n-button
              size="tiny"
              type="primary"
              :loading="autoCheckinRunning"
              @click="runAutoCheckinNow"
            >
              <template #icon><n-icon :component="PlayOutline" size="12" /></template>
              {{ t("siteManagement.runNow") }}
            </n-button>
            <n-button
              size="tiny"
              quaternary
              :loading="autoCheckinLoading"
              @click="loadAutoCheckinConfig"
            >
              <template #icon><n-icon :component="RefreshOutline" size="12" /></template>
            </n-button>
          </n-space>
        </div>

        <!-- Config form - single row layout -->
        <div v-if="autoCheckinConfig" class="auto-checkin-config-row">
          <!-- Global enabled -->
          <span class="config-inline-item">
            <n-text depth="3" class="config-label">{{ t("siteManagement.globalEnabled") }}</n-text>
            <n-switch v-model:value="autoCheckinConfig.global_enabled" size="small" />
          </span>

          <!-- Schedule mode -->
          <span class="config-inline-item">
            <n-text depth="3" class="config-label">{{ t("siteManagement.scheduleMode") }}</n-text>
            <n-select
              v-model:value="autoCheckinConfig.schedule_mode"
              :options="scheduleModeOptions"
              size="tiny"
              style="width: 100px"
              :consistent-menu-width="false"
            />
          </span>

          <!-- Multiple times mode -->
          <span v-if="autoCheckinConfig.schedule_mode === 'multiple'" class="config-inline-item">
            <n-text depth="3" class="config-label">{{ t("siteManagement.scheduleTimes") }}</n-text>
            <n-space size="small" align="center" :wrap="false">
              <n-tag
                v-for="(time, index) in autoCheckinConfig.schedule_times"
                :key="index"
                closable
                size="tiny"
                @close="removeScheduleTime(index)"
              >
                {{ time }}
              </n-tag>
              <n-input
                v-model:value="newScheduleTime"
                placeholder="HH:MM"
                size="tiny"
                style="width: 60px"
                @keyup.enter="addScheduleTime"
              />
              <n-button size="tiny" quaternary @click="addScheduleTime">
                {{ t("common.add") }}
              </n-button>
            </n-space>
          </span>

          <!-- Random mode -->
          <template v-if="autoCheckinConfig.schedule_mode === 'random'">
            <span class="config-inline-item">
              <n-text depth="3" class="config-label">{{ t("siteManagement.windowStart") }}</n-text>
              <n-input
                v-model:value="autoCheckinConfig.window_start"
                placeholder="09:00"
                size="tiny"
                style="width: 60px"
              />
            </span>
            <span class="config-inline-item">
              <n-text depth="3" class="config-label">{{ t("siteManagement.windowEnd") }}</n-text>
              <n-input
                v-model:value="autoCheckinConfig.window_end"
                placeholder="18:00"
                size="tiny"
                style="width: 60px"
              />
            </span>
          </template>

          <!-- Deterministic mode -->
          <span
            v-if="autoCheckinConfig.schedule_mode === 'deterministic'"
            class="config-inline-item"
          >
            <n-text depth="3" class="config-label">
              {{ t("siteManagement.deterministicTime") }}
            </n-text>
            <n-input
              v-model:value="autoCheckinConfig.deterministic_time"
              placeholder="10:00"
              size="tiny"
              style="width: 60px"
            />
          </span>

          <!-- Retry settings -->
          <span class="config-inline-item">
            <n-text depth="3" class="config-label">{{ t("siteManagement.retryEnabled") }}</n-text>
            <n-switch v-model:value="autoCheckinConfig.retry_strategy.enabled" size="small" />
          </span>

          <template v-if="autoCheckinConfig.retry_strategy.enabled">
            <span class="config-inline-item">
              <n-text depth="3" class="config-label">
                {{ t("siteManagement.retryInterval") }}
              </n-text>
              <n-input-number
                v-model:value="autoCheckinConfig.retry_strategy.interval_minutes"
                :min="1"
                :max="1440"
                size="tiny"
                style="width: 70px"
              />
              <n-text depth="3" style="font-size: 11px; margin-left: 2px">
                {{ t("siteManagement.minutes") }}
              </n-text>
            </span>
            <span class="config-inline-item">
              <n-text depth="3" class="config-label">
                {{ t("siteManagement.retryMaxAttempts") }}
              </n-text>
              <n-input-number
                v-model:value="autoCheckinConfig.retry_strategy.max_attempts_per_day"
                :min="1"
                :max="10"
                size="tiny"
                style="width: 70px"
              />
            </span>
          </template>

          <!-- Save button and note -->
          <span class="config-inline-item config-save">
            <n-text depth="3" style="font-size: 10px">
              {{ t("siteManagement.beijingTimeNote") }}
            </n-text>
            <n-button
              size="tiny"
              type="primary"
              :loading="autoCheckinLoading"
              @click="saveAutoCheckinConfig"
            >
              {{ t("common.save") }}
            </n-button>
          </span>
        </div>

        <!-- Last run summary -->
        <div v-if="autoCheckinStatus?.summary" class="auto-checkin-summary">
          <n-text depth="3" style="font-size: 11px">
            {{ t("siteManagement.lastRun") }}: {{ lastRunDisplay }}
          </n-text>
          <n-tag
            size="tiny"
            :type="autoCheckinStatus.summary.failed_count > 0 ? 'error' : 'success'"
          >
            {{
              t("siteManagement.checkinSummary", {
                success: autoCheckinStatus.summary.success_count,
                failed: autoCheckinStatus.summary.failed_count,
                skipped: autoCheckinStatus.summary.skipped_count,
              })
            }}
          </n-tag>
        </div>
      </div>
    </n-collapse-transition>
    <!-- Filter row with search, filters and stats -->
    <div class="filter-row">
      <n-space align="center" :size="6" class="filter-left">
        <!-- Search input -->
        <n-input
          v-model:value="filters.search"
          :placeholder="t('siteManagement.searchPlaceholder')"
          size="tiny"
          clearable
          style="width: 150px"
        >
          <template #prefix>
            <n-icon :component="Search" size="14" />
          </template>
        </n-input>

        <!-- Enabled filter with label -->
        <span class="filter-item">
          <n-text depth="3" class="filter-label">
            {{ t("siteManagement.filterEnabledLabel") }}
          </n-text>
          <n-select
            v-model:value="filters.enabled"
            size="tiny"
            style="width: 72px"
            :options="enabledFilterOptions"
            :consistent-menu-width="false"
          />
        </span>

        <!-- Checkin available filter with label -->
        <span class="filter-item">
          <n-text depth="3" class="filter-label">
            {{ t("siteManagement.filterCheckinLabel") }}
          </n-text>
          <n-select
            v-model:value="filters.checkinAvailable"
            size="tiny"
            style="width: 72px"
            :options="checkinFilterOptions"
            :consistent-menu-width="false"
          />
        </span>

        <!-- Reset button -->
        <n-button size="tiny" quaternary @click="resetFilters">
          <template #icon><n-icon :component="RefreshOutline" size="14" /></template>
        </n-button>
      </n-space>

      <!-- Stats -->
      <n-text depth="3" style="font-size: 12px">
        {{ t("siteManagement.totalCount", { count: pagination.total }) }}
      </n-text>
    </div>
    <!-- Table wrapper with tabindex for keyboard navigation (arrow keys scroll) -->
    <div class="site-table-wrapper" tabindex="0">
      <n-data-table
        size="small"
        :loading="loading"
        :columns="columns"
        :data="sites"
        :bordered="false"
        :single-line="false"
        :max-height="'calc(100vh - 280px)'"
        :scroll-x="900"
        :row-class-name="rowClassName"
      />
    </div>

    <!-- Pagination -->
    <div class="pagination-wrapper">
      <n-pagination
        v-model:page="pagination.page"
        v-model:page-size="pagination.pageSize"
        :item-count="pagination.total"
        :page-sizes="[20, 50, 100]"
        size="small"
        show-size-picker
        show-quick-jumper
        :prefix="({ itemCount }) => t('siteManagement.paginationPrefix', { total: itemCount })"
        @update:page="handlePageChange"
        @update:page-size="handlePageSizeChange"
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
              <n-form-item :label="t('siteManagement.autoCheckinEnabled')" class="form-item-switch">
                <n-switch v-model:value="siteForm.checkin_enabled" />
              </n-form-item>
              <n-form-item :label="t('siteManagement.useProxy')" class="form-item-switch">
                <n-switch v-model:value="siteForm.use_proxy" />
              </n-form-item>
            </div>
            <n-form-item v-if="siteForm.use_proxy" :label="t('siteManagement.proxyUrl')">
              <n-input
                v-model:value="siteForm.proxy_url"
                :placeholder="t('siteManagement.proxyUrlPlaceholder')"
              />
            </n-form-item>
            <n-form-item :label="t('siteManagement.bypassMethod')">
              <n-select
                v-model:value="siteForm.bypass_method"
                :options="bypassMethodOptions"
                style="width: 200px"
              />
            </n-form-item>
            <!-- Stealth bypass hint -->
            <n-text
              v-if="siteForm.bypass_method === 'stealth'"
              depth="3"
              style="
                font-size: 12px;
                display: block;
                margin-top: -4px;
                margin-bottom: 8px;
                color: #f0a020;
              "
            >
              {{ t("siteManagement.stealthBypassHint") }}
            </n-text>
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
                    siteForm.auth_type === 'cookie'
                      ? t('siteManagement.authTypeCookiePlaceholder')
                      : editingSite
                        ? t('siteManagement.authValueEditHint')
                        : t('siteManagement.authValuePlaceholder')
                  "
                />
              </n-form-item>
            </div>
            <!-- Cookie auth hint -->
            <n-text
              v-if="siteForm.auth_type === 'cookie'"
              depth="3"
              style="font-size: 12px; display: block; margin-top: -4px; margin-bottom: 8px"
            >
              {{ t("siteManagement.authTypeCookieHint") }}
            </n-text>
            <!-- Stealth cookie requirement hint -->
            <n-text
              v-if="siteForm.bypass_method === 'stealth' && siteForm.auth_type === 'cookie'"
              depth="3"
              style="
                font-size: 12px;
                display: block;
                margin-top: -4px;
                margin-bottom: 8px;
                color: #18a058;
              "
            >
              {{ t("siteManagement.stealthCookieHint") }}
            </n-text>
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
  gap: 2px;
}

/* Compact table row height */
.site-management :deep(.n-data-table-td) {
  padding: 4px 8px !important;
}
.site-management :deep(.n-data-table-th) {
  padding: 6px 8px !important;
}

/* Disabled site row style - grayed out appearance */
.site-management :deep(.site-row-disabled) {
  opacity: 0.5;
  background-color: var(--n-color-hover) !important;
}
.site-management :deep(.site-row-disabled:hover) {
  opacity: 0.65;
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

/* Auto check-in panel styles - matching filter-row style */
.auto-checkin-panel {
  margin: 2px 0;
  padding: 4px 6px;
  background: var(--n-color-embedded);
  border-radius: 4px;
  font-size: 12px;
}
.auto-checkin-header {
  display: flex;
  justify-content: space-between;
  align-items: center;
  margin-bottom: 4px;
}
.auto-checkin-config-row {
  display: flex;
  flex-wrap: wrap;
  align-items: center;
  gap: 8px;
}
.config-inline-item {
  display: inline-flex;
  align-items: center;
  gap: 4px;
}
.config-label {
  font-size: 12px;
  white-space: nowrap;
}
.config-save {
  margin-left: auto;
}
.auto-checkin-summary {
  display: flex;
  align-items: center;
  gap: 8px;
  margin-top: 4px;
  padding-top: 4px;
  border-top: 1px dashed var(--n-border-color);
}

/* Stats row with search input */
.filter-row {
  display: flex;
  justify-content: space-between;
  align-items: center;
  margin: 2px 0;
  padding: 3px 6px;
  background: var(--n-color-embedded);
  border-radius: 4px;
  font-size: 12px;
  gap: 8px;
}
.filter-left {
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
.pagination-wrapper {
  display: flex;
  justify-content: flex-end;
  margin-top: 2px;
  padding: 2px 0;
}
.site-name-row {
  display: flex;
  align-items: center;
  gap: 4px;
  min-width: 0;
  overflow: hidden;
}
.site-name-text {
  flex: 1;
  min-width: 0;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
  font-weight: 500;
}
.site-notes-cell {
  display: inline-block;
  max-width: 100%;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
  font-size: 13px;
  color: var(--n-text-color-2);
}
.site-url-cell {
  display: block;
  max-width: 100%;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
  color: var(--primary-color);
  text-decoration: none;
}
.balance-cell {
  display: block;
  max-width: 100%;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
  cursor: pointer;
  color: var(--n-text-color);
}

/* Force single line display for table cells rendered via h() function */
.site-management :deep(.site-name-row) {
  display: flex;
  align-items: center;
  gap: 4px;
  min-width: 0;
  overflow: hidden;
}
.site-management :deep(.site-name-text) {
  flex: 1;
  min-width: 0;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
  font-weight: 500;
}
.site-management :deep(.site-url-cell) {
  display: block;
  max-width: 100%;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
  color: var(--primary-color);
  text-decoration: none;
}
.site-management :deep(.balance-cell) {
  display: block;
  max-width: 100%;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
  cursor: pointer;
  color: var(--n-text-color);
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

<!-- Global styles for popover content (teleported outside scoped context) -->
<style>
.bound-groups-list {
  min-width: 140px;
  max-width: 280px;
}
.bound-group-item {
  display: flex;
  align-items: center;
  padding: 6px 12px;
  cursor: pointer;
  transition: background-color 0.15s ease;
  border-radius: 4px;
  margin: 2px 4px;
}
.bound-group-item:hover {
  background-color: var(--n-color-hover, rgba(0, 0, 0, 0.05));
}
.bound-group-name {
  flex: 1;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
  font-size: 13px;
}
</style>
