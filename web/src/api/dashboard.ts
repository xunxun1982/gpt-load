import type {
  ChartData,
  DashboardStatsResponse,
  DashboardTokenUsageResponse,
  GroupListItem,
} from "@/types/models";
import http from "@/utils/http";

export type DashboardChartRange =
  | "last_24_hours"
  | "last_7_days"
  | "last_30_days"
  | "today"
  | "yesterday"
  | "this_week"
  | "last_week"
  | "this_month"
  | "last_month";

/**
 * Get basic dashboard statistics data
 */
export const getDashboardStats = () => {
  return http.get<DashboardStatsResponse>("/dashboard/stats");
};

/**
 * Get dashboard chart data
 * @param groupId Optional group ID
 */
export const getDashboardChart = (groupId?: number, range?: DashboardChartRange) => {
  return http.get<ChartData>("/dashboard/chart", {
    params: {
      ...(groupId !== undefined ? { groupId } : {}),
      ...(range ? { range } : {}),
    },
  });
};

export const getDashboardTokenUsage = (
  groupId?: number,
  range?: DashboardChartRange,
  limit?: number,
  model?: string
) => {
  return http.get<DashboardTokenUsageResponse>("/dashboard/token-usage", {
    params: {
      ...(groupId !== undefined ? { groupId } : {}),
      ...(range ? { range } : {}),
      ...(limit !== undefined ? { limit } : {}),
      ...(model ? { model } : {}),
    },
  });
};

/**
 * Get group list for filters
 */
export const getGroupList = () => {
  return http.get<GroupListItem[]>("/groups/list");
};
