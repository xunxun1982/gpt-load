<script setup lang="ts">
import { keysApi } from "@/api/keys";
import ProxyKeysInput from "@/components/common/ProxyKeysInput.vue";
import { type ChannelType, type Group } from "@/types/models";
import { Close } from "@vicons/ionicons5";
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
  useMessage,
  type FormRules,
  type FormInst,
} from "naive-ui";
import { reactive, ref, watch } from "vue";
import { useI18n } from "vue-i18n";

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
];

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
  formRef.value?.restoreValidation();
}

// Load group data (edit mode)
function loadGroupData() {
  if (!props.group) {
    return;
  }

  formRef.value?.restoreValidation();

  const config = (props.group.config || {}) as Record<string, unknown>;
  const maxRetries = (config["max_retries"] as number | undefined) ?? 0;
  const subMaxRetries = (config["sub_max_retries"] as number | undefined) ?? 0;

  Object.assign(formData, {
    name: props.group.name || "",
    display_name: props.group.display_name || "",
    description: props.group.description || "",
    channel_type: props.group.channel_type || "openai",
    sort: props.group.sort ?? 1,
    proxy_keys: props.group.proxy_keys || "",
    max_retries: maxRetries,
    sub_max_retries: subMaxRetries,
  });
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
    const apiMessage = (error as { response?: { data?: { message?: string } } })?.response?.data
      ?.message;
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
    try {
      await formRef.value?.validate();
    } catch {
      // Validation errors are already displayed by the form
      return;
    }

    loading.value = true;

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
              :max="5"
              style="width: 100%"
            />
          </n-form-item>

          <n-form-item :label="t('keys.subMaxRetries')">
            <n-input-number
              v-model:value="formData.sub_max_retries"
              :placeholder="t('keys.subMaxRetriesPlaceholder')"
              :min="0"
              :max="5"
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

/* ===== CRITICAL FIX: Force compact form spacing ===== */
/* Using !important as last resort to override NaiveUI defaults */
:deep(.n-form-item) {
  margin-bottom: 8px !important;
  --n-feedback-height: 0 !important;
}

.aggregate-form :deep(.n-form-item) {
  margin-bottom: 12px !important;
}

:deep(.n-form .n-form-item) {
  margin-bottom: 12px !important;
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
