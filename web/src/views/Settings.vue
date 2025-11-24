<script setup lang="ts">
import { settingsApi, type Setting, type SettingCategory } from "@/api/settings";
import ProxyKeysInput from "@/components/common/ProxyKeysInput.vue";
import http from "@/utils/http";
import { HelpCircle, Save, CloudDownloadOutline, CloudUploadOutline } from "@vicons/ionicons5";
import {
  NButton,
  NCard,
  NForm,
  NFormItem,
  NGrid,
  NGridItem,
  NIcon,
  NInput,
  NInputNumber,
  NSpace,
  NSwitch,
  NTooltip,
  useDialog,
  useMessage,
  type FormItemRule,
} from "naive-ui";
import { h, ref } from "vue";
import { useI18n } from "vue-i18n";

const { t } = useI18n();

const settingList = ref<SettingCategory[]>([]);
const formRef = ref();
const form = ref<Record<string, string | number | boolean>>({});
const isSaving = ref(false);
const message = useMessage();
const dialog = useDialog();
const systemFileInputRef = ref<HTMLInputElement | null>(null);

fetchSettings();

async function fetchSettings() {
  try {
    const data = await settingsApi.getSettings();
    settingList.value = data || [];
    initForm();
  } catch (_error) {
    message.error(t("settings.loadFailed"));
  }
}

function initForm() {
  form.value = settingList.value.reduce(
    (acc: Record<string, string | number | boolean>, category) => {
      category.settings?.forEach(setting => {
        acc[setting.key] = setting.value;
      });
      return acc;
    },
    {}
  );
}

async function handleSubmit() {
  if (isSaving.value) {
    return;
  }

  try {
    await formRef.value.validate();
    isSaving.value = true;
    await settingsApi.updateSettings(form.value);
    await fetchSettings();
  } finally {
    isSaving.value = false;
  }
}

function generateValidationRules(item: Setting): FormItemRule[] {
  const rules: FormItemRule[] = [];
  if (item.required) {
    const rule: FormItemRule = {
      required: true,
      message: t("settings.pleaseInput", { field: item.name }),
      trigger: ["input", "blur"],
    };
    if (item.type === "int") {
      rule.type = "number";
    }
    rules.push(rule);
  }
  if (item.type === "int" && item.min_value !== undefined && item.min_value !== null) {
    rules.push({
      validator: (_rule: FormItemRule, value: number) => {
        if (value === null || value === undefined) {
          return true;
        }
        if (item.min_value !== undefined && item.min_value !== null && value < item.min_value) {
          return new Error(t("settings.minValueError", { value: item.min_value }));
        }
        return true;
      },
      trigger: ["input", "blur"],
    });
  }
  return rules;
}

// Export full system configuration
async function handleExportAll() {
  const { askExportMode } = await import("@/utils/export-import");
  const mode = await askExportMode(dialog, t);

  try {
    await settingsApi.exportAll(mode);
    message.success(t("settings.exportSuccess"));
  } catch (error: unknown) {
    const errorMessage = error instanceof Error ? error.message : t("settings.exportFailed");
    message.error(errorMessage);
  }
}

// Import state management
const isImporting = ref(false);

// Trigger system file selection
function handleSystemImportClick() {
  if (isImporting.value) {
    message.warning(t("settings.importInProgress"));
    return;
  }
  systemFileInputRef.value?.click();
}

// Handle system file import
async function handleSystemFileChange(event: Event) {
  const target = event.target as HTMLInputElement;
  const file = target.files?.[0];

  if (!file) {
    return;
  }

  try {
    const text = await file.text();
    let parsedData: unknown;

    try {
      parsedData = JSON.parse(text);
    } catch (parseError) {
      console.error("JSON parse error:", parseError);
      message.error(`${t("settings.invalidImportFile")}: JSON format error`);
      target.value = "";
      return;
    }

    // Handle possible data wrapping (response.data format)
    // Support two formats:
    // 1. Direct data format: { version, system_settings, groups }
    // 2. Wrapped format: { code, message, data: { version, system_settings, groups } }
    interface ImportData {
      version?: string;
      system_settings?: Record<string, unknown>;
      groups?: unknown[];
    }

    interface WrappedResponse {
      data?: ImportData;
      code?: number;
      message?: string;
    }

    let data: ImportData;

    // Check if it's wrapped format (contains code or message field, and has data field)
    const wrappedData = parsedData as WrappedResponse;
    if (
      wrappedData.data &&
      typeof wrappedData.data === "object" &&
      (wrappedData.code !== undefined || wrappedData.message !== undefined)
    ) {
      data = wrappedData.data as ImportData;
    } else {
      data = parsedData as ImportData;
    }

    // Debug information (only output in development environment)
    if (import.meta.env.DEV) {
      globalThis.console.warn("Parsed import data structure:", {
        originalKeys: Object.keys(parsedData as Record<string, unknown>),
        dataKeys: Object.keys(data),
        hasSystemSettings: !!data.system_settings,
        hasGroups: !!data.groups,
        systemSettingsType: typeof data.system_settings,
        groupsType: Array.isArray(data.groups) ? "array" : typeof data.groups,
        systemSettingsKeys: data.system_settings ? Object.keys(data.system_settings) : [],
        groupsLength: Array.isArray(data.groups) ? data.groups.length : "not array",
      });
    }

    // Determine import data type - more lenient check
    const hasSystemSettings = data.system_settings && typeof data.system_settings === "object";
    const hasGroups = data.groups && Array.isArray(data.groups);
    const systemSettingsCount =
      hasSystemSettings && data.system_settings ? Object.keys(data.system_settings).length : 0;
    const groupsCount = hasGroups && data.groups ? data.groups.length : 0;

    // If both system settings and groups exist (even if one is empty), use full import
    if (hasSystemSettings && systemSettingsCount > 0 && hasGroups && groupsCount > 0) {
      dialog.warning({
        title: t("settings.importSystem"),
        content: t("settings.importSystemWithGroups"),
        positiveText: t("settings.importBoth"),
        negativeText: t("common.cancel"),
        onPositiveClick: () => {
          if (isImporting.value) {
            message.warning(t("settings.importInProgress"));
            return false;
          }

          // Close dialog immediately and start import
          isImporting.value = true;
          message.loading(t("settings.importingSystem"), { duration: 0 });

          // Execute import asynchronously after dialog closes
          setTimeout(async () => {
            try {
              const { askImportMode } = await import("@/utils/export-import");
              const mode = await askImportMode(dialog, t);
              await settingsApi.importAll(data, { mode, filename: file.name });
              message.destroyAll();
              message.success(t("settings.importSuccess"));
              await fetchSettings();
            } catch (error: unknown) {
              message.destroyAll();
              const errorMessage =
                error instanceof Error ? error.message : t("settings.importFailed");
              message.error(errorMessage);
            } finally {
              isImporting.value = false;
            }
          }, 100);

          return true; // Close dialog immediately
        },
      });
    } else if (hasSystemSettings && systemSettingsCount > 0) {
      // System settings only
      dialog.info({
        title: t("settings.importSystemSettings"),
        content: t("settings.importSystemSettingsConfirm"),
        positiveText: t("common.confirm"),
        negativeText: t("common.cancel"),
        onPositiveClick: () => {
          if (isImporting.value) {
            message.warning(t("settings.importInProgress"));
            return false;
          }

          // Close dialog immediately and start import
          isImporting.value = true;
          message.loading(t("settings.importingSettings"), { duration: 0 });

          // Execute import asynchronously after dialog closes
          setTimeout(async () => {
            try {
              // Type guard: ensure system_settings exists and is an object
              if (!data.system_settings || typeof data.system_settings !== "object") {
                throw new Error(t("settings.invalidImportFile"));
              }
              // Convert Record<string, unknown> to Record<string, string>
              const systemSettings: Record<string, string> = {};
              for (const [key, value] of Object.entries(data.system_settings)) {
                systemSettings[key] = String(value ?? "");
              }
              await settingsApi.importSystemSettings({ system_settings: systemSettings });
              message.destroyAll();
              message.success(t("settings.importSuccess"));
              await fetchSettings();
            } catch (error: unknown) {
              message.destroyAll();
              const errorMessage =
                error instanceof Error ? error.message : t("settings.importFailed");
              message.error(errorMessage);
            } finally {
              isImporting.value = false;
            }
          }, 100);

          return true; // Close dialog immediately
        },
      });
    } else if (hasGroups && groupsCount > 0) {
      // Groups only
      dialog.info({
        title: t("settings.importGroups"),
        content: t("settings.importGroupsConfirm", { count: groupsCount }),
        positiveText: t("common.confirm"),
        negativeText: t("common.cancel"),
        onPositiveClick: () => {
          if (isImporting.value) {
            message.warning(t("settings.importInProgress"));
            return false;
          }

          // Close dialog immediately and start import
          isImporting.value = true;
          message.loading(t("settings.importingGroups"), { duration: 0 });

          // Execute import asynchronously after dialog closes
          setTimeout(async () => {
            try {
              // Type guard: ensure groups exists and is an array
              if (!data.groups || !Array.isArray(data.groups)) {
                throw new Error(t("settings.invalidImportFile"));
              }
              // Ask import mode (backend will ignore if unsupported)
              const { askImportMode } = await import("@/utils/export-import");
              const mode = await askImportMode(dialog, t);
              // Prefer full system import path when only groups provided? Keep batch endpoint for compatibility
              await settingsApi.importGroupsBatch(
                { groups: data.groups },
                { mode, filename: file.name }
              );
              message.destroyAll();
              message.success(t("settings.importSuccess"));
            } catch (error: unknown) {
              message.destroyAll();
              const errorMessage =
                error instanceof Error ? error.message : t("settings.importFailed");
              message.error(errorMessage);
            } finally {
              isImporting.value = false;
            }
          }, 100);

          return true; // Close dialog immediately
        },
      });
    } else if (hasSystemSettings || hasGroups) {
      // Has fields but both are empty, validate before importing
      // Reject truly empty imports to prevent unintentional data clearing
      if (systemSettingsCount === 0 && groupsCount === 0) {
        console.error("Empty import data detected:", data);
        message.warning(
          `${t("settings.invalidImportFile")}: Import data is empty, both system_settings and groups have no valid content. Please check the import file.`
        );
        return;
      }

      // If one field has content but the other is empty, use full import
      dialog.warning({
        title: t("settings.importSystem"),
        content: `${t("settings.importSystemConfirm")}\n\nNote: Partial data is empty, will use full import mode.`,
        positiveText: t("common.confirm"),
        negativeText: t("common.cancel"),
        onPositiveClick: () => {
          if (isImporting.value) {
            message.warning(t("settings.importInProgress"));
            return false;
          }

          // Close dialog immediately and start import
          isImporting.value = true;
          message.loading(t("settings.importingSystem"), { duration: 0 });

          // Execute import asynchronously after dialog closes
          setTimeout(async () => {
            try {
              await settingsApi.importAll(data);
              message.destroyAll();
              message.success(t("settings.importSuccess"));
              await fetchSettings();
            } catch (error: unknown) {
              message.destroyAll();
              const errorMessage =
                error instanceof Error ? error.message : t("settings.importFailed");
              message.error(errorMessage);
            } finally {
              isImporting.value = false;
            }
          }, 100);

          return true; // Close dialog immediately
        },
      });
    } else {
      // Invalid import file
      console.error("Invalid import file structure:", data);
      message.error(
        `${t("settings.invalidImportFile")}: File format is incorrect, missing system_settings or groups field`
      );
    }
  } catch (error: unknown) {
    console.error("Import error:", error);
    const errorMessage = error instanceof Error ? error.message : "Unknown error";
    message.error(`${t("settings.invalidImportFile")}: ${errorMessage}`);
  } finally {
    // Clear file input to allow selecting the same file again
    target.value = "";
  }
}

// ============ Debug Mode: Delete All Groups ============
// This is a dangerous operation that should only be used for testing/debugging
// Requires DEBUG_MODE environment variable to be enabled on the server
const isDeletingAllGroups = ref(false);

/**
 * Handle delete all groups with multiple confirmations
 * This function implements a three-step confirmation process to prevent accidental deletion:
 * 1. First warning dialog explaining the danger
 * 2. Second confirmation requiring typing "DELETE ALL"
 * 3. Final confirmation before actual deletion
 */
function handleDeleteAllGroups() {
  // First confirmation: Warning dialog
  dialog.warning({
    title: `⚠️ ${t("settings.dangerZone")}`,
    content: t("settings.deleteAllGroupsWarning"),
    positiveText: t("common.confirm"),
    negativeText: t("common.cancel"),
    onPositiveClick: () => {
      // Second confirmation: Require typing confirmation text
      showDeleteAllGroupsConfirmation();
    },
  });
}

/**
 * Show second confirmation dialog requiring user to type "DELETE ALL"
 * Uses a simpler approach with validation in the callback
 */
function showDeleteAllGroupsConfirmation() {
  let confirmText = "";
  const REQUIRED_TEXT = "DELETE ALL";

  dialog.warning({
    title: `⚠️⚠️ ${t("settings.finalWarning")}`,
    content: () => {
      return h("div", [
        h(
          "p",
          { style: "margin-bottom: 12px; color: #d03050; font-weight: bold;" },
          t("settings.deleteAllGroupsConfirmText")
        ),
        h(
          "p",
          { style: "margin-bottom: 12px;" },
          t("settings.typeToConfirm", { text: REQUIRED_TEXT })
        ),
        h(
          "p",
          { style: "margin-bottom: 8px; font-size: 12px; color: #666;" },
          t("settings.caseSensitiveWarning")
        ),
        h(NInput, {
          defaultValue: "",
          onUpdateValue: (val: string) => {
            confirmText = val;
          },
          placeholder: REQUIRED_TEXT,
          style: "margin-top: 8px;",
          autofocus: true,
        }),
      ]);
    },
    positiveText: t("settings.deleteAllGroups"),
    negativeText: t("common.cancel"),
    positiveButtonProps: {
      type: "error",
    },
    onPositiveClick: () => {
      // Validate the input text
      if (confirmText.trim() !== REQUIRED_TEXT) {
        message.error(t("settings.confirmTextMismatch", { text: REQUIRED_TEXT }));
        return false; // Prevent dialog from closing
      }
      // Proceed with deletion
      performDeleteAllGroups();
      return true; // Allow dialog to close
    },
  });
}

/**
 * Perform the actual deletion of all groups
 * This is the final step after all confirmations
 */
async function performDeleteAllGroups() {
  if (isDeletingAllGroups.value) {
    return;
  }

  try {
    isDeletingAllGroups.value = true;

    // Import keysApi to access deleteAllGroups method
    const { keysApi } = await import("@/api/keys");

    await keysApi.deleteAllGroups();
    message.success(t("groups.allGroupsDeletedSuccess"));

    // Optionally refresh the page or redirect
    setTimeout(() => {
      window.location.reload();
    }, 1500);
  } catch (error: unknown) {
    console.error("Delete all groups error:", error);
    const errorMessage = error instanceof Error ? error.message : String(error);
    if (errorMessage?.includes("DEBUG_MODE")) {
      message.error(t("groups.debugModeRequired"));
    } else {
      message.error(errorMessage || t("settings.deleteAllGroupsFailed"));
    }
  } finally {
    isDeletingAllGroups.value = false;
  }
}

// Check if debug mode is enabled by checking if the endpoint is accessible
// We'll show the button only if we can detect debug mode is enabled
const isDebugModeEnabled = ref(false);

// Check if debug mode is enabled from server
async function checkDebugMode() {
  try {
    const response = await http.get<{ debug_mode: boolean }>("/system/environment");
    // API response format: { code: 0, message: "success", data: { debug_mode: true } }
    // http.get returns the full ApiResponse, so we need to access response.data
    isDebugModeEnabled.value = response.data?.debug_mode === true;
  } catch (error) {
    console.error("Failed to check debug mode:", error);
    isDebugModeEnabled.value = false;
  }
}

checkDebugMode();
</script>

<template>
  <n-space vertical>
    <n-form ref="formRef" :model="form" label-placement="top">
      <n-space vertical>
        <n-card
          size="small"
          v-for="category in settingList"
          :key="category.category_name"
          :title="category.category_name"
          hoverable
          bordered
        >
          <n-grid :x-gap="36" :y-gap="0" responsive="screen" cols="1 s:2 m:2 l:4 xl:4">
            <n-grid-item
              v-for="item in category.settings"
              :key="item.key"
              :span="item.key === 'proxy_keys' ? 3 : 1"
            >
              <n-form-item :path="item.key" :rule="generateValidationRules(item)">
                <template #label>
                  <n-space align="center" :size="4" :wrap-item="false">
                    <n-tooltip trigger="hover" placement="top">
                      <template #trigger>
                        <n-icon
                          :component="HelpCircle"
                          :size="16"
                          style="cursor: help; color: #9ca3af"
                        />
                      </template>
                      {{ item.description }}
                    </n-tooltip>
                    <span>{{ item.name }}</span>
                  </n-space>
                </template>

                <n-input-number
                  v-if="item.type === 'int'"
                  v-model:value="form[item.key] as number"
                  :min="
                    item.min_value !== undefined && item.min_value >= 0 ? item.min_value : undefined
                  "
                  :placeholder="t('settings.inputNumber')"
                  clearable
                  style="width: 100%"
                  size="small"
                />
                <n-switch
                  v-else-if="item.type === 'bool'"
                  v-model:value="form[item.key] as boolean"
                  size="small"
                />
                <proxy-keys-input
                  v-else-if="item.key === 'proxy_keys'"
                  v-model="form[item.key] as string"
                  :placeholder="t('settings.inputContent')"
                  size="small"
                />
                <n-input
                  v-else
                  v-model:value="form[item.key] as string"
                  :placeholder="t('settings.inputContent')"
                  clearable
                  size="small"
                />
              </n-form-item>
            </n-grid-item>
          </n-grid>
        </n-card>
      </n-space>
    </n-form>

    <div
      v-if="settingList.length > 0"
      style="display: flex; justify-content: center; gap: 12px; padding-top: 12px; flex-wrap: wrap"
    >
      <n-button
        type="primary"
        size="large"
        :loading="isSaving"
        :disabled="isSaving"
        @click="handleSubmit"
        style="min-width: 200px"
      >
        <template #icon>
          <n-icon :component="Save" />
        </template>
        {{ isSaving ? t("settings.saving") : t("settings.saveSettings") }}
      </n-button>
      <n-button
        type="info"
        size="large"
        :disabled="isSaving || isImporting"
        @click="handleExportAll"
        style="min-width: 200px"
      >
        <template #icon>
          <n-icon :component="CloudDownloadOutline" />
        </template>
        {{ t("settings.exportSystem") }}
      </n-button>
      <n-button
        type="warning"
        size="large"
        :loading="isImporting"
        :disabled="isSaving || isImporting"
        @click="handleSystemImportClick"
        style="min-width: 200px"
      >
        <template #icon>
          <n-icon :component="CloudUploadOutline" />
        </template>
        {{ isImporting ? t("settings.importing") : t("settings.importSystem") }}
      </n-button>
    </div>

    <!-- Danger Zone: Debug Mode Only -->
    <n-card
      v-if="isDebugModeEnabled"
      size="small"
      title="⚠️ Danger Zone (Debug Mode Only)"
      style="margin-top: 24px; border: 2px solid #d03050"
      :segmented="{
        content: true,
        footer: 'soft',
      }"
    >
      <template #header-extra>
        <n-tooltip trigger="hover" placement="top">
          <template #trigger>
            <n-icon :component="HelpCircle" :size="18" style="cursor: help; color: #d03050" />
          </template>
          {{ t("settings.debugModeOnlyFeature") }}
        </n-tooltip>
      </template>

      <n-space vertical :size="12">
        <div style="color: #d03050; font-weight: bold">
          {{ t("settings.dangerZoneWarning") }}
        </div>
        <div style="color: #666">
          {{ t("settings.dangerZoneDescription") }}
        </div>
      </n-space>

      <template #footer>
        <div style="display: flex; justify-content: center">
          <n-button
            type="error"
            size="large"
            :loading="isDeletingAllGroups"
            :disabled="isDeletingAllGroups || isSaving"
            @click="handleDeleteAllGroups"
            style="min-width: 250px"
            secondary
          >
            {{ t("settings.deleteAllGroups") }}
          </n-button>
        </div>
      </template>
    </n-card>

    <!-- Hidden file input -->
    <input
      ref="systemFileInputRef"
      type="file"
      accept=".json"
      style="display: none"
      @change="handleSystemFileChange"
    />
  </n-space>
</template>
