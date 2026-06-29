import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import test from "node:test";
import { URL } from "node:url";

const groupFormModal = readFileSync(
  new URL("../src/components/keys/GroupFormModal.vue", import.meta.url),
  "utf8"
);
const settingsView = readFileSync(new URL("../src/views/Settings.vue", import.meta.url), "utf8");

test("retry delay backoff is configured through one visible option", () => {
  assert.match(groupFormModal, /const retryDelayConfigKey = "retry_delay_ms"/);
  assert.match(groupFormModal, /const retryBackoffEnabledConfigKey = "retry_backoff_enabled"/);
  assert.match(
    groupFormModal,
    /const retryBackoffMaxPercentConfigKey = "retry_backoff_max_percent"/
  );
  assert.match(groupFormModal, /hiddenConfigOptionKeys = new Set\(\[/);
  assert.match(groupFormModal, /retryBackoffEnabledConfigKey,/);
  assert.match(groupFormModal, /retryBackoffMaxPercentConfigKey,/);
  assert.match(groupFormModal, /getConfigOption\(retryBackoffEnabledConfigKey\)\?\.default_value/);
  assert.match(
    groupFormModal,
    /getConfigOption\(retryBackoffMaxPercentConfigKey\)\?\.default_value/
  );
  assert.match(groupFormModal, /v-if="configItem\.key === retryDelayConfigKey"/);
  assert.match(
    groupFormModal,
    /config\[retryBackoffEnabledConfigKey\] = Boolean\(item\.retryBackoffEnabled\)/
  );
  assert.match(
    groupFormModal,
    /config\[retryBackoffMaxPercentConfigKey\] = Math\.trunc\(maxPercent\)/
  );
  assert.doesNotMatch(
    groupFormModal,
    /buildConfigItem\(retryDelayConfigKey,\s*rawConfig\[retryDelayConfigKey\]\s*\?\?\s*0,\s*rawConfig\)/
  );
  assert.match(groupFormModal, /retryDelayInherited/);
  assert.match(
    groupFormModal,
    /Object\.prototype\.hasOwnProperty\.call\(rawConfig,\s*retryBackoffEnabledConfigKey\)/
  );
  assert.match(
    groupFormModal,
    /Object\.prototype\.hasOwnProperty\.call\(rawConfig,\s*retryBackoffMaxPercentConfigKey\)/
  );
  assert.match(
    groupFormModal,
    /if \(!item\.retryDelayInherited \|\| numValue !== item\.retryDelayInitialValue\)/
  );

  assert.match(
    settingsView,
    /hiddenSettingKeys = new Set\(\["retry_backoff_enabled", "retry_backoff_max_percent"\]\)/
  );
  assert.match(settingsView, /item\.key === 'retry_delay_ms'/);
  assert.match(settingsView, /:wrap="true"/);
  assert.doesNotMatch(settingsView, /v-model(?::value)?="[^"]+\bas\b/);
  assert.doesNotMatch(groupFormModal, /v-model(?::value)?="[^"]+\bas\b/);
  assert.match(settingsView, /@update:value="value => setSettingValue\(item\.key, value\)"/);
  assert.match(
    settingsView,
    /@update:value="value => setSettingValue\('retry_backoff_enabled', value\)"/
  );
  assert.match(
    settingsView,
    /@update:value="\s*value => setSettingValue\('retry_backoff_max_percent', value\)\s*"/
  );
  assert.match(
    groupFormModal,
    /@update:value="\s*value => updateConfigItemValue\(configItem, value\)\s*"/
  );
  assert.match(settingsView, /width: 128px/);
  assert.match(groupFormModal, /width: 128px/);
});
