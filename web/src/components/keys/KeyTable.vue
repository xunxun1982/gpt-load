<script setup lang="ts">
import { keysApi } from "@/api/keys";
import type { APIKey, Group, KeyStatus } from "@/types/models";
import { appState, triggerSyncOperationRefresh } from "@/utils/app-state";
import { copyWithFallback, createManualCopyContent } from "@/utils/clipboard";
import { getGroupDisplayName, maskKey } from "@/utils/display";
import {
  AddCircleOutline,
  AlertCircleOutline,
  CheckmarkCircle,
  CopyOutline,
  EyeOffOutline,
  EyeOutline,
  Pencil,
  RemoveCircleOutline,
  Search,
} from "@vicons/ionicons5";
import {
  NButton,
  NDropdown,
  NEmpty,
  NIcon,
  NInput,
  NModal,
  NSelect,
  NSpace,
  NSpin,
  useDialog,
  type MessageReactive,
} from "naive-ui";
import { computed, h, ref, watch } from "vue";
import { useI18n } from "vue-i18n";
import KeyCreateDialog from "./KeyCreateDialog.vue";
import KeyDeleteDialog from "./KeyDeleteDialog.vue";

const { t } = useI18n();

interface KeyRow extends APIKey {
  is_visible: boolean;
}

interface Props {
  selectedGroup: Group | null;
}

const props = defineProps<Props>();

const keys = ref<KeyRow[]>([]);
const loading = ref(false);
const searchText = ref("");
const statusFilter = ref<"all" | "active" | "invalid">("all");
const currentPage = ref(1);
const pageSize = ref(12);
const total = ref(0);
const totalPages = computed(() => {
  if (total.value < 0) {
    return -1;
  }
  return Math.ceil(total.value / pageSize.value);
});

// Track visibility state for each key separately
const keyVisibility = ref<Record<number, boolean>>({});

// Display text for pagination info
const totalRecordsText = computed(() => {
  if (total.value < 0) {
    return t("keys.calculatingTotal");
  }
  return t("keys.totalRecords", { total: total.value });
});

const pageInfoText = computed(() => {
  if (totalPages.value < 0) {
    return t("keys.pageInfoUnknown", { current: currentPage.value });
  }
  return t("keys.pageInfo", { current: currentPage.value, total: totalPages.value });
});

// Check if next page button should be disabled
const isNextPageDisabled = computed(() => {
  if (totalPages.value > 0 && currentPage.value >= totalPages.value) {
    return true;
  }
  if (keys.value.length === 0) {
    return true;
  }
  return false;
});
const dialog = useDialog();
const confirmInput = ref("");

// Status filter options
const statusOptions = [
  { label: t("common.all"), value: "all" },
  { label: t("keys.valid"), value: "active" },
  { label: t("keys.invalid"), value: "invalid" },
];

// More actions dropdown menu options
const moreOptions = [
  { label: t("keys.exportAllKeys"), key: "copyAll" },
  { label: t("keys.exportValidKeys"), key: "copyValid" },
  { label: t("keys.exportInvalidKeys"), key: "copyInvalid" },
  { type: "divider" },
  { label: t("keys.restoreAllInvalidKeys"), key: "restoreAll" },
  {
    label: t("keys.clearAllInvalidKeys"),
    key: "clearInvalid",
    props: { style: { color: "#d03050" } },
  },
  {
    label: t("keys.clearAllKeys"),
    key: "clearAll",
    props: { style: { color: "red", fontWeight: "bold" } },
  },
  { type: "divider" },
  { label: t("keys.validateAllKeys"), key: "validateAll" },
  { label: t("keys.validateValidKeys"), key: "validateActive" },
  { label: t("keys.validateInvalidKeys"), key: "validateInvalid" },
];

let testingMsg: MessageReactive | null = null;
const isDeling = ref(false);
const isRestoring = ref(false);

const createDialogShow = ref(false);
const deleteDialogShow = ref(false);

// State related to editing key notes
const notesDialogShow = ref(false);
const editingKey = ref<KeyRow | null>(null);
const editingNotes = ref("");

watch(
  () => props.selectedGroup,
  async newGroup => {
    if (newGroup) {
      // Check whether resetting the page will trigger the pagination watcher
      const willWatcherTrigger = currentPage.value !== 1 || statusFilter.value !== "all";
      resetPage();
      // If the pagination watcher does not trigger, manually load data
      if (!willWatcherTrigger) {
        await loadKeys();
      }
    }
  },
  { immediate: true }
);

watch([currentPage, pageSize], async () => {
  await loadKeys();
});

watch(statusFilter, async () => {
  if (currentPage.value !== 1) {
    currentPage.value = 1;
  } else {
    await loadKeys();
  }
});

// Watch task completion events and automatically refresh key list
watch(
  () => appState.groupDataRefreshTrigger,
  () => {
    // Check whether the current group's key list should be refreshed
    if (appState.lastCompletedTask && props.selectedGroup) {
      // Match by group name
      const isCurrentGroup = appState.lastCompletedTask.groupName === props.selectedGroup.name;

      const shouldRefresh =
        appState.lastCompletedTask.taskType === "KEY_VALIDATION" ||
        appState.lastCompletedTask.taskType === "KEY_IMPORT" ||
        appState.lastCompletedTask.taskType === "KEY_DELETE" ||
        appState.lastCompletedTask.taskType === "KEY_RESTORE";

      if (isCurrentGroup && shouldRefresh) {
        // Refresh key list for current group
        loadKeys();
      }
    }
  }
);

// Handle debounced search input
function handleSearchInput() {
  if (currentPage.value !== 1) {
    currentPage.value = 1;
  } else {
    loadKeys();
  }
}

// Handle selection from the more actions menu
function handleMoreAction(key: string) {
  switch (key) {
    case "copyAll":
      copyAllKeys();
      break;
    case "copyValid":
      copyValidKeys();
      break;
    case "copyInvalid":
      copyInvalidKeys();
      break;
    case "restoreAll":
      restoreAllInvalid();
      break;
    case "validateAll":
      validateKeys("all");
      break;
    case "validateActive":
      validateKeys("active");
      break;
    case "validateInvalid":
      validateKeys("invalid");
      break;
    case "clearInvalid":
      clearAllInvalid();
      break;
    case "clearAll":
      clearAll();
      break;
  }
}

async function loadKeys() {
  if (!props.selectedGroup?.id) {
    return;
  }

  try {
    loading.value = true;
    const result = await keysApi.getGroupKeys({
      group_id: props.selectedGroup.id,
      page: currentPage.value,
      page_size: pageSize.value,
      status: statusFilter.value === "all" ? undefined : (statusFilter.value as KeyStatus),
      key_value: searchText.value.trim() || undefined,
    });
    keys.value = result.items as KeyRow[];
    total.value = result.pagination.total_items;
  } finally {
    loading.value = false;
  }
}

// Handle list refresh after batch delete succeeds
async function handleBatchDeleteSuccess() {
  const groupName = props.selectedGroup?.name;
  await loadKeys();
  // Trigger sync operation refresh
  if (groupName) {
    triggerSyncOperationRefresh(groupName, "BATCH_DELETE");
  }
}

async function copyKey(key: KeyRow) {
  await copyWithFallback(key.key_value, {
    onSuccess: () => {
      window.$message.success(t("keys.keyCopied"));
    },
    onError: () => {
      window.$message.error(t("keys.copyFailed"));
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

async function testKey(_key: KeyRow) {
  const group = props.selectedGroup;
  const groupId = group?.id;
  const groupName = group?.name;
  if (!groupId || !_key.key_value || testingMsg) {
    return;
  }

  testingMsg = window.$message.info(t("keys.testingKey"), {
    duration: 0,
  });

  try {
    const response = await keysApi.testKeys(groupId, _key.key_value);
    const curValid = (response.results?.[0] ?? {}) as { is_valid?: boolean; error?: string };
    if (curValid.is_valid) {
      window.$message.success(
        t("keys.testSuccess", { duration: formatDuration(response.total_duration) })
      );
    } else {
      window.$message.error(curValid.error || t("keys.testFailed"), {
        keepAliveOnHover: true,
        duration: 5000,
        closable: true,
      });
    }
    await loadKeys();
    // Trigger sync operation refresh
    if (groupName) {
      triggerSyncOperationRefresh(groupName, "TEST_SINGLE");
    }
  } catch (_error) {
    console.error("Test failed");
  } finally {
    testingMsg?.destroy();
    testingMsg = null;
  }
}

function formatDuration(ms: number): string {
  if (ms < 0) {
    return "0ms";
  }

  const minutes = Math.floor(ms / 60000);
  const seconds = Math.floor((ms % 60000) / 1000);
  const milliseconds = ms % 1000;

  let result = "";
  if (minutes > 0) {
    result += `${minutes}m`;
  }
  if (seconds > 0) {
    result += `${seconds}s`;
  }
  if (milliseconds > 0 || result === "") {
    result += `${milliseconds}ms`;
  }

  return result;
}

// Toggle key visibility using separate reactive state
function toggleKeyVisibility(key: KeyRow) {
  keyVisibility.value[key.id] = !keyVisibility.value[key.id];
}

// Check if a key is currently visible
function isKeyVisible(key: KeyRow): boolean {
  return !!keyVisibility.value[key.id];
}

// Get the display value (show notes only when key is hidden, otherwise show key)
function getDisplayValue(key: KeyRow): string {
  const visible = isKeyVisible(key);
  if (key.notes && !visible) {
    return key.notes;
  }
  return visible ? key.key_value : maskKey(key.key_value);
}

// Open notes editor for a key
function editKeyNotes(key: KeyRow) {
  editingKey.value = key;
  editingNotes.value = key.notes || "";
  notesDialogShow.value = true;
}

// Save notes for the current key
async function saveKeyNotes() {
  if (!editingKey.value) {
    return;
  }

  try {
    const trimmed = editingNotes.value.trim();
    await keysApi.updateKeyNotes(editingKey.value.id, trimmed);
    editingKey.value.notes = trimmed;
    window.$message.success(t("keys.notesUpdated"));
    notesDialogShow.value = false;
  } catch (error) {
    console.error("Update notes failed", error);
    window.$message.error(t("keys.notesUpdateFailed"));
  }
}

async function restoreKey(key: KeyRow) {
  const group = props.selectedGroup;
  const groupId = group?.id;
  const groupName = group?.name;
  if (!groupId || !key.key_value || isRestoring.value) {
    return;
  }

  const d = dialog.warning({
    title: t("keys.restoreKey"),
    content: t("keys.confirmRestoreKey", { key: maskKey(key.key_value) }),
    positiveText: t("common.confirm"),
    negativeText: t("common.cancel"),
    onPositiveClick: async () => {
      isRestoring.value = true;
      d.loading = true;

      try {
        await keysApi.restoreKeys(groupId, key.key_value);
        await loadKeys();
        // Trigger sync operation refresh
        if (groupName) {
          triggerSyncOperationRefresh(groupName, "RESTORE_SINGLE");
        }
      } catch (_error) {
        console.error("Restore failed");
      } finally {
        d.loading = false;
        isRestoring.value = false;
      }
    },
  });
}

async function deleteKey(key: KeyRow) {
  const group = props.selectedGroup;
  const groupId = group?.id;
  const groupName = group?.name;
  if (!groupId || !key.key_value || isDeling.value) {
    return;
  }

  const d = dialog.warning({
    title: t("keys.deleteKey"),
    content: t("keys.confirmDeleteKey", { key: maskKey(key.key_value) }),
    positiveText: t("common.confirm"),
    negativeText: t("common.cancel"),
    onPositiveClick: async () => {
      d.loading = true;
      isDeling.value = true;

      try {
        await keysApi.deleteKeys(groupId, key.key_value);
        await loadKeys();
        // Trigger sync operation refresh
        if (groupName) {
          triggerSyncOperationRefresh(groupName, "DELETE_SINGLE");
        }
      } catch (_error) {
        console.error("Delete failed");
      } finally {
        d.loading = false;
        isDeling.value = false;
      }
    },
  });
}

function formatRelativeTime(date: string) {
  if (!date) {
    return t("keys.never");
  }
  const now = new Date();
  const target = new Date(date);
  const diffSeconds = Math.floor((now.getTime() - target.getTime()) / 1000);
  const diffMinutes = Math.floor(diffSeconds / 60);
  const diffHours = Math.floor(diffMinutes / 60);
  const diffDays = Math.floor(diffHours / 24);

  if (diffDays > 0) {
    return t("keys.daysAgo", { days: diffDays });
  }
  if (diffHours > 0) {
    return t("keys.hoursAgo", { hours: diffHours });
  }
  if (diffMinutes > 0) {
    return t("keys.minutesAgo", { minutes: diffMinutes });
  }
  if (diffSeconds > 0) {
    return t("keys.secondsAgo", { seconds: diffSeconds });
  }
  return t("keys.justNow");
}

function getStatusClass(status: KeyStatus): string {
  switch (status) {
    case "active":
      return "status-valid";
    case "invalid":
      return "status-invalid";
    default:
      return "status-unknown";
  }
}

function copyAllKeys() {
  if (!props.selectedGroup?.id) {
    return;
  }

  keysApi.exportKeys(props.selectedGroup.id, "all");
}

function copyValidKeys() {
  if (!props.selectedGroup?.id) {
    return;
  }

  keysApi.exportKeys(props.selectedGroup.id, "active");
}

function copyInvalidKeys() {
  if (!props.selectedGroup?.id) {
    return;
  }

  keysApi.exportKeys(props.selectedGroup.id, "invalid");
}

async function restoreAllInvalid() {
  const group = props.selectedGroup;
  const groupId = group?.id;
  const groupName = group?.name;
  if (!groupId || isRestoring.value) {
    return;
  }

  const d = dialog.warning({
    title: t("keys.restoreKeys"),
    content: t("keys.confirmRestoreAllInvalid"),
    positiveText: t("common.confirm"),
    negativeText: t("common.cancel"),
    onPositiveClick: async () => {
      isRestoring.value = true;
      d.loading = true;
      try {
        await keysApi.restoreAllInvalidKeys(groupId);
        await loadKeys();
        // Trigger sync operation refresh
        if (groupName) {
          triggerSyncOperationRefresh(groupName, "RESTORE_ALL_INVALID");
        }
      } catch (_error) {
        console.error("Restore failed");
      } finally {
        d.loading = false;
        isRestoring.value = false;
      }
    },
  });
}

async function validateKeys(status: "all" | "active" | "invalid") {
  if (!props.selectedGroup?.id || testingMsg) {
    return;
  }

  let statusText = t("common.all");
  if (status === "active") {
    statusText = t("keys.valid");
  } else if (status === "invalid") {
    statusText = t("keys.invalid");
  }

  testingMsg = window.$message.info(t("keys.validatingKeysMsg", { type: statusText }), {
    duration: 0,
  });

  try {
    await keysApi.validateGroupKeys(props.selectedGroup.id, status === "all" ? undefined : status);
    localStorage.removeItem("last_closed_task");
    appState.taskPollingTrigger++;
  } catch (_error) {
    console.error("Test failed");
  } finally {
    testingMsg?.destroy();
    testingMsg = null;
  }
}

async function clearAllInvalid() {
  const group = props.selectedGroup;
  const groupId = group?.id;
  const groupName = group?.name;
  if (!groupId || isDeling.value) {
    return;
  }

  const d = dialog.warning({
    title: t("keys.clearKeys"),
    content: t("keys.confirmClearInvalidKeys"),
    positiveText: t("common.confirm"),
    negativeText: t("common.cancel"),
    onPositiveClick: async () => {
      isDeling.value = true;
      d.loading = true;
      try {
        const { data } = await keysApi.clearAllInvalidKeys(groupId);
        window.$message.success(data?.message || t("keys.clearSuccess"));
        await loadKeys();
        // Trigger sync operation refresh
        if (groupName) {
          triggerSyncOperationRefresh(groupName, "CLEAR_ALL_INVALID");
        }
      } catch (_error) {
        console.error("Delete failed");
      } finally {
        d.loading = false;
        isDeling.value = false;
      }
    },
  });
}

async function clearAll() {
  const group = props.selectedGroup;
  const groupId = group?.id;
  const groupName = group?.name;
  if (!groupId || !groupName || isDeling.value) {
    return;
  }

  dialog.warning({
    title: t("keys.clearAllKeys"),
    content: t("keys.confirmClearAllKeys"),
    positiveText: t("common.confirm"),
    negativeText: t("common.cancel"),
    onPositiveClick: () => {
      confirmInput.value = ""; // Reset before opening second dialog
      dialog.create({
        title: t("keys.enterGroupNameToConfirm"),
        content: () =>
          h("div", null, [
            h("p", null, [
              t("keys.dangerousOperationWarning1"),
              h("strong", null, t("common.all")),
              t("keys.dangerousOperationWarning2"),
              h("strong", { style: { color: "#d03050" } }, groupName),
              t("keys.toConfirm"),
            ]),
            h(NInput, {
              value: confirmInput.value,
              "onUpdate:value": v => {
                confirmInput.value = v;
              },
              placeholder: t("keys.enterGroupName"),
            }),
          ]),
        positiveText: t("keys.confirmClear"),
        negativeText: t("common.cancel"),
        onPositiveClick: async () => {
          if (confirmInput.value !== groupName) {
            window.$message.error(t("keys.incorrectGroupName"));
            return false; // Prevent dialog from closing
          }

          isDeling.value = true;
          try {
            await keysApi.clearAllKeys(groupId);
            window.$message.success(t("keys.clearAllKeysSuccess"));
            await loadKeys();
            // Trigger sync operation refresh
            triggerSyncOperationRefresh(groupName, "CLEAR_ALL");
          } catch (_error) {
            console.error("Clear all failed", _error);
          } finally {
            isDeling.value = false;
          }
        },
      });
    },
  });
}

function changePage(page: number) {
  currentPage.value = page;
}

function changePageSize(size: number) {
  pageSize.value = size;
  currentPage.value = 1;
}

function resetPage() {
  currentPage.value = 1;
  searchText.value = "";
  statusFilter.value = "all";
}
</script>

<template>
  <div class="key-table-container">
    <!-- Toolbar -->
    <div class="toolbar">
      <div class="toolbar-left">
        <n-button type="success" size="small" @click="createDialogShow = true">
          <template #icon>
            <n-icon :component="AddCircleOutline" />
          </template>
          {{ t("keys.addKey") }}
        </n-button>
        <n-button type="error" size="small" @click="deleteDialogShow = true">
          <template #icon>
            <n-icon :component="RemoveCircleOutline" />
          </template>
          {{ t("keys.deleteKey") }}
        </n-button>
      </div>
      <div class="toolbar-right">
        <n-space :size="12" align="center">
          <n-select
            v-model:value="statusFilter"
            :options="statusOptions"
            size="small"
            style="width: 120px"
            :placeholder="t('keys.allStatus')"
          />
          <n-input-group>
            <n-input
              v-model:value="searchText"
              :placeholder="t('keys.keyExactMatch')"
              size="small"
              style="width: 200px"
              clearable
              @keyup.enter="handleSearchInput"
            >
              <template #prefix>
                <n-icon :component="Search" />
              </template>
            </n-input>
            <n-button
              type="primary"
              ghost
              size="small"
              :disabled="loading"
              @click="handleSearchInput"
            >
              {{ t("common.search") }}
            </n-button>
          </n-input-group>
          <n-dropdown :options="moreOptions" trigger="click" @select="handleMoreAction">
            <n-button size="small" tertiary>
              <template #icon>
                <span style="font-size: 16px; font-weight: bold">⋯</span>
              </template>
            </n-button>
          </n-dropdown>
        </n-space>
      </div>
    </div>

    <!-- Key cards grid -->
    <div class="keys-grid-container">
      <n-spin :show="loading">
        <div v-if="keys.length === 0 && !loading" class="empty-state">
          <n-empty :description="t('keys.noMatchingKeys')" />
        </div>
        <div v-else class="keys-grid">
          <div
            v-for="key in keys"
            :key="key.id"
            class="key-card"
            :class="getStatusClass(key.status)"
          >
            <!-- Main info row: Key + Quick actions -->
            <div class="key-main">
              <div class="key-section">
                <n-tag v-if="key.status === 'active'" type="success" :bordered="false" round>
                  <template #icon>
                    <n-icon :component="CheckmarkCircle" />
                  </template>
                  {{ t("keys.validShort") }}
                </n-tag>
                <n-tag v-else :bordered="false" round>
                  <template #icon>
                    <n-icon :component="AlertCircleOutline" />
                  </template>
                  {{ t("keys.invalidShort") }}
                </n-tag>
                <n-input class="key-text" :value="getDisplayValue(key)" readonly size="small" />
                <div class="quick-actions">
                  <n-button
                    size="tiny"
                    text
                    @click="editKeyNotes(key)"
                    :title="t('keys.editNotes')"
                  >
                    <template #icon>
                      <n-icon :component="Pencil" />
                    </template>
                  </n-button>
                  <n-button
                    size="tiny"
                    text
                    @click="toggleKeyVisibility(key)"
                    :title="t('keys.showHide')"
                  >
                    <template #icon>
                      <n-icon :component="key.is_visible ? EyeOffOutline : EyeOutline" />
                    </template>
                  </n-button>
                  <n-button size="tiny" text @click="copyKey(key)" :title="t('common.copy')">
                    <template #icon>
                      <n-icon :component="CopyOutline" />
                    </template>
                  </n-button>
                </div>
              </div>
            </div>

            <!-- Statistics + Action buttons row -->
            <div class="key-bottom">
              <div class="key-stats">
                <span class="stat-item">
                  {{ t("keys.requestsShort") }}
                  <strong>{{ key.request_count }}</strong>
                </span>
                <span class="stat-item">
                  {{ t("keys.failuresShort") }}
                  <strong>{{ key.failure_count }}</strong>
                </span>
                <span class="stat-item">
                  {{ key.last_used_at ? formatRelativeTime(key.last_used_at) : t("keys.unused") }}
                </span>
              </div>
              <n-button-group class="key-actions">
                <n-button
                  round
                  tertiary
                  type="info"
                  size="tiny"
                  @click="testKey(key)"
                  :title="t('keys.testKey')"
                >
                  {{ t("keys.testShort") }}
                </n-button>
                <n-button
                  v-if="key.status !== 'active'"
                  tertiary
                  size="tiny"
                  @click="restoreKey(key)"
                  :title="t('keys.restoreKey')"
                  type="warning"
                >
                  {{ t("keys.restoreShort") }}
                </n-button>
                <n-button
                  round
                  tertiary
                  size="tiny"
                  type="error"
                  @click="deleteKey(key)"
                  :title="t('keys.deleteKey')"
                >
                  {{ t("common.deleteShort") }}
                </n-button>
              </n-button-group>
            </div>
          </div>
        </div>
      </n-spin>
    </div>

    <!-- Pagination -->
    <div class="pagination-container">
      <div class="pagination-info">
        <span>{{ totalRecordsText }}</span>
        <n-select
          v-model:value="pageSize"
          :options="[
            { label: t('keys.recordsPerPage', { count: 12 }), value: 12 },
            { label: t('keys.recordsPerPage', { count: 24 }), value: 24 },
            { label: t('keys.recordsPerPage', { count: 60 }), value: 60 },
            { label: t('keys.recordsPerPage', { count: 120 }), value: 120 },
          ]"
          size="small"
          style="width: 100px; margin-left: 12px"
          @update:value="changePageSize"
        />
      </div>
      <div class="pagination-controls">
        <n-button size="small" :disabled="currentPage <= 1" @click="changePage(currentPage - 1)">
          {{ t("common.previousPage") }}
        </n-button>
        <span class="page-info">
          {{ pageInfoText }}
        </span>
        <n-button size="small" :disabled="isNextPageDisabled" @click="changePage(currentPage + 1)">
          {{ t("common.nextPage") }}
        </n-button>
      </div>
    </div>

    <key-create-dialog
      v-if="selectedGroup?.id"
      v-model:show="createDialogShow"
      :group-id="selectedGroup.id"
      :group-name="getGroupDisplayName(selectedGroup!)"
      @success="loadKeys"
    />

    <key-delete-dialog
      v-if="selectedGroup?.id"
      v-model:show="deleteDialogShow"
      :group-id="selectedGroup.id"
      :group-name="getGroupDisplayName(selectedGroup!)"
      @success="handleBatchDeleteSuccess"
    />
  </div>

  <!-- Notes edit dialog -->
  <n-modal v-model:show="notesDialogShow" preset="dialog" :title="t('keys.editKeyNotes')">
    <n-input
      v-model:value="editingNotes"
      type="textarea"
      :placeholder="t('keys.enterNotes')"
      :rows="3"
      maxlength="255"
      show-count
    />
    <template #action>
      <n-button @click="notesDialogShow = false">{{ t("common.cancel") }}</n-button>
      <n-button type="primary" @click="saveKeyNotes">{{ t("common.save") }}</n-button>
    </template>
  </n-modal>
</template>

<style scoped>
.key-table-container {
  background: var(--card-bg-solid);
  border-radius: 8px;
  box-shadow: var(--shadow-md);
  border: 1px solid var(--border-color);
  overflow: hidden;
  height: 100%;
  display: flex;
  flex-direction: column;
}

.toolbar {
  display: flex;
  justify-content: space-between;
  align-items: center;
  padding: 16px;
  background: var(--card-bg-solid);
  border-bottom: 1px solid var(--border-color);
  flex-shrink: 0;
  gap: 16px;
  min-height: 64px;
}

.toolbar :deep(.n-button) {
  font-weight: 500;
}

.toolbar-left {
  display: flex;
  gap: 8px;
  flex-shrink: 0;
}

.toolbar-right {
  display: flex;
  gap: 12px;
  align-items: center;
  flex: 1;
  justify-content: flex-end;
  min-width: 0;
}

.filter-group {
  display: flex;
  align-items: center;
  gap: 8px;
}

.more-actions {
  position: relative;
}

.more-menu {
  position: absolute;
  top: 100%;
  right: 0;
  background: var(--card-bg-solid);
  border: 1px solid var(--border-color);
  border-radius: 6px;
  box-shadow: var(--shadow-lg);
  min-width: 180px;
  z-index: 1000;
  overflow: hidden;
}

.menu-item {
  display: block;
  width: 100%;
  padding: 8px 12px;
  border: none;
  background: none;
  text-align: left;
  cursor: pointer;
  font-size: 14px;
  color: #333;
  transition: background-color 0.2s;
}

.menu-item:hover {
  background: #f8f9fa;
}

.menu-item.danger {
  color: #dc3545;
}

.menu-item.danger:hover {
  background: #f8d7da;
}

.menu-divider {
  height: 1px;
  background: #e9ecef;
  margin: 4px 0;
}

.btn {
  padding: 6px 12px;
  border: none;
  border-radius: 4px;
  cursor: pointer;
  font-size: 14px;
  transition: all 0.2s;
  white-space: nowrap;
}

.btn-sm {
  padding: 4px 8px;
  font-size: 12px;
}

.btn:disabled {
  opacity: 0.6;
  cursor: not-allowed;
}

.btn-primary {
  background: #007bff;
  color: white;
}

.btn-primary:hover:not(:disabled) {
  background: #0056b3;
}

.btn-secondary {
  background: #6c757d;
  color: white;
}

.btn-secondary:hover:not(:disabled) {
  background: #545b62;
}

.more-icon {
  font-size: 16px;
  font-weight: bold;
}

.filter-select,
.search-input,
.page-size-select {
  padding: 4px 8px;
  border: 1px solid #ced4da;
  border-radius: 4px;
  font-size: 12px;
}

.search-input {
  width: 180px;
}

.filter-select:focus,
.search-input:focus,
.page-size-select:focus {
  outline: none;
  border-color: #007bff;
  box-shadow: 0 0 0 2px rgba(0, 123, 255, 0.25);
}

/* Key cards grid */
.keys-grid-container {
  flex: 1;
  overflow-y: auto;
  padding: 16px;
}

.keys-grid {
  display: grid;
  grid-template-columns: repeat(auto-fill, minmax(240px, 1fr));
  gap: 16px;
}

.key-card {
  background: var(--card-bg-solid);
  border: 1px solid var(--border-color);
  border-radius: 8px;
  padding: 14px;
  transition: all 0.2s;
  display: flex;
  flex-direction: column;
  gap: 10px;
  box-shadow: 0 1px 3px rgba(0, 0, 0, 0.05);
}

.key-card:hover {
  box-shadow: var(--shadow-md);
  transform: translateY(-1px);
}

/* Status-related styles */
.key-card.status-valid {
  border-color: var(--success-border);
  background: var(--success-bg);
  border-width: 1.5px;
}

.key-card.status-invalid {
  border-color: var(--invalid-border);
  background: var(--card-bg-solid);
  opacity: 0.85;
}

.key-card.status-error {
  border-color: var(--error-border);
  background: var(--error-bg);
}

/* Main info row */
.key-main {
  display: flex;
  justify-content: space-between;
  align-items: center;
  gap: 8px;
}

.key-section {
  display: flex;
  align-items: center;
  gap: 8px;
  flex: 1;
  min-width: 0;
}

/* Bottom statistics and buttons row */
.key-bottom {
  display: flex;
  justify-content: space-between;
  align-items: center;
  gap: 8px;
}

.key-stats {
  display: flex;
  gap: 8px;
  font-size: 12px;
  overflow: hidden;
  color: var(--text-secondary);
  flex: 1;
  min-width: 0;
}

.stat-item {
  white-space: nowrap;
  color: var(--text-secondary);
}

.stat-item strong {
  color: var(--text-primary);
  font-weight: 600;
}

.key-actions {
  flex-shrink: 0;
}

.key-actions :deep(.n-button) {
  padding: 0 4px;
}

.key-text {
  font-family: "SFMono-Regular", Consolas, "Liberation Mono", Menlo, Courier, monospace;
  font-weight: 500;
  flex: 1;
  min-width: 0;
  overflow: hidden;
  white-space: nowrap;
}

/* Light theme */
:root:not(.dark) .key-text {
  color: #495057;
  background: #f8f9fa;
}

/* Dark theme */
:root.dark .key-text {
  color: var(--text-primary);
  background: var(--bg-tertiary);
}

:deep(.n-input__input-el) {
  font-family: "SFMono-Regular", Consolas, "Liberation Mono", Menlo, Courier, monospace;
  font-size: 13px;
}

.quick-actions {
  display: flex;
  gap: 4px;
  flex-shrink: 0;
}

/* Statistics row */

.action-btn {
  padding: 2px 6px;
  border: 1px solid var(--border-color);
  background: var(--card-bg-solid);
  border-radius: 3px;
  cursor: pointer;
  font-size: 10px;
  font-weight: 500;
  transition: all 0.2s;
  white-space: nowrap;
  color: var(--text-primary);
}

.action-btn:hover {
  background: var(--bg-secondary);
}

.action-btn.primary {
  border-color: var(--primary-color);
  color: var(--primary-color);
}

.action-btn.primary:hover {
  background: var(--primary-color);
  color: white;
}

.action-btn.secondary {
  border-color: #6c757d;
  color: #6c757d;
}

.action-btn.secondary:hover {
  background: #6c757d;
  color: white;
}

.action-btn.danger {
  border-color: #dc3545;
  color: #dc3545;
}

.action-btn.danger:hover {
  background: #dc3545;
  color: white;
}

/* Empty state */
.empty-state {
  display: flex;
  justify-content: center;
  align-items: center;
  height: 200px;
  color: #6c757d;
}

/* Pagination */
.pagination-container {
  display: flex;
  justify-content: space-between;
  align-items: center;
  padding: 12px 16px;
  background: var(--card-bg-solid);
  border-top: 1px solid var(--border-color);
  flex-shrink: 0;
  border-radius: 0 0 8px 8px;
}

.pagination-info {
  display: flex;
  align-items: center;
  gap: 12px;
  font-size: 12px;
  color: var(--text-secondary);
}

.pagination-controls {
  display: flex;
  align-items: center;
  gap: 12px;
}

.page-info {
  font-size: 12px;
  color: var(--text-secondary);
}

@media (max-width: 768px) {
  .toolbar {
    flex-direction: column;
    align-items: stretch;
    gap: 12px;
  }

  .toolbar-left,
  .toolbar-right {
    width: 100%;
    justify-content: space-between;
  }

  .toolbar-right .n-space {
    width: 100%;
    justify-content: space-between;
  }
}
</style>
