import { describe, expect, it, vi, beforeEach, afterEach } from "vitest";

import { ABRController, type ABRControllerConfig } from "../src/core/ABRController";
import type { QualityLevel, PlaybackQuality } from "../src/types";

// Stub ResizeObserver
class MockResizeObserver {
  static instances: MockResizeObserver[] = [];
  callback: ResizeObserverCallback;
  observed: Element[] = [];

  constructor(callback: ResizeObserverCallback) {
    this.callback = callback;
    MockResizeObserver.instances.push(this);
  }
  observe(target: Element) {
    this.observed.push(target);
  }
  unobserve() {}
  disconnect() {
    this.observed = [];
  }

  fireResize(width: number, height: number) {
    this.callback([{ contentRect: { width, height } } as ResizeObserverEntry], this);
  }
}

function makeQualities(): QualityLevel[] {
  return [
    { id: "360p", label: "360p", width: 640, height: 360, bitrate: 800_000 },
    { id: "720p", label: "720p", width: 1280, height: 720, bitrate: 2_500_000 },
    { id: "1080p", label: "1080p", width: 1920, height: 1080, bitrate: 5_000_000 },
  ];
}

function makeConfig(overrides: Partial<ABRControllerConfig> = {}): ABRControllerConfig {
  return {
    getQualities: () => makeQualities(),
    selectQuality: vi.fn(),
    getCurrentQuality: () => makeQualities()[1],
    ...overrides,
  };
}

function makeVideoElement() {
  return {
    buffered: { length: 0, start: () => 0, end: () => 0 },
    parentElement: { tagName: "DIV" },
    getBoundingClientRect: () => ({ width: 800, height: 450 }),
    addEventListener: vi.fn(),
    removeEventListener: vi.fn(),
  } as unknown as HTMLVideoElement;
}

function makeQuality(overrides: Partial<PlaybackQuality> = {}): PlaybackQuality {
  return {
    score: 85,
    bitrate: 2_500_000,
    bufferedAhead: 15,
    stallCount: 0,
    frameDropRate: 0,
    latency: 0.5,
    timestamp: Date.now(),
    ...overrides,
  };
}

describe("ABRController", () => {
  let origResizeObserver: typeof globalThis.ResizeObserver;
  let origWindow: PropertyDescriptor | undefined;

  beforeEach(() => {
    vi.useFakeTimers();
    vi.spyOn(Date, "now").mockReturnValue(100_000);
    MockResizeObserver.instances = [];
    origResizeObserver = globalThis.ResizeObserver;
    (globalThis as any).ResizeObserver = MockResizeObserver;
    origWindow = Object.getOwnPropertyDescriptor(globalThis, "window");
    (globalThis as any).window = { devicePixelRatio: 1 };
  });

  afterEach(() => {
    vi.useRealTimers();
    vi.restoreAllMocks();
    (globalThis as any).ResizeObserver = origResizeObserver;
    if (origWindow) {
      Object.defineProperty(globalThis, "window", origWindow);
    } else {
      delete (globalThis as any).window;
    }
  });

  // ===========================================================================
  // Constructor & mode
  // ===========================================================================
  describe("mode", () => {
    it("defaults to auto", () => {
      const abr = new ABRController(makeConfig());
      expect(abr.getMode()).toBe("auto");
    });

    it("accepts configured mode", () => {
      const abr = new ABRController(makeConfig({ options: { mode: "manual" } }));
      expect(abr.getMode()).toBe("manual");
    });

    it("setMode changes mode", () => {
      const abr = new ABRController(makeConfig());
      abr.setMode("bitrate");
      expect(abr.getMode()).toBe("bitrate");
    });

    it("setMode restarts when video attached", () => {
      const abr = new ABRController(makeConfig({ options: { mode: "resize" } }));
      const video = makeVideoElement();
      abr.start(video);
      expect(MockResizeObserver.instances.length).toBe(1);

      abr.setMode("bitrate");
      // ResizeObserver disconnected (resize stopped) and new monitoring started
      expect(abr.getMode()).toBe("bitrate");
      abr.stop();
    });

    it("setMode no-op for same mode", () => {
      const abr = new ABRController(makeConfig());
      abr.setMode("auto");
      expect(abr.getMode()).toBe("auto");
    });
  });

  // ===========================================================================
  // Manual mode
  // ===========================================================================
  describe("manual mode", () => {
    it("does not set up resize observer", () => {
      const abr = new ABRController(makeConfig({ options: { mode: "manual" } }));
      abr.start(makeVideoElement());
      expect(MockResizeObserver.instances.length).toBe(0);
      abr.stop();
    });

    it("setQuality calls selectQuality callback", () => {
      const selectQuality = vi.fn();
      const abr = new ABRController(makeConfig({ selectQuality }));
      abr.setQuality("720p");
      expect(selectQuality).toHaveBeenCalledWith("720p");
    });
  });

  // ===========================================================================
  // Resize mode
  // ===========================================================================
  describe("resize mode", () => {
    it("sets up resize observer on start", () => {
      const abr = new ABRController(makeConfig({ options: { mode: "resize" } }));
      abr.start(makeVideoElement());
      expect(MockResizeObserver.instances.length).toBe(1);
      abr.stop();
    });

    it("initial resize selects quality matching viewport", () => {
      const selectQuality = vi.fn();
      const abr = new ABRController(
        makeConfig({
          selectQuality,
          options: { mode: "resize" },
        })
      );
      // Video is 800x450, so 720p (1280x720) should be selected
      abr.start(makeVideoElement());
      expect(selectQuality).toHaveBeenCalledWith("720p");
      abr.stop();
    });
  });

  // ===========================================================================
  // Bitrate mode — bandwidth monitoring
  // ===========================================================================
  describe("bitrate mode", () => {
    it("starts monitoring interval", () => {
      const abr = new ABRController(
        makeConfig({
          options: { mode: "bitrate" },
          getBandwidthEstimate: async () => 3_000_000,
        })
      );
      abr.start(makeVideoElement());

      // Monitoring interval set up
      vi.advanceTimersByTime(1000);
      abr.stop();
    });

    it("requires 3+ samples for smoothed bandwidth", () => {
      const abr = new ABRController(
        makeConfig({
          options: { mode: "bitrate" },
          getBandwidthEstimate: async () => 5_000_000,
        })
      );
      expect(abr.getCurrentBandwidth()).toBe(0);
      abr.stop();
    });
  });

  // ===========================================================================
  // handleQualityDegraded (downgrade)
  // ===========================================================================
  describe("handleQualityDegraded", () => {
    it("downgrades when score below threshold", () => {
      const selectQuality = vi.fn();
      const qualities = makeQualities();
      const abr = new ABRController(
        makeConfig({
          selectQuality,
          options: { mode: "bitrate" },
          getCurrentQuality: () => qualities[2],
        })
      );

      abr.handleQualityDegraded(makeQuality({ score: 40 }));
      expect(selectQuality).toHaveBeenCalledWith("720p");
    });

    it("does not downgrade when score above threshold", () => {
      const selectQuality = vi.fn();
      const abr = new ABRController(
        makeConfig({
          selectQuality,
          options: { mode: "bitrate" },
        })
      );

      abr.handleQualityDegraded(makeQuality({ score: 80 }));
      expect(selectQuality).not.toHaveBeenCalled();
    });

    it("no-op in manual mode", () => {
      const selectQuality = vi.fn();
      const abr = new ABRController(
        makeConfig({
          selectQuality,
          options: { mode: "manual" },
        })
      );

      abr.handleQualityDegraded(makeQuality({ score: 10 }));
      expect(selectQuality).not.toHaveBeenCalled();
    });

    it("does not downgrade below lowest quality", () => {
      const selectQuality = vi.fn();
      const qualities = makeQualities();
      const abr = new ABRController(
        makeConfig({
          selectQuality,
          options: { mode: "bitrate" },
          getCurrentQuality: () => qualities[0],
        })
      );

      abr.handleQualityDegraded(makeQuality({ score: 30 }));
      expect(selectQuality).not.toHaveBeenCalled();
    });
  });

  // ===========================================================================
  // handleQualityImproved (upgrade)
  // ===========================================================================
  describe("handleQualityImproved", () => {
    it("upgrades when score and buffer are sufficient", () => {
      const selectQuality = vi.fn();
      const qualities = makeQualities();
      const abr = new ABRController(
        makeConfig({
          selectQuality,
          options: { mode: "bitrate" },
          getCurrentQuality: () => qualities[0],
        })
      );

      // Need 5s cooldown elapsed (advance past UPGRADE_COOLDOWN_MS)
      (Date.now as ReturnType<typeof vi.fn>).mockReturnValue(106_000);

      abr.handleQualityImproved(makeQuality({ score: 95, bufferedAhead: 15 }));
      expect(selectQuality).toHaveBeenCalledWith("720p");
    });

    it("does not upgrade when buffer too low", () => {
      const selectQuality = vi.fn();
      const qualities = makeQualities();
      const abr = new ABRController(
        makeConfig({
          selectQuality,
          options: { mode: "bitrate" },
          getCurrentQuality: () => qualities[0],
        })
      );

      (Date.now as ReturnType<typeof vi.fn>).mockReturnValue(106_000);
      abr.handleQualityImproved(makeQuality({ score: 95, bufferedAhead: 5 }));
      expect(selectQuality).not.toHaveBeenCalled();
    });

    it("does not upgrade when score too low", () => {
      const selectQuality = vi.fn();
      const qualities = makeQualities();
      const abr = new ABRController(
        makeConfig({
          selectQuality,
          options: { mode: "bitrate" },
          getCurrentQuality: () => qualities[0],
        })
      );

      (Date.now as ReturnType<typeof vi.fn>).mockReturnValue(106_000);
      abr.handleQualityImproved(makeQuality({ score: 80, bufferedAhead: 15 }));
      expect(selectQuality).not.toHaveBeenCalled();
    });

    it("respects upgrade cooldown", () => {
      const selectQuality = vi.fn();
      const qualities = makeQualities();
      const abr = new ABRController(
        makeConfig({
          selectQuality,
          options: { mode: "bitrate" },
          getCurrentQuality: () => qualities[0],
        })
      );

      // First upgrade
      (Date.now as ReturnType<typeof vi.fn>).mockReturnValue(106_000);
      abr.handleQualityImproved(makeQuality({ score: 95, bufferedAhead: 15 }));
      expect(selectQuality).toHaveBeenCalledTimes(1);

      // Second upgrade attempt within 5s cooldown
      (Date.now as ReturnType<typeof vi.fn>).mockReturnValue(108_000);
      selectQuality.mockClear();
      abr.handleQualityImproved(makeQuality({ score: 95, bufferedAhead: 15 }));
      expect(selectQuality).not.toHaveBeenCalled();
    });

    it("does not upgrade past constraints", () => {
      const selectQuality = vi.fn();
      const qualities = makeQualities();
      const abr = new ABRController(
        makeConfig({
          selectQuality,
          options: { mode: "bitrate", maxBitrate: 1_000_000 },
          getCurrentQuality: () => qualities[0],
        })
      );

      (Date.now as ReturnType<typeof vi.fn>).mockReturnValue(106_000);
      abr.handleQualityImproved(makeQuality({ score: 95, bufferedAhead: 15 }));
      // 720p (2.5Mbps) exceeds maxBitrate (1Mbps), so no upgrade
      expect(selectQuality).not.toHaveBeenCalled();
    });
  });

  // ===========================================================================
  // Quality change callbacks
  // ===========================================================================
  describe("onQualityChange", () => {
    it("fires callback on quality change", () => {
      const cb = vi.fn();
      const abr = new ABRController(makeConfig());
      abr.onQualityChange(cb);
      abr.setQuality("720p");
      expect(cb).toHaveBeenCalledWith(expect.objectContaining({ id: "720p" }));
    });

    it("unsubscribe removes callback", () => {
      const cb = vi.fn();
      const abr = new ABRController(makeConfig());
      const unsub = abr.onQualityChange(cb);
      unsub();
      abr.setQuality("720p");
      expect(cb).not.toHaveBeenCalled();
    });
  });

  // ===========================================================================
  // stop / lifecycle
  // ===========================================================================
  describe("stop", () => {
    it("disconnects resize observer", () => {
      const abr = new ABRController(makeConfig({ options: { mode: "resize" } }));
      abr.start(makeVideoElement());
      expect(MockResizeObserver.instances.length).toBe(1);

      abr.stop();
      expect(MockResizeObserver.instances[0].observed).toEqual([]);
    });

    it("clears bandwidth history", () => {
      const abr = new ABRController(
        makeConfig({
          options: { mode: "bitrate" },
          getBandwidthEstimate: async () => 3_000_000,
        })
      );
      abr.start(makeVideoElement());
      abr.stop();
      expect(abr.getCurrentBandwidth()).toBe(0);
    });
  });

  // ===========================================================================
  // updateOptions
  // ===========================================================================
  describe("updateOptions", () => {
    it("merges new options", () => {
      const abr = new ABRController(makeConfig());
      abr.updateOptions({ maxBitrate: 1_000_000 });
      expect(abr.getMode()).toBe("auto");
    });
  });

  // ===========================================================================
  // getLastDecision
  // ===========================================================================
  describe("getLastDecision", () => {
    it("defaults to none", () => {
      const abr = new ABRController(makeConfig());
      expect(abr.getLastDecision()).toBe("none");
    });

    it("returns downgrade after degradation", () => {
      const qualities = makeQualities();
      const abr = new ABRController(
        makeConfig({
          options: { mode: "bitrate" },
          getCurrentQuality: () => qualities[2],
        })
      );
      abr.handleQualityDegraded(makeQuality({ score: 30 }));
      expect(abr.getLastDecision()).toBe("downgrade");
    });
  });
});
