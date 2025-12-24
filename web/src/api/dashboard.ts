import type { ChartData, DashboardStatsResponse, GroupListItem } from "@/types/models";
import http from "@/utils/http";

export type DashboardChartRange =
  | "today"
  | "yesterday"
  | "this_week"
  | "last_week"
  | "this_month"
  | "last_month"
  | "last_30_days";

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

/**
 * Get group list for filters
 */
export const getGroupList = () => {
  return http.get<GroupListItem[]>("/groups/list");
};
