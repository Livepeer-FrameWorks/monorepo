import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import { test } from "node:test";

import { capacityLines, multistreamLine, videoLine } from "./planCopy.js";

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

test("paid plan cards state no stream or viewer cap and the catalog rewind window", () => {
  const expected = { supporter: "6 hours", developer: "12 hours", production: "24 hours" };
  for (const [id, window] of Object.entries(expected)) {
    assert.deepEqual(capacityLines(tier(id)), [
      "No cap on concurrent streams or viewers",
      `Live rewind (DVR) up to ${window}`,
    ]);
  }
});

test("a capped plan gets no 'no cap' line", () => {
  assert.deepEqual(capacityLines(tier("free")), []);
});

test("video ceiling follows the ladders the catalog tier allows", () => {
  assert.equal(videoLine(tier("free")), "Up to 1080p video");
  for (const id of ["payg", "supporter", "developer", "production"]) {
    assert.equal(videoLine(tier(id)), "Up to 4K video", id);
  }
});
