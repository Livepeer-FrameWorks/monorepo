import { test } from "node:test";
import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import { distanceKm, frameClusters, timezoneOrigin } from "./mapFraming.js";

const table = JSON.parse(
  readFileSync(new URL("../data/timezone-coordinates.json", import.meta.url))
);

// The production topology on 2026-11-04: three EU clusters, two US clusters.
const clusters = [
  { clusterId: "regional-eu-primary", latitude: 51.0, longitude: 10.5 },
  { clusterId: "regional-us-primary", latitude: 39.0, longitude: -77.5 },
  { clusterId: "media-eu-1", latitude: 52.2, longitude: 6.9 },
  { clusterId: "media-us-1", latitude: 37.7, longitude: -97.8 },
  { clusterId: "core-central-primary", latitude: 50.4, longitude: 10.4 },
];

test("distanceKm measures great-circle distance", () => {
  const amsterdamToNewYork = distanceKm([52.4, 4.9], [40.7, -74.0]);
  assert.ok(amsterdamToNewYork > 5800 && amsterdamToNewYork < 5900, `${amsterdamToNewYork}`);
});

test("timezoneOrigin reads a zone's own point, an area centre for a link, and nothing for UTC", () => {
  assert.deepEqual(timezoneOrigin("Europe/Amsterdam", table), [52.4, 4.9]);
  const calcutta = timezoneOrigin("Asia/Calcutta", table);
  assert.ok(calcutta && calcutta[1] > 40 && calcutta[1] < 140, `${calcutta}`);
  assert.equal(timezoneOrigin("UTC", table), null);
  assert.equal(timezoneOrigin("Etc/GMT+2", table), null);
  assert.equal(timezoneOrigin("Mars/Olympus", table), null);
  assert.equal(timezoneOrigin(undefined, table), null);
});

test("a European visitor's frame holds the European clusters only", () => {
  const [[west, south], [east, north]] = frameClusters(
    clusters,
    timezoneOrigin("Europe/Amsterdam", table)
  );
  assert.deepEqual([west, south, east, north], [6.9, 50.4, 10.5, 52.2]);
});

test("an American visitor's frame holds the American clusters only", () => {
  const [[west, south], [east, north]] = frameClusters(
    clusters,
    timezoneOrigin("America/Chicago", table)
  );
  assert.deepEqual([west, south, east, north], [-97.8, 37.7, -77.5, 39.0]);
});

test("a visitor far from every cluster sees the nearest ones, not the whole world", () => {
  const [[west], [east]] = frameClusters(clusters, timezoneOrigin("America/Sao_Paulo", table));
  assert.ok(west <= -77.5 && east <= -77.5, `frame ${west}..${east} reaches past the Americas`);
});

test("without a visitor location the densest region is framed", () => {
  const [[west, south], [east, north]] = frameClusters(clusters, null);
  assert.deepEqual([west, south, east, north], [6.9, 50.4, 10.5, 52.2]);
});

test("clusters without a location never shape the frame", () => {
  assert.equal(
    frameClusters(
      [
        { latitude: 0, longitude: 0 },
        { latitude: "x", longitude: 1 },
      ],
      null
    ),
    null
  );
  assert.equal(frameClusters([], [52.4, 4.9]), null);
});
