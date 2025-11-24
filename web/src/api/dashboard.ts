import type { ChartData, DashboardStatsResponse, Group } from "@/types/models";
import http from "@/utils/http";

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
export const getDashboardChart = (groupId?: number) => {
  return http.get<ChartData>("/dashboard/chart", {
    params: groupId ? { groupId } : {},
  });
};

/**
 * Get group list for filters
 */
export const getGroupList = () => {
  return http.get<Group[]>("/groups/list");
};
