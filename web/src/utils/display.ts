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

interface BalanceText {
  numericText: string;
  negative: boolean;
}

function extractBalanceText(balance: string | null | undefined): BalanceText | null {
  const value = balance?.trim();
  if (!value) {
    return null;
  }

  const match = value.match(/\d[\d.,]*/);
  if (!match) {
    return null;
  }

  const numericText = match[0].replace(/[.,]+$/, "");
  if (!numericText) {
    return null;
  }

  const prefix = value.slice(0, match.index ?? 0);
  return { numericText, negative: prefix.includes("-") };
}

export function formatBalanceValue(balance: string | null | undefined): string {
  const extracted = extractBalanceText(balance);
  if (!extracted) {
    return "-";
  }
  return extracted.negative ? `-${extracted.numericText}` : extracted.numericText;
}

export function formatSubGroupBalanceValue(
  subGroup: SubGroupInfo,
  groupsById: ReadonlyMap<number, Group>,
  siteBalances: Readonly<Record<number, string | null | undefined>>
): string {
  const canonicalGroup =
    typeof subGroup.group.id === "number" ? groupsById.get(subGroup.group.id) : undefined;
  const ownSiteId = subGroup.group.bound_site_id ?? canonicalGroup?.bound_site_id;
  const parentGroupId = subGroup.group.parent_group_id ?? canonicalGroup?.parent_group_id;
  const parentSiteId =
    parentGroupId === null || parentGroupId === undefined
      ? undefined
      : groupsById.get(parentGroupId)?.bound_site_id;
  const siteId = ownSiteId ?? parentSiteId;

  return formatBalanceValue(siteId === null || siteId === undefined ? null : siteBalances[siteId]);
}

export function parseBalanceValue(balance: string | null | undefined): number | null {
  const extracted = extractBalanceText(balance);
  if (!extracted) {
    return null;
  }

  const { numericText } = extracted;
  const lastComma = numericText.lastIndexOf(",");
  const lastDot = numericText.lastIndexOf(".");
  let normalized = numericText;

  if (lastComma >= 0 && lastDot >= 0) {
    const decimalSeparator = lastComma > lastDot ? "," : ".";
    const groupingSeparator = decimalSeparator === "," ? "." : ",";
    normalized = numericText.split(groupingSeparator).join("").replace(decimalSeparator, ".");
  } else {
    const separator = lastComma >= 0 ? "," : lastDot >= 0 ? "." : "";
    if (separator) {
      const parts = numericText.split(separator);
      const grouped =
        parts.length > 2 || (parts.length === 2 && parts[0] !== "0" && parts[1]?.length === 3);
      normalized = grouped ? parts.join("") : `${parts[0]}.${parts[1] ?? ""}`;
    }
  }

  const value = Number(normalized);
  if (!Number.isFinite(value)) {
    return null;
  }
  return extracted.negative ? -value : value;
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

export function formatTokenCount(value: number): string {
  const roundedValue = Math.round(value);
  const units = [
    { threshold: 1_000, suffix: "K" },
    { threshold: 1_000_000, suffix: "M" },
    { threshold: 1_000_000_000, suffix: "B" },
    { threshold: 1_000_000_000_000, suffix: "T" },
  ];

  let unitIndex = -1;
  for (let i = 0; i < units.length; i += 1) {
    const unit = units[i];
    if (unit && Math.abs(roundedValue) >= unit.threshold) {
      unitIndex = i;
    }
  }

  if (unitIndex === -1) {
    return roundedValue.toString();
  }

  let currentUnit = units[unitIndex];
  if (!currentUnit) {
    return roundedValue.toString();
  }

  let scaled = roundedValue / currentUnit.threshold;
  let fractionDigits = Math.abs(scaled) >= 100 ? 0 : 1;
  while (unitIndex < units.length - 1 && Number(scaled.toFixed(fractionDigits)) >= 1000) {
    unitIndex += 1;
    const nextUnit = units[unitIndex];
    if (!nextUnit) {
      break;
    }
    currentUnit = nextUnit;
    scaled = roundedValue / currentUnit.threshold;
    fractionDigits = Math.abs(scaled) >= 100 ? 0 : 1;
  }
  return `${scaled.toFixed(fractionDigits)}${currentUnit.suffix}`;
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

/**
 * Formats an effective weight value with 1 decimal place.
 * Used for displaying dynamic effective weights in the UI.
 *
 * @param weight The effective weight value.
 * @returns The formatted weight string with 1 decimal place.
 *
 * @example
 * formatEffectiveWeight(1.5)  // "1.5"
 * formatEffectiveWeight(1)    // "1.0"
 * formatEffectiveWeight(10)   // "10.0"
 */
export function formatEffectiveWeight(weight: number): string {
  return weight.toFixed(1);
}
