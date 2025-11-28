<script setup lang="ts">
import { copy } from "@/utils/clipboard";
import { Copy, Key } from "@vicons/ionicons5";
import { NButton, NIcon, NInput, NInputNumber, NModal, NSpace, useMessage } from "naive-ui";
import { nextTick, ref } from "vue";
import { useI18n } from "vue-i18n";

interface Props {
  modelValue: string;
  placeholder?: string;
  size?: "small" | "medium" | "large";
}

// Open manual copy fallback modal
function openManualCopyModal(text: string) {
  manualCopyText.value = text;
  showManualCopyModal.value = true;

  // Auto-select content to let user press Ctrl+C / Command+C quickly
  nextTick(() => {
    const textarea = manualCopyTextareaRef.value;
    if (textarea) {
      textarea.focus();
      textarea.select();
    }
  });
}

interface Emits {
  (e: "update:modelValue", value: string): void;
}

const { t } = useI18n();

const props = withDefaults(defineProps<Props>(), {
  size: "small",
});

const emit = defineEmits<Emits>();

const message = useMessage();

// Key generator modal state
const showKeyGeneratorModal = ref(false);
const keyCount = ref(1);
const isGenerating = ref(false);

// Manual copy fallback modal state
const showManualCopyModal = ref(false);
const manualCopyText = ref("");
const manualCopyTextareaRef = ref<HTMLTextAreaElement | null>(null);

// Generate random string
function generateRandomString(length: number): string {
  const chars = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789-_";
  const charsLength = chars.length;
  let result = "";

  // Prefer cryptographically strong random generator when available
  if (typeof crypto !== "undefined" && typeof crypto.getRandomValues === "function") {
    const randomValues = new Uint32Array(length);
    crypto.getRandomValues(randomValues);
    for (let i = 0; i < length; i++) {
      const value = randomValues[i]!;
      result += chars.charAt(value % charsLength);
    }
    return result;
  }

  for (let i = 0; i < length; i++) {
    result += chars.charAt(Math.floor(Math.random() * charsLength));
  }

  return result;
}

// Generate keys
function generateKeys(): string[] {
  const keys: string[] = [];
  // Defensive bounds check (UI already enforces 1-100 via NInputNumber)
  const count = Math.min(Math.max(keyCount.value, 1), 100);
  for (let i = 0; i < count; i++) {
    keys.push(`sk-${generateRandomString(48)}`);
  }
  return keys;
}

// Open key generator modal
function openKeyGenerator() {
  showKeyGeneratorModal.value = true;
  keyCount.value = 1;
}

// Confirm key generation
function confirmGenerateKeys() {
  if (isGenerating.value) {
    return;
  }

  try {
    isGenerating.value = true;
    const newKeys = generateKeys();
    const currentValue = props.modelValue || "";

    let updatedValue = currentValue.trim();

    // Handle comma compatibility
    if (updatedValue && !updatedValue.endsWith(",")) {
      updatedValue += ",";
    }

    // Append newly generated keys
    if (updatedValue) {
      updatedValue += newKeys.join(",");
    } else {
      updatedValue = newKeys.join(",");
    }

    emit("update:modelValue", updatedValue);
    showKeyGeneratorModal.value = false;

    message.success(t("keys.keysGeneratedSuccess", { count: newKeys.length }));
  } finally {
    isGenerating.value = false;
  }
}

// Copy proxy keys
async function copyProxyKeys() {
  const proxyKeys = props.modelValue || "";
  if (!proxyKeys.trim()) {
    message.warning(t("keys.noKeysToCopy"));
    return;
  }

  // Convert comma/newline-separated keys to newline-separated text
  const formattedKeys = proxyKeys
    .split(/[\n,]+/)
    .map(key => key.trim())
    .filter(key => key.length > 0)
    .join("\n");

  if (!formattedKeys.trim()) {
    message.warning(t("keys.noKeysToCopy"));
    return;
  }

  // We still attempt programmatic copy in insecure contexts to opportunistically
  // update the clipboard when the browser allows it, even though we always show
  // the manual fallback dialog for transparency.
  const success = await copy(formattedKeys);
  const isSecureContext = typeof window !== "undefined" && window.isSecureContext;

  if (success && isSecureContext) {
    message.success(t("keys.keysCopiedToClipboard"));
    return;
  }

  openManualCopyModal(formattedKeys);
  message.error(t("keys.copyFailedManual"));
}

// Handle input value changes
function handleInput(value: string) {
  emit("update:modelValue", value);
}
</script>

<template>
  <div class="proxy-keys-input">
    <n-input
      :value="modelValue"
      :placeholder="placeholder || t('keys.multiKeysPlaceholder')"
      clearable
      :size="size"
      @update:value="handleInput"
    >
      <template #suffix>
        <n-space :size="4" :wrap-item="false">
          <n-button text type="primary" :size="size" @click="openKeyGenerator">
            <template #icon>
              <n-icon :component="Key" />
            </template>
            {{ t("keys.generate") }}
          </n-button>
          <n-button text type="tertiary" :size="size" @click="copyProxyKeys" style="opacity: 0.7">
            <template #icon>
              <n-icon :component="Copy" />
            </template>
            {{ t("common.copy") }}
          </n-button>
        </n-space>
      </template>
    </n-input>

    <!-- Key generator modal -->
    <n-modal
      v-model:show="showKeyGeneratorModal"
      preset="dialog"
      :title="t('keys.generateProxyKeys')"
      :positive-text="t('keys.confirmGenerate')"
      :negative-text="t('common.cancel')"
      :positive-button-props="{ loading: isGenerating }"
      @positive-click="confirmGenerateKeys"
    >
      <n-space vertical :size="16">
        <div>
          <p style="margin: 0 0 8px 0; color: #666; font-size: 14px">
            {{ t("keys.enterKeysCount") }}
          </p>
          <n-input-number
            v-model:value="keyCount"
            :min="1"
            :max="100"
            :placeholder="t('keys.enterCountPlaceholder')"
            style="width: 100%"
            :disabled="isGenerating"
          />
        </div>
        <div style="color: #999; font-size: 12px; line-height: 1.4">
          <p>{{ t("keys.generatedKeysWillAppend") }}</p>
        </div>
      </n-space>
    </n-modal>

    <!-- Manual copy fallback modal for insecure contexts or clipboard failures -->
    <n-modal
      v-model:show="showManualCopyModal"
      preset="dialog"
      :title="t('common.copy')"
      :positive-text="t('common.close')"
      @positive-click="showManualCopyModal = false"
    >
      <p style="margin: 0 0 8px 0; color: #666; font-size: 14px">
        {{ t("keys.copyFailedManual") }}
      </p>
      <p style="margin: 0 0 12px 0; color: #999; font-size: 12px; line-height: 1.5">
        {{ t("keys.manualCopyHint") }}
      </p>
      <textarea
        ref="manualCopyTextareaRef"
        :value="manualCopyText"
        readonly
        style="
          width: 100%;
          min-height: 120px;
          font-family: monospace;
          font-size: 13px;
          resize: vertical;
          box-sizing: border-box;
        "
      />
    </n-modal>
  </div>
</template>

<style scoped>
.proxy-keys-input {
  width: 100%;
}
</style>
