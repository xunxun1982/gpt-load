<script setup lang="ts">
interface ChannelTypeInfo {
  channelType: string;
  icon: string;
  color: string;
  sectionKey: string;
  isAggregate?: boolean;
}

interface Props {
  channelTypes: ChannelTypeInfo[];
  activeChannelType?: string;
}

interface Emits {
  (e: "navigate", sectionKey: string, channelType: string): void;
}

const props = withDefaults(defineProps<Props>(), {
  activeChannelType: undefined,
});

const emit = defineEmits<Emits>();

// Handle navigation click
function handleNavigate(item: ChannelTypeInfo) {
  emit("navigate", item.sectionKey, item.channelType);
}

// Check if channel type is active
function isActive(channelType: string): boolean {
  return props.activeChannelType === channelType;
}
</script>

<template>
  <div class="quick-navigation">
    <button
      v-for="item in channelTypes"
      :key="`${item.sectionKey}-${item.channelType}`"
      type="button"
      class="nav-indicator"
      :class="{ active: isActive(item.channelType) }"
      :style="{ '--indicator-color': item.color }"
      :title="item.isAggregate ? '聚合分组' : item.channelType"
      :aria-label="item.isAggregate ? '聚合分组' : item.channelType"
      @click="handleNavigate(item)"
    />
  </div>
</template>

<style scoped>
.quick-navigation {
  position: absolute;
  left: 0;
  top: 0;
  bottom: 0;
  width: 6px;
  display: flex;
  flex-direction: column;
  gap: 1px;
  padding: 8px 0;
  z-index: 10;
  pointer-events: none;
}

.nav-indicator {
  /* Reset button default styles */
  border: none;
  padding: 0;
  margin: 0;
  font: inherit;
  color: inherit;
  background: none;
  /* Apply custom styles */
  flex: 1;
  min-height: 24px;
  background: var(--indicator-color);
  opacity: 0.5;
  cursor: pointer;
  transition: all 0.3s cubic-bezier(0.4, 0, 0.2, 1);
  border-radius: 0 4px 4px 0;
  pointer-events: auto;
  position: relative;
  width: 6px;
}

.nav-indicator::after {
  content: "";
  position: absolute;
  left: 0;
  top: 0;
  bottom: 0;
  width: 0;
  background: var(--indicator-color);
  transition: width 0.3s cubic-bezier(0.4, 0, 0.2, 1);
  border-radius: 0 4px 4px 0;
  opacity: 0.8;
}

.nav-indicator:hover {
  opacity: 0.8;
  width: 16px;
}

.nav-indicator:hover::after {
  width: 8px;
}

.nav-indicator.active {
  opacity: 1;
  width: 20px;
  box-shadow: 0 0 16px var(--indicator-color);
}

.nav-indicator.active::after {
  width: 10px;
  opacity: 1;
}

/* Mobile optimization - hide on small screens */
@media (max-width: 767px) {
  .quick-navigation {
    display: none;
  }
}
</style>
