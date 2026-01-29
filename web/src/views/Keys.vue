<script setup lang="ts">
import { keysApi } from "@/api/keys";
import EncryptionMismatchAlert from "@/components/EncryptionMismatchAlert.vue";
import GroupInfoCard from "@/components/keys/GroupInfoCard.vue";
import GroupList from "@/components/keys/GroupList.vue";
import KeyTable from "@/components/keys/KeyTable.vue";
import QuickNavigation from "@/components/keys/QuickNavigation.vue";
import SubGroupTable from "@/components/keys/SubGroupTable.vue";
import type { Group, SubGroupInfo } from "@/types/models";
import { appState } from "@/utils/app-state";
import { getNearestGroupIdAfterDeletion, getSidebarOrderedGroupIds } from "@/utils/group-sidebar";
import { computed, onBeforeUnmount, onMounted, ref, watch } from "vue";
import { useRoute, useRouter } from "vue-router";

const LAST_SELECTED_GROUP_ID_KEY = "keys:lastGroupId";
const groups = ref<Group[]>([]);
const loading = ref(false);
const selectedGroup = ref<Group | null>(null);
const subGroups = ref<SubGroupInfo[]>([]);
const loadingSubGroups = ref(false);
const router = useRouter();
const route = useRoute();
// Ref to GroupList component for direct method calls
const groupListRef = ref<InstanceType<typeof GroupList> | null>(null);
// Active channel type for quick navigation
const activeChannelType = ref<string | undefined>(undefined);
// Timer for resetting active channel type
const activeChannelResetTimer = ref<number | undefined>(undefined);

// Get visible channel types for quick navigation
const visibleChannelTypes = computed(() => {
  if (!groupListRef.value) {
    return [];
  }
  const channelTypes = groupListRef.value.getVisibleChannelTypes();

  // Group by section: aggregate and standard
  const result: Array<{
    channelType: string;
    icon: string;
    color: string;
    sectionKey: string;
    isAggregate?: boolean;
  }> = [];

  // Add aggregate group as one item (if exists)
  const aggregateItems = channelTypes.filter(item => item.sectionKey === "aggregate");
  if (aggregateItems.length > 0) {
    result.push({
      channelType: "aggregate",
      icon: "🔗",
      color: "#667eea",
      sectionKey: "aggregate",
      isAggregate: true,
    });
  }

  // Add standard groups by channel type
  const standardItems = channelTypes.filter(item => item.sectionKey === "standard");
  const uniqueChannelTypes = new Map<string, (typeof standardItems)[0]>();

  for (const item of standardItems) {
    if (!uniqueChannelTypes.has(item.channelType)) {
      uniqueChannelTypes.set(item.channelType, item);
    }
  }

  for (const item of uniqueChannelTypes.values()) {
    result.push({
      ...item,
      color: getChannelColor(item.channelType),
    });
  }

  return result;
});

// Get channel type display color
function getChannelColor(channelType: string): string {
  const channelColorMap: Record<string, string> = {
    openai: "#10a37f",
    gemini: "#4285f4",
    anthropic: "#d97706",
  };
  const lowerType = channelType.toLowerCase();
  return channelColorMap[lowerType] ?? "#6b7280";
}

function getLastSelectedGroupId(): string | null {
  if (typeof localStorage === "undefined") {
    return null;
  }
  try {
    return localStorage.getItem(LAST_SELECTED_GROUP_ID_KEY);
  } catch (error) {
    console.error("Failed to read last selected group id from storage:", error);
    return null;
  }
}

function setLastSelectedGroupId(groupId?: number | null) {
  if (typeof localStorage === "undefined") {
    return;
  }
  try {
    if (groupId) {
      localStorage.setItem(LAST_SELECTED_GROUP_ID_KEY, String(groupId));
    } else {
      localStorage.removeItem(LAST_SELECTED_GROUP_ID_KEY);
    }
  } catch (error) {
    console.error("Failed to write last selected group id to storage:", error);
  }
}

onMounted(async () => {
  await loadGroups();
});

async function loadGroups() {
  try {
    loading.value = true;
    groups.value = await keysApi.getGroups();
    // Select default group
    if (groups.value.length > 0 && !selectedGroup.value) {
      const queryGroupId = Array.isArray(route.query.groupId)
        ? route.query.groupId[0]
        : route.query.groupId;
      const groupId = queryGroupId ?? getLastSelectedGroupId();
      const found = groups.value.find(g => String(g.id) === String(groupId));
      if (found) {
        handleGroupSelect(found);
      } else {
        handleGroupSelect(groups.value[0] ?? null);
      }
    }
  } catch (error) {
    console.error("Failed to load groups:", error);
    window.$message?.error("加载分组列表失败");
  } finally {
    loading.value = false;
  }
}

async function loadSubGroups() {
  if (!selectedGroup.value?.id || selectedGroup.value.group_type !== "aggregate") {
    subGroups.value = [];
    return;
  }

  try {
    loadingSubGroups.value = true;
    subGroups.value = await keysApi.getSubGroups(selectedGroup.value.id);
  } catch (error) {
    console.error("Failed to load sub groups:", error);
    window.$message?.error("加载子分组失败");
    subGroups.value = [];
  } finally {
    loadingSubGroups.value = false;
  }
}

// Watch for selected group changes, load subgroup data
watch(selectedGroup, async newGroup => {
  if (newGroup?.group_type === "aggregate") {
    await loadSubGroups();
  } else {
    subGroups.value = [];
  }
});

// Watch for task completion to refresh group list
// This handles async group deletion completion
watch(
  () => appState.groupDataRefreshTrigger,
  async () => {
    // Refresh group list when a task completes
    await loadGroups();
    // Also refresh sub-groups if current selection is an aggregate group
    if (selectedGroup.value?.group_type === "aggregate") {
      await loadSubGroups();
    }
  }
);

function handleGroupSelect(group: Group | null) {
  selectedGroup.value = group || null;
  setLastSelectedGroupId(group?.id ?? null);
  if (String(group?.id) !== String(route.query.groupId)) {
    router.push({ name: "keys", query: group?.id ? { groupId: group.id } : {} });
  }
}

async function refreshGroupsAndSelect(targetGroupId?: number, selectFirst = true) {
  await loadGroups();

  if (groups.value.length === 0) {
    handleGroupSelect(null);
    return;
  }

  if (targetGroupId) {
    const targetGroup = groups.value.find(g => g.id === targetGroupId);
    if (targetGroup) {
      handleGroupSelect(targetGroup);
      return;
    }
  }

  if (selectedGroup.value) {
    const currentGroup = groups.value.find(g => g.id === selectedGroup.value?.id);
    if (currentGroup) {
      handleGroupSelect(currentGroup);
      if (currentGroup.group_type === "aggregate") {
        await loadSubGroups();
      }
      return;
    }
  }

  if (selectFirst && groups.value.length > 0) {
    handleGroupSelect(groups.value[0] ?? null);
  } else if (!selectedGroup.value && groups.value.length > 0) {
    // Defensive fallback: if nothing is selected (e.g., target parent was also deleted),
    // select the first group to avoid empty state
    handleGroupSelect(groups.value[0] ?? null);
  }
}

function getGroupRemovalSet(deletedGroup: Group): Set<number> {
  const removed = new Set<number>();
  if (typeof deletedGroup.id === "number") {
    removed.add(deletedGroup.id);
  }

  // Deleting a parent standard group also deletes its child groups on the backend.
  if (
    typeof deletedGroup.id === "number" &&
    deletedGroup.group_type !== "aggregate" &&
    (deletedGroup.parent_group_id === null || deletedGroup.parent_group_id === undefined)
  ) {
    for (const group of groups.value) {
      if (group.parent_group_id === deletedGroup.id && typeof group.id === "number") {
        removed.add(group.id);
      }
    }
  }

  return removed;
}

async function handleGroupDeleted(
  deletedGroup: Group,
  parentGroupId?: number,
  isAsyncDeletion?: boolean
) {
  const deletedId = deletedGroup.id;
  let nextGroupId: number | undefined;

  // Calculate removal set once and reuse
  const removedIds = getGroupRemovalSet(deletedGroup);

  if (typeof deletedId === "number") {
    const orderedIds = getSidebarOrderedGroupIds(groups.value);
    nextGroupId = getNearestGroupIdAfterDeletion(orderedIds, deletedId, removedIds);
  }

  // Fallback for cases where the deleted item is not present in the current ordered list.
  if (nextGroupId === undefined && typeof parentGroupId === "number") {
    nextGroupId = parentGroupId;
  }

  // Optimistically remove the deleted group from the local list immediately
  // This provides instant UI feedback, especially for async deletions
  groups.value = groups.value.filter(g => g.id !== undefined && !removedIds.has(g.id));

  // Select the next group
  if (nextGroupId !== undefined) {
    const targetGroup = groups.value.find(g => g.id === nextGroupId);
    if (targetGroup) {
      handleGroupSelect(targetGroup);
    } else if (groups.value.length > 0) {
      handleGroupSelect(groups.value[0] ?? null);
    } else {
      handleGroupSelect(null);
    }
  } else if (groups.value.length > 0) {
    handleGroupSelect(groups.value[0] ?? null);
  } else {
    handleGroupSelect(null);
  }

  // For async deletions, don't refresh immediately as the backend task is still running
  // The group list will be refreshed when the task completes or when user manually refreshes
  // For sync deletions, refresh from backend to ensure consistency
  if (!isAsyncDeletion) {
    await loadGroups();
  }
}

// Handle subgroup selection, navigate to corresponding group
function handleSubGroupSelect(groupId: number) {
  const targetGroup = groups.value.find(g => g.id === groupId);
  if (targetGroup) {
    handleGroupSelect(targetGroup);
  }
}

// Handle aggregate group navigation, navigate to corresponding aggregate group
function handleNavigateToGroup(groupId: number) {
  const targetGroup = groups.value.find(g => g.id === groupId);
  if (targetGroup) {
    handleGroupSelect(targetGroup);
  }
}

// Handle site navigation, navigate to site management page
function handleNavigateToSite(_siteId: number) {
  router.push({ name: "more", query: { tab: "site" } });
}

// Handle group refresh from GroupInfoCard (e.g., after binding/unbinding)
function handleGroupRefresh(updatedGroup?: Group) {
  if (updatedGroup && selectedGroup.value?.id === updatedGroup.id) {
    // Check if enabled status changed - need full refresh to update child groups
    const enabledChanged = selectedGroup.value?.enabled !== updatedGroup.enabled;

    // Check if name changed - need to refresh child groups as their upstream URLs depend on parent name
    const nameChanged = selectedGroup.value?.name !== updatedGroup.name;

    // Check if this is a child group - need to refresh sidebar childGroupsMap
    // The childGroupsMap in GroupList.vue is loaded separately from backend,
    // so we need to explicitly refresh it to sync the sidebar display.
    const isChildGroup =
      updatedGroup.parent_group_id !== null && updatedGroup.parent_group_id !== undefined;

    // Update selected group with new data from child component
    selectedGroup.value = updatedGroup;
    // Also update the group in the list to keep sidebar in sync
    const index = groups.value.findIndex(g => g.id === updatedGroup.id);
    if (index !== -1) {
      groups.value[index] = updatedGroup;
    }

    // If enabled status changed or name changed, reload all groups to sync child groups
    if (enabledChanged || nameChanged) {
      refreshGroupsAndSelect(updatedGroup.id);
    } else if (isChildGroup) {
      // For child group updates, directly call loadAllChildGroups to refresh sidebar
      // This is more reliable than depending on watch to detect props changes
      groupListRef.value?.loadAllChildGroups();
    }
  } else {
    refreshGroupsAndSelect();
  }
}

// Handle quick navigation
function handleQuickNavigate(sectionKey: string, channelType: string) {
  activeChannelType.value = channelType;

  // For aggregate groups, scroll to the section header
  if (channelType === "aggregate") {
    groupListRef.value?.scrollToSection("aggregate");
  } else {
    groupListRef.value?.scrollToChannelType(sectionKey, channelType);
  }

  // Clear existing timer to prevent race conditions
  if (activeChannelResetTimer.value) {
    clearTimeout(activeChannelResetTimer.value);
  }

  // Reset active state after a delay to allow user to see which section they navigated to
  activeChannelResetTimer.value = window.setTimeout(() => {
    activeChannelType.value = undefined;
    activeChannelResetTimer.value = undefined;
  }, 2000);
}

// Clean up timer on component unmount
onBeforeUnmount(() => {
  if (activeChannelResetTimer.value) {
    clearTimeout(activeChannelResetTimer.value);
  }
});
</script>

<template>
  <div>
    <!-- Encryption configuration error warning -->
    <encryption-mismatch-alert style="margin-bottom: 16px" />

    <div class="keys-container">
      <div class="sidebar-wrapper">
        <!-- Group list with quick navigation overlay -->
        <div class="sidebar">
          <quick-navigation
            v-if="visibleChannelTypes.length > 0"
            :channel-types="visibleChannelTypes"
            :active-channel-type="activeChannelType"
            @navigate="handleQuickNavigate"
          />
          <group-list
            ref="groupListRef"
            :groups="groups"
            :selected-group="selectedGroup"
            :loading="loading"
            @group-select="handleGroupSelect"
            @refresh="() => refreshGroupsAndSelect()"
            @refresh-and-select="id => refreshGroupsAndSelect(id)"
          />
        </div>
      </div>

      <!-- Right side main content area, 80% width -->
      <div class="main-content">
        <!-- Group info card, more compact -->
        <div class="group-info">
          <group-info-card
            :group="selectedGroup"
            :groups="groups"
            :sub-groups="subGroups"
            @refresh="handleGroupRefresh"
            @delete="handleGroupDeleted"
            @copy-success="group => refreshGroupsAndSelect(group.id)"
            @navigate-to-group="handleNavigateToGroup"
            @navigate-to-site="handleNavigateToSite"
          />
        </div>

        <!-- Key table area / Subgroup list area -->
        <div class="key-table-section">
          <!-- Standard group displays key list -->
          <key-table
            v-if="!selectedGroup || selectedGroup.group_type !== 'aggregate'"
            :selected-group="selectedGroup"
          />

          <!-- Aggregate group displays subgroup list -->
          <sub-group-table
            v-else
            :selected-group="selectedGroup"
            :sub-groups="subGroups"
            :groups="groups"
            :loading="loadingSubGroups"
            @refresh="loadSubGroups"
            @group-select="handleSubGroupSelect"
          />
        </div>
      </div>
    </div>
  </div>
</template>

<style scoped>
.keys-container {
  display: flex;
  flex-direction: column;
  gap: 8px;
  width: 100%;
}

.sidebar-wrapper {
  width: 100%;
  flex-shrink: 0;
}

.sidebar {
  position: relative;
  width: 100%;
}

.main-content {
  flex: 1;
  display: flex;
  flex-direction: column;
  gap: 8px;
}

.group-info {
  flex-shrink: 0;
}

.key-table-section {
  flex: 1;
  display: flex;
  flex-direction: column;
  min-height: 0;
}

@media (min-width: 768px) {
  .keys-container {
    flex-direction: row;
    height: calc(100vh - 159px);
  }

  .sidebar-wrapper {
    width: 242px;
    height: 100%;
  }

  .sidebar {
    height: 100%;
  }

  .main-content {
    height: 100%;
    overflow: hidden;
  }
}

/* Medium screen optimization */
@media (min-width: 1280px) {
  .sidebar-wrapper {
    width: 275px;
  }
}

/* Extra large screen further optimization */
@media (min-width: 1600px) {
  .sidebar-wrapper {
    width: 308px;
  }
}
</style>
