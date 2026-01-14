// Common API response structure
export interface ApiResponse<T> {
  code: number;
  message: string;
  data: T;
}

// Key status
export type KeyStatus = "active" | "invalid" | undefined;

// Group type
export type GroupType = "standard" | "aggregate";

// Channel type
export type ChannelType = "openai" | "gemini" | "anthropic" | "codex";

// Data model definitions
export interface APIKey {
  id: number;
  group_id: number;
  key_value: string;
  notes?: string;
  status: KeyStatus;
  request_count: number;
  failure_count: number;
  last_used_at?: string;
  created_at: string;
  updated_at: string;
}

export interface UpstreamInfo {
  url: string;
  weight: number;
  proxy_url?: string;
}

export interface HeaderRule {
  key: string;
  value: string;
  action: "set" | "remove";
}

export interface PathRedirectRule {
  from: string;
  to: string;
}

// V2 Model Redirect Types (one-to-many mapping with weighted selection)
export interface ModelRedirectTarget {
  model: string;
  weight?: number; // Default 100, 0 means disabled
  enabled?: boolean; // Default true
}

export interface ModelRedirectRuleV2 {
  targets: ModelRedirectTarget[];
  fallback?: string[]; // P1 extension
}

// V2 rules map: source model -> rule
export type ModelRedirectRulesV2 = Record<string, ModelRedirectRuleV2>;

// Sub-group configuration (used when creating/updating)
export interface SubGroupConfig {
  group_id: number;
  weight: number;
}

// Sub-group information (used for display)
export interface SubGroupInfo {
  group: Group;
  weight: number;
  total_keys: number;
  active_keys: number;
  invalid_keys: number;
}

// Parent aggregate group information (used for display)
export interface ParentAggregateGroup {
  group_id: number;
  name: string;
  display_name: string;
  weight: number;
}

export interface Group {
  id?: number;
  name: string;
  display_name: string;
  description: string;
  sort: number;
  test_model: string;
  channel_type: ChannelType;
  enabled: boolean;
  upstreams: UpstreamInfo[];
  validation_endpoint: string;
  config: Record<string, unknown>;
  api_keys?: APIKey[];
  endpoint?: string;
  param_overrides: Record<string, unknown>;
  model_mapping?: string; // Deprecated: for backward compatibility
  model_redirect_rules: Record<string, string>; // V1: one-to-one mapping
  model_redirect_rules_v2?: ModelRedirectRulesV2; // V2: one-to-many mapping
  model_redirect_strict: boolean;
  header_rules?: HeaderRule[];
  path_redirects?: PathRedirectRule[];
  proxy_keys: string;
  group_type?: GroupType;
  parent_group_id?: number | null; // Parent group ID for child groups
  bound_site_id?: number | null; // Bound site ID for standard groups
  sub_groups?: SubGroupInfo[]; // List of sub-groups (aggregate groups only)
  sub_group_ids?: number[]; // List of sub-group IDs
  created_at?: string;
  updated_at?: string;
}

// Lightweight group shape used by list endpoints for dropdowns/filters.
export type GroupListItem = Pick<
  Group,
  "id" | "name" | "display_name" | "sort" | "group_type" | "parent_group_id"
>;

// Child group information (used for display)
export interface ChildGroupInfo {
  id: number;
  name: string;
  display_name: string;
  enabled: boolean;
  created_at: string;
}

export interface GroupConfigOption {
  key: string;
  name: string;
  description: string;
  default_value: string | number;
}

// GroupStatsResponse defines the complete statistics for a group.
export interface GroupStatsResponse {
  key_stats: KeyStats;
  stats_24_hour: RequestStats;
  stats_7_day: RequestStats;
  stats_30_day: RequestStats;
}

// KeyStats defines the statistics for API keys in a group.
export interface KeyStats {
  total_keys: number;
  active_keys: number;
  invalid_keys: number;
}

// RequestStats defines the statistics for requests over a period.
export interface RequestStats {
  total_requests: number;
  failed_requests: number;
  failure_rate: number;
}

export type TaskType = "KEY_VALIDATION" | "KEY_IMPORT" | "KEY_DELETE";

export interface KeyValidationResult {
  invalid_keys: number;
  total_keys: number;
  valid_keys: number;
}

export interface KeyImportResult {
  added_count: number;
  ignored_count: number;
}

export interface KeyDeleteResult {
  deleted_count: number;
  ignored_count: number;
}

export interface TaskInfo {
  task_type: TaskType;
  is_running: boolean;
  group_name?: string;
  processed?: number;
  total?: number;
  started_at?: string;
  finished_at?: string;
  result?: KeyValidationResult | KeyImportResult | KeyDeleteResult;
  error?: string;
}

// Based on backend response
export interface RequestLog {
  id: string;
  timestamp: string;
  group_id: number;
  key_id: number;
  is_success: boolean;
  source_ip: string;
  status_code: number;
  request_path: string;
  duration_ms: number;
  error_message: string;
  user_agent: string;
  request_type: "retry" | "final";
  group_name?: string;
  parent_group_name?: string;
  key_value?: string;
  model: string;
  mapped_model?: string;
  upstream_addr: string;
  proxy_url?: string;
  is_stream: boolean;
  request_body?: string;
  response_body?: string;
}

export interface Pagination {
  page: number;
  page_size: number;
  total_items: number;
  total_pages: number;
}

export interface LogsResponse {
  items: RequestLog[];
  pagination: Pagination;
}

export interface LogFilter {
  page?: number;
  page_size?: number;
  group_name?: string;
  parent_group_name?: string;
  key_value?: string;
  model?: string;
  is_success?: boolean | null;
  status_code?: number | null;
  source_ip?: string;
  error_contains?: string;
  start_time?: string | null;
  end_time?: string | null;
  request_type?: "retry" | "final";
}

export interface DashboardStats {
  total_requests: number;
  success_requests: number;
  success_rate: number;
  group_stats: GroupRequestStat[];
}

export interface GroupRequestStat {
  display_name: string;
  request_count: number;
}

// Dashboard statistics card data
export interface StatCard {
  value: number;
  sub_value?: number;
  sub_value_tip?: string;
  trend: number;
  trend_is_growth: boolean;
}

// Security warning information
export type SecuritySeverity = "low" | "medium" | "high";

export interface SecurityWarning {
  type: string; // Warning type: auth_key, encryption_key, etc.
  message: string; // Warning message
  severity: SecuritySeverity; // Severity level: low, medium, high
  suggestion: string; // Suggested resolution
}

// Dashboard base statistics response
export interface DashboardStatsResponse {
  key_count: StatCard;
  rpm: StatCard;
  request_count: StatCard;
  error_rate: StatCard;
  security_warnings: SecurityWarning[];
}

// Chart dataset definition
export interface ChartDataset {
  label: string;
  data: number[];
  color: string;
}

// Chart data for dashboard charts
export interface ChartData {
  labels: string[];
  datasets: ChartDataset[];
}
