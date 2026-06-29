import {
  autoCheckinApi,
  type AutoCheckinConfig,
  type AutoCheckinStatus,
} from "@/api/site-management";
import {
  formatServerCheckinDay,
  resolveCheckinDayRefreshTarget,
} from "@/features/site-management/utils/checkin-time";
import { computed, onUnmounted, ref, type ComputedRef } from "vue";

interface UseAutoCheckinStatusOptions {
  statusTimeLocale: ComputedRef<string | undefined>;
  t: (key: string) => string;
  refreshSites: () => Promise<unknown> | unknown;
}

const CHECKIN_REFRESH_ERROR_RETRY_MS = 5 * 60 * 1000;

export function useAutoCheckinStatus({
  statusTimeLocale,
  t,
  refreshSites,
}: UseAutoCheckinStatusOptions) {
  const autoCheckinConfig = ref<AutoCheckinConfig | null>(null);
  const autoCheckinStatus = ref<AutoCheckinStatus | null>(null);
  const autoCheckinLoading = ref(false);
  const checkinDayRefreshTimer = ref<number | undefined>(undefined);
  const fallbackNow = ref(Date.now());

  function clearCheckinDayRefresh() {
    if (checkinDayRefreshTimer.value) {
      window.clearTimeout(checkinDayRefreshTimer.value);
      checkinDayRefreshTimer.value = undefined;
    }
  }

  function scheduleCheckinDayRefresh(status: AutoCheckinStatus | null, delayOverride?: number) {
    clearCheckinDayRefresh();

    const now = Date.now();
    fallbackNow.value = now;
    const target = resolveCheckinDayRefreshTarget(status, now);
    const delay = delayOverride ?? Math.min(Math.max(target.getTime() - now, 1000), 2_147_483_647);

    checkinDayRefreshTimer.value = window.setTimeout(() => {
      fallbackNow.value = Date.now();
      void (async () => {
        await loadAutoCheckinConfig();
        try {
          await refreshSites();
        } catch {
          scheduleCheckinDayRefresh(autoCheckinStatus.value, CHECKIN_REFRESH_ERROR_RETRY_MS);
          /* Retry stale site-list refresh without surfacing an unhandled rejection. */
        }
      })();
    }, delay);
  }

  // Prefer backend metadata; fallback uses the same server timezone default as the backend.
  const currentCheckinDay = computed(() => {
    const status = autoCheckinStatus.value;
    const resetAt = status?.next_checkin_reset_at ? new Date(status.next_checkin_reset_at) : null;
    if (
      status?.current_checkin_day &&
      resetAt &&
      !Number.isNaN(resetAt.getTime()) &&
      resetAt.getTime() > fallbackNow.value
    ) {
      return status.current_checkin_day;
    }
    return formatServerCheckinDay(fallbackNow.value, status?.timezone);
  });

  async function loadAutoCheckinConfig() {
    autoCheckinLoading.value = true;
    try {
      const [configResult, statusResult] = await Promise.allSettled([
        autoCheckinApi.getConfig(),
        autoCheckinApi.getStatus(),
      ]);

      if (configResult.status === "fulfilled") {
        autoCheckinConfig.value = configResult.value;
      }
      if (statusResult.status === "fulfilled") {
        autoCheckinStatus.value = statusResult.value;
        scheduleCheckinDayRefresh(statusResult.value);
      } else {
        scheduleCheckinDayRefresh(autoCheckinStatus.value, CHECKIN_REFRESH_ERROR_RETRY_MS);
      }
    } finally {
      autoCheckinLoading.value = false;
    }
  }

  function formatStatusTime(value: string): string {
    try {
      const utcDate = new Date(value);
      if (Number.isNaN(utcDate.getTime())) {
        return value;
      }
      const timezone = autoCheckinStatus.value?.timezone;
      if (timezone) {
        return `${utcDate.toLocaleString(statusTimeLocale.value, { timeZone: timezone })} (${timezone})`;
      }
      return `${utcDate.toLocaleString(statusTimeLocale.value)} (${t("siteManagement.clientLocalTime")})`;
    } catch {
      return value;
    }
  }

  const nextScheduledDisplay = computed(() => {
    if (!autoCheckinStatus.value?.next_scheduled_at) {
      return "";
    }
    return formatStatusTime(autoCheckinStatus.value.next_scheduled_at);
  });

  const lastRunDisplay = computed(() => {
    if (!autoCheckinStatus.value?.last_run_at) {
      return "";
    }
    return formatStatusTime(autoCheckinStatus.value.last_run_at);
  });

  onUnmounted(clearCheckinDayRefresh);

  return {
    autoCheckinConfig,
    autoCheckinStatus,
    autoCheckinLoading,
    currentCheckinDay,
    nextScheduledDisplay,
    lastRunDisplay,
    loadAutoCheckinConfig,
  };
}
