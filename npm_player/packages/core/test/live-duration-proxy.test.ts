import { describe, expect, it, vi, beforeEach, afterEach } from "vitest";

import { LiveDurationProxy, createLiveVideoProxy } from "../src/core/LiveDurationProxy";

// Minimal mock video element with controllable properties
function createMockVideo(
  overrides: Partial<{
    duration: number;
    currentTime: number;
    buffered: { length: number; start(i: number): number; end(i: number): number };
  }> = {}
) {
  const listeners = new Map<string, Set<Function>>();

  const video = {
    duration: overrides.duration ?? 120,
    currentTime: overrides.currentTime ?? 0,
    buffered: overrides.buffered ?? { length: 0, start: () => 0, end: () => 0 },

    addEventListener: vi.fn((event: string, handler: Function) => {
      if (!listeners.has(event)) listeners.set(event, new Set());
      listeners.get(event)!.add(handler);
    }),
    removeEventListener: vi.fn((event: string, handler: Function) => {
      listeners.get(event)?.delete(handler);
    }),

    _fire(event: string) {
      listeners.get(event)?.forEach((h) => h());
    },
    _listeners: listeners,
  };

  return video as unknown as HTMLVideoElement & { _fire(e: string): void };
}

function makeBuffered(...ranges: Array<[number, number]>) {
  return {
    length: ranges.length,
    start: (i: number) => ranges[i][0],
    end: (i: number) => ranges[i][1],
  };
}

describe("LiveDurationProxy", () => {
  let nowMs: number;

  beforeEach(() => {
    nowMs = 50000;
    vi.spyOn(Date, "now").mockImplementation(() => nowMs);
  });

  afterEach(() => {
    vi.restoreAllMocks();
  });

  // ===========================================================================
  // isLive
  // ===========================================================================
  describe("isLive", () => {
    it("returns true for Infinity duration", () => {
      const video = createMockVideo({ duration: Infinity });
      const proxy = new LiveDurationProxy(video);
      expect(proxy.isLive()).toBe(true);
      proxy.destroy();
    });

    it("returns true for NaN duration", () => {
      const video = createMockVideo({ duration: NaN });
      const proxy = new LiveDurationProxy(video);
      expect(proxy.isLive()).toBe(true);
      proxy.destroy();
    });

    it("returns false for finite duration", () => {
      const video = createMockVideo({ duration: 120 });
      const proxy = new LiveDurationProxy(video);
      expect(proxy.isLive()).toBe(false);
      proxy.destroy();
    });
  });

  // ===========================================================================
  // getDuration
  // ===========================================================================
  describe("getDuration", () => {
    it("returns native duration for VOD", () => {
      const video = createMockVideo({ duration: 300 });
      const proxy = new LiveDurationProxy(video);
      expect(proxy.getDuration()).toBe(300);
      proxy.destroy();
    });

    it("returns calculated duration for live (bufferEnd + elapsed)", () => {
      const video = createMockVideo({
        duration: Infinity,
        currentTime: 10,
        buffered: makeBuffered([0, 30]),
      });
      const proxy = new LiveDurationProxy(video);

      // Trigger progress to set lastProgressTime
      video._fire("progress");
      const progressTime = nowMs;

      // Advance time by 5 seconds
      nowMs = progressTime + 5000;
      video._fire("timeupdate");

      // Duration = bufferEnd(30) + elapsed(5) = 35
      expect(proxy.getDuration()).toBeCloseTo(35, 0);
      proxy.destroy();
    });

    it("returns 0 for live with no buffer and no progress", () => {
      const video = createMockVideo({ duration: Infinity });
      const proxy = new LiveDurationProxy(video);
      // No progress fired, so elapsed=0, bufferEnd=0 → duration=0
      expect(proxy.getDuration()).toBe(0);
      proxy.destroy();
    });
  });

  // ===========================================================================
  // getBufferEnd
  // ===========================================================================
  describe("getBufferEnd", () => {
    it("returns 0 when no buffered ranges", () => {
      const video = createMockVideo({ duration: Infinity });
      const proxy = new LiveDurationProxy(video);
      expect(proxy.getBufferEnd()).toBe(0);
      proxy.destroy();
    });

    it("returns end of range containing currentTime", () => {
      const video = createMockVideo({
        duration: Infinity,
        currentTime: 15,
        buffered: makeBuffered([0, 10], [12, 25]),
      });
      const proxy = new LiveDurationProxy(video);
      expect(proxy.getBufferEnd()).toBe(25);
      proxy.destroy();
    });

    it("returns end of last range if currentTime not in any range", () => {
      const video = createMockVideo({
        duration: Infinity,
        currentTime: 50,
        buffered: makeBuffered([0, 10], [20, 30]),
      });
      const proxy = new LiveDurationProxy(video);
      expect(proxy.getBufferEnd()).toBe(30);
      proxy.destroy();
    });

    it("returns end of single range", () => {
      const video = createMockVideo({
        duration: Infinity,
        currentTime: 5,
        buffered: makeBuffered([0, 20]),
      });
      const proxy = new LiveDurationProxy(video);
      expect(proxy.getBufferEnd()).toBe(20);
      proxy.destroy();
    });
  });

  // ===========================================================================
  // getLiveEdge / getLatency
  // ===========================================================================
  describe("getLiveEdge", () => {
    it("equals bufferEnd when liveOffset is 0", () => {
      const video = createMockVideo({
        duration: Infinity,
        currentTime: 5,
        buffered: makeBuffered([0, 30]),
      });
      const proxy = new LiveDurationProxy(video);
      expect(proxy.getLiveEdge()).toBe(30);
      proxy.destroy();
    });

    it("subtracts liveOffset", () => {
      const video = createMockVideo({
        duration: Infinity,
        currentTime: 5,
        buffered: makeBuffered([0, 30]),
      });
      const proxy = new LiveDurationProxy(video, { liveOffset: 3 });
      expect(proxy.getLiveEdge()).toBe(27);
      proxy.destroy();
    });
  });

  describe("getLatency", () => {
    it("returns 0 for VOD", () => {
      const video = createMockVideo({ duration: 120, currentTime: 60 });
      const proxy = new LiveDurationProxy(video);
      expect(proxy.getLatency()).toBe(0);
      proxy.destroy();
    });

    it("returns distance from live edge", () => {
      const video = createMockVideo({
        duration: Infinity,
        currentTime: 25,
        buffered: makeBuffered([0, 30]),
      });
      const proxy = new LiveDurationProxy(video);
      expect(proxy.getLatency()).toBe(5);
      proxy.destroy();
    });

    it("never returns negative", () => {
      const video = createMockVideo({
        duration: Infinity,
        currentTime: 35,
        buffered: makeBuffered([0, 30]),
      });
      const proxy = new LiveDurationProxy(video);
      expect(proxy.getLatency()).toBe(0);
      proxy.destroy();
    });
  });

  // ===========================================================================
  // seek
  // ===========================================================================
  describe("seek", () => {
    it("sets currentTime directly for VOD", () => {
      const video = createMockVideo({ duration: 120 });
      const proxy = new LiveDurationProxy(video);
      proxy.seek(60);
      expect(video.currentTime).toBe(60);
      proxy.destroy();
    });

    it("sets currentTime directly when constrainSeek is false", () => {
      const video = createMockVideo({ duration: Infinity });
      const proxy = new LiveDurationProxy(video, { constrainSeek: false });
      proxy.seek(999);
      expect(video.currentTime).toBe(999);
      proxy.destroy();
    });

    it("clamps to buffered range for live", () => {
      const video = createMockVideo({
        duration: Infinity,
        currentTime: 15,
        buffered: makeBuffered([10, 30]),
      });
      const proxy = new LiveDurationProxy(video);

      // Below buffer start
      proxy.seek(5);
      expect(video.currentTime).toBe(10);

      // Above live edge
      proxy.seek(100);
      expect(video.currentTime).toBe(30);

      // Within range
      proxy.seek(20);
      expect(video.currentTime).toBe(20);

      proxy.destroy();
    });

    it("sets currentTime directly when no buffered ranges (live)", () => {
      const video = createMockVideo({ duration: Infinity });
      const proxy = new LiveDurationProxy(video);
      proxy.seek(42);
      expect(video.currentTime).toBe(42);
      proxy.destroy();
    });
  });

  // ===========================================================================
  // jumpToLive
  // ===========================================================================
  describe("jumpToLive", () => {
    it("sets currentTime to live edge", () => {
      const video = createMockVideo({
        duration: Infinity,
        currentTime: 10,
        buffered: makeBuffered([0, 50]),
      });
      const proxy = new LiveDurationProxy(video);
      proxy.jumpToLive();
      expect(video.currentTime).toBe(50);
      proxy.destroy();
    });

    it("does nothing for VOD", () => {
      const video = createMockVideo({ duration: 120, currentTime: 30 });
      const proxy = new LiveDurationProxy(video);
      proxy.jumpToLive();
      expect(video.currentTime).toBe(30);
      proxy.destroy();
    });
  });

  // ===========================================================================
  // isAtLiveEdge
  // ===========================================================================
  describe("isAtLiveEdge", () => {
    it("returns true within threshold", () => {
      const video = createMockVideo({
        duration: Infinity,
        currentTime: 49,
        buffered: makeBuffered([0, 50]),
      });
      const proxy = new LiveDurationProxy(video);
      expect(proxy.isAtLiveEdge(2)).toBe(true);
      proxy.destroy();
    });

    it("returns false beyond threshold", () => {
      const video = createMockVideo({
        duration: Infinity,
        currentTime: 40,
        buffered: makeBuffered([0, 50]),
      });
      const proxy = new LiveDurationProxy(video);
      expect(proxy.isAtLiveEdge(2)).toBe(false);
      proxy.destroy();
    });

    it("returns false for VOD", () => {
      const video = createMockVideo({ duration: 120, currentTime: 119 });
      const proxy = new LiveDurationProxy(video);
      expect(proxy.isAtLiveEdge()).toBe(false);
      proxy.destroy();
    });

    it("uses default threshold of 2", () => {
      const video = createMockVideo({
        duration: Infinity,
        currentTime: 48.5,
        buffered: makeBuffered([0, 50]),
      });
      const proxy = new LiveDurationProxy(video);
      expect(proxy.isAtLiveEdge()).toBe(true);
      proxy.destroy();
    });
  });

  // ===========================================================================
  // getState
  // ===========================================================================
  describe("getState", () => {
    it("returns VOD state", () => {
      const video = createMockVideo({ duration: 120 });
      const proxy = new LiveDurationProxy(video);
      const state = proxy.getState();
      expect(state.isLive).toBe(false);
      expect(state.duration).toBe(120);
      expect(state.elapsed).toBe(0);
      proxy.destroy();
    });

    it("returns live state with elapsed time", () => {
      const video = createMockVideo({
        duration: Infinity,
        currentTime: 5,
        buffered: makeBuffered([0, 20]),
      });
      const proxy = new LiveDurationProxy(video);
      video._fire("progress");
      nowMs += 3000;

      const state = proxy.getState();
      expect(state.isLive).toBe(true);
      expect(state.bufferEnd).toBe(20);
      expect(state.elapsed).toBeCloseTo(3, 0);
      proxy.destroy();
    });
  });

  // ===========================================================================
  // onDurationChange callback
  // ===========================================================================
  describe("onDurationChange callback", () => {
    it("fires when duration changes significantly", () => {
      const onDurationChange = vi.fn();
      const video = createMockVideo({
        duration: Infinity,
        currentTime: 5,
        buffered: makeBuffered([0, 10]),
      });
      const proxy = new LiveDurationProxy(video, { onDurationChange });

      // Initial updateDuration in constructor fires it (0 → 10 is >0.1 change)
      onDurationChange.mockClear();

      // Update buffer significantly
      (video as any).buffered = makeBuffered([0, 20]);
      video._fire("progress");

      expect(onDurationChange).toHaveBeenCalledWith(expect.closeTo(20, 0));
      proxy.destroy();
    });
  });

  // ===========================================================================
  // Event listeners
  // ===========================================================================
  describe("event listeners", () => {
    it("listens on progress, timeupdate, durationchange, loadedmetadata", () => {
      const video = createMockVideo();
      const proxy = new LiveDurationProxy(video);

      expect(video.addEventListener).toHaveBeenCalledWith("progress", expect.any(Function));
      expect(video.addEventListener).toHaveBeenCalledWith("timeupdate", expect.any(Function));
      expect(video.addEventListener).toHaveBeenCalledWith("durationchange", expect.any(Function));
      expect(video.addEventListener).toHaveBeenCalledWith("loadedmetadata", expect.any(Function));
      proxy.destroy();
    });

    it("destroy removes all listeners", () => {
      const video = createMockVideo();
      const proxy = new LiveDurationProxy(video);
      proxy.destroy();

      expect(video.removeEventListener).toHaveBeenCalledWith("progress", expect.any(Function));
      expect(video.removeEventListener).toHaveBeenCalledWith("timeupdate", expect.any(Function));
      expect(video.removeEventListener).toHaveBeenCalledWith(
        "durationchange",
        expect.any(Function)
      );
      expect(video.removeEventListener).toHaveBeenCalledWith(
        "loadedmetadata",
        expect.any(Function)
      );
    });
  });

  // ===========================================================================
  // createLiveVideoProxy
  // ===========================================================================
  describe("createLiveVideoProxy", () => {
    it("proxy.duration returns calculated duration for live", () => {
      const video = createMockVideo({
        duration: Infinity,
        currentTime: 5,
        buffered: makeBuffered([0, 25]),
      });
      const { proxy, controller } = createLiveVideoProxy(video);

      // proxy.duration should use controller.getDuration()
      expect(proxy.duration).toBe(controller.getDuration());
      controller.destroy();
    });

    it("proxy.duration returns native duration for VOD", () => {
      const video = createMockVideo({ duration: 200 });
      const { proxy, controller } = createLiveVideoProxy(video);
      expect(proxy.duration).toBe(200);
      controller.destroy();
    });

    it("setting proxy.currentTime on live calls controller.seek()", () => {
      const video = createMockVideo({
        duration: Infinity,
        currentTime: 10,
        buffered: makeBuffered([5, 40]),
      });
      const { proxy, controller } = createLiveVideoProxy(video);
      const seekSpy = vi.spyOn(controller, "seek");

      proxy.currentTime = 20;
      expect(seekSpy).toHaveBeenCalledWith(20);
      controller.destroy();
    });

    it("setting proxy.currentTime on VOD goes directly to video", () => {
      const video = createMockVideo({ duration: 120 });
      const { proxy, controller } = createLiveVideoProxy(video);
      proxy.currentTime = 60;
      expect(video.currentTime).toBe(60);
      controller.destroy();
    });

    it("other proxy properties pass through", () => {
      const video = createMockVideo({ duration: Infinity });
      const { proxy, controller } = createLiveVideoProxy(video);
      expect(typeof proxy.addEventListener).toBe("function");
      controller.destroy();
    });
  });
});
