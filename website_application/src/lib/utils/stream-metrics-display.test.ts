import { describe, expect, it } from "vitest";

import { streamCurrentViewers, streamResolutionLabel } from "./stream-metrics-display";

describe("stream-metrics-display helpers", () => {
  const baseMetrics = {
    currentViewers: 0,
    qualityTier: null,
    primaryWidth: null,
    primaryHeight: null,
  };

  it("derives viewer count from the metrics edge", () => {
    expect(streamCurrentViewers({ ...baseMetrics, currentViewers: 36 })).toBe(36);
  });

  it("defaults missing viewer metrics to zero", () => {
    expect(streamCurrentViewers(null)).toBe(0);
  });

  it("prefers concrete primary dimensions for resolution", () => {
    expect(
      streamResolutionLabel({
        ...baseMetrics,
        primaryWidth: 1920,
        primaryHeight: 1080,
        qualityTier: "full",
      })
    ).toBe("1920x1080");
  });

  it("falls back to quality tier, then unknown", () => {
    expect(streamResolutionLabel({ ...baseMetrics, qualityTier: "SD JPEG @ 5kbps" })).toBe(
      "SD JPEG @ 5kbps"
    );
    expect(streamResolutionLabel(null)).toBe("Unknown");
  });
});
