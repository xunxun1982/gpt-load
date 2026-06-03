type Translate = (key: string, params?: Record<string, unknown>) => string;

const intervalValues = [
  60 * 60,
  2 * 60 * 60,
  4 * 60 * 60,
  6 * 60 * 60,
  12 * 60 * 60,
  24 * 60 * 60,
  2 * 24 * 60 * 60,
  7 * 24 * 60 * 60,
];

function formatIntervalLabel(t: Translate, seconds: number): string {
  const hours = seconds / 3600;
  if (hours < 24) {
    return t("keys.healthResetEveryHours", { count: hours });
  }
  return t("keys.healthResetEveryDays", { count: hours / 24 });
}

function buildIntervalOptions(t: Translate) {
  return intervalValues.map(value => ({
    label: formatIntervalLabel(t, value),
    value,
  }));
}

export function getAggregateHealthResetOptions(t: Translate) {
  return [{ label: t("keys.healthResetDisabled"), value: 0 }, ...buildIntervalOptions(t)];
}

export function getSubGroupHealthResetOptions(t: Translate) {
  return [
    { label: t("subGroups.healthResetFollowAggregate"), value: 0 },
    ...buildIntervalOptions(t),
  ];
}
