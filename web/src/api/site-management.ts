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
  custom_checkin_url: string;

  auth_type: ManagedSiteAuthType;
  has_auth: boolean;

  last_checkin_at?: string;
  last_checkin_date: string;
  last_checkin_status: ManagedSiteCheckinStatus;
  last_checkin_message: string;

  bound_group_id?: number;
  bound_group_name?: string;

  created_at: string;
  updated_at: string;
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

  // Copy a site with unique name
  async copySite(id: number): Promise<ManagedSiteDTO> {
    const res = await http.post(`/site-management/sites/${id}/copy`);
    return res.data;
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

  // Get sites available for binding (sorted by sort order)
  async listSitesForBinding(): Promise<
    { id: number; name: string; sort: number; enabled: boolean; bound_group_id?: number }[]
  > {
    const res = await http.get("/site-management/sites-for-binding");
    return res.data || [];
  },

  // Unbind site from its bound group
  async unbindSiteFromGroup(siteId: number): Promise<void> {
    await http.delete(`/site-management/sites/${siteId}/binding`);
  },

  // Get bound group info for a site
  async getBoundGroupInfo(
    siteId: number
  ): Promise<{ id: number; name: string; display_name: string } | null> {
    const res = await http.get(`/site-management/sites/${siteId}/bound-group`);
    return res.data;
  },

  // Get count of unbound sites
  async getUnboundCount(): Promise<number> {
    const res = await http.get("/site-management/unbound-count");
    return res.data?.count || 0;
  },

  // Delete all unbound sites
  async deleteAllUnboundSites(): Promise<{ count: number }> {
    const res = await http.delete("/site-management/unbound-sites");
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
  custom_checkin_url: string;
  auth_type: ManagedSiteAuthType;
  auth_value?: string;
}

export interface SiteImportData {
  version?: string;
  sites: SiteExportInfo[];
}
