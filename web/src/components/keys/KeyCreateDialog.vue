<script setup lang="ts">
import { keysApi } from "@/api/keys";
import { appState } from "@/utils/app-state";
import { Close, DocumentText } from "@vicons/ionicons5";
import { NAlert, NButton, NCard, NIcon, NInput, NModal } from "naive-ui";
import { computed, ref, watch } from "vue";
import { useI18n } from "vue-i18n";

// Constants for file size thresholds and key estimation
// Threshold for switching to streaming import (MB)
const LARGE_FILE_THRESHOLD_MB = 10;
// Average bytes per key line (matches server-side estimation in key_import_service.go)
const ESTIMATED_BYTES_PER_KEY = 170;
// Maximum file size for upload (MB) - should match backend MAX_REQUEST_BODY_SIZE_MB
// Note: Hardcoded for simplicity. While fetching from backend would ensure sync,
// it adds complexity (extra API call, loading state, error handling) for a value
// that rarely changes. The backend will reject oversized requests anyway.
const MAX_FILE_SIZE_MB = 150;

interface Props {
  show: boolean;
  groupId: number;
  groupName?: string;
}

interface Emits {
  (e: "update:show", value: boolean): void;
  (e: "success"): void;
}

const props = defineProps<Props>();

const emit = defineEmits<Emits>();

const { t } = useI18n();

const loading = ref(false);
const keysText = ref("");
const inputMode = ref<"text" | "file">("text");
const selectedFile = ref<File | null>(null);
const fileContent = ref("");
const fileInputRef = ref<HTMLInputElement | null>(null);
const estimatedKeyCount = ref(0); // For large files, estimate without reading entire file

// Computed property to check if submit is enabled
const canSubmit = computed(() => {
  if (inputMode.value === "text") {
    return keysText.value.trim().length > 0;
  } else {
    // For file mode, check if file is selected (not fileContent)
    // Large files don't load content into memory
    return selectedFile.value !== null;
  }
});

// Computed property for key count to avoid recalculation on re-renders
const keyCount = computed(() => {
  // For large files, use estimated count
  if (estimatedKeyCount.value > 0) {
    return estimatedKeyCount.value;
  }
  // For small files, calculate from content
  if (!fileContent.value) {
    return 0;
  }
  return fileContent.value.split("\n").filter(line => line.trim()).length;
});

// Watch dialog show state
watch(
  () => props.show,
  show => {
    if (show) {
      resetForm();
    }
  }
);

// Reset form
function resetForm() {
  keysText.value = "";
  inputMode.value = "text";
  selectedFile.value = null;
  fileContent.value = "";
  estimatedKeyCount.value = 0;
  if (fileInputRef.value) {
    fileInputRef.value.value = "";
  }
}

// Close dialog
function handleClose() {
  emit("update:show", false);
}

// Trigger file selection
function handleSelectFile() {
  fileInputRef.value?.click();
}

// Handle file selection
async function handleFileChange(event: Event) {
  const target = event.target as HTMLInputElement;
  const file = target.files?.[0];

  if (!file) {
    return;
  }

  // Check file size (limit to 150MB to support large key files)
  // Note: This limit should ideally match the backend MAX_REQUEST_BODY_SIZE_MB configuration
  // TODO: Consider fetching this limit from backend API to avoid configuration drift
  const maxSize = MAX_FILE_SIZE_MB * 1024 * 1024;
  if (file.size > maxSize) {
    window.$message.error(t("keys.fileSizeExceeded"));
    handleClearFile(); // Clear stale file state
    target.value = "";
    return;
  }

  // Check file type (only .txt files, case-insensitive)
  if (!file.name.toLowerCase().endsWith(".txt")) {
    window.$message.error(t("keys.invalidFileType"));
    handleClearFile(); // Clear stale file state
    target.value = "";
    return;
  }

  try {
    selectedFile.value = file;
    const fileSizeMB = file.size / (1024 * 1024);

    // For large files (>LARGE_FILE_THRESHOLD_MB), estimate key count without reading entire file
    // This prevents loading 150MB into browser memory
    if (fileSizeMB > LARGE_FILE_THRESHOLD_MB) {
      // Estimate using ESTIMATED_BYTES_PER_KEY (same as server-side calculation)
      estimatedKeyCount.value = Math.floor(file.size / ESTIMATED_BYTES_PER_KEY);
      fileContent.value = ""; // Don't store content for large files
      window.$message.success(
        `${t("keys.fileLoadedSuccess", {
          name: file.name,
        })} (${fileSizeMB.toFixed(2)}MB, ~${estimatedKeyCount.value.toLocaleString()} keys)`
      );
    } else {
      // For small files, read content for accurate key count
      const text = await file.text();
      // Guard against race condition if user selected a different file during read
      if (selectedFile.value !== file) {
        return;
      }
      fileContent.value = text;
      estimatedKeyCount.value = 0;
      window.$message.success(t("keys.fileLoadedSuccess", { name: file.name }));
    }

    inputMode.value = "file";
  } catch (_error) {
    window.$message.error(t("keys.fileReadError"));
    selectedFile.value = null;
    fileContent.value = "";
    estimatedKeyCount.value = 0;
    inputMode.value = "text";
    target.value = "";
  }
}

// Clear selected file
function handleClearFile() {
  selectedFile.value = null;
  fileContent.value = "";
  estimatedKeyCount.value = 0;
  inputMode.value = "text";
  if (fileInputRef.value) {
    fileInputRef.value.value = "";
  }
}

// Submit form
async function handleSubmit() {
  if (loading.value || !canSubmit.value) {
    return;
  }

  try {
    loading.value = true;

    // Determine which API to use based on input mode and file size
    if (inputMode.value === "file" && selectedFile.value) {
      const fileSizeMB = selectedFile.value.size / (1024 * 1024);

      // Show appropriate message based on file size
      if (fileSizeMB > LARGE_FILE_THRESHOLD_MB) {
        // Large file - show info about streaming import
        window.$message.info(t("keys.largeFileImportStarting", { size: fileSizeMB.toFixed(2) }), {
          duration: 3000,
        });
        await keysApi.addKeysAsyncStream(props.groupId, selectedFile.value);
      } else {
        // Small file - use regular JSON API
        await keysApi.addKeysAsync(props.groupId, fileContent.value);
      }
    } else {
      // Text input always uses JSON API
      await keysApi.addKeysAsync(props.groupId, keysText.value);
    }

    // Show task started message and trigger polling
    window.$message.info(t("keys.importTaskStarted"), { duration: 5000 });
    appState.taskPollingTrigger++;

    // Emit success event to notify parent component
    emit("success");

    // Close dialog and reset form
    resetForm();
    handleClose();
  } catch (error) {
    // Error is already handled by http interceptor
    console.error("Import failed:", error);
  } finally {
    loading.value = false;
  }
}
</script>

<template>
  <n-modal :show="show" @update:show="handleClose" class="form-modal">
    <n-card
      style="width: 800px"
      :title="t('keys.addKeysToGroup', { group: groupName || t('keys.currentGroup') })"
      :bordered="false"
      size="huge"
      role="dialog"
      aria-modal="true"
    >
      <template #header-extra>
        <n-button quaternary circle @click="handleClose">
          <template #icon>
            <n-icon :component="Close" />
          </template>
        </n-button>
      </template>

      <!-- Hidden file input -->
      <input
        ref="fileInputRef"
        type="file"
        accept=".txt"
        style="display: none"
        @change="handleFileChange"
      />

      <!-- File import button -->
      <div style="margin-top: 20px; margin-bottom: 12px">
        <n-button @click="handleSelectFile" :disabled="loading">
          <template #icon>
            <n-icon :component="DocumentText" />
          </template>
          {{ t("keys.importFromFile") }}
        </n-button>
      </div>

      <!-- File info display (when file is selected) -->
      <n-alert
        v-if="inputMode === 'file' && selectedFile"
        type="info"
        style="margin-bottom: 12px"
        closable
        @close="handleClearFile"
      >
        <template #header>{{ t("keys.fileSelected") }}</template>
        <div>
          <div>{{ t("keys.fileName") }}: {{ selectedFile.name }}</div>
          <div>
            {{ t("keys.fileSize") }}:
            {{
              selectedFile.size > 1024 * 1024
                ? (selectedFile.size / (1024 * 1024)).toFixed(2) + " MB"
                : (selectedFile.size / 1024).toFixed(2) + " KB"
            }}
          </div>
          <div>
            {{ t("keys.keyCount") }}: {{ estimatedKeyCount > 0 ? "~" : ""
            }}{{ keyCount.toLocaleString() }}
          </div>
        </div>
      </n-alert>

      <!-- Text input (only show when in text mode) -->
      <n-input
        v-if="inputMode === 'text'"
        v-model:value="keysText"
        type="textarea"
        :placeholder="t('keys.enterKeysPlaceholder')"
        :rows="8"
      />

      <template #footer>
        <div style="display: flex; justify-content: flex-end; gap: 12px">
          <n-button @click="handleClose">{{ t("common.cancel") }}</n-button>
          <n-button type="primary" @click="handleSubmit" :loading="loading" :disabled="!canSubmit">
            {{ t("common.create") }}
          </n-button>
        </div>
      </template>
    </n-card>
  </n-modal>
</template>

<style scoped>
.form-modal {
  --n-color: rgba(255, 255, 255, 0.95);
}

:deep(.n-input) {
  --n-border-radius: 6px;
}

:deep(.n-card-header) {
  border-bottom: 1px solid rgba(239, 239, 245, 0.8);
  padding: 10px 20px;
}

:deep(.n-card__content) {
  max-height: calc(100vh - 68px - 61px - 50px);
  overflow-y: auto;
}

:deep(.n-card__footer) {
  border-top: 1px solid rgba(239, 239, 245, 0.8);
  padding: 10px 15px;
}
</style>
