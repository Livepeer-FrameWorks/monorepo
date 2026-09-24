import { test } from "node:test";
import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import {
  averageHeldGiB,
  estimatePlans,
  gibPerHour,
  heldAtEndGiB,
  ladderMbps,
} from "./pricingEstimate.js";

const catalog = JSON.parse(
  readFileSync(new URL("../data/pricing-catalog.json", import.meta.url), "utf8")
);
const days = catalog.storageHoursPerMonth / 24;
const close = (actual, expected, eps = 1e-6) =>
  assert.ok(Math.abs(actual - expected) < eps, `${actual} !≈ ${expected}`);

const baseInputs = {
  broadcastHours: 0,
  avgViewers: 0,
  concurrentChannels: 1,
  restreamTargets: 0,
  onDemandHours: 0,
  edgeOffloadPercent: 0,
  recordLive: false,
  sourceQuality: "1080p",
  recordingRetentionDays: 30,
  vodHours: 0,
  vodRetentionDays: Infinity,
};

test("storage held reaches retention steady state", () => {
  // 30.4 GiB/month kept 30 days settles at ~30 GiB held.
  const perMonth = days; // 1 GiB/day
  close(averageHeldGiB(perMonth, 30, 12, days), 30);
  close(heldAtEndGiB(perMonth, 30, 12, days), 30);
  // Month 1 ramps from zero: average of a linear ramp capped at 30 days.
  assert.ok(averageHeldGiB(perMonth, 30, 1, days) < 16);
});

test("storage kept forever grows every month", () => {
  const m1 = averageHeldGiB(100, Infinity, 1, days);
  const m12 = averageHeldGiB(100, Infinity, 12, days);
  close(m1, 50);
  close(m12, 1150);
});

test("bitrate conversions", () => {
  close(gibPerHour(8), (8e6 * 3600) / 8 / 2 ** 30);
  close(ladderMbps(720), 0.9 + 1.6 + 3.2);
  close(ladderMbps(2160), 0.9 + 1.6 + 3.2 + 6.5);
});

test("daily creator on Supporter: delivery overage plus GiB-month storage", () => {
  const inputs = {
    ...baseInputs,
    broadcastHours: 60,
    avgViewers: 100,
    recordLive: true,
    recordingRetentionDays: 30,
  };
  const { plans, usage } = estimatePlans(inputs, catalog, 12);
  const supporter = plans.find((p) => p.id === "supporter");
  close(usage.billableMinutes, 360_000);
  close(supporter.delivery, 240_000 * 0.00055);
  // Steady state: 60 h × 6 Mbps recorded per month, each kept 30 days.
  const heldGiB = ((60 * gibPerHour(6)) / days) * 30;
  close(supporter.storage, heldGiB * 0.035);
  // Guard against GiB-hour pricing: storage must stay a few euros here.
  assert.ok(supporter.storage < 10, `storage €${supporter.storage}`);
});

test("free plan is ineligible beyond its caps and never recommended then", () => {
  const inputs = { ...baseInputs, broadcastHours: 10, avgViewers: 500 };
  const { plans, recommendedId } = estimatePlans(inputs, catalog, 1);
  const free = plans.find((p) => p.id === "free");
  assert.equal(free.eligible, false);
  assert.notEqual(recommendedId, "free");
});

test("small workload fits the free plan", () => {
  const inputs = { ...baseInputs, broadcastHours: 4, avgViewers: 20 };
  const { recommendedId } = estimatePlans(inputs, catalog, 1);
  assert.equal(recommendedId, "free");
});

test("edge offload reduces viewer minutes but not restream minutes", () => {
  const inputs = {
    ...baseInputs,
    broadcastHours: 10,
    avgViewers: 100,
    restreamTargets: 2,
    edgeOffloadPercent: 100,
  };
  const { usage } = estimatePlans(inputs, catalog, 1);
  close(usage.billableMinutes, 10 * 60 * 2);
});
