export const retryDelayConfigKey = "retry_delay_ms";
export const retryBackoffEnabledConfigKey = "retry_backoff_enabled";
export const retryBackoffMaxPercentConfigKey = "retry_backoff_max_percent";

export interface RetryConfigDefaults {
  backoffEnabled: boolean;
  backoffMaxPercent: number;
}

export interface RetryConfigFormState {
  key: string;
  value: number | string | boolean | null;
  retryBackoffEnabled: boolean;
  retryBackoffMaxPercent: number;
  retryBackoffEnabledExplicit: boolean;
  retryBackoffMaxPercentExplicit: boolean;
  retryDelayInherited: boolean;
  retryDelayInitialValue: number;
}

export function hasOwnConfigValue(config: Record<string, unknown>, key: string): boolean {
  return Object.prototype.hasOwnProperty.call(config, key);
}

export function numberConfigValue(value: unknown, fallback: number): number {
  const numeric = Number(value);
  return Number.isFinite(numeric) ? numeric : fallback;
}

export function booleanConfigValue(value: unknown, fallback: boolean): boolean {
  return typeof value === "boolean" ? value : fallback;
}

export function buildRetryConfigState(
  key: string,
  value: number | string | boolean | null,
  rawConfig: Record<string, unknown>,
  defaults: RetryConfigDefaults,
  retryDelayInherited = false
): RetryConfigFormState {
  const hasBackoffEnabled = hasOwnConfigValue(rawConfig, retryBackoffEnabledConfigKey);
  const hasBackoffMaxPercent = hasOwnConfigValue(rawConfig, retryBackoffMaxPercentConfigKey);
  return {
    key,
    value,
    retryBackoffEnabled:
      key === retryDelayConfigKey
        ? booleanConfigValue(
            hasBackoffEnabled ? rawConfig[retryBackoffEnabledConfigKey] : undefined,
            defaults.backoffEnabled
          )
        : false,
    retryBackoffMaxPercent:
      key === retryDelayConfigKey
        ? numberConfigValue(
            hasBackoffMaxPercent ? rawConfig[retryBackoffMaxPercentConfigKey] : undefined,
            defaults.backoffMaxPercent
          )
        : defaults.backoffMaxPercent,
    retryBackoffEnabledExplicit: key === retryDelayConfigKey ? hasBackoffEnabled : false,
    retryBackoffMaxPercentExplicit: key === retryDelayConfigKey ? hasBackoffMaxPercent : false,
    retryDelayInherited: key === retryDelayConfigKey ? retryDelayInherited : false,
    retryDelayInitialValue: key === retryDelayConfigKey ? numberConfigValue(value, 0) : 0,
  };
}

export function shouldWriteRetryDelay(item: RetryConfigFormState, value: number): boolean {
  return !item.retryDelayInherited || value !== item.retryDelayInitialValue;
}

export function writeRetryBackoffConfig(
  config: Record<string, number | string | boolean>,
  item: RetryConfigFormState,
  maxPercent: number
) {
  if (item.retryBackoffEnabledExplicit) {
    config[retryBackoffEnabledConfigKey] = Boolean(item.retryBackoffEnabled);
  }
  if (item.retryBackoffMaxPercentExplicit) {
    config[retryBackoffMaxPercentConfigKey] = Math.trunc(maxPercent);
  }
}
