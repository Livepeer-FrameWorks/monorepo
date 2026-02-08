import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import { TelemetryReporter } from "../src/core/TelemetryReporter";
import type { PlaybackQuality } from "../src/types";

function createMockVideo() {
  const listeners = new Map<string, Function[]>();
  return {
    currentTime: 12,
    duration: 120,
    paused: false,
    muted: false,
    volume: 1,
    playbackRate: 1,
    videoWidth: 1920,
    videoHeight: 1080,
    error: null as any,
    buffered: { length: 1, start: () => 0, end: () => 30 },
    seekable: { length: 1, start: () => 0, end: () => 100 },
    style: { width: "", height: "" },
    play: vi.fn(async () => {}),
    pause: vi.fn(),
    addEventListener: vi.fn((event: string, handler: Function) => {
      if (!listeners.has(event)) listeners.set(event, []);
      listeners.get(event)!.push(handler);
    }),
    removeEventListener: vi.fn((event: string, handler: Function) => {
      listeners.set(
        event,
        (listeners.get(event) || []).filter((h) => h !== handler)
      );
    }),
    requestPictureInPicture: vi.fn(async () => {}),
    getVideoPlaybackQuality: vi.fn(() => ({ totalVideoFrames: 100, droppedVideoFrames: 2 })),
    _fire(event: string) {
      listeners.get(event)?.forEach((h) => h());
    },
    _listeners: listeners,
  } as unknown as HTMLVideoElement;
}

describe("TelemetryReporter", () => {
  let origFetch: typeof globalThis.fetch;
  let origNavigator: any;
  let origWindow: any;
  let origCrypto: any;

  beforeEach(() => {
    vi.useFakeTimers();
    origFetch = globalThis.fetch;
    origNavigator = globalThis.navigator;
    origWindow = (globalThis as any).window;

    globalThis.fetch = vi.fn().mockResolvedValue({ ok: true, status: 200 });
    origCrypto = (globalThis as any).crypto;

    vi.stubGlobal("navigator", { sendBeacon: vi.fn() });
    vi.stubGlobal("window", {
      addEventListener: vi.fn(),
      removeEventListener: vi.fn(),
    });
    vi.stubGlobal("crypto", {
      getRandomValues: (arr: Uint8Array) => {
        arr.fill(1);
        return arr;
      },
    });
  });

  afterEach(() => {
    vi.useRealTimers();
    globalThis.fetch = origFetch;
    vi.unstubAllGlobals();
    if (origNavigator === undefined) {
      // @ts-expect-error optional
      delete (globalThis as any).navigator;
    } else {
      Object.defineProperty(globalThis, "navigator", {
        value: origNavigator,
        configurable: true,
      });
    }
    if (origWindow === undefined) {
      // @ts-expect-error optional
      delete (globalThis as any).window;
    } else {
      Object.defineProperty(globalThis, "window", { value: origWindow, configurable: true });
    }
    if (origCrypto === undefined) {
      // @ts-expect-error optional
      delete (globalThis as any).crypto;
    } else {
      Object.defineProperty(globalThis, "crypto", { value: origCrypto, configurable: true });
    }
    vi.restoreAllMocks();
  });

  it("uses constructor defaults and starts reporting", async () => {
    const reporter = new TelemetryReporter({
      endpoint: "https://telemetry.test",
      contentId: "content",
      contentType: "live",
      playerType: "native",
      protocol: "hls",
    });

    const video = createMockVideo();
    reporter.start(video);

    expect(reporter.isActive()).toBe(true);
    expect((globalThis.fetch as any).mock.calls[0][0]).toBe("https://telemetry.test");

    vi.advanceTimersByTime(5000);
    expect(globalThis.fetch).toHaveBeenCalled();
  });

  it("stops reporting and removes listeners", () => {
    const reporter = new TelemetryReporter({
      endpoint: "https://telemetry.test",
      contentId: "content",
      contentType: "live",
      playerType: "native",
      protocol: "hls",
    });
    const video = createMockVideo();
    reporter.start(video);

    reporter.stop();
    expect(reporter.isActive()).toBe(false);
    expect((globalThis.navigator as any).sendBeacon).toHaveBeenCalled();
    expect((globalThis as any).window.removeEventListener).toHaveBeenCalled();
  });

  it("records errors and generates payload", () => {
    const reporter = new TelemetryReporter({
      endpoint: "https://telemetry.test",
      contentId: "content",
      contentType: "vod",
      playerType: "native",
      protocol: "hls",
    });
    const video = createMockVideo();

    reporter.start(video, () => ({ bitrate: 123, score: 87 }) as PlaybackQuality);
    reporter.recordError("E1", "oops");

    const payload = (reporter as any).generatePayload();
    expect(payload.metrics.bitrate).toBe(123);
    expect(payload.metrics.qualityScore).toBe(87);
    expect(payload.metrics.bufferedSeconds).toBe(18);
    expect(payload.metrics.framesDecoded).toBe(100);
    expect(payload.metrics.framesDropped).toBe(2);
    expect(payload.errors).toEqual([expect.objectContaining({ code: "E1", message: "oops" })]);
  });

  it("queues payloads and re-queues on failure", async () => {
    (globalThis.fetch as any).mockResolvedValueOnce({ ok: false, status: 500 });

    const reporter = new TelemetryReporter({
      endpoint: "https://telemetry.test",
      contentId: "content",
      contentType: "vod",
      playerType: "native",
      protocol: "hls",
      batchSize: 1,
    });
    const video = createMockVideo();

    reporter.start(video);
    await (reporter as any).flush();

    expect((reporter as any).pendingPayloads.length).toBeGreaterThan(0);
  });

  it("flushSync uses sendBeacon", () => {
    const reporter = new TelemetryReporter({
      endpoint: "https://telemetry.test",
      contentId: "content",
      contentType: "vod",
      playerType: "native",
      protocol: "hls",
    });
    const video = createMockVideo();

    reporter.start(video);
    reporter.stop();

    expect((globalThis.navigator as any).sendBeacon).toHaveBeenCalledWith(
      "https://telemetry.test",
      expect.any(Blob)
    );
  });

  it("tracks stalls using waiting/playing events", () => {
    const nowSpy = vi
      .spyOn(globalThis.performance, "now")
      .mockReturnValueOnce(100)
      .mockReturnValueOnce(400);

    const reporter = new TelemetryReporter({
      endpoint: "https://telemetry.test",
      contentId: "content",
      contentType: "vod",
      playerType: "native",
      protocol: "hls",
    });
    const video = createMockVideo();

    reporter.start(video);

    video._fire("waiting");
    video._fire("playing");

    const payload = (reporter as any).generatePayload();
    expect(payload.metrics.stallCount).toBe(1);
    expect(payload.metrics.totalStallMs).toBe(300);

    nowSpy.mockRestore();
  });
});
