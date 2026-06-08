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

export const proxyPoolApi = {
  async list(): Promise<ProxyPoolItem[]> {
    const res = await http.get("/proxy-pool");
    return res.data || [];
  },

  async create(payload: ProxyPoolPayload): Promise<ProxyPoolItem> {
    const res = await http.post("/proxy-pool", payload);
    return res.data;
  },

  async update(id: number, payload: ProxyPoolPayload): Promise<ProxyPoolItem> {
    const res = await http.put(`/proxy-pool/${id}`, payload);
    return res.data;
  },

  async delete(id: number): Promise<void> {
    await http.delete(`/proxy-pool/${id}`);
  },

  async test(id: number): Promise<ProxyPoolTestResult> {
    const res = await http.post(`/proxy-pool/${id}/test`, {}, { hideMessage: true });
    return res.data;
  },
};
