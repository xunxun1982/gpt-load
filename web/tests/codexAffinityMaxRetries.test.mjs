import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import test from "node:test";
import { URL } from "node:url";

const component = readFileSync(
  new URL("../src/components/keys/AggregateGroupModal.vue", import.meta.url),
  "utf8"
);
const zhLocale = readFileSync(new URL("../src/locales/zh-CN.ts", import.meta.url), "utf8");
const enLocale = readFileSync(new URL("../src/locales/en-US.ts", import.meta.url), "utf8");
const jaLocale = readFileSync(new URL("../src/locales/ja-JP.ts", import.meta.url), "utf8");

test("aggregate modal keeps Codex affinity attempts separate from sub-group retries", () => {
  assert.match(component, /codex_affinity_max_retries\?: number;/);
  assert.match(component, /codex_affinity_max_retries: 5,/);
  assert.match(
    component,
    /const codexAffinityMaxRetries = config\.codex_affinity_max_retries \?\? 5;/
  );
  assert.match(component, /codex_affinity_max_retries: codexAffinityMaxRetries,/);
  assert.match(
    component,
    /config\.codex_affinity_max_retries = formData\.codex_affinity_max_retries \?\? 5;/
  );
});

test("Codex affinity max retries is a conditional 1-500 sub-option", () => {
  assert.match(
    component,
    /v-if="\s*formData\.channel_type === 'openai-response' &&\s*formData\.codex_affinity_enabled\s*"[\s\S]*?:label="t\('keys\.codexAffinityMaxRetries'\)"[\s\S]*?v-model:value="formData\.codex_affinity_max_retries"[\s\S]*?:min="1"[\s\S]*?:max="500"/
  );
  assert.match(component, /t\("keys\.codexAffinityMaxRetriesHint"\)/);
});

test("Codex affinity retry copy is present in all supported locales", () => {
  assert.match(zhLocale, /codexAffinityMaxRetries:\s*"Codex 亲和最大重试"/);
  assert.match(zhLocale, /codexAffinityMaxRetriesHint:[\s\S]*包含首次请求/);
  assert.match(zhLocale, /codexAffinityMaxRetriesHint:[\s\S]*配置的亲和尝试次数耗尽后/);
  assert.match(zhLocale, /subMaxRetriesPlaceholder:\s*\n?\s*"[^"]*亲和降级后/);

  assert.match(enLocale, /codexAffinityMaxRetries:\s*"Codex Affinity Max Retries"/);
  assert.match(enLocale, /codexAffinityMaxRetriesHint:[\s\S]*Includes the first request/);
  assert.match(
    enLocale,
    /codexAffinityMaxRetriesHint:[\s\S]*configured affinity attempt limit is exhausted/
  );
  assert.match(enLocale, /subMaxRetriesPlaceholder:\s*\n?\s*"[^"]*affinity degradation/);

  assert.match(jaLocale, /codexAffinityMaxRetries:\s*"Codex アフィニティ最大リトライ"/);
  assert.match(jaLocale, /codexAffinityMaxRetriesHint:[\s\S]*初回リクエストを含み/);
  assert.match(
    jaLocale,
    /codexAffinityMaxRetriesHint:[\s\S]*設定したアフィニティ試行回数を使い切ると/
  );
  assert.match(jaLocale, /subMaxRetriesPlaceholder:\s*\n?\s*"[^"]*アフィニティ降格後/);
});
