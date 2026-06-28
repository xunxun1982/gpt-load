import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import test from "node:test";

const panel = readFileSync(
  "src/features/site-management/components/SiteManagementPanel.vue",
  "utf8"
);
const zhSiteLocale = readFileSync("src/locales/site-management/zh-CN.ts", "utf8");
const enSiteLocale = readFileSync("src/locales/site-management/en-US.ts", "utf8");
const jaSiteLocale = readFileSync("src/locales/site-management/ja-JP.ts", "utf8");

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
  assert.match(panel, /function resolveCheckinDayRefreshTarget/);
  assert.match(
    panel,
    /Number\.isNaN\(resetAt\.getTime\(\)\) \|\| resetAt\.getTime\(\) <= now[\s\S]+nextLocalMidnight\(new Date\(now\)\)/
  );
  assert.match(
    panel,
    /const now = Date\.now\(\);\s+const target = resolveCheckinDayRefreshTarget\(status, now\)/
  );
});

test("auto check-in config load failure retries before the next midnight", () => {
  assert.match(panel, /const CHECKIN_REFRESH_ERROR_RETRY_MS = 5 \* 60 \* 1000/);
  assert.match(
    panel,
    /function scheduleCheckinDayRefresh\(status: AutoCheckinStatus \| null, delayOverride\?: number\)/
  );
  assert.match(panel, /delayOverride \?\?\s+Math\.min/);
  assert.match(
    panel,
    /scheduleCheckinDayRefresh\(autoCheckinStatus\.value, CHECKIN_REFRESH_ERROR_RETRY_MS\)/
  );
});

test("auto check-in status time uses active i18n locale", () => {
  assert.match(panel, /const \{ t,\s*locale \} = useI18n\(\)/);
  assert.match(panel, /const statusTimeLocale = computed\(\(\) => locale\.value \|\| undefined\)/);
  assert.match(
    panel,
    /utcDate\.toLocaleString\(statusTimeLocale\.value, \{ timeZone: timezone \}\)/
  );
  assert.match(panel, /utcDate\.toLocaleString\(statusTimeLocale\.value\)/);
  assert.doesNotMatch(panel, /toLocaleString\("zh-CN"/);
});
