import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import test from "node:test";

const panel = readFileSync("src/features/proxy-pool/components/ProxyPoolPanel.vue", "utf8");

test("proxy pool page removes the standalone periodic test switch", () => {
  assert.equal(panel.includes("NSwitch"), false);
  assert.equal(panel.includes("proxy-pool-auto-test"), false);
  assert.equal(panel.includes("handleAutoTestChange"), false);
  assert.equal(panel.includes("autoTesting"), false);
});

test("proxy pool periodic settings live in the test settings modal", () => {
  assert.match(panel, /proxyPoolSettingsForm\.intervalMinutes/);
  assert.match(panel, /proxyPoolSettingsForm\.gatewayIntervalMinutes/);
  assert.match(panel, /proxyPool\.autoTestInterval/);
  assert.match(panel, /saveProxyPoolSettings/);
});

test("manual gateway test refreshes the active gateway state", () => {
  assert.match(panel, /async function testGateway/);
  assert.match(panel, /await loadItems\(\)/);
});
