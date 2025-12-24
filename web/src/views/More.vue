<script setup lang="ts">
import { NCard, NEmpty, NTabPane, NTabs } from "naive-ui";
import { ref, watch } from "vue";
import { useI18n } from "vue-i18n";
import { useRoute, useRouter } from "vue-router";

const { t } = useI18n();

type MoreTab = "site" | "central" | "agent";

const DEFAULT_TAB: MoreTab = "site";

const router = useRouter();
const route = useRoute();

const panes: Array<{ key: MoreTab; labelKey: string }> = [
  { key: "site", labelKey: "more.siteManagement" },
  { key: "central", labelKey: "more.centralService" },
  { key: "agent", labelKey: "more.agent" },
];

function normalizeTab(value: unknown): MoreTab {
  const raw = Array.isArray(value) ? value[0] : value;
  if (raw === "site" || raw === "central" || raw === "agent") {
    return raw;
  }
  return DEFAULT_TAB;
}

const activeTab = ref<MoreTab>(DEFAULT_TAB);

watch(
  () => route.query.tab,
  tab => {
    activeTab.value = normalizeTab(tab);
  },
  { immediate: true }
);

function handleTabChange(tab: MoreTab) {
  const current = normalizeTab(route.query.tab);
  if (tab === current) {
    return;
  }

  router.replace({
    name: "more",
    query: {
      ...route.query,
      tab,
    },
  });
}
</script>

<template>
  <div class="more-page">
    <n-card size="small" hoverable bordered :content-style="{ padding: '4px 12px 8px' }">
      <n-tabs
        size="small"
        :value="activeTab"
        animated
        type="line"
        :pane-style="{ padding: '6px 0 0' }"
        @update:value="handleTabChange"
      >
        <template #prefix>
          <span class="more-title">{{ t("nav.more") }}</span>
        </template>
        <n-tab-pane v-for="pane in panes" :key="pane.key" :name="pane.key" :tab="t(pane.labelKey)">
          <n-empty size="tiny" :show-icon="false" :description="t('more.emptyDescription')" />
        </n-tab-pane>
      </n-tabs>
    </n-card>
  </div>
</template>

<style scoped>
.more-page {
  display: flex;
  flex-direction: column;
}

:deep(.n-tabs-nav__prefix) {
  display: flex;
  align-items: center;
}

.more-title {
  font-weight: 600;
  margin-right: 8px;
}
</style>
