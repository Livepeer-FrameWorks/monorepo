import { describe, it, expect } from "vitest";
import {
  getLatencyTier,
  calculateLiveThresholds,
  calculateIsNearLive,
  isLiveContent,
  LATENCY_TIERS,
} from "../src/core/SeekingUtils";

describe("getLatencyTier", () => {
  it("returns 'medium' for undefined", () => {
    expect(getLatencyTier(undefined)).toBe("medium");
  });

  it("returns 'ultra-low' for WebRTC protocols", () => {
    expect(getLatencyTier("whep")).toBe("ultra-low");
    expect(getLatencyTier("webrtc")).toBe("ultra-low");
    expect(getLatencyTier("WHEP")).toBe("ultra-low");
    expect(getLatencyTier("mist/webrtc")).toBe("ultra-low");
  });

  it("returns 'low' for WebSocket protocols", () => {
    expect(getLatencyTier("ws/video/mp4")).toBe("low");
    expect(getLatencyTier("wss/video/mp4")).toBe("low");
  });

  it("returns 'medium' for HLS/DASH", () => {
    expect(getLatencyTier("application/vnd.apple.mpegurl")).toBe("medium");
    expect(getLatencyTier("application/dash+xml")).toBe("medium");
  });

  it("returns 'medium' for progressive formats", () => {
    expect(getLatencyTier("video/mp4")).toBe("medium");
    expect(getLatencyTier("video/webm")).toBe("medium");
  });

  it("returns 'medium' for unknown types", () => {
    expect(getLatencyTier("unknown")).toBe("medium");
  });
});

describe("LATENCY_TIERS", () => {
  it("has expected tiers", () => {
    expect(LATENCY_TIERS["ultra-low"]).toBeDefined();
    expect(LATENCY_TIERS["low"]).toBeDefined();
    expect(LATENCY_TIERS["medium"]).toBeDefined();
    expect(LATENCY_TIERS["high"]).toBeDefined();
  });

  it("each tier has exitLive > enterLive for hysteresis", () => {
    for (const [tier, thresholds] of Object.entries(LATENCY_TIERS)) {
      expect(thresholds.exitLive).toBeGreaterThan(thresholds.enterLive);
    }
  });

  it("ultra-low has smallest thresholds", () => {
    expect(LATENCY_TIERS["ultra-low"].exitLive).toBeLessThan(LATENCY_TIERS["low"].exitLive);
    expect(LATENCY_TIERS["ultra-low"].enterLive).toBeLessThan(LATENCY_TIERS["low"].enterLive);
  });
});

describe("calculateLiveThresholds", () => {
  it("returns ultra-low thresholds for WebRTC", () => {
    const thresholds = calculateLiveThresholds("whep");
    expect(thresholds).toEqual(LATENCY_TIERS["ultra-low"]);
  });

  it("returns ultra-low thresholds when isWebRTC is true", () => {
    const thresholds = calculateLiveThresholds(undefined, true);
    expect(thresholds).toEqual(LATENCY_TIERS["ultra-low"]);
  });

  it("returns medium thresholds for HLS", () => {
    const thresholds = calculateLiveThresholds("application/vnd.apple.mpegurl");
    expect(thresholds).toEqual(LATENCY_TIERS["medium"]);
  });

  it("scales medium tier thresholds based on buffer window", () => {
    const thresholds = calculateLiveThresholds("application/vnd.apple.mpegurl", false, 60000);
    expect(thresholds.exitLive).toBeGreaterThanOrEqual(LATENCY_TIERS["medium"].exitLive);
    expect(thresholds.enterLive).toBeGreaterThanOrEqual(LATENCY_TIERS["medium"].enterLive);
  });

  it("does not scale ultra-low/low tiers", () => {
    const ultraLow = calculateLiveThresholds("whep", false, 60000);
    expect(ultraLow).toEqual(LATENCY_TIERS["ultra-low"]);

    const low = calculateLiveThresholds("ws/video/mp4", false, 60000);
    expect(low).toEqual(LATENCY_TIERS["low"]);
  });
});

describe("calculateIsNearLive", () => {
  const thresholds = { exitLive: 10, enterLive: 3 };

  it("returns true for invalid liveEdge", () => {
    expect(calculateIsNearLive(50, 0, thresholds, false)).toBe(true);
    expect(calculateIsNearLive(50, -1, thresholds, false)).toBe(true);
    expect(calculateIsNearLive(50, Infinity, thresholds, false)).toBe(true);
  });

  it("stays in LIVE state when within exit threshold", () => {
    expect(calculateIsNearLive(55, 60, thresholds, true)).toBe(true);
    expect(calculateIsNearLive(52, 60, thresholds, true)).toBe(true);
  });

  it("exits LIVE state when significantly behind", () => {
    expect(calculateIsNearLive(40, 60, thresholds, true)).toBe(false);
  });

  it("enters LIVE state when close to edge", () => {
    expect(calculateIsNearLive(59, 60, thresholds, false)).toBe(true);
  });

  it("stays behind when not close enough", () => {
    expect(calculateIsNearLive(50, 60, thresholds, false)).toBe(false);
  });

  it("maintains state in hysteresis zone", () => {
    expect(calculateIsNearLive(53, 60, thresholds, true)).toBe(true);
    expect(calculateIsNearLive(53, 60, thresholds, false)).toBe(false);
  });
});

describe("isLiveContent", () => {
  it("returns explicit flag when provided", () => {
    expect(isLiveContent(true, undefined, 300)).toBe(true);
    expect(isLiveContent(false, undefined, Infinity)).toBe(false);
  });

  it("checks MistStreamInfo type", () => {
    expect(isLiveContent(undefined, { type: "live" } as any, 300)).toBe(true);
    expect(isLiveContent(undefined, { type: "vod" } as any, Infinity)).toBe(false);
  });

  it("falls back to duration check", () => {
    expect(isLiveContent(undefined, undefined, Infinity)).toBe(true);
    expect(isLiveContent(undefined, undefined, 300)).toBe(false);
  });

  it("returns true for NaN duration", () => {
    expect(isLiveContent(undefined, undefined, NaN)).toBe(true);
  });
});
