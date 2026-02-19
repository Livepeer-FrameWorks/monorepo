import { describe, expect, it, vi, beforeEach, afterEach } from "vitest";

import { QualityMonitor, PROTOCOL_THRESHOLDS } from "../src/core/QualityMonitor";

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

function makeBuffered(...ranges: [number, number][]) {
  return {
    length: ranges.length,
    start: (i: number) => ranges[i][0],
    end: (i: number) => ranges[i][1],
  };
}

function createMockVideo(overrides: Record<string, unknown> = {}) {
  const listeners = new Map<string, Function[]>();
  return {
    currentTime: 5,
    duration: 60,
    playbackRate: 1,
    readyState: 4,
    buffered: makeBuffered([0, 15]),
    getVideoPlaybackQuality: vi.fn(() => ({
      totalVideoFrames: 1000,
      droppedVideoFrames: 10,
    })),
    addEventListener: vi.fn((event: string, handler: Function) => {
      if (!listeners.has(event)) listeners.set(event, []);
      listeners.get(event)!.push(handler);
    }),
    removeEventListener: vi.fn((event: string, handler: Function) => {
      const list = listeners.get(event);
      if (list) {
        const idx = list.indexOf(handler);
        if (idx >= 0) list.splice(idx, 1);
      }
    }),
    _fire(event: string) {
      listeners.get(event)?.forEach((h) => h());
    },
    ...overrides,
  } as unknown as HTMLVideoElement & { _fire: (e: string) => void };
}

// ---------------------------------------------------------------------------
// Tests
// ---------------------------------------------------------------------------

describe("QualityMonitor", () => {
  let perfNowMs: number;

  beforeEach(() => {
    vi.useFakeTimers();
    perfNowMs = 1000;
    vi.spyOn(performance, "now").mockImplementation(() => perfNowMs);
    vi.spyOn(Date, "now").mockReturnValue(100_000);
    vi.spyOn(console, "warn").mockImplementation(() => {});
  });

  afterEach(() => {
    vi.useRealTimers();
    vi.restoreAllMocks();
  });

  // ===========================================================================
  // Protocol thresholds
  // ===========================================================================
  describe("protocol thresholds", () => {
    it("defaults to unknown protocol", () => {
      const qm = new QualityMonitor();
      expect(qm.getProtocol()).toBe("unknown");
      expect(qm.getPlaybackScoreThreshold()).toBe(0.75);
    });

    it("webrtc threshold is stricter", () => {
      const qm = new QualityMonitor({ protocol: "webrtc" });
      expect(qm.getPlaybackScoreThreshold()).toBe(0.95);
    });

    it("hls threshold is lenient", () => {
      const qm = new QualityMonitor({ protocol: "hls" });
      expect(qm.getPlaybackScoreThreshold()).toBe(0.75);
    });

    it("custom threshold overrides protocol", () => {
      const qm = new QualityMonitor({ protocol: "webrtc", playbackScoreThreshold: 0.5 });
      expect(qm.getPlaybackScoreThreshold()).toBe(0.5);
    });

    it("setProtocol changes threshold", () => {
      const qm = new QualityMonitor();
      qm.setProtocol("webrtc");
      expect(qm.getProtocol()).toBe("webrtc");
      expect(qm.getPlaybackScoreThreshold()).toBe(PROTOCOL_THRESHOLDS.webrtc);
    });

    it("setPlaybackScoreThreshold overrides protocol", () => {
      const qm = new QualityMonitor({ protocol: "webrtc" });
      qm.setPlaybackScoreThreshold(0.3);
      expect(qm.getPlaybackScoreThreshold()).toBe(0.3);
    });

    it("setPlaybackScoreThreshold(null) reverts to protocol", () => {
      const qm = new QualityMonitor({ protocol: "webrtc", playbackScoreThreshold: 0.3 });
      qm.setPlaybackScoreThreshold(null);
      expect(qm.getPlaybackScoreThreshold()).toBe(0.95);
    });
  });

  // ===========================================================================
  // start / stop / isMonitoring
  // ===========================================================================
  describe("start / stop", () => {
    it("starts monitoring and takes initial sample", () => {
      const onSample = vi.fn();
      const qm = new QualityMonitor({ onSample });
      const video = createMockVideo();

      qm.start(video);
      expect(qm.isMonitoring()).toBe(true);
      expect(onSample).toHaveBeenCalledTimes(1);
      qm.stop();
    });

    it("sampling interval fires repeatedly", () => {
      const onSample = vi.fn();
      const qm = new QualityMonitor({ onSample, sampleInterval: 200 });
      const video = createMockVideo();

      qm.start(video);
      expect(onSample).toHaveBeenCalledTimes(1);

      vi.advanceTimersByTime(200);
      expect(onSample).toHaveBeenCalledTimes(2);

      vi.advanceTimersByTime(200);
      expect(onSample).toHaveBeenCalledTimes(3);
      qm.stop();
    });

    it("stop cleans up and stops monitoring", () => {
      const qm = new QualityMonitor();
      const video = createMockVideo();

      qm.start(video);
      expect(qm.isMonitoring()).toBe(true);

      qm.stop();
      expect(qm.isMonitoring()).toBe(false);
      expect(video.removeEventListener).toHaveBeenCalled();
    });

    it("start cleans up previous before restarting", () => {
      const qm = new QualityMonitor();
      const video1 = createMockVideo();
      const video2 = createMockVideo();

      qm.start(video1);
      qm.start(video2);

      expect(video1.removeEventListener).toHaveBeenCalled();
      expect(qm.isMonitoring()).toBe(true);
      qm.stop();
    });
  });

  // ===========================================================================
  // Quality calculation — buffer
  // ===========================================================================
  describe("quality score — buffer", () => {
    it("full buffer gives 100 minus frame drop penalty", () => {
      const qm = new QualityMonitor();
      const video = createMockVideo({
        currentTime: 5,
        buffered: makeBuffered([0, 15]),
        getVideoPlaybackQuality: vi.fn(() => ({
          totalVideoFrames: 1000,
          droppedVideoFrames: 0,
        })),
      });

      qm.start(video);
      const q = qm.getCurrentQuality();
      expect(q).not.toBeNull();
      expect(q!.score).toBe(100);
      qm.stop();
    });

    it("low buffer penalizes score", () => {
      const qm = new QualityMonitor();
      const video = createMockVideo({
        currentTime: 5,
        buffered: makeBuffered([0, 5.5]), // 0.5s ahead, minBuffer is 2
        getVideoPlaybackQuality: vi.fn(() => ({
          totalVideoFrames: 1000,
          droppedVideoFrames: 0,
        })),
      });

      qm.start(video);
      const q = qm.getCurrentQuality();
      expect(q!.score).toBeLessThan(100);
      expect(q!.bufferedAhead).toBeCloseTo(0.5, 1);
      qm.stop();
    });

    it("no buffer range returns 0 bufferedAhead", () => {
      const qm = new QualityMonitor();
      const video = createMockVideo({
        currentTime: 5,
        buffered: makeBuffered(),
        getVideoPlaybackQuality: vi.fn(() => ({
          totalVideoFrames: 0,
          droppedVideoFrames: 0,
        })),
      });

      qm.start(video);
      const q = qm.getCurrentQuality();
      expect(q!.bufferedAhead).toBe(0);
      qm.stop();
    });
  });

  // ===========================================================================
  // Quality calculation — stalls
  // ===========================================================================
  describe("quality score — stalls", () => {
    it("waiting event increments stall count", () => {
      const qm = new QualityMonitor({ sampleInterval: 1000 });
      const video = createMockVideo();

      qm.start(video);
      video._fire("waiting");
      video._fire("waiting");

      vi.advanceTimersByTime(1000);
      const q = qm.getCurrentQuality();
      expect(q!.stallCount).toBe(2);
      qm.stop();
    });

    it("playing after waiting records stall duration", () => {
      const qm = new QualityMonitor({ sampleInterval: 1000 });
      const video = createMockVideo();

      qm.start(video);

      // Stall starts
      perfNowMs = 2000;
      video._fire("waiting");

      // Stall ends 500ms later
      perfNowMs = 2500;
      video._fire("playing");

      expect(qm.getTotalStallMs()).toBe(500);
      qm.stop();
    });

    it("canplay also ends stall", () => {
      const qm = new QualityMonitor({ sampleInterval: 1000 });
      const video = createMockVideo();

      qm.start(video);
      perfNowMs = 1000;
      video._fire("waiting");
      perfNowMs = 1200;
      video._fire("canplay");

      expect(qm.getTotalStallMs()).toBe(200);
      qm.stop();
    });

    it("D4: duration-weighted stall penalty", () => {
      const qm = new QualityMonitor({ sampleInterval: 1000 });
      const video = createMockVideo({
        getVideoPlaybackQuality: vi.fn(() => ({
          totalVideoFrames: 1000,
          droppedVideoFrames: 0,
        })),
      });

      qm.start(video);

      // Simulate 2 stalls of 1s each
      perfNowMs = 2000;
      video._fire("waiting");
      perfNowMs = 3000;
      video._fire("playing");
      perfNowMs = 4000;
      video._fire("waiting");
      perfNowMs = 5000;
      video._fire("playing");

      vi.advanceTimersByTime(1000);
      const q = qm.getCurrentQuality();
      // 2 stalls * 5pts + 2s * 2pts = 14pts stall penalty
      // Score should be 100 - 14 = 86
      expect(q!.score).toBe(86);
      qm.stop();
    });

    it("resetStallCounters clears counters", () => {
      const qm = new QualityMonitor({ sampleInterval: 1000 });
      const video = createMockVideo();

      qm.start(video);
      video._fire("waiting");
      perfNowMs = 2000;
      video._fire("playing");

      qm.resetStallCounters();
      expect(qm.getTotalStallMs()).toBe(0);
      qm.stop();
    });
  });

  // ===========================================================================
  // Quality calculation — frame drops
  // ===========================================================================
  describe("quality score — frame drops", () => {
    it("high frame drop rate penalizes score", () => {
      const qm = new QualityMonitor();
      const video = createMockVideo({
        getVideoPlaybackQuality: vi.fn(() => ({
          totalVideoFrames: 100,
          droppedVideoFrames: 10, // 10% drop rate
        })),
      });

      qm.start(video);
      const q = qm.getCurrentQuality();
      // 10% * 2 = 20 point penalty
      expect(q!.frameDropRate).toBe(10);
      qm.stop();
    });
  });

  // ===========================================================================
  // Quality calculation — latency
  // ===========================================================================
  describe("quality score — latency", () => {
    it("live stream reports latency", () => {
      const qm = new QualityMonitor();
      const video = createMockVideo({
        currentTime: 5,
        duration: Infinity,
        buffered: makeBuffered([0, 15]),
        getVideoPlaybackQuality: vi.fn(() => ({
          totalVideoFrames: 1000,
          droppedVideoFrames: 0,
        })),
      });

      qm.start(video);
      const q = qm.getCurrentQuality();
      // latency = (15 - 5) * 1000 = 10000ms
      expect(q!.latency).toBe(10000);
      qm.stop();
    });

    it("latency above 5s penalizes score", () => {
      const qm = new QualityMonitor();
      const video = createMockVideo({
        currentTime: 0,
        duration: Infinity,
        buffered: makeBuffered([0, 15]), // 15s latency
        getVideoPlaybackQuality: vi.fn(() => ({
          totalVideoFrames: 1000,
          droppedVideoFrames: 0,
        })),
      });

      qm.start(video);
      const q = qm.getCurrentQuality();
      // latency = 15000ms, penalty = min(10, (15000-5000)/1000) = 10
      expect(q!.score).toBe(90);
      qm.stop();
    });

    it("VOD stream has zero latency", () => {
      const qm = new QualityMonitor();
      const video = createMockVideo({
        duration: 120,
      });

      qm.start(video);
      const q = qm.getCurrentQuality();
      expect(q!.latency).toBe(0);
      qm.stop();
    });
  });

  // ===========================================================================
  // onQualityDegraded callback
  // ===========================================================================
  describe("onQualityDegraded", () => {
    it("fires when score below minScore", () => {
      const onQualityDegraded = vi.fn();
      const qm = new QualityMonitor({
        onQualityDegraded,
        thresholds: { minScore: 95 },
      });

      // 10% frame drops = 20pt penalty = score 80 < 95
      const video = createMockVideo({
        getVideoPlaybackQuality: vi.fn(() => ({
          totalVideoFrames: 100,
          droppedVideoFrames: 10,
        })),
      });

      qm.start(video);
      expect(onQualityDegraded).toHaveBeenCalledTimes(1);
      qm.stop();
    });

    it("fires when stalls exceed maxStalls", () => {
      const onQualityDegraded = vi.fn();
      const qm = new QualityMonitor({
        onQualityDegraded,
        thresholds: { maxStalls: 1 },
        sampleInterval: 500,
      });

      const video = createMockVideo({
        getVideoPlaybackQuality: vi.fn(() => ({
          totalVideoFrames: 1000,
          droppedVideoFrames: 0,
        })),
      });

      qm.start(video);
      // First sample is fine
      onQualityDegraded.mockClear();

      video._fire("waiting");
      video._fire("waiting");
      vi.advanceTimersByTime(500);

      expect(onQualityDegraded).toHaveBeenCalled();
      qm.stop();
    });

    it("fires when buffer below minBuffer", () => {
      const onQualityDegraded = vi.fn();
      const qm = new QualityMonitor({
        onQualityDegraded,
        thresholds: { minBuffer: 10 },
      });

      const video = createMockVideo({
        currentTime: 5,
        buffered: makeBuffered([0, 10]), // 5s ahead < 10s minBuffer
        getVideoPlaybackQuality: vi.fn(() => ({
          totalVideoFrames: 1000,
          droppedVideoFrames: 0,
        })),
      });

      qm.start(video);
      expect(onQualityDegraded).toHaveBeenCalledTimes(1);
      qm.stop();
    });
  });

  // ===========================================================================
  // Playback score (MistPlayer-style)
  // ===========================================================================
  describe("playback score", () => {
    it("defaults to 1.0", () => {
      const qm = new QualityMonitor();
      expect(qm.getPlaybackScore()).toBe(1.0);
    });

    it("isPlaybackPoor when score below threshold", () => {
      const qm = new QualityMonitor({ protocol: "hls", playbackScoreThreshold: 0.9 });
      // Score starts at 1.0, so not poor
      expect(qm.isPlaybackPoor()).toBe(false);
    });

    it("resetPlaybackScore resets to 1.0", () => {
      const qm = new QualityMonitor();
      qm.resetPlaybackScore();
      expect(qm.getPlaybackScore()).toBe(1.0);
    });
  });

  // ===========================================================================
  // Fallback tracking
  // ===========================================================================
  describe("fallback tracking", () => {
    it("triggers after N consecutive poor samples", () => {
      const onFallbackRequest = vi.fn();
      const qm = new QualityMonitor({
        onFallbackRequest,
        poorSamplesBeforeFallback: 3,
        sampleInterval: 100,
        playbackScoreThreshold: 2.0, // Force "poor" state
      });

      const video = createMockVideo({
        getVideoPlaybackQuality: vi.fn(() => ({
          totalVideoFrames: 1000,
          droppedVideoFrames: 0,
        })),
      });

      qm.start(video); // sample 1
      vi.advanceTimersByTime(100); // sample 2
      vi.advanceTimersByTime(100); // sample 3

      expect(onFallbackRequest).toHaveBeenCalledTimes(1);
      expect(onFallbackRequest).toHaveBeenCalledWith(
        expect.objectContaining({
          consecutivePoorSamples: 3,
        })
      );
      qm.stop();
    });

    it("does not re-trigger after first fallback", () => {
      const onFallbackRequest = vi.fn();
      const qm = new QualityMonitor({
        onFallbackRequest,
        poorSamplesBeforeFallback: 2,
        sampleInterval: 100,
        playbackScoreThreshold: 2.0,
      });

      const video = createMockVideo({
        getVideoPlaybackQuality: vi.fn(() => ({
          totalVideoFrames: 1000,
          droppedVideoFrames: 0,
        })),
      });

      qm.start(video);
      vi.advanceTimersByTime(100); // sample 2 -> triggers
      vi.advanceTimersByTime(100); // sample 3 -> should not re-trigger

      expect(onFallbackRequest).toHaveBeenCalledTimes(1);
      qm.stop();
    });

    it("resetFallbackState allows re-trigger", () => {
      const onFallbackRequest = vi.fn();
      const qm = new QualityMonitor({
        onFallbackRequest,
        poorSamplesBeforeFallback: 2,
        sampleInterval: 100,
        playbackScoreThreshold: 2.0,
      });

      const video = createMockVideo({
        getVideoPlaybackQuality: vi.fn(() => ({
          totalVideoFrames: 1000,
          droppedVideoFrames: 0,
        })),
      });

      qm.start(video);
      vi.advanceTimersByTime(100);
      expect(onFallbackRequest).toHaveBeenCalledTimes(1);

      qm.resetFallbackState();
      vi.advanceTimersByTime(100);
      vi.advanceTimersByTime(100);
      expect(onFallbackRequest).toHaveBeenCalledTimes(2);
      qm.stop();
    });

    it("getConsecutivePoorSamples returns count", () => {
      const qm = new QualityMonitor({
        playbackScoreThreshold: 2.0,
        sampleInterval: 100,
      });

      const video = createMockVideo({
        getVideoPlaybackQuality: vi.fn(() => ({
          totalVideoFrames: 1000,
          droppedVideoFrames: 0,
        })),
      });

      qm.start(video);
      expect(qm.getConsecutivePoorSamples()).toBeGreaterThanOrEqual(1);
      qm.stop();
    });

    it("hasFallbackTriggered returns false initially", () => {
      const qm = new QualityMonitor();
      expect(qm.hasFallbackTriggered()).toBe(false);
    });
  });

  // ===========================================================================
  // History + averages
  // ===========================================================================
  describe("history and averages", () => {
    it("getCurrentQuality returns null before start", () => {
      const qm = new QualityMonitor();
      expect(qm.getCurrentQuality()).toBeNull();
    });

    it("getAverageQuality returns null with no history", () => {
      const qm = new QualityMonitor();
      expect(qm.getAverageQuality()).toBeNull();
    });

    it("getHistory returns a copy", () => {
      const qm = new QualityMonitor();
      const video = createMockVideo();

      qm.start(video);
      const h1 = qm.getHistory();
      const h2 = qm.getHistory();
      expect(h1).toEqual(h2);
      expect(h1).not.toBe(h2);
      qm.stop();
    });

    it("getAverageQuality averages all samples", () => {
      const qm = new QualityMonitor({ sampleInterval: 100 });
      const video = createMockVideo({
        getVideoPlaybackQuality: vi.fn(() => ({
          totalVideoFrames: 1000,
          droppedVideoFrames: 0,
        })),
      });

      qm.start(video);
      vi.advanceTimersByTime(100);
      vi.advanceTimersByTime(100);

      const avg = qm.getAverageQuality();
      expect(avg).not.toBeNull();
      expect(avg!.stallCount).toBe(0);
      qm.stop();
    });

    it("history is capped at rolling window", () => {
      const qm = new QualityMonitor({ sampleInterval: 10 });
      const video = createMockVideo({
        getVideoPlaybackQuality: vi.fn(() => ({
          totalVideoFrames: 1000,
          droppedVideoFrames: 0,
        })),
      });

      qm.start(video);
      // 20 is ROLLING_WINDOW_SIZE, generate 25 samples
      for (let i = 0; i < 25; i++) {
        vi.advanceTimersByTime(10);
      }

      // History should not exceed 20
      expect(qm.getHistory().length).toBeLessThanOrEqual(20);
      qm.stop();
    });
  });
});
