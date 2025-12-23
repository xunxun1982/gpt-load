<script setup lang="ts">
import CCBadge from "@/components/keys/CCBadge.vue";
import { computed } from "vue";
import { useI18n } from "vue-i18n";

interface Props {
  label: string;
  isChildGroup?: boolean;
  channelType?: string;
  ccSupport?: boolean;
  showChildTag?: boolean;
}

const props = withDefaults(defineProps<Props>(), {
  isChildGroup: false,
  channelType: undefined,
  ccSupport: false,
  showChildTag: true,
});

const { t } = useI18n();

const trimmedLabel = computed(() => props.label.trim());
const showCCBadge = computed(() => props.channelType === "openai" && props.ccSupport);
</script>

<template>
  <div class="group-select-label" :title="trimmedLabel">
    <span v-if="isChildGroup" class="child-indicator" aria-hidden="true">🌿</span>
    <span class="label-text">{{ trimmedLabel }}</span>
    <c-c-badge v-if="showCCBadge" channel-type="openai" :cc-support="true" />
    <span v-if="isChildGroup && showChildTag" class="child-tag">
      {{ t("keys.isChildGroup") }}
    </span>
  </div>
</template>

<style scoped>
.group-select-label {
  display: flex;
  align-items: center;
  gap: 6px;
  min-width: 0;
}

.label-text {
  min-width: 0;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.child-indicator {
  flex: 0 0 auto;
  color: var(--success-color);
}

.child-tag {
  flex: 0 0 auto;
  font-size: 11px;
  color: var(--success-color);
  background: var(--success-bg);
  padding: 1px 4px;
  border-radius: 3px;
}
</style>
