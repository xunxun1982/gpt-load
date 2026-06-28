import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import test from "node:test";
import { URL } from "node:url";

const groupFormModal = readFileSync(
  new URL("../src/components/keys/GroupFormModal.vue", import.meta.url),
  "utf8"
);
const zhLocale = readFileSync(new URL("../src/locales/zh-CN.ts", import.meta.url), "utf8");
const enLocale = readFileSync(new URL("../src/locales/en-US.ts", import.meta.url), "utf8");
const jaLocale = readFileSync(new URL("../src/locales/ja-JP.ts", import.meta.url), "utf8");

test("simulated client defaults match the latest official CLI versions", () => {
  assert.match(groupFormModal, /const DEFAULT_CODEX_VERSION = "0\.142\.3"/);
  assert.match(groupFormModal, /const DEFAULT_CLAUDE_CODE_VERSION = "2\.1\.195"/);

  for (const locale of [zhLocale, enLocale, jaLocale]) {
    assert.match(locale, /0\.142\.3/);
    assert.match(locale, /2\.1\.195/);
  }
});
