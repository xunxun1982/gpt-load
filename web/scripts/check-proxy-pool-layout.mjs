import { readFileSync } from "node:fs";

/* global console, process */

const source = readFileSync("src/features/proxy-pool/components/ProxyPoolPanel.vue", "utf8");

const checks = [
  {
    name: "uses a vertical stack container",
    pass: source.includes('class="proxy-pool-content-stack"'),
  },
  {
    name: "renders manual proxies before gateway proxies",
    pass:
      source.indexOf('t("proxyPool.manualProxies")') > -1 &&
      source.indexOf('t("proxyPool.gatewayProxies")') > -1 &&
      source.indexOf('t("proxyPool.manualProxies")') <
        source.indexOf('t("proxyPool.gatewayProxies")'),
  },
  {
    name: "paginates gateway proxy data locally",
    pass:
      source.includes("pagedGatewayOptions") &&
      source.includes("gatewayCurrentPage") &&
      source.includes("gatewayPageSize"),
  },
  {
    name: "renders gateway pagination controls",
    pass: source.includes('class="proxy-pool-gateway-pagination"'),
  },
  {
    name: "does not split dense proxy tables into narrow columns",
    pass:
      !source.includes('class="proxy-pool-content-grid"') &&
      !source.includes("grid-template-columns: minmax(0, 1fr) minmax(360px, 0.9fr);"),
  },
  {
    name: "keeps vertical stack spacing compact",
    pass: /\.proxy-pool-content-stack\s*\{[\s\S]*?gap:\s*8px;/.test(source),
  },
  {
    name: "keeps section spacing compact",
    pass:
      /\.proxy-pool-section\s*\{[\s\S]*?gap:\s*6px;/.test(source) &&
      /\.proxy-pool-section\s*\{[\s\S]*?padding:\s*8px 10px;/.test(source),
  },
  {
    name: "keeps pagination spacing compact",
    pass:
      /\.proxy-pool-pagination\s*\{[\s\S]*?padding-top:\s*6px;/.test(source) &&
      /\.proxy-pool-gateway-pagination\s*\{[\s\S]*?padding-top:\s*6px;/.test(source),
  },
];

const failures = checks.filter(check => !check.pass);

if (failures.length > 0) {
  console.error("Proxy pool layout contract failed:");
  for (const failure of failures) {
    console.error(`- ${failure.name}`);
  }
  process.exit(1);
}

console.warn(`Proxy pool layout contract passed (${checks.length}/${checks.length}).`);
