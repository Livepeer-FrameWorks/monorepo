import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import { test } from "node:test";

import {
  compactLabel,
  featureOf,
  roadmapHref,
  showcaseLink,
  showcaseTiers,
  statusTone,
  tierAnchor,
} from "./featureStatus.js";

const status = JSON.parse(readFileSync(new URL("../data/status.json", import.meta.url), "utf8"));

test("showcase groups follow the registry tiers and lead with StreamCrafter", () => {
  const tiers = showcaseTiers(status);
  assert.deepEqual(
    tiers.map((tier) => tier.horizon),
    ["now", "next", "later"]
  );
  assert.equal(tiers[0].features[0].id, "streamcrafter");
  assert.equal(tiers[1].anchor, tierAnchor(status.tiers[1].label));
  for (const tier of tiers) {
    for (const feature of tier.features) {
      assert.equal(feature.horizon, tier.horizon, `${feature.id} sits in the wrong group`);
      assert.ok(feature.showcase?.summary, `${feature.id} has no showcase summary`);
    }
  }
});

test("tier anchors are stable slugs", () => {
  assert.equal(tierAnchor("Ready now"), "ready-now");
  assert.equal(tierAnchor(" Two  words, and more "), "two-words-and-more");
});

test("an unknown feature id fails instead of rendering an undeclared status", () => {
  assert.throws(() => featureOf(status, "no-such-feature"), /no feature "no-such-feature"/);
});

test("compact labels say Available, Expanding or Beta for shipped work and the tier tag otherwise", () => {
  assert.equal(compactLabel(featureOf(status, "streams")), "Available");
  assert.equal(compactLabel(featureOf(status, "pull-streams")), "Expanding");
  assert.equal(
    compactLabel(featureOf(status, "streamcrafter")),
    featureOf(status, "streamcrafter").tag
  );
  assert.equal(compactLabel(featureOf(status, "drm")), featureOf(status, "drm").tag);
});

test("tones separate shipping, beta, next and later", () => {
  assert.equal(statusTone(featureOf(status, "streams")), "green");
  assert.equal(statusTone(featureOf(status, "streamcrafter")), "cyan");
  assert.equal(statusTone(featureOf(status, "drm")), "yellow");
  assert.equal(statusTone(featureOf(status, "sdk-languages")), "neutral");
});

test("links resolve to marketing routes or docs URLs", () => {
  assert.deepEqual(showcaseLink({ label: "Try", site: "marketing", path: "/stream-test" }, "x"), {
    label: "Try",
    to: "/stream-test",
  });
  assert.deepEqual(
    showcaseLink({ label: "Docs", site: "docs", path: "/a" }, "https://d.example/"),
    {
      label: "Docs",
      href: "https://d.example/a",
    }
  );
  assert.equal(
    roadmapHref(featureOf(status, "drm"), "https://d.example"),
    "https://d.example/roadmap#drm"
  );
  assert.equal(roadmapHref(featureOf(status, "streams"), "https://d.example"), null);
});
