<script setup lang="ts">
import { keysApi } from "@/api/keys";
import ProxyKeysInput from "@/components/common/ProxyKeysInput.vue";
import {
  type ChannelType,
  type Group,
  type PreconditionItem,
  type PreconditionOption,
} from "@/types/models";
import { Add, Close, Remove } from "@vicons/ionicons5";
import {
  NButton,
  NCard,
  NForm,
  NFormItem,
  NIcon,
  NInput,
  NInputNumber,
  NModal,
  NSelect,
  NTag,
  useMessage,
  type FormInst,
  type FormRules,
  type SelectOption,
} from "naive-ui";
import { computed, reactive, ref, watch } from "vue";
import { useI18n } from "vue-i18n";

// Type definitions for better type safety
// Note: These are component-local interfaces. While shared types could be considered,
// GroupConfig here is specifically for this modal's config structure (max_retries, sub_max_retries)
// which differs from the broader GroupConfigOption in models.ts. ApiError is a simple
// error wrapper specific to this component's API mutation pattern.
interface GroupConfig {
  max_retries?: number;
  sub_max_retries?: number;
}

interface ApiError {
  response?: {
    data?: {
      message?: string;
    };
  };
}

interface Props {
  show: boolean;
  group?: Group | null;
}

interface Emits {
  (e: "update:show", value: boolean): void;
  (e: "success", value: Group): void;
}

const props = withDefaults(defineProps<Props>(), {
  group: null,
});

const emit = defineEmits<Emits>();

const { t } = useI18n();
const message = useMessage();
const loading = ref(false);
const formRef = ref<FormInst | null>(null);

// Channel type options
const channelTypeOptions = [
  { label: "OpenAI", value: "openai" as ChannelType },
  { label: "Gemini", value: "gemini" as ChannelType },
  { label: "Anthropic", value: "anthropic" as ChannelType },
  { label: "Codex", value: "codex" as ChannelType },
];

// Precondition options (可扩展)
const preconditionOptions = computed<PreconditionOption[]>(() => [
  {
    key: "max_request_size_kb",
    label: t("keys.preconditionMaxRequestSize"),
    description: t("keys.maxRequestSizeHint"),
    default_value: 0, // 0 = no limit
    min: 0,
    max: 153600, // 150 MB (aligned with backend MAX_REQUEST_BODY_SIZE_MB default)
    unit: t("keys.maxRequestSizeUnit"),
  },
]);

// Get available precondition options (exclude already added)
const availablePreconditionOptions = computed<SelectOption[]>(() => {
  const addedKeys = new Set(formData.preconditionItems.map(item => item.key));
  return preconditionOptions.value
    .filter(opt => !addedKeys.has(opt.key))
    .map(opt => ({
      label: opt.label,
      value: opt.key,
    }));
});

// Get precondition option by key
const getPreconditionOption = (key: string): PreconditionOption | undefined => {
  return preconditionOptions.value.find(opt => opt.key === key);
};

// Default form data
const defaultFormData = {
  name: "",
  display_name: "",
  description: "",
  channel_type: "openai" as ChannelType,
  sort: 1,
  proxy_keys: "",
  max_retries: 0,
  sub_max_retries: 0,
  preconditionItems: [] as PreconditionItem[], // Dynamic precondition list
};

// Reactive form data
const formData = reactive({ ...defaultFormData });

// Form validation rules
const rules: FormRules = {
  name: [
    {
      required: true,
      message: t("keys.enterGroupName"),
      trigger: ["blur", "input"],
    },
    {
      pattern: /^[a-z0-9_-]{1,100}$/,
      message: t("keys.groupNamePattern"),
      trigger: ["blur", "input"],
    },
  ],
  channel_type: [
    {
      required: true,
      message: t("keys.selectChannelType"),
      trigger: ["blur", "change"],
    },
  ],
};

// Watch dialog visibility
watch(
  () => props.show,
  show => {
    if (show) {
      // In create mode reset the form; in edit mode load data
      if (props.group) {
        loadGroupData();
      } else {
        resetForm();
      }
    }
  }
);

// Reset form
function resetForm() {
  Object.assign(formData, defaultFormData);
  formData.preconditionItems = [];
  formRef.value?.restoreValidation();
}

// Load group data (edit mode)
function loadGroupData() {
  if (!props.group) {
    return;
  }

  formRef.value?.restoreValidation();

  const config = (props.group.config || {}) as GroupConfig;
  const maxRetries = config.max_retries ?? 0;
  const subMaxRetries = config.sub_max_retries ?? 0;

  // Load preconditions as dynamic items
  const preconditionItems: PreconditionItem[] = [];
  if (props.group.preconditions) {
    for (const [key, value] of Object.entries(props.group.preconditions)) {
      if (typeof value === "number") {
        preconditionItems.push({ key, value });
      }
    }
  }

  Object.assign(formData, {
    name: props.group.name || "",
    display_name: props.group.display_name || "",
    description: props.group.description || "",
    channel_type: props.group.channel_type || "openai",
    sort: props.group.sort ?? 1,
    proxy_keys: props.group.proxy_keys || "",
    max_retries: maxRetries,
    sub_max_retries: subMaxRetries,
    preconditionItems,
  });
}

// Add precondition item
function addPreconditionItem() {
  if (availablePreconditionOptions.value.length === 0) {
    return;
  }
  const firstAvailable = availablePreconditionOptions.value[0];
  if (!firstAvailable) {
    return;
  }
  const option = getPreconditionOption(firstAvailable.value as string);
  if (option) {
    formData.preconditionItems.push({
      key: option.key,
      value: option.default_value,
    });
  }
}

// Remove precondition item
function removePreconditionItem(index: number) {
  formData.preconditionItems.splice(index, 1);
}

// Handle precondition key change
function handlePreconditionKeyChange(index: number, key: string) {
  const option = getPreconditionOption(key);
  const target = formData.preconditionItems[index];
  if (option && target) {
    target.value = option.default_value;
  }
}

// Close modal
function handleClose() {
  emit("update:show", false);
}

async function executeGroupMutation(
  action: "create" | "update",
  fn: () => Promise<Group>
): Promise<Group | null> {
  try {
    return await fn();
  } catch (error) {
    console.error(`Error ${action} group:`, error);
    // Extract API error message if available
    const apiMessage = (error as ApiError)?.response?.data?.message;
    message.error(apiMessage || t("common.operationFailed"));
    return null;
  }
}

// Submit form
async function handleSubmit() {
  if (loading.value) {
    return;
  }

  try {
    // Nested try-catch for validation deliberately separates validation errors from API errors.
    // AI review suggested flattening, but this pattern keeps validation logic scoped and allows
    // NaiveUI to handle validation display while preventing API calls on invalid forms.
    // Note: Validation errors are expected user behavior, not exceptions requiring logging.
    try {
      await formRef.value?.validate();
    } catch {
      // Validation errors are already displayed by NaiveUI form component
      return;
    }

    loading.value = true;

    // Build preconditions from dynamic items
    const preconditions: Record<string, number> = {};
    for (const item of formData.preconditionItems) {
      preconditions[item.key] = item.value;
    }

    // Build submit payload
    const submitData = {
      name: formData.name,
      display_name: formData.display_name,
      description: formData.description,
      channel_type: formData.channel_type,
      sort: formData.sort,
      proxy_keys: formData.proxy_keys,
      group_type: "aggregate" as const,
      config: {
        max_retries: formData.max_retries ?? 0,
        sub_max_retries: formData.sub_max_retries ?? 0,
      },
      preconditions,
    };

    let result: Group;
    if (props.group) {
      // Edit mode
      const groupId = props.group.id;
      if (!groupId) {
        message.error(t("keys.invalidGroup"));
        return;
      }
      const updated = await executeGroupMutation("update", () =>
        keysApi.updateGroup(groupId, submitData)
      );
      if (!updated) {
        return;
      }
      result = updated;
    } else {
      // Create mode
      const created = await executeGroupMutation("create", () => keysApi.createGroup(submitData));
      if (!created) {
        return;
      }
      result = created;
    }

    emit("success", result);
    handleClose();
  } finally {
    loading.value = false;
  }
}
</script>

<template>
  <n-modal :show="show" @update:show="handleClose" class="aggregate-group-modal">
    <n-card
      class="aggregate-group-card"
      :title="group ? t('keys.editAggregateGroup') : t('keys.createAggregateGroup')"
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

      <n-form
        ref="formRef"
        :model="formData"
        :rules="rules"
        label-placement="left"
        label-width="120px"
        class="aggregate-form"
        :style="{ '--n-label-height': '32px' }"
      >
        <!-- Basic information -->
        <div class="form-section">
          <h4 class="section-title">{{ t("keys.basicInfo") }}</h4>

          <n-form-item :label="t('keys.groupName')" path="name">
            <n-input
              v-model:value="formData.name"
              :placeholder="t('keys.groupNamePlaceholder')"
              clearable
            />
          </n-form-item>

          <n-form-item :label="t('keys.displayName')">
            <n-input
              v-model:value="formData.display_name"
              :placeholder="t('keys.displayNamePlaceholder')"
              clearable
            />
          </n-form-item>

          <n-form-item :label="t('keys.channelType')" path="channel_type">
            <n-select
              v-model:value="formData.channel_type"
              :options="channelTypeOptions"
              :placeholder="t('keys.selectChannelType')"
              :disabled="!!props.group"
            />
          </n-form-item>

          <n-form-item :label="t('keys.sortOrder')">
            <n-input-number
              v-model:value="formData.sort"
              :placeholder="t('keys.sortValue')"
              style="width: 100%"
            />
          </n-form-item>

          <n-form-item :label="t('keys.maxRetries')">
            <n-input-number
              v-model:value="formData.max_retries"
              :placeholder="t('keys.maxRetriesPlaceholder')"
              :min="0"
              :max="50"
              style="width: 100%"
            />
          </n-form-item>

          <n-form-item :label="t('keys.subMaxRetries')">
            <n-input-number
              v-model:value="formData.sub_max_retries"
              :placeholder="t('keys.subMaxRetriesPlaceholder')"
              :min="0"
              :max="50"
              style="width: 100%"
            />
          </n-form-item>

          <n-form-item :label="t('keys.proxyKeys')">
            <proxy-keys-input v-model="formData.proxy_keys" />
          </n-form-item>

          <n-form-item :label="t('common.description')">
            <n-input
              v-model:value="formData.description"
              type="textarea"
              placeholder=""
              :rows="1"
              :autosize="{ minRows: 1, maxRows: 5 }"
              style="resize: none"
            />
          </n-form-item>

          <!-- Dynamic preconditions section -->
          <div v-if="formData.preconditionItems.length > 0" class="preconditions-section">
            <h5 class="subsection-title">
              {{ t("keys.preconditions") }}
              <n-tag size="tiny" type="info" style="margin-left: 8px">
                {{ t("keys.optional") }}
              </n-tag>
            </h5>

            <n-form-item
              v-for="(item, index) in formData.preconditionItems"
              :key="index"
              class="precondition-row"
            >
              <template #label>
                <div class="precondition-label">{{ t("keys.precondition") }} {{ index + 1 }}</div>
              </template>
              <div class="precondition-content">
                <div class="precondition-select">
                  <n-select
                    v-model:value="item.key"
                    :options="[
                      ...availablePreconditionOptions,
                      {
                        label: getPreconditionOption(item.key)?.label || item.key,
                        value: item.key,
                      },
                    ]"
                    :placeholder="t('keys.selectPrecondition')"
                    @update:value="(val: string) => handlePreconditionKeyChange(index, val)"
                    style="width: 200px"
                  />
                </div>
                <div class="precondition-value">
                  <n-input-number
                    v-model:value="item.value"
                    :placeholder="t('keys.preconditionValue')"
                    :min="getPreconditionOption(item.key)?.min ?? 0"
                    :max="getPreconditionOption(item.key)?.max ?? 999999"
                    style="width: 100%"
                  >
                    <template #suffix v-if="getPreconditionOption(item.key)?.unit">
                      <span style="color: var(--text-secondary)">
                        {{ getPreconditionOption(item.key)?.unit }}
                      </span>
                    </template>
                  </n-input-number>
                </div>
                <div class="precondition-actions">
                  <n-button
                    @click="removePreconditionItem(index)"
                    type="error"
                    quaternary
                    circle
                    size="small"
                  >
                    <template #icon>
                      <n-icon :component="Remove" />
                    </template>
                  </n-button>
                </div>
              </div>
              <template #feedback v-if="getPreconditionOption(item.key)?.description">
                <span style="color: var(--text-secondary); font-size: 12px">
                  {{ getPreconditionOption(item.key)?.description }}
                </span>
              </template>
            </n-form-item>
          </div>

          <!-- Add precondition button -->
          <div style="margin-top: 12px; padding-left: 100px">
            <n-button
              @click="addPreconditionItem"
              dashed
              style="width: 100%"
              :disabled="availablePreconditionOptions.length === 0"
            >
              <template #icon>
                <n-icon :component="Add" />
              </template>
              {{ t("keys.addPrecondition") }}
            </n-button>
          </div>
        </div>
      </n-form>

      <template #footer>
        <div style="display: flex; justify-content: flex-end; gap: 12px">
          <n-button @click="handleClose">{{ t("common.cancel") }}</n-button>
          <n-button type="primary" @click="handleSubmit" :loading="loading">
            {{ group ? t("common.update") : t("common.create") }}
          </n-button>
        </div>
      </template>
    </n-card>
  </n-modal>
</template>

<style scoped>
.aggregate-group-modal {
  width: 600px;
}

.form-section {
  margin-top: 8px;
}

.form-section:first-child {
  margin-top: 0;
}

.section-title {
  font-size: 1rem;
  font-weight: 600;
  color: var(--text-primary);
  margin-bottom: 8px;
  padding-bottom: 2px;
  border-bottom: 1px solid var(--border-color);
}

.subsection-title {
  font-size: 0.9rem;
  font-weight: 600;
  color: var(--text-primary);
  margin-bottom: 8px;
  display: flex;
  align-items: center;
}

.preconditions-section {
  margin-top: 16px;
  margin-bottom: 8px;
}

.precondition-row {
  margin-bottom: 8px !important;
}

.precondition-content {
  display: flex;
  gap: 8px;
  align-items: center;
  width: 100%;
}

.precondition-select {
  flex-shrink: 0;
}

.precondition-value {
  flex: 1;
  min-width: 0;
}

.precondition-actions {
  flex-shrink: 0;
}

.precondition-label {
  font-weight: 500;
  color: var(--text-primary);
}

/* ===== CRITICAL FIX: Force compact form spacing ===== */
/* Note: --n-feedback-height: 0 intentionally set to minimize vertical spacing.
 * AI review suggested this could hide validation feedback, but this approach is
 * based on a prior validated compact layout solution that successfully passed testing.
 * The compact layout is a design requirement for this admin panel.
 * Validation errors are still visible inline due to NaiveUI's internal rendering. */
:deep(.n-form-item) {
  margin-bottom: 8px !important;
  --n-feedback-height: 0 !important;
}

:deep(.n-form-item-label) {
  font-weight: 500;
  color: var(--text-primary);
  display: flex;
  align-items: center;
  height: 32px;
  line-height: 32px;
}

/* Fix required mark vertical alignment */
:deep(.n-form-item-label__asterisk) {
  display: flex;
  align-items: center;
  height: 32px;
}

:deep(.n-form-item-blank) {
  display: flex;
  align-items: center;
  min-height: 32px;
}

:deep(.n-input),
:deep(.n-input-number) {
  --n-height: 32px;
}

:deep(.n-base-selection-label) {
  height: 32px;
  line-height: 32px;
  display: flex;
  align-items: center;
}

:deep(.n-base-selection) {
  --n-height: 32px;
}
</style>
