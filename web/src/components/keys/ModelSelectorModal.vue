<template>
  <n-modal
    :show="show"
    @update:show="value => emit('update:show', value)"
    preset="card"
    :title="t('keys.selectModels')"
    :style="{ width: '800px', maxHeight: '80vh' }"
    :bordered="false"
    :segmented="{ content: 'soft', footer: 'soft' }"
  >
    <div class="model-selector-content">
      <!-- Search and filter -->
      <n-input
        v-model:value="searchKeyword"
        :placeholder="t('keys.searchModels')"
        clearable
        style="margin-bottom: 16px"
      >
        <template #prefix>
          <n-icon :component="Search" />
        </template>
      </n-input>

      <!-- Stats -->
      <div class="stats-bar">
        <span>{{ t("keys.totalModels") }}: {{ filteredModels.length }}</span>
        <span>{{ t("keys.selectedModels") }}: {{ selectedModelIds.length }}</span>
      </div>

      <!-- Global prefix/suffix for redirect targets -->
      <div class="prefix-suffix-row">
        <n-input
          v-model:value="redirectPrefix"
          :placeholder="t('keys.redirectPrefixPlaceholder')"
          size="small"
        />
        <n-input
          v-model:value="redirectSuffix"
          :placeholder="t('keys.redirectSuffixPlaceholder')"
          size="small"
        />
      </div>

      <!-- Model list with checkboxes and redirect target -->
      <div class="model-list">
        <div v-for="modelId in filteredModels" :key="modelId" class="model-item">
          <n-checkbox
            :checked="selectedModelIds.includes(modelId)"
            @update:checked="checked => handleModelToggle(modelId, checked)"
          >
            <!-- Make model id text selectable and copyable without toggling checkbox -->
            <span class="model-id" @click.stop>{{ modelId }}</span>
          </n-checkbox>

          <!-- Show redirect input only if selected -->
          <div v-if="selectedModelIds.includes(modelId)" class="redirect-input-container">
            <span class="redirect-arrow">→</span>
            <n-input
              v-model:value="modelRedirects[modelId]"
              :placeholder="t('keys.redirectTarget')"
              size="small"
              style="flex: 1"
            />
          </div>
        </div>

        <n-empty
          v-if="filteredModels.length === 0"
          :description="t('keys.noModelsFound')"
          style="margin: 40px 0"
        />
      </div>
    </div>

    <template #footer>
      <div style="display: flex; justify-content: space-between; align-items: center">
        <n-checkbox
          v-model:checked="selectAll"
          :indeterminate="
            selectedModelIds.length > 0 && selectedModelIds.length < filteredModels.length
          "
          @update:checked="handleSelectAll"
        >
          {{ t("keys.selectAll") }}
        </n-checkbox>

        <div style="display: flex; gap: 12px">
          <n-button @click="handleClose">
            {{ t("common.cancel") }}
          </n-button>
          <n-button type="primary" :disabled="selectedModelIds.length === 0" @click="handleConfirm">
            {{ t("keys.addToRedirectRules") }} ({{ selectedModelIds.length }})
          </n-button>
        </div>
      </div>
    </template>
  </n-modal>
</template>

<script setup lang="ts">
import { Search } from "@vicons/ionicons5";
import { NButton, NCheckbox, NEmpty, NIcon, NInput, NModal } from "naive-ui";
import { computed, reactive, ref, watch } from "vue";
import { useI18n } from "vue-i18n";

interface Props {
  show: boolean;
  models: string[];
}

interface Emits {
  (e: "update:show", value: boolean): void;
  (e: "confirm", redirectRules: Record<string, string>): void;
}

const props = defineProps<Props>();
const emit = defineEmits<Emits>();

const { t } = useI18n();

// Local state
const searchKeyword = ref("");
const selectedModelIds = ref<string[]>([]);
const modelRedirects = reactive<Record<string, string>>({});
const redirectPrefix = ref("");
const redirectSuffix = ref("");

// Computed
const filteredModels = computed(() => {
  if (!searchKeyword.value.trim()) {
    return props.models;
  }
  const keyword = searchKeyword.value.toLowerCase();
  return props.models.filter(model => model.toLowerCase().includes(keyword));
});

const selectAll = computed({
  get: () =>
    selectedModelIds.value.length === filteredModels.value.length &&
    filteredModels.value.length > 0,
  set: () => {
    // Handled by handleSelectAll
  },
});

// Watchers
watch(
  () => props.show,
  newShow => {
    if (newShow) {
      // Reset state when modal opens
      searchKeyword.value = "";
      selectedModelIds.value = [];
      // Clear redirects
      Object.keys(modelRedirects).forEach(key => {
        delete modelRedirects[key];
      });
      redirectPrefix.value = "";
      redirectSuffix.value = "";
    }
  }
);

// Methods
function handleModelToggle(modelId: string, checked: boolean) {
  if (checked) {
    if (!selectedModelIds.value.includes(modelId)) {
      selectedModelIds.value.push(modelId);
      // Auto-fill redirect target when a model is first selected
      if (!modelRedirects[modelId] || !modelRedirects[modelId].trim()) {
        const prefix = redirectPrefix.value || "";
        const suffix = redirectSuffix.value || "";
        if (prefix || suffix) {
          modelRedirects[modelId] = `${prefix}${modelId}${suffix}`;
        } else {
          // Fallback to original model id when no prefix/suffix is provided
          modelRedirects[modelId] = modelId;
        }
      }
    }
  } else {
    const index = selectedModelIds.value.indexOf(modelId);
    if (index > -1) {
      selectedModelIds.value.splice(index, 1);
      // Remove redirect mapping when unchecked
      delete modelRedirects[modelId];
    }
  }
}

function handleSelectAll(checked: boolean) {
  if (checked) {
    selectedModelIds.value = [...filteredModels.value];
    // Auto-fill redirect targets for all selected models when using select-all
    selectedModelIds.value.forEach(modelId => {
      if (!modelRedirects[modelId] || !modelRedirects[modelId].trim()) {
        const prefix = redirectPrefix.value || "";
        const suffix = redirectSuffix.value || "";
        if (prefix || suffix) {
          modelRedirects[modelId] = `${prefix}${modelId}${suffix}`;
        } else {
          modelRedirects[modelId] = modelId;
        }
      }
    });
  } else {
    selectedModelIds.value = [];
    // Clear all redirects
    Object.keys(modelRedirects).forEach(key => {
      delete modelRedirects[key];
    });
  }
}

function handleClose() {
  emit("update:show", false);
}

function handleConfirm() {
  // Build redirect rules object
  const redirectRules: Record<string, string> = {};

  selectedModelIds.value.forEach(modelId => {
    let redirectTarget = modelRedirects[modelId]?.trim();

    // If redirect target is empty, try to build one from prefix/suffix
    if (!redirectTarget) {
      const prefix = redirectPrefix.value || "";
      const suffix = redirectSuffix.value || "";
      if (prefix || suffix) {
        redirectTarget = `${prefix}${modelId}${suffix}`;
      }
    }

    // If still empty, fall back to using the original model id
    if (!redirectTarget) {
      redirectTarget = modelId;
    }

    if (redirectTarget) {
      // Only add when we have a non-empty redirect target
      redirectRules[redirectTarget] = modelId;
    }
  });

  emit("confirm", redirectRules);
  emit("update:show", false);
}
</script>

<style scoped>
.model-selector-content {
  max-height: 60vh;
  overflow-y: auto;
}

.stats-bar {
  display: flex;
  gap: 24px;
  padding: 8px 12px;
  background: var(--bg-secondary);
  border-radius: 4px;
  margin-bottom: 16px;
  font-size: 13px;
  color: var(--text-secondary);
}

.prefix-suffix-row {
  display: flex;
  gap: 12px;
  margin: 12px 0;
}

.model-list {
  display: flex;
  flex-direction: column;
  gap: 12px;
}

.model-item {
  display: flex;
  align-items: center;
  gap: 12px;
  padding: 8px 12px;
  border: 1px solid var(--border-color);
  border-radius: 4px;
  transition: all 0.2s;
}

.model-item:hover {
  border-color: var(--success-color);
  background: var(--success-bg);
}

.model-id {
  font-family: "Consolas", "Monaco", monospace;
  font-size: 13px;
  color: var(--text-primary);
  user-select: text;
}

.redirect-input-container {
  display: flex;
  align-items: center;
  gap: 8px;
  flex: 1;
  margin-left: 12px;
}

.redirect-arrow {
  color: #666;
  font-weight: bold;
  font-size: 16px;
}
</style>
