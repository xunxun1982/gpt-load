import assert from "node:assert/strict";
import { Buffer } from "node:buffer";
import { readFileSync } from "node:fs";
import test from "node:test";
import { URL } from "node:url";
import ts from "typescript";

const modal = readFileSync(
  new URL("../src/components/keys/GroupFormModal.vue", import.meta.url),
  "utf8"
);
const zhLocale = readFileSync(new URL("../src/locales/zh-CN.ts", import.meta.url), "utf8");
const enLocale = readFileSync(new URL("../src/locales/en-US.ts", import.meta.url), "utf8");
const jaLocale = readFileSync(new URL("../src/locales/ja-JP.ts", import.meta.url), "utf8");

async function loadModelRedirectUtils() {
  const source = readFileSync(new URL("../src/utils/model-redirect.ts", import.meta.url), "utf8");
  const { outputText } = ts.transpileModule(source, {
    compilerOptions: {
      module: ts.ModuleKind.ES2022,
      target: ts.ScriptTarget.ES2022,
    },
  });
  return import(`data:text/javascript;base64,${Buffer.from(outputText).toString("base64")}`);
}

test("model redirect notice only appears when effective redirect rules are configured", async () => {
  const { hasEffectiveModelRedirectItems, hasEffectiveModelRedirectJson } =
    await loadModelRedirectUtils();

  assert.equal(
    hasEffectiveModelRedirectItems([
      { from: "", targets: [{ model: "", weight: 100, enabled: true }] },
    ]),
    false
  );
  assert.equal(
    hasEffectiveModelRedirectItems([
      { from: "GLM-5.2", targets: [{ model: "glm-5.2-air", weight: 100, enabled: true }] },
    ]),
    true
  );
  assert.equal(hasEffectiveModelRedirectJson("{"), false);
  assert.equal(hasEffectiveModelRedirectJson('{"GLM-5.2":{"targets":[]}}'), false);
  assert.equal(
    hasEffectiveModelRedirectJson('{"GLM-5.2":{"targets":[{"model":"glm-5.2-air"}]}}'),
    true
  );

  assert.match(modal, /const hasModelRedirectRulesConfigured = computed/);
  assert.match(modal, /hasEffectiveModelRedirectJson\(modelRedirectJsonStr\.value\)/);
  assert.match(modal, /hasEffectiveModelRedirectItems\(formData\.model_redirect_items_v2\)/);
  assert.match(modal, /v-if="hasModelRedirectRulesConfigured"/);
  assert.match(modal, /t\("keys\.modelRedirectBehaviorNotice"\)/);
});

test("model redirect serialization merges duplicate source rules", async () => {
  const { modelRedirectItemsV2ToJson, modelRedirectItemsV2ToFormattedJson } =
    await loadModelRedirectUtils();
  const duplicateItems = [
    { from: "GLM-5.2", targets: [{ model: "glm-5.2-air", weight: 100, enabled: true }] },
    { from: " GLM-5.2 ", targets: [{ model: "glm-5.2-pro", weight: 80, enabled: true }] },
  ];

  assert.equal(
    modelRedirectItemsV2ToJson(duplicateItems),
    '{"GLM-5.2":{"targets":[{"model":"glm-5.2-air"},{"model":"glm-5.2-pro","weight":80}]}}'
  );
  assert.match(modelRedirectItemsV2ToFormattedJson(duplicateItems), /glm-5\.2-air/);
  assert.match(modelRedirectItemsV2ToFormattedJson(duplicateItems), /glm-5\.2-pro/);
});

test("model redirect notice copy explains case-sensitive matching and strict mode self mapping", () => {
  assert.match(zhLocale, /modelRedirectBehaviorNotice:/);
  assert.match(zhLocale, /大小写敏感/);
  assert.match(zhLocale, /源模型名/);
  assert.match(zhLocale, /自动转换大小写/);
  assert.match(zhLocale, /大小写归一后重名/);
  assert.match(zhLocale, /只会接管请求 GLM-5\.2/);
  assert.match(zhLocale, /保持原名交给上游处理/);
  assert.match(zhLocale, /宽松模式/);
  assert.match(zhLocale, /严格模式/);
  assert.match(zhLocale, /自映射/);

  assert.match(enLocale, /modelRedirectBehaviorNotice:/);
  assert.match(enLocale, /case-sensitive/);
  assert.match(enLocale, /source model name/);
  assert.match(enLocale, /normalize case/);
  assert.match(enLocale, /collide after case normalization/);
  assert.match(enLocale, /only takes over requests for GLM-5\.2/);
  assert.match(enLocale, /sent upstream unchanged/);
  assert.match(enLocale, /loose mode/);
  assert.match(enLocale, /strict mode/);
  assert.match(enLocale, /self-redirect/);

  assert.match(jaLocale, /modelRedirectBehaviorNotice:/);
  assert.match(jaLocale, /大文字と小文字/);
  assert.match(jaLocale, /ソースモデル名/);
  assert.match(jaLocale, /自動変換/);
  assert.match(jaLocale, /正規化すると衝突/);
  assert.match(jaLocale, /GLM-5\.2 のリクエストだけを引き受けます/);
  assert.match(jaLocale, /元の名前のままアップストリームへ送信/);
  assert.match(jaLocale, /寛容モード/);
  assert.match(jaLocale, /厳格モード/);
  assert.match(jaLocale, /自己リダイレクト/);
});
