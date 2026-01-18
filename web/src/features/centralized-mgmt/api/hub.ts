/**
 * Hub (Centralized Management) API client.
 * Provides functions for interacting with Hub endpoints.
 *
 * Note: Hub API admin routes are at /hub/admin/* (no /v1/ prefix),
 * while proxy routes use /hub/v1/* for API compatibility.
 */

import i18n from "@/locales";
import { useAuthService } from "@/services/auth";
import { appState } from "@/utils/app-state";
import axios from "axios";
import type {
  AccessKeyListParams,
  AccessKeyListResponse,
  BatchOperationResponse,
  CreateAccessKeyParams,
  HubAccessKey,
  HubSettings,
  ModelPoolEntry,
  ModelPoolParams,
  ModelPoolResponse,
  ModelPoolV2Response,
  UpdateAccessKeyParams,
  UpdateModelGroupPriorityParams,
} from "../types/hub";

/**
 * Hub-specific HTTP client.
 * Admin routes are at /hub/admin (no /v1/ prefix).
 * Proxy routes (/hub/v1/*) are for aggregated API access.
 */
const hubHttp = axios.create({
  baseURL: "/hub/admin",
  timeout: 60000,
  headers: { "Content-Type": "application/json" },
});

// Request interceptor
hubHttp.interceptors.request.use(config => {
  appState.loading = true;
  const authKey = localStorage.getItem("authKey");
  if (authKey) {
    config.headers.Authorization = `Bearer ${authKey}`;
  }
  const locale = localStorage.getItem("locale") || "zh-CN";
  config.headers["Accept-Language"] = locale;
  return config;
});

// Response interceptor
hubHttp.interceptors.response.use(
  response => {
    appState.loading = false;
    if (response.config.method !== "get") {
      window.$message.success(response.data.message ?? i18n.global.t("common.operationSuccess"));
    }
    // Return the data field from standard response { code, message, data }
    return response.data.data ?? response.data;
  },
  error => {
    appState.loading = false;
    if (error.response) {
      if (error.response.status === 401) {
        if (window.location.pathname !== "/login") {
          const { logout } = useAuthService();
          logout();
          window.location.href = "/login";
        }
      }
      window.$message.error(
        error.response.data?.message ||
          i18n.global.t("common.requestFailed", { status: error.response.status }),
        {
          keepAliveOnHover: true,
          duration: 5000,
          closable: true,
        }
      );
    } else if (error.request) {
      window.$message.error(i18n.global.t("common.networkError"));
    } else {
      window.$message.error(i18n.global.t("common.requestSetupError"));
    }
    return Promise.reject(error);
  }
);

export const hubApi = {
  /**
   * Get the aggregated model pool with all available models from enabled groups.
   * Supports pagination and search filtering.
   */
  async getModelPool(params: ModelPoolParams = {}): Promise<ModelPoolResponse> {
    const query = new URLSearchParams();
    if (params.page) {
      query.set("page", params.page.toString());
    }
    if (params.page_size) {
      query.set("page_size", params.page_size.toString());
    }
    if (params.search) {
      query.set("search", params.search);
    }

    const queryString = query.toString();
    const url = queryString ? `/model-pool?${queryString}` : "/model-pool";
    return hubHttp.get<unknown, ModelPoolResponse>(url);
  },

  /**
   * Get the model pool V2 with priority information.
   */
  async getModelPoolV2(): Promise<ModelPoolV2Response> {
    return hubHttp.get<unknown, ModelPoolV2Response>("/model-pool/v2");
  },

  /**
   * Get all models without pagination (for dropdowns/selectors).
   */
  async getAllModels(): Promise<ModelPoolEntry[]> {
    const res = await hubHttp.get<unknown, ModelPoolEntry[]>("/model-pool/all");
    return res || [];
  },

  /**
   * Update model-group priority.
   */
  async updateModelGroupPriority(params: UpdateModelGroupPriorityParams): Promise<void> {
    await hubHttp.put("/model-pool/priority", params);
  },

  /**
   * Batch update model-group priorities.
   */
  async batchUpdatePriorities(updates: UpdateModelGroupPriorityParams[]): Promise<void> {
    await hubHttp.put("/model-pool/priorities", { updates });
  },

  /**
   * Get Hub settings.
   */
  async getSettings(): Promise<HubSettings> {
    return hubHttp.get<unknown, HubSettings>("/settings");
  },

  /**
   * Update Hub settings.
   */
  async updateSettings(settings: HubSettings): Promise<void> {
    await hubHttp.put("/settings", settings);
  },

  /**
   * List Hub access keys with optional pagination and filtering.
   */
  async listAccessKeys(params: AccessKeyListParams = {}): Promise<AccessKeyListResponse> {
    const query = new URLSearchParams();
    if (params.page) {
      query.set("page", params.page.toString());
    }
    if (params.page_size) {
      query.set("page_size", params.page_size.toString());
    }
    if (params.search) {
      query.set("search", params.search);
    }
    if (params.enabled !== undefined && params.enabled !== null) {
      query.set("enabled", params.enabled.toString());
    }

    const queryString = query.toString();
    const url = queryString ? `/access-keys?${queryString}` : "/access-keys";
    return hubHttp.get<unknown, AccessKeyListResponse>(url);
  },

  /**
   * Create a new Hub access key.
   * If key_value is not provided, it will be auto-generated.
   */
  async createAccessKey(
    params: CreateAccessKeyParams
  ): Promise<{ access_key: HubAccessKey; key_value: string }> {
    return hubHttp.post<unknown, { access_key: HubAccessKey; key_value: string }>(
      "/access-keys",
      params
    );
  },

  /**
   * Update an existing Hub access key.
   */
  async updateAccessKey(id: number, params: UpdateAccessKeyParams): Promise<HubAccessKey> {
    return hubHttp.put<unknown, HubAccessKey>(`/access-keys/${id}`, params);
  },

  /**
   * Delete a Hub access key.
   */
  async deleteAccessKey(id: number): Promise<void> {
    await hubHttp.delete(`/access-keys/${id}`);
  },

  /**
   * Toggle the enabled status of a Hub access key.
   */
  async toggleAccessKey(id: number, enabled: boolean): Promise<HubAccessKey> {
    return hubHttp.put<unknown, HubAccessKey>(`/access-keys/${id}`, { enabled });
  },

  /**
   * Batch delete Hub access keys.
   */
  async batchDeleteAccessKeys(ids: number[]): Promise<BatchOperationResponse> {
    return hubHttp.delete<unknown, BatchOperationResponse>("/access-keys/batch", {
      data: { ids },
    });
  },

  /**
   * Batch enable or disable Hub access keys.
   */
  async batchUpdateAccessKeysEnabled(
    ids: number[],
    enabled: boolean
  ): Promise<BatchOperationResponse> {
    return hubHttp.put<unknown, BatchOperationResponse>("/access-keys/batch/enabled", {
      ids,
      enabled,
    });
  },

  /**
   * Get usage statistics for a Hub access key.
   */
  async getAccessKeyUsageStats(id: number): Promise<HubAccessKey> {
    return hubHttp.get<unknown, HubAccessKey>(`/access-keys/${id}/stats`);
  },

  /**
   * Get plaintext (decrypted) key value for an access key.
   * This is used for copying the full key value.
   */
  async getAccessKeyPlaintext(id: number): Promise<{ key_value: string }> {
    return hubHttp.get<unknown, { key_value: string }>(`/access-keys/${id}/plaintext`);
  },
};

// Re-export types for convenience
export type {
  AccessKeyListParams,
  AccessKeyListResponse,
  AllowedModelsMode,
  BatchAccessKeyOperationParams,
  BatchEnableDisableParams,
  BatchOperationResponse,
  CreateAccessKeyParams,
  HubAccessKey,
  HubEndpointInfo,
  HubSettings,
  ModelGroupPriority,
  ModelPoolEntry,
  ModelPoolEntryV2,
  ModelPoolParams,
  ModelPoolResponse,
  ModelPoolV2Response,
  ModelSource,
  UpdateAccessKeyParams,
  UpdateModelGroupPriorityParams,
} from "../types/hub";
