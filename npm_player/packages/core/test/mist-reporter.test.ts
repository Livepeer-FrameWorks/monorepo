import { describe, expect, it, vi, beforeEach, afterEach } from "vitest";

import { MistReporter, type MistReporterOptions } from "../src/core/MistReporter";

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

function createMockVideo() {
  const listeners = new Map<string, Function[]>();
  return {
    videoHeight: 1080,
    videoWidth: 1920,
    parentElement: { clientHeight: 720, clientWidth: 1280 },
    addEventListener: vi.fn((event: string, handler: Function) => {
      if (!listeners.has(event)) listeners.set(event, []);
      listeners.get(event)!.push(handler);
    }),
    removeEventListener: vi.fn(),
    _fire(event: string, detail: Record<string, unknown> = {}) {
      const handlers = listeners.get(event);
      if (!handlers) return;
      const evt = { ...detail };
      handlers.forEach((h) => h(evt));
    },
    _listeners: listeners,
  };
}

function createMockSocket(readyState = 1) {
  return {
    readyState,
    send: vi.fn(),
    OPEN: 1,
    close: vi.fn(),
  };
}

// ---------------------------------------------------------------------------
// Tests
// ---------------------------------------------------------------------------

describe("MistReporter", () => {
  let origWebSocket: any;

  beforeEach(() => {
    vi.useFakeTimers();
    vi.spyOn(performance, "now").mockReturnValue(1000);
    vi.spyOn(Date, "now").mockReturnValue(50000);
    origWebSocket = (globalThis as any).WebSocket;
    (globalThis as any).WebSocket = { OPEN: 1, CONNECTING: 0, CLOSED: 3 };
  });

  afterEach(() => {
    vi.useRealTimers();
    vi.restoreAllMocks();
    (globalThis as any).WebSocket = origWebSocket;
  });

  // ===========================================================================
  // Constructor
  // ===========================================================================
  describe("constructor", () => {
    it("creates with defaults", () => {
      const r = new MistReporter();
      const stats = r.getStats();
      expect(stats.nWaiting).toBe(0);
      expect(stats.nStalled).toBe(0);
      expect(stats.nError).toBe(0);
      expect(stats.playbackScore).toBe(1);
      expect(stats.autoplay).toBeNull();
      expect(stats.firstPlayback).toBeNull();
    });

    it("accepts custom options", () => {
      const logs: string[] = ["boot"];
      const r = new MistReporter({
        reportInterval: 10000,
        batchFlushInterval: 2000,
        bootMs: 40000,
        logs,
      });
      const stats = r.getStats();
      expect(stats.nLog).toBe(1);
    });
  });

  // ===========================================================================
  // getStats
  // ===========================================================================
  describe("getStats", () => {
    it("returns default stats", () => {
      const r = new MistReporter();
      const stats = r.getStats();

      expect(stats.nWaiting).toBe(0);
      expect(stats.timeWaiting).toBe(0);
      expect(stats.nStalled).toBe(0);
      expect(stats.timeStalled).toBe(0);
      expect(stats.timeUnpaused).toBe(0);
      expect(stats.nError).toBe(0);
      expect(stats.lastError).toBeNull();
      expect(stats.firstPlayback).toBeNull();
      expect(stats.playbackScore).toBe(1);
      expect(stats.autoplay).toBeNull();
      expect(stats.videoHeight).toBeNull();
      expect(stats.videoWidth).toBeNull();
      expect(stats.playerHeight).toBeNull();
      expect(stats.playerWidth).toBeNull();
      expect(stats.tracks).toBeNull();
      expect(stats.nLog).toBe(0);
    });
  });

  // ===========================================================================
  // set / add
  // ===========================================================================
  describe("set / add", () => {
    it("set nWaiting", () => {
      const r = new MistReporter();
      r.set("nWaiting", 5);
      expect(r.getStats().nWaiting).toBe(5);
    });

    it("set nStalled", () => {
      const r = new MistReporter();
      r.set("nStalled", 3);
      expect(r.getStats().nStalled).toBe(3);
    });

    it("set nError", () => {
      const r = new MistReporter();
      r.set("nError", 2);
      expect(r.getStats().nError).toBe(2);
    });

    it("set lastError", () => {
      const r = new MistReporter();
      r.set("lastError", "decode error");
      expect(r.getStats().lastError).toBe("decode error");
    });

    it("set firstPlayback", () => {
      const r = new MistReporter();
      r.set("firstPlayback", 1234);
      expect(r.getStats().firstPlayback).toBe(1234);
    });

    it("set playbackScore", () => {
      const r = new MistReporter();
      r.set("playbackScore", 0.85);
      expect(r.getStats().playbackScore).toBe(0.9); // rounded to 1 decimal
    });

    it("set autoplay", () => {
      const r = new MistReporter();
      r.set("autoplay", "muted");
      expect(r.getStats().autoplay).toBe("muted");
    });

    it("set tracks", () => {
      const r = new MistReporter();
      r.set("tracks", "video1,audio1");
      expect(r.getStats().tracks).toBe("video1,audio1");
    });

    it("add increments nWaiting", () => {
      const r = new MistReporter();
      r.add("nWaiting");
      r.add("nWaiting", 2);
      expect(r.getStats().nWaiting).toBe(3);
    });

    it("add increments nStalled", () => {
      const r = new MistReporter();
      r.add("nStalled");
      expect(r.getStats().nStalled).toBe(1);
    });

    it("add increments nError", () => {
      const r = new MistReporter();
      r.add("nError", 5);
      expect(r.getStats().nError).toBe(5);
    });
  });

  // ===========================================================================
  // setPlaybackScore / setAutoplayStatus / setTracks
  // ===========================================================================
  describe("convenience setters", () => {
    it("setPlaybackScore updates playbackScore", () => {
      const r = new MistReporter();
      r.setPlaybackScore(0.7);
      expect(r.getStats().playbackScore).toBe(0.7);
    });

    it("setAutoplayStatus updates autoplay", () => {
      const r = new MistReporter();
      r.setAutoplayStatus("failed");
      expect(r.getStats().autoplay).toBe("failed");
    });

    it("setTracks joins array", () => {
      const r = new MistReporter();
      r.setTracks(["video1", "audio2"]);
      expect(r.getStats().tracks).toBe("video1,audio2");
    });
  });

  // ===========================================================================
  // log
  // ===========================================================================
  describe("log", () => {
    it("adds to internal logs array", () => {
      const logs: string[] = [];
      const r = new MistReporter({ logs });
      r.log("test message");
      expect(logs).toContain("test message");
      expect(r.getStats().nLog).toBe(1);
    });

    it("increments nLog count", () => {
      const r = new MistReporter();
      r.log("a");
      r.log("b");
      r.log("c");
      expect(r.getStats().nLog).toBe(3);
    });
  });

  // ===========================================================================
  // destroy
  // ===========================================================================
  describe("destroy", () => {
    it("can be called on fresh instance", () => {
      const r = new MistReporter();
      expect(() => r.destroy()).not.toThrow();
    });

    it("cleans up after init", () => {
      const r = new MistReporter();
      const video = createMockVideo();
      r.init(video as unknown as HTMLVideoElement);

      r.destroy();
      expect(video.removeEventListener).toHaveBeenCalled();
    });
  });

  // ===========================================================================
  // init + video events
  // ===========================================================================
  describe("init and video events", () => {
    it("init registers event listeners", () => {
      const r = new MistReporter();
      const video = createMockVideo();
      r.init(video as unknown as HTMLVideoElement);

      expect(video.addEventListener).toHaveBeenCalledWith("playing", expect.any(Function));
      expect(video.addEventListener).toHaveBeenCalledWith("waiting", expect.any(Function));
      expect(video.addEventListener).toHaveBeenCalledWith("stalled", expect.any(Function));
      expect(video.addEventListener).toHaveBeenCalledWith("pause", expect.any(Function));
      expect(video.addEventListener).toHaveBeenCalledWith("error", expect.any(Function));
      expect(video.addEventListener).toHaveBeenCalledWith("canplay", expect.any(Function));
    });

    it("waiting event increments nWaiting", () => {
      const r = new MistReporter();
      const video = createMockVideo();
      r.init(video as unknown as HTMLVideoElement);

      video._fire("waiting");
      expect(r.getStats().nWaiting).toBe(1);
    });

    it("stalled event increments nStalled", () => {
      const r = new MistReporter();
      const video = createMockVideo();
      r.init(video as unknown as HTMLVideoElement);

      video._fire("stalled");
      expect(r.getStats().nStalled).toBe(1);
    });

    it("error event increments nError", () => {
      const r = new MistReporter();
      const video = createMockVideo();
      r.init(video as unknown as HTMLVideoElement);

      video._fire("error", { message: "decode failed" });
      expect(r.getStats().nError).toBe(1);
    });

    it("playing event records firstPlayback", () => {
      const r = new MistReporter({ bootMs: 49000 });
      const video = createMockVideo();
      r.init(video as unknown as HTMLVideoElement);

      video._fire("playing");
      expect(r.getStats().firstPlayback).toBe(1000); // Date.now(50000) - bootMs(49000)
    });

    it("playing event records firstPlayback only once", () => {
      const r = new MistReporter({ bootMs: 49000 });
      const video = createMockVideo();
      r.init(video as unknown as HTMLVideoElement);

      video._fire("playing");
      const first = r.getStats().firstPlayback;

      (Date.now as ReturnType<typeof vi.fn>).mockReturnValue(60000);
      video._fire("playing");
      expect(r.getStats().firstPlayback).toBe(first);
    });

    it("getStats reads video dimensions", () => {
      const r = new MistReporter();
      const video = createMockVideo();
      r.init(video as unknown as HTMLVideoElement);

      const stats = r.getStats();
      expect(stats.videoHeight).toBe(1080);
      expect(stats.videoWidth).toBe(1920);
      expect(stats.playerHeight).toBe(720);
      expect(stats.playerWidth).toBe(1280);
    });
  });
});
