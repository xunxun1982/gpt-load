import type { ProxyPoolItem } from "@/types/models";
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

function requireProxyPoolData<T>(data: T | null | undefined, action: string): T {
  if (data === null || data === undefined) {
    throw new Error(`Proxy pool ${action} response missing data`);
  }
  return data;
}

export const proxyPoolApi = {
  async list(): Promise<ProxyPoolItem[]> {
    const res = await http.get("/proxy-pool");
    const data = requireProxyPoolData<ProxyPoolItem[]>(res?.data, "list");
    if (!Array.isArray(data)) {
      throw new Error("Proxy pool list response data is invalid");
    }
    return data;
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
