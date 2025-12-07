<script setup lang="ts">
import { keysApi } from "@/api/keys";
import type { Group, SubGroupInfo } from "@/types/models";
import { getGroupDisplayName } from "@/utils/display";
import { Add, Close } from "@vicons/ionicons5";
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
  type FormRules,
  type SelectOption,
} from "naive-ui";
import { computed, h, reactive, ref, watch } from "vue";
import { useI18n } from "vue-i18n";
import CCBadge from "./CCBadge.vue";

interface Props {
  show: boolean;
  aggregateGroup: Group | null;
  existingSubGroups: SubGroupInfo[];
  groups: Group[];
}

interface Emits {
  (e: "update:show", value: boolean): void;
  (e: "success"): void;
}

interface SubGroupItem {
  group_id: number | null;
  weight: number;
}

const props = defineProps<Props>();
const emit = defineEmits<Emits>();

const { t } = useI18n();
const message = useMessage();
const loading = ref(false);
const formRef = ref();

// Form data
const formData = reactive<{
  sub_groups: SubGroupItem[];
}>({
  sub_groups: [{ group_id: null, weight: 1 }],
});

// Compute available group options (exclude already added ones)
const getAvailableOptions = computed(() => {
  if (!props.aggregateGroup?.channel_type) {
    return [];
  }

  // Get IDs of existing sub-groups
  const existingIds = props.existingSubGroups.map(sg => sg.group.id);

  return props.groups
    .filter(group => {
      // Must be a standard group
      if (group.group_type === "aggregate") {
        return false;
      }

      // Exclude auto-generated CC groups (created by old buggy code)
      // These have names ending with -cc or display names containing (CC Support)
      // combined with auto-generated description
      if (
        group.description?.includes("Auto-generated") &&
        (group.name?.endsWith("-cc") || group.display_name?.includes("(CC Support)"))
      ) {
        return false;
      }

      // Channel type compatibility check
      // For Anthropic (Claude) aggregate groups, allow both Anthropic channels and OpenAI channels with CC support
      if (props.aggregateGroup?.channel_type === "anthropic") {
        const isAnthropic = group.channel_type === "anthropic";
        const isOpenAIWithCC = group.channel_type === "openai" && group.config?.cc_support === true;
        if (!isAnthropic && !isOpenAIWithCC) {
          return false;
        }
      } else {
        // For non-Anthropic aggregate groups, require exact channel type match
        if (group.channel_type !== props.aggregateGroup?.channel_type) {
          return false;
        }
      }

      // Cannot be the aggregate group itself
      if (props.aggregateGroup?.id && group.id === props.aggregateGroup.id) {
        return false;
      }

      // Cannot be a group that is already a sub-group
      if (group.id && existingIds.includes(group.id)) {
        return false;
      }

      return true;
    })
    .map(group => {
      let label = getGroupDisplayName(group);
      return {
        label,
        value: group?.id,
        // Store additional info for sorting
        isAnthropic: group.channel_type === "anthropic",
        isOpenAIWithCC: group.channel_type === "openai" && group.config?.cc_support === true,
      };
    })
    .sort((a, b) => {
      // Sort order: Anthropic channels first, then OpenAI with CC support
      if (a.isAnthropic && !b.isAnthropic) return -1;
      if (!a.isAnthropic && b.isAnthropic) return 1;
      // Within same type, sort by label
      return a.label.localeCompare(b.label, "zh-CN");
    });
});

// Compute available options for each sub-group item
const getOptionsForItems = computed(() => {
  return formData.sub_groups.map((currentItem, currentIndex) => {
    const otherSelectedIds = formData.sub_groups
      .filter((_item, idx) => idx !== currentIndex)
      .map(sg => sg.group_id)
      .filter((id): id is number => id !== null);

    return getAvailableOptions.value.filter(option => {
      // If it's the current item's selected value, allow it to be displayed
      if (option.value === currentItem.group_id) {
        return true;
      }
      // Otherwise, only show options not selected by other items
      return !otherSelectedIds.includes(option.value as number);
    });
  });
});

// Get options for a given index
function getOptionsForItem(index: number) {
  return getOptionsForItems.value[index] || [];
}

// Form validation rules
const rules: FormRules = {
  sub_groups: {
    type: "array",
    required: true,
    validator: (_rule, value: SubGroupItem[]) => {
      // Check if there's at least one valid sub-group
      const validItems = value.filter(item => item.group_id !== null);
      if (validItems.length === 0) {
        return new Error(t("keys.atLeastOneSubGroup"));
      }

      // Check if weights are valid
      for (const item of validItems) {
        if (item.weight < 0) {
          return new Error(t("keys.weightCannotBeNegative"));
        }
      }

      // Check for duplicate sub-groups
      const groupIds = validItems.map(item => item.group_id);
      const uniqueIds = new Set(groupIds);
      if (uniqueIds.size !== groupIds.length) {
        return new Error(t("keys.duplicateSubGroup"));
      }

      return true;
    },
    trigger: ["blur", "change"],
  },
};

// Watch dialog visibility
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
  formData.sub_groups = [{ group_id: null, weight: 1 }];
}

// Add sub-group item
function addSubGroupItem() {
  formData.sub_groups.push({ group_id: null, weight: 1 });
}

// Remove sub-group item
function removeSubGroupItem(index: number) {
  if (formData.sub_groups.length > 1) {
    formData.sub_groups.splice(index, 1);
  }
}

// Close modal
function handleClose() {
  emit("update:show", false);
}

// Submit form
async function handleSubmit() {
  const aggregateId = props.aggregateGroup?.id;
  if (loading.value || typeof aggregateId !== "number") {
    return;
  }

  try {
    loading.value = true;

    await formRef.value?.validate();

    // Filter out valid sub-groups
    const validSubGroups = formData.sub_groups.filter(sg => sg.group_id !== null);

    await keysApi.addSubGroups(
      aggregateId,
      validSubGroups as { group_id: number; weight: number }[]
    );

    emit("success");
    handleClose();
  } catch (_error) {
    message.error(t("common.error"));
  } finally {
    loading.value = false;
  }
}

// Whether more sub-group items can be added
const canAddMore = computed(() => {
  return formData.sub_groups.length < getAvailableOptions.value.length;
});

// Render label with badge for CC support groups
// Use shared CCBadge component for consistency
function renderLabel(option: SelectOption) {
  const isOpenAIWithCC = option.isOpenAIWithCC as boolean;
  const displayName = option.label as string;

  if (isOpenAIWithCC) {
    return [
      displayName + " ",
      h(CCBadge, {
        channelType: "openai",
        ccSupport: true,
      }),
    ];
  }
  return displayName;
}
</script>

<template>
  <n-modal :show="show" @update:show="handleClose" class="add-sub-group-modal">
    <n-card
      class="add-sub-group-card"
      :title="t('keys.addSubGroup')"
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
        label-width="100px"
      >
        <div class="form-section">
          <h4 class="section-title">
            {{ t("keys.selectSubGroups") }}
            <span class="section-subtitle">
              ({{ t("keys.channelType") }}: {{ aggregateGroup?.channel_type?.toUpperCase() }})
            </span>
          </h4>

          <div class="sub-groups-list">
            <div v-for="(item, index) in formData.sub_groups" :key="index" class="sub-group-item">
              <span class="item-label">{{ t("keys.subGroup") }} {{ index + 1 }}</span>

              <n-form-item
                class="item-select"
                :path="`sub_groups[${index}].group_id`"
                :show-feedback="false"
              >
                <n-select
                  v-model:value="item.group_id"
                  :options="getOptionsForItem(index)"
                  :placeholder="t('keys.selectSubGroup')"
                  :render-label="renderLabel"
                  clearable
                />
              </n-form-item>

              <n-form-item
                class="item-weight"
                :path="`sub_groups[${index}].weight`"
                :show-feedback="false"
              >
                <n-input-number
                  v-model:value="item.weight"
                  :min="0"
                  :max="1000"
                  :placeholder="t('keys.enterWeight')"
                  style="width: 100%"
                />
              </n-form-item>

              <n-button
                @click="removeSubGroupItem(index)"
                type="error"
                quaternary
                circle
                size="small"
                class="item-delete"
                :style="{ visibility: formData.sub_groups.length > 1 ? 'visible' : 'hidden' }"
              >
                <template #icon>
                  <n-icon :component="Close" />
                </template>
              </n-button>
            </div>
          </div>

          <div class="add-item-section">
            <n-button v-if="canAddMore" @click="addSubGroupItem" dashed style="width: 100%">
              <template #icon>
                <n-icon :component="Add" />
              </template>
              {{ t("keys.addMoreSubGroup") }}
            </n-button>
            <div v-else class="no-more-tip">
              {{ t("keys.noMoreAvailableGroups") }}
            </div>
          </div>
        </div>
      </n-form>

      <template #footer>
        <div style="display: flex; justify-content: flex-end; gap: 12px">
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
.add-sub-group-modal {
  width: 700px;
}

.form-section {
  margin-top: 0;
}

.section-title {
  font-size: 1rem;
  font-weight: 600;
  color: var(--text-primary);
  margin-bottom: 16px;
  padding-bottom: 8px;
  border-bottom: 1px solid var(--border-color);
}

.section-subtitle {
  font-size: 0.85rem;
  font-weight: 400;
  color: var(--text-secondary);
  margin-left: 8px;
}

.sub-groups-list {
  display: flex;
  flex-direction: column;
  gap: 16px;
  margin-bottom: 20px;
}

.sub-group-item {
  display: flex;
  align-items: center;
  gap: 12px;
  padding: 12px;
  background: var(--bg-secondary);
  border: 1px solid var(--border-color);
  border-radius: var(--border-radius-md);
}

.item-label {
  flex-shrink: 0;
  min-width: 80px;
  font-weight: 500;
  color: var(--text-primary);
  font-size: 0.9rem;
}

.item-select {
  flex: 1;
  min-width: 200px;
}

.item-weight {
  width: 100px;
  flex-shrink: 0;
}

.item-delete {
  flex-shrink: 0;
}

.add-item-section {
  margin-top: 16px;
}

.no-more-tip {
  text-align: center;
  color: var(--text-tertiary);
  font-size: 0.9rem;
  padding: 12px;
  background: var(--bg-secondary);
  border-radius: var(--border-radius-sm);
}

/* Responsive layout */
@media (max-width: 768px) {
  .add-sub-group-modal {
    width: 90vw;
  }

  .sub-group-item {
    flex-direction: column;
    align-items: stretch;
    gap: 8px;
  }

  .item-label {
    min-width: auto;
    text-align: center;
  }

  .item-select,
  .item-weight {
    width: 100%;
    min-width: auto;
  }

  .item-delete {
    align-self: center;
  }
}

/* Dark mode adjustments */
:root.dark .sub-group-item {
  background: var(--bg-tertiary);
  border-color: var(--border-color);
}

:root.dark .no-more-tip {
  background: var(--bg-tertiary);
}
</style>
