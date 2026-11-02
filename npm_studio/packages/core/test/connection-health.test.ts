import { describe, expect, it } from "vitest";
import { assessConnectionHealth, intervalLossPct } from "../src/core/connectionHealth";
import { IngestControllerV2 } from "../src/core/IngestControllerV2";
import type { IngestStats } from "../src/types";

function stats(lossPct: number, rtt: number): IngestStats {
  return {
    video: {
      bytesSent: 0,
      packetsSent: 900,
      packetsLost: 0,
      framesEncoded: 0,
      framesPerSecond: 30,
      bitrate: 2_500_000,
    },
    audio: { bytesSent: 0, packetsSent: 100, packetsLost: 0, bitrate: 128_000 },
    connection: { rtt, packetLossPct: lossPct, state: "connected", iceState: "connected" },
    timestamp: 0,
  };
}

function counters(sent: number, lost: number): IngestStats {
  const s = stats(0, 0);
  s.video.packetsSent = sent;
  s.video.packetsLost = lost;
  s.audio.packetsSent = 0;
  return s;
}

describe("assessConnectionHealth", () => {
  it("has nothing to say before stats arrive", () => {
    expect(assessConnectionHealth(null)).toBeNull();
  });

  it("grades loss and round-trip time", () => {
    expect(assessConnectionHealth(stats(0, 40))?.level).toBe("good");
    expect(assessConnectionHealth(stats(3, 40))?.level).toBe("fair");
    expect(assessConnectionHealth(stats(0, 200))?.level).toBe("fair");
    expect(assessConnectionHealth(stats(8, 40))?.level).toBe("poor");
    expect(assessConnectionHealth(stats(0, 400))?.level).toBe("poor");
  });

  it("reports the send rate in kbps", () => {
    expect(assessConnectionHealth(stats(0, 40))?.bitrateKbps).toBe(2628);
  });
});

describe("intervalLossPct", () => {
  it("measures loss since the previous sample, not since the start", () => {
    // An early burst of 500 lost, then a clean interval.
    expect(intervalLossPct(counters(11_000, 500), counters(10_000, 500))).toBe(0);
    // A clean hour, then 60 lost out of the last 1000.
    expect(intervalLossPct(counters(1_001_000, 60), counters(1_000_000, 0))).toBeCloseTo(6);
  });

  it("uses the totals for the first sample and after counters restart", () => {
    expect(intervalLossPct(counters(1000, 30), null)).toBeCloseTo(3);
    expect(intervalLossPct(counters(1000, 30), counters(50_000, 400))).toBeCloseTo(3);
  });
});

describe("IngestControllerV2 stats", () => {
  function controllerWith(reports: Map<string, Record<string, unknown>>[]) {
    const controller = new IngestControllerV2({
      whipUrl: "https://edge.example/webrtc/key",
      useWebCodecs: false,
    });
    let call = 0;
    (controller as unknown as { whipClient: unknown }).whipClient = {
      getStats: async () => reports[Math.min(call++, reports.length - 1)],
      getPeerConnection: () => null,
    };
    return controller;
  }

  function release(controller: IngestControllerV2) {
    (controller as unknown as { whipClient: unknown }).whipClient = null;
    controller.destroy();
  }

  it("reports the round-trip time in milliseconds", async () => {
    const controller = controllerWith([
      new Map([
        ["pair", { type: "candidate-pair", state: "succeeded", currentRoundTripTime: 0.12 }],
      ]),
    ]);

    const result = await controller.getStats();

    expect(result?.connection.rtt).toBeCloseTo(120);
    release(controller);
  });

  it("reads loss from the receiver reports and grades the last interval", async () => {
    const sample = (sent: number, lost: number) =>
      new Map<string, Record<string, unknown>>([
        ["out-v", { type: "outbound-rtp", kind: "video", packetsSent: sent, bytesSent: 0 }],
        [
          "remote-v",
          { type: "remote-inbound-rtp", kind: "video", packetsLost: lost, roundTripTime: 0.05 },
        ],
      ]);
    const controller = controllerWith([sample(10_000, 0), sample(11_000, 60)]);
    const internals = controller as unknown as { lastStats: IngestStats | null };

    internals.lastStats = await controller.getStats();
    const result = await controller.getStats();

    expect(result?.video.packetsLost).toBe(60);
    expect(result?.connection.rtt).toBeCloseTo(50);
    const health = assessConnectionHealth(result);
    expect(health?.packetLossPct).toBeCloseTo(6);
    expect(health?.level).toBe("poor");
    release(controller);
  });
});
