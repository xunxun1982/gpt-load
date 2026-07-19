import assert from "node:assert/strict";
import { Buffer } from "node:buffer";
import { existsSync, readFileSync } from "node:fs";
import { createRequire } from "node:module";
import test from "node:test";
import { pathToFileURL, URL } from "node:url";
import ts from "typescript";

const require = createRequire(import.meta.url);

const panel = readFileSync(
  new URL("../src/features/site-management/components/SiteManagementPanel.vue", import.meta.url),
  "utf8"
);
const autoCheckinComposableUrl = new URL(
  "../src/features/site-management/composables/useAutoCheckinStatus.ts",
  import.meta.url
);
const autoCheckinTimeUtilsUrl = new URL(
  "../src/features/site-management/utils/checkin-time.ts",
  import.meta.url
);
const autoCheckinComposable = existsSync(autoCheckinComposableUrl)
  ? readFileSync(autoCheckinComposableUrl, "utf8")
  : "";
const zhSiteLocale = readFileSync(
  new URL("../src/locales/site-management/zh-CN.ts", import.meta.url),
  "utf8"
);
const enSiteLocale = readFileSync(
  new URL("../src/locales/site-management/en-US.ts", import.meta.url),
  "utf8"
);
const jaSiteLocale = readFileSync(
  new URL("../src/locales/site-management/ja-JP.ts", import.meta.url),
  "utf8"
);
const zhRootLocale = readFileSync(new URL("../src/locales/zh-CN.ts", import.meta.url), "utf8");
const enRootLocale = readFileSync(new URL("../src/locales/en-US.ts", import.meta.url), "utf8");
const jaRootLocale = readFileSync(new URL("../src/locales/ja-JP.ts", import.meta.url), "utf8");
const siteManagementApi = readFileSync(
  new URL("../src/api/site-management.ts", import.meta.url),
  "utf8"
);

async function loadAutoCheckinTimeUtils() {
  const source = readFileSync(autoCheckinTimeUtilsUrl, "utf8");
  const { outputText } = ts.transpileModule(source, {
    compilerOptions: {
      module: ts.ModuleKind.ES2022,
      target: ts.ScriptTarget.ES2022,
    },
  });
  return import(`data:text/javascript;base64,${Buffer.from(outputText).toString("base64")}`);
}

async function loadAutoCheckinStatusComposable(apiMock) {
  const timeUtilsSource = readFileSync(autoCheckinTimeUtilsUrl, "utf8");
  const timeUtilsOutput = ts.transpileModule(timeUtilsSource, {
    compilerOptions: {
      module: ts.ModuleKind.ES2022,
      target: ts.ScriptTarget.ES2022,
    },
  }).outputText;
  const timeUtilsUrl = `data:text/javascript;base64,${Buffer.from(timeUtilsOutput).toString("base64")}`;

  const vueUrl = pathToFileURL(require.resolve("vue")).href;
  const vueMockUrl = `data:text/javascript;base64,${Buffer.from(
    `export { computed, ref } from ${JSON.stringify(vueUrl)}; export function onUnmounted() {}`
  ).toString("base64")}`;
  globalThis.__autoCheckinApiMock = apiMock;
  const apiMockUrl = `data:text/javascript;base64,${Buffer.from(
    `export const autoCheckinApi = new Proxy({}, { get: (_target, prop) => globalThis.__autoCheckinApiMock[prop] });`
  ).toString("base64")}`;

  const source = readFileSync(autoCheckinComposableUrl, "utf8")
    .replace(/from\s+["']@\/api\/site-management["'];?/g, `from ${JSON.stringify(apiMockUrl)};`)
    .replace(
      /from\s+["']@\/features\/site-management\/utils\/checkin-time["'];?/g,
      `from ${JSON.stringify(timeUtilsUrl)};`
    )
    .replace(/from\s+["']vue["'];?/g, `from ${JSON.stringify(vueMockUrl)};`);
  const output = ts.transpileModule(source, {
    compilerOptions: {
      module: ts.ModuleKind.ES2022,
      target: ts.ScriptTarget.ES2022,
    },
  }).outputText;
  return import(`data:text/javascript;base64,${Buffer.from(output).toString("base64")}`);
}

test("Sub2API access token auth has a separate refresh token input", () => {
  assert.match(panel, /refresh_token:\s*""/);
  assert.match(panel, /authValueInputs\.refresh_token\s*=\s*""/);
  assert.match(
    panel,
    /siteForm\.site_type === 'sub2api' && siteForm\.auth_type\.includes\('access_token'\)/
  );
  assert.match(panel, /siteManagement\.sub2ApiRefreshToken/);
  assert.match(panel, /v-model:value="authValueInputs\.refresh_token"/);
});

test("Sub2API submit serializes refresh_token with access token", () => {
  assert.match(
    panel,
    /siteForm\.site_type === "sub2api" && siteForm\.auth_type\.includes\("access_token"\)/
  );
  assert.match(panel, /const refreshToken = authValueInputs\.refresh_token\.trim\(\)/);
  assert.match(panel, /authValues\.refresh_token = refreshToken/);
  assert.match(panel, /const isSub2APIAccessTokenAuth =/);
  assert.match(
    panel,
    /siteForm\.auth_type\.length === 1 &&\s+!authValues\.refresh_token &&\s+!isSub2APIAccessTokenAuth/
  );
});

test("Sub2API locale hints tell users where auth_token and refresh_token come from", () => {
  for (const locale of [zhSiteLocale, enSiteLocale, jaSiteLocale]) {
    assert.match(locale, /auth_token/);
    assert.match(locale, /refresh_token/);
  }
});

test("Sub2API hints distinguish verified v1 balance from optional compatible check-in", () => {
  assert.match(zhSiteLocale, /\/api\/v1\/auth\/me/);
  assert.match(zhSiteLocale, /无内置签到/);
  assert.match(enSiteLocale, /\/api\/v1\/auth\/me/);
  assert.match(enSiteLocale, /no built-in check-in/i);
  assert.match(jaSiteLocale, /\/api\/v1\/auth\/me/);
  assert.match(jaSiteLocale, /内蔵チェックイン.*ありません/);
});

test("Sub2API custom check-in field does not suggest a generic built-in endpoint", () => {
  assert.match(panel, /siteManagement\.sub2ApiCustomCheckinUrlPlaceholder/);
  assert.match(panel, /siteManagement\.sub2ApiCustomCheckinHint/);
  for (const locale of [zhSiteLocale, enSiteLocale, jaSiteLocale]) {
    assert.match(locale, /sub2ApiCustomCheckinUrlPlaceholder:/);
    assert.match(locale, /sub2ApiCustomCheckinHint:/);
  }
});

test("site-specific auth options match Sub2API and AnyRouter provider contracts", () => {
  assert.match(panel, /const authTypeOptions = computed\(\(\) => \{/);
  assert.match(panel, /case "sub2api":/);
  assert.match(panel, /return \[accessTokenOption\]/);
  assert.match(panel, /case "anyrouter":\s+return \[cookieOption\]/);
});

test("AnyRouter requires exactly one cookie auth value before submit", () => {
  assert.match(
    panel,
    /siteForm\.site_type === "anyrouter"[\s\S]*siteForm\.auth_type\.length !== 1[\s\S]*siteForm\.auth_type\[0\] !== "cookie"/
  );
  assert.match(panel, /backendMsg_anyrouterRequiresCookie/);
});

test("AnyRouter balance capability is exposed by the site table", () => {
  assert.match(panel, /function supportsBalance\(siteType: string\)/);
  assert.match(panel, /"wong-gongyi",\s+"anyrouter"/);
});

test("site auth hints explain AnyRouter user ID and browser-bound cookies", () => {
  assert.match(zhSiteLocale, /AnyRouter[^\n]*Cookie/);
  assert.match(zhSiteLocale, /anyrouterUserIDHint[^\n]*(必须|必填)/);
  assert.match(zhSiteLocale, /浏览器指纹|出口 IP/);
  assert.doesNotMatch(zhSiteLocale, /AnyRouter[^\n]*用户ID留空/);

  assert.match(enSiteLocale, /AnyRouter[^\n]*Cookie/);
  assert.match(enSiteLocale, /User ID[^\n]*(recommended|required)/i);
  assert.match(enSiteLocale, /browser fingerprint|egress IP/i);
  assert.doesNotMatch(enSiteLocale, /AnyRouter[^\n]*Leave User ID empty/);

  assert.match(jaSiteLocale, /AnyRouter[^\n]*Cookie/);
  assert.match(jaSiteLocale, /ユーザーID[^\n]*(推奨|必要)/);
  assert.match(jaSiteLocale, /ブラウザ指紋|送信元IP/);
  assert.doesNotMatch(jaSiteLocale, /AnyRouter[^\n]*ユーザーIDは空欄/);
});

test("provider-specific required fields are validated before submit", () => {
  assert.match(panel, /siteManagement\.anyrouterUserIDRequired/);
  assert.match(panel, /siteManagement\.anyrouterCookieRequired/);
  assert.match(panel, /siteManagement\.sub2ApiCustomCheckinRequired/);
  assert.match(
    panel,
    /siteForm\.site_type === "sub2api"[\s\S]*siteForm\.checkin_enabled[\s\S]*siteForm\.custom_checkin_url\.trim\(\)/
  );
});

test("active Sub2API sites require JWT auth and a token source", () => {
  assert.match(panel, /siteManagement\.sub2ApiAccessTokenRequired/);
  assert.match(panel, /siteManagement\.sub2ApiCredentialRequired/);
  assert.match(
    panel,
    /siteForm\.site_type === "sub2api"[\s\S]*siteForm\.enabled \|\| siteForm\.checkin_enabled/
  );
  for (const locale of [zhSiteLocale, enSiteLocale, jaSiteLocale]) {
    assert.match(locale, /sub2ApiAccessTokenRequired:/);
    assert.match(locale, /sub2ApiCredentialRequired:/);
  }
});

test("provider guidance is placed next to site type, user ID, bypass, and auth fields", () => {
  for (const key of ["siteCapabilityHintKey", "siteUserIDHintKey", "bypassHintKey"]) {
    assert.match(panel, new RegExp(`const ${key} = computed`));
    assert.match(panel, new RegExp(`t\\(${key}\\)`));
  }
  assert.match(panel, /class="field-stack"/);
  assert.match(panel, /class="field-hint provider-field-hint"/);
  assert.match(panel, /const bypassMethodOptions = computed/);
  assert.match(panel, /function updateSiteType\(siteType: ManagedSiteType\)/);
  assert.match(panel, /siteType === "anyrouter"/);
  assert.match(
    panel,
    /siteForm\.auth_type = siteForm\.auth_type\.includes\("cookie"\) \? \["cookie"\] : \[\]/
  );
  assert.match(panel, /@update:value="updateSiteType"/);

  for (const locale of [zhSiteLocale, enSiteLocale, jaSiteLocale]) {
    for (const key of [
      "sub2ApiCapabilityHint",
      "anyrouterCapabilityHint",
      "newApiCapabilityHint",
      "capabilitylessHint",
      "sub2ApiUserIDHint",
      "anyrouterUserIDHint",
      "genericUserIDHint",
      "bypassNoneHint",
      "bypassStealthHint",
      "anyrouterStealthHint",
      "sub2ApiStealthHint",
      "sub2ApiRefreshTokenHint",
    ]) {
      assert.match(locale, new RegExp(`${key}:`));
    }
  }
});

test("auto check-in status still updates when config refresh fails", async () => {
  assert.ok(existsSync(autoCheckinComposableUrl));
  assert.ok(existsSync(autoCheckinTimeUtilsUrl));
  assert.match(panel, /useAutoCheckinStatus/);
  assert.doesNotMatch(panel, /function resolveCheckinDayRefreshTarget/);

  const originalWindow = globalThis.window;
  const originalDateNow = Date.now;
  globalThis.window = {
    setTimeout: () => 1,
    clearTimeout: () => {},
  };
  Date.now = () => Date.parse("2026-06-29T03:30:00.000Z");
  try {
    const { useAutoCheckinStatus } = await loadAutoCheckinStatusComposable({
      getConfig: async () => {
        throw new Error("config failed");
      },
      getStatus: async () => ({
        is_running: false,
        current_checkin_day: "2026-06-28",
        timezone: "America/New_York",
        next_checkin_reset_at: "2026-06-29T04:00:00.000Z",
        pending_retry: false,
      }),
    });
    const state = useAutoCheckinStatus({
      statusTimeLocale: { value: "en-US" },
      t: key => key,
      refreshSites: async () => {},
    });

    await state.loadAutoCheckinConfig();

    assert.equal(state.autoCheckinConfig.value, null);
    assert.equal(state.autoCheckinStatus.value.current_checkin_day, "2026-06-28");
    assert.equal(state.currentCheckinDay.value, "2026-06-28");
  } finally {
    Date.now = originalDateNow;
    globalThis.window = originalWindow;
  }
});

test("auto check-in fallback day uses a reactive clock at scheduled refresh", async () => {
  let scheduledCallback;
  const originalWindow = globalThis.window;
  const originalDateNow = Date.now;
  globalThis.window = {
    setTimeout: callback => {
      scheduledCallback = callback;
      return 1;
    },
    clearTimeout: () => {},
  };
  Date.now = () => Date.parse("2026-06-29T15:59:00.000Z");
  try {
    const { useAutoCheckinStatus } = await loadAutoCheckinStatusComposable({
      getConfig: async () => ({ enabled: true }),
      getStatus: async () => ({
        is_running: false,
        current_checkin_day: "2026-06-29",
        timezone: "Asia/Shanghai",
        next_checkin_reset_at: "2026-06-29T16:00:00.000Z",
        pending_retry: false,
      }),
    });
    const state = useAutoCheckinStatus({
      statusTimeLocale: { value: "zh-CN" },
      t: key => key,
      refreshSites: async () => {},
    });

    await state.loadAutoCheckinConfig();
    assert.equal(state.currentCheckinDay.value, "2026-06-29");

    Date.now = () => Date.parse("2026-06-29T16:00:00.000Z");
    scheduledCallback();

    assert.equal(state.currentCheckinDay.value, "2026-06-30");
    await new Promise(resolve => globalThis.setTimeout(resolve, 0));
  } finally {
    Date.now = originalDateNow;
    globalThis.window = originalWindow;
  }
});

test("auto check-in status time uses active i18n locale", () => {
  assert.match(panel, /const \{ t,\s*locale \} = useI18n\(\)/);
  assert.match(panel, /const statusTimeLocale = computed\(\(\) => locale\.value \|\| undefined\)/);
  assert.match(autoCheckinComposable, /resolveServerTimezone/);
  assert.match(
    autoCheckinComposable,
    /\$\{utcDate\.toLocaleString\(statusTimeLocale\.value, \{ timeZone: timezone \}\)\} \(\$\{timezone\}\)/
  );
  assert.doesNotMatch(autoCheckinComposable, /toLocaleString\("zh-CN"/);
});

test("auto check-in status time falls back to the server timezone default", async () => {
  const { useAutoCheckinStatus } = await loadAutoCheckinStatusComposable({
    getConfig: async () => ({ enabled: true }),
    getStatus: async () => ({
      is_running: false,
      next_scheduled_at: "2026-06-29T03:30:00.000Z",
      pending_retry: false,
    }),
  });
  const originalWindow = globalThis.window;
  globalThis.window = {
    setTimeout: () => 1,
    clearTimeout: () => {},
  };
  try {
    const state = useAutoCheckinStatus({
      statusTimeLocale: { value: "en-US" },
      t: key => key,
      refreshSites: async () => {},
    });

    await state.loadAutoCheckinConfig();

    assert.match(state.nextScheduledDisplay.value, /\(Asia\/Shanghai\)$/);
  } finally {
    globalThis.window = originalWindow;
  }
});

test("auto check-in timezone note distinguishes unset and invalid TZ", () => {
  assert.match(zhRootLocale, /TZ 未设置时使用服务端本地时区/);
  assert.match(zhRootLocale, /TZ 无效时回退北京时间/);
  assert.match(enRootLocale, /if TZ is unset, they use the server local timezone/);
  assert.match(enRootLocale, /if invalid, they fall back to Beijing time/);
  assert.match(jaRootLocale, /TZ 未設定時はサーバーのローカルタイムゾーン/);
  assert.match(jaRootLocale, /無効時は北京時間/);
});

test("auto check-in fallback day boundaries use the server timezone", async () => {
  const { formatServerCheckinDay, resolveCheckinDayRefreshTarget } =
    await loadAutoCheckinTimeUtils();
  const now = Date.parse("2026-06-29T03:30:00.000Z");

  assert.equal(formatServerCheckinDay(now, "America/New_York"), "2026-06-28");
  assert.equal(formatServerCheckinDay(now, undefined), "2026-06-29");
  assert.equal(formatServerCheckinDay(now, "Invalid/Timezone"), "2026-06-29");

  assert.equal(
    resolveCheckinDayRefreshTarget(
      {
        timezone: "America/New_York",
        next_checkin_reset_at: "2026-06-29T01:00:00.000Z",
      },
      now
    ).toISOString(),
    "2026-06-29T04:00:00.000Z"
  );
  assert.equal(resolveCheckinDayRefreshTarget(null, now).toISOString(), "2026-06-29T16:00:00.000Z");
  assert.equal(
    resolveCheckinDayRefreshTarget({ timezone: "Invalid/Timezone" }, now).toISOString(),
    "2026-06-29T16:00:00.000Z"
  );

  const dstNow = Date.parse("2026-03-08T06:30:00.000Z");
  assert.equal(formatServerCheckinDay(dstNow, "America/New_York"), "2026-03-08");
  assert.equal(
    resolveCheckinDayRefreshTarget({ timezone: "America/New_York" }, dstNow).toISOString(),
    "2026-03-09T04:00:00.000Z"
  );
});

test("auto check-in status DTO exposes attempts metadata", () => {
  const statusInterface = siteManagementApi.match(
    /export interface AutoCheckinStatus \{[\s\S]*?\n\}/
  )?.[0];

  assert.ok(statusInterface);
  assert.match(statusInterface, /attempts\?:\s*AutoCheckinAttemptsTracker/);
});
