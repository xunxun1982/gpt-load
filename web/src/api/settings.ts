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

  // ============ 系统全量导入导出 ============

  // 导出系统全量配置
  async exportAll(): Promise<void> {
    try {
      const response = await http.get("/system/export", { hideMessage: true });

      // 处理响应格式：响应拦截器已经返回了 response.data，所以 response 就是数据对象
      // 但如果数据被包装在 { code, message, data } 格式中，需要提取 data
      let exportData: any = response;
      if (response && typeof response === 'object' && 'data' in response) {
        const resp = response as any;
        if (resp.data && typeof resp.data === 'object' &&
            (resp.code !== undefined || resp.message !== undefined)) {
          // 如果响应是标准的 { code, message, data } 格式，提取 data
          exportData = resp.data;
        }
      }

      // 将数据转换为 JSON 字符串
      const jsonStr = JSON.stringify(exportData, null, 2);

      // 创建 Blob 对象
      const blob = new Blob([jsonStr], { type: "application/json;charset=utf-8" });

      // 生成文件名
      const timestamp = new Date().toISOString().replace(/[:.]/g, "-").slice(0, 19);
      const filename = `system_export_${timestamp}.json`;

      // 创建下载链接
      const url = window.URL.createObjectURL(blob);
      const link = document.createElement("a");
      link.href = url;
      link.download = filename;

      // 触发下载
      document.body.appendChild(link);
      link.click();

      // 清理
      document.body.removeChild(link);
      window.URL.revokeObjectURL(url);
    } catch (error) {
      console.error("Export failed:", error);
      throw error;
    }
  },

  // 导入系统全量配置
  async importAll(data: unknown): Promise<void> {
    await http.post("/system/import", data);
  },

  // ============ 分离的导入功能 ============

  // 导入系统设置（仅系统设置，会强制刷新缓存）
  async importSystemSettings(data: { system_settings: Record<string, string> }): Promise<void> {
    await http.post("/system/import-settings", data);
  },

  // 批量导入分组
  async importGroupsBatch(data: { groups: unknown[] }): Promise<void> {
    await http.post("/system/import-groups-batch", data);
  },
};
