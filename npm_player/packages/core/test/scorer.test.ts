import { describe, expect, it } from "vitest";

import {
  DEFAULT_TRACK_SCORES,
  calculateMaxScore,
  calculatePriorityScore,
  calculateSourceScore,
  calculateTrackScore,
} from "../src/core/scorer";

describe("scorer", () => {
  it("calculateTrackScore returns 1.9 for boolean true", () => {
    expect(calculateTrackScore(true)).toBe(1.9);
  });

  it("calculateTrackScore returns 0 for false or empty array", () => {
    expect(calculateTrackScore(false as any)).toBe(0);
    expect(calculateTrackScore([])).toBe(0);
  });

  it("calculateTrackScore sums default track scores", () => {
    expect(calculateTrackScore(["video"], DEFAULT_TRACK_SCORES)).toBe(2.0);
    expect(calculateTrackScore(["audio"], DEFAULT_TRACK_SCORES)).toBe(1.0);
    expect(calculateTrackScore(["subtitle"], DEFAULT_TRACK_SCORES)).toBe(0.5);
    expect(calculateTrackScore(["video", "audio"], DEFAULT_TRACK_SCORES)).toBe(3.0);
  });

  it("calculateMaxScore delegates to calculateTrackScore", () => {
    expect(calculateMaxScore(["video", "audio"])).toBe(3.0);
  });

  it("calculatePriorityScore favors lower priority numbers", () => {
    expect(calculatePriorityScore(0, 10)).toBeCloseTo(1);
    expect(calculatePriorityScore(10, 10)).toBeCloseTo(0);
    expect(calculatePriorityScore(5, 10)).toBeCloseTo(0.5);
  });

  it("calculateSourceScore favors earlier sources", () => {
    expect(calculateSourceScore(0, 3)).toBeCloseTo(1);
    expect(calculateSourceScore(1, 3)).toBeCloseTo(0.5);
    expect(calculateSourceScore(2, 3)).toBeCloseTo(0);
  });
});
