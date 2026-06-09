import { describe, expect, it } from "vitest";

import {
  KEEP_AWAY_DEFAULTS,
  KEEP_AWAY_FLOOR_MS,
  decideKeepAwayRate,
  keepAwareTargetMs,
  maxPlayableKeepAwayMs,
  type KeepAwayConfig,
} from "../src/core/delivery/keep-away-rate-controller";

// target 2000ms, deadband 500 → band [1500, 2500], rebuild 0.99, catchUp 1.01
const cfg: KeepAwayConfig = KEEP_AWAY_DEFAULTS;

describe("decideKeepAwayRate", () => {
  it("holds at normal rate inside the deadband", () => {
    expect(decideKeepAwayRate({ currentLatencyMs: 2000, currentRate: 1.0 }, cfg)).toEqual({
      kind: "hold",
    });
    expect(decideKeepAwayRate({ currentLatencyMs: 1600, currentRate: 1.0 }, cfg)).toEqual({
      kind: "hold",
    });
    expect(decideKeepAwayRate({ currentLatencyMs: 2400, currentRate: 1.0 }, cfg)).toEqual({
      kind: "hold",
    });
  });

  it("speeds up when latency exceeds the upper band (too far behind)", () => {
    expect(decideKeepAwayRate({ currentLatencyMs: 3000, currentRate: 1.0 }, cfg)).toEqual({
      kind: "set_rate",
      rate: 1.01,
    });
  });

  it("slows down when latency drops below the lower band (too close to edge)", () => {
    expect(decideKeepAwayRate({ currentLatencyMs: 1000, currentRate: 1.0 }, cfg)).toEqual({
      kind: "set_rate",
      rate: 0.99,
    });
  });

  it("holds the catch-up rate until latency crosses back through the setpoint (hysteresis)", () => {
    // Already catching up; still above target but back inside deadband → keep speeding.
    expect(decideKeepAwayRate({ currentLatencyMs: 2300, currentRate: 1.01 }, cfg)).toEqual({
      kind: "hold",
    });
    // Crossed the setpoint → normalize.
    expect(decideKeepAwayRate({ currentLatencyMs: 1900, currentRate: 1.01 }, cfg)).toEqual({
      kind: "set_rate",
      rate: 1.0,
    });
  });

  it("holds the rebuild rate until latency rises back through the setpoint (hysteresis)", () => {
    expect(decideKeepAwayRate({ currentLatencyMs: 1700, currentRate: 0.99 }, cfg)).toEqual({
      kind: "hold",
    });
    expect(decideKeepAwayRate({ currentLatencyMs: 2100, currentRate: 0.99 }, cfg)).toEqual({
      kind: "set_rate",
      rate: 1.0,
    });
  });

  it("holds on non-finite latency", () => {
    expect(decideKeepAwayRate({ currentLatencyMs: NaN, currentRate: 1.0 }, cfg)).toEqual({
      kind: "hold",
    });
    expect(decideKeepAwayRate({ currentLatencyMs: Infinity, currentRate: 1.01 }, cfg)).toEqual({
      kind: "hold",
    });
  });
});

describe("keepAwareTargetMs", () => {
  it("floors low/zero/unknown keepaway", () => {
    expect(keepAwareTargetMs(0)).toBe(KEEP_AWAY_FLOOR_MS);
    expect(keepAwareTargetMs(undefined)).toBe(KEEP_AWAY_FLOOR_MS);
    expect(keepAwareTargetMs(34)).toBe(KEEP_AWAY_FLOOR_MS); // 34*1.5+500=551 < floor
  });

  it("scales above the floor for high keepaway", () => {
    // 1500*1.5 + 500 = 2750
    expect(keepAwareTargetMs(1500)).toBe(2750);
    expect(keepAwareTargetMs(1500)).toBeGreaterThan(keepAwareTargetMs(34));
  });
});

describe("maxPlayableKeepAwayMs", () => {
  const tracks = {
    video_1: { type: "video", jitter: 13 },
    audio_2: { type: "audio", jitter: 34 },
    meta_0: { type: "meta", jitter: 120 },
  };

  it("takes the max over A/V tracks and ignores meta tracks", () => {
    expect(maxPlayableKeepAwayMs(tracks, 120)).toBe(34);
  });

  it("falls back to the stream-level keepaway when no A/V jitter present", () => {
    expect(maxPlayableKeepAwayMs({ meta_0: { type: "meta", jitter: 120 } }, 120)).toBe(120);
    expect(maxPlayableKeepAwayMs(undefined, 80)).toBe(80);
    expect(maxPlayableKeepAwayMs(undefined, undefined)).toBe(0);
  });
});
