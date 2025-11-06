<script setup lang="ts">
import type { Group } from "@/types/models";
import { getGroupDisplayName } from "@/utils/display";
import { Add, LinkOutline, Search, ChevronDown, ChevronForward } from "@vicons/ionicons5";
import { NButton, NCard, NEmpty, NInput, NSpin, NTag, NIcon } from "naive-ui";
import { computed, ref, watch } from "vue";
import { useI18n } from "vue-i18n";
import AggregateGroupModal from "./AggregateGroupModal.vue";
import GroupFormModal from "./GroupFormModal.vue";

const { t } = useI18n();

// 常量定义
const GROUP_TYPE_AGGREGATE = "aggregate" as const;
const ICON_AGGREGATE = "🔗";
const ICON_STANDARD = "📦";
const ICON_OPENAI = "🤖";
const ICON_GEMINI = "💎";
const ICON_ANTHROPIC = "🧠";
const ICON_DEFAULT = "🔧";

// 折叠状态管理
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

type ChannelType = "openai" | "gemini" | "anthropic" | "default";

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

const props = withDefaults(defineProps<Props>(), {
  loading: false,
});

const emit = defineEmits<Emits>();

const searchText = ref("");
const showGroupModal = ref(false);
// 存储分组项 DOM 元素的引用
const groupItemRefs = ref(new Map());
const showAggregateGroupModal = ref(false);

// 按 sort 字段排序（升序），sort 相同时按 id 降序
function sortBySort(a: Group, b: Group) {
  const sortA = a.sort ?? 0;
  const sortB = b.sort ?? 0;
  if (sortA !== sortB) {
    return sortA - sortB;
  }
  return (b.id ?? 0) - (a.id ?? 0);
}

// 过滤和分组的分组列表
const filteredGroups = computed(() => {
  let groups = props.groups;

  // 应用搜索过滤
  if (searchText.value.trim()) {
    const search = searchText.value.toLowerCase().trim();
    groups = groups.filter(
      group =>
        group.name.toLowerCase().includes(search) ||
        group.display_name?.toLowerCase().includes(search)
    );
  }

  // 分离聚合分组和标准分组
  const aggregateGroups = groups.filter(g => g.group_type === GROUP_TYPE_AGGREGATE);
  const standardGroups = groups.filter(g => g.group_type !== GROUP_TYPE_AGGREGATE);

  aggregateGroups.sort(sortBySort);
  standardGroups.sort(sortBySort);

  return { aggregateGroups, standardGroups };
});

// 分组区域配置
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

// 为每个分组区域计算渠道分组（缓存以提高性能）
const sectionChannelGroups = computed(() => {
  const result = new Map<string, ChannelGroup[]>();
  for (const section of groupSections.value) {
    result.set(section.sectionKey, groupByChannelType(section.groups));
  }
  return result;
});

// 类型守卫：验证是否为有效的渠道类型
function isValidChannelType(value: string): value is ChannelType {
  return value === "openai" || value === "gemini" || value === "anthropic" || value === "default";
}

// 获取渠道类型图标
function getChannelTypeIcon(channelType: string): string {
  switch (channelType) {
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

// 按渠道类型分组
function groupByChannelType(groups: Group[]): ChannelGroup[] {
  const channelMap = new Map<ChannelType, Group[]>();

  for (const group of groups) {
    const rawType = group.channel_type?.trim() || "default";
    const channelType: ChannelType = isValidChannelType(rawType) ? rawType : "default";
    if (!channelMap.has(channelType)) {
      channelMap.set(channelType, []);
    }
    channelMap.get(channelType)?.push(group);
  }

  const result: ChannelGroup[] = [];
  for (const [channelType, channelGroups] of channelMap) {
    // 对每个渠道类型内的分组按 sort 排序
    channelGroups.sort(sortBySort);
    result.push({
      channelType,
      groups: channelGroups,
      icon: getChannelTypeIcon(channelType),
    });
  }

  // 按渠道类型排序以确保一致的显示顺序
  result.sort((a, b) => {
    const order: ChannelType[] = ["openai", "gemini", "anthropic", "default"];
    return order.indexOf(a.channelType) - order.indexOf(b.channelType);
  });

  return result;
}

// 切换分组区域折叠状态
function toggleSection(sectionKey: string) {
  const next = new Set(collapsedSections.value);
  if (next.has(sectionKey)) {
    next.delete(sectionKey);
  } else {
    next.add(sectionKey);
  }
  collapsedSections.value = next;
}

// 切换渠道分组折叠状态
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

// 检查分组区域是否折叠
function isSectionCollapsed(sectionKey: string): boolean {
  return collapsedSections.value.has(sectionKey);
}

// 检查渠道分组是否折叠
function isChannelCollapsed(sectionKey: string, channelType: string): boolean {
  return collapsedChannels.value.has(`${sectionKey}-${channelType}`);
}

// 获取分组图标
function getGroupIcon(group: Group, isAggregate: boolean): string {
  if (isAggregate) {
    return ICON_AGGREGATE;
  }
  return getChannelTypeIcon(group.channel_type || "default");
}

// 监听选中项 ID 的变化，并自动滚动到该项
watch(
  () => props.selectedGroup?.id,
  id => {
    if (!id || props.groups.length === 0) {
      return;
    }

    const element = groupItemRefs.value.get(id);
    if (element) {
      element.scrollIntoView({
        behavior: "smooth", // 平滑滚动
        block: "nearest", // 将元素滚动到最近的边缘
      });
    }
  },
  {
    flush: "post", // 确保在 DOM 更新后执行回调
    immediate: true, // 立即执行一次以处理初始加载
  }
);

function handleGroupClick(group: Group) {
  // 允许选中禁用的分组，以便用户可以启用或修改配置
  emit("group-select", group);
}

// 获取渠道类型的标签颜色
function getChannelTagType(channelType: string) {
  switch (channelType) {
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
  if (group?.id) {
    emit("refresh-and-select", group.id);
  }
}
</script>

<template>
  <div class="group-list-container">
    <n-card class="group-list-card modern-card" :bordered="false" size="small">
      <!-- 搜索框 -->
      <div class="search-section">
        <n-input
          v-model:value="searchText"
          :placeholder="t('keys.searchGroupPlaceholder')"
          size="small"
          clearable
        >
          <template #prefix>
            <n-icon :component="Search" />
          </template>
        </n-input>
      </div>

      <!-- 分组列表 -->
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
            <!-- 分组区域（统一渲染） -->
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
                <!-- 按渠道类型分组 -->
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
                    </div>
                  </div>
                </div>
              </div>
            </div>
          </div>
        </n-spin>
      </div>

      <!-- 添加分组按钮 -->
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

/* 分组区域 */
.group-section {
  display: flex;
  flex-direction: column;
  gap: 0px;
}

/* 区域标题 */
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

/* 区域项目容器 */
.section-items {
  display: flex;
  flex-direction: column;
  gap: 3px;
  padding-left: 6px;
  border-left: 2px solid var(--border-color);
  margin-left: 5px;
}

/* 渠道分组 */
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

/* 深色模式优化 */
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

/* 聚合分组样式 */
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

/* hover 状态下的文字保持可见 */
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

/* 选中状态下的文字样式 - 更加醒目 */
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

/* 选中状态 hover 时保持样式 */
.group-item.active:hover .group-name {
  color: white !important;
  font-weight: 700;
}

.group-item.active:hover .group-id {
  color: rgba(255, 255, 255, 0.95) !important;
  opacity: 1 !important;
}

/* 禁用但已选中的分组 - 保持选中状态可见 */
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

/* 禁用但已选中状态下的文字 - 保持醒目 */
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

/* 禁用但已选中状态 hover 时保持样式 */
.group-item.disabled.active:hover .group-name {
  color: white !important;
}

.group-item.disabled.active:hover .group-id {
  color: rgba(255, 255, 255, 0.9) !important;
}

/* 禁用分组样式 - 使用橙色边框和淡背景表示禁用 */
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

/* 禁用状态 hover 的文字 - 使用白色文字配深色背景，最高对比度 */
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

/* 禁用分组的文字颜色 - 增强对比度 */
.group-item.disabled .group-name {
  color: rgba(0, 0, 0, 0.85);
  opacity: 1;
  font-weight: 600;
}

.group-item.disabled .group-id {
  color: rgba(0, 0, 0, 0.65);
  opacity: 1;
}

/* 禁用分组的文字颜色 - 暗黑模式 */
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

/* 禁用状态 hover 时的图标 - 白色背景 */
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

/* 滚动条样式 */
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

/* 暗黑模式特殊样式 */
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

/* 标签样式优化 */
:root.dark .group-meta :deep(.n-tag) {
  background: rgba(102, 126, 234, 0.15);
  border: 1px solid rgba(102, 126, 234, 0.3);
}

:root.dark .group-item.active .group-meta :deep(.n-tag) {
  background: rgba(255, 255, 255, 0.2);
  border-color: rgba(255, 255, 255, 0.3);
}
</style>
