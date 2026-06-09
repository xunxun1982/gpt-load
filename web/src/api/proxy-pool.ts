import type { Pagination, ProxyPoolItem } from "@/types/models";
import http from "@/utils/http";

export interface ProxyPoolPayload {
  name: string;
  url: string;
}

export interface ProxyPoolTestResult {
  success: boolean;
  url: string;
  target_url: string;
  timeout_ms: number;
  duration_ms: number;
  status_code?: number;
  error?: string;
}

export interface ProxyPoolListParams {
  page?: number;
  page_size?: number;
}

export interface ProxyPoolListResponse {
  items: ProxyPoolItem[];
  pagination: Pagination;
}

function requireProxyPoolData<T>(data: T | null | undefined, action: string): T {
  if (data === null || data === undefined) {
    throw new Error(`Proxy pool ${action} response missing data`);
  }
  return data;
}

function normalizeProxyPoolListResponse(
  data: ProxyPoolItem[] | ProxyPoolListResponse,
  params: ProxyPoolListParams = {}
): ProxyPoolListResponse {
  if (Array.isArray(data)) {
    return {
      items: data,
      pagination: {
        page: params.page ?? 1,
        page_size: params.page_size ?? data.length,
        total_items: data.length,
        total_pages: data.length > 0 ? 1 : 0,
        has_more: false,
      },
    };
  }
  if (!Array.isArray(data?.items) || !data.pagination) {
    throw new Error("Proxy pool list response data is invalid");
  }
  return data;
}

export const proxyPoolApi = {
  async list(): Promise<ProxyPoolItem[]> {
    const res = await http.get("/proxy-pool", { params: { page: 1, page_size: 1000 } });
    const data = requireProxyPoolData<ProxyPoolItem[] | ProxyPoolListResponse>(res?.data, "list");
    return normalizeProxyPoolListResponse(data, { page: 1, page_size: 1000 }).items;
  },

  async listPage(params: ProxyPoolListParams = {}): Promise<ProxyPoolListResponse> {
    const res = await http.get("/proxy-pool", { params });
    const data = requireProxyPoolData<ProxyPoolItem[] | ProxyPoolListResponse>(res?.data, "list");
    return normalizeProxyPoolListResponse(data, params);
  },

  async create(payload: ProxyPoolPayload): Promise<ProxyPoolItem> {
    const res = await http.post("/proxy-pool", payload);
    return requireProxyPoolData<ProxyPoolItem>(res?.data, "create");
  },

  async update(id: number, payload: ProxyPoolPayload): Promise<ProxyPoolItem> {
    const res = await http.put(`/proxy-pool/${id}`, payload);
    return requireProxyPoolData<ProxyPoolItem>(res?.data, "update");
  },

  async delete(id: number): Promise<void> {
    await http.delete(`/proxy-pool/${id}`);
  },

  async test(id: number): Promise<ProxyPoolTestResult> {
    const res = await http.post(`/proxy-pool/${id}/test`, {}, { hideMessage: true });
    return requireProxyPoolData<ProxyPoolTestResult>(res?.data, "test");
  },
};
