import type { SettingCategory, SettingsUpdatePayload } from "@/api/settings";

export type SettingFormValue = string | number | boolean | null;

export function buildSettingsUpdatePayload(
  categories: SettingCategory[],
  form: Record<string, SettingFormValue>
): SettingsUpdatePayload {
  const payload: SettingsUpdatePayload = {};
  for (const category of categories) {
    for (const setting of category.settings || []) {
      if (!Object.prototype.hasOwnProperty.call(form, setting.key)) {
        continue;
      }
      const value = form[setting.key];
      if (value === null || value === undefined) {
        // Naive UI clearable select emits null, while the backend expects "" for no proxy.
        if (setting.key === "proxy_url") {
          payload[setting.key] = "";
        }
        continue;
      }
      payload[setting.key] = value;
    }
  }
  return payload;
}
