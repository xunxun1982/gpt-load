<script setup lang="ts">
import http from "@/utils/http";
import { Search } from "@vicons/ionicons5";
import { NButton, NCheckbox, NEmpty, NIcon, NInput, NModal, NTooltip } from "naive-ui";
import { computed, reactive, ref, watch } from "vue";
import { useI18n } from "vue-i18n";

interface Props {
  show: boolean;
  models: string[];
}

interface Emits {
  (e: "update:show", value: boolean): void;
  (e: "confirm", redirectRules: Record<string, string[]>): void;
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
const lowercaseRedirect = ref(true);
const autoPrefix = ref(false);

// Cache for brand prefix results from backend
const brandPrefixCache = reactive<Record<string, string>>({});

// Computed
const sortedModels = computed(() => {
  return [...props.models].sort((a, b) => a.toLowerCase().localeCompare(b.toLowerCase()));
});

const filteredModels = computed(() => {
  const keyword = searchKeyword.value.trim().toLowerCase();
  const source = sortedModels.value;
  if (!keyword) {
    return source;
  }
  return source.filter(model => model.toLowerCase().includes(keyword));
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
      Object.keys(modelRedirects).forEach(key => {
        delete modelRedirects[key];
      });
      Object.keys(brandPrefixCache).forEach(key => {
        delete brandPrefixCache[key];
      });
      redirectPrefix.value = "";
      redirectSuffix.value = "";
      lowercaseRedirect.value = true;
      autoPrefix.value = false;
    }
  }
);

// Watch for autoPrefix or lowercaseRedirect changes to update existing redirects
watch([autoPrefix, lowercaseRedirect], async () => {
  if (selectedModelIds.value.length > 0) {
    await updateSelectedModelsRedirects();
  }
});

// Methods

/**
 * Call backend API to apply brand prefixes to models.
 */
async function applyBrandPrefixFromBackend(
  models: string[],
  useLowercase: boolean
): Promise<Record<string, string>> {
  // Check cache first for models that haven't changed lowercase setting
  const cacheKey = useLowercase ? "lower" : "upper";
  const uncachedModels = models.filter(m => !brandPrefixCache[`${cacheKey}:${m}`]);

  if (uncachedModels.length === 0) {
    // All models are cached
    const result: Record<string, string> = {};
    for (const m of models) {
      result[m] = brandPrefixCache[`${cacheKey}:${m}`] ?? m;
    }
    return result;
  }

  try {
    const response = await http.post(
      "/models/apply-brand-prefix",
      {
        models: uncachedModels,
        use_lowercase: useLowercase,
      },
      { hideMessage: true }
    );

    const data = response.data as { results: Record<string, string> };

    // Cache results
    for (const [original, prefixed] of Object.entries(data.results)) {
      brandPrefixCache[`${cacheKey}:${original}`] = prefixed;
    }

    // Build full result including cached values
    const result: Record<string, string> = {};
    for (const m of models) {
      result[m] = brandPrefixCache[`${cacheKey}:${m}`] ?? m;
    }
    return result;
  } catch {
    // Fallback: return original model names if API fails
    const result: Record<string, string> = {};
    for (const m of models) {
      result[m] = m;
    }
    return result;
  }
}

/**
 * Build redirect target for a model based on current settings.
 */
function buildRedirectTarget(modelId: string, prefixedName?: string): string {
  const prefix = redirectPrefix.value || "";
  const suffix = redirectSuffix.value || "";

  let baseName = modelId;

  // Use prefixed name if auto prefix is enabled and we have a prefixed version
  if (autoPrefix.value && prefixedName) {
    baseName = prefixedName;
  }

  return `${prefix}${baseName}${suffix}`;
}

/**
 * Update redirect targets for all selected models.
 */
async function updateSelectedModelsRedirects(): Promise<void> {
  if (!autoPrefix.value) {
    // If auto prefix is disabled, just rebuild with original names
    for (const modelId of selectedModelIds.value) {
      modelRedirects[modelId] = buildRedirectTarget(modelId);
    }
    return;
  }

  // Get prefixed names from backend
  const prefixedNames = await applyBrandPrefixFromBackend(
    selectedModelIds.value,
    lowercaseRedirect.value
  );

  for (const modelId of selectedModelIds.value) {
    modelRedirects[modelId] = buildRedirectTarget(modelId, prefixedNames[modelId]);
  }
}

function getDisplayRedirect(modelId: string): string {
  const base = modelRedirects[modelId] || "";
  return lowercaseRedirect.value ? base.toLowerCase() : base;
}

function handleRedirectInputChange(modelId: string, value: string) {
  modelRedirects[modelId] = value;
}

async function handleModelToggle(modelId: string, checked: boolean) {
  if (checked) {
    if (!selectedModelIds.value.includes(modelId)) {
      selectedModelIds.value.push(modelId);
      // Auto-fill redirect target when a model is first selected
      if (!modelRedirects[modelId] || !modelRedirects[modelId]?.trim()) {
        if (autoPrefix.value) {
          const prefixedNames = await applyBrandPrefixFromBackend(
            [modelId],
            lowercaseRedirect.value
          );
          const prefixedName = prefixedNames[modelId] || modelId;
          modelRedirects[modelId] = buildRedirectTarget(modelId, prefixedName);
        } else {
          modelRedirects[modelId] = buildRedirectTarget(modelId);
        }
      }
    }
  } else {
    const index = selectedModelIds.value.indexOf(modelId);
    if (index > -1) {
      selectedModelIds.value.splice(index, 1);
      delete modelRedirects[modelId];
    }
  }
}

async function handleSelectAll(checked: boolean) {
  if (checked) {
    selectedModelIds.value = [...filteredModels.value];
    // Auto-fill redirect targets for all selected models
    if (autoPrefix.value) {
      const prefixedNames = await applyBrandPrefixFromBackend(
        selectedModelIds.value,
        lowercaseRedirect.value
      );
      for (const modelId of selectedModelIds.value) {
        if (!modelRedirects[modelId] || !modelRedirects[modelId]?.trim()) {
          const prefixedName = prefixedNames[modelId] || modelId;
          modelRedirects[modelId] = buildRedirectTarget(modelId, prefixedName);
        }
      }
    } else {
      for (const modelId of selectedModelIds.value) {
        if (!modelRedirects[modelId] || !modelRedirects[modelId]?.trim()) {
          modelRedirects[modelId] = buildRedirectTarget(modelId);
        }
      }
    }
  } else {
    selectedModelIds.value = [];
    Object.keys(modelRedirects).forEach(key => {
      delete modelRedirects[key];
    });
  }
}

function handleClose() {
  emit("update:show", false);
}

function handleConfirm() {
  // Use Map to support one-to-many: redirectTarget -> [modelId1, modelId2, ...]
  const redirectMap = new Map<string, string[]>();

  for (const modelId of selectedModelIds.value) {
    let redirectTarget = getDisplayRedirect(modelId).trim();

    if (!redirectTarget) {
      modelRedirects[modelId] = buildRedirectTarget(modelId);
      redirectTarget = getDisplayRedirect(modelId).trim();
    }

    if (!redirectTarget) {
      modelRedirects[modelId] = modelId;
      redirectTarget = getDisplayRedirect(modelId).trim();
    }

    // Collect all modelIds for the same redirectTarget
    const existing = redirectMap.get(redirectTarget) || [];
    if (!existing.includes(modelId)) {
      existing.push(modelId);
    }
    redirectMap.set(redirectTarget, existing);
  }

  // Convert to the format expected by parent: { from: to[] }
  // But current interface is Record<string, string>, so we emit multiple entries
  // Actually we need to change the emit format to support one-to-many
  const redirectRules: Record<string, string[]> = {};
  for (const [target, models] of redirectMap) {
    redirectRules[target] = models;
  }

  emit("confirm", redirectRules);
  emit("update:show", false);
}
</script>

<template>
  <n-modal
    :show="show"
    @update:show="value => emit('update:show', value)"
    preset="card"
    :title="t('keys.selectModels')"
    :style="{ width: '850px', maxHeight: '80vh' }"
    :bordered="false"
    :segmented="{ content: 'soft', footer: 'soft' }"
    :content-style="{ padding: '12px 16px' }"
    :footer-style="{ padding: '12px 16px' }"
  >
    <div class="model-selector-content">
      <!-- Search, stats and redirect options in a single toolbar row -->
      <div class="toolbar-row">
        <n-input
          v-model:value="searchKeyword"
          :placeholder="t('keys.searchModels')"
          clearable
          size="small"
          :style="{ width: '150px', flexShrink: 1 }"
        >
          <template #prefix>
            <n-icon :component="Search" />
          </template>
        </n-input>
        <n-tooltip placement="bottom" trigger="hover">
          <template #trigger>
            <div class="toolbar-stats">
              <span>{{ sortedModels.length }}/{{ selectedModelIds.length }}</span>
            </div>
          </template>
          {{ t("keys.modelStatsTooltip") }}
        </n-tooltip>
        <n-input
          v-model:value="redirectPrefix"
          :placeholder="t('keys.redirectPrefixPlaceholder')"
          size="small"
          :style="{ width: '145px', flexShrink: 1 }"
        />
        <n-input
          v-model:value="redirectSuffix"
          :placeholder="t('keys.redirectSuffixPlaceholder')"
          size="small"
          :style="{ width: '145px', flexShrink: 1 }"
        />
        <n-tooltip placement="bottom" trigger="hover">
          <template #trigger>
            <n-checkbox v-model:checked="lowercaseRedirect" size="small" class="compact-checkbox">
              {{ t("keys.lowercaseRedirectShort") }}
            </n-checkbox>
          </template>
          {{ t("keys.lowercaseRedirect") }}
        </n-tooltip>
        <n-tooltip placement="bottom" trigger="hover">
          <template #trigger>
            <n-checkbox v-model:checked="autoPrefix" size="small" class="compact-checkbox">
              {{ t("keys.autoPrefixShort") }}
            </n-checkbox>
          </template>
          {{ t("keys.autoPrefix") }}
        </n-tooltip>
      </div>

      <!-- Model list with checkboxes and redirect target -->
      <div class="model-list">
        <div v-for="modelId in filteredModels" :key="modelId" class="model-item">
          <n-checkbox
            :checked="selectedModelIds.includes(modelId)"
            @update:checked="checked => handleModelToggle(modelId, checked)"
          >
            <span class="model-id" @click.stop>{{ modelId }}</span>
          </n-checkbox>

          <div v-if="selectedModelIds.includes(modelId)" class="redirect-input-container">
            <span class="redirect-arrow">→</span>
            <n-input
              :value="getDisplayRedirect(modelId)"
              @update:value="val => handleRedirectInputChange(modelId, val)"
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
      <div class="modal-footer">
        <n-checkbox
          v-model:checked="selectAll"
          :indeterminate="
            selectedModelIds.length > 0 && selectedModelIds.length < filteredModels.length
          "
          @update:checked="handleSelectAll"
        >
          {{ t("keys.selectAll") }}
        </n-checkbox>

        <div class="footer-buttons">
          <n-button size="small" @click="handleClose">
            {{ t("common.cancel") }}
          </n-button>
          <n-button
            type="primary"
            size="small"
            :disabled="selectedModelIds.length === 0"
            @click="handleConfirm"
          >
            {{ t("keys.addToRedirectRules") }} ({{ selectedModelIds.length }})
          </n-button>
        </div>
      </div>
    </template>
  </n-modal>
</template>

<style scoped>
.model-selector-content {
  max-height: 55vh;
  overflow-y: auto;
}

.toolbar-row {
  display: flex;
  align-items: center;
  gap: 6px;
  margin-bottom: 12px;
  flex-wrap: nowrap;
  position: sticky;
  top: 0;
  background-color: var(--bg-primary, #fff);
  z-index: 1;
  padding: 4px 0;
}

/* Compact checkbox style to prevent wrapping */
.compact-checkbox {
  white-space: nowrap;
  flex-shrink: 0;
}

.toolbar-stats {
  display: flex;
  align-items: center;
  justify-content: center;
  min-width: 50px;
  padding: 0 2px;
  font-size: 12px;
  color: var(--text-secondary);
  white-space: nowrap;
  flex-shrink: 0;
}

.model-list {
  display: flex;
  flex-direction: column;
  gap: 10px;
}

.model-item {
  display: flex;
  align-items: center;
  gap: 10px;
  padding: 6px 10px;
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
  gap: 6px;
  flex: 1;
  margin-left: 8px;
}

.redirect-arrow {
  color: #666;
  font-weight: bold;
  font-size: 14px;
}

/* Footer styles */
.modal-footer {
  display: flex;
  justify-content: space-between;
  align-items: center;
  flex-wrap: nowrap;
  gap: 8px;
}

.footer-buttons {
  display: flex;
  gap: 8px;
  flex-shrink: 0;
}
</style>
