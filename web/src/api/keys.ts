import i18n from "@/locales";
import type {
  APIKey,
  Group,
  GroupConfigOption,
  GroupListItem,
  GroupStatsResponse,
  KeyStatus,
  ParentAggregateGroup,
  TaskInfo,
} from "@/types/models";
import http from "@/utils/http";

// Type for model item in OpenAI format
interface OpenAIModelItem {
  id: string;
  [key: string]: unknown;
}

// Type for model item in Gemini format
interface GeminiModelItem {
  name: string;
  [key: string]: unknown;
}

// Response type for fetchGroupModels
interface FetchModelsResponse {
  data?: OpenAIModelItem[];
  models?: GeminiModelItem[];
}

export const keysApi = {
  // Get all groups
  async getGroups(): Promise<Group[]> {
    const res = await http.get("/groups");
    return res.data || [];
  },

  // Create a new group
  async createGroup(group: Partial<Group>): Promise<Group> {
    const res = await http.post("/groups", group);
    return res.data;
  },

  // Update an existing group
  async updateGroup(groupId: number, group: Partial<Group>): Promise<Group> {
    const res = await http.put(`/groups/${groupId}`, group);
    return res.data;
  },

  // Delete a group
  // Returns response data which may contain status information for async deletions
  deleteGroup(groupId: number): Promise<{ message?: string; code?: string }> {
    return http.delete(`/groups/${groupId}`);
  },

  // Delete all groups (debug mode only)
  // This is a dangerous operation that deletes ALL groups and keys
  // Only available when DEBUG_MODE environment variable is enabled
  // Uses extended timeout (5 minutes) to handle large datasets
  deleteAllGroups(): Promise<void> {
    return http.delete("/groups/debug/delete-all", {
      timeout: 300000, // 5 minutes timeout for large deletions
    });
  },

  // Get group statistics
  async getGroupStats(groupId: number): Promise<GroupStatsResponse> {
    const res = await http.get(`/groups/${groupId}/stats`);
    return res.data;
  },

  // Get group configurable options
  async getGroupConfigOptions(): Promise<GroupConfigOption[]> {
    const res = await http.get("/groups/config-options");
    return res.data || [];
  },

  // Copy a group
  async copyGroup(
    groupId: number,
    copyData: {
      copy_keys: "none" | "valid_only" | "all";
    }
  ): Promise<{
    group: Group;
  }> {
    const res = await http.post(`/groups/${groupId}/copy`, copyData, {
      hideMessage: true,
    });
    return res.data;
  },

  // Get list of groups (simplified)
  async listGroups(): Promise<GroupListItem[]> {
    const res = await http.get("/groups/list");
    return res.data || [];
  },

  // Get keys for a specific group
  async getGroupKeys(params: {
    group_id: number;
    page: number;
    page_size: number;
    key_value?: string;
    status?: KeyStatus;
  }): Promise<{
    items: APIKey[];
    pagination: {
      total_items: number;
      total_pages: number;
    };
  }> {
    const res = await http.get("/keys", { params });
    return res.data;
  },

  // Add multiple keys (deprecated)
  async addMultipleKeys(
    group_id: number,
    keys_text: string
  ): Promise<{
    added_count: number;
    ignored_count: number;
    total_in_group: number;
  }> {
    const res = await http.post("/keys/add-multiple", {
      group_id,
      keys_text,
    });
    return res.data;
  },

  // Add keys asynchronously (batch)
  async addKeysAsync(group_id: number, keys_text: string): Promise<TaskInfo> {
    const res = await http.post(
      "/keys/add-async",
      {
        group_id,
        keys_text,
      },
      {
        hideMessage: true,
        timeout: 300000, // 5 minutes timeout to support large files over slow networks
      }
    );
    return res.data;
  },

  // Add keys asynchronously using streaming (for large files > 10MB)
  // This method uploads the file using multipart/form-data and processes it in batches on the server
  // Memory usage is constant regardless of file size
  async addKeysAsyncStream(group_id: number, file: File): Promise<TaskInfo> {
    const formData = new FormData();
    formData.append("group_id", group_id.toString());
    formData.append("file", file);

    // Note: Must explicitly set Content-Type to undefined for FormData
    // The default axios instance has "Content-Type: application/json" which overrides FormData's automatic header
    // Setting to undefined allows axios to set the correct multipart/form-data with boundary
    const res = await http.post("/keys/add-async-stream", formData, {
      hideMessage: true,
      timeout: 300000, // 5 minutes timeout
      headers: {
        "Content-Type": undefined, // Let axios set multipart/form-data with boundary
      },
    });
    return res.data;
  },

  // Update key notes
  async updateKeyNotes(keyId: number, notes: string): Promise<void> {
    await http.put(`/keys/${keyId}/notes`, { notes }, { hideMessage: true });
  },

  // Test keys
  async testKeys(
    group_id: number,
    keys_text: string
  ): Promise<{
    results: {
      key_value: string;
      is_valid: boolean;
      error: string;
    }[];
    total_duration: number;
  }> {
    const res = await http.post(
      "/keys/test-multiple",
      {
        group_id,
        keys_text,
      },
      {
        hideMessage: true,
      }
    );
    return res.data;
  },

  // Delete keys
  async deleteKeys(
    group_id: number,
    keys_text: string
  ): Promise<{ deleted_count: number; ignored_count: number; total_in_group: number }> {
    const res = await http.post("/keys/delete-multiple", {
      group_id,
      keys_text,
    });
    return res.data;
  },

  // Delete keys asynchronously (batch)
  async deleteKeysAsync(group_id: number, keys_text: string): Promise<TaskInfo> {
    const res = await http.post(
      "/keys/delete-async",
      {
        group_id,
        keys_text,
      },
      {
        hideMessage: true,
        timeout: 300000, // 5 minutes timeout to support large files over slow networks
      }
    );
    return res.data;
  },

  // Restore keys
  restoreKeys(group_id: number, keys_text: string): Promise<null> {
    return http.post("/keys/restore-multiple", {
      group_id,
      keys_text,
    });
  },

  // Restore all invalid keys
  restoreAllInvalidKeys(group_id: number): Promise<TaskInfo | { message: string }> {
    return http.post("/keys/restore-all-invalid", { group_id });
  },

  // Clear all invalid keys
  clearAllInvalidKeys(group_id: number): Promise<TaskInfo | { message: string }> {
    return http.post(
      "/keys/clear-all-invalid",
      { group_id },
      {
        hideMessage: true,
      }
    );
  },

  // Clear all keys
  clearAllKeys(group_id: number): Promise<TaskInfo | { message: string }> {
    return http.post(
      "/keys/clear-all",
      { group_id },
      {
        hideMessage: true,
      }
    );
  },

  // Export keys
  exportKeys(groupId: number, status: "all" | "active" | "invalid" = "all"): void {
    const authKey = localStorage.getItem("authKey");
    if (!authKey) {
      window.$message.error(i18n.global.t("auth.noAuthKeyFound"));
      return;
    }

    const params = new URLSearchParams({
      group_id: groupId.toString(),
      key: authKey,
    });

    if (status !== "all") {
      params.append("status", status);
    }

    const url = `${http.defaults.baseURL}/keys/export?${params.toString()}`;

    const link = document.createElement("a");
    link.href = url;
    link.setAttribute("download", `keys-group_${groupId}-${status}-${Date.now()}.txt`);
    document.body.appendChild(link);
    link.click();
    document.body.removeChild(link);
  },

  // Validate group keys
  async validateGroupKeys(
    groupId: number,
    status?: "active" | "invalid"
  ): Promise<{
    is_running: boolean;
    group_name: string;
    processed: number;
    total: number;
    started_at: string;
  }> {
    const payload: { group_id: number; status?: string } = { group_id: groupId };
    if (status) {
      payload.status = status;
    }
    const res = await http.post("/keys/validate-group", payload);
    return res.data;
  },

  // Get task status
  async getTaskStatus(): Promise<TaskInfo> {
    const res = await http.get("/tasks/status");
    return res.data;
  },

  // Get sub-groups for an aggregate group
  async getSubGroups(aggregateGroupId: number): Promise<import("@/types/models").SubGroupInfo[]> {
    const res = await http.get(`/groups/${aggregateGroupId}/sub-groups`);
    return res.data || [];
  },

  // Add sub-groups to an aggregate group
  async addSubGroups(
    aggregateGroupId: number,
    subGroups: { group_id: number; weight: number }[]
  ): Promise<void> {
    await http.post(`/groups/${aggregateGroupId}/sub-groups`, {
      sub_groups: subGroups,
    });
  },

  // Update sub-group weight
  async updateSubGroupWeight(
    aggregateGroupId: number,
    subGroupId: number,
    weight: number
  ): Promise<void> {
    await http.put(`/groups/${aggregateGroupId}/sub-groups/${subGroupId}/weight`, {
      weight,
    });
  },

  // Delete a sub-group
  async deleteSubGroup(aggregateGroupId: number, subGroupId: number): Promise<void> {
    await http.delete(`/groups/${aggregateGroupId}/sub-groups/${subGroupId}`);
  },

  // Get parent aggregate groups that reference this group
  async getParentAggregateGroups(groupId: number): Promise<ParentAggregateGroup[]> {
    const res = await http.get(`/groups/${groupId}/parent-aggregate-groups`);
    return res.data || [];
  },

  // Toggle group enabled/disabled status
  async toggleGroupEnabled(groupId: number, enabled: boolean): Promise<void> {
    await http.put(`/groups/${groupId}/toggle-enabled`, { enabled }, { hideMessage: true });
  },

  // ============ Import/Export ============

  // Export complete group data
  async exportGroup(groupId: number, mode: "plain" | "encrypted" = "encrypted"): Promise<void> {
    try {
      // Request export with mode parameter
      const data = await http.get(`/groups/${groupId}/export`, {
        params: { mode },
        hideMessage: true,
      });

      if (!data) {
        throw new Error("Export data is empty");
      }

      const jsonStr = JSON.stringify(data, null, 2);
      const blob = new Blob([jsonStr], { type: "application/json;charset=utf-8" });

      // Generate filename with different prefix and mode suffix
      const exportData = data as { group?: { name?: string; group_type?: string } };
      const groupData = exportData.group;
      const groupName = groupData?.name || `group_${groupId}`;
      const groupType = groupData?.group_type || "standard";
      const prefix = groupType === "aggregate" ? "aggregate-group" : "standard-group";
      const timestamp = new Date().toISOString().replace(/[:.]/g, "-").slice(0, 19);
      const suffix = mode === "plain" ? "plain" : "enc";
      const filename = `${prefix}_${groupName}_${timestamp}-${suffix}.json`;

      const url = window.URL.createObjectURL(blob);
      const link = document.createElement("a");
      link.href = url;
      link.download = filename;
      document.body.appendChild(link);
      link.click();
      document.body.removeChild(link);
      window.URL.revokeObjectURL(url);
    } catch (error) {
      console.error("Export failed:", error);
      throw error;
    }
  },

  // Import group data
  async importGroup(
    data: unknown,
    options?: { mode?: "plain" | "encrypted" | "auto"; filename?: string }
  ): Promise<Group> {
    const params: Record<string, string> = {};
    if (options?.mode) {
      params.mode = options.mode;
    }
    if (options?.filename) {
      params.filename = options.filename;
    }
    const res = await http.post("/groups/import", data, { params });
    return res.data;
  },

  // Fetch available models from upstream service
  async fetchGroupModels(groupId: number): Promise<FetchModelsResponse> {
    const res = await http.get(`/groups/${groupId}/models`, {
      hideMessage: true,
    });
    return res.data;
  },

  // Child group APIs
  // Create a child group for a standard group
  async createChildGroup(
    parentGroupId: number,
    data: { name?: string; display_name?: string; description?: string }
  ): Promise<Group> {
    const res = await http.post(`/groups/${parentGroupId}/child-groups`, data);
    return res.data;
  },

  // Get child groups for a standard group
  async getChildGroups(parentGroupId: number): Promise<import("@/types/models").ChildGroupInfo[]> {
    const res = await http.get(`/groups/${parentGroupId}/child-groups`);
    return res.data || [];
  },

  // Get parent group for a child group
  async getParentGroup(childGroupId: number): Promise<Group | null> {
    const res = await http.get(`/groups/${childGroupId}/parent-group`);
    return res.data;
  },

  // Get child group count for deletion warning
  async getChildGroupCount(groupId: number): Promise<number> {
    const res = await http.get(`/groups/${groupId}/child-group-count`);
    return res.data?.count || 0;
  },

  // Get all child groups for all parent groups in one request
  async getAllChildGroups(): Promise<Record<number, import("@/types/models").ChildGroupInfo[]>> {
    const res = await http.get("/groups/all-child-groups");
    return res.data || {};
  },

  // ============ Site Binding ============

  // Bind a group to a site
  async bindGroupToSite(groupId: number, siteId: number): Promise<void> {
    await http.post(`/groups/${groupId}/bind-site`, { site_id: siteId });
  },

  // Unbind a group from its bound site
  async unbindGroupFromSite(groupId: number): Promise<void> {
    await http.delete(`/groups/${groupId}/bind-site`);
  },

  // Get bound site info for a group
  async getBoundSiteInfo(groupId: number): Promise<{ id: number; name: string } | null> {
    const res = await http.get(`/groups/${groupId}/bound-site`);
    return res.data;
  },

  // Get model redirect dynamic weights for a group
  async getModelRedirectDynamicWeights(
    groupId: number
  ): Promise<import("@/types/models").ModelRedirectDynamicWeight[]> {
    const res = await http.get(`/groups/${groupId}/model-redirect-weights`, {
      hideMessage: true,
    });
    return res.data || [];
  },

  // Reset sub-group health metrics
  async resetSubGroupHealth(aggregateGroupId: number, subGroupId: number): Promise<void> {
    await http.post(`/groups/${aggregateGroupId}/sub-groups/${subGroupId}/reset-health`);
  },

  // Reset model redirect health metrics
  async resetModelRedirectHealth(
    groupId: number,
    sourceModel: string,
    targetModel: string
  ): Promise<void> {
    await http.post(`/groups/${groupId}/model-redirect-health/reset`, {
      source_model: sourceModel,
      target_model: targetModel,
    });
  },
};
