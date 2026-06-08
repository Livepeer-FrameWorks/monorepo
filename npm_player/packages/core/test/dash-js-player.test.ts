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
        on: dashMocks.on,
        off: dashMocks.off,
        reset: dashMocks.reset,
      }),
    }),
  },
}));

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
    dashMocks.on.mockReset();
    dashMocks.off.mockReset();
    dashMocks.reset.mockReset();
  });

  it("uses dash.js DVR window instead of controller range hints for live DASH", () => {
    const player = new DashJsPlayerImpl();
    const video = document.createElement("video");
    Object.defineProperty(video, "duration", { configurable: true, value: Infinity });
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
  });

  it("routes live DASH seeks and live jumps through dash.js APIs", () => {
    const player = new DashJsPlayerImpl();
    const video = document.createElement("video");
    Object.defineProperty(video, "duration", { configurable: true, value: Infinity });
    (player as any).videoElement = video;
    (player as any).dashPlayer = {
      getDvrWindow: dashMocks.getDvrWindow,
      seekToPresentationTime: dashMocks.seekToPresentationTime,
      seekToOriginalLive: dashMocks.seekToOriginalLive,
      time: dashMocks.time,
    };
    (player as any).streamType = "live";

    player.seek(68_500);
    player.jumpToLive();

    expect(dashMocks.seekToPresentationTime).toHaveBeenCalledWith(68.5);
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

  it("uses MPD suggested presentation delay and applies caller dashConfig last", async () => {
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
    expect(calls[0][0]).toEqual({
      streaming: {
        text: { defaultEnabled: false },
        delay: { useSuggestedPresentationDelay: true },
      },
      debug: { logLevel: 2 },
    });
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
      false
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
      await vi.advanceTimersByTimeAsync(3000);

      expect(errors).toContain("DASH fatal startup timed out before stream initialization");
    } finally {
      await player.destroy();
      vi.useRealTimers();
    }
  });

  it("does not override DASH live-edge and buffer settings by default", async () => {
    const player = new DashJsPlayerImpl();
    const container = document.createElement("div");

    await player.initialize(
      container,
      { type: "dash/video/mp4", url: "https://edge.example/live/index.mpd" },
      { autoplay: false, muted: true },
      { source: [], meta: { tracks: [] }, type: "live" }
    );

    expect(dashMocks.updateSettings).toHaveBeenCalledTimes(1);
    expect(dashMocks.updateSettings).toHaveBeenCalledWith({
      streaming: {
        text: { defaultEnabled: false },
        delay: { useSuggestedPresentationDelay: true },
      },
      debug: {
        logLevel: 2,
      },
    });
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
