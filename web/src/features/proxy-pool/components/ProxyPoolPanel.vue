<script setup lang="ts">
import { proxyPoolApi, type ProxyPoolPayload, type ProxyPoolTestResult } from "@/api/proxy-pool";
import type { ProxyPoolItem } from "@/types/models";
import {
  Add,
  CheckmarkCircleOutline,
  Close,
  CreateOutline,
  RefreshOutline,
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
  NModal,
  NSpace,
  NSwitch,
  NTag,
  NText,
  NTooltip,
  useDialog,
  useMessage,
  type DataTableColumns,
  type FormRules,
} from "naive-ui";
import { computed, h, onMounted, onUnmounted, reactive, ref } from "vue";
import { useI18n } from "vue-i18n";

const { t } = useI18n();
const message = useMessage();
const dialog = useDialog();
const proxyTestTimeoutSeconds = 10;
const proxyAutoTestIntervalMs = 60 * 60 * 1000;
const proxyBatchTestConcurrency = 5;

const loading = ref(false);
const saving = ref(false);
const showModal = ref(false);
const editingItem = ref<ProxyPoolItem | null>(null);
const items = ref<ProxyPoolItem[]>([]);
const testingAll = ref(false);
const autoTesting = ref(false);
const batchTesting = ref(false);
const testingIds = ref<number[]>([]);
const testResults = reactive<Record<number, ProxyPoolTestResult>>({});
let autoTestTimer: number | undefined;

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

const testTimeoutText = computed(() =>
  t("proxyPool.testTimeoutValue", { seconds: proxyTestTimeoutSeconds })
);
const autoTestIntervalText = computed(() =>
  t("proxyPool.autoTestIntervalValue", { hours: proxyAutoTestIntervalMs / (60 * 60 * 1000) })
);

function renderTestStatus(content: ReturnType<typeof h>) {
  return h("div", { class: "proxy-pool-test-status" }, [
    content,
    h(NText, { depth: 3 }, () =>
      t("proxyPool.testTimeoutInline", { timeout: testTimeoutText.value })
    ),
  ]);
}

async function loadItems() {
  loading.value = true;
  try {
    items.value = await proxyPoolApi.list();
    if (items.value.length === 0 && autoTesting.value) {
      handleAutoTestChange(false);
    }
  } catch {
    message.error(t("proxyPool.loadFailed"));
  } finally {
    loading.value = false;
  }
}

function isTesting(id: number): boolean {
  return testingIds.value.includes(id);
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
      timeout_ms: proxyTestTimeoutSeconds * 1000,
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

function clearAutoTestTimer() {
  if (autoTestTimer !== undefined) {
    window.clearInterval(autoTestTimer);
    autoTestTimer = undefined;
  }
}

function handleAutoTestChange(enabled: boolean) {
  autoTesting.value = enabled;
  clearAutoTestTimer();
  if (!enabled) {
    return;
  }
  void testAll(true);
  autoTestTimer = window.setInterval(() => {
    void testAll(true);
  }, proxyAutoTestIntervalMs);
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

function openEdit(item: ProxyPoolItem) {
  editingItem.value = item;
  form.name = item.name;
  form.url = item.url;
  showModal.value = true;
}

async function submit() {
  await formRef.value?.validate();
  saving.value = true;
  try {
    const payload = { name: form.name.trim(), url: form.url.trim() };
    if (editingItem.value) {
      await proxyPoolApi.update(editingItem.value.id, payload);
    } else {
      await proxyPoolApi.create(payload);
    }
    showModal.value = false;
    resetForm();
    await loadItems();
  } finally {
    saving.value = false;
  }
}

function confirmDelete(item: ProxyPoolItem) {
  dialog.warning({
    title: t("common.confirm"),
    content: t("proxyPool.deleteConfirm", { name: item.name }),
    positiveText: t("common.delete"),
    negativeText: t("common.cancel"),
    onPositiveClick: async () => {
      await proxyPoolApi.delete(item.id);
      delete testResults[item.id];
      await loadItems();
    },
  });
}

onMounted(loadItems);
onUnmounted(clearAutoTestTimer);
</script>

<template>
  <div class="proxy-pool-panel">
    <div class="proxy-pool-toolbar">
      <div class="proxy-pool-meta">
        <span>{{ t("proxyPool.testTimeout") }}: {{ testTimeoutText }}</span>
        <span>{{ t("proxyPool.autoTestInterval") }}: {{ autoTestIntervalText }}</span>
      </div>
      <n-space align="center">
        <n-button type="primary" size="small" @click="openCreate">
          <template #icon>
            <n-icon :component="Add" />
          </template>
          {{ t("proxyPool.add") }}
        </n-button>
        <n-button size="small" :loading="loading" @click="loadItems">
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
          {{ t("proxyPool.testAll") }}
        </n-button>
        <div class="proxy-pool-auto-test">
          <n-switch
            size="small"
            :value="autoTesting"
            :disabled="items.length === 0"
            @update:value="handleAutoTestChange"
          />
          <span>{{ t("proxyPool.autoTest") }}</span>
        </div>
      </n-space>
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
          <n-button quaternary circle size="small" @click="showModal = false">
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
  gap: 10px;
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

.proxy-pool-auto-test {
  display: inline-flex;
  align-items: center;
  gap: 6px;
  color: var(--text-color-2);
  font-size: 13px;
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
}

.proxy-pool-form {
  padding-top: 2px;
}
</style>
