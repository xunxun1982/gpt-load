<script setup lang="ts">
import { keysApi } from "@/api/keys";
import { siteManagementApi, type SiteBindingOption } from "@/api/site-management";
import { appState, triggerSiteBindingRefresh } from "@/utils/app-state";
import { formatBalanceValue } from "@/utils/display";
import { ArrowForward, LinkOutline, UnlinkOutline } from "@vicons/ionicons5";
import { NButton, NIcon, NSelect, NTag, NTooltip, useMessage } from "naive-ui";
import { computed, onMounted, ref, watch } from "vue";
import { useI18n } from "vue-i18n";

const { t } = useI18n();
const message = useMessage();

interface Props {
  groupId: number | undefined;
  boundSiteId: number | null | undefined;
  disabled?: boolean;
}

interface Emits {
  (e: "bound", siteId: number, siteName: string, siteSort?: number): void;
  (e: "unbound"): void;
  (e: "navigate-to-site", siteId: number): void;
}

const props = defineProps<Props>();
const emit = defineEmits<Emits>();

const sites = ref<SiteBindingOption[]>([]);
const loading = ref(false);
const selectedSiteId = ref<number | null>(null);
const boundSiteName = ref<string>("");

// Filter options for select - allow binding to any enabled site (many-to-one)
const siteOptions = computed(() => {
  return sites.value.map(s => ({
    label: `${s.name}${!s.enabled ? ` (${t("keys.disabled")})` : ""}${s.bound_group_count ? ` [${s.bound_group_count}]` : ""}`,
    value: s.id,
    disabled: !s.enabled && s.id !== props.boundSiteId,
  }));
});

// Check if current group has a bound site
const hasBoundSite = computed(() => {
  return props.boundSiteId !== null && props.boundSiteId !== undefined && props.boundSiteId > 0;
});

const balanceSiteId = computed(() => {
  return hasBoundSite.value ? props.boundSiteId : selectedSiteId.value;
});

const balanceDisplay = computed(() => {
  const siteId = balanceSiteId.value;
  if (!siteId) {
    return "-";
  }

  if (Object.prototype.hasOwnProperty.call(appState.siteBalances, siteId)) {
    return formatBalanceValue(appState.siteBalances[siteId]);
  }

  return formatBalanceValue(sites.value.find(site => site.id === siteId)?.last_balance);
});

async function loadSites() {
  loading.value = true;
  try {
    sites.value = await siteManagementApi.listSitesForBinding();
    // Set initial selected value
    if (props.boundSiteId) {
      selectedSiteId.value = props.boundSiteId;
      const site = sites.value.find(s => s.id === props.boundSiteId);
      boundSiteName.value = site?.name || "";
    }
  } finally {
    loading.value = false;
  }
}

async function handleBind() {
  if (!props.groupId || !selectedSiteId.value) {
    return;
  }

  const siteId = selectedSiteId.value;
  try {
    await keysApi.bindGroupToSite(props.groupId, siteId);
    const site = sites.value.find(s => s.id === siteId);
    boundSiteName.value = site?.name || "";
    message.success(t("binding.bindSuccess"));
    triggerSiteBindingRefresh();
    await loadSites();
    emit("bound", siteId, boundSiteName.value, site?.sort);
  } catch (_) {
    // Error handled by http interceptor
  }
}

async function handleUnbind() {
  if (!props.groupId) {
    return;
  }

  try {
    await keysApi.unbindGroupFromSite(props.groupId);
    selectedSiteId.value = null;
    boundSiteName.value = "";
    message.success(t("binding.unbindSuccess"));
    triggerSiteBindingRefresh();
    await loadSites();
    emit("unbound");
  } catch (_) {
    // Error handled by http interceptor
  }
}

function handleNavigateToSite() {
  if (props.boundSiteId) {
    emit("navigate-to-site", props.boundSiteId);
  }
}

// Watch for group changes
watch(
  () => props.groupId,
  () => {
    loadSites();
  }
);

watch(
  () => props.boundSiteId,
  newVal => {
    selectedSiteId.value = newVal ?? null;
    if (newVal) {
      const site = sites.value.find(s => s.id === newVal);
      boundSiteName.value = site?.name || "";
    } else {
      boundSiteName.value = "";
    }
  }
);

onMounted(() => {
  loadSites();
});
</script>

<template>
  <div class="site-binding-selector">
    <div class="binding-row">
      <!-- Site selector -->
      <n-select
        class="site-select"
        v-model:value="selectedSiteId"
        :options="siteOptions"
        :placeholder="t('binding.selectSite')"
        :loading="loading"
        :disabled="disabled || hasBoundSite"
        filterable
        clearable
        size="small"
      />

      <span class="site-balance" :title="balanceDisplay">
        {{ balanceDisplay }}
      </span>

      <!-- Bind/Unbind button -->
      <n-tooltip v-if="!hasBoundSite" trigger="hover">
        <template #trigger>
          <n-button
            size="small"
            type="primary"
            :disabled="!selectedSiteId || disabled"
            @click="handleBind"
          >
            <template #icon>
              <n-icon :component="LinkOutline" />
            </template>
            {{ t("binding.bind") }}
          </n-button>
        </template>
        {{ t("binding.bindTooltip") }}
      </n-tooltip>

      <n-tooltip v-else trigger="hover">
        <template #trigger>
          <n-button size="small" type="warning" :disabled="disabled" @click="handleUnbind">
            <template #icon>
              <n-icon :component="UnlinkOutline" />
            </template>
            {{ t("binding.unbind") }}
          </n-button>
        </template>
        {{ t("binding.unbindTooltip") }}
      </n-tooltip>

      <!-- Navigate to site button (only when bound) -->
      <n-tooltip v-if="hasBoundSite" trigger="hover">
        <template #trigger>
          <n-tag
            type="success"
            size="small"
            round
            :bordered="false"
            class="bound-site-tag"
            @click="handleNavigateToSite"
          >
            <template #icon>
              <n-icon :component="ArrowForward" />
            </template>
            {{ boundSiteName || t("binding.boundSite") }}
          </n-tag>
        </template>
        {{ t("binding.navigateToSite") }}
      </n-tooltip>
    </div>
  </div>
</template>

<style scoped>
.site-binding-selector {
  display: flex;
  align-items: center;
  min-width: 0;
  max-width: 100%;
  overflow: hidden;
  white-space: nowrap;
}

.binding-row {
  display: flex;
  align-items: center;
  gap: 8px;
  width: 100%;
  min-width: 0;
  max-width: 100%;
  overflow: hidden;
}

.site-select {
  flex: 1 1 180px;
  min-width: 80px;
  max-width: 180px;
}

.binding-row :deep(.n-button) {
  flex: 0 0 auto;
}

.site-balance {
  display: inline-block;
  flex: 0 0 auto;
  max-width: 112px;
  overflow: hidden;
  padding: 3px 8px;
  color: #2080f0;
  font-size: 12px;
  font-weight: 600;
  line-height: 1.4;
  text-overflow: ellipsis;
  white-space: nowrap;
  background: rgba(32, 128, 240, 0.1);
  border: 1px solid rgba(32, 128, 240, 0.22);
  border-radius: 999px;
}

.bound-site-tag {
  flex: 0 1 auto;
  min-width: 0;
  max-width: 140px;
  overflow: hidden;
  cursor: pointer;
  transition: all 0.2s ease;
}

.bound-site-tag :deep(.n-tag__content) {
  min-width: 0;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.bound-site-tag:hover {
  transform: translateX(2px);
  opacity: 0.9;
}
</style>
