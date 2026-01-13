<script setup lang="ts">
import { keysApi } from "@/api/keys";
import { siteManagementApi } from "@/api/site-management";
import { triggerSiteBindingRefresh } from "@/utils/app-state";
import { ArrowForward, LinkOutline, UnlinkOutline } from "@vicons/ionicons5";
import { NButton, NIcon, NSelect, NSpace, NTag, NTooltip, useMessage } from "naive-ui";
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
  (e: "bound", siteId: number, siteName: string): void;
  (e: "unbound"): void;
  (e: "navigate-to-site", siteId: number): void;
}

const props = defineProps<Props>();
const emit = defineEmits<Emits>();

interface SiteOption {
  id: number;
  name: string;
  sort: number;
  enabled: boolean;
  bound_group_count?: number;
}

const sites = ref<SiteOption[]>([]);
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
    emit("bound", siteId, boundSiteName.value);
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
    <n-space align="center" :size="8" :wrap="false">
      <!-- Site selector -->
      <n-select
        v-model:value="selectedSiteId"
        :options="siteOptions"
        :placeholder="t('binding.selectSite')"
        :loading="loading"
        :disabled="disabled || hasBoundSite"
        filterable
        clearable
        size="small"
        style="min-width: 180px"
      />

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
    </n-space>
  </div>
</template>

<style scoped>
.site-binding-selector {
  display: flex;
  align-items: center;
}

.bound-site-tag {
  cursor: pointer;
  transition: all 0.2s ease;
}

.bound-site-tag:hover {
  transform: translateX(2px);
  opacity: 0.9;
}
</style>
