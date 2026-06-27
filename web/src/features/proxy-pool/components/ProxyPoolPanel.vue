<script setup lang="ts">
import { proxyPoolApi, type ProxyPoolPayload, type ProxyPoolTestResult } from "@/api/proxy-pool";
import { settingsApi, type SettingsUpdatePayload } from "@/api/settings";
import type { ProxyPoolItem, ProxyPoolSelectionOption } from "@/types/models";
import {
  Add,
  CheckmarkCircleOutline,
  Close,
  CreateOutline,
  RefreshOutline,
  SettingsOutline,
  TrashOutline,
} from "@vicons/ionicons5";
import {
  NButton,
  NCard,
  NDataTable,
  NForm,
  NFormItem,
  NIcon,
  NInput,
  NInputNumber,
  NModal,
  NSelect,
  NSpace,
  NTag,
  NText,
  NTooltip,
  useDialog,
  useMessage,
  type DataTableColumns,
  type FormRules,
} from "naive-ui";
import { computed, h, onMounted, reactive, ref } from "vue";
import { useI18n } from "vue-i18n";

const { t } = useI18n();
const message = useMessage();
const dialog = useDialog();
const defaultProxyPoolTestTargetURL = "https://www.gstatic.com/generate_204";
const defaultProxyTestTimeoutSeconds = 10;
const defaultProxyAutoTestIntervalMinutes = 60;
const defaultGatewayProxyTestTimeoutSeconds = 10;
const defaultGatewayProxyAutoTestIntervalMinutes = 60;
const proxyBatchTestConcurrency = 5;

const loading = ref(false);
const saving = ref(false);
const settingsLoading = ref(false);
const settingsSaving = ref(false);
const showModal = ref(false);
const showSettingsModal = ref(false);
const editingItem = ref<ProxyPoolItem | null>(null);
const items = ref<ProxyPoolItem[]>([]);
const gatewayOptions = ref<ProxyPoolSelectionOption[]>([]);
const currentPage = ref(1);
const pageSize = ref(12);
const gatewayCurrentPage = ref(1);
const gatewayPageSize = ref(6);
const total = ref(0);
const hasMore = ref(false);
const testingAll = ref(false);
const batchTesting = ref(false);
const testingIds = ref<number[]>([]);
const testResults = reactive<Record<number, ProxyPoolTestResult>>({});
const gatewayTestResults = reactive<Record<string, ProxyPoolTestResult>>({});

const proxyPoolSettingsForm = reactive<{
  targetUrl: string;
  timeoutSeconds: number | null;
  intervalMinutes: number | null;
  gatewayTimeoutSeconds: number | null;
  gatewayIntervalMinutes: number | null;
}>({
  targetUrl: defaultProxyPoolTestTargetURL,
  timeoutSeconds: defaultProxyTestTimeoutSeconds,
  intervalMinutes: defaultProxyAutoTestIntervalMinutes,
  gatewayTimeoutSeconds: defaultGatewayProxyTestTimeoutSeconds,
  gatewayIntervalMinutes: defaultGatewayProxyAutoTestIntervalMinutes,
});

const proxyPoolSettingsApplied = reactive({
  targetUrl: defaultProxyPoolTestTargetURL,
  timeoutSeconds: defaultProxyTestTimeoutSeconds,
  intervalMinutes: defaultProxyAutoTestIntervalMinutes,
  gatewayTimeoutSeconds: defaultGatewayProxyTestTimeoutSeconds,
  gatewayIntervalMinutes: defaultGatewayProxyAutoTestIntervalMinutes,
});

const form = reactive<ProxyPoolPayload>({
  name: "",
  url: "",
});

const rules: FormRules = {
  name: [{ required: true, message: t("proxyPool.nameRequired"), trigger: ["blur", "input"] }],
  url: [
    { required: true, message: t("proxyPool.urlRequired"), trigger: ["blur", "input"] },
    {
      trigger: ["blur", "input"],
      validator: (_rule, value: string) => {
        const normalized = value?.trim();
        if (!normalized) {
          return true;
        }
        if (/\s/.test(normalized)) {
          return new Error(t("proxyPool.invalidUrl"));
        }
        try {
          const parsed = new URL(normalized);
          return ["http:", "https:", "socks5:"].includes(parsed.protocol) && !!parsed.hostname
            ? true
            : new Error(t("proxyPool.invalidUrl"));
        } catch {
          return new Error(t("proxyPool.invalidUrl"));
        }
      },
    },
  ],
};

const formRef = ref();

const columns = computed<DataTableColumns<ProxyPoolItem>>(() => [
  {
    title: t("proxyPool.name"),
    key: "name",
    minWidth: 140,
  },
  {
    title: t("proxyPool.url"),
    key: "url",
    minWidth: 260,
    ellipsis: { tooltip: true },
  },
  {
    title: t("proxyPool.testStatus"),
    key: "test_status",
    minWidth: 180,
    render(row) {
      const result = testResults[row.id];
      if (isTesting(row.id)) {
        return renderTestStatus(
          h(NTag, { size: "small", type: "info", bordered: false }, () => t("proxyPool.testing"))
        );
      }
      if (!result) {
        return renderTestStatus(h(NText, { depth: 3 }, () => t("proxyPool.notTested")));
      }
      const tag = h(
        NTag,
        { size: "small", type: result.success ? "success" : "error", bordered: false },
        () =>
          result.success
            ? t("proxyPool.testPassedShort", { duration: result.duration_ms })
            : t("proxyPool.testFailedShort")
      );
      if (result.success || !result.error) {
        return renderTestStatus(tag);
      }
      return renderTestStatus(
        h(
          NTooltip,
          { trigger: "hover" },
          {
            trigger: () => tag,
            default: () => t("proxyPool.testFailed", { error: result.error }),
          }
        )
      );
    },
  },
  {
    title: t("proxyPool.country"),
    key: "country",
    minWidth: 120,
    render(row) {
      const result = testResults[row.id];
      return result?.country_name || result?.country_code || "-";
    },
  },
  {
    title: t("common.actions"),
    key: "actions",
    width: 160,
    render(row) {
      return h(
        NSpace,
        { size: 4, justify: "end" },
        {
          default: () => [
            h(
              NTooltip,
              { trigger: "hover" },
              {
                trigger: () =>
                  h(
                    NButton,
                    {
                      quaternary: true,
                      circle: true,
                      size: "small",
                      loading: isTesting(row.id),
                      disabled: batchTesting.value && !isTesting(row.id),
                      "aria-label": `${t("proxyPool.test")}: ${row.name}`,
                      title: `${t("proxyPool.test")}: ${row.name}`,
                      onClick: () => testItem(row),
                    },
                    { icon: () => h(NIcon, null, { default: () => h(CheckmarkCircleOutline) }) }
                  ),
                default: () => t("proxyPool.test"),
              }
            ),
            h(
              NButton,
              {
                quaternary: true,
                circle: true,
                size: "small",
                "aria-label": t("common.edit"),
                title: t("common.edit"),
                onClick: () => openEdit(row),
              },
              { icon: () => h(NIcon, null, { default: () => h(CreateOutline) }) }
            ),
            h(
              NButton,
              {
                quaternary: true,
                circle: true,
                size: "small",
                type: "error",
                "aria-label": t("common.delete"),
                title: t("common.delete"),
                onClick: () => confirmDelete(row),
              },
              { icon: () => h(NIcon, null, { default: () => h(TrashOutline) }) }
            ),
          ],
        }
      );
    },
  },
]);

const gatewayColumns = computed<DataTableColumns<ProxyPoolSelectionOption>>(() => [
  {
    title: t("proxyPool.name"),
    key: "label",
    minWidth: 160,
    render(row) {
      return h(
        "div",
        { class: "gateway-name-cell" },
        [
          h("span", row.label),
          row.active
            ? h(NTag, { size: "small", type: "success", bordered: false }, () =>
                t("proxyPool.activeGateway")
              )
            : null,
        ].filter(Boolean)
      );
    },
  },
  {
    title: t("proxyPool.url"),
    key: "url",
    minWidth: 260,
    ellipsis: { tooltip: true },
  },
  {
    title: t("proxyPool.testStatus"),
    key: "test_status",
    minWidth: 180,
    render(row) {
      const key = gatewayResultKey(row);
      const result = gatewayTestResults[key];
      if (isTestingGateway(key)) {
        return renderTestStatus(
          h(NTag, { size: "small", type: "info", bordered: false }, () => t("proxyPool.testing"))
        );
      }
      if (!result) {
        return renderTestStatus(h(NText, { depth: 3 }, () => t("proxyPool.notTested")));
      }
      const tag = h(
        NTag,
        { size: "small", type: result.success ? "success" : "error", bordered: false },
        () =>
          result.success
            ? t("proxyPool.testPassedShort", { duration: result.duration_ms })
            : t("proxyPool.testFailedShort")
      );
      if (result.success || !result.error) {
        return renderTestStatus(tag);
      }
      return renderTestStatus(
        h(
          NTooltip,
          { trigger: "hover" },
          {
            trigger: () => tag,
            default: () => t("proxyPool.gatewayTestFailed", { error: result.error }),
          }
        )
      );
    },
  },
  {
    title: t("common.actions"),
    key: "actions",
    width: 96,
    render(row) {
      const key = gatewayResultKey(row);
      return h(
        NTooltip,
        { trigger: "hover" },
        {
          trigger: () =>
            h(
              NButton,
              {
                quaternary: true,
                circle: true,
                size: "small",
                loading: isTestingGateway(key),
                "aria-label": `${t("proxyPool.testGateway")}: ${row.label}`,
                title: `${t("proxyPool.testGateway")}: ${row.label}`,
                onClick: () => testGateway(row),
              },
              { icon: () => h(NIcon, null, { default: () => h(CheckmarkCircleOutline) }) }
            ),
          default: () => t("proxyPool.testGateway"),
        }
      );
    },
  },
]);

const testTimeoutText = computed(() =>
  t("proxyPool.testTimeoutValue", { seconds: proxyPoolSettingsApplied.timeoutSeconds })
);
const gatewayTestTimeoutText = computed(() =>
  t("proxyPool.testTimeoutValue", { seconds: proxyPoolSettingsApplied.gatewayTimeoutSeconds })
);

const activeGatewayOption = computed(() => gatewayOptions.value.find(item => item.active));

const activeGatewayText = computed(() => {
  const option = activeGatewayOption.value;
  if (!option) {
    return t("proxyPool.noGatewayAvailable");
  }
  return t("proxyPool.activeGatewayValue", {
    label: option.label,
    url: option.url || option.value,
  });
});
const autoTestIntervalText = computed(() => {
  const minutes = proxyPoolSettingsApplied.intervalMinutes;
  if (minutes % 60 === 0) {
    return t("proxyPool.autoTestIntervalHours", { hours: minutes / 60 });
  }
  return t("proxyPool.autoTestIntervalMinutes", { minutes });
});
const gatewayAutoTestIntervalText = computed(() => {
  const minutes = proxyPoolSettingsApplied.gatewayIntervalMinutes;
  if (minutes % 60 === 0) {
    return t("proxyPool.autoTestIntervalHours", { hours: minutes / 60 });
  }
  return t("proxyPool.autoTestIntervalMinutes", { minutes });
});
const totalPages = computed(() => {
  if (total.value < 0) {
    return -1;
  }
  return Math.max(1, Math.ceil(total.value / pageSize.value));
});
const totalRecordsText = computed(() => {
  if (total.value < 0) {
    return t("proxyPool.calculatingTotal");
  }
  return t("proxyPool.totalRecords", { total: total.value });
});
const pageInfoText = computed(() => {
  if (totalPages.value < 0) {
    return t("proxyPool.pageInfoUnknown", { current: currentPage.value });
  }
  return t("proxyPool.pageInfo", { current: currentPage.value, total: totalPages.value });
});
const isNextPageDisabled = computed(() => {
  if (totalPages.value > 0 && currentPage.value >= totalPages.value) {
    return true;
  }
  if (totalPages.value < 0) {
    return !hasMore.value;
  }
  return items.value.length === 0;
});
const gatewayTotal = computed(() => gatewayOptions.value.length);
const gatewayTotalPages = computed(() =>
  Math.max(1, Math.ceil(gatewayTotal.value / gatewayPageSize.value))
);
const pagedGatewayOptions = computed(() => {
  const start = (gatewayCurrentPage.value - 1) * gatewayPageSize.value;
  return gatewayOptions.value.slice(start, start + gatewayPageSize.value);
});
const gatewayTotalRecordsText = computed(() =>
  t("proxyPool.totalRecords", { total: gatewayTotal.value })
);
const gatewayPageInfoText = computed(() =>
  t("proxyPool.pageInfo", { current: gatewayCurrentPage.value, total: gatewayTotalPages.value })
);
function renderTestStatus(content: ReturnType<typeof h>) {
  return h("div", { class: "proxy-pool-test-status" }, [content]);
}

function normalizeGatewayPage() {
  if (gatewayCurrentPage.value > gatewayTotalPages.value) {
    gatewayCurrentPage.value = gatewayTotalPages.value;
  }
}

async function loadItems() {
  loading.value = true;
  try {
    const [resultState, gatewayState] = await Promise.allSettled([
      proxyPoolApi.listPage({
        page: currentPage.value,
        page_size: pageSize.value,
      }),
      proxyPoolApi.listGatewayOptions(),
    ]);

    if (gatewayState.status === "fulfilled") {
      gatewayOptions.value = gatewayState.value;
      syncGatewayTestResults(gatewayState.value);
      normalizeGatewayPage();
    } else {
      gatewayOptions.value = [];
      gatewayCurrentPage.value = 1;
      console.error("Failed to load gateway proxy options:", gatewayState.reason);
    }

    if (resultState.status === "rejected") {
      throw resultState.reason;
    }

    const result = resultState.value;
    items.value = result.items;
    total.value = result.pagination.total_items;
    hasMore.value =
      result.pagination.has_more ??
      (result.pagination.total_items < 0 && items.value.length >= pageSize.value);
  } catch (error) {
    hasMore.value = false;
    message.error(t("proxyPool.loadFailed"));
    throw error;
  } finally {
    loading.value = false;
  }
}

function syncGatewayTestResults(options: ProxyPoolSelectionOption[]) {
  const next: Record<string, ProxyPoolTestResult> = {};
  for (const item of options) {
    if (item.test_result) {
      next[gatewayResultKey(item)] = item.test_result;
    }
  }
  for (const key of Object.keys(gatewayTestResults)) {
    if (!Object.prototype.hasOwnProperty.call(next, key)) {
      delete gatewayTestResults[key];
    }
  }
  Object.assign(gatewayTestResults, next);
}

async function onRefreshClick() {
  try {
    await loadItems();
  } catch {
    // loadItems already shows the failure toast; consume the click-path rejection.
  }
}

async function changePage(page: number) {
  if (page < 1 || page === currentPage.value) {
    return;
  }
  const previousPage = currentPage.value;
  currentPage.value = page;
  try {
    await loadItems();
  } catch {
    currentPage.value = previousPage;
  }
}

async function changePageSize(size: number) {
  const nextPageSize = positiveIntegerValue(size, pageSize.value);
  if (nextPageSize === pageSize.value) {
    return;
  }
  const previousPage = currentPage.value;
  const previousPageSize = pageSize.value;
  pageSize.value = nextPageSize;
  currentPage.value = 1;
  try {
    await loadItems();
  } catch {
    pageSize.value = previousPageSize;
    currentPage.value = previousPage;
  }
}

function changeGatewayPage(page: number) {
  if (page < 1 || page > gatewayTotalPages.value || page === gatewayCurrentPage.value) {
    return;
  }
  gatewayCurrentPage.value = page;
}

function changeGatewayPageSize(size: number) {
  const nextPageSize = positiveIntegerValue(size, gatewayPageSize.value);
  if (nextPageSize === gatewayPageSize.value) {
    return;
  }
  gatewayPageSize.value = nextPageSize;
  gatewayCurrentPage.value = 1;
  normalizeGatewayPage();
}

function positiveIntegerValue(value: unknown, fallback: number): number {
  const parsed = Number(value);
  return Number.isFinite(parsed) && parsed >= 1 ? Math.trunc(parsed) : fallback;
}

function applyProxyPoolSettings(
  targetUrl: string,
  timeoutSeconds: number,
  intervalMinutes: number,
  gatewayTimeoutSeconds: number,
  gatewayIntervalMinutes: number
) {
  proxyPoolSettingsApplied.targetUrl = targetUrl;
  proxyPoolSettingsApplied.timeoutSeconds = timeoutSeconds;
  proxyPoolSettingsApplied.intervalMinutes = intervalMinutes;
  proxyPoolSettingsApplied.gatewayTimeoutSeconds = gatewayTimeoutSeconds;
  proxyPoolSettingsApplied.gatewayIntervalMinutes = gatewayIntervalMinutes;
  proxyPoolSettingsForm.targetUrl = targetUrl;
  proxyPoolSettingsForm.timeoutSeconds = timeoutSeconds;
  proxyPoolSettingsForm.intervalMinutes = intervalMinutes;
  proxyPoolSettingsForm.gatewayTimeoutSeconds = gatewayTimeoutSeconds;
  proxyPoolSettingsForm.gatewayIntervalMinutes = gatewayIntervalMinutes;
}

function validTestTargetURL(value: string): boolean {
  if (/\s/.test(value)) {
    return false;
  }
  try {
    const parsed = new URL(value);
    return ["http:", "https:"].includes(parsed.protocol) && !!parsed.hostname;
  } catch {
    return false;
  }
}

async function loadProxyPoolSettings(): Promise<boolean> {
  settingsLoading.value = true;
  try {
    const settings = await settingsApi.getProxyPoolSettings();
    const targetUrl =
      String(settings.proxy_pool_test_target_url || defaultProxyPoolTestTargetURL).trim() ||
      defaultProxyPoolTestTargetURL;
    const timeoutSeconds = positiveIntegerValue(
      settings.proxy_pool_test_timeout_seconds,
      defaultProxyTestTimeoutSeconds
    );
    const intervalMinutes = positiveIntegerValue(
      settings.proxy_pool_auto_test_interval_minutes,
      defaultProxyAutoTestIntervalMinutes
    );
    const gatewayTimeoutSeconds = positiveIntegerValue(
      settings.gateway_proxy_test_timeout_seconds,
      defaultGatewayProxyTestTimeoutSeconds
    );
    const gatewayIntervalMinutes = positiveIntegerValue(
      settings.gateway_proxy_auto_test_interval_minutes,
      defaultGatewayProxyAutoTestIntervalMinutes
    );
    applyProxyPoolSettings(
      targetUrl,
      timeoutSeconds,
      intervalMinutes,
      gatewayTimeoutSeconds,
      gatewayIntervalMinutes
    );
    return true;
  } catch {
    message.error(t("proxyPool.settingsLoadFailed"));
    return false;
  } finally {
    settingsLoading.value = false;
  }
}

async function saveProxyPoolSettings() {
  const targetUrl = proxyPoolSettingsForm.targetUrl.trim();
  if (!validTestTargetURL(targetUrl)) {
    message.error(t("proxyPool.invalidTestTarget"));
    return;
  }
  const timeoutSeconds = positiveIntegerValue(
    proxyPoolSettingsForm.timeoutSeconds,
    defaultProxyTestTimeoutSeconds
  );
  const intervalMinutes = positiveIntegerValue(
    proxyPoolSettingsForm.intervalMinutes,
    defaultProxyAutoTestIntervalMinutes
  );
  const gatewayTimeoutSeconds = positiveIntegerValue(
    proxyPoolSettingsForm.gatewayTimeoutSeconds,
    defaultGatewayProxyTestTimeoutSeconds
  );
  const gatewayIntervalMinutes = positiveIntegerValue(
    proxyPoolSettingsForm.gatewayIntervalMinutes,
    defaultGatewayProxyAutoTestIntervalMinutes
  );

  settingsSaving.value = true;
  try {
    const payload: SettingsUpdatePayload = {
      proxy_pool_test_target_url: targetUrl,
      proxy_pool_test_timeout_seconds: timeoutSeconds,
      proxy_pool_auto_test_interval_minutes: intervalMinutes,
      gateway_proxy_test_timeout_seconds: gatewayTimeoutSeconds,
      gateway_proxy_auto_test_interval_minutes: gatewayIntervalMinutes,
    };
    await settingsApi.updateSettings(payload);
    applyProxyPoolSettings(
      targetUrl,
      timeoutSeconds,
      intervalMinutes,
      gatewayTimeoutSeconds,
      gatewayIntervalMinutes
    );
    showSettingsModal.value = false;
    message.success(t("proxyPool.settingsSaved"));
  } catch {
    message.error(t("proxyPool.settingsSaveFailed"));
  } finally {
    settingsSaving.value = false;
  }
}

function isTesting(id: number): boolean {
  return testingIds.value.includes(id);
}

function gatewayResultKey(item: ProxyPoolSelectionOption): string {
  return item.candidate_id || `${item.value}:${item.url || ""}`;
}

function isTestingGateway(key: string): boolean {
  return testingIds.value.includes(-Math.abs(hashGatewayKey(key)));
}

function hashGatewayKey(key: string): number {
  let hash = 0;
  for (let i = 0; i < key.length; i += 1) {
    hash = (hash * 31 + key.charCodeAt(i)) | 0;
  }
  return hash || 1;
}

function setTesting(id: number, testing: boolean) {
  if (testing) {
    if (!testingIds.value.includes(id)) {
      testingIds.value = [...testingIds.value, id];
    }
    return;
  }
  testingIds.value = testingIds.value.filter(itemID => itemID !== id);
}

async function testGateway(item: ProxyPoolSelectionOption) {
  const key = gatewayResultKey(item);
  const testID = -Math.abs(hashGatewayKey(key));
  if (isTestingGateway(key)) {
    return;
  }
  setTesting(testID, true);
  try {
    const result = await proxyPoolApi.testGateway(gatewayResultKey(item));
    gatewayTestResults[key] = { ...result, url: item.url || result.url };
    if (result.success) {
      await loadItems().catch(() => undefined);
      message.success(t("proxyPool.gatewayTestSuccess", { duration: result.duration_ms }));
    } else {
      message.error(
        t("proxyPool.gatewayTestFailed", { error: result.error || t("proxyPool.unknownError") })
      );
    }
  } catch {
    gatewayTestResults[key] = {
      success: false,
      url: item.url || "",
      target_url: "",
      timeout_ms: proxyPoolSettingsApplied.gatewayTimeoutSeconds * 1000,
      duration_ms: 0,
      error: t("proxyPool.testRequestFailed"),
    };
    message.error(t("proxyPool.testRequestFailed"));
  } finally {
    setTesting(testID, false);
  }
}

async function testItem(item: ProxyPoolItem, silent = false) {
  if (isTesting(item.id)) {
    return;
  }
  setTesting(item.id, true);
  try {
    const result = await proxyPoolApi.test(item.id);
    testResults[item.id] = result;
    if (!silent) {
      if (result.success) {
        message.success(t("proxyPool.testSuccess", { duration: result.duration_ms }));
      } else {
        message.error(
          t("proxyPool.testFailed", { error: result.error || t("proxyPool.unknownError") })
        );
      }
    }
  } catch {
    testResults[item.id] = {
      success: false,
      url: item.url,
      target_url: "",
      timeout_ms: proxyPoolSettingsApplied.timeoutSeconds * 1000,
      duration_ms: 0,
      error: t("proxyPool.testRequestFailed"),
    };
    if (!silent) {
      message.error(t("proxyPool.testRequestFailed"));
    }
  } finally {
    setTesting(item.id, false);
  }
}

async function testAll(silent = false) {
  if (batchTesting.value || items.value.length === 0) {
    return;
  }
  batchTesting.value = true;
  testingAll.value = true;
  try {
    const testItems = [...items.value];
    for (let i = 0; i < testItems.length; i += proxyBatchTestConcurrency) {
      const batch = testItems.slice(i, i + proxyBatchTestConcurrency);
      await Promise.all(batch.map(item => testItem(item, true)));
    }
    if (!silent) {
      message.success(t("proxyPool.testAllFinished"));
    }
  } finally {
    testingAll.value = false;
    batchTesting.value = false;
  }
}

function resetForm() {
  editingItem.value = null;
  form.name = "";
  form.url = "";
}

function openCreate() {
  resetForm();
  showModal.value = true;
}

async function openSettings() {
  if (await loadProxyPoolSettings()) {
    showSettingsModal.value = true;
  }
}

function openEdit(item: ProxyPoolItem) {
  editingItem.value = item;
  form.name = item.name;
  form.url = item.url;
  showModal.value = true;
}

async function submit() {
  await formRef.value?.validate();
  saving.value = true;
  const creating = !editingItem.value;
  try {
    const payload = { name: form.name.trim(), url: form.url.trim() };
    if (editingItem.value) {
      await proxyPoolApi.update(editingItem.value.id, payload);
    } else {
      await proxyPoolApi.create(payload);
    }
  } catch (error) {
    console.error("Failed to save proxy pool item:", error);
    message.error(t("common.operationFailed"));
    return;
  } finally {
    saving.value = false;
  }
  showModal.value = false;
  resetForm();
  if (creating) {
    currentPage.value = 1;
  }
  await loadItems();
}

function confirmDelete(item: ProxyPoolItem) {
  dialog.warning({
    title: t("common.confirm"),
    content: t("proxyPool.deleteConfirm", { name: item.name }),
    positiveText: t("common.delete"),
    negativeText: t("common.cancel"),
    onPositiveClick: async () => {
      const previousPage = currentPage.value;
      const hadTestResult = Object.prototype.hasOwnProperty.call(testResults, item.id);
      const previousTestResult = testResults[item.id];
      let deleted = false;

      try {
        await proxyPoolApi.delete(item.id);
        deleted = true;
        delete testResults[item.id];
        if (items.value.length === 1 && currentPage.value > 1) {
          currentPage.value -= 1;
        }
        await loadItems();
      } catch {
        if (!deleted) {
          currentPage.value = previousPage;
          if (hadTestResult && previousTestResult !== undefined) {
            testResults[item.id] = previousTestResult;
          }
          message.error(t("common.operationFailed"));
        }
      }
    },
  });
}

onMounted(() => {
  void loadProxyPoolSettings();
  void loadItems().catch(() => undefined);
});
</script>

<template>
  <div class="proxy-pool-panel">
    <div class="proxy-pool-toolbar">
      <div class="proxy-pool-meta">
        <span>{{ t("proxyPool.testTarget") }}: {{ proxyPoolSettingsApplied.targetUrl }}</span>
        <span>
          {{ t("proxyPool.proxyTestSettings") }}: {{ testTimeoutText }} / {{ autoTestIntervalText }}
        </span>
        <span>
          {{ t("proxyPool.gatewayTestSettings") }}: {{ gatewayTestTimeoutText }} /
          {{ gatewayAutoTestIntervalText }}
        </span>
      </div>
      <n-space align="center">
        <n-button type="primary" size="small" @click="openCreate">
          <template #icon>
            <n-icon :component="Add" />
          </template>
          {{ t("proxyPool.add") }}
        </n-button>
        <n-button size="small" :loading="settingsLoading" @click="openSettings">
          <template #icon>
            <n-icon :component="SettingsOutline" />
          </template>
          {{ t("proxyPool.testSettings") }}
        </n-button>
        <n-button size="small" :loading="loading" @click="onRefreshClick">
          <template #icon>
            <n-icon :component="RefreshOutline" />
          </template>
          {{ t("common.refresh") }}
        </n-button>
        <n-button
          size="small"
          :loading="testingAll"
          :disabled="items.length === 0"
          @click="testAll(false)"
        >
          <template #icon>
            <n-icon :component="CheckmarkCircleOutline" />
          </template>
          {{ t("proxyPool.testCurrentPage") }}
        </n-button>
      </n-space>
    </div>

    <div class="proxy-pool-content-stack">
      <section class="proxy-pool-section proxy-pool-manual-section">
        <div class="proxy-pool-section-header">
          <div>
            <div class="proxy-pool-section-title">{{ t("proxyPool.manualProxies") }}</div>
            <div class="proxy-pool-section-subtitle">{{ t("proxyPool.manualSectionHint") }}</div>
          </div>
        </div>
        <n-data-table
          size="small"
          :columns="columns"
          :data="items"
          :loading="loading"
          :bordered="false"
          :single-line="false"
          :row-key="row => row.id"
        />

        <div class="proxy-pool-pagination">
          <div class="proxy-pool-pagination-info">
            <span>{{ totalRecordsText }}</span>
            <n-select
              :value="pageSize"
              :options="[
                { label: t('proxyPool.recordsPerPage', { count: 12 }), value: 12 },
                { label: t('proxyPool.recordsPerPage', { count: 24 }), value: 24 },
                { label: t('proxyPool.recordsPerPage', { count: 60 }), value: 60 },
                { label: t('proxyPool.recordsPerPage', { count: 120 }), value: 120 },
              ]"
              size="small"
              style="width: 108px"
              @update:value="changePageSize"
            />
          </div>
          <div class="proxy-pool-pagination-controls">
            <n-button
              size="small"
              :disabled="currentPage <= 1"
              @click="changePage(currentPage - 1)"
            >
              {{ t("common.previousPage") }}
            </n-button>
            <span class="proxy-pool-page-info">{{ pageInfoText }}</span>
            <n-button
              size="small"
              :disabled="isNextPageDisabled"
              @click="changePage(currentPage + 1)"
            >
              {{ t("common.nextPage") }}
            </n-button>
          </div>
        </div>
      </section>

      <section class="proxy-pool-section proxy-pool-gateway-section">
        <div class="proxy-pool-section-header">
          <div>
            <div class="proxy-pool-section-title">{{ t("proxyPool.gatewayProxies") }}</div>
            <div class="proxy-pool-section-subtitle">{{ t("proxyPool.gatewaySectionHint") }}</div>
            <div class="gateway-active-summary">{{ activeGatewayText }}</div>
          </div>
        </div>
        <n-data-table
          size="small"
          :columns="gatewayColumns"
          :data="pagedGatewayOptions"
          :loading="loading"
          :bordered="false"
          :single-line="false"
          :row-key="gatewayResultKey"
        />

        <div class="proxy-pool-gateway-pagination">
          <div class="proxy-pool-pagination-info">
            <span>{{ gatewayTotalRecordsText }}</span>
            <n-select
              :value="gatewayPageSize"
              :options="[
                { label: t('proxyPool.recordsPerPage', { count: 6 }), value: 6 },
                { label: t('proxyPool.recordsPerPage', { count: 12 }), value: 12 },
                { label: t('proxyPool.recordsPerPage', { count: 24 }), value: 24 },
                { label: t('proxyPool.recordsPerPage', { count: 60 }), value: 60 },
              ]"
              size="small"
              style="width: 108px"
              @update:value="changeGatewayPageSize"
            />
          </div>
          <div class="proxy-pool-pagination-controls">
            <n-button
              size="small"
              :disabled="gatewayCurrentPage <= 1"
              @click="changeGatewayPage(gatewayCurrentPage - 1)"
            >
              {{ t("common.previousPage") }}
            </n-button>
            <span class="proxy-pool-page-info">{{ gatewayPageInfoText }}</span>
            <n-button
              size="small"
              :disabled="gatewayCurrentPage >= gatewayTotalPages"
              @click="changeGatewayPage(gatewayCurrentPage + 1)"
            >
              {{ t("common.nextPage") }}
            </n-button>
          </div>
        </div>
      </section>
    </div>

    <n-modal v-model:show="showSettingsModal" class="proxy-pool-form-modal">
      <n-card
        class="proxy-pool-form-card"
        :title="t('proxyPool.testSettings')"
        :bordered="false"
        size="medium"
        role="dialog"
        aria-modal="true"
      >
        <template #header-extra>
          <n-button
            quaternary
            circle
            size="small"
            :aria-label="t('common.close')"
            :title="t('common.close')"
            @click="showSettingsModal = false"
          >
            <template #icon>
              <n-icon :component="Close" />
            </template>
          </n-button>
        </template>

        <n-form
          label-placement="top"
          size="small"
          class="proxy-pool-form"
          @submit.prevent="saveProxyPoolSettings"
        >
          <div class="proxy-pool-settings-group-title">{{ t("proxyPool.proxyTestSettings") }}</div>
          <n-form-item :label="t('proxyPool.testTarget')">
            <n-input
              v-model:value="proxyPoolSettingsForm.targetUrl"
              :placeholder="defaultProxyPoolTestTargetURL"
              :disabled="settingsLoading || settingsSaving"
            />
          </n-form-item>
          <div class="proxy-pool-settings-grid">
            <n-form-item :label="t('proxyPool.testTimeout')">
              <n-input-number
                v-model:value="proxyPoolSettingsForm.timeoutSeconds"
                :min="1"
                :precision="0"
                :show-button="false"
                :disabled="settingsLoading || settingsSaving"
              />
            </n-form-item>
            <n-form-item :label="t('proxyPool.autoTestInterval')">
              <n-input-number
                v-model:value="proxyPoolSettingsForm.intervalMinutes"
                :min="1"
                :precision="0"
                :show-button="false"
                :disabled="settingsLoading || settingsSaving"
              />
            </n-form-item>
          </div>
          <div class="proxy-pool-settings-group-title">
            {{ t("proxyPool.gatewayTestSettings") }}
          </div>
          <div class="proxy-pool-settings-grid">
            <n-form-item :label="t('proxyPool.testTimeout')">
              <n-input-number
                v-model:value="proxyPoolSettingsForm.gatewayTimeoutSeconds"
                :min="1"
                :precision="0"
                :show-button="false"
                :disabled="settingsLoading || settingsSaving"
              />
            </n-form-item>
            <n-form-item :label="t('proxyPool.autoTestInterval')">
              <n-input-number
                v-model:value="proxyPoolSettingsForm.gatewayIntervalMinutes"
                :min="1"
                :precision="0"
                :show-button="false"
                :disabled="settingsLoading || settingsSaving"
              />
            </n-form-item>
          </div>
        </n-form>

        <template #footer>
          <n-space justify="end">
            <n-button @click="showSettingsModal = false">{{ t("common.cancel") }}</n-button>
            <n-button
              type="primary"
              :loading="settingsSaving"
              :disabled="settingsLoading"
              @click="saveProxyPoolSettings"
            >
              {{ t("common.save") }}
            </n-button>
          </n-space>
        </template>
      </n-card>
    </n-modal>

    <n-modal v-model:show="showModal" class="proxy-pool-form-modal">
      <n-card
        class="proxy-pool-form-card"
        :title="editingItem ? t('proxyPool.edit') : t('proxyPool.add')"
        :bordered="false"
        size="medium"
        role="dialog"
        aria-modal="true"
      >
        <template #header-extra>
          <n-button
            quaternary
            circle
            size="small"
            :aria-label="t('common.close')"
            :title="t('common.close')"
            @click="showModal = false"
          >
            <template #icon>
              <n-icon :component="Close" />
            </template>
          </n-button>
        </template>

        <n-form
          ref="formRef"
          :model="form"
          :rules="rules"
          label-placement="left"
          label-width="96px"
          require-mark-placement="right-hanging"
          size="small"
          class="proxy-pool-form"
        >
          <n-form-item :label="t('proxyPool.name')" path="name">
            <n-input v-model:value="form.name" :placeholder="t('proxyPool.namePlaceholder')" />
          </n-form-item>
          <n-form-item :label="t('proxyPool.url')" path="url">
            <n-input v-model:value="form.url" :placeholder="t('proxyPool.urlPlaceholder')" />
          </n-form-item>
        </n-form>

        <template #footer>
          <n-space justify="end">
            <n-button @click="showModal = false">{{ t("common.cancel") }}</n-button>
            <n-button type="primary" :loading="saving" @click="submit">
              {{ t("common.save") }}
            </n-button>
          </n-space>
        </template>
      </n-card>
    </n-modal>
  </div>
</template>

<style scoped>
.proxy-pool-panel {
  display: flex;
  flex-direction: column;
  gap: 8px;
}

.proxy-pool-toolbar {
  display: flex;
  justify-content: space-between;
  gap: 12px;
  align-items: center;
  flex-wrap: wrap;
}

.proxy-pool-meta {
  display: flex;
  flex-wrap: wrap;
  gap: 10px;
  color: var(--text-color-3);
  font-size: 12px;
}

.proxy-pool-content-stack {
  display: flex;
  flex-direction: column;
  gap: 8px;
}

.proxy-pool-section {
  display: flex;
  flex-direction: column;
  gap: 6px;
  min-width: 0;
  padding: 8px 10px;
  border: 1px solid var(--border-color);
  border-radius: 8px;
}

.proxy-pool-section-header {
  display: flex;
  justify-content: space-between;
  gap: 12px;
  align-items: flex-end;
}

.proxy-pool-section-title {
  color: var(--text-color-1);
  font-size: 14px;
  font-weight: 600;
}

.proxy-pool-section-subtitle {
  color: var(--text-color-3);
  font-size: 12px;
  margin-top: 2px;
}

.gateway-active-summary {
  color: var(--text-color-2);
  font-size: 12px;
  margin-top: 2px;
  word-break: break-all;
}

.gateway-name-cell {
  display: inline-flex;
  align-items: center;
  gap: 6px;
  flex-wrap: wrap;
}

.proxy-pool-section :deep(.n-data-table-th),
.proxy-pool-section :deep(.n-data-table-td) {
  padding: 6px 8px;
}

.proxy-pool-test-status {
  display: inline-flex;
  flex-direction: column;
  gap: 3px;
  align-items: flex-start;
  line-height: 1.2;
}

.proxy-pool-form-modal {
  width: min(480px, calc(100vw - 32px));
}

.proxy-pool-form-card {
  max-height: 85vh;
  overflow: auto;
}

.proxy-pool-form {
  padding-top: 2px;
}

.proxy-pool-settings-group-title {
  color: var(--text-color-2);
  font-size: 13px;
  font-weight: 600;
  margin-bottom: 8px;
}

.proxy-pool-settings-group-title:not(:first-child) {
  margin-top: 4px;
}

.proxy-pool-settings-grid {
  display: grid;
  grid-template-columns: 1fr 1fr;
  gap: 12px;
}

.proxy-pool-inline-setting {
  display: inline-flex;
  align-items: center;
  gap: 8px;
  width: 100%;
}

.proxy-pool-inline-setting .n-input-number {
  width: 96px;
}

.proxy-pool-pagination {
  display: flex;
  justify-content: space-between;
  gap: 12px;
  align-items: center;
  flex-wrap: wrap;
  padding-top: 6px;
  border-top: 1px solid var(--border-color);
}

.proxy-pool-gateway-pagination {
  display: flex;
  justify-content: space-between;
  gap: 12px;
  align-items: center;
  flex-wrap: wrap;
  padding-top: 6px;
  border-top: 1px solid var(--border-color);
}

.proxy-pool-pagination-info,
.proxy-pool-pagination-controls {
  display: inline-flex;
  align-items: center;
  gap: 10px;
  color: var(--text-color-3);
  font-size: 12px;
}

.proxy-pool-page-info {
  min-width: 72px;
  text-align: center;
}

@media (max-width: 520px) {
  .proxy-pool-settings-grid {
    grid-template-columns: 1fr;
  }

  .proxy-pool-pagination,
  .proxy-pool-gateway-pagination,
  .proxy-pool-pagination-info,
  .proxy-pool-pagination-controls {
    width: 100%;
  }

  .proxy-pool-pagination-controls {
    justify-content: space-between;
  }
}
</style>
