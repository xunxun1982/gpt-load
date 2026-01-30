import type { Group, GroupListItem, SubGroupInfo } from "@/types/models";

/**
 * Formats a string from camelCase, snake_case, or kebab-case
 * into a more readable format with spaces and capitalized words.
 *
 * @param name The input string.
 * @returns The formatted string.
 *
 * @example
 * formatDisplayName("myGroupName")      // "My Group Name"
 * formatDisplayName("my_group_name")    // "My Group Name"
 * formatDisplayName("my-group-name")    // "My Group Name"
 * formatDisplayName("MyGroup")          // "My Group"
 */
export function formatDisplayName(name: string): string {
  if (!name) {
    return "";
  }

  // Replace snake_case and kebab-case with spaces, and add a space before uppercase letters in camelCase.
  const result = name.replace(/[_-]/g, " ").replace(/([a-z])([A-Z])/g, "$1 $2");

  // Capitalize the first letter of each word.
  return result
    .split(" ")
    .filter(word => word.length > 0)
    .map(word => word.charAt(0).toUpperCase() + word.slice(1))
    .join(" ");
}

/**
 * Gets the display name for a group or subgroup, falling back to a formatted version of its name.
 * @param item The group or subgroup object.
 * @returns The display name for the group.
 */
export function getGroupDisplayName(item: Group | GroupListItem | SubGroupInfo): string {
  // Check if it's a SubGroupInfo with a valid group property
  if ("group" in item && item.group && typeof item.group === "object") {
    const group = item.group as Group;
    return group.display_name || formatDisplayName(group.name);
  }

  // Otherwise treat it as a Group-like object
  const group = item as GroupListItem;
  return group.display_name || formatDisplayName(group.name);
}

/**
 * Masks a long key string for display.
 * @param key The key string.
 * @returns The masked key.
 */
export function maskKey(key: string): string {
  if (!key || key.length <= 8) {
    return key || "";
  }
  return `${key.substring(0, 4)}...${key.substring(key.length - 4)}`;
}

/**
 * Masks a comma-separated string of keys.
 * @param keys The comma-separated keys string.
 * @returns The masked keys string.
 */
export function maskProxyKeys(keys: string): string {
  if (!keys) {
    return "";
  }
  return keys
    .split(",")
    .map(key => maskKey(key.trim()))
    .join(", ");
}

/**
 * Formats a percentage value with dynamic precision.
 * Uses 1 decimal place for values >= 1%, and 2 decimal places for values < 1%.
 * This ensures small percentages are visible while keeping larger values concise.
 *
 * @param value The percentage value (0-100).
 * @returns The formatted percentage string with % suffix.
 *
 * @example
 * formatPercentage(5.3)    // "5.3%"
 * formatPercentage(0.15)   // "0.15%"
 * formatPercentage(0.05)   // "0.05%"
 * formatPercentage(100)    // "100.0%"
 */
export function formatPercentage(value: number): string {
  if (value >= 1) {
    return `${value.toFixed(1)}%`;
  } else {
    return `${value.toFixed(2)}%`;
  }
}

/**
 * Formats a health score (0-1 range) as a percentage with dynamic precision.
 * Converts the 0-1 range to 0-100% and applies dynamic precision formatting.
 *
 * @param score The health score value (0-1).
 * @returns The formatted percentage string with % suffix.
 *
 * @example
 * formatHealthScore(1.0)    // "100.0%"
 * formatHealthScore(0.053)  // "5.3%"
 * formatHealthScore(0.0015) // "0.15%"
 */
export function formatHealthScore(score: number): string {
  return formatPercentage(score * 100);
}
