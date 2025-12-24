<script setup lang="ts">
import { NCard, NEmpty, NSpace } from "naive-ui";
import { useI18n } from "vue-i18n";

const { t } = useI18n();
</script>

<template>
  <n-space vertical>
    <n-card size="small" :title="t('nav.more')" hoverable bordered>
      <n-empty :description="t('more.emptyDescription')" />
    </n-card>
  </n-space>
</template>
