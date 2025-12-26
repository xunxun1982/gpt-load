import i18n from "@/locales";
import { useAuthService } from "@/services/auth";
import axios from "axios";
import { appState } from "./app-state";

// Define API endpoints that should not trigger the global loading state
const noLoadingUrls = ["/tasks/status"];

declare module "axios" {
  interface AxiosRequestConfig {
    hideMessage?: boolean;
  }
}

/**
 * HTTP client wrapper based on axios.
 *
 * Type Definition Note:
 * The response interceptor returns `response.data` directly instead of the full AxiosResponse.
 * This causes TypeScript to infer incorrect return types (AxiosResponse instead of the actual data).
 * For blob responses (e.g., file downloads), callers need to use `as unknown as Blob` cast.
 *
 * Why not fix the type definitions:
 * 1. Changing return types would require updating all existing API calls across the project
 * 2. Axios's type system doesn't easily support conditional return types based on responseType
 * 3. The current approach works correctly at runtime; only the type inference is affected
 * 4. Risk of introducing regressions outweighs the benefit of cleaner types
 *
 * Workaround for blob responses:
 * ```typescript
 * const res = await http.get("/api/export", { responseType: "blob" });
 * return res as unknown as Blob;
 * ```
 */
const http = axios.create({
  baseURL: "/api",
  timeout: 60000,
  headers: { "Content-Type": "application/json" },
});

// Request interceptor
http.interceptors.request.use(config => {
  // Check whether the current request URL is in the no-loading list
  if (config.url && !noLoadingUrls.includes(config.url)) {
    appState.loading = true;
  }
  const authKey = localStorage.getItem("authKey");
  if (authKey) {
    config.headers.Authorization = `Bearer ${authKey}`;
  }
  // Add language header
  const locale = localStorage.getItem("locale") || "zh-CN";
  config.headers["Accept-Language"] = locale;
  return config;
});

// Response interceptor
http.interceptors.response.use(
  response => {
    appState.loading = false;
    if (response.config.method !== "get" && !response.config.hideMessage) {
      window.$message.success(response.data.message ?? i18n.global.t("common.operationSuccess"));
    }
    return response.data;
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

export default http;
