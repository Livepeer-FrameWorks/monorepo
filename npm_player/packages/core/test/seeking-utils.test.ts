import { describe, it, expect, beforeAll, afterAll } from "vitest";
import {
  getLatencyTier,
  calculateLiveThresholds,
  calculateIsNearLive,
  calculateSeekableRange,
  canSeekStream,
  isLiveContent,
  isMediaStreamSource,
  supportsPlaybackRate,
  LATENCY_TIERS,
  DEFAULT_BUFFER_WINDOW_MS,
  mapPlayerTimeToMistTimeline,
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
  const thresholds = { exitLive: 10000, enterLive: 3000 };

  it("returns true for invalid liveEdge", () => {
    expect(calculateIsNearLive(50000, 0, thresholds, false)).toBe(true);
    expect(calculateIsNearLive(50000, -1, thresholds, false)).toBe(true);
    expect(calculateIsNearLive(50000, Infinity, thresholds, false)).toBe(true);
  });

  it("stays in LIVE state when within exit threshold", () => {
    expect(calculateIsNearLive(55000, 60000, thresholds, true)).toBe(true);
    expect(calculateIsNearLive(52000, 60000, thresholds, true)).toBe(true);
  });

  it("exits LIVE state when significantly behind", () => {
    expect(calculateIsNearLive(40000, 60000, thresholds, true)).toBe(false);
  });

  it("enters LIVE state when close to edge", () => {
    expect(calculateIsNearLive(59000, 60000, thresholds, false)).toBe(true);
  });

  it("stays behind when not close enough", () => {
    expect(calculateIsNearLive(50000, 60000, thresholds, false)).toBe(false);
  });

  it("maintains state in hysteresis zone", () => {
    expect(calculateIsNearLive(53000, 60000, thresholds, true)).toBe(true);
    expect(calculateIsNearLive(53000, 60000, thresholds, false)).toBe(false);
  });
});

describe("calculateSeekableRange", () => {
  const originalMediaStream = (globalThis as any).MediaStream;

  beforeAll(() => {
    if (!(globalThis as any).MediaStream) {
      (globalThis as any).MediaStream = class MediaStreamMock {};
    }
  });

  afterAll(() => {
    (globalThis as any).MediaStream = originalMediaStream;
  });

  const makeVideo = (startSec: number, endSec: number) =>
    ({
      seekable: {
        length: 1,
        start: () => startSec,
        end: () => endSec,
      },
      srcObject: null,
    }) as unknown as HTMLVideoElement;

  it("returns video.seekable range directly (clamping is done by PlayerController)", () => {
    const range = calculateSeekableRange({
      isLive: true,
      video: makeVideo(0, 600),
      mistStreamInfo: { meta: { buffer_window: 60_000 } } as any,
      currentTime: 0,
      duration: Infinity,
    });

    expect(range.seekableStart).toBe(0);
    expect(range.liveEdge).toBe(600_000);
  });

  it("preserves live seekable windows that already match Mist buffer_window", () => {
    const range = calculateSeekableRange({
      isLive: true,
      video: makeVideo(540, 600),
      mistStreamInfo: { meta: { buffer_window: 60_000 } } as any,
      currentTime: 0,
      duration: Infinity,
    });

    expect(range.seekableStart).toBe(540_000);
    expect(range.liveEdge).toBe(600_000);
  });
});

describe("canSeekStream", () => {
  it("keeps seeking enabled for live playback before a DVR window is discovered", () => {
    expect(
      canSeekStream({
        video: null,
        isLive: true,
        duration: Infinity,
      })
    ).toBe(true);
  });

  it("honors an explicit player-level no-seek capability", () => {
    expect(
      canSeekStream({
        video: null,
        isLive: true,
        duration: Infinity,
        playerCanSeek: () => false,
      })
    ).toBe(false);
  });
});

describe("mapPlayerTimeToMistTimeline", () => {
  it("maps zero-based live player time by distance from live edge", () => {
    expect(
      mapPlayerTimeToMistTimeline({
        isLive: true,
        playerTimeMs: 2_000,
        playerSeekableRange: { start: 0, end: 5_000 },
        mistSeekableRange: { start: 1_950_000, end: 2_000_000 },
      })
    ).toBe(1_997_000);
  });

  it("preserves absolute live ranges through the same live-edge mapping", () => {
    expect(
      mapPlayerTimeToMistTimeline({
        isLive: true,
        playerTimeMs: 1_997_000,
        playerSeekableRange: { start: 1_950_000, end: 2_000_000 },
        mistSeekableRange: { start: 1_950_000, end: 2_000_000 },
      })
    ).toBe(1_997_000);
  });

  it("does not translate VOD or unknown ranges", () => {
    expect(
      mapPlayerTimeToMistTimeline({
        isLive: false,
        playerTimeMs: 2_000,
        playerSeekableRange: { start: 0, end: 5_000 },
        mistSeekableRange: { start: 1_950_000, end: 2_000_000 },
      })
    ).toBe(2_000);

    expect(
      mapPlayerTimeToMistTimeline({
        isLive: true,
        playerTimeMs: 2_000,
        playerSeekableRange: null,
        mistSeekableRange: { start: 1_950_000, end: 2_000_000 },
      })
    ).toBe(2_000);
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

// ---------------------------------------------------------------------------
// MediaStream-source detection (used by the seekable-range + canSeek fallbacks)
// ---------------------------------------------------------------------------
describe("isMediaStreamSource / supportsPlaybackRate", () => {
  const original = (globalThis as any).MediaStream;
  class FakeMediaStream {}
  beforeAll(() => {
    (globalThis as any).MediaStream = FakeMediaStream;
  });
  afterAll(() => {
    (globalThis as any).MediaStream = original;
  });

  it("detects a MediaStream srcObject", () => {
    const ms = { srcObject: new FakeMediaStream() } as unknown as HTMLVideoElement;
    const url = { srcObject: null } as unknown as HTMLVideoElement;
    expect(isMediaStreamSource(ms)).toBe(true);
    expect(isMediaStreamSource(url)).toBe(false);
    expect(isMediaStreamSource(null)).toBe(false);
  });

  it("disables playback-rate control only for MediaStream sources", () => {
    expect(supportsPlaybackRate(null)).toBe(true); // no element → assume controllable
    expect(supportsPlaybackRate({ srcObject: null } as unknown as HTMLVideoElement)).toBe(true);
    expect(
      supportsPlaybackRate({ srcObject: new FakeMediaStream() } as unknown as HTMLVideoElement)
    ).toBe(false);
  });
});

// ---------------------------------------------------------------------------
// calculateSeekableRange — the live buffer-window fallback (no video.seekable)
// ---------------------------------------------------------------------------
describe("calculateSeekableRange (buffer-window fallback)", () => {
  it("returns [0, duration] for VOD without seekable ranges", () => {
    expect(
      calculateSeekableRange({ isLive: false, video: null, currentTime: 0, duration: 300_000 })
    ).toEqual({ seekableStart: 0, liveEdge: 300_000 });
  });

  it("uses Mist buffer_window to size the live window behind the edge", () => {
    // liveEdge = duration (finite) = 600_000; window 60s → start 540_000.
    expect(
      calculateSeekableRange({
        isLive: true,
        video: null,
        mistStreamInfo: { meta: { buffer_window: 60_000 } } as any,
        currentTime: 0,
        duration: 600_000,
      })
    ).toEqual({ seekableStart: 540_000, liveEdge: 600_000 });
  });

  it("derives the window from bufferedStartMs when buffer_window is absent", () => {
    // liveEdge 600s, bufferedStart 570s → 30s window.
    expect(
      calculateSeekableRange({
        isLive: true,
        video: null,
        currentTime: 0,
        duration: 600_000,
        bufferedStartMs: 570_000,
      })
    ).toEqual({ seekableStart: 570_000, liveEdge: 600_000 });
  });

  it("derives the window from video.buffered.start(0) when no explicit hint", () => {
    const video = {
      seekable: { length: 0, start: () => 0, end: () => 0 },
      buffered: { length: 1, start: () => 580, end: () => 600 },
      srcObject: null,
    } as unknown as HTMLVideoElement;
    expect(
      calculateSeekableRange({ isLive: true, video, currentTime: 0, duration: 600_000 })
    ).toEqual({ seekableStart: 580_000, liveEdge: 600_000 });
  });

  it("falls back to the 60s default window when nothing else is known", () => {
    const range = calculateSeekableRange({
      isLive: true,
      video: null,
      currentTime: 0,
      duration: 600_000,
    });
    expect(range.liveEdge).toBe(600_000);
    expect(range.seekableStart).toBe(600_000 - DEFAULT_BUFFER_WINDOW_MS);
  });

  it("exposes buffer_window at startup (liveEdge 0) so DVR is visible pre-playback", () => {
    // duration non-finite & currentTime 0 → liveEdge = currentTime = 0; with
    // buffer_window present, range becomes [0, buffer_window].
    expect(
      calculateSeekableRange({
        isLive: true,
        video: null,
        mistStreamInfo: { meta: { buffer_window: 45_000 } } as any,
        currentTime: 0,
        duration: Infinity,
      })
    ).toEqual({ seekableStart: 0, liveEdge: 45_000 });
  });
});

// ---------------------------------------------------------------------------
// canSeekStream — the full decision tree
// ---------------------------------------------------------------------------
describe("canSeekStream (decision tree)", () => {
  const original = (globalThis as any).MediaStream;
  class FakeMediaStream {}
  beforeAll(() => {
    (globalThis as any).MediaStream = FakeMediaStream;
  });
  afterAll(() => {
    (globalThis as any).MediaStream = original;
  });

  it("trusts a valid player-reported seekable range above everything", () => {
    expect(
      canSeekStream({
        video: null,
        isLive: false,
        duration: 0,
        playerSeekableRange: { start: 0, end: 10_000 },
      })
    ).toBe(true);
  });

  it("trusts an affirmative playerCanSeek()", () => {
    expect(
      canSeekStream({ video: null, isLive: false, duration: 0, playerCanSeek: () => true })
    ).toBe(true);
  });

  it("returns false for VOD with no video element and no player hints", () => {
    expect(canSeekStream({ video: null, isLive: false, duration: 300_000 })).toBe(false);
  });

  it("gates MediaStream sources on an explicit buffer_window", () => {
    const video = { srcObject: new FakeMediaStream() } as unknown as HTMLVideoElement;
    expect(canSeekStream({ video, isLive: false, duration: 0 })).toBe(false);
    expect(canSeekStream({ video, isLive: false, duration: 0, bufferWindowMs: 30_000 })).toBe(true);
  });

  it("allows seeking when the browser reports seekable ranges", () => {
    const video = {
      srcObject: null,
      seekable: { length: 1, start: () => 0, end: () => 100 },
    } as unknown as HTMLVideoElement;
    expect(canSeekStream({ video, isLive: false, duration: Infinity })).toBe(true);
  });

  it("allows seeking for VOD with a finite duration and no seekable ranges", () => {
    const video = {
      srcObject: null,
      seekable: { length: 0, start: () => 0, end: () => 0 },
    } as unknown as HTMLVideoElement;
    expect(canSeekStream({ video, isLive: false, duration: 300_000 })).toBe(true);
  });

  it("returns false for VOD with no duration, no ranges, no hints", () => {
    const video = {
      srcObject: null,
      seekable: { length: 0, start: () => 0, end: () => 0 },
    } as unknown as HTMLVideoElement;
    expect(canSeekStream({ video, isLive: false, duration: NaN })).toBe(false);
  });
});

describe("mapPlayerTimeToMistTimeline (guards)", () => {
  it("returns the player time unchanged when the Mist range is invalid", () => {
    expect(
      mapPlayerTimeToMistTimeline({
        isLive: true,
        playerTimeMs: 2_000,
        playerSeekableRange: { start: 0, end: 5_000 },
        mistSeekableRange: { start: 5, end: 5 }, // end <= start → invalid
      })
    ).toBe(2_000);
  });

  it("clamps the mapped time into the Mist range", () => {
    // playerTime beyond the player edge → behindLive 0 → maps to mist end, clamped.
    expect(
      mapPlayerTimeToMistTimeline({
        isLive: true,
        playerTimeMs: 9_999,
        playerSeekableRange: { start: 0, end: 5_000 },
        mistSeekableRange: { start: 1_000, end: 2_000 },
      })
    ).toBe(2_000);
  });
});
