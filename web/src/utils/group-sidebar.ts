import type { Group } from "@/types/models";
import { naturalCompare } from "@/utils/sort";

const DEFAULT_CHANNEL_TYPE = "default";
const KNOWN_CHANNEL_ORDER: string[] = ["openai", "gemini", "anthropic", DEFAULT_CHANNEL_TYPE];

export function normalizeChannelType(channelType?: string | null): string {
  return channelType?.trim() || DEFAULT_CHANNEL_TYPE;
}

export function sortBySortThenName<
  T extends {
    sort?: number | null;
    name?: string | null;
  },
>(a: T, b: T): number {
  const sortA = a.sort ?? 0;
  const sortB = b.sort ?? 0;
  if (sortA !== sortB) {
    return sortA - sortB;
  }
  // Use naturalCompare for consistency with sortChildGroupsByName,
  // handles numeric suffixes intuitively (e.g., "group1" < "group2" < "group10")
  return naturalCompare(a.name ?? "", b.name ?? "");
}

export interface ChannelGroup<T> {
  channelType: string;
  groups: T[];
}

export function groupByChannelType<
  T extends {
    channel_type?: string | null;
    sort?: number | null;
    name?: string | null;
  },
>(groups: T[]): ChannelGroup<T>[] {
  const channelMap = new Map<string, T[]>();

  for (const group of groups) {
    const channelType = normalizeChannelType(group.channel_type);
    const bucket = channelMap.get(channelType);
    if (bucket) {
      bucket.push(group);
    } else {
      channelMap.set(channelType, [group]);
    }
  }

  const result: ChannelGroup<T>[] = [];
  for (const [channelType, channelGroups] of channelMap) {
    channelGroups.sort(sortBySortThenName);
    result.push({ channelType, groups: channelGroups });
  }

  result.sort((a, b) => {
    const aLower = a.channelType.toLowerCase();
    const bLower = b.channelType.toLowerCase();
    const aIndex = KNOWN_CHANNEL_ORDER.indexOf(aLower);
    const bIndex = KNOWN_CHANNEL_ORDER.indexOf(bLower);

    if (aIndex !== -1 && bIndex !== -1) {
      return aIndex - bIndex;
    }
    if (aIndex !== -1) {
      return -1;
    }
    if (bIndex !== -1) {
      return 1;
    }
    return aLower.localeCompare(bLower);
  });

  return result;
}

function sortChildGroupsByName(a: Group, b: Group): number {
  return naturalCompare(a.name ?? "", b.name ?? "");
}

export function getSidebarOrderedGroupIds(groups: Group[]): number[] {
  const childGroupsByParentId = new Map<number, Group[]>();
  for (const group of groups) {
    const groupId = group.id;
    const parentId = group.parent_group_id;
    if (typeof groupId !== "number" || typeof parentId !== "number") {
      continue;
    }
    const bucket = childGroupsByParentId.get(parentId);
    if (bucket) {
      bucket.push(group);
    } else {
      childGroupsByParentId.set(parentId, [group]);
    }
  }

  const aggregateGroups = groups.filter(g => g.group_type === "aggregate");
  const standardParentGroups = groups.filter(
    g =>
      g.group_type !== "aggregate" &&
      (g.parent_group_id === null || g.parent_group_id === undefined)
  );

  aggregateGroups.sort(sortBySortThenName);
  standardParentGroups.sort(sortBySortThenName);

  const orderedIds: number[] = [];

  const appendSection = (sectionGroups: Group[], includeChildren: boolean) => {
    const channelGroups = groupByChannelType(sectionGroups);
    for (const channelGroup of channelGroups) {
      for (const group of channelGroup.groups) {
        if (typeof group.id !== "number") {
          continue;
        }
        orderedIds.push(group.id);

        if (!includeChildren) {
          continue;
        }

        const children = childGroupsByParentId.get(group.id);
        if (!children || children.length === 0) {
          continue;
        }

        const sortedChildren = [...children].sort(sortChildGroupsByName);
        for (const child of sortedChildren) {
          if (typeof child.id === "number") {
            orderedIds.push(child.id);
          }
        }
      }
    }
  };

  appendSection(aggregateGroups, false);
  appendSection(standardParentGroups, true);

  return orderedIds;
}

// Best practice after deleting a selected item in a list: move focus to the next item,
// or the previous item if there is no next item.
export function getNearestGroupIdAfterDeletion(
  orderedGroupIds: readonly number[],
  deletedGroupId: number,
  removedGroupIds: ReadonlySet<number> = new Set([deletedGroupId])
): number | undefined {
  const index = orderedGroupIds.indexOf(deletedGroupId);
  if (index === -1) {
    return undefined;
  }

  for (let i = index + 1; i < orderedGroupIds.length; i++) {
    const id = orderedGroupIds[i];
    if (id === undefined) {
      continue;
    }
    if (!removedGroupIds.has(id)) {
      return id;
    }
  }

  for (let i = index - 1; i >= 0; i--) {
    const id = orderedGroupIds[i];
    if (id === undefined) {
      continue;
    }
    if (!removedGroupIds.has(id)) {
      return id;
    }
  }

  return undefined;
}
