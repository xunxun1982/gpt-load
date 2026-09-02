import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import test from "node:test";
import { URL } from "node:url";

const component = readFileSync(
  new URL("../src/components/logs/LogTable.vue", import.meta.url),
  "utf8"
);

function columnWidth(key) {
  const match = component.match(new RegExp(`key: "${key}",[\\s\\S]*?width: (\\d+),`));
  assert.ok(match, `missing width for ${key}`);
  return Number(match[1]);
}

test("log table keeps primary columns readable after adding first-byte timing", () => {
  assert.equal(columnWidth("group_name"), 160, "aihub_x666_search needs the wider group column");
  assert.equal(columnWidth("status_code"), 110);
  assert.equal(columnWidth("first_byte_duration_ms"), 120);
  assert.equal(columnWidth("is_stream"), 130);
  assert.equal(
    columnWidth("group_name") + columnWidth("status_code") + columnWidth("is_stream"),
    400
  );
});
