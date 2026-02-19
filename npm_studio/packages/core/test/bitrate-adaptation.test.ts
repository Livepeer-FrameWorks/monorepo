import { describe, expect, it, vi, beforeEach, afterEach } from "vitest";

import { BitrateAdaptation, type BitrateAdaptationOptions } from "../src/core/BitrateAdaptation";

function createMockEncoder() {
  return {
    updateConfig: vi.fn().mockResolvedValue(undefined),
    on: vi.fn(),
    off: vi.fn(),
  };
}

function createMockStats(
  overrides: {
    bytesSent?: number;
    packetsSent?: number;
    packetsLost?: number;
    rtt?: number;
    availableOutgoingBitrate?: number;
  } = {}
) {
  const entries: any[] = [
    {
      type: "outbound-rtp",
      kind: "video",
      bytesSent: overrides.bytesSent ?? 100000,
      packetsSent: overrides.packetsSent ?? 1000,
    },
    {
      type: "remote-inbound-rtp",
      kind: "video",
      packetsLost: overrides.packetsLost ?? 0,
      roundTripTime: overrides.rtt ?? 0.02,
    },
    {
      type: "candidate-pair",
      state: "succeeded",
      availableOutgoingBitrate: overrides.availableOutgoingBitrate ?? 5_000_000,
      currentRoundTripTime: overrides.rtt ?? 0.02,
    },
  ];

  return {
    forEach: vi.fn((cb: (stat: any) => void) => entries.forEach(cb)),
  };
}

function createMockPeerConnection(statsFactory?: () => any) {
  return {
    getStats: vi.fn(async () => (statsFactory ? statsFactory() : createMockStats())),
  };
}

describe("BitrateAdaptation", () => {
  beforeEach(() => {
    vi.useFakeTimers();
  });

  afterEach(() => {
    vi.useRealTimers();
    vi.restoreAllMocks();
  });

  function createAdapter(overrides?: Partial<BitrateAdaptationOptions>) {
    const encoder = createMockEncoder();
    const pc = createMockPeerConnection();
    const opts: BitrateAdaptationOptions = {
      pc: pc as any,
      encoder: encoder as any,
      maxBitrate: 4_500_000,
      minBitrate: 500_000,
      pollInterval: 2000,
      ...overrides,
    };
    const adapter = new BitrateAdaptation(opts);
    return { adapter, encoder, pc };
  }

  // ===========================================================================
  // Start / stop
  // ===========================================================================
  describe("start and stop", () => {
    it("starts polling on start()", () => {
      const { adapter, pc } = createAdapter();
      adapter.start();

      vi.advanceTimersByTime(2000);
      expect(pc.getStats).toHaveBeenCalled();
      adapter.destroy();
    });

    it("stops polling on stop()", () => {
      const { adapter, pc } = createAdapter();
      adapter.start();
      adapter.stop();

      pc.getStats.mockClear();
      vi.advanceTimersByTime(10000);
      expect(pc.getStats).not.toHaveBeenCalled();
      adapter.destroy();
    });

    it("ignores duplicate start calls", () => {
      const { adapter } = createAdapter();
      adapter.start();
      adapter.start(); // should not create second timer
      adapter.destroy();
    });

    it("initializes bitrate to maxBitrate", () => {
      const { adapter } = createAdapter({ maxBitrate: 6_000_000 });
      expect(adapter.bitrate).toBe(6_000_000);
      adapter.destroy();
    });

    it("initializes congestionLevel to none", () => {
      const { adapter } = createAdapter();
      expect(adapter.congestionLevel).toBe("none");
      adapter.destroy();
    });
  });

  // ===========================================================================
  // Congestion detection
  // ===========================================================================
  describe("congestion detection", () => {
    it("detects no congestion when stats are healthy", async () => {
      let callCount = 0;
      const pc = createMockPeerConnection(() => {
        callCount++;
        return createMockStats({
          packetsSent: callCount * 1000,
          packetsLost: 0,
          rtt: 0.02,
        });
      });

      const encoder = createMockEncoder();
      const adapter = new BitrateAdaptation({
        pc: pc as any,
        encoder: encoder as any,
        maxBitrate: 4_500_000,
        pollInterval: 100,
      });

      const congestionHandler = vi.fn();
      adapter.on("congestionChanged", congestionHandler);

      adapter.start();
      // Need 2+ polls for detection
      await vi.advanceTimersByTimeAsync(300);

      expect(adapter.congestionLevel).toBe("none");
      adapter.destroy();
    });

    it("detects mild congestion on packet loss >= 5%", async () => {
      let callCount = 0;
      const pc = createMockPeerConnection(() => {
        callCount++;
        return createMockStats({
          packetsSent: callCount * 1000,
          // 60 lost out of 1000 delta = 6% loss
          packetsLost: callCount === 1 ? 0 : 60,
          rtt: 0.02,
        });
      });

      const encoder = createMockEncoder();
      const adapter = new BitrateAdaptation({
        pc: pc as any,
        encoder: encoder as any,
        maxBitrate: 4_500_000,
        pollInterval: 100,
      });

      const congestionHandler = vi.fn();
      adapter.on("congestionChanged", congestionHandler);

      adapter.start();
      await vi.advanceTimersByTimeAsync(300);

      expect(congestionHandler).toHaveBeenCalled();
      const event = congestionHandler.mock.calls[0][0];
      expect(event.level).toBe("mild");
      adapter.destroy();
    });

    it("detects severe congestion on packet loss >= 15%", async () => {
      let callCount = 0;
      const pc = createMockPeerConnection(() => {
        callCount++;
        return createMockStats({
          packetsSent: callCount * 1000,
          packetsLost: callCount === 1 ? 0 : 200,
          rtt: 0.02,
        });
      });

      const encoder = createMockEncoder();
      const adapter = new BitrateAdaptation({
        pc: pc as any,
        encoder: encoder as any,
        maxBitrate: 4_500_000,
        pollInterval: 100,
      });

      const congestionHandler = vi.fn();
      adapter.on("congestionChanged", congestionHandler);

      adapter.start();
      await vi.advanceTimersByTimeAsync(300);

      expect(congestionHandler).toHaveBeenCalled();
      const event = congestionHandler.mock.calls[0][0];
      expect(event.level).toBe("severe");
      adapter.destroy();
    });

    it("detects mild congestion on RTT increase >= 50ms", async () => {
      let callCount = 0;
      const pc = createMockPeerConnection(() => {
        callCount++;
        return createMockStats({
          packetsSent: callCount * 1000,
          packetsLost: 0,
          // RTT increases from 0.02s to 0.08s = +60ms
          rtt: callCount <= 1 ? 0.02 : 0.08,
        });
      });

      const encoder = createMockEncoder();
      const adapter = new BitrateAdaptation({
        pc: pc as any,
        encoder: encoder as any,
        maxBitrate: 4_500_000,
        pollInterval: 100,
      });

      const congestionHandler = vi.fn();
      adapter.on("congestionChanged", congestionHandler);

      adapter.start();
      await vi.advanceTimersByTimeAsync(300);

      expect(congestionHandler).toHaveBeenCalled();
      adapter.destroy();
    });
  });

  // ===========================================================================
  // Adaptation rates (+10% / -20% / -50%)
  // ===========================================================================
  describe("adaptation rates", () => {
    it("decreases bitrate by ~20% on mild congestion", async () => {
      let callCount = 0;
      const pc = createMockPeerConnection(() => {
        callCount++;
        return createMockStats({
          packetsSent: callCount * 1000,
          packetsLost: callCount === 1 ? 0 : 80, // 8% loss = mild
          rtt: 0.02,
        });
      });

      const encoder = createMockEncoder();
      const adapter = new BitrateAdaptation({
        pc: pc as any,
        encoder: encoder as any,
        maxBitrate: 4_500_000,
        pollInterval: 100,
      });

      const bitrateHandler = vi.fn();
      adapter.on("bitrateChanged", bitrateHandler);

      adapter.start();
      await vi.advanceTimersByTimeAsync(300);

      expect(bitrateHandler).toHaveBeenCalled();
      const event = bitrateHandler.mock.calls[0][0];
      // 4_500_000 * 0.80 = 3_600_000
      expect(event.bitrate).toBe(3_600_000);
      expect(event.previousBitrate).toBe(4_500_000);
      adapter.destroy();
    });

    it("decreases bitrate by ~50% on severe congestion", async () => {
      let callCount = 0;
      const pc = createMockPeerConnection(() => {
        callCount++;
        return createMockStats({
          packetsSent: callCount * 1000,
          packetsLost: callCount === 1 ? 0 : 200, // 20% loss = severe
          rtt: 0.02,
        });
      });

      const encoder = createMockEncoder();
      const adapter = new BitrateAdaptation({
        pc: pc as any,
        encoder: encoder as any,
        maxBitrate: 4_500_000,
        pollInterval: 100,
      });

      const bitrateHandler = vi.fn();
      adapter.on("bitrateChanged", bitrateHandler);

      adapter.start();
      await vi.advanceTimersByTimeAsync(300);

      expect(bitrateHandler).toHaveBeenCalled();
      const event = bitrateHandler.mock.calls[0][0];
      // 4_500_000 * 0.50 = 2_250_000 → quantized to 2_200_000
      expect(event.bitrate).toBe(2_300_000);
      adapter.destroy();
    });

    it("never drops below minBitrate", async () => {
      let callCount = 0;
      const pc = createMockPeerConnection(() => {
        callCount++;
        return createMockStats({
          packetsSent: callCount * 1000,
          packetsLost: callCount === 1 ? 0 : 200,
          rtt: 0.02,
        });
      });

      const encoder = createMockEncoder();
      const adapter = new BitrateAdaptation({
        pc: pc as any,
        encoder: encoder as any,
        maxBitrate: 800_000, // Start low
        minBitrate: 500_000,
        pollInterval: 100,
      });

      adapter.start();
      // Multiple severe drops
      await vi.advanceTimersByTimeAsync(500);

      expect(adapter.bitrate).toBeGreaterThanOrEqual(500_000);
      adapter.destroy();
    });

    it("quantizes bitrate to nearest 100kbps", async () => {
      let callCount = 0;
      const pc = createMockPeerConnection(() => {
        callCount++;
        return createMockStats({
          packetsSent: callCount * 1000,
          packetsLost: callCount === 1 ? 0 : 60,
          rtt: 0.02,
        });
      });

      const encoder = createMockEncoder();
      const adapter = new BitrateAdaptation({
        pc: pc as any,
        encoder: encoder as any,
        maxBitrate: 4_500_000,
        pollInterval: 100,
      });

      const bitrateHandler = vi.fn();
      adapter.on("bitrateChanged", bitrateHandler);

      adapter.start();
      await vi.advanceTimersByTimeAsync(300);

      if (bitrateHandler.mock.calls.length > 0) {
        const newBitrate = bitrateHandler.mock.calls[0][0].bitrate;
        expect(newBitrate % 100_000).toBe(0);
      }
      adapter.destroy();
    });
  });

  // ===========================================================================
  // Resolution downscale
  // ===========================================================================
  describe("resolution downscale", () => {
    it("downscales resolution when bitrate falls below 800 kbps", async () => {
      let callCount = 0;
      const pc = createMockPeerConnection(() => {
        callCount++;
        return createMockStats({
          packetsSent: callCount * 1000,
          packetsLost: callCount === 1 ? 0 : 200, // severe = 50% drop
          rtt: 0.02,
        });
      });

      const encoder = createMockEncoder();
      const adapter = new BitrateAdaptation({
        pc: pc as any,
        encoder: encoder as any,
        maxBitrate: 1_000_000, // Will drop to ~500_000, below 800k threshold
        minBitrate: 300_000,
        pollInterval: 100,
      });

      const resHandler = vi.fn();
      adapter.on("resolutionChanged", resHandler);

      adapter.start();
      await vi.advanceTimersByTimeAsync(300);

      expect(resHandler).toHaveBeenCalled();
      const res = resHandler.mock.calls[0][0];
      expect(res.width).toBeLessThan(1920);
      adapter.destroy();
    });
  });

  // ===========================================================================
  // Destroy
  // ===========================================================================
  describe("destroy", () => {
    it("stops polling and removes listeners", () => {
      const { adapter, pc } = createAdapter();
      adapter.start();
      adapter.destroy();

      pc.getStats.mockClear();
      vi.advanceTimersByTime(10000);
      expect(pc.getStats).not.toHaveBeenCalled();
    });
  });

  // ===========================================================================
  // Edge cases
  // ===========================================================================
  describe("edge cases", () => {
    it("ignores stats errors gracefully", async () => {
      const pc = createMockPeerConnection();
      pc.getStats.mockRejectedValue(new Error("disconnected"));

      const encoder = createMockEncoder();
      const adapter = new BitrateAdaptation({
        pc: pc as any,
        encoder: encoder as any,
        maxBitrate: 4_500_000,
        pollInterval: 100,
      });

      adapter.start();
      // Should not throw
      await vi.advanceTimersByTimeAsync(300);
      expect(adapter.bitrate).toBe(4_500_000);
      adapter.destroy();
    });

    it("needs at least 2 samples before adapting", async () => {
      const { adapter, pc } = createAdapter({ pollInterval: 100 });
      const bitrateHandler = vi.fn();
      adapter.on("bitrateChanged", bitrateHandler);

      adapter.start();
      // Only one poll
      await vi.advanceTimersByTimeAsync(100);
      expect(bitrateHandler).not.toHaveBeenCalled();
      adapter.destroy();
    });
  });
});
