/**
 * Search filter utilities for Hub model pool.
 * Implements Property 10: Search Filter Correctness from design.md
 *
 * For any search query on the model pool, the results SHALL contain only entries
 * where the model name or group name contains the search term (case-insensitive).
 */

import type { ModelPoolEntry } from "../types/hub";

/**
 * Filter model pool entries by search keyword.
 * Matches against model name and source group names (case-insensitive).
 *
 * @param models - Array of model pool entries to filter
 * @param keyword - Search keyword (case-insensitive)
 * @returns Filtered array of model pool entries
 */
export function filterModelPool(models: ModelPoolEntry[], keyword: string): ModelPoolEntry[] {
  const trimmedKeyword = keyword.trim().toLowerCase();

  // Return all models if no keyword
  if (!trimmedKeyword) {
    return models;
  }

  return models.filter(model => {
    // Check model name
    if (model.model_name.toLowerCase().includes(trimmedKeyword)) {
      return true;
    }

    // Check source group names across all channel types
    for (const sources of Object.values(model.sources_by_type)) {
      if (sources.some(source => source.group_name.toLowerCase().includes(trimmedKeyword))) {
        return true;
      }
    }

    return false;
  });
}

/**
 * Check if a model entry matches the search keyword.
 * Used for individual entry validation.
 *
 * @param model - Model pool entry to check
 * @param keyword - Search keyword (case-insensitive)
 * @returns True if the model matches the keyword
 */
export function modelMatchesKeyword(model: ModelPoolEntry, keyword: string): boolean {
  const trimmedKeyword = keyword.trim().toLowerCase();

  // Empty keyword matches everything
  if (!trimmedKeyword) {
    return true;
  }

  // Check model name
  if (model.model_name.toLowerCase().includes(trimmedKeyword)) {
    return true;
  }

  // Check source group names across all channel types
  for (const sources of Object.values(model.sources_by_type)) {
    if (sources.some(source => source.group_name.toLowerCase().includes(trimmedKeyword))) {
      return true;
    }
  }

  return false;
}
