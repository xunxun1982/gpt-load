import type { DashboardChartRange } from "@/api/dashboard";

export const DEFAULT_DASHBOARD_RANGE: DashboardChartRange = "last_24_hours";

export const DASHBOARD_TIME_RANGES: Array<{
  value: DashboardChartRange;
  labelKey: string;
}> = [
  { value: "last_24_hours", labelKey: "charts.rangeLast24Hours" },
  { value: "last_7_days", labelKey: "charts.rangeLast7Days" },
  { value: "last_30_days", labelKey: "charts.rangeLast1Month" },
  { value: "today", labelKey: "charts.rangeToday" },
  { value: "yesterday", labelKey: "charts.rangeYesterday" },
  { value: "this_week", labelKey: "charts.rangeThisWeek" },
  { value: "last_week", labelKey: "charts.rangeLastWeek" },
  { value: "this_month", labelKey: "charts.rangeThisMonth" },
  { value: "last_month", labelKey: "charts.rangeLastMonth" },
];
