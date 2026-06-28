import {
  autoCheckinApi,
  type AutoCheckinConfig,
  type AutoCheckinStatus,
} from "@/api/site-management";
import { computed, onUnmounted, ref, type ComputedRef } from "vue";

interface UseAutoCheckinStatusOptions {
  statusTimeLocale: ComputedRef<string | undefined>;
  t: (key: string) => string;
  refreshSites: () => Promise<unknown> | unknown;
}

function formatLocalDate(date: Date): string {
  const year = date.getFullYear();
  const month = String(date.getMonth() + 1).padStart(2, "0");
  const day = String(date.getDate()).padStart(2, "0");
  return `${year}-${month}-${day}`;
}

function nextLocalMidnight(now = new Date()): Date {
  return new Date(now.getFullYear(), now.getMonth(), now.getDate() + 1, 0, 0, 1);
}

function resolveCheckinDayRefreshTarget(status: AutoCheckinStatus | null, now = Date.now()): Date {
  const resetAt = status?.next_checkin_reset_at
    ? new Date(status.next_checkin_reset_at)
    : nextLocalMidnight(new Date(now));
  // Stale reset metadata should not create a tight reload loop after refresh failures.
  if (Number.isNaN(resetAt.getTime()) || resetAt.getTime() <= now) {
    return nextLocalMidnight(new Date(now));
  }
  return resetAt;
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

  // Prefer the backend-resolved site-management day. The local fallback only covers
  // initial loading and older backends that do not return current_checkin_day yet.
  const currentCheckinDay = computed(
    () => autoCheckinStatus.value?.current_checkin_day || formatLocalDate(new Date())
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
        return utcDate.toLocaleString(statusTimeLocale.value, { timeZone: timezone });
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
