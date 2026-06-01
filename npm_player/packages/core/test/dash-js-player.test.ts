// @vitest-environment jsdom

import { beforeEach, describe, expect, it, vi } from "vitest";

import { DashJsPlayerImpl } from "../src/players/DashJsPlayer";

const dashMocks = vi.hoisted(() => ({
  initialize: vi.fn(),
  updateSettings: vi.fn(),
  on: vi.fn(),
  reset: vi.fn(),
}));

vi.mock("dashjs", () => ({
  default: {
    MediaPlayer: () => ({
      create: () => ({
        initialize: dashMocks.initialize,
        updateSettings: dashMocks.updateSettings,
        on: dashMocks.on,
        reset: dashMocks.reset,
      }),
    }),
  },
}));

describe("DashJsPlayerImpl", () => {
  beforeEach(() => {
    dashMocks.initialize.mockReset();
    dashMocks.updateSettings.mockReset();
    dashMocks.on.mockReset();
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

  it("emits ready after dash.js attaches the MPD", async () => {
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
      true
    );
    expect(onReady).toHaveBeenCalledWith(expect.any(HTMLVideoElement));
    expect(dashMocks.initialize.mock.invocationCallOrder[0]).toBeLessThan(
      onReady.mock.invocationCallOrder[0]
    );
  });

  it("uses the manifest live delay hint for live DASH startup", async () => {
    const player = new DashJsPlayerImpl();
    const container = document.createElement("div");

    await player.initialize(
      container,
      { type: "dash/video/mp4", url: "https://edge.example/live/index.mpd" },
      { autoplay: false, muted: true },
      { source: [], meta: { tracks: [] }, type: "live" }
    );

    expect(dashMocks.updateSettings).toHaveBeenCalledWith(
      expect.objectContaining({
        streaming: expect.objectContaining({
          delay: {
            liveDelay: 5,
            liveDelayFragmentCount: null,
            useSuggestedPresentationDelay: true,
          },
        }),
      })
    );
  });
});
