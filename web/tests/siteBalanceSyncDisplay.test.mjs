import assert from "node:assert/strict";
import { Buffer } from "node:buffer";
import { readFileSync } from "node:fs";
import test from "node:test";
import { URL } from "node:url";
import ts from "typescript";

function readSource(path) {
  return readFileSync(new URL(path, import.meta.url), "utf8");
}

async function loadDisplayUtils() {
  const { outputText } = ts.transpileModule(readSource("../src/utils/display.ts"), {
    compilerOptions: {
      module: ts.ModuleKind.ES2022,
      target: ts.ScriptTarget.ES2022,
    },
  });
  return import(`data:text/javascript;base64,${Buffer.from(outputText).toString("base64")}`);
}

async function loadAppStateUtils() {
  const source = readSource("../src/utils/app-state.ts").replace(
    'import { reactive } from "vue";',
    "const reactive = value => value;"
  );
  const { outputText } = ts.transpileModule(source, {
    compilerOptions: {
      module: ts.ModuleKind.ES2022,
      target: ts.ScriptTarget.ES2022,
    },
  });
  return import(`data:text/javascript;base64,${Buffer.from(outputText).toString("base64")}`);
}

const siteApi = readSource("../src/api/site-management.ts");
const appState = readSource("../src/utils/app-state.ts");
const keysView = readSource("../src/views/Keys.vue");
const sitePanel = readSource("../src/features/site-management/components/SiteManagementPanel.vue");
const bindingSelector = readSource("../src/components/keys/SiteBindingSelector.vue");
const subGroupTable = readSource("../src/components/keys/SubGroupTable.vue");
const groupInfoCard = readSource("../src/components/keys/GroupInfoCard.vue");

test("binding site snapshot exposes and stores cached balances", () => {
  assert.match(siteApi, /last_balance:\s*string/);
  assert.match(siteApi, /listSitesForBinding\(hideMessage = false\)/);
  assert.match(appState, /siteBalances:\s*Record<number, string \| null>/);
  assert.match(appState, /export function replaceSiteBalances/);
  assert.match(appState, /export function updateSiteBalance/);
  assert.match(appState, /export function getSiteBalanceRevision/);
});

test("keys page bounds background balance staleness with a five-minute refresh", () => {
  assert.match(keysView, /siteManagementApi\.listSitesForBinding\(true\)/);
  assert.match(keysView, /replaceSiteBalances/);
  assert.match(keysView, /Promise\.all\(\[loadGroups\(\), loadSiteBalances\(\)\]\)/);
  assert.match(keysView, /SITE_BALANCE_REFRESH_INTERVAL_MS\s*=\s*5\s*\*\s*60\s*\*\s*1000/);
  assert.match(
    keysView,
    /window\.setInterval\(\s*loadSiteBalances,\s*SITE_BALANCE_REFRESH_INTERVAL_MS\s*\)/s
  );
  assert.match(keysView, /clearInterval\(siteBalanceRefreshTimer\)/);
  assert.doesNotMatch(keysView, /30\s*\*\s*1000/);
});

test("background balance refreshes do not overwrite newer local results", () => {
  assert.match(appState, /expectedRevision\?: number/);
  assert.match(appState, /siteBalanceRevision !== expectedRevision/);
  assert.match(keysView, /siteBalanceRefreshPromise/);
  assert.match(keysView, /getSiteBalanceRevision\(\)/);
  assert.match(keysView, /replaceSiteBalances\(sites, refreshRevision\)/);
  assert.doesNotMatch(bindingSelector, /replaceSiteBalances/);
});

test("stale paginated site responses do not overwrite newer balance updates", async () => {
  const {
    appState: runtimeState,
    getSiteBalanceRevision,
    updateSiteBalance,
    updateSiteBalances,
  } = await loadAppStateUtils();

  const requestRevision = getSiteBalanceRevision();
  updateSiteBalance(1, "$20.00");

  assert.equal(updateSiteBalances([{ id: 1, last_balance: "$10.00" }], requestRevision), false);
  assert.equal(runtimeState.siteBalances[1], "$20.00");
  assert.match(sitePanel, /const siteBalanceRevision = getSiteBalanceRevision\(\)/);
  assert.match(sitePanel, /updateSiteBalances\(result\.sites, siteBalanceRevision\)/);
});

test("site management pushes single and batch refresh results into shared balances", () => {
  assert.match(sitePanel, /updateSiteBalance\(siteId, result\.balance\)/);
  assert.match(sitePanel, /updateSiteBalance\(siteId, info\.balance\)/);
  assert.match(sitePanel, /Object\.prototype\.hasOwnProperty\.call\(balances\.value, site\.id\)/);
});

test("key balance display removes upstream currency and unit text", async () => {
  const { formatBalanceValue } = await loadDisplayUtils();

  assert.equal(formatBalanceValue("$12.34"), "12.34");
  assert.equal(formatBalanceValue("¥8.90"), "8.90");
  assert.equal(formatBalanceValue("CNY 1,234.56"), "1,234.56");
  assert.equal(formatBalanceValue("EUR 1.234,56"), "1.234,56");
  assert.equal(formatBalanceValue("- €0.50"), "-0.50");
  assert.equal(formatBalanceValue(null), "-");
  assert.equal(formatBalanceValue("unknown"), "-");
  assert.match(bindingSelector, /formatBalanceValue/);
  assert.match(subGroupTable, /formatBalanceValue/);
  assert.match(sitePanel, /formatBalanceValue/);
});

test("binding selector keeps balance before actions on one compact line", () => {
  const balanceIndex = bindingSelector.indexOf('class="site-balance"');
  const actionIndex = bindingSelector.indexOf("<!-- Bind/Unbind button -->");
  const boundSiteIndex = bindingSelector.indexOf("<!-- Navigate to site button");

  assert.ok(balanceIndex > 0);
  assert.ok(balanceIndex < actionIndex);
  assert.ok(balanceIndex < boundSiteIndex);
  assert.match(bindingSelector, /const balanceDisplay = computed/);
  assert.match(
    bindingSelector,
    /<span class="site-balance"[^>]*>\s*\{\{ balanceDisplay \}\}\s*<\/span>/s
  );
  assert.match(bindingSelector, /class="site-balance" :title="balanceDisplay"/);
  assert.doesNotMatch(
    bindingSelector,
    /\{\{ t\("siteManagement\.balance"\) \}\}\s*\{\{ balanceDisplay \}\}/
  );
  assert.match(bindingSelector, /\.site-balance\s*\{[^}]*white-space:\s*nowrap/s);
  assert.match(bindingSelector, /\.site-binding-selector[^}]*white-space:\s*nowrap/s);
  assert.doesNotMatch(bindingSelector, /min-width:\s*180px/);
  assert.match(bindingSelector, /\.site-select\s*\{[^}]*min-width:\s*80px/s);
  assert.match(bindingSelector, /\.bound-site-tag\s*\{[^}]*max-width:/s);
  assert.match(groupInfoCard, /\.site-binding-in-header\s*\{[^}]*min-width:\s*0/s);
  assert.doesNotMatch(groupInfoCard, /\.site-binding-in-header\s*\{[^}]*flex-shrink:\s*0/s);
});

test("aggregate subgroup cards show a compact top-right balance without wrapping names", () => {
  assert.match(subGroupTable, /class="sub-group-balance"/);
  assert.match(subGroupTable, /getSiteBalanceDisplay\(subGroup\.group\.bound_site_id\)/);
  assert.match(subGroupTable, /:title="getSiteBalanceDisplay\(subGroup\.group\.bound_site_id\)"/);
  assert.doesNotMatch(
    subGroupTable,
    /\{\{ t\("siteManagement\.balance"\) \}\}\s*\{\{ getSiteBalanceDisplay/
  );
  assert.match(subGroupTable, /\.sub-group-balance\s*\{[^}]*white-space:\s*nowrap/s);
  assert.match(subGroupTable, /\.display-name\s*\{[^}]*white-space:\s*nowrap/s);
  assert.match(subGroupTable, /\.group-name\s*\{[^}]*white-space:\s*nowrap/s);
});
