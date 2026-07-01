<script setup lang="ts">
import { keysApi } from "@/api/keys";
import type { Group, SubGroupInfo } from "@/types/models";
import { formatPercentage } from "@/utils/display";
import { getSubGroupHealthResetOptions } from "@/utils/health-reset";
import { Close } from "@vicons/ionicons5";
import {
  NButton,
  NCard,
  NForm,
  NFormItem,
  NIcon,
  NInputNumber,
  NModal,
  NSelect,
  useMessage,
  type FormInst,
  type FormRules,
} from "naive-ui";
import { computed, reactive, ref, watch } from "vue";
import { useI18n } from "vue-i18n";

interface Props {
  show: boolean;
  subGroup: SubGroupInfo | null;
  aggregateGroup: Group | null;
  subGroups: SubGroupInfo[]; // Current sub-group list
}

interface Emits {
  (e: "update:show", value: boolean): void;
  (e: "success"): void;
}

const props = defineProps<Props>();
const emit = defineEmits<Emits>();

const { t } = useI18n();
const message = useMessage();
const loading = ref(false);
const formRef = ref<FormInst | null>(null);
const healthResetOptions = computed(() => getSubGroupHealthResetOptions(t));

// Form data
const formData = reactive<{
  weight: number;
  min_effective_weight: number;
  health_reset_interval_seconds: number;
}>({
  weight: 0,
  min_effective_weight: 1,
  health_reset_interval_seconds: 0,
});

// Preview new weight percentage (assuming other sub-group weights stay unchanged)
// Only considers enabled sub-groups with weight > 0 for percentage calculation
const previewPercentage = computed<string>(() => {
  if (!props.subGroups || !props.subGroup) {
    return "0%";
  }

  // Check if current sub-group is enabled
  const currentEnabled = props.subGroup.group.enabled;

  // Calculate effective total weight (only enabled sub-groups with weight > 0)
  // Use dynamic effective weight for other sub-groups if available
  const totalWeight = props.subGroups.reduce((sum, sg) => {
    if (sg.group.id === props.subGroup?.group.id) {
      // For current sub-group, use new weight only if enabled
      return sum + (currentEnabled ? previewEffectiveWeight.value : 0);
    }
    // For other sub-groups, only count if enabled and has positive weight
    // Use effective weight from dynamic weight info if available
    if (sg.group.enabled && sg.weight > 0) {
      const weight = sg.dynamic_weight?.effective_weight ?? sg.weight;
      return sum + weight;
    }
    return sum;
  }, 0);

  // If current sub-group is disabled, percentage is always 0
  if (!currentEnabled) {
    return "0%";
  }

  if (totalWeight === 0) {
    return "0%";
  }

  const percentage = (previewEffectiveWeight.value / totalWeight) * 100;
  return formatPercentage(percentage);
});

const previewEffectiveWeight = computed(() => {
  if (!props.subGroup?.group.enabled || formData.weight <= 0) {
    return 0;
  }
  const dynamicWeight = Math.min(
    props.subGroup.dynamic_weight?.effective_weight ?? formData.weight,
    formData.weight
  );
  const minEffectiveWeight =
    formData.min_effective_weight > 0
      ? Math.min(formData.min_effective_weight, formData.weight)
      : 1;
  return Math.max(dynamicWeight, minEffectiveWeight);
});

// Form validation rules
const rules: FormRules = {
  weight: [
    {
      validator: (_rule, value) => {
        if (value === null || value === undefined || value === "") {
          return new Error(t("keys.enterWeight"));
        }
        if (value < 0) {
          return new Error(t("keys.weightCannotBeNegative"));
        }
        if (value > 5000) {
          return new Error(t("keys.weightMaxExceeded"));
        }
        return true;
      },
      trigger: ["blur", "input"],
    },
  ],
  min_effective_weight: [
    {
      validator: (_rule, value) => {
        if (formData.weight <= 0) {
          return true;
        }
        if (value === null || value === undefined || value === "") {
          return new Error(t("subGroups.enterMinEffectiveWeight"));
        }
        if (value < 1) {
          return new Error(t("subGroups.minEffectiveWeightCannotBeLessThanOne"));
        }
        if (value > formData.weight) {
          return new Error(t("subGroups.minEffectiveWeightCannotExceedWeight"));
        }
        return true;
      },
      trigger: ["blur", "input"],
    },
  ],
};

// Watch dialog visibility and sub-group changes
watch(
  () => [props.show, props.subGroup] as const,
  ([show, subGroup]) => {
    if (show && subGroup) {
      formData.weight = subGroup.weight;
      formData.min_effective_weight = subGroup.weight > 0 ? subGroup.min_effective_weight || 1 : 0;
      formData.health_reset_interval_seconds = subGroup.health_reset_interval_seconds ?? 0;
    }
  },
  { immediate: true }
);

watch(
  () => formData.weight,
  weight => {
    formData.min_effective_weight =
      weight > 0 ? Math.min(Math.max(formData.min_effective_weight || 1, 1), weight) : 0;
  }
);

// Close modal
function handleClose() {
  emit("update:show", false);
}

// Submit form
async function handleSubmit() {
  if (loading.value) {
    return;
  }

  const subGroupId = props.subGroup?.group.id;
  if (subGroupId === undefined) {
    message.error(t("keys.invalidSubGroup"));
    return;
  }

  const aggregateGroupId = props.aggregateGroup?.id;
  if (aggregateGroupId === undefined) {
    message.error(t("keys.invalidAggregateGroup"));
    return;
  }

  // Short-circuit on validation failure and guard against double-submit.
  loading.value = true;
  try {
    await formRef.value?.validate();
  } catch {
    loading.value = false;
    return;
  }

  try {
    await keysApi.updateSubGroupWeight(aggregateGroupId, subGroupId, {
      weight: formData.weight, // Integer weight value (already constrained by input precision)
      minEffectiveWeight:
        formData.weight > 0 ? Math.min(formData.min_effective_weight, formData.weight) : 0,
      healthResetIntervalSeconds: formData.health_reset_interval_seconds,
    });

    // Backend has already displayed a success message through API response, no need to repeat here
    emit("success");
    handleClose();
  } finally {
    loading.value = false;
  }
}

// Quickly adjust weight by delta
function adjustWeight(delta: number) {
  const newWeight = Math.max(0, Math.min(5000, formData.weight + delta));
  formData.weight = newWeight;
  formData.min_effective_weight =
    newWeight > 0 ? Math.min(Math.max(formData.min_effective_weight, 1), newWeight) : 0;
}
</script>

<template>
  <n-modal :show="show" @update:show="handleClose" class="edit-weight-modal">
    <n-card
      class="edit-weight-card"
      :title="t('keys.editWeight')"
      :bordered="false"
      size="medium"
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
      >
        <div class="form-section">
          <div class="sub-group-info">
            <h4 class="section-title">
              {{ t("keys.editingSubGroup") }}:
              <span class="group-name">
                {{ subGroup?.group.display_name || subGroup?.group.name }}
              </span>
            </h4>
            <div class="group-details">
              <span class="detail-item">
                {{ t("keys.groupId") }}: {{ subGroup?.group.id }} · {{ t("keys.currentWeight") }}:
                {{ subGroup?.weight }} ·
                {{ t("keys.weightRangeHint") }}
              </span>
            </div>
          </div>

          <n-form-item
            :label="t('keys.newWeight')"
            path="weight"
            class="compact-form-item"
            :show-feedback="false"
          >
            <div class="weight-input-section">
              <n-input-number
                v-model:value="formData.weight"
                :min="0"
                :max="5000"
                :precision="0"
                :placeholder="t('keys.enterWeight')"
                size="small"
                style="flex: 1"
              />
              <div class="quick-adjust">
                <n-button size="small" @click="adjustWeight(-10)" :disabled="formData.weight <= 0">
                  -10
                </n-button>
                <n-button size="small" @click="adjustWeight(-1)" :disabled="formData.weight <= 0">
                  -1
                </n-button>
                <n-button size="small" @click="adjustWeight(1)" :disabled="formData.weight >= 5000">
                  +1
                </n-button>
                <n-button
                  size="small"
                  @click="adjustWeight(10)"
                  :disabled="formData.weight >= 5000"
                >
                  +10
                </n-button>
              </div>
            </div>
          </n-form-item>

          <n-form-item
            :label="t('subGroups.minEffectiveWeight')"
            path="min_effective_weight"
            class="compact-form-item"
            :show-feedback="false"
          >
            <n-input-number
              v-model:value="formData.min_effective_weight"
              :min="formData.weight > 0 ? 1 : 0"
              :max="Math.max(formData.weight, 0)"
              :precision="0"
              :disabled="formData.weight <= 0"
              :placeholder="t('subGroups.minEffectiveWeight')"
              size="small"
            />
          </n-form-item>

          <n-form-item
            :label="t('subGroups.healthResetInterval')"
            class="compact-form-item"
            :show-feedback="false"
          >
            <n-select
              v-model:value="formData.health_reset_interval_seconds"
              :options="healthResetOptions"
              :placeholder="t('subGroups.healthResetFollowAggregate')"
              size="small"
            />
          </n-form-item>

          <div class="preview-section">
            <div class="preview-item">
              <span class="preview-label">{{ t("keys.previewPercentage") }}:</span>
              <span class="preview-value">{{ previewPercentage }}</span>
            </div>
            <div class="preview-item">
              <span class="preview-label">{{ t("subGroups.effectiveWeight") }}:</span>
              <span class="preview-value">{{ previewEffectiveWeight }}</span>
            </div>
            <div class="preview-note">
              {{ t("keys.weightPreviewNote") }}
            </div>
          </div>
        </div>
      </n-form>

      <template #footer>
        <div class="footer-actions">
          <n-button @click="handleClose">{{ t("common.cancel") }}</n-button>
          <n-button type="primary" @click="handleSubmit" :loading="loading">
            {{ t("common.confirm") }}
          </n-button>
        </div>
      </template>
    </n-card>
  </n-modal>
</template>

<style scoped>
.edit-weight-modal {
  width: 460px;
}

.edit-weight-card :deep(.n-card-header) {
  padding: 24px 28px 16px;
}

.edit-weight-card :deep(.n-card__content) {
  padding: 0 28px 18px;
}

.edit-weight-card :deep(.n-card__footer) {
  padding: 18px 28px 24px;
}

.edit-weight-card :deep(.n-form-item) {
  margin-bottom: 8px;
}

.compact-form-item :deep(.n-form-item-label) {
  height: 28px;
  min-height: 28px;
  line-height: 28px;
  white-space: nowrap;
}

.compact-form-item :deep(.n-form-item-blank) {
  min-height: 28px;
}

.form-section {
  margin-top: 0;
}

.sub-group-info {
  margin-bottom: 12px;
  padding: 8px 10px;
  background: var(--bg-secondary);
  border-radius: var(--border-radius-md);
  border: 1px solid var(--border-color);
}

.section-title {
  font-size: 0.95rem;
  font-weight: 600;
  color: var(--text-primary);
  line-height: 22px;
  margin: 0 0 4px;
}

.group-name {
  color: var(--primary-color);
  font-weight: 600;
}

.group-details {
  display: flex;
  flex-direction: row;
  gap: 24px;
  align-items: center;
}

.detail-item {
  font-size: 0.85rem;
  line-height: 18px;
  color: var(--text-secondary);
}

.detail-item strong {
  color: var(--text-primary);
}

.weight-input-section {
  display: flex;
  align-items: center;
  gap: 8px;
  width: 100%;
}

.quick-adjust {
  display: flex;
  gap: 4px;
  flex-shrink: 0;
}

.preview-section {
  margin-top: 8px;
  padding: 10px 12px;
  background: var(--bg-tertiary);
  border-radius: var(--border-radius-sm);
  border: 1px solid var(--border-color);
}

.preview-item {
  display: flex;
  justify-content: space-between;
  align-items: center;
  line-height: 22px;
  margin-bottom: 2px;
}

.preview-label {
  font-weight: 600;
  color: var(--text-primary);
}

.preview-value {
  font-size: 1rem;
  font-weight: 700;
  color: var(--primary-color);
}

.preview-note {
  font-size: 0.8rem;
  line-height: 18px;
  margin-top: 4px;
  color: var(--text-tertiary);
  font-style: italic;
}

.footer-actions {
  display: flex;
  justify-content: flex-end;
  gap: 12px;
}

/* Responsive layout */
@media (max-width: 768px) {
  .edit-weight-modal {
    width: 90vw;
  }

  .weight-input-section {
    flex-direction: column;
    align-items: stretch;
  }

  .quick-adjust {
    justify-content: center;
  }

  .preview-item {
    flex-direction: column;
    align-items: flex-start;
    gap: 4px;
  }
}

/* Dark mode adjustments */
:root.dark .sub-group-info {
  background: var(--bg-tertiary);
  border-color: var(--border-color);
}

:root.dark .preview-section {
  background: var(--bg-secondary);
  border-color: var(--border-color);
}
</style>
