<script setup lang="ts">
/**
 * AccessKeyModal Component
 * Create/Edit Hub access key form modal.
 */
import { CopyOutline, Search } from "@vicons/ionicons5";
import { useClipboard } from "@vueuse/core";
import {
  NAlert,
  NButton,
  NCheckbox,
  NEmpty,
  NForm,
  NFormItem,
  NIcon,
  NInput,
  NModal,
  NRadio,
  NRadioGroup,
  NSpace,
  NSpin,
  NText,
  useMessage,
  type FormInst,
  type FormRules,
} from "naive-ui";
import { computed, reactive, ref, watch } from "vue";
import { useI18n } from "vue-i18n";
import { hubApi } from "../api/hub";
import type { HubAccessKey, ModelPoolEntry } from "../types/hub";

const { copy } = useClipboard();

interface Props {
  show: boolean;
  editKey?: HubAccessKey | null;
}

interface Emits {
  (e: "update:show", value: boolean): void;
  (e: "success"): void;
}

const props = withDefaults(defineProps<Props>(), {
  editKey: null,
});

const emit = defineEmits<Emits>();

const { t } = useI18n();
const message = useMessage();
const formRef = ref<FormInst | null>(null);
const loading = ref(false);
const loadingModels = ref(false);
const availableModels = ref<ModelPoolEntry[]>([]);
const modelSearchText = ref("");

// Created key value (only shown after creation)
const createdKeyValue = ref<string | null>(null);
const keyCopied = ref(false);

// Form data
const formData = reactive({
  name: "",
  key_value: "",
  allowed_models_mode: "all" as "all" | "specific",
  allowed_models: [] as string[],
  enabled: true,
});

// Form validation rules
const rules: FormRules = {
  name: [
    {
      required: true,
      message: t("hub.keyNamePlaceholder"),
      trigger: ["blur", "input"],
    },
  ],
};

// Computed
const isEditMode = computed(() => !!props.editKey);

const modalTitle = computed(() =>
  isEditMode.value ? t("hub.editAccessKey") : t("hub.createAccessKey")
);

const filteredModels = computed(() => {
  if (!modelSearchText.value.trim()) {
    return availableModels.value;
  }
  const keyword = modelSearchText.value.toLowerCase().trim();
  return availableModels.value.filter(model => model.model_name.toLowerCase().includes(keyword));
});

// Watch modal visibility
watch(
  () => props.show,
  async show => {
    if (show) {
      resetForm();
      if (props.editKey) {
        loadEditData();
      }
      await loadAvailableModels();
    }
  }
);

// Reset form
function resetForm() {
  formRef.value?.restoreValidation();
  createdKeyValue.value = null;
  keyCopied.value = false;
  Object.assign(formData, {
    name: "",
    key_value: "",
    allowed_models_mode: "all",
    allowed_models: [],
    enabled: true,
  });
  modelSearchText.value = "";
}

// Load edit data
function loadEditData() {
  if (!props.editKey) {
    return;
  }
  Object.assign(formData, {
    name: props.editKey.name,
    key_value: "",
    allowed_models_mode: props.editKey.allowed_models_mode,
    allowed_models: [...props.editKey.allowed_models],
    enabled: props.editKey.enabled,
  });
}

// Load available models
async function loadAvailableModels() {
  loadingModels.value = true;
  try {
    const models = await hubApi.getAllModels();
    availableModels.value = models;
  } catch (error) {
    console.error("Failed to load models:", error);
    availableModels.value = [];
  } finally {
    loadingModels.value = false;
  }
}

// Toggle model selection
function toggleModel(modelName: string, checked: boolean) {
  if (checked) {
    if (!formData.allowed_models.includes(modelName)) {
      formData.allowed_models.push(modelName);
    }
  } else {
    const index = formData.allowed_models.indexOf(modelName);
    if (index > -1) {
      formData.allowed_models.splice(index, 1);
    }
  }
}

// Select all filtered models
function selectAllFiltered() {
  filteredModels.value.forEach(model => {
    if (!formData.allowed_models.includes(model.model_name)) {
      formData.allowed_models.push(model.model_name);
    }
  });
}

// Deselect all filtered models
function deselectAllFiltered() {
  const filteredNames = new Set(filteredModels.value.map(m => m.model_name));
  formData.allowed_models = formData.allowed_models.filter(name => !filteredNames.has(name));
}

// Copy key to clipboard
async function copyKeyValue() {
  if (!createdKeyValue.value) {
    return;
  }
  try {
    await copy(createdKeyValue.value);
    keyCopied.value = true;
    message.success(t("common.copied"));
  } catch {
    message.error(t("keys.copyFailed"));
  }
}

// Close modal
function handleClose() {
  emit("update:show", false);
}

// Submit form
async function handleSubmit() {
  if (loading.value) {
    return;
  }

  try {
    await formRef.value?.validate();
  } catch {
    return;
  }

  loading.value = true;
  try {
    const params = {
      name: formData.name.trim(),
      allowed_models: formData.allowed_models_mode === "all" ? [] : formData.allowed_models,
      enabled: formData.enabled,
    };

    if (isEditMode.value && props.editKey) {
      await hubApi.updateAccessKey(props.editKey.id, params);
      message.success(t("hub.accessKeyUpdated"));
      emit("success");
      handleClose();
    } else {
      const result = await hubApi.createAccessKey({
        ...params,
        key_value: formData.key_value.trim() || undefined,
      });
      // Show the created key value
      createdKeyValue.value = result.key_value;
      message.success(t("hub.accessKeyCreated"));
      emit("success");
    }
  } catch (error) {
    console.error("Failed to save access key:", error);
    message.error(t("common.saveFailed"));
  } finally {
    loading.value = false;
  }
}
</script>

<template>
  <n-modal
    :show="show"
    @update:show="value => emit('update:show', value)"
    preset="card"
    :title="modalTitle"
    :style="{ width: '600px', maxHeight: '80vh' }"
    :bordered="false"
    :segmented="{ content: 'soft', footer: 'soft' }"
  >
    <!-- Show created key value -->
    <template v-if="createdKeyValue">
      <n-space vertical :size="16">
        <n-alert type="success" :title="t('hub.accessKeyCreated')">
          {{ t("hub.keyCreatedCopyHint") }}
        </n-alert>
        <div class="key-display">
          <code class="key-value">{{ createdKeyValue }}</code>
          <n-button :type="keyCopied ? 'success' : 'primary'" size="small" @click="copyKeyValue">
            <template #icon>
              <n-icon :component="CopyOutline" />
            </template>
            {{ keyCopied ? t("common.copied") : t("common.copy") }}
          </n-button>
        </div>
        <n-alert type="warning" :bordered="false">
          {{ t("hub.keyOnlyShownOnce") }}
        </n-alert>
      </n-space>
    </template>

    <!-- Form -->
    <template v-else>
      <n-form
        ref="formRef"
        :model="formData"
        :rules="rules"
        label-placement="left"
        label-width="100"
      >
        <n-form-item :label="t('hub.keyName')" path="name">
          <n-input v-model:value="formData.name" :placeholder="t('hub.keyNamePlaceholder')" />
        </n-form-item>

        <n-form-item v-if="!isEditMode" :label="t('hub.keyValue')" path="key_value">
          <n-input v-model:value="formData.key_value" :placeholder="t('hub.keyValuePlaceholder')" />
          <template #feedback>
            <n-text depth="3" style="font-size: 12px">
              {{ t("hub.keyValueHint") }}
            </n-text>
          </template>
        </n-form-item>

        <n-form-item :label="t('hub.allowedModelsMode')">
          <n-radio-group v-model:value="formData.allowed_models_mode">
            <n-space vertical>
              <n-radio value="all">{{ t("hub.allowedModelsModeAll") }}</n-radio>
              <n-radio value="specific">{{ t("hub.allowedModelsModeSpecific") }}</n-radio>
            </n-space>
          </n-radio-group>
        </n-form-item>

        <n-form-item
          v-if="formData.allowed_models_mode === 'specific'"
          :label="t('hub.selectAllowedModels')"
        >
          <div class="model-selector">
            <div class="model-selector-header">
              <n-input
                v-model:value="modelSearchText"
                :placeholder="t('hub.searchModelsPlaceholder')"
                clearable
                size="small"
                style="width: 200px"
              >
                <template #prefix>
                  <n-icon :component="Search" />
                </template>
              </n-input>
              <n-space size="small">
                <n-button size="tiny" quaternary @click="selectAllFiltered">
                  {{ t("common.selectAll") }}
                </n-button>
                <n-button size="tiny" quaternary @click="deselectAllFiltered">
                  {{ t("common.deselectAll") }}
                </n-button>
              </n-space>
              <n-text depth="3" style="font-size: 12px">
                {{ t("hub.selectedModelsCount", { count: formData.allowed_models.length }) }}
              </n-text>
            </div>

            <n-spin :show="loadingModels">
              <div class="model-list">
                <div v-for="model in filteredModels" :key="model.model_name" class="model-item">
                  <n-checkbox
                    :checked="formData.allowed_models.includes(model.model_name)"
                    @update:checked="checked => toggleModel(model.model_name, checked)"
                  >
                    <code class="model-name">{{ model.model_name }}</code>
                  </n-checkbox>
                </div>
                <n-empty
                  v-if="filteredModels.length === 0 && !loadingModels"
                  :description="t('hub.noModels')"
                  size="small"
                />
              </div>
            </n-spin>
          </div>
        </n-form-item>
      </n-form>
    </template>

    <template #footer>
      <n-space justify="end">
        <n-button @click="handleClose">
          {{ createdKeyValue ? t("common.close") : t("common.cancel") }}
        </n-button>
        <n-button v-if="!createdKeyValue" type="primary" :loading="loading" @click="handleSubmit">
          {{ t("common.save") }}
        </n-button>
      </n-space>
    </template>
  </n-modal>
</template>

<style scoped>
.model-selector {
  width: 100%;
}

.model-selector-header {
  display: flex;
  align-items: center;
  gap: 12px;
  margin-bottom: 12px;
  flex-wrap: wrap;
}

.model-list {
  max-height: 250px;
  overflow-y: auto;
  border: 1px solid var(--n-border-color);
  border-radius: 4px;
  padding: 8px;
}

.model-item {
  padding: 4px 8px;
  border-radius: 4px;
  transition: background-color 0.2s;
}

.model-item:hover {
  background: var(--n-color-hover);
}

.model-name {
  font-size: 12px;
  background: transparent;
  padding: 0;
}

.key-display {
  display: flex;
  align-items: center;
  gap: 12px;
  padding: 16px;
  background: var(--n-color-modal);
  border: 1px solid var(--n-border-color);
  border-radius: 8px;
}

.key-value {
  flex: 1;
  font-size: 14px;
  font-family: "SF Mono", "Monaco", "Inconsolata", "Fira Mono", "Droid Sans Mono", monospace;
  word-break: break-all;
  background: transparent;
  padding: 0;
  user-select: all;
}
</style>
