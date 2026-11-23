import { test } from "node:test";
import assert from "node:assert/strict";
import { dedupeOrchestratorVantages, orchestratorLocation } from "./orchestratorLocation.js";

test("unknown and invalid locations have no fabricated coordinates", () => {
  for (const location of [
    {},
    { longitude: 4 },
    { latitude: null, longitude: 4 },
    { latitude: "", longitude: 4 },
    { latitude: 0, longitude: 0 },
    { latitude: NaN, longitude: 4 },
    { latitude: Infinity, longitude: 4 },
    { latitude: 91, longitude: 4 },
    { latitude: 52, longitude: -181 },
  ]) {
    assert.equal(orchestratorLocation(location), null);
  }
});

test("equator and prime meridian remain valid coordinates", () => {
  assert.deepEqual(orchestratorLocation({ latitude: 0, longitude: 4 }), [0, 4]);
  assert.deepEqual(orchestratorLocation({ latitude: 52, longitude: 0 }), [52, 0]);
});

test("an endpoint remains located when its fastest vantage has no geography", () => {
  const unknown = {
    orchAddr: "0xorch",
    resolvedIp: "192.0.2.1",
    dialedRecently: true,
    latestLatencyMs: 20,
  };
  const located = { ...unknown, latestLatencyMs: 150, latitude: 52, longitude: 4 };
  for (const vantages of [
    [unknown, located],
    [located, unknown],
  ]) {
    const points = dedupeOrchestratorVantages(vantages);
    assert.equal(points.length, 1);
    assert.deepEqual(orchestratorLocation(points[0]), [52, 4]);
  }
});

test("located vantages retain latency and score preference", () => {
  const slow = {
    orchAddr: "0xorch",
    resolvedIp: "192.0.2.1",
    dialedRecently: true,
    latitude: 52,
    longitude: 4,
    latestLatencyMs: 150,
    score: 1,
  };
  const fast = { ...slow, latestLatencyMs: 20 };
  const best = { ...fast, score: 2 };
  assert.deepEqual(dedupeOrchestratorVantages([slow, fast, best]), [best]);
});
