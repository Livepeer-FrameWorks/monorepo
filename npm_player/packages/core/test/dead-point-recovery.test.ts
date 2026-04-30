import { describe, expect, it } from "vitest";
import { decideDeadPointRecovery } from "../src/core/mist/dead-point-recovery";

describe("decideDeadPointRecovery", () => {
  it("returns noop for non dead-point pauses", () => {
    expect(decideDeadPointRecovery({ reason: "manual" }, 1)).toEqual({ kind: "noop" });
  });

  it("seeks +1000ms when slowed", () => {
    expect(decideDeadPointRecovery({ reason: "at_dead_point", begin: 5000 }, 0.5)).toEqual({
      kind: "seek_recover",
      seekToMs: 6000,
      resetSpeedToAuto: true,
    });
  });

  it("seeks +5000ms when not slowed", () => {
    expect(decideDeadPointRecovery({ reason: "at_dead_point", begin: 5000 }, "auto")).toEqual({
      kind: "seek_recover",
      seekToMs: 10000,
      resetSpeedToAuto: false,
    });
  });
});
