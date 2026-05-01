import { describe, expect, it } from "vitest";

import { nextSpeedBucket, type SpeedBucket } from "../src/core/delivery/speed-bucket";

describe("nextSpeedBucket", () => {
  const decide = (bucket: SpeedBucket, currentMs: number) =>
    nextSpeedBucket({
      bucket,
      currentMs,
      desiredMs: 1000,
      speedDownThreshold: 0.6,
      speedUpThreshold: 1.4,
    });

  it("handles normal, low, and high bucket transitions", () => {
    expect(decide("normal", 500)).toBe("low");
    expect(decide("normal", 1500)).toBe("high");
    expect(decide("normal", 1000)).toBe("normal");
    expect(decide("low", 999)).toBe("low");
    expect(decide("low", 1000)).toBe("normal");
    expect(decide("high", 1001)).toBe("high");
    expect(decide("high", 1000)).toBe("normal");
  });
});
