import { describe, expect, it } from "vitest";
import { assessConnectionHealth } from "../src/core/connectionHealth";
import { IngestControllerV2 } from "../src/core/IngestControllerV2";
import type { IngestStats } from "../src/types";

function stats(lost: number, rtt: number): IngestStats {
  return {
    video: {
      bytesSent: 0,
      packetsSent: 900,
      packetsLost: lost,
      framesEncoded: 0,
      framesPerSecond: 30,
      bitrate: 2_500_000,
    },
    audio: { bytesSent: 0, packetsSent: 100, packetsLost: 0, bitrate: 128_000 },
    connection: { rtt, state: "connected", iceState: "connected" },
    timestamp: 0,
  };
}

describe("assessConnectionHealth", () => {
  it("has nothing to say before stats arrive", () => {
    expect(assessConnectionHealth(null)).toBeNull();
  });

  it("grades loss and round-trip time", () => {
    expect(assessConnectionHealth(stats(0, 40))?.level).toBe("good");
    expect(assessConnectionHealth(stats(30, 40))?.level).toBe("fair");
    expect(assessConnectionHealth(stats(0, 200))?.level).toBe("fair");
    expect(assessConnectionHealth(stats(80, 40))?.level).toBe("poor");
    expect(assessConnectionHealth(stats(0, 400))?.level).toBe("poor");
  });

  it("reports the send rate in kbps", () => {
    expect(assessConnectionHealth(stats(0, 40))?.bitrateKbps).toBe(2628);
  });
});

describe("IngestControllerV2 stats", () => {
  it("reports the round-trip time in milliseconds", async () => {
    const controller = new IngestControllerV2({
      whipUrl: "https://edge.example/webrtc/key",
      useWebCodecs: false,
    });
    const report = new Map([
      ["pair", { type: "candidate-pair", state: "succeeded", currentRoundTripTime: 0.12 }],
    ]);
    (controller as unknown as { whipClient: unknown }).whipClient = {
      getStats: async () => report,
      getPeerConnection: () => null,
    };

    const result = await controller.getStats();

    expect(result?.connection.rtt).toBeCloseTo(120);
    (controller as unknown as { whipClient: unknown }).whipClient = null;
    controller.destroy();
  });
});
