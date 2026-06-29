import assert from "node:assert/strict";
import { Buffer } from "node:buffer";
import { existsSync, readFileSync } from "node:fs";
import test from "node:test";
import { URL } from "node:url";
import ts from "typescript";

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

test("auto check-in refresh falls back when reset metadata is stale", () => {
  assert.ok(existsSync(autoCheckinComposableUrl));
  assert.ok(existsSync(autoCheckinTimeUtilsUrl));
  assert.match(panel, /useAutoCheckinStatus/);
  assert.match(autoCheckinComposable, /resolveCheckinDayRefreshTarget\(status,\s*now\)/);
  assert.doesNotMatch(autoCheckinComposable, /nextLocalMidnight\(new Date\(now\)\)/);
  assert.match(
    autoCheckinComposable,
    /const now = Date\.now\(\);\s+const target = resolveCheckinDayRefreshTarget\(status, now\)/
  );
  assert.doesNotMatch(panel, /function resolveCheckinDayRefreshTarget/);
});

test("auto check-in config load failure retries before the next midnight", () => {
  assert.ok(existsSync(autoCheckinComposableUrl));
  assert.match(autoCheckinComposable, /const CHECKIN_REFRESH_ERROR_RETRY_MS = 5 \* 60 \* 1000/);
  assert.match(
    autoCheckinComposable,
    /function scheduleCheckinDayRefresh\(status: AutoCheckinStatus \| null, delayOverride\?: number\)/
  );
  assert.match(autoCheckinComposable, /delayOverride \?\?\s+Math\.min/);
  assert.match(
    autoCheckinComposable,
    /scheduleCheckinDayRefresh\(autoCheckinStatus\.value, CHECKIN_REFRESH_ERROR_RETRY_MS\)/
  );
  assert.match(autoCheckinComposable, /try\s*\{\s*await refreshSites\(\);\s*\}\s*catch/);
  assert.match(
    autoCheckinComposable,
    /catch\s*\{\s*scheduleCheckinDayRefresh\(autoCheckinStatus\.value, CHECKIN_REFRESH_ERROR_RETRY_MS\);\s*\/\* Retry stale site-list refresh/
  );
  assert.doesNotMatch(panel, /const CHECKIN_REFRESH_ERROR_RETRY_MS = 5 \* 60 \* 1000/);
});

test("auto check-in status time uses active i18n locale", () => {
  assert.match(panel, /const \{ t,\s*locale \} = useI18n\(\)/);
  assert.match(panel, /const statusTimeLocale = computed\(\(\) => locale\.value \|\| undefined\)/);
  assert.match(
    autoCheckinComposable,
    /\$\{utcDate\.toLocaleString\(statusTimeLocale\.value, \{ timeZone: timezone \}\)\} \(\$\{timezone\}\)/
  );
  assert.match(autoCheckinComposable, /utcDate\.toLocaleString\(statusTimeLocale\.value\)/);
  assert.doesNotMatch(autoCheckinComposable, /toLocaleString\("zh-CN"/);
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
    "2026-06-29T04:00:01.000Z"
  );
  assert.equal(resolveCheckinDayRefreshTarget(null, now).toISOString(), "2026-06-29T16:00:01.000Z");
  assert.equal(
    resolveCheckinDayRefreshTarget({ timezone: "Invalid/Timezone" }, now).toISOString(),
    "2026-06-29T16:00:01.000Z"
  );

  const dstNow = Date.parse("2026-03-08T06:30:00.000Z");
  assert.equal(formatServerCheckinDay(dstNow, "America/New_York"), "2026-03-08");
  assert.equal(
    resolveCheckinDayRefreshTarget({ timezone: "America/New_York" }, dstNow).toISOString(),
    "2026-03-09T04:00:01.000Z"
  );
});

test("auto check-in status DTO exposes attempts metadata", () => {
  const statusInterface = siteManagementApi.match(
    /export interface AutoCheckinStatus \{[\s\S]*?\n\}/
  )?.[0];

  assert.ok(statusInterface);
  assert.match(statusInterface, /attempts\?:\s*AutoCheckinAttemptsTracker/);
});
