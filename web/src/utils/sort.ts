/**
 * Compare strings using natural sort order (e.g. child2 < child10).
 */
export function naturalCompare(strA: string, strB: string): number {
  const regex = /(\d+)/g;
  const splitA = strA.split(regex).filter(Boolean);
  const splitB = strB.split(regex).filter(Boolean);

  for (let i = 0; i < Math.max(splitA.length, splitB.length); i++) {
    const partA = splitA[i] ?? "";
    const partB = splitB[i] ?? "";

    const numA = parseInt(partA, 10);
    const numB = parseInt(partB, 10);
    const isNumA = !Number.isNaN(numA) && String(numA) === partA;
    const isNumB = !Number.isNaN(numB) && String(numB) === partB;

    if (isNumA && isNumB) {
      if (numA !== numB) {
        return numA - numB;
      }
      continue;
    }

    const cmp = partA.localeCompare(partB);
    if (cmp !== 0) {
      return cmp;
    }
  }

  return 0;
}

/**
 * Sort groups so that a parent group is followed immediately by its child groups.
 *
 * Rules:
 * 1) Parent groups are sorted by `sort` then `name` (natural compare)
 * 2) Within the same parent, the parent group comes before child groups
 * 3) Child groups are sorted by `sort` then `name` (natural compare)
 */
export function sortGroupsWithChildren<
  T extends {
    id?: number;
    name: string;
    sort?: number;
    parent_group_id?: number | null;
  },
>(groups: T[]): T[] {
  const parentById = new Map<number, T>();
  for (const group of groups) {
    if (
      group.id !== undefined &&
      (group.parent_group_id === null || group.parent_group_id === undefined)
    ) {
      parentById.set(group.id, group);
    }
  }

  const getParent = (group: T): T => {
    if (group.parent_group_id === null || group.parent_group_id === undefined) {
      return group;
    }
    const parent = parentById.get(group.parent_group_id);
    return parent ?? group;
  };

  return [...groups].sort((a, b) => {
    const aParent = getParent(a);
    const bParent = getParent(b);

    const aParentId = aParent.id;
    const bParentId = bParent.id;
    if (aParentId !== bParentId) {
      const aParentSort = aParent.sort ?? 0;
      const bParentSort = bParent.sort ?? 0;
      if (aParentSort !== bParentSort) {
        return aParentSort - bParentSort;
      }
      return naturalCompare(aParent.name ?? "", bParent.name ?? "");
    }

    const aIsChild = a.parent_group_id !== null && a.parent_group_id !== undefined;
    const bIsChild = b.parent_group_id !== null && b.parent_group_id !== undefined;

    if (!aIsChild && bIsChild) {
      return -1;
    }
    if (aIsChild && !bIsChild) {
      return 1;
    }

    const aSort = a.sort ?? 0;
    const bSort = b.sort ?? 0;
    if (aSort !== bSort) {
      return aSort - bSort;
    }

    return naturalCompare(a.name ?? "", b.name ?? "");
  });
}
