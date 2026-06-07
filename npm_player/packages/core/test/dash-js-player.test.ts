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
    dashMocks.on.mockReset();
    dashMocks.off.mockReset();
    dashMocks.reset.mockReset();
  });

  it("reads proxied media properties with the native video element receiver", () => {
    const player = new DashJsPlayerImpl();
    const video = document.createElement("video");

    Object.defineProperty(video, "duration", {
      configurable: true,
      get() {
        if (this !== video) {
          throw new TypeError("duration getter called with wrong receiver");
        }
        return 42;
      },
    });

    const proxy = (player as any).createVideoProxy(video) as HTMLVideoElement;

    expect(proxy.duration).toBe(42);
  });

  it("routes dash.js live DVR null-range rejections through onError", () => {
    const player = new DashJsPlayerImpl();
    const errors: string[] = [];
    let prevented = false;
    let stopped = false;

    const handler = (player as any).createInternalRejectionHandler({
      onError: (error: string | Error) => errors.push(String(error)),
    });

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

    const handler = (player as any).createInternalRejectionHandler({
      onError: (error: string | Error) => errors.push(String(error)),
    });

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

  it("applies caller dashConfig after the built-in text default", async () => {
    const player = new DashJsPlayerImpl();
    const container = document.createElement("div");
    const dashConfig = { streaming: { delay: { liveDelay: 2 } } };

    await player.initialize(
      container,
      { type: "dash/video/mp4", url: "https://edge.example/live/index.mpd" },
      { autoplay: false, muted: true, dashConfig },
      { source: [], meta: { tracks: [] }, type: "live" }
    );

    // The built-in text default is applied first, then the caller override (dash.js deep-merges),
    // so the final updateSettings call carries the caller's dashConfig.
    const calls = dashMocks.updateSettings.mock.calls;
    expect(calls.length).toBeGreaterThanOrEqual(2);
    expect(calls[calls.length - 1][0]).toEqual(dashConfig);
  });

  it("emits ready after dash.js initializes the stream", async () => {
    const player = new DashJsPlayerImpl();
    const container = document.createElement("div");
    const onReady = vi.fn();

    await player.initialize(
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
    expect(onReady).not.toHaveBeenCalled();

    const video = container.querySelector("video") as HTMLVideoElement;
    video.dispatchEvent(new Event("loadedmetadata"));

    expect(onReady).toHaveBeenCalledWith(video);
    expect(dashMocks.initialize.mock.invocationCallOrder[0]).toBeLessThan(
      onReady.mock.invocationCallOrder[0]
    );
  });

  it("rejects initialization instead of emitting ready when dash.js never starts", async () => {
    vi.useFakeTimers();
    dashMocks.state.startupEvent = null;
    const player = new DashJsPlayerImpl();
    const container = document.createElement("div");
    const onReady = vi.fn();

    try {
      const initialization = player.initialize(
        container,
        { type: "dash/video/mp4", url: "https://edge.example/live/index.mpd" },
        { autoplay: true, muted: true, onReady },
        { source: [], meta: { tracks: [] }, type: "live" }
      );
      const expectedFailure = expect(initialization).rejects.toThrow(
        "DASH startup failed: DASH startup timed out before stream initialization"
      );

      await vi.waitFor(() => expect(dashMocks.initialize).toHaveBeenCalled());
      await vi.advanceTimersByTimeAsync(3000);

      await expectedFailure;
      expect(onReady).not.toHaveBeenCalled();
    } finally {
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
      },
      debug: {
        logLevel: 2,
      },
    });
  });

  it("reports repeated startup fragment abandonment before playable media", async () => {
    const player = new DashJsPlayerImpl();
    const container = document.createElement("div");
    const onError = vi.fn();

    await player.initialize(
      container,
      { type: "dash/video/mp4", url: "https://edge.example/live/index.mpd" },
      { autoplay: false, muted: true, onError },
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

    expect(onError).toHaveBeenCalledWith("DASH fatal startup fragment abandoned repeatedly");
  });
});
