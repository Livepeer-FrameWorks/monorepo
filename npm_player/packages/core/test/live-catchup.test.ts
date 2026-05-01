import { describe, expect, it } from "vitest";

import { normalizeLiveCatchupConfig } from "../src/core/delivery/live-catchup";

describe("normalizeLiveCatchupConfig", () => {
  it("preserves per-caller undefined defaults", () => {
    expect(normalizeLiveCatchupConfig(undefined, { undefinedMeans: "off" }).enabled).toBe(false);
    expect(
      normalizeLiveCatchupConfig(undefined, { undefinedMeans: { thresholdMs: 5000 } })
    ).toMatchObject({ enabled: true, thresholdMs: 5000 });
  });

  it("normalizes boolean, number, and object options", () => {
    const defaults = { undefinedMeans: "off" } as const;

    expect(normalizeLiveCatchupConfig(false, defaults).enabled).toBe(false);
    expect(normalizeLiveCatchupConfig(0, defaults).enabled).toBe(false);
    expect(normalizeLiveCatchupConfig(true, defaults)).toMatchObject({
      enabled: true,
      thresholdMs: 60000,
    });
    expect(normalizeLiveCatchupConfig(12, defaults)).toMatchObject({
      enabled: true,
      thresholdMs: 12000,
    });
    expect(
      normalizeLiveCatchupConfig({ thresholdMs: 3000, requestMs: 4000, cooldownMs: 1000 }, defaults)
    ).toMatchObject({ enabled: true, thresholdMs: 3000, requestMs: 4000, cooldownMs: 1000 });
  });
});
