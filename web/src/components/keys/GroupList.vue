<script setup lang="ts">
import type { Group } from "@/types/models";
import { getGroupDisplayName } from "@/utils/display";
import {
  Add,
  LinkOutline,
  Search,
  ChevronDown,
  ChevronForward,
  CloudDownloadOutline,
  CloudUploadOutline,
} from "@vicons/ionicons5";
import {
  NButton,
  NCard,
  NEmpty,
  NInput,
  NSpin,
  NTag,
  NIcon,
  useDialog,
  useMessage,
} from "naive-ui";
import { computed, ref, watch } from "vue";
import { useI18n } from "vue-i18n";
import AggregateGroupModal from "./AggregateGroupModal.vue";
import GroupFormModal from "./GroupFormModal.vue";
import { keysApi } from "@/api/keys";

const { t } = useI18n();
const message = useMessage();
const dialog = useDialog();

// Constant definitions
const GROUP_TYPE_AGGREGATE = "aggregate" as const;
const ICON_AGGREGATE = "🔗";
const ICON_STANDARD = "📦";
const ICON_OPENAI = "🤖";
const ICON_GEMINI = "💎";
const ICON_ANTHROPIC = "🧠";
const ICON_DEFAULT = "🔧";

// Collapse state management
const collapsedSections = ref<Set<string>>(new Set());
const collapsedChannels = ref<Set<string>>(new Set());

interface Props {
  groups: Group[];
  selectedGroup: Group | null;
  loading?: boolean;
}

interface Emits {
  (e: "group-select", group: Group): void;
  (e: "refresh"): void;
  (e: "refresh-and-select", groupId: number): void;
}

type ChannelType = string;

interface GroupSection {
  groups: Group[];
  icon: string;
  titleKey: string;
  isAggregate: boolean;
  sectionKey: string;
}

interface ChannelGroup {
  channelType: ChannelType;
  groups: Group[];
  icon: string;
}

// Known channel type sort order
const KNOWN_CHANNEL_ORDER: string[] = ["openai", "gemini", "anthropic", "default"];

const props = withDefaults(defineProps<Props>(), {
  loading: false,
});

const emit = defineEmits<Emits>();

const searchText = ref("");
const showGroupModal = ref(false);
// Store references to group item DOM elements
const groupItemRefs = ref(new Map());
const showAggregateGroupModal = ref(false);
const fileInputRef = ref<HTMLInputElement | null>(null);

// Sort by sort field (ascending), if sort is the same, sort by id descending
function sortBySort(a: Group, b: Group) {
  const sortA = a.sort ?? 0;
  const sortB = b.sort ?? 0;
  if (sortA !== sortB) {
    return sortA - sortB;
  }
  return (b.id ?? 0) - (a.id ?? 0);
}

// Filtered and grouped group list
const filteredGroups = computed(() => {
  let groups = props.groups;

  // Apply search filter
  if (searchText.value.trim()) {
    const search = searchText.value.toLowerCase().trim();
    groups = groups.filter(
      group =>
        group.name.toLowerCase().includes(search) ||
        group.display_name?.toLowerCase().includes(search)
    );
  }

  // Separate aggregate groups and standard groups
  const aggregateGroups = groups.filter(g => g.group_type === GROUP_TYPE_AGGREGATE);
  const standardGroups = groups.filter(g => g.group_type !== GROUP_TYPE_AGGREGATE);

  aggregateGroups.sort(sortBySort);
  standardGroups.sort(sortBySort);

  return { aggregateGroups, standardGroups };
});

// Group section configuration
const groupSections = computed<GroupSection[]>(() => {
  const sections: GroupSection[] = [];

  if (filteredGroups.value.aggregateGroups.length > 0) {
    sections.push({
      groups: filteredGroups.value.aggregateGroups,
      icon: ICON_AGGREGATE,
      titleKey: "keys.aggregateGroupsTitle",
      isAggregate: true,
      sectionKey: "aggregate",
    });
  }

  if (filteredGroups.value.standardGroups.length > 0) {
    sections.push({
      groups: filteredGroups.value.standardGroups,
      icon: ICON_STANDARD,
      titleKey: "keys.standardGroupsTitle",
      isAggregate: false,
      sectionKey: "standard",
    });
  }

  return sections;
});

// Calculate channel groups for each group section (cached for performance)
const sectionChannelGroups = computed(() => {
  const result = new Map<string, ChannelGroup[]>();
  for (const section of groupSections.value) {
    result.set(section.sectionKey, groupByChannelType(section.groups));
  }
  return result;
});

// Get channel type icon (only provides specific icons for known types)
function getChannelTypeIcon(channelType: string): string {
  const lowerType = channelType.toLowerCase();
  switch (lowerType) {
    case "openai":
      return ICON_OPENAI;
    case "gemini":
      return ICON_GEMINI;
    case "anthropic":
      return ICON_ANTHROPIC;
    default:
      return ICON_DEFAULT;
  }
}

// Group by channel type (preserve all original channel types, no forced conversion)
function groupByChannelType(groups: Group[]): ChannelGroup[] {
  const channelMap = new Map<ChannelType, Group[]>();

  for (const group of groups) {
    // Preserve original channel type, only use default when empty
    const channelType = group.channel_type?.trim() || "default";
    if (!channelMap.has(channelType)) {
      channelMap.set(channelType, []);
    }
    channelMap.get(channelType)?.push(group);
  }

  const result: ChannelGroup[] = [];
  for (const [channelType, channelGroups] of channelMap) {
    // Sort groups within each channel type by sort
    channelGroups.sort(sortBySort);
    result.push({
      channelType,
      groups: channelGroups,
      icon: getChannelTypeIcon(channelType),
    });
  }

  // Sort by channel type: known types first, unknown types alphabetically
  result.sort((a, b) => {
    const aLower = a.channelType.toLowerCase();
    const bLower = b.channelType.toLowerCase();
    const aIndex = KNOWN_CHANNEL_ORDER.indexOf(aLower);
    const bIndex = KNOWN_CHANNEL_ORDER.indexOf(bLower);

    // Both are known types, sort by predefined order
    if (aIndex !== -1 && bIndex !== -1) {
      return aIndex - bIndex;
    }
    // a is known type, b is not, a comes first
    if (aIndex !== -1) {
      return -1;
    }
    // b is known type, a is not, b comes first
    if (bIndex !== -1) {
      return 1;
    }
    // Both are unknown types, sort alphabetically
    return aLower.localeCompare(bLower);
  });

  return result;
}

// Toggle group section collapse state
function toggleSection(sectionKey: string) {
  const next = new Set(collapsedSections.value);
  if (next.has(sectionKey)) {
    next.delete(sectionKey);
  } else {
    next.add(sectionKey);
  }
  collapsedSections.value = next;
}

// Toggle channel group collapse state
function toggleChannel(sectionKey: string, channelType: string) {
  const key = `${sectionKey}-${channelType}`;
  const next = new Set(collapsedChannels.value);
  if (next.has(key)) {
    next.delete(key);
  } else {
    next.add(key);
  }
  collapsedChannels.value = next;
}

// Check if group section is collapsed
function isSectionCollapsed(sectionKey: string): boolean {
  return collapsedSections.value.has(sectionKey);
}

// Check if channel group is collapsed
function isChannelCollapsed(sectionKey: string, channelType: string): boolean {
  return collapsedChannels.value.has(`${sectionKey}-${channelType}`);
}

// Get group icon
function getGroupIcon(group: Group, isAggregate: boolean): string {
  if (isAggregate) {
    return ICON_AGGREGATE;
  }
  const channelType = group.channel_type?.trim() || "default";
  return getChannelTypeIcon(channelType);
}

// Watch for changes in selected item ID and automatically scroll to that item
watch(
  () => props.selectedGroup?.id,
  id => {
    if (!id || props.groups.length === 0) {
      return;
    }

    const element = groupItemRefs.value.get(id);
    if (element) {
      element.scrollIntoView({
        behavior: "smooth", // Smooth scrolling
        block: "nearest", // Scroll element to nearest edge
      });
    }
  },
  {
    flush: "post", // Ensure callback executes after DOM update
    immediate: true, // Execute once immediately to handle initial load
  }
);

function handleGroupClick(group: Group) {
  // Allow selecting disabled groups so users can enable or modify configuration
  emit("group-select", group);
}

// Get channel type tag color
function getChannelTagType(channelType: string) {
  const normalized = channelType?.trim().toLowerCase();
  switch (normalized) {
    case "openai":
      return "success";
    case "gemini":
      return "info";
    case "anthropic":
      return "warning";
    default:
      return "default";
  }
}

function openCreateGroupModal() {
  showGroupModal.value = true;
}

function openCreateAggregateGroupModal() {
  showAggregateGroupModal.value = true;
}

function handleGroupCreated(group: Group) {
  showGroupModal.value = false;
  showAggregateGroupModal.value = false;
  const groupId = group.id;
  if (groupId) {
    emit("refresh-and-select", groupId);
  }
}

// Export group
async function handleExportGroup(group: Group, event: Event) {
  event.stopPropagation();

  if (!group || !group.id) {
    message.error(t("common.error"));
    return;
  }

  const { askExportMode } = await import("@/utils/export-import");
  const mode = await askExportMode(dialog, t);

  try {
    const groupId = group.id;
    await keysApi.exportGroup(groupId, mode);
    message.success(t("keys.exportSuccess"));
  } catch (_error: unknown) {
    const errorMessage = _error instanceof Error ? _error.message : t("keys.exportFailed");
    message.error(errorMessage);
  }
}

// Import state management
const isImporting = ref(false);

// Trigger file selection (group import)
function handleImportClick() {
  if (isImporting.value) {
    message.warning(t("keys.importInProgress"));
    return;
  }
  fileInputRef.value?.click();
}

// Handle file import (group import)
async function handleFileChange(event: Event) {
  const target = event.target as HTMLInputElement;
  const file = target.files?.[0];

  if (!file) {
    return;
  }

  try {
    const text = await file.text();
    const data = JSON.parse(text);

    const { askImportMode } = await import("@/utils/export-import");
    const mode = await askImportMode(dialog, t);

    // Prevent duplicate imports
    if (isImporting.value) {
      message.warning(t("keys.importInProgress"));
      return;
    }

    isImporting.value = true;
    const loadingMessage = message.loading(t("keys.importing"), { duration: 0 });

    try {
      const created = await keysApi.importGroup(data, { mode, filename: file.name });
      message.success(t("keys.importSuccess"));
      if (created?.id) {
        emit("refresh-and-select", created.id);
      } else {
        emit("refresh");
      }
    } catch (_error: unknown) {
      const errorMessage = _error instanceof Error ? _error.message : t("keys.importFailed");
      message.error(errorMessage);
    } finally {
      loadingMessage.destroy();
      isImporting.value = false;
    }
  } catch (_error) {
    message.error(t("keys.invalidImportFile"));
  } finally {
    // Clear file input to allow selecting the same file again
    target.value = "";
  }
}
</script>

<template>
  <div class="group-list-container">
    <n-card class="group-list-card modern-card" :bordered="false" size="small">
      <!-- Search box and import/export buttons -->
      <div class="search-section">
        <n-input
          v-model:value="searchText"
          :placeholder="t('keys.searchGroupPlaceholder')"
          size="small"
          clearable
          style="flex: 1"
        >
          <template #prefix>
            <n-icon :component="Search" />
          </template>
        </n-input>
        <n-button
          type="primary"
          size="small"
          @click="handleImportClick"
          :title="t('keys.importGroup')"
        >
          <template #icon>
            <n-icon :component="CloudUploadOutline" />
          </template>
        </n-button>
      </div>

      <!-- Group list -->
      <div class="groups-section">
        <n-spin :show="loading" size="small">
          <div
            v-if="
              filteredGroups.aggregateGroups.length === 0 &&
              filteredGroups.standardGroups.length === 0 &&
              !loading
            "
            class="empty-container"
          >
            <n-empty
              size="small"
              :description="searchText ? t('keys.noMatchingGroups') : t('keys.noGroups')"
            />
          </div>
          <div v-else class="groups-list">
            <!-- Group section (unified rendering) -->
            <div v-for="section in groupSections" :key="section.titleKey" class="group-section">
              <div
                class="section-header"
                role="button"
                tabindex="0"
                :aria-expanded="!isSectionCollapsed(section.sectionKey)"
                @click="toggleSection(section.sectionKey)"
                @keydown.enter="toggleSection(section.sectionKey)"
                @keydown.space.prevent="toggleSection(section.sectionKey)"
              >
                <n-icon
                  class="collapse-icon"
                  :component="isSectionCollapsed(section.sectionKey) ? ChevronForward : ChevronDown"
                />
                <span class="section-icon">{{ section.icon }}</span>
                <span class="section-title">{{ t(section.titleKey) }}</span>
                <span class="section-count">{{ section.groups.length }}</span>
              </div>
              <div v-if="!isSectionCollapsed(section.sectionKey)" class="section-items">
                <!-- Group by channel type -->
                <div
                  v-for="channelGroup in sectionChannelGroups.get(section.sectionKey) || []"
                  :key="channelGroup.channelType"
                  class="channel-group"
                >
                  <div
                    class="channel-header"
                    role="button"
                    tabindex="0"
                    :aria-expanded="
                      !isChannelCollapsed(section.sectionKey, channelGroup.channelType)
                    "
                    @click="toggleChannel(section.sectionKey, channelGroup.channelType)"
                    @keydown.enter="toggleChannel(section.sectionKey, channelGroup.channelType)"
                    @keydown.space.prevent="
                      toggleChannel(section.sectionKey, channelGroup.channelType)
                    "
                  >
                    <n-icon
                      class="collapse-icon"
                      :component="
                        isChannelCollapsed(section.sectionKey, channelGroup.channelType)
                          ? ChevronForward
                          : ChevronDown
                      "
                    />
                    <span class="channel-icon">{{ channelGroup.icon }}</span>
                    <span class="channel-title">{{ channelGroup.channelType }}</span>
                    <span class="channel-count">{{ channelGroup.groups.length }}</span>
                  </div>
                  <div
                    v-if="!isChannelCollapsed(section.sectionKey, channelGroup.channelType)"
                    class="channel-items"
                  >
                    <div
                      v-for="group in channelGroup.groups"
                      :key="group.id"
                      class="group-item"
                      :class="{
                        aggregate: section.isAggregate,
                        active: selectedGroup?.id === group.id,
                        disabled: !group.enabled,
                      }"
                      :aria-label="
                        !group.enabled
                          ? `${getGroupDisplayName(group)} (${t('keys.disabled')})`
                          : undefined
                      "
                      @click="handleGroupClick(group)"
                      :ref="
                        el => {
                          if (el) {
                            groupItemRefs.set(group.id, el);
                          } else {
                            groupItemRefs.delete(group.id);
                          }
                        }
                      "
                    >
                      <div class="group-icon">
                        <span>{{ getGroupIcon(group, section.isAggregate) }}</span>
                      </div>
                      <div class="group-content">
                        <div class="group-name">{{ getGroupDisplayName(group) }}</div>
                        <div class="group-meta">
                          <n-tag size="tiny" :type="getChannelTagType(group.channel_type)">
                            {{ group.channel_type }}
                          </n-tag>
                          <n-tag v-if="!group.enabled" size="tiny" type="error" round>
                            {{ t("keys.disabled") }}
                          </n-tag>
                          <span class="group-id">#{{ group.name }}</span>
                        </div>
                      </div>
                      <div class="group-actions" @click.stop>
                        <n-button
                          text
                          size="tiny"
                          @click="handleExportGroup(group, $event)"
                          :title="t('keys.exportGroup')"
                        >
                          <template #icon>
                            <n-icon :component="CloudDownloadOutline" :size="16" />
                          </template>
                        </n-button>
                      </div>
                    </div>
                  </div>
                </div>
              </div>
            </div>
          </div>
        </n-spin>
      </div>

      <!-- Add group button -->
      <div class="add-section">
        <n-button type="success" size="small" block @click="openCreateGroupModal">
          <template #icon>
            <n-icon :component="Add" />
          </template>
          {{ t("keys.createGroup") }}
        </n-button>
        <n-button type="info" size="small" block @click="openCreateAggregateGroupModal">
          <template #icon>
            <n-icon :component="LinkOutline" />
          </template>
          {{ t("keys.createAggregateGroup") }}
        </n-button>
      </div>
    </n-card>

    <!-- Hidden file input -->
    <input
      ref="fileInputRef"
      type="file"
      accept=".json"
      style="display: none"
      @change="handleFileChange"
    />

    <group-form-modal v-model:show="showGroupModal" @success="handleGroupCreated" />
    <aggregate-group-modal
      v-model:show="showAggregateGroupModal"
      :groups="groups"
      @success="handleGroupCreated"
    />
  </div>
</template>

<style scoped>
:deep(.n-card__content) {
  height: 100%;
}

.groups-section::-webkit-scrollbar {
  width: 8px;
  height: 8px;
}

.groups-section::-webkit-scrollbar-track {
  background: var(--bg-secondary);
  border-radius: 4px;
}

.groups-section::-webkit-scrollbar-thumb {
  background: var(--scrollbar-bg);
  border-radius: 4px;
  border: 2px solid var(--bg-secondary);
}

.groups-section::-webkit-scrollbar-thumb:hover {
  background: var(--border-color);
}

.groups-section::-webkit-scrollbar-thumb:active {
  background: var(--primary-color);
}

.group-list-container {
  height: 100%;
}

.group-list-card {
  height: 100%;
  display: flex;
  flex-direction: column;
  background: var(--card-bg-solid);
}

.group-list-card:hover {
  transform: none;
  box-shadow: var(--shadow-lg);
}

.search-section {
  height: 41px;
  display: flex;
  align-items: center;
  gap: 8px;
}

.groups-section {
  flex: 1;
  height: calc(100% - 120px);
  overflow: auto;
}

.empty-container {
  padding: 20px 0;
}

.groups-list {
  display: flex;
  flex-direction: column;
  gap: 4px;
  max-height: 100%;
  overflow-y: auto;
  width: 100%;
}

/* Group section */
.group-section {
  display: flex;
  flex-direction: column;
  gap: 0px;
}

/* Section header */
.section-header {
  display: flex;
  align-items: center;
  gap: 3px;
  padding: 2px 4px;
  font-size: 13px;
  font-weight: 700;
  color: var(--text-primary);
  letter-spacing: 0.3px;
  background: var(--bg-secondary);
  border-radius: 3px;
  margin-bottom: 2px;
  transition: all 0.2s ease;
  cursor: pointer;
  user-select: none;
}

.section-header:hover {
  background: var(--bg-tertiary);
}

.section-header:focus {
  outline: 2px solid var(--primary-color);
  outline-offset: 2px;
}

.collapse-icon {
  font-size: 14px;
  transition: transform 0.2s ease;
}

.section-icon {
  font-size: 16px;
  line-height: 1;
}

.section-title {
  flex: 1;
  font-size: 13px;
  font-weight: 700;
}

.section-count {
  font-size: 12px;
  font-weight: 600;
  color: var(--text-secondary);
  background: var(--bg-tertiary);
  padding: 2px 8px;
  border-radius: 10px;
  min-width: 24px;
  text-align: center;
}

/* Section items container */
.section-items {
  display: flex;
  flex-direction: column;
  gap: 3px;
  padding-left: 6px;
  border-left: 2px solid var(--border-color);
  margin-left: 5px;
}

/* Channel group */
.channel-group {
  display: flex;
  flex-direction: column;
  gap: 2px;
}

.channel-header {
  display: flex;
  align-items: center;
  gap: 3px;
  padding: 2px 5px;
  font-size: 12px;
  font-weight: 600;
  color: var(--text-secondary);
  background: var(--bg-tertiary);
  border-radius: 3px;
  cursor: pointer;
  user-select: none;
  transition: all 0.2s ease;
}

.channel-header:hover {
  background: var(--bg-secondary);
  color: var(--text-primary);
}

.channel-header:focus {
  outline: 2px solid var(--primary-color);
  outline-offset: 2px;
}

.channel-icon {
  font-size: 14px;
  line-height: 1;
}

.channel-title {
  flex: 1;
  font-size: 12px;
  text-transform: capitalize;
}

.channel-count {
  font-size: 11px;
  font-weight: 500;
  color: var(--text-secondary);
  background: var(--bg-secondary);
  padding: 1px 6px;
  border-radius: 8px;
  min-width: 20px;
  text-align: center;
}

.channel-items {
  display: flex;
  flex-direction: column;
  gap: 2px;
  padding-left: 8px;
  margin-left: 2px;
  border-left: 1px solid var(--border-color);
}

/* Dark mode optimization */
:root.dark .section-header {
  color: rgba(255, 255, 255, 0.95);
  background: rgba(255, 255, 255, 0.05);
}

:root.dark .section-header:hover {
  background: rgba(255, 255, 255, 0.08);
}

:root.dark .section-count {
  color: rgba(255, 255, 255, 0.7);
  background: rgba(255, 255, 255, 0.08);
}

:root.dark .section-items {
  border-left-color: rgba(255, 255, 255, 0.1);
}

:root.dark .channel-header {
  color: rgba(255, 255, 255, 0.7);
  background: rgba(255, 255, 255, 0.03);
}

:root.dark .channel-header:hover {
  background: rgba(255, 255, 255, 0.05);
  color: rgba(255, 255, 255, 0.9);
}

:root.dark .channel-count {
  color: rgba(255, 255, 255, 0.6);
  background: rgba(255, 255, 255, 0.05);
}

:root.dark .channel-items {
  border-left-color: rgba(255, 255, 255, 0.08);
}

.group-item {
  display: flex;
  align-items: center;
  gap: 6px;
  padding: 5px 7px;
  border-radius: 4px;
  cursor: pointer;
  transition: all 0.2s ease;
  border: 1px solid var(--border-color);
  font-size: 12px;
  color: var(--text-primary);
  background: transparent;
  box-sizing: border-box;
  position: relative;
}

/* Aggregate group style */
.group-item.aggregate {
  border-style: dashed;
  background: linear-gradient(135deg, rgba(102, 126, 234, 0.02) 0%, rgba(102, 126, 234, 0.05) 100%);
}

:root.dark .group-item.aggregate {
  background: linear-gradient(135deg, rgba(102, 126, 234, 0.05) 0%, rgba(102, 126, 234, 0.1) 100%);
  border-color: rgba(102, 126, 234, 0.2);
}

.group-item:hover {
  background: var(--bg-tertiary);
  border-color: var(--primary-color);
}

.group-item.aggregate:hover {
  background: linear-gradient(135deg, rgba(102, 126, 234, 0.05) 0%, rgba(102, 126, 234, 0.1) 100%);
  border-style: dashed;
  border-color: var(--primary-color);
}

:root.dark .group-item:hover {
  background: rgba(102, 126, 234, 0.1);
  border-color: rgba(102, 126, 234, 0.3);
}

:root.dark .group-item.aggregate:hover {
  background: linear-gradient(135deg, rgba(102, 126, 234, 0.1) 0%, rgba(102, 126, 234, 0.15) 100%);
  border-color: rgba(102, 126, 234, 0.4);
}

/* Keep text visible in hover state */
.group-item:hover .group-name {
  color: var(--text-primary) !important;
  opacity: 1 !important;
}

:root.dark .group-item:hover .group-name {
  color: rgba(255, 255, 255, 0.95) !important;
  opacity: 1 !important;
}

.group-item:hover .group-id {
  color: var(--text-secondary) !important;
  opacity: 1 !important;
}

:root.dark .group-item:hover .group-id {
  color: rgba(255, 255, 255, 0.7) !important;
  opacity: 1 !important;
}

.group-item.aggregate.active {
  background: var(--primary-gradient);
  border-style: solid;
}

.group-item.active,
:root.dark .group-item.active,
:root.dark .group-item.aggregate.active {
  background: var(--primary-gradient);
  color: white;
  border-color: transparent;
  box-shadow: var(--shadow-md);
  border-style: solid;
}

.group-icon {
  font-size: 16px;
  width: 28px;
  height: 28px;
  display: flex;
  align-items: center;
  justify-content: center;
  background: var(--bg-secondary);
  border-radius: 6px;
  flex-shrink: 0;
  box-sizing: border-box;
}

.group-item.active .group-icon {
  background: rgba(255, 255, 255, 0.2);
}

.group-content {
  flex: 1;
  min-width: 0;
}

.group-actions {
  display: flex;
  align-items: center;
  gap: 4px;
  opacity: 0;
  transition: opacity 0.2s ease;
  flex-shrink: 0;
  margin-left: auto;
  padding-left: 8px;
}

.group-item:hover .group-actions {
  opacity: 1;
}

.group-name {
  font-weight: 600;
  font-size: 14px;
  line-height: 1.2;
  margin-bottom: 4px;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.group-meta {
  display: flex;
  align-items: center;
  gap: 6px;
  font-size: 10px;
  flex-wrap: wrap;
}

.group-id {
  opacity: 0.8;
  color: var(--text-secondary);
}

/* Text style in selected state - more prominent */
.group-item.active .group-name {
  color: white !important;
  font-weight: 700;
  text-shadow: 0 1px 3px rgba(0, 0, 0, 0.2);
}

.group-item.active .group-id {
  opacity: 1 !important;
  color: rgba(255, 255, 255, 0.95) !important;
  font-weight: 500;
}

/* Maintain style when hovering in selected state */
.group-item.active:hover .group-name {
  color: white !important;
  font-weight: 700;
}

.group-item.active:hover .group-id {
  color: rgba(255, 255, 255, 0.95) !important;
  opacity: 1 !important;
}

/* Disabled but selected group - keep selected state visible */
.group-item.disabled.active {
  background: var(--primary-gradient);
  opacity: 0.75;
  border-color: transparent;
  color: white;
}

:root.dark .group-item.disabled.active {
  background: var(--primary-gradient);
  opacity: 0.75;
}

/* Text in disabled but selected state - keep prominent */
.group-item.disabled.active .group-name {
  color: white !important;
  font-weight: 700;
  opacity: 1 !important;
  text-shadow: 0 1px 3px rgba(0, 0, 0, 0.2);
}

.group-item.disabled.active .group-id {
  color: rgba(255, 255, 255, 0.9) !important;
  opacity: 1 !important;
  font-weight: 500;
}

/* Maintain style when hovering in disabled but selected state */
.group-item.disabled.active:hover .group-name {
  color: white !important;
}

.group-item.disabled.active:hover .group-id {
  color: rgba(255, 255, 255, 0.9) !important;
}

/* Disabled group style - use orange border and light background to indicate disabled */
.group-item.disabled {
  background: linear-gradient(135deg, rgba(245, 166, 35, 0.12) 0%, rgba(230, 140, 20, 0.12) 100%);
  border-color: #f5a623;
  border-width: 2px;
}

:root.dark .group-item.disabled {
  background: linear-gradient(135deg, rgba(245, 166, 35, 0.15) 0%, rgba(230, 140, 20, 0.15) 100%);
  border-color: rgba(245, 166, 35, 0.7);
  border-width: 2px;
}

.group-item.disabled:hover {
  background: linear-gradient(135deg, rgba(245, 166, 35, 0.9) 0%, rgba(230, 140, 20, 0.9) 100%);
  border-color: #f5a623;
  border-width: 2px;
}

:root.dark .group-item.disabled:hover {
  background: linear-gradient(135deg, rgba(245, 166, 35, 0.85) 0%, rgba(230, 140, 20, 0.85) 100%);
  border-color: rgba(245, 166, 35, 0.9);
  border-width: 2px;
}

.group-item.disabled.aggregate:hover {
  background: linear-gradient(135deg, rgba(245, 166, 35, 0.9) 0%, rgba(230, 140, 20, 0.9) 100%);
  border-style: dashed;
  border-color: #f5a623;
  border-width: 2px;
}

:root.dark .group-item.disabled.aggregate:hover {
  background: linear-gradient(135deg, rgba(245, 166, 35, 0.85) 0%, rgba(230, 140, 20, 0.85) 100%);
  border-color: rgba(245, 166, 35, 0.9);
  border-width: 2px;
}

/* Text in disabled state hover - use white text with dark background for maximum contrast */
.group-item.disabled:hover .group-name {
  color: #ffffff !important;
  opacity: 1 !important;
  font-weight: 700 !important;
  text-shadow: 0 1px 2px rgba(0, 0, 0, 0.3);
}

:root.dark .group-item.disabled:hover .group-name {
  color: #ffffff !important;
  opacity: 1 !important;
  font-weight: 700 !important;
  text-shadow: 0 1px 2px rgba(0, 0, 0, 0.3);
}

.group-item.disabled:hover .group-id {
  color: rgba(255, 255, 255, 0.95) !important;
  opacity: 1 !important;
  font-weight: 500 !important;
}

:root.dark .group-item.disabled:hover .group-id {
  color: rgba(255, 255, 255, 0.95) !important;
  opacity: 1 !important;
  font-weight: 500 !important;
}

/* Disabled group text color - enhanced contrast */
.group-item.disabled .group-name {
  color: rgba(0, 0, 0, 0.85);
  opacity: 1;
  font-weight: 600;
}

.group-item.disabled .group-id {
  color: rgba(0, 0, 0, 0.65);
  opacity: 1;
}

/* Disabled group text color - dark mode */
:root.dark .group-item.disabled .group-name {
  color: rgba(255, 255, 255, 0.85);
  opacity: 1;
  font-weight: 600;
}

:root.dark .group-item.disabled .group-id {
  color: rgba(255, 255, 255, 0.7);
  opacity: 1;
}

.group-item.disabled .group-icon {
  opacity: 0.7;
  background: rgba(245, 166, 35, 0.15);
}

:root.dark .group-item.disabled .group-icon {
  opacity: 0.8;
  background: rgba(245, 166, 35, 0.2);
}

/* Icon in disabled state hover - white background */
.group-item.disabled:hover .group-icon {
  background: rgba(255, 255, 255, 0.3) !important;
  opacity: 1 !important;
}

.add-section {
  border-top: 1px solid var(--border-color);
  padding-top: 12px;
  display: flex;
  flex-direction: column;
  gap: 8px;
}

/* Scrollbar style */
.groups-list::-webkit-scrollbar {
  width: 8px;
}

.groups-list::-webkit-scrollbar-track {
  background: var(--bg-secondary);
  border-radius: 4px;
  margin: 4px 0;
}

.groups-list::-webkit-scrollbar-thumb {
  background: var(--scrollbar-bg);
  border-radius: 4px;
  border: 2px solid var(--bg-secondary);
  min-height: 40px;
}

.groups-list::-webkit-scrollbar-thumb:hover {
  background: var(--border-color);
}

.groups-list::-webkit-scrollbar-thumb:active {
  background: var(--primary-color);
}

/* Dark mode special styles */
:root.dark .group-item {
  border-color: rgba(255, 255, 255, 0.05);
}

:root.dark .group-icon {
  background: rgba(255, 255, 255, 0.05);
  border: 1px solid rgba(255, 255, 255, 0.08);
}

:root.dark .search-section :deep(.n-input) {
  --n-border: 1px solid rgba(255, 255, 255, 0.08);
  --n-border-hover: 1px solid rgba(102, 126, 234, 0.4);
  --n-border-focus: 1px solid var(--primary-color);
  background: rgba(255, 255, 255, 0.03);
}

/* Tag style optimization */
:root.dark .group-meta :deep(.n-tag) {
  background: rgba(102, 126, 234, 0.15);
  border: 1px solid rgba(102, 126, 234, 0.3);
}

:root.dark .group-item.active .group-meta :deep(.n-tag) {
  background: rgba(255, 255, 255, 0.2);
  border-color: rgba(255, 255, 255, 0.3);
}
</style>
