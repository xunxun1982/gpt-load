import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import test from "node:test";

const modal = readFileSync("src/components/keys/GroupFormModal.vue", "utf8");
const zhLocale = readFileSync("src/locales/zh-CN.ts", "utf8");
const enLocale = readFileSync("src/locales/en-US.ts", "utf8");
const jaLocale = readFileSync("src/locales/ja-JP.ts", "utf8");

test("model redirect notice only appears when redirect rules are configured", () => {
  assert.match(modal, /const hasModelRedirectRulesConfigured = computed/);
  assert.match(modal, /formData\.model_redirect_items_v2\.some/);
  assert.match(modal, /modelRedirectJsonStr\.value\.trim\(\)/);
  assert.match(modal, /v-if="hasModelRedirectRulesConfigured"/);
  assert.match(modal, /t\("keys\.modelRedirectBehaviorNotice"\)/);
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
