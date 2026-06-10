<script setup lang="ts">
import { keysApi } from "@/api/keys";
import GroupSelectLabel from "@/components/common/GroupSelectLabel.vue";
import type { Group, SubGroupInfo } from "@/types/models";
import { getGroupDisplayName } from "@/utils/display";
import { sortGroupsWithChildren } from "@/utils/sort";
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

// Extended option interface for sorting
interface GroupOption extends SelectOption {
  isChildGroup?: boolean;
  channelType?: string;
  ccSupport?: boolean;
  parentGroupId?: number | null;
  name?: string;
}

const props = defineProps<Props>();
const emit = defineEmits<Emits>();

const { t } = useI18n();
const message = useMessage();
const loading = ref(false);
const formRef = ref();
const defaultSubGroupWeight = 100;
const weightInputProps = computed(() => ({
  "aria-label": t("keys.weight"),
  title: t("keys.weight"),
}));

function createDefaultSubGroupItem(): SubGroupItem {
  return { group_id: null, weight: defaultSubGroupWeight };
}

// Form data
const formData = reactive<{
  sub_groups: SubGroupItem[];
}>({
  sub_groups: [createDefaultSubGroupItem()],
});

// Compute available group options (exclude already added ones)
const getAvailableOptions = computed(() => {
  if (!props.aggregateGroup?.channel_type) {
    return [];
  }

  // Get IDs of existing sub-groups
  const existingIds = props.existingSubGroups.map(sg => sg.group.id);

  const filteredGroups = props.groups
    .filter(group => {
      // Must be a standard group (including child groups)
      if (group.group_type === "aggregate") {
        return false;
      }

      // Exclude auto-generated CC groups (created by old buggy code)
      if (
        group.description?.includes("Auto-generated") &&
        (group.name?.endsWith("-cc") || group.display_name?.includes("(CC Support)"))
      ) {
        return false;
      }

      // Channel type compatibility check
      if (props.aggregateGroup?.channel_type === "anthropic") {
        const isAnthropic = group.channel_type === "anthropic";
        const isOpenAIWithCC = group.channel_type === "openai" && group.config?.cc_support === true;
        const isOpenAIResponseWithCC =
          group.channel_type === "openai-response" && group.config?.cc_support === true;
        const isGeminiWithCC = group.channel_type === "gemini" && group.config?.cc_support === true;
        if (!isAnthropic && !isOpenAIWithCC && !isOpenAIResponseWithCC && !isGeminiWithCC) {
          return false;
        }
      } else {
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
    .filter(group => group.id !== undefined);
  const sortedGroups = sortGroupsWithChildren(filteredGroups);

  return sortedGroups.map(group => ({
    label: getGroupDisplayName(group),
    value: group.id,
    isChildGroup: !!group.parent_group_id,
    parentGroupId: group.parent_group_id,
    channelType: group.channel_type,
    ccSupport: group.config?.cc_support === true,
    name: group.name,
  }));
});

// Compute available options for each sub-group item
const getOptionsForItems = computed(() => {
  return formData.sub_groups.map((currentItem, currentIndex) => {
    const otherSelectedIds = formData.sub_groups
      .filter((_item, idx) => idx !== currentIndex)
      .map(sg => sg.group_id)
      .filter((id): id is number => id !== null);

    return getAvailableOptions.value.filter(option => {
      if (option.value === currentItem.group_id) {
        return true;
      }
      return !otherSelectedIds.includes(option.value as number);
    });
  });
});

// Get options for a given index
function getOptionsForItem(index: number) {
  return getOptionsForItems.value[index] || [];
}

// Keep integer enforcement shared between input, form validation, and submit payload.
function isValidWeight(value: number | null | undefined): value is number {
  return typeof value === "number" && Number.isInteger(value) && value >= 0 && value <= 1000;
}

function validateWeight(value: number | null | undefined) {
  if (value === null || value === undefined) {
    return new Error(t("keys.enterWeight"));
  }
  if (!Number.isInteger(value)) {
    return new Error(t("keys.weightMustBeInteger"));
  }
  if (value < 0) {
    return new Error(t("keys.weightCannotBeNegative"));
  }
  if (value > 1000) {
    return new Error(t("keys.weightMaxExceeded"));
  }
  return true;
}

function handleWeightUpdate(item: SubGroupItem, value: number | null) {
  if (isValidWeight(value)) {
    item.weight = value;
  }
}

function sanitizeSubGroupWeight(value: number): number {
  if (!isValidWeight(value)) {
    throw new Error("Invalid sub-group weight");
  }
  return value;
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
        const weightValidation = validateWeight(item.weight);
        if (weightValidation !== true) {
          return weightValidation;
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
  formData.sub_groups = [createDefaultSubGroupItem()];
}

// Add sub-group item
function addSubGroupItem() {
  formData.sub_groups.push(createDefaultSubGroupItem());
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
    const validSubGroups = formData.sub_groups
      .filter((sg): sg is SubGroupItem & { group_id: number } => sg.group_id !== null)
      .map(sg => ({
        group_id: sg.group_id,
        weight: sanitizeSubGroupWeight(sg.weight),
      }));

    await keysApi.addSubGroups(aggregateId, validSubGroups);

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

// Render label with badge for CC support groups and child group indicator
function renderLabel(option: SelectOption) {
  const opt = option as GroupOption;
  return h(GroupSelectLabel, {
    label: String(option.label ?? ""),
    isChildGroup: opt.isChildGroup === true,
    channelType: opt.channelType,
    ccSupport: opt.ccSupport === true,
    showChildTag: true,
  });
}

// Custom filter function for NSelect to search by label and name
function filterOption(pattern: string, option: SelectOption): boolean {
  const search = pattern.toLowerCase().trim();
  if (!search) {
    return true;
  }
  const label = String(option.label ?? "").toLowerCase();
  const name = String((option as GroupOption).name ?? "").toLowerCase();
  return label.includes(search) || name.includes(search);
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
          <div class="section-header">
            <h4 class="section-title">
              {{ t("keys.selectSubGroups") }}
            </h4>
            <span class="channel-badge">
              {{ aggregateGroup?.channel_type?.toUpperCase() }}
            </span>
          </div>

          <!-- Sub-groups list container with integrated search -->
          <div class="sub-groups-container">
            <div class="sub-groups-list">
              <div v-for="(item, index) in formData.sub_groups" :key="index" class="sub-group-item">
                <span class="item-index">{{ index + 1 }}</span>

                <n-form-item
                  class="item-select"
                  :path="`sub_groups[${index}].group_id`"
                  :show-feedback="false"
                >
                  <n-select
                    v-model:value="item.group_id"
                    :options="getOptionsForItem(index)"
                    :placeholder="t('keys.searchAndSelectSubGroup')"
                    :render-label="renderLabel"
                    :filter="filterOption"
                    filterable
                    clearable
                  />
                </n-form-item>

                <div class="weight-section">
                  <span class="weight-label">{{ t("keys.weight") }}</span>
                  <n-form-item
                    class="item-weight"
                    :path="`sub_groups[${index}].weight`"
                    :show-feedback="false"
                  >
                    <n-input-number
                      :value="item.weight"
                      :min="0"
                      :max="1000"
                      :precision="0"
                      :validator="isValidWeight"
                      :placeholder="t('keys.weight')"
                      :input-props="weightInputProps"
                      size="small"
                      class="weight-input"
                      @update:value="value => handleWeightUpdate(item, value)"
                    />
                  </n-form-item>
                </div>

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

            <!-- Add more button and available count -->
            <div class="list-footer">
              <n-button
                v-if="canAddMore"
                @click="addSubGroupItem"
                dashed
                size="small"
                class="add-btn"
              >
                <template #icon>
                  <n-icon :component="Add" />
                </template>
                {{ t("keys.addMoreSubGroup") }}
              </n-button>
              <span v-else class="no-more-tip">
                {{ t("keys.noMoreAvailableGroups") }}
              </span>
              <span class="available-count">
                {{ t("keys.availableSubGroupsCount", { count: getAvailableOptions.length }) }}
              </span>
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
  width: 600px;
}

.form-section {
  margin-top: 0;
}

.section-header {
  display: flex;
  align-items: center;
  justify-content: space-between;
  margin-bottom: 16px;
  padding-bottom: 12px;
  border-bottom: 1px solid var(--border-color);
}

.section-title {
  font-size: 1rem;
  font-weight: 600;
  color: var(--text-primary);
  margin: 0;
}

.channel-badge {
  font-size: 11px;
  font-weight: 600;
  color: var(--primary-color);
  background: var(--primary-bg);
  padding: 4px 10px;
  border-radius: 12px;
  border: 1px solid var(--primary-color);
}

.sub-groups-container {
  background: var(--bg-secondary);
  border: 1px solid var(--border-color);
  border-radius: var(--border-radius-md);
  padding: 12px;
}

.sub-groups-list {
  display: flex;
  flex-direction: column;
  gap: 8px;
  max-height: 320px;
  overflow-y: auto;
}

.sub-group-item {
  display: flex;
  align-items: center;
  gap: 10px;
  padding: 10px 12px;
  background: var(--card-bg-solid);
  border: 1px solid var(--border-color);
  border-radius: var(--border-radius-sm);
  transition: all 0.2s ease;
}

.sub-group-item:hover {
  border-color: var(--primary-color);
  box-shadow: 0 2px 8px rgba(0, 0, 0, 0.06);
}

.item-index {
  flex-shrink: 0;
  width: 24px;
  height: 24px;
  display: flex;
  align-items: center;
  justify-content: center;
  font-size: 12px;
  font-weight: 600;
  color: var(--text-secondary);
  background: var(--bg-tertiary);
  border-radius: 50%;
}

.item-select {
  flex: 1;
  min-width: 0;
}

.weight-section {
  display: flex;
  align-items: center;
  gap: 6px;
  flex-shrink: 0;
}

.weight-label {
  font-size: 12px;
  color: var(--text-secondary);
  white-space: nowrap;
}

.item-weight {
  width: 108px;
  flex-shrink: 0;
}

.weight-input {
  width: 100%;
}

.item-delete {
  flex-shrink: 0;
}

.list-footer {
  display: flex;
  align-items: center;
  justify-content: space-between;
  margin-top: 12px;
  padding-top: 12px;
  border-top: 1px dashed var(--border-color);
}

.add-btn {
  flex-shrink: 0;
}

.available-count {
  font-size: 12px;
  color: var(--text-tertiary);
  white-space: nowrap;
}

.no-more-tip {
  font-size: 12px;
  color: var(--text-tertiary);
}

/* Scrollbar styles */
.sub-groups-list::-webkit-scrollbar {
  width: 6px;
}

.sub-groups-list::-webkit-scrollbar-track {
  background: transparent;
}

.sub-groups-list::-webkit-scrollbar-thumb {
  background: var(--scrollbar-bg);
  border-radius: 3px;
}

.sub-groups-list::-webkit-scrollbar-thumb:hover {
  background: var(--border-color);
}

/* Responsive layout */
@media (max-width: 768px) {
  .add-sub-group-modal {
    width: 95vw;
  }

  .sub-group-item {
    gap: 8px;
  }
}

@media (max-width: 480px) {
  .sub-group-item {
    gap: 6px;
    padding: 8px;
  }

  .weight-label {
    display: none;
  }
}

/* Dark mode adjustments */
:root.dark .sub-groups-container {
  background: var(--bg-tertiary);
}

:root.dark .sub-group-item {
  background: var(--bg-secondary);
  border-color: rgba(255, 255, 255, 0.08);
}

:root.dark .sub-group-item:hover {
  border-color: var(--primary-color);
  box-shadow: 0 2px 8px rgba(0, 0, 0, 0.2);
}

:root.dark .channel-badge {
  background: rgba(102, 126, 234, 0.15);
  border-color: rgba(102, 126, 234, 0.4);
}
</style>
