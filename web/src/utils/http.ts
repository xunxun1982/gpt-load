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
  timeout: 120000, // Increased to 120s for large import operations
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

// Helper to detect timeout errors (client-side or server-side)
function isTimeoutError(error: { code?: string; message?: string }): boolean {
  return (
    error.code === "ECONNABORTED" ||
    /timeout|deadline exceeded|context deadline exceeded/i.test(String(error.message || ""))
  );
}

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
      // Normalize error message to string (can be object/array/blob from server)
      // Handle both plain string responses and object responses with .message property
      const responseData = error.response?.data;
      const rawMsg =
        (typeof responseData === "string" ? responseData : responseData?.message) ??
        error.message ??
        "";
      // Check for both null and undefined to preserve original behavior
      // Using explicit checks instead of loose equality (!=) to satisfy ESLint
      const errorMsg =
        typeof rawMsg === "string"
          ? rawMsg
          : rawMsg !== null && rawMsg !== undefined
            ? (() => {
                try {
                  return JSON.stringify(rawMsg);
                } catch {
                  return String(rawMsg);
                }
              })()
            : "";
      let displayMsg = errorMsg;

      // Detect timeout errors: check error code, message patterns, and HTTP status codes
      // Note: In error.response branch, ECONNABORTED is rare (server responded),
      // but we check message patterns for server-side database timeouts
      // HTTP 408 (Request Timeout) and 504 (Gateway Timeout) are also treated as timeouts
      const status = error.response?.status;
      const isTimeout =
        isTimeoutError({ code: error.code, message: errorMsg }) || status === 408 || status === 504;

      if (isTimeout) {
        // Show database busy message for better UX
        // This covers both client timeouts and server-reported database timeouts
        displayMsg = i18n.global.t("common.databaseBusy");
      } else if (!errorMsg) {
        displayMsg = i18n.global.t("common.requestFailed", { status: error.response.status });
      }

      window.$message.error(displayMsg, {
        keepAliveOnHover: true,
        duration: 8000, // Longer duration for important error messages
        closable: true,
      });
    } else if (error.request) {
      // Network errors or timeouts without response
      const isTimeout = isTimeoutError({ code: error.code, message: error.message });

      // Note: Using "databaseBusy" for timeouts is intentional - most timeouts in this app
      // occur during backend operations (imports, validations) rather than pure network issues.
      // This provides more accurate context to users about what's happening.
      window.$message.error(
        isTimeout ? i18n.global.t("common.databaseBusy") : i18n.global.t("common.networkError")
      );
    } else {
      window.$message.error(i18n.global.t("common.requestSetupError"));
    }
    return Promise.reject(error);
  }
);

export default http;
