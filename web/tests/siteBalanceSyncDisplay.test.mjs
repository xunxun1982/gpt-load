import assert from "node:assert/strict";
import { Buffer } from "node:buffer";
import { existsSync, readFileSync } from "node:fs";
import test from "node:test";
import { URL } from "node:url";
import ts from "typescript";

function readSource(path) {
  return readFileSync(new URL(path, import.meta.url), "utf8");
}

async function loadTypeScriptModule(source) {
  const { outputText } = ts.transpileModule(source, {
    compilerOptions: {
      module: ts.ModuleKind.ES2022,
      target: ts.ScriptTarget.ES2022,
    },
  });
  return import(`data:text/javascript;base64,${Buffer.from(outputText).toString("base64")}`);
}

async function loadDisplayUtils() {
  return loadTypeScriptModule(readSource("../src/utils/display.ts"));
}

async function loadAppStateUtils() {
  const source = readSource("../src/utils/app-state.ts").replace(
    'import { reactive } from "vue";',
    "const reactive = value => value;"
  );
  return loadTypeScriptModule(source);
}

const siteApi = readSource("../src/api/site-management.ts");
const appState = readSource("../src/utils/app-state.ts");
const keysView = readSource("../src/views/Keys.vue");
const sitePanel = readSource("../src/features/site-management/components/SiteManagementPanel.vue");
const settingsView = readSource("../src/views/Settings.vue");
const managedSiteImportUtilsURL = new URL("../src/utils/managed-site-import.ts", import.meta.url);
const siteManagementEnLocale = readSource("../src/locales/site-management/en-US.ts");
const siteManagementZhLocale = readSource("../src/locales/site-management/zh-CN.ts");
const siteManagementJaLocale = readSource("../src/locales/site-management/ja-JP.ts");
const bindingSelector = readSource("../src/components/keys/SiteBindingSelector.vue");
const subGroupTable = readSource("../src/components/keys/SubGroupTable.vue");
const groupInfoCard = readSource("../src/components/keys/GroupInfoCard.vue");

test("binding site snapshot exposes and stores cached balances", () => {
  assert.match(siteApi, /last_balance:\s*string\s*\|\s*null/);
  assert.match(siteApi, /last_checkin_status:\s*ManagedSiteCheckinStatus/);
  assert.match(siteApi, /listSitesForBinding\(hideMessage = false\)/);
  assert.match(appState, /siteBalances:\s*Record<number, string \| null>/);
  assert.match(appState, /siteCheckinStatuses:\s*Record<number, ManagedSiteCheckinStatus>/);
  assert.match(appState, /export function replaceSiteBalances/);
  assert.match(appState, /export function updateSiteBalance/);
  assert.match(appState, /export function getSiteBalanceRevision/);
});

test("binding site snapshot stores cached check-in status with balances", async () => {
  const { appState: runtimeState, replaceSiteBalances } = await loadAppStateUtils();

  replaceSiteBalances([
    { id: 1, last_balance: "$10.00", last_checkin_status: "failed" },
    { id: 2, last_balance: "0", last_checkin_status: "" },
  ]);

  assert.equal(runtimeState.siteBalances[1], "$10.00");
  assert.equal(runtimeState.siteCheckinStatuses[1], "failed");
  assert.equal(runtimeState.siteCheckinStatuses[2], "");
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

test("site management only pushes authoritative refresh results into shared balances", () => {
  assert.match(
    sitePanel,
    /if \(result\.balance === null\) \{\s*(?:\/\/[^\r\n]*(?:\r?\n|$)\s*)*return;\s*\}/
  );
  assert.match(sitePanel, /if \(!isNaN\(siteId\) && info\.balance !== null\)/);
  assert.match(sitePanel, /updateSiteBalance\(siteId, result\.balance\)/);
  assert.match(sitePanel, /updateSiteBalance\(siteId, info\.balance\)/);
  assert.match(sitePanel, /Object\.prototype\.hasOwnProperty\.call\(balances\.value, site\.id\)/);
});

test("auto balance uses an hourly interval anchored to the site-management timezone", () => {
  assert.match(siteApi, /export interface AutoBalanceConfig/);
  assert.match(siteApi, /global_enabled:\s*boolean/);
  assert.match(siteApi, /interval_hours:\s*number/);
  assert.match(siteApi, /autoBalanceApi/);
  assert.match(siteApi, /\/site-management\/auto-balance\/config/);
  // The modular locale files are merged into siteManagement at runtime; this is the toolbar action key.
  assert.match(
    sitePanel,
    /siteManagement\.autoCheckin[\s\S]*siteManagement\.autoBalance[\s\S]*siteManagement\.refreshBalance/
  );
  for (const locale of [siteManagementEnLocale, siteManagementZhLocale, siteManagementJaLocale]) {
    assert.match(locale, /refreshBalance:\s*/);
    assert.match(locale, /refreshBalanceTooltip:\s*/);
  }
  // balanceRefreshInterval is a separate form label and must not replace the toolbar action assertion.
  assert.match(sitePanel, /siteManagement\.balanceRefreshInterval/);
  assert.match(sitePanel, /v-model:value="autoBalanceConfig\.interval_hours"/);
  assert.match(sitePanel, /:min="1"[\s\S]*:max="24"[\s\S]*:precision="0"/);
  assert.match(sitePanel, /siteManagement\.serverTimezoneNote/);
});

test("managed-site import reloads the imported schedule configuration", () => {
  assert.match(
    sitePanel,
    /siteManagementApi\.importSites\([\s\S]*?Promise\.all\(\[[\s\S]*?loadSites\(\)[\s\S]*?loadAutoCheckinConfig\(\)[\s\S]*?loadAutoBalanceConfig\(\)[\s\S]*?\]\)/
  );
});

test("managed-site import accepts schedule-only payloads without sites", () => {
  assert.match(siteApi, /sites\?:\s*SiteExportInfo\[\]/);
  assert.match(sitePanel, /const parsed: unknown = JSON\.parse\(text\)/);
  assert.match(sitePanel, /const hasSites = Array\.isArray\(data\.sites\)/);
  assert.match(
    sitePanel,
    /const hasScheduleConfig =[\s\S]*?data\.auto_checkin !== undefined[\s\S]*?data\.auto_checkin !== null[\s\S]*?data\.auto_balance !== undefined[\s\S]*?data\.auto_balance !== null/
  );
  assert.match(sitePanel, /data\.sites !== undefined && !hasSites/);
  assert.match(sitePanel, /!hasSiteRows && !hasScheduleConfig/);
  assert.match(sitePanel, /sites:\s*data\.sites \?\? \[\]/);
  assert.match(sitePanel, /siteManagementApi\.importSites\(\s*normalizedData/);
});

test("managed-site balance multiplier is optional in create and import payloads", () => {
  assert.match(siteApi, /balance_multiplier:\s*number/);
  assert.match(siteApi, /balance_multiplier\?:\s*number/);
  const createRequest = /export interface CreateManagedSiteRequest\s*\{([\s\S]*?)\n\}/.exec(
    siteApi
  );
  assert.ok(createRequest);
  assert.match(createRequest[1], /balance_multiplier\?:\s*number;/);
  assert.doesNotMatch(createRequest[1], /balance_multiplier:\s*number;/);
  assert.match(sitePanel, /balance_multiplier:\s*null as number \| null/);
  assert.match(sitePanel, /site\.balance_multiplier \|\| 1/);
  assert.match(sitePanel, /siteForm\.balance_multiplier \?\? 1/);
  assert.match(sitePanel, /Number\.isSafeInteger\(balanceMultiplier\)/);
  assert.doesNotMatch(sitePanel, /Number\.isInteger\(balanceMultiplier\)/);
  assert.match(sitePanel, /v-model:value="siteForm\.balance_multiplier"/);
  assert.match(sitePanel, /:min="1"/);
  assert.match(sitePanel, /:precision="0"/);
  assert.doesNotMatch(sitePanel, /v-model:value="siteForm\.balance_multiplier"[\s\S]{0,160}:max=/);
});

test("managed-site imports reject positive multipliers that JSON cannot preserve", async () => {
  assert.ok(existsSync(managedSiteImportUtilsURL), "shared import precision guard must exist");
  const { hasImpreciseManagedSiteBalanceMultiplier } = await loadTypeScriptModule(
    readFileSync(managedSiteImportUtilsURL, "utf8")
  );

  const imprecise = JSON.parse('{"sites":[{"balance_multiplier":9007199254740993}]}');
  assert.equal(hasImpreciseManagedSiteBalanceMultiplier(imprecise), true);
  assert.equal(
    hasImpreciseManagedSiteBalanceMultiplier({
      sites: [{ balance_multiplier: Number.MAX_SAFE_INTEGER }],
    }),
    false
  );
  assert.equal(
    hasImpreciseManagedSiteBalanceMultiplier({ sites: [{ balance_multiplier: 0 }] }),
    false
  );
  assert.equal(
    hasImpreciseManagedSiteBalanceMultiplier({ sites: [{ balance_multiplier: -1 }] }),
    false
  );
  assert.equal(hasImpreciseManagedSiteBalanceMultiplier({ sites: [{}] }), false);

  assert.match(sitePanel, /hasImpreciseManagedSiteBalanceMultiplier\(data\)/);
  assert.match(settingsView, /hasImpreciseManagedSiteBalanceMultiplier\(data\.managed_sites\)/);
});

test("managed-site modal keeps actions outside the scroll area with responsive sections", () => {
  const formEnd = sitePanel.indexOf("</n-form>", sitePanel.indexOf('class="site-form"'));
  const footer = sitePanel.indexOf("<template #footer>", formEnd);
  assert.ok(formEnd > 0);
  assert.ok(footer > formEnd);
  assert.match(sitePanel, /\.site-form-modal\s*\{\s*width:\s*min\(760px, calc\(100vw - 24px\)\)/s);
  assert.match(sitePanel, /\.form-section\s*\{[^}]*border:/s);
  assert.match(sitePanel, /\.form-section\s*\{[^}]*border-radius:/s);
  assert.match(
    sitePanel,
    /\.site-form-card :deep\(\.n-card-content\)[\s\S]*?\{[^}]*flex:\s*1 1 auto[^}]*min-height:\s*0/s
  );
  assert.doesNotMatch(sitePanel, /\.site-form-card :deep\(\.n-card__content\)/);
  assert.match(sitePanel, /v-model:value="siteForm\.sort"[\s\S]{0,100}style="width:\s*150px"/);
  assert.match(sitePanel, /siteManagement\.balanceMultiplier[\s\S]{0,120}label-width="auto"/);
  assert.match(sitePanel, /\.form-section\s*\{[^}]*margin-bottom:\s*8px[^}]*padding:\s*8px 12px/s);
  assert.match(sitePanel, /\.section-title\s*\{[^}]*margin:\s*0 0 4px 0/s);
  assert.match(
    sitePanel,
    /\.multiplier-field :deep\(\.n-input-number\)\s*\{[^}]*width:\s*180px[^}]*max-width:\s*100%/s
  );
  assert.match(sitePanel, /\.site-form :deep\(\.n-form-item\)\s*\{[^}]*margin-bottom:\s*4px/s);
});

test("key balance display removes upstream currency and unit text", async () => {
  const { formatBalanceValue, parseBalanceValue } = await loadDisplayUtils();

  assert.equal(formatBalanceValue("$12.34"), "12.34");
  assert.equal(formatBalanceValue("¥8.90"), "8.90");
  assert.equal(formatBalanceValue("CNY 1,234.56"), "1,234.56");
  assert.equal(formatBalanceValue("EUR 1.234,56"), "1.234,56");
  assert.equal(formatBalanceValue("- €0.50"), "-0.50");
  assert.equal(formatBalanceValue(null), "-");
  assert.equal(formatBalanceValue("unknown"), "-");
  assert.equal(parseBalanceValue("$12.34"), 12.34);
  assert.equal(parseBalanceValue("CNY 1,234.56"), 1234.56);
  assert.equal(parseBalanceValue("EUR 1.234,56"), 1234.56);
  assert.equal(parseBalanceValue("0.125"), 0.125);
  assert.equal(parseBalanceValue("0,125"), 0.125);
  assert.equal(parseBalanceValue("1,234"), 1234);
  assert.equal(parseBalanceValue("0"), 0);
  assert.equal(parseBalanceValue("- €0.50"), -0.5);
  assert.equal(parseBalanceValue(null), null);
  assert.equal(parseBalanceValue("unknown"), null);
  assert.match(bindingSelector, /formatBalanceValue/);
  assert.match(subGroupTable, /formatSubGroupBalanceValue/);
  assert.match(sitePanel, /formatBalanceValue/);
});

test("subgroup balance follows the binding precedence table", async () => {
  const { formatSubGroupBalanceValue } = await loadDisplayUtils();
  assert.equal(typeof formatSubGroupBalanceValue, "function");

  const parentAndCanonicalGroups = [
    [1, { id: 1, bound_site_id: 101 }],
    [2, { id: 2, parent_group_id: 1, bound_site_id: 202 }],
  ];
  const parentGroups = [
    [1, { id: 1, bound_site_id: 101 }],
    [2, { id: 2, parent_group_id: 1, bound_site_id: null }],
  ];
  const cases = [
    {
      name: "subgroup response direct binding",
      subGroup: { group: { id: 2, parent_group_id: 1, bound_site_id: 303 } },
      groups: parentAndCanonicalGroups,
      balances: { 101: "$1.01", 202: "$2.02", 303: "$3.03" },
      expected: "3.03",
    },
    {
      name: "canonical direct binding",
      subGroup: { group: { id: 2, parent_group_id: 1, bound_site_id: null } },
      groups: parentAndCanonicalGroups,
      balances: { 101: "$1.01", 202: "$2.02" },
      expected: "2.02",
    },
    {
      name: "parent standard group binding",
      subGroup: { group: { id: 2, parent_group_id: 1, bound_site_id: null } },
      groups: parentGroups,
      balances: { 101: "$1.01" },
      expected: "1.01",
    },
    {
      name: "missing parent binding",
      subGroup: { group: { id: 2, parent_group_id: 1, bound_site_id: null } },
      groups: [[2, { id: 2, parent_group_id: 1, bound_site_id: null }]],
      balances: { 101: "$1.01" },
      expected: "-",
    },
    {
      name: "missing parent balance cache",
      subGroup: { group: { id: 2, parent_group_id: 1, bound_site_id: null } },
      groups: parentGroups,
      balances: {},
      expected: "-",
    },
    {
      name: "missing direct balance does not fall back to parent balance",
      subGroup: { group: { id: 2, parent_group_id: 1, bound_site_id: 303 } },
      groups: [
        [1, { id: 1, bound_site_id: 101 }],
        [2, { id: 2, parent_group_id: 1, bound_site_id: null }],
      ],
      balances: { 101: "$1.01" },
      expected: "-",
    },
  ];

  for (const { name, subGroup, groups, balances, expected } of cases) {
    assert.equal(formatSubGroupBalanceValue(subGroup, new Map(groups), balances), expected, name);
  }
});

test("aggregate subgroup balances reuse a dedicated computed display cache", () => {
  assert.match(subGroupTable, /const subGroupBalanceDisplays = computed\(\(\) => \{/);
  assert.match(
    subGroupTable,
    /displays\.set\(\s*subGroup\.group\.id,\s*formatSubGroupBalanceValue\(\s*subGroup,\s*groupById\.value,\s*appState\.siteBalances\s*\)\s*\)/s
  );
  assert.match(subGroupTable, /subGroupBalanceDisplays\.value\.get\(groupId\) \?\? "-"/);
  assert.equal(subGroupTable.match(/formatSubGroupBalanceValue\(/g)?.length, 1);
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

test("aggregate subgroup cards use the shared balance resolver without wrapping names", () => {
  assert.match(subGroupTable, /class="sub-group-balance"/);
  assert.match(subGroupTable, /function getSubGroupBalanceDisplay\(subGroup: SubGroupInfo\)/);
  assert.match(
    subGroupTable,
    /formatSubGroupBalanceValue\(subGroup, groupById\.value, appState\.siteBalances\)/
  );
  assert.match(subGroupTable, /:title="getSubGroupBalanceDisplay\(subGroup\)"/);
  assert.match(subGroupTable, /\{\{ getSubGroupBalanceDisplay\(subGroup\) \}\}/);
  assert.doesNotMatch(subGroupTable, /getSiteBalanceDisplay\(subGroup\.group\.bound_site_id\)/);
  assert.doesNotMatch(
    subGroupTable,
    /\{\{ t\("siteManagement\.balance"\) \}\}\s*\{\{ getSubGroupBalanceDisplay/
  );
  assert.match(subGroupTable, /\.sub-group-balance\s*\{[^}]*white-space:\s*nowrap/s);
  assert.match(subGroupTable, /\.display-name\s*\{[^}]*white-space:\s*nowrap/s);
  assert.match(subGroupTable, /\.group-name\s*\{[^}]*white-space:\s*nowrap/s);
});
