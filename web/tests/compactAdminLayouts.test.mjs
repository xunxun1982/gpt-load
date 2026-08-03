import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import test from "node:test";
import { URL } from "node:url";

const readSource = relativePath => readFileSync(new URL(relativePath, import.meta.url), "utf8");

test("settings actions remain visible while the page scrolls", () => {
  const source = readSource("../src/views/Settings.vue");

  assert.match(source, /class="settings-actions"/);
  assert.ok(source.indexOf('class="settings-actions"') < source.indexOf('<n-form ref="formRef"'));
  assert.match(source, /\.settings-actions\s*\{[\s\S]*position:\s*sticky;[\s\S]*top:\s*0;/);
});

test("aggregate editor keeps actions visible and uses a compact desktop grid", () => {
  const source = readSource("../src/components/keys/AggregateGroupModal.vue");

  assert.match(source, /class="aggregate-group-card"[\s\S]*size="medium"/);
  assert.match(source, /\.aggregate-group-card\s*\{[\s\S]*max-height:/);
  assert.match(
    source,
    /\.aggregate-group-card\s+:deep\(\.n-card__content\)[\s\S]*overflow-y:\s*auto;/
  );
  assert.match(source, /grid-template-columns:\s*repeat\(2,\s*minmax\(0,\s*1fr\)\)/);
});

test("site editor uses the available desktop width for a compact two-column form", () => {
  const source = readSource("../src/features/site-management/components/SiteManagementPanel.vue");

  assert.match(source, /\.site-form-modal\s*\{[\s\S]*width:\s*min\(1040px,/);
  assert.match(source, /\.site-form-card\s+:deep\(\.n-card__content\)[\s\S]*overflow-y:\s*auto;/);
  assert.match(
    source,
    /@media \(min-width:\s*900px\)[\s\S]*\.site-form\s*\{[\s\S]*grid-template-columns:\s*repeat\(2,/
  );
  assert.match(
    source,
    /\.site-form\s*>\s*\.form-section:last-child\s*\{[\s\S]*display:\s*grid;[\s\S]*grid-template-columns:\s*repeat\(2,/
  );
});
