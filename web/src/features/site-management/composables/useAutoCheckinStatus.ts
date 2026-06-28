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

  function clearCheckinDayRefresh() {
    if (checkinDayRefreshTimer.value) {
      window.clearTimeout(checkinDayRefreshTimer.value);
      checkinDayRefreshTimer.value = undefined;
    }
  }

  function scheduleCheckinDayRefresh(status: AutoCheckinStatus | null, delayOverride?: number) {
    clearCheckinDayRefresh();

    const now = Date.now();
    const target = resolveCheckinDayRefreshTarget(status, now);
    const delay =
      delayOverride ?? Math.min(Math.max(target.getTime() - now + 1000, 1000), 2_147_483_647);

    checkinDayRefreshTimer.value = window.setTimeout(() => {
      void (async () => {
        await loadAutoCheckinConfig();
        await refreshSites();
      })();
    }, delay);
  }

  // Prefer backend metadata; fallback uses the same server timezone default as the backend.
  const currentCheckinDay = computed(
    () =>
      autoCheckinStatus.value?.current_checkin_day ||
      formatServerCheckinDay(Date.now(), autoCheckinStatus.value?.timezone)
  );

  async function loadAutoCheckinConfig() {
    autoCheckinLoading.value = true;
    try {
      const [config, status] = await Promise.all([
        autoCheckinApi.getConfig(),
        autoCheckinApi.getStatus(),
      ]);
      autoCheckinConfig.value = config;
      autoCheckinStatus.value = status;
      scheduleCheckinDayRefresh(status);
    } catch (_) {
      scheduleCheckinDayRefresh(autoCheckinStatus.value, CHECKIN_REFRESH_ERROR_RETRY_MS);
      /* handled by centralized error handler */
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
