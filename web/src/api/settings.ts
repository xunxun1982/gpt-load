import http from "@/utils/http";

export interface Setting {
  key: string;
  name: string;
  value: string | number | boolean;
  type: "int" | "string" | "bool";
  min_value?: number;
  description: string;
  required: boolean;
}

export interface SettingCategory {
  category_name: string;
  settings: Setting[];
}

export type SettingsUpdatePayload = Record<string, string | number | boolean>;

export const settingsApi = {
  async getSettings(): Promise<SettingCategory[]> {
    const response = await http.get("/settings");
    return response.data || [];
  },
  updateSettings(data: SettingsUpdatePayload): Promise<void> {
    return http.put("/settings", data);
  },
  async getChannelTypes(): Promise<string[]> {
    const response = await http.get("/channel-types");
    return response.data || [];
  },

  // ============ System Import/Export ============

  // Export full system configuration
  async exportAll(): Promise<void> {
    try {
      const response = await http.get("/system/export", { hideMessage: true });

      // Response interceptor already returns response.data, use it directly
      const jsonStr = JSON.stringify(response, null, 2);

      // Create Blob object
      const blob = new Blob([jsonStr], { type: "application/json;charset=utf-8" });

      // Generate filename
      const timestamp = new Date().toISOString().replace(/[:.]/g, "-").slice(0, 19);
      const filename = `system_export_${timestamp}.json`;

      // Create download link
      const url = window.URL.createObjectURL(blob);
      const link = document.createElement("a");
      link.href = url;
      link.download = filename;

      // Trigger download
      document.body.appendChild(link);
      link.click();

      // Cleanup
      document.body.removeChild(link);
      window.URL.revokeObjectURL(url);
    } catch (error) {
      console.error("Export failed:", error);
      throw error;
    }
  },

  // Import full system configuration
  async importAll(data: unknown): Promise<void> {
    // Use longer timeout for large imports (5 minutes)
    await http.post("/system/import", data, { timeout: 300000 });
  },

  // ============ Separated Import Functions ============

  // Import system settings only (will force cache refresh)
  async importSystemSettings(data: { system_settings: Record<string, string> }): Promise<void> {
    // System settings import is usually fast, use default timeout
    await http.post("/system/import-settings", data);
  },

  // Batch import groups
  async importGroupsBatch(data: { groups: unknown[] }): Promise<void> {
    // Use longer timeout for large batch imports (5 minutes)
    await http.post("/system/import-groups-batch", data, { timeout: 300000 });
  },
};
