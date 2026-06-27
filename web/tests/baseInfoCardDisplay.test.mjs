import { readFileSync } from "node:fs";
import { test } from "node:test";
import assert from "node:assert/strict";
import { URL } from "node:url";

const component = readFileSync(
  new URL("../src/components/BaseInfoCard.vue", import.meta.url),
  "utf8"
);
const zhLocale = readFileSync(new URL("../src/locales/zh-CN.ts", import.meta.url), "utf8");
const enLocale = readFileSync(new URL("../src/locales/en-US.ts", import.meta.url), "utf8");
const jaLocale = readFileSync(new URL("../src/locales/ja-JP.ts", import.meta.url), "utf8");

test("24h token card renders input, output, and estimated portion as separate rows", () => {
  assert.match(component, /class="token-usage-breakdown"/);
  assert.equal((component.match(/class="token-usage-row/g) ?? []).length, 3);
  assert.doesNotMatch(component, /\s·\s/);
});

test("estimated token copy describes a portion, not an input or output suffix", () => {
  assert.match(zhLocale, /includesEstimated:\s*"其中估算"/);
  assert.match(enLocale, /includesEstimated:\s*"Estimated portion"/);
  assert.match(jaLocale, /includesEstimated:\s*"推定分"/);
});
