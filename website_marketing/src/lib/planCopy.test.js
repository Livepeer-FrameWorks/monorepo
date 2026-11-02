import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import { test } from "node:test";

import { multistreamLine } from "./planCopy.js";

const catalog = JSON.parse(
  readFileSync(new URL("../data/pricing-catalog.json", import.meta.url), "utf8")
);
const tier = (id) => catalog.tiers.find((t) => t.id === id);

test("free multistream line names the catalog viewer cap", () => {
  const free = tier("free");
  const line = multistreamLine(free);
  assert.match(line, /viewer slots/);
  assert.ok(line.includes(free.limits.maxConcurrentViewers.toLocaleString("en-US")));
});

test("paid multistream lines carry no viewer-slot caveat", () => {
  for (const id of ["supporter", "developer", "production"]) {
    const line = multistreamLine(tier(id));
    assert.doesNotMatch(line, /viewer/, id);
    assert.match(line, /delivered minutes/, id);
  }
});
