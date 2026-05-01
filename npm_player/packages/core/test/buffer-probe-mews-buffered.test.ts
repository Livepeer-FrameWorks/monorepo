import { describe, expect, it } from "vitest";

import { MewsBufferedProbe } from "../src/players/MewsWsPlayer/BufferProbe.MewsBuffered";

describe("MewsBufferedProbe", () => {
  it("measures forward buffer from the active buffered range", () => {
    const video = {
      currentTime: 2,
      buffered: {
        length: 1,
        start: () => 1,
        end: () => 4.5,
      },
    } as unknown as HTMLVideoElement;
    const probe = new MewsBufferedProbe(video);
    probe.updateServerState({ currentMs: 1000, endMs: 5000, jitterMs: 25 });

    expect(probe.sample()).toEqual({
      currentMs: 2500,
      serverCurrentMs: 1000,
      serverEndMs: 5000,
      jitterMs: 25,
    });
  });
});
