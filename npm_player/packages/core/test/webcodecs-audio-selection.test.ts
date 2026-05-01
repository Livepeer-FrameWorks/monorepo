import { describe, expect, it } from "vitest";

import { buildSupportedAudioCodecList } from "../src/players/WebCodecsPlayer";
import { SyncController } from "../src/players/WebCodecsPlayer/SyncController";

describe("WebCodecs audio stability", () => {
  it("advertises all supported raw WebCodecs audio codecs", () => {
    const codecs = buildSupportedAudioCodecList([
      { codec: "AAC", rate: 44100 },
      { codec: "opus", rate: 48000 },
      { codec: "AAC", rate: 44100 },
    ]);

    expect(codecs).toEqual(["AAC", "opus"]);
  });

  it("keeps local rate tweaks enabled by default", () => {
    const speedChanges: number[] = [];
    const sync = new SyncController({
      isLive: true,
      onSpeedChange: (_main, tweak) => speedChanges.push(tweak),
    });

    const desired = sync.getDesiredBuffer();
    sync.evaluateBuffer(desired * 3, {
      playRateCurr: "auto",
      serverCurrentMs: 1_000,
      serverEndMs: 2_000,
      serverJitterMs: 100,
    });

    expect(speedChanges).toEqual([1.05]);
  });
});
