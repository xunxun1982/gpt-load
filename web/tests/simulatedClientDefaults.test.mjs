import assert from "node:assert/strict";
import { Buffer } from "node:buffer";
import { readFileSync } from "node:fs";
import test from "node:test";
import { URL } from "node:url";
import ts from "typescript";

const groupFormModal = readFileSync(
  new URL("../src/components/keys/GroupFormModal.vue", import.meta.url),
  "utf8"
);
const zhLocale = readFileSync(new URL("../src/locales/zh-CN.ts", import.meta.url), "utf8");
const enLocale = readFileSync(new URL("../src/locales/en-US.ts", import.meta.url), "utf8");
const jaLocale = readFileSync(new URL("../src/locales/ja-JP.ts", import.meta.url), "utf8");

async function loadSimulatedClientDefaults() {
  const source = readFileSync(
    new URL("../src/utils/simulated-client-defaults.ts", import.meta.url),
    "utf8"
  );
  const { outputText } = ts.transpileModule(source, {
    compilerOptions: {
      module: ts.ModuleKind.ES2022,
      target: ts.ScriptTarget.ES2022,
    },
  });
  return import(`data:text/javascript;base64,${Buffer.from(outputText).toString("base64")}`);
}

test("simulated client defaults stay aligned with the pinned UI defaults", async () => {
  const { DEFAULT_CODEX_VERSION, DEFAULT_CLAUDE_CODE_VERSION } =
    await loadSimulatedClientDefaults();

  assert.match(DEFAULT_CODEX_VERSION, /^\d+\.\d+\.\d+$/);
  assert.match(DEFAULT_CLAUDE_CODE_VERSION, /^\d+\.\d+\.\d+$/);
  assert.match(groupFormModal, /from "@\/utils\/simulated-client-defaults"/);
  assert.doesNotMatch(groupFormModal, /const DEFAULT_CODEX_VERSION = "[^"]+"/);
  assert.doesNotMatch(groupFormModal, /const DEFAULT_CLAUDE_CODE_VERSION = "[^"]+"/);

  for (const locale of [zhLocale, enLocale, jaLocale]) {
    assert.match(locale, /simulatedCodexVersionPlaceholder:\s*`[^`]*\$\{DEFAULT_CODEX_VERSION\}/);
    assert.match(
      locale,
      /simulatedClaudeCodeVersionPlaceholder:\s*`[^`]*\$\{DEFAULT_CLAUDE_CODE_VERSION\}/
    );
    assert.doesNotMatch(locale, new RegExp(DEFAULT_CODEX_VERSION.replaceAll(".", "\\.")));
    assert.doesNotMatch(locale, new RegExp(DEFAULT_CLAUDE_CODE_VERSION.replaceAll(".", "\\.")));
  }
});
