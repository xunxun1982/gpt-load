import http from "@/utils/http";

// Note: "Veloera" is capitalized to match backend SiteTypeVeloera constant
export type ManagedSiteType =
  | "unknown"
  | "new-api"
  | "Veloera"
  | "wong-gongyi"
  | "one-hub"
  | "done-hub";
export type ManagedSiteAuthType = "none" | "access_token";

export type ManagedSiteCheckinStatus = "success" | "failed" | "skipped" | "already_checked" | "";

export interface ManagedSiteDTO {
  id: number;
  name: string;
  notes: string;
  description: string;
  sort: number;
  enabled: boolean;

  base_url: string;
  site_type: ManagedSiteType;
  user_id: string;
  checkin_page_url: string;

  checkin_available: boolean;
  checkin_enabled: boolean;
  auto_checkin_enabled: boolean;
  custom_checkin_url: string;

  auth_type: ManagedSiteAuthType;
  has_auth: boolean;

  last_checkin_at?: string;
  last_checkin_date: string;
  last_checkin_status: ManagedSiteCheckinStatus;
  last_checkin_message: string;

  created_at: string;
  updated_at: string;
}

export interface AutoCheckinRetryStrategy {
  enabled: boolean;
  interval_minutes: number;
  max_attempts_per_day: number;
}

export type AutoCheckinScheduleMode = "random" | "deterministic";

export interface AutoCheckinConfig {
  global_enabled: boolean;
  window_start: string;
  window_end: string;
  schedule_mode: AutoCheckinScheduleMode;
  deterministic_time?: string;
  retry_strategy: AutoCheckinRetryStrategy;
}

export interface AutoCheckinAttemptsTracker {
  date: string;
  attempts: number;
}

export interface AutoCheckinRunSummary {
  total_eligible: number;
  executed: number;
  success_count: number;
  failed_count: number;
  skipped_count: number;
  needs_retry: boolean;
}

export type AutoCheckinRunResult = "success" | "partial" | "failed" | "";

export interface AutoCheckinStatus {
  is_running: boolean;
  last_run_at?: string;
  last_run_result?: AutoCheckinRunResult;
  next_scheduled_at?: string;
  summary?: AutoCheckinRunSummary;
  attempts?: AutoCheckinAttemptsTracker;
  pending_retry: boolean;
}

export interface CheckinResult {
  site_id: number;
  status: ManagedSiteCheckinStatus;
  message: string;
}

export interface CheckinLogDTO {
  id: number;
  site_id: number;
  status: ManagedSiteCheckinStatus;
  message: string;
  created_at: string;
}

export interface CreateManagedSiteRequest {
  name: string;
  notes: string;
  description: string;
  sort: number;
  enabled: boolean;

  base_url: string;
  site_type: ManagedSiteType;
  user_id: string;
  checkin_page_url: string;

  checkin_available: boolean;
  checkin_enabled: boolean;
  auto_checkin_enabled: boolean;
  custom_checkin_url: string;

  auth_type: ManagedSiteAuthType;
  auth_value: string;
}

export type UpdateManagedSiteRequest = Partial<CreateManagedSiteRequest>;

export const siteManagementApi = {
  async listSites(): Promise<ManagedSiteDTO[]> {
    const res = await http.get("/site-management/sites");
    return res.data || [];
  },

  async createSite(payload: CreateManagedSiteRequest): Promise<ManagedSiteDTO> {
    const res = await http.post("/site-management/sites", payload);
    return res.data;
  },

  async updateSite(id: number, payload: UpdateManagedSiteRequest): Promise<ManagedSiteDTO> {
    const res = await http.put(`/site-management/sites/${id}`, payload);
    return res.data;
  },

  deleteSite(id: number): Promise<void> {
    return http.delete(`/site-management/sites/${id}`);
  },

  async getAutoCheckinConfig(): Promise<AutoCheckinConfig> {
    const res = await http.get("/site-management/auto-checkin/config", { hideMessage: true });
    return res.data;
  },

  async updateAutoCheckinConfig(payload: AutoCheckinConfig): Promise<AutoCheckinConfig> {
    const res = await http.put("/site-management/auto-checkin/config", payload);
    return res.data;
  },

  async getAutoCheckinStatus(): Promise<AutoCheckinStatus> {
    const res = await http.get("/site-management/auto-checkin/status", { hideMessage: true });
    return res.data;
  },

  runAutoCheckinNow(): Promise<void> {
    return http.post("/site-management/auto-checkin/run-now", {}, { hideMessage: true });
  },

  async checkinSite(id: number): Promise<CheckinResult> {
    const res = await http.post(`/site-management/sites/${id}/checkin`, {}, { hideMessage: true });
    return res.data;
  },

  async listCheckinLogs(id: number, limit = 50): Promise<CheckinLogDTO[]> {
    const res = await http.get(`/site-management/sites/${id}/checkin-logs`, {
      params: { limit },
      hideMessage: true,
    });
    return res.data || [];
  },

  // Export sites
  // Note: The double cast (as unknown as Blob) is necessary because the http interceptor
  // returns response.data directly, but TypeScript infers the return type as AxiosResponse.
  // Fixing this properly would require modifying the http utility's type definitions.
  async exportSites(
    mode: "plain" | "encrypted" = "encrypted",
    includeConfig = true
  ): Promise<Blob> {
    const res = await http.get("/site-management/export", {
      params: { mode, include_config: includeConfig },
      responseType: "blob",
    });
    return res as unknown as Blob;
  },

  // Import sites
  async importSites(
    data: SiteImportData,
    mode?: "plain" | "encrypted"
  ): Promise<{ imported: number; skipped: number; total: number }> {
    const params: Record<string, string> = {};
    if (mode) {
      params.mode = mode;
    }
    const res = await http.post("/site-management/import", data, { params });
    return res.data;
  },
};

// Export/Import types
export interface SiteExportInfo {
  name: string;
  notes: string;
  description: string;
  sort: number;
  enabled: boolean;
  base_url: string;
  site_type: ManagedSiteType;
  user_id: string;
  checkin_page_url: string;
  checkin_available: boolean;
  checkin_enabled: boolean;
  auto_checkin_enabled: boolean;
  custom_checkin_url: string;
  auth_type: ManagedSiteAuthType;
  auth_value?: string;
}

export interface SiteImportData {
  version?: string;
  auto_checkin?: AutoCheckinConfig;
  sites: SiteExportInfo[];
}
