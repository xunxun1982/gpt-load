<script setup lang="ts">
import { keysApi } from "@/api/keys";
import type { Group, SubGroupInfo } from "@/types/models";
import { appState } from "@/utils/app-state";
import { calculateAutoSubGroupWeights } from "@/utils/auto-subgroup-weight";
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

const props = defineProps<Props>();
const emit = defineEmits<Emits>();
const { t } = useI18n();
const message = useMessage();
const loading = ref(false);
const maxWeight = ref(100);
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
      maxWeight.value = 100;
    }
  }
);

function handleClose() {
  if (!loading.value) {
    emit("update:show", false);
  }
}

async function handleSubmit() {
  const aggregateGroupId = props.aggregateGroup?.id;
  if (
    !aggregateGroupId ||
    loading.value ||
    !Number.isInteger(maxWeight.value) ||
    maxWeight.value < 1 ||
    maxWeight.value > 5000
  ) {
    return;
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
  const result = calculateAutoSubGroupWeights(candidates, maxWeight.value);
  if (result.updates.length === 0) {
    message.warning(t("subGroups.autoWeightNoEligible"));
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

      <n-form-item :label="t('subGroups.autoWeightMax')" :show-feedback="false">
        <n-input-number
          v-model:value="maxWeight"
          :min="1"
          :max="5000"
          :precision="0"
          style="width: 100%"
        />
      </n-form-item>
      <n-alert type="info" :show-icon="false">
        {{ t("subGroups.autoWeightHint") }}
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
  width: 460px;
}

.auto-weight-card :deep(.n-card__content) {
  display: flex;
  flex-direction: column;
  gap: 12px;
}

.footer-actions {
  display: flex;
  justify-content: flex-end;
  gap: 12px;
}
</style>
