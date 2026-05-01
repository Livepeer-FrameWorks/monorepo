import { describe, expect, it } from "vitest";

import { FrameTimingBufferProbe } from "../src/players/WebCodecsPlayer/BufferProbe.FrameTiming";

describe("FrameTimingBufferProbe", () => {
  it("converts worker microsecond timing into buffer milliseconds", () => {
    const probe = new FrameTimingBufferProbe(() => ({
      decoded: 2_500_000,
      out: 2_000_000,
      serverCurrentMs: 1000,
      serverEndMs: 2000,
      jitterMs: 50,
    }));

    expect(probe.sample()).toEqual({
      currentMs: 500,
      serverCurrentMs: 1000,
      serverEndMs: 2000,
      jitterMs: 50,
    });
  });
});
