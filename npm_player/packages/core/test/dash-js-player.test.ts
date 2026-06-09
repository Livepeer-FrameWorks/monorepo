// @vitest-environment jsdom

import { beforeEach, describe, expect, it, vi } from "vitest";

import { DashJsPlayerImpl } from "../src/players/DashJsPlayer";

const dashMocks = vi.hoisted(() => {
  const handlers = new Map<string, Array<(event?: unknown) => void>>();
  const state: { startupEvent: string | null } = { startupEvent: "streamInitialized" };
  const emit = (event: string, payload?: unknown) => {
    handlers.get(event)?.forEach((handler) => handler(payload));
  };

  return {
    handlers,
    state,
    emit,
    initialize: vi.fn(() => {
      if (state.startupEvent) {
        emit(state.startupEvent, { type: state.startupEvent });
      }
    }),
    updateSettings: vi.fn(),
    time: vi.fn(() => 64),
    getDvrWindow: vi.fn(() => ({ start: 12, end: 72, size: 60 })),
    seekToPresentationTime: vi.fn(),
    seekToOriginalLive: vi.fn(),
    refreshManifest: vi.fn((callback?: (manifest: unknown, error: unknown) => void) => {
      callback?.({}, null);
    }),
    getBufferLength: vi.fn((type?: string) => {
      if (type === "video" || type === "audio") return 8;
      return 8;
    }),
    getCurrentLiveLatency: vi.fn(() => 6),
    getTargetLiveDelay: vi.fn(() => 6),
    on: vi.fn((event: string, handler: (event?: unknown) => void) => {
      const eventHandlers = handlers.get(event) ?? [];
      eventHandlers.push(handler);
      handlers.set(event, eventHandlers);
    }),
    off: vi.fn((event: string, handler: (event?: unknown) => void) => {
      const eventHandlers = handlers.get(event) ?? [];
      handlers.set(
        event,
        eventHandlers.filter((candidate) => candidate !== handler)
      );
    }),
    reset: vi.fn(),
  };
});

vi.mock("dashjs", () => ({
  default: {
    MediaPlayer: () => ({
      create: () => ({
        initialize: dashMocks.initialize,
        updateSettings: dashMocks.updateSettings,
        time: dashMocks.time,
        getDvrWindow: dashMocks.getDvrWindow,
        seekToPresentationTime: dashMocks.seekToPresentationTime,
        seekToOriginalLive: dashMocks.seekToOriginalLive,
        refreshManifest: dashMocks.refreshManifest,
        getBufferLength: dashMocks.getBufferLength,
        getCurrentLiveLatency: dashMocks.getCurrentLiveLatency,
        getTargetLiveDelay: dashMocks.getTargetLiveDelay,
        on: dashMocks.on,
        off: dashMocks.off,
        reset: dashMocks.reset,
      }),
    }),
    Constants: {
      LIVE_CATCHUP_MODE_LOLP: "liveCatchupModeLoLP",
      LOW_LATENCY_DOWNLOAD_TIME_CALCULATION_MODE: {
        MOOF_PARSING: "lowLatencyDownloadTimeCalculationModeMoofParsing",
      },
    },
  },
}));

const EXPECTED_STANDARD_DASH_SETTINGS = {
  streaming: {
    text: { defaultEnabled: false },
  },
  debug: { logLevel: 2 },
};

describe("DashJsPlayerImpl", () => {
  beforeEach(() => {
    dashMocks.initialize.mockReset();
    dashMocks.handlers.clear();
    dashMocks.state.startupEvent = "streamInitialized";
    dashMocks.updateSettings.mockReset();
    dashMocks.time.mockReset();
    dashMocks.time.mockReturnValue(64);
    dashMocks.getDvrWindow.mockReset();
    dashMocks.getDvrWindow.mockReturnValue({ start: 12, end: 72, size: 60 });
    dashMocks.seekToPresentationTime.mockReset();
    dashMocks.seekToOriginalLive.mockReset();
    dashMocks.refreshManifest.mockClear();
    dashMocks.getBufferLength.mockReset();
    dashMocks.getBufferLength.mockImplementation((type?: string) => {
      if (type === "video" || type === "audio") return 8;
      return 8;
    });
    dashMocks.getCurrentLiveLatency.mockReset();
    dashMocks.getCurrentLiveLatency.mockReturnValue(6);
    dashMocks.getTargetLiveDelay.mockReset();
    dashMocks.getTargetLiveDelay.mockReturnValue(6);
    dashMocks.on.mockReset();
    dashMocks.off.mockReset();
    dashMocks.reset.mockReset();
  });

  it("uses the native MSE seekable window instead of dash.js presentation APIs for live DASH", () => {
    const player = new DashJsPlayerImpl();
    const video = document.createElement("video");
    Object.defineProperty(video, "duration", { configurable: true, value: Infinity });
    Object.defineProperty(video, "currentTime", { configurable: true, value: 64 });
    Object.defineProperty(video, "seekable", {
      configurable: true,
      value: { length: 1, start: () => 12, end: () => 72 },
    });
    (player as any).videoElement = video;
    (player as any).dashPlayer = {
      getDvrWindow: dashMocks.getDvrWindow,
      seekToPresentationTime: dashMocks.seekToPresentationTime,
      seekToOriginalLive: dashMocks.seekToOriginalLive,
      time: dashMocks.time,
    };
    (player as any).streamType = "live";

    player.setSeekableRangeHint({ start: 100_000, end: 160_000 });

    expect(player.getSeekableRange()).toEqual({ start: 12_000, end: 72_000 });
    expect(player.getDuration()).toBe(72_000);
    expect(player.getCurrentTime()).toBe(64_000);
    expect(dashMocks.time).not.toHaveBeenCalled();
    expect(dashMocks.getDvrWindow).not.toHaveBeenCalled();
  });

  it("routes live DASH seeks and live jumps through dash.js DVR APIs (not native currentTime)", () => {
    const player = new DashJsPlayerImpl();
    const video = document.createElement("video");
    Object.defineProperty(video, "duration", { configurable: true, value: Infinity });
    let currentTime = 0;
    Object.defineProperty(video, "currentTime", {
      configurable: true,
      get: () => currentTime,
      set: (value) => {
        currentTime = value;
      },
    });
    Object.defineProperty(video, "seekable", {
      configurable: true,
      value: { length: 1, start: () => 12, end: () => 72 },
    });
    (player as any).videoElement = video;
    (player as any).dashPlayer = {
      getDvrWindow: dashMocks.getDvrWindow,
      seekToPresentationTime: dashMocks.seekToPresentationTime,
      seekToOriginalLive: dashMocks.seekToOriginalLive,
      time: dashMocks.time,
    };
    (player as any).streamType = "live";

    // dash.js must own the DVR seek (presentation time); poking currentTime makes it
    // reset its SourceBuffers and snap back to live.
    player.seek(68_500);
    expect(dashMocks.seekToPresentationTime).toHaveBeenCalledWith(68.5);
    expect(currentTime).toBe(0); // we did NOT set the element directly

    player.jumpToLive();
    expect(dashMocks.seekToOriginalLive).toHaveBeenCalledTimes(1);
  });

  it("routes dash.js live DVR null-range rejections through the player error event", () => {
    const player = new DashJsPlayerImpl();
    const errors: string[] = [];
    let prevented = false;
    let stopped = false;
    player.on("error", (error) => errors.push(String(error)));

    const handler = (player as any).createInternalRejectionHandler();

    handler({
      reason: new TypeError(`can't access property "range", v.getCurrentDVRInfo() is null`),
      preventDefault: () => {
        prevented = true;
      },
      stopImmediatePropagation: () => {
        stopped = true;
      },
    } as PromiseRejectionEvent);

    expect(prevented).toBe(true);
    expect(stopped).toBe(true);
    expect(errors).toEqual([
      `DASH fatal internal error: can't access property "range", v.getCurrentDVRInfo() is null`,
    ]);
  });

  it("ignores unrelated app rejections not originating from dash.js", () => {
    const player = new DashJsPlayerImpl();
    const errors: string[] = [];
    let prevented = false;
    player.on("error", (error) => errors.push(String(error)));

    const handler = (player as any).createInternalRejectionHandler();

    // A generic null-property rejection with no dash.js signature and no
    // dash.js stack frame must NOT be claimed as a DASH failure — otherwise the
    // global handler swallows unrelated app errors while this player is alive.
    handler({
      reason: new TypeError("Cannot read properties of null (reading 'foo')"),
      preventDefault: () => {
        prevented = true;
      },
      stopImmediatePropagation: () => {},
    } as PromiseRejectionEvent);

    expect(prevented).toBe(false);
    expect(errors).toEqual([]);
  });

  it("applies DASH integration settings by default and the caller dashConfig last", async () => {
    const player = new DashJsPlayerImpl();
    const container = document.createElement("div");
    const dashConfig = { streaming: { delay: { liveDelay: 2 } } };

    await player.initialize(
      container,
      { type: "dash/video/mp4", url: "https://edge.example/live/index.mpd" },
      { autoplay: false, muted: true, dashConfig },
      { source: [], meta: { tracks: [] }, type: "live" }
    );

    const calls = dashMocks.updateSettings.mock.calls;
    expect(calls[0][0]).toEqual(EXPECTED_STANDARD_DASH_SETTINGS);
    // The built-in defaults are applied first, then the caller override (dash.js deep-merges),
    // so the final updateSettings call carries the caller's dashConfig.
    expect(calls.length).toBeGreaterThanOrEqual(2);
    expect(calls[calls.length - 1][0]).toEqual(dashConfig);
  });

  it("returns the video element after dash.js source attachment and emits ready immediately", async () => {
    const player = new DashJsPlayerImpl();
    const container = document.createElement("div");
    const onReady = vi.fn();

    const initializedVideo = await player.initialize(
      container,
      { type: "dash/video/mp4", url: "https://edge.example/live/index.mpd" },
      { autoplay: true, muted: true, onReady },
      { source: [], meta: { tracks: [] }, type: "live" }
    );

    expect(dashMocks.initialize).toHaveBeenCalledWith(
      expect.any(HTMLVideoElement),
      "https://edge.example/live/index.mpd",
      true
    );
    const video = container.querySelector("video") as HTMLVideoElement;
    expect(initializedVideo).toBe(video);
    expect(onReady).toHaveBeenCalledWith(video);
    await player.destroy();
  });

  it("reports a player error when dash.js never starts after source attachment", async () => {
    vi.useFakeTimers();
    dashMocks.state.startupEvent = null;
    const player = new DashJsPlayerImpl();
    const container = document.createElement("div");
    const onReady = vi.fn();
    const errors: string[] = [];
    player.on("error", (error) => errors.push(String(error)));

    try {
      const initializedVideo = await player.initialize(
        container,
        { type: "dash/video/mp4", url: "https://edge.example/live/index.mpd" },
        { autoplay: true, muted: true, onReady },
        { source: [], meta: { tracks: [] }, type: "live" }
      );

      expect(initializedVideo).toBe(container.querySelector("video"));
      expect(onReady).toHaveBeenCalledOnce();
      await vi.advanceTimersByTimeAsync(10000);

      expect(errors).toContain("DASH fatal startup timed out before stream initialization");
    } finally {
      await player.destroy();
      vi.useRealTimers();
    }
  });

  it("does not override DASH live-edge, buffer, or catch-up settings by default", async () => {
    const player = new DashJsPlayerImpl();
    const container = document.createElement("div");

    await player.initialize(
      container,
      { type: "dash/video/mp4", url: "https://edge.example/live/index.mpd" },
      { autoplay: false, muted: true },
      { source: [], meta: { tracks: [] }, type: "live" }
    );

    expect(dashMocks.updateSettings).toHaveBeenCalledTimes(1);
    expect(dashMocks.updateSettings).toHaveBeenCalledWith(EXPECTED_STANDARD_DASH_SETTINGS);
  });

  it("does not switch settings from source low-latency metadata", async () => {
    const player = new DashJsPlayerImpl();
    const container = document.createElement("div");

    await player.initialize(
      container,
      {
        type: "dash/video/mp4",
        url: "https://edge.example/live/index.mpd",
        dashMode: "low-latency",
      },
      { autoplay: false, muted: true },
      { source: [], meta: { tracks: [] }, type: "live" }
    );

    expect(dashMocks.updateSettings).toHaveBeenCalledTimes(1);
    expect(dashMocks.updateSettings).toHaveBeenCalledWith(EXPECTED_STANDARD_DASH_SETTINGS);
  });

  it("refreshes the live DASH manifest through dash.js when playback stalls", async () => {
    const player = new DashJsPlayerImpl();
    const container = document.createElement("div");

    await player.initialize(
      container,
      { type: "dash/video/mp4", url: "https://edge.example/live/index.mpd" },
      { autoplay: false, muted: true },
      { source: [], meta: { tracks: [] }, type: "live" }
    );

    dashMocks.emit("bufferStalled", { type: "bufferStalled" });
    dashMocks.emit("playbackWaiting", { type: "playbackWaiting" });
    dashMocks.emit("fragmentLoadingFailed", {
      request: { url: "https://edge.example/live/chunk_1.m4s" },
    });

    expect(dashMocks.refreshManifest).toHaveBeenCalledTimes(1);
  });

  it("does not refresh the live DASH manifest before dash.js initializes the stream", async () => {
    dashMocks.state.startupEvent = null;
    const player = new DashJsPlayerImpl();
    const container = document.createElement("div");

    await player.initialize(
      container,
      { type: "dash/video/mp4", url: "https://edge.example/live/index.mpd" },
      { autoplay: false, muted: true },
      { source: [], meta: { tracks: [] }, type: "live" }
    );

    dashMocks.emit("bufferLevelUpdated", { type: "bufferLevelUpdated", bufferLevel: 0 });
    dashMocks.emit("bufferStalled", { type: "bufferStalled" });

    expect(dashMocks.refreshManifest).not.toHaveBeenCalled();
  });

  it("refreshes the live DASH manifest on low buffer pressure before a stall", async () => {
    const player = new DashJsPlayerImpl();
    const container = document.createElement("div");
    const bufferLow = vi.fn();
    player.on("bufferlow", bufferLow);

    await player.initialize(
      container,
      { type: "dash/video/mp4", url: "https://edge.example/live/index.mpd" },
      { autoplay: false, muted: true },
      { source: [], meta: { tracks: [] }, type: "live" }
    );

    container.querySelector("video")?.dispatchEvent(new Event("canplay"));
    dashMocks.getBufferLength.mockImplementation((type?: string) => {
      if (type === "video") return 1.5;
      if (type === "audio") return 5;
      return 1.5;
    });

    dashMocks.emit("bufferLevelUpdated", { type: "bufferLevelUpdated", bufferLevel: 1.5 });
    expect(dashMocks.refreshManifest).toHaveBeenCalledTimes(1);
    expect(bufferLow).toHaveBeenCalledWith({ current: 1500, desired: 2000 });

    // Follow-up events in the same refresh interval must not spam manifest reloads.
    dashMocks.emit("bufferLevelUpdated", { type: "bufferLevelUpdated", bufferLevel: 1 });
    dashMocks.emit("bufferStalled", { type: "bufferStalled" });
    expect(dashMocks.refreshManifest).toHaveBeenCalledTimes(1);
  });

  it("does not refresh for low buffer while dash.js is already loading a fragment", async () => {
    const player = new DashJsPlayerImpl();
    const container = document.createElement("div");

    await player.initialize(
      container,
      { type: "dash/video/mp4", url: "https://edge.example/live/index.mpd" },
      { autoplay: false, muted: true },
      { source: [], meta: { tracks: [] }, type: "live" }
    );

    container.querySelector("video")?.dispatchEvent(new Event("canplay"));
    dashMocks.getBufferLength.mockImplementation((type?: string) => {
      if (type === "video") return 0.5;
      if (type === "audio") return 4;
      return 0.5;
    });

    dashMocks.emit("fragmentLoadingStarted", {
      request: { url: "https://edge.example/live/chunk_1.m4s" },
    });
    dashMocks.emit("bufferLevelUpdated", { type: "bufferLevelUpdated", bufferLevel: 0.5 });
    expect(dashMocks.refreshManifest).not.toHaveBeenCalled();

    dashMocks.emit("fragmentLoadingCompleted", {
      request: { url: "https://edge.example/live/chunk_1.m4s" },
    });
    dashMocks.emit("bufferLevelUpdated", { type: "bufferLevelUpdated", bufferLevel: 0.5 });
    expect(dashMocks.refreshManifest).toHaveBeenCalledTimes(1);
  });

  it("does not refresh the DASH manifest for VOD stalls", async () => {
    const player = new DashJsPlayerImpl();
    const container = document.createElement("div");

    await player.initialize(
      container,
      { type: "dash/video/mp4", url: "https://edge.example/vod/index.mpd" },
      { autoplay: false, muted: true },
      { source: [], meta: { tracks: [] }, type: "vod" }
    );

    dashMocks.emit("bufferStalled", { type: "bufferStalled" });

    expect(dashMocks.refreshManifest).not.toHaveBeenCalled();
  });

  it("does not reconfigure dash.js from parsed LL-DASH manifest signals", async () => {
    const player = new DashJsPlayerImpl();
    const container = document.createElement("div");

    await player.initialize(
      container,
      { type: "dash/video/mp4", url: "https://edge.example/live/index.mpd" },
      { autoplay: false, muted: true },
      { source: [], meta: { tracks: [] }, type: "live" }
    );

    expect(dashMocks.updateSettings).toHaveBeenCalledTimes(1);
    expect(dashMocks.updateSettings).toHaveBeenLastCalledWith(EXPECTED_STANDARD_DASH_SETTINGS);

    dashMocks.emit("manifestLoaded", {
      data: {
        Period: [
          {
            AdaptationSet: [
              {
                SegmentTemplate: {
                  availabilityTimeComplete: false,
                  availabilityTimeOffset: 1.5,
                },
              },
            ],
          },
        ],
      },
    });

    expect(dashMocks.updateSettings).toHaveBeenCalledTimes(1);

    dashMocks.emit("manifestLoaded", {
      data: {
        Period: [{ AdaptationSet: [{ SegmentTemplate: { availabilityTimeComplete: false } }] }],
      },
    });

    expect(dashMocks.updateSettings).toHaveBeenCalledTimes(1);
  });

  it("reports repeated startup fragment abandonment before playable media", async () => {
    const player = new DashJsPlayerImpl();
    const container = document.createElement("div");
    const errors: string[] = [];
    player.on("error", (error) => errors.push(String(error)));

    await player.initialize(
      container,
      { type: "dash/video/mp4", url: "https://edge.example/live/index.mpd" },
      { autoplay: false, muted: true },
      { source: [], meta: { tracks: [] }, type: "live" }
    );

    dashMocks.emit("fragmentLoadingAbandoned", {
      request: { url: "https://edge.example/live/chunk_1.m4s" },
    });
    dashMocks.emit("fragmentLoadingAbandoned", {
      request: { url: "https://edge.example/live/chunk_2.m4s" },
    });
    dashMocks.emit("fragmentLoadingAbandoned", {
      request: { url: "https://edge.example/live/chunk_3.m4s" },
    });

    expect(errors).toContain("DASH fatal startup fragment abandoned repeatedly");
  });
});
