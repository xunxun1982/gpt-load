<script setup lang="ts">
import { keysApi } from "@/api/keys";
import type { Group, SubGroupInfo } from "@/types/models";
import { appState } from "@/utils/app-state";
import {
  calculateAutoSubGroupWeights,
  createUniformSubGroupWeights,
} from "@/utils/auto-subgroup-weight";
import { parseBalanceValue, resolveSubGroupSiteId } from "@/utils/display";
import { Close } from "@vicons/ionicons5";
import {
  NAlert,
  NButton,
  NCard,
  NFormItem,
  NIcon,
  NInputNumber,
  NModal,
  NRadioButton,
  NRadioGroup,
  useMessage,
} from "naive-ui";
import { computed, ref, watch } from "vue";
import { useI18n } from "vue-i18n";

interface Props {
  show: boolean;
  aggregateGroup: Group | null;
  subGroups: SubGroupInfo[];
  groups: Group[];
}

interface Emits {
  (e: "update:show", value: boolean): void;
  (e: "success"): void;
}

type AutoWeightStrategy = "balance" | "uniform";

const props = defineProps<Props>();
const emit = defineEmits<Emits>();
const { t } = useI18n();
const message = useMessage();
const loading = ref(false);
// Only this modal starts at 1000; the existing input and backend limits stay at 5000.
const defaultAutoWeightMax = 1000;
const defaultUniformWeight = 100;
const strategy = ref<AutoWeightStrategy>("balance");
const maxWeight = ref(defaultAutoWeightMax);
const uniformWeight = ref(defaultUniformWeight);
const groupById = computed(() => {
  const groups = new Map<number, Group>();
  for (const group of props.groups) {
    if (typeof group.id === "number") {
      groups.set(group.id, group);
    }
  }
  return groups;
});

watch(
  () => props.show,
  show => {
    if (show) {
      strategy.value = "balance";
      maxWeight.value = defaultAutoWeightMax;
      uniformWeight.value = defaultUniformWeight;
    }
  }
);

function handleClose() {
  if (!loading.value) {
    emit("update:show", false);
  }
}

function buildWeightResult() {
  if (strategy.value === "uniform") {
    // Uniform mode intentionally includes disabled, unbound, and uncached sub-groups.
    return createUniformSubGroupWeights(
      props.subGroups.map(subGroup => subGroup.group.id ?? 0),
      uniformWeight.value
    );
  }

  const candidates = props.subGroups.map(subGroup => {
    const siteId = resolveSubGroupSiteId(subGroup, groupById.value);
    const hasSite = siteId !== null && siteId !== undefined;
    return {
      subGroupId: subGroup.group.id ?? 0,
      balance: hasSite ? parseBalanceValue(appState.siteBalances[siteId]) : null,
      checkinStatus: hasSite ? appState.siteCheckinStatuses[siteId] : "",
    };
  });
  return calculateAutoSubGroupWeights(candidates, maxWeight.value);
}

async function handleSubmit() {
  const aggregateGroupId = props.aggregateGroup?.id;
  const selectedWeight = strategy.value === "uniform" ? uniformWeight.value : maxWeight.value;
  if (
    !aggregateGroupId ||
    loading.value ||
    !Number.isInteger(selectedWeight) ||
    selectedWeight < 1 ||
    selectedWeight > 5000
  ) {
    return;
  }

  const result = buildWeightResult();
  if (result.updates.length === 0) {
    message.warning(
      t(
        strategy.value === "uniform"
          ? "subGroups.autoWeightNoSubGroups"
          : "subGroups.autoWeightNoEligible"
      )
    );
    return;
  }

  loading.value = true;
  let successCount = 0;
  let failedCount = 0;
  try {
    // Keep updates serial to avoid a burst of write requests and preserve predictable partial progress.
    for (const update of result.updates) {
      try {
        await keysApi.updateSubGroupWeight(
          aggregateGroupId,
          update.subGroupId,
          { weight: update.weight },
          true
        );
        successCount++;
      } catch {
        failedCount++;
      }
    }

    if (successCount > 0) {
      emit("success");
    }
    const params = {
      success: successCount,
      failed: failedCount,
      skipped: result.skippedCount,
    };
    if (failedCount > 0) {
      message.warning(t("subGroups.autoWeightPartial", params));
      return;
    }
    message.success(t("subGroups.autoWeightSuccess", params));
    emit("update:show", false);
  } finally {
    loading.value = false;
  }
}
</script>

<template>
  <n-modal :show="show" @update:show="handleClose" class="auto-weight-modal">
    <n-card
      class="auto-weight-card"
      :title="t('subGroups.autoWeightTitle')"
      :bordered="false"
      size="medium"
      role="dialog"
      aria-modal="true"
    >
      <template #header-extra>
        <n-button quaternary circle :disabled="loading" @click="handleClose">
          <template #icon>
            <n-icon :component="Close" />
          </template>
        </n-button>
      </template>

      <n-form-item :label="t('subGroups.autoWeightStrategy')" :show-feedback="false">
        <n-radio-group v-model:value="strategy" class="strategy-selector">
          <n-radio-button value="balance">
            {{ t("subGroups.autoWeightStrategyBalance") }}
          </n-radio-button>
          <n-radio-button value="uniform">
            {{ t("subGroups.autoWeightStrategyUniform") }}
          </n-radio-button>
        </n-radio-group>
      </n-form-item>

      <div class="weight-settings">
        <n-form-item
          v-if="strategy === 'balance'"
          :label="t('subGroups.autoWeightMax')"
          :show-feedback="false"
        >
          <n-input-number
            v-model:value="maxWeight"
            :min="1"
            :max="5000"
            :precision="0"
            style="width: 100%"
          />
        </n-form-item>
        <n-form-item v-else :label="t('subGroups.autoWeightUniform')" :show-feedback="false">
          <n-input-number
            v-model:value="uniformWeight"
            :min="1"
            :max="5000"
            :precision="0"
            style="width: 100%"
          />
        </n-form-item>
      </div>
      <n-alert type="info" :show-icon="false">
        {{
          t(strategy === "balance" ? "subGroups.autoWeightHint" : "subGroups.autoWeightUniformHint")
        }}
      </n-alert>

      <template #footer>
        <div class="footer-actions">
          <n-button :disabled="loading" @click="handleClose">{{ t("common.cancel") }}</n-button>
          <n-button type="primary" :loading="loading" @click="handleSubmit">
            {{ t("common.confirm") }}
          </n-button>
        </div>
      </template>
    </n-card>
  </n-modal>
</template>

<style scoped>
.auto-weight-modal {
  width: min(520px, calc(100vw - 32px));
}

.auto-weight-card :deep(.n-card-content) {
  display: flex;
  flex-direction: column;
  gap: 12px;
}

.strategy-selector {
  display: flex;
  width: 100%;
}

.strategy-selector :deep(.n-radio-button) {
  flex: 1;
  text-align: center;
}

.weight-settings {
  padding: 12px 14px;
  border: 1px solid var(--n-border-color);
  border-radius: 8px;
  background: var(--n-color-embedded, transparent);
}

.weight-settings :deep(.n-form-item) {
  margin-bottom: 0;
}

.footer-actions {
  display: flex;
  justify-content: flex-end;
  gap: 12px;
}

@media (max-width: 480px) {
  .strategy-selector {
    flex-direction: column;
    height: auto;
  }

  .strategy-selector :deep(.n-radio-button) {
    width: 100%;
  }

  .strategy-selector :deep(.n-radio-button:first-child) {
    border-radius: var(--n-button-border-radius) var(--n-button-border-radius) 0 0;
  }

  .strategy-selector :deep(.n-radio-button:last-child) {
    border-radius: 0 0 var(--n-button-border-radius) var(--n-button-border-radius);
  }

  .strategy-selector :deep(.n-radio-group__splitor) {
    display: none;
  }
}
</style>
