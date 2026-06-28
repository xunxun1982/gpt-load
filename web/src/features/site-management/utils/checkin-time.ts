const SERVER_FALLBACK_TIMEZONE = "Asia/Shanghai";

type AutoCheckinStatusTimeMetadata = {
  timezone?: string;
  next_checkin_reset_at?: string;
};

type ZonedDateTimeParts = {
  year: number;
  month: number;
  day: number;
  hour: number;
  minute: number;
  second: number;
};

function resolveServerTimezone(timezone?: string): string {
  const candidate = timezone?.trim() || SERVER_FALLBACK_TIMEZONE;
  try {
    new Intl.DateTimeFormat("en-US", { timeZone: candidate }).format(new Date(0));
    return candidate;
  } catch {
    return SERVER_FALLBACK_TIMEZONE;
  }
}

function getZonedDateTimeParts(date: Date, timezone: string): ZonedDateTimeParts {
  const parts = new Intl.DateTimeFormat("en-US", {
    timeZone: timezone,
    year: "numeric",
    month: "2-digit",
    day: "2-digit",
    hour: "2-digit",
    minute: "2-digit",
    second: "2-digit",
    hourCycle: "h23",
  }).formatToParts(date);
  const values = new Map(parts.map(part => [part.type, part.value]));
  const hour = Number(values.get("hour") ?? "0");
  return {
    year: Number(values.get("year")),
    month: Number(values.get("month")),
    day: Number(values.get("day")),
    hour: hour === 24 ? 0 : hour,
    minute: Number(values.get("minute") ?? "0"),
    second: Number(values.get("second") ?? "0"),
  };
}

function addDays(year: number, month: number, day: number, days: number) {
  const result = new Date(Date.UTC(year, month - 1, day + days, 0, 0, 0));
  return {
    year: result.getUTCFullYear(),
    month: result.getUTCMonth() + 1,
    day: result.getUTCDate(),
  };
}

function zonedDateTimeToUtc(parts: ZonedDateTimeParts, timezone: string): Date {
  const targetAsUtc = Date.UTC(
    parts.year,
    parts.month - 1,
    parts.day,
    parts.hour,
    parts.minute,
    parts.second
  );
  let utcMillis = targetAsUtc;

  // Iteratively align the UTC instant with the requested IANA-zone wall time,
  // including DST offsets, without depending on the browser's local timezone.
  for (let i = 0; i < 3; i += 1) {
    const actualParts = getZonedDateTimeParts(new Date(utcMillis), timezone);
    const actualAsUtc = Date.UTC(
      actualParts.year,
      actualParts.month - 1,
      actualParts.day,
      actualParts.hour,
      actualParts.minute,
      actualParts.second
    );
    const delta = actualAsUtc - targetAsUtc;
    if (delta === 0) {
      break;
    }
    utcMillis -= delta;
  }

  return new Date(utcMillis);
}

function nextServerMidnight(now: number, timezone?: string): Date {
  const resolvedTimezone = resolveServerTimezone(timezone);
  const current = getZonedDateTimeParts(new Date(now), resolvedTimezone);
  const nextDay = addDays(current.year, current.month, current.day, 1);
  return zonedDateTimeToUtc(
    {
      ...nextDay,
      hour: 0,
      minute: 0,
      second: 1,
    },
    resolvedTimezone
  );
}

export function formatServerCheckinDay(now: number, timezone?: string): string {
  const resolvedTimezone = resolveServerTimezone(timezone);
  const parts = getZonedDateTimeParts(new Date(now), resolvedTimezone);
  return `${parts.year}-${String(parts.month).padStart(2, "0")}-${String(parts.day).padStart(2, "0")}`;
}

export function resolveCheckinDayRefreshTarget(
  status: AutoCheckinStatusTimeMetadata | null,
  now = Date.now()
): Date {
  const resetAt = status?.next_checkin_reset_at ? new Date(status.next_checkin_reset_at) : null;
  if (!resetAt || Number.isNaN(resetAt.getTime()) || resetAt.getTime() <= now) {
    return nextServerMidnight(now, status?.timezone);
  }
  return resetAt;
}
