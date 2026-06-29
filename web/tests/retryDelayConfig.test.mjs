import assert from "node:assert/strict";
import { Buffer } from "node:buffer";
import { readFileSync } from "node:fs";
import test from "node:test";
import { URL } from "node:url";
import ts from "typescript";

async function loadTsModule(path) {
  const source = readFileSync(new URL(path, import.meta.url), "utf8");
  const { outputText } = ts.transpileModule(source, {
    compilerOptions: {
      module: ts.ModuleKind.ES2022,
      target: ts.ScriptTarget.ES2022,
    },
  });
  return import(`data:text/javascript;base64,${Buffer.from(outputText).toString("base64")}`);
}

// DOM mounting would require adding a test DOM dependency; these tests exercise
// the runtime helpers used by the components instead of pinning source strings.
test("retry delay backoff state preserves inherited delay and hidden sibling keys", async () => {
  const {
    buildRetryConfigState,
    retryBackoffEnabledConfigKey,
    retryBackoffMaxPercentConfigKey,
    retryDelayConfigKey,
    shouldWriteRetryDelay,
    writeRetryBackoffConfig,
  } = await loadTsModule("../src/components/keys/retry-config.ts");

  const backoffOnly = buildRetryConfigState(
    retryDelayConfigKey,
    null,
    { [retryBackoffEnabledConfigKey]: true },
    { backoffEnabled: false, backoffMaxPercent: 500 },
    true
  );

  assert.equal(backoffOnly.value, null);
  assert.equal(backoffOnly.retryDelayInherited, true);
  assert.equal(backoffOnly.retryBackoffEnabled, true);
  assert.equal(backoffOnly.retryBackoffEnabledExplicit, true);
  assert.equal(backoffOnly.retryBackoffMaxPercent, 500);
  assert.equal(backoffOnly.retryBackoffMaxPercentExplicit, false);
  assert.equal(shouldWriteRetryDelay(backoffOnly, 0), false);

  const config = {};
  writeRetryBackoffConfig(config, backoffOnly, backoffOnly.retryBackoffMaxPercent);
  assert.deepEqual(config, { [retryBackoffEnabledConfigKey]: true });

  backoffOnly.retryBackoffMaxPercent = 300;
  backoffOnly.retryBackoffMaxPercentExplicit = true;
  writeRetryBackoffConfig(config, backoffOnly, backoffOnly.retryBackoffMaxPercent);
  assert.deepEqual(config, {
    [retryBackoffEnabledConfigKey]: true,
    [retryBackoffMaxPercentConfigKey]: 300,
  });
});

test("retry delay explicit row does not materialize inherited backoff siblings", async () => {
  const {
    buildRetryConfigState,
    retryBackoffEnabledConfigKey,
    retryBackoffMaxPercentConfigKey,
    retryDelayConfigKey,
    shouldWriteRetryDelay,
    writeRetryBackoffConfig,
  } = await loadTsModule("../src/components/keys/retry-config.ts");

  const retryDelay = buildRetryConfigState(
    retryDelayConfigKey,
    250,
    { [retryDelayConfigKey]: 250 },
    { backoffEnabled: false, backoffMaxPercent: 500 },
    false
  );
  const config = {};

  assert.equal(shouldWriteRetryDelay(retryDelay, 250), true);
  writeRetryBackoffConfig(config, retryDelay, retryDelay.retryBackoffMaxPercent);

  assert.equal(retryDelay.retryBackoffEnabledExplicit, false);
  assert.equal(retryDelay.retryBackoffMaxPercentExplicit, false);
  assert.equal(Object.hasOwn(config, retryBackoffEnabledConfigKey), false);
  assert.equal(Object.hasOwn(config, retryBackoffMaxPercentConfigKey), false);
});

test("settings payload omits nullable clearable numbers and normalizes empty proxy", async () => {
  const { buildSettingsUpdatePayload } = await loadTsModule("../src/views/settings-payload.ts");
  const categories = [
    {
      category_name: "test",
      settings: [
        { key: "retry_delay_ms", type: "int" },
        { key: "retry_backoff_max_percent", type: "int" },
        { key: "retry_backoff_enabled", type: "bool" },
        { key: "proxy_url", type: "string" },
      ],
    },
  ];

  const payload = buildSettingsUpdatePayload(categories, {
    retry_delay_ms: null,
    retry_backoff_max_percent: null,
    retry_backoff_enabled: false,
    proxy_url: null,
  });

  assert.deepEqual(payload, {
    retry_backoff_enabled: false,
    proxy_url: "",
  });
});
