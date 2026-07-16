import assert from "node:assert/strict";
import { Buffer } from "node:buffer";
import { readFileSync } from "node:fs";
import test from "node:test";
import { URL } from "node:url";
import ts from "typescript";

function readOptionalSource(path) {
  try {
    return readFileSync(new URL(path, import.meta.url), "utf8");
  } catch (error) {
    if (error?.code === "ENOENT") {
      return "";
    }
    throw error;
  }
}

async function loadAutoWeightUtils() {
  const { outputText } = ts.transpileModule(
    readOptionalSource("../src/utils/auto-subgroup-weight.ts"),
    {
      compilerOptions: {
        module: ts.ModuleKind.ES2022,
        target: ts.ScriptTarget.ES2022,
      },
    }
  );
  return import(`data:text/javascript;base64,${Buffer.from(outputText).toString("base64")}`);
}

async function loadDisplayUtils() {
  const { outputText } = ts.transpileModule(readOptionalSource("../src/utils/display.ts"), {
    compilerOptions: {
      module: ts.ModuleKind.ES2022,
      target: ts.ScriptTarget.ES2022,
    },
  });
  return import(`data:text/javascript;base64,${Buffer.from(outputText).toString("base64")}`);
}

const subGroupTable = readOptionalSource("../src/components/keys/SubGroupTable.vue");
const autoWeightModal = readOptionalSource("../src/components/keys/AutoSubGroupWeightModal.vue");
const keysApi = readOptionalSource("../src/api/keys.ts");
const zhLocale = readOptionalSource("../src/locales/zh-CN.ts");
const enLocale = readOptionalSource("../src/locales/en-US.ts");
const jaLocale = readOptionalSource("../src/locales/ja-JP.ts");

test("calculates proportional initial weights with check-in penalty", async () => {
  const { calculateAutoSubGroupWeights } = await loadAutoWeightUtils();
  assert.equal(typeof calculateAutoSubGroupWeights, "function");

  const result = calculateAutoSubGroupWeights(
    [
      { subGroupId: 1, balance: 100, checkinStatus: "success" },
      { subGroupId: 2, balance: 50, checkinStatus: "success" },
      { subGroupId: 3, balance: 20, checkinStatus: "failed" },
      { subGroupId: 4, balance: 20, checkinStatus: "" },
    ],
    100
  );

  assert.deepEqual(result.updates, [
    { subGroupId: 1, weight: 100 },
    { subGroupId: 2, weight: 50 },
    { subGroupId: 3, weight: 14 },
    { subGroupId: 4, weight: 14 },
  ]);
  assert.equal(result.skippedCount, 0);
});

test("resolves subgroup site binding from canonical group and parent group", async () => {
  const { resolveSubGroupSiteId } = await loadDisplayUtils();
  assert.equal(typeof resolveSubGroupSiteId, "function");

  const groupsById = new Map([
    [2, { id: 2, bound_site_id: 22 }],
    [3, { id: 3, parent_group_id: 4 }],
    [4, { id: 4, bound_site_id: 44 }],
  ]);

  assert.equal(resolveSubGroupSiteId({ group: { id: 2 } }, groupsById), 22);
  assert.equal(resolveSubGroupSiteId({ group: { id: 3 } }, groupsById), 44);
});

test("assigns weight one to zero and tiny balances and skips unavailable balances", async () => {
  const { calculateAutoSubGroupWeights } = await loadAutoWeightUtils();
  assert.equal(typeof calculateAutoSubGroupWeights, "function");

  const result = calculateAutoSubGroupWeights(
    [
      { subGroupId: 1, balance: 1000, checkinStatus: "success" },
      { subGroupId: 2, balance: 0, checkinStatus: "failed" },
      { subGroupId: 3, balance: 0.1, checkinStatus: "success" },
      { subGroupId: 4, balance: null, checkinStatus: "" },
      { subGroupId: 5, balance: -1, checkinStatus: "success" },
    ],
    100
  );

  assert.deepEqual(result.updates, [
    { subGroupId: 1, weight: 100 },
    { subGroupId: 2, weight: 1 },
    { subGroupId: 3, weight: 1 },
  ]);
  assert.equal(result.skippedCount, 2);
});

test("assigns weight one when every usable balance is zero", async () => {
  const { calculateAutoSubGroupWeights } = await loadAutoWeightUtils();
  assert.equal(typeof calculateAutoSubGroupWeights, "function");

  const result = calculateAutoSubGroupWeights(
    [
      { subGroupId: 1, balance: 0, checkinStatus: "success" },
      { subGroupId: 2, balance: 0, checkinStatus: "" },
    ],
    500
  );

  assert.deepEqual(result.updates, [
    { subGroupId: 1, weight: 1 },
    { subGroupId: 2, weight: 1 },
  ]);
  assert.equal(result.skippedCount, 0);
});

test("does not penalize skipped or already-checked statuses", async () => {
  const { calculateAutoSubGroupWeights } = await loadAutoWeightUtils();
  const result = calculateAutoSubGroupWeights(
    [
      { subGroupId: 1, balance: 100, checkinStatus: "skipped" },
      { subGroupId: 2, balance: 100, checkinStatus: "already_checked" },
    ],
    100
  );

  assert.deepEqual(result.updates, [
    { subGroupId: 1, weight: 100 },
    { subGroupId: 2, weight: 100 },
  ]);
});

test("places auto weight button after reset-all-health and mounts the modal", () => {
  const resetIndex = subGroupTable.indexOf('t("subGroups.resetAllHealth")');
  const autoIndex = subGroupTable.indexOf('t("subGroups.autoArrangeWeight")');

  assert.ok(resetIndex >= 0);
  assert.ok(autoIndex > resetIndex);
  assert.match(subGroupTable, /<auto-sub-group-weight-modal/);
  assert.match(subGroupTable, /v-model:show="autoWeightModalShow"/);
  assert.match(subGroupTable, /:groups="groups \|\| \[\]"/);
  assert.match(zhLocale, /autoArrangeWeight:\s*"自动分配权重"/);
  assert.match(zhLocale, /计算结果乘以 0\.7/);
  assert.match(enLocale, /calculated weight by 0\.7/);
  assert.match(jaLocale, /計算結果に 0\.7 を掛けます/);
});

test("auto weight modal reads cached data and updates only initial weight serially", () => {
  assert.match(autoWeightModal, /const defaultAutoWeightMax = 1000/);
  assert.match(autoWeightModal, /const maxWeight = ref\(defaultAutoWeightMax\)/);
  assert.match(autoWeightModal, /maxWeight\.value = defaultAutoWeightMax/);
  assert.match(autoWeightModal, /:max="5000"/);
  assert.match(autoWeightModal, /typeof group\.id === "number"/);
  assert.match(autoWeightModal, /resolveSubGroupSiteId\(subGroup, groupById\.value\)/);
  assert.match(autoWeightModal, /parseBalanceValue\(appState\.siteBalances\[siteId\]\)/);
  assert.match(autoWeightModal, /appState\.siteCheckinStatuses\[siteId\]/);
  assert.match(autoWeightModal, /for \(const update of result\.updates\)/);
  assert.match(autoWeightModal, /await keysApi\.updateSubGroupWeight/);
  assert.match(autoWeightModal, /weight:\s*update\.weight/);
  assert.match(autoWeightModal, /true\s*\)/);
  assert.doesNotMatch(autoWeightModal, /effective_weight/);
});

test("auto weight modal stays open when any update fails", () => {
  assert.match(autoWeightModal, /if \(successCount > 0\)/);
  assert.match(
    autoWeightModal,
    /if \(failedCount > 0\) \{[\s\S]*?message\.warning\([\s\S]*?\);[\s\S]*?return;[\s\S]*?\}[\s\S]*?message\.success/
  );
  assert.ok(
    autoWeightModal.lastIndexOf('emit("update:show", false)') >
      autoWeightModal.indexOf("if (failedCount > 0)")
  );
});

test("weight update API can suppress per-item messages", () => {
  assert.match(keysApi, /options:\s*UpdateSubGroupWeightOptions,\s*hideMessage = false\s*\)/s);
  assert.match(keysApi, /\{\s*hideMessage,\s*\}/);
});
