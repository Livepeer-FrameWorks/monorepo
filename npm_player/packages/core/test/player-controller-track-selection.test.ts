import { describe, expect, it, vi } from "vitest";

import { PlayerController } from "../src/core/PlayerController";
import { StreamStateClient } from "../src/core/StreamStateClient";

function controllerWithTextTracks() {
  const controller = new PlayerController({
    contentId: "live-1",
    playerManager: { on: vi.fn(() => () => {}) } as any,
  });
  const selectTextTrack = vi.fn();
  (controller as any).currentPlayer = {
    selectTextTrack,
    getTextTracks: () => [
      { id: "4", label: "English", active: false },
      { id: "5", label: "Spanish", active: false },
    ],
  };
  return { controller, selectTextTrack };
}

describe("PlayerController text-track selection", () => {
  it("selects an explicit track exactly once when captions were disabled", () => {
    const { controller, selectTextTrack } = controllerWithTextTracks();

    controller.selectTextTrack("5");

    expect(selectTextTrack).toHaveBeenCalledTimes(1);
    expect(selectTextTrack).toHaveBeenCalledWith("5");
    expect(controller.isSubtitlesEnabled()).toBe(true);
  });

  it("restores the last explicit track after captions are toggled off and on", () => {
    const { controller, selectTextTrack } = controllerWithTextTracks();
    controller.selectTextTrack("5");

    controller.setSubtitlesEnabled(false);
    controller.setSubtitlesEnabled(true);

    expect(selectTextTrack.mock.calls).toEqual([["5"], [null], ["5"]]);
  });

  it("keeps an early enable request retryable until a track can be applied", () => {
    const controller = new PlayerController({
      contentId: "live-1",
      playerManager: { on: vi.fn(() => () => {}) } as any,
    });
    const selectTextTrack = vi.fn();
    let tracks: Array<{ id: string; label: string; active: boolean }> = [];
    (controller as any).currentPlayer = {
      selectTextTrack,
      getTextTracks: () => tracks,
    };

    controller.setSubtitlesEnabled(true);
    expect(controller.isSubtitlesEnabled()).toBe(true);
    expect(selectTextTrack).not.toHaveBeenCalled();

    tracks = [{ id: "7", label: "English", active: false }];
    controller.setSubtitlesEnabled(true);
    expect(controller.isSubtitlesEnabled()).toBe(true);
    expect(selectTextTrack).toHaveBeenCalledWith("7");
  });

  it("lets a second toggle cancel an enable request while tracks are absent", () => {
    const controller = new PlayerController({
      contentId: "live-1",
      playerManager: { on: vi.fn(() => () => {}) } as any,
    });
    const selectTextTrack = vi.fn();
    let tracks: Array<{ id: string; label: string; active: boolean }> = [];
    (controller as any).currentPlayer = {
      selectTextTrack,
      getTextTracks: () => tracks,
    };

    controller.setSubtitlesEnabled(true);
    controller.toggleSubtitles();
    tracks = [{ id: "7", label: "English", active: false }];
    (controller as any).applyRequestedSubtitleSelection();

    expect(selectTextTrack.mock.calls).toEqual([[null]]);
    expect(controller.isSubtitlesEnabled()).toBe(false);
  });

  it("does not replace an explicit track choice while that track is absent", () => {
    const { controller, selectTextTrack } = controllerWithTextTracks();
    (controller as any).currentPlayer.getTextTracks = () => [
      { id: "4", label: "English", active: true },
    ];

    controller.selectTextTrack("5");

    expect(selectTextTrack).not.toHaveBeenCalled();
    expect((controller as any).selectedTextTrackId).toBe("5");
    expect(controller.isSubtitlesEnabled()).toBe(true);
  });

  it("applies a pending caption request when live tracks appear later", () => {
    let stateChange: ((payload: any) => void) | undefined;
    vi.spyOn(StreamStateClient.prototype, "on").mockImplementation((event, callback) => {
      if (event === "stateChange") stateChange = callback as (payload: any) => void;
      return () => {};
    });
    vi.spyOn(StreamStateClient.prototype, "start").mockImplementation(() => {});
    const controller = new PlayerController({
      contentId: "live-1",
      mistUrl: "https://mist.test",
      playerManager: { on: vi.fn(() => () => {}) } as any,
    });
    const selectTextTrack = vi.fn();
    const trackChanges = vi.fn();
    controller.on("tracksChange", trackChanges);
    (controller as any).streamInfo = { source: [], meta: { tracks: [] }, type: "live" };
    (controller as any).currentPlayer = {
      selectTextTrack,
      getTextTracks: () =>
        (controller as any).streamInfo.meta.tracks
          .filter((track: any) => track.type === "meta" && track.codec === "subtitle")
          .map((track: any) => ({ id: String(track.idx), label: "English", active: false })),
    };

    controller.setSubtitlesEnabled(true);
    (controller as any).startStreamStatePolling();
    const liveState = {
      state: {
        isOnline: true,
        status: "online",
        streamInfo: {
          meta: { tracks: { "7": { type: "meta", codec: "subtitle", idx: 7 } } },
        },
      },
    };
    stateChange?.(liveState);
    stateChange?.(liveState);

    expect(selectTextTrack).toHaveBeenCalledWith("7");
    expect(selectTextTrack).toHaveBeenCalledTimes(1);
    expect(controller.isSubtitlesEnabled()).toBe(true);
    expect(trackChanges).toHaveBeenCalledTimes(2);
  });

  it("maps an explicit caption semantically across protocol track namespaces", () => {
    const { controller } = controllerWithTextTracks();
    const firstPlayer = (controller as any).currentPlayer;
    firstPlayer.getTextTracks = () => [{ id: "5", label: "English", lang: "en", active: false }];
    controller.selectTextTrack("5");

    const selectTextTrack = vi.fn();
    (controller as any).currentPlayer = {
      selectTextTrack,
      getTextTracks: () => [
        { id: "5", label: "Spanish", lang: "es", active: false },
        { id: "12", label: "English", lang: "en", active: false },
      ],
    };
    (controller as any).appliedTextTrackId = null;

    expect((controller as any).applyRequestedSubtitleSelection()).toBe(true);
    expect(selectTextTrack).toHaveBeenCalledWith("12");
  });

  it("does not reuse an explicit id or forget its language after a protocol swap", () => {
    const { controller } = controllerWithTextTracks();
    const firstPlayer = (controller as any).currentPlayer;
    firstPlayer.getTextTracks = () => [{ id: "5", label: "English", lang: "en", active: false }];
    controller.selectTextTrack("5");

    const selectTextTrack = vi.fn();
    (controller as any).currentPlayer = {
      selectTextTrack,
      getTextTracks: () => [{ id: "5", label: "Spanish", lang: "es", active: false }],
    };
    (controller as any).appliedTextTrackId = null;

    expect((controller as any).applyRequestedSubtitleSelection()).toBe(false);
    expect(selectTextTrack).not.toHaveBeenCalled();

    controller.setSubtitlesEnabled(false);
    controller.setSubtitlesEnabled(true);
    expect(selectTextTrack.mock.calls).toEqual([[null]]);
    expect((controller as any).selectedTextTrackLang).toBe("en");
  });

  it("applies the semantic caption match when fallback tracks arrive late", () => {
    const { controller } = controllerWithTextTracks();
    const firstPlayer = (controller as any).currentPlayer;
    firstPlayer.getTextTracks = () => [{ id: "5", label: "English", lang: "en", active: false }];
    controller.selectTextTrack("5");

    let tracks: Array<{ id: string; label: string; lang: string; active: boolean }> = [];
    const selectTextTrack = vi.fn();
    const nextPlayer = {
      selectTextTrack,
      getTextTracks: () => tracks,
    };
    (controller as any).currentPlayer = nextPlayer;
    (controller as any).appliedTextTrackId = null;

    expect((controller as any).applyRequestedSubtitleSelection()).toBe(false);
    expect((controller as any).selectedTextTrackOwner).toBe(firstPlayer);

    tracks = [{ id: "12", label: "English", lang: "en", active: false }];
    expect((controller as any).applyRequestedSubtitleSelection()).toBe(true);
    expect(selectTextTrack).toHaveBeenCalledWith("12");
    expect((controller as any).selectedTextTrackOwner).toBe(nextPlayer);
  });

  it("reapplies a requested caption when late tracks disappear and return", () => {
    const controller = new PlayerController({
      contentId: "live-1",
      playerManager: { on: vi.fn(() => () => {}) } as any,
    });
    const selectTextTrack = vi.fn();
    const listeners = new Map<string, () => void>();
    let tracks: Array<{ id: string; label: string; active: boolean }> = [];
    const player = {
      selectTextTrack,
      getTextTracks: () => tracks,
      getAudioTracks: () => [],
      on: vi.fn((event: string, listener: () => void) => listeners.set(event, listener)),
      off: vi.fn(),
      isDirectRendering: false,
    };
    (controller as any).currentPlayer = player;
    const trackChanges = vi.fn();
    controller.on("tracksChange", trackChanges);
    controller.selectTextTrack("7");
    (controller as any).bindCurrentPlayerEvents(player);

    tracks = [{ id: "7", label: "English", active: false }];
    listeners.get("trackschange")?.();
    tracks = [];
    listeners.get("trackschange")?.();
    tracks = [{ id: "7", label: "English", active: false }];
    listeners.get("trackschange")?.();

    expect(selectTextTrack.mock.calls).toEqual([["7"], [null], ["7"]]);
    expect(controller.isSubtitlesEnabled()).toBe(true);
    expect(trackChanges).toHaveBeenCalledTimes(3);
  });

  it("resets caption selection at each attach boundary", () => {
    const controller = new PlayerController({
      contentId: "live-1",
      playerManager: { on: vi.fn(() => () => {}) } as any,
    });
    (controller as any)._subtitlesEnabled = true;
    (controller as any).subtitleEnableRequested = true;
    (controller as any).selectedTextTrackId = "7";
    (controller as any).selectedTextTrackExplicit = true;
    (controller as any).appliedTextTrackId = "7";
    (controller as any).resetAttachScopedTrackSelection();

    expect(controller.isSubtitlesEnabled()).toBe(false);
    expect((controller as any).subtitleEnableRequested).toBe(false);
    expect((controller as any).selectedTextTrackId).toBeNull();
    expect((controller as any).selectedTextTrackExplicit).toBe(false);
    expect((controller as any).appliedTextTrackId).toBeNull();
  });

  it("renders selected MEWS subtitle metadata instead of transport-only selection", () => {
    vi.useFakeTimers();
    const originalDocument = globalThis.document;
    const overlay = { className: "", style: {}, textContent: "", remove: vi.fn() };
    (globalThis as any).document = { createElement: vi.fn(() => overlay) };
    const controller = new PlayerController({
      contentId: "live-1",
      playerManager: { on: vi.fn(() => () => {}) } as any,
    });
    let callback: ((event: any) => void) | undefined;
    let playbackTime = 1_000;
    (controller as any).container = { appendChild: vi.fn() };
    (controller as any).currentPlayer = {
      capability: { shortname: "mews" },
      getCurrentTime: () => playbackTime,
    };
    (controller as any).selectedTextTrackId = "7";
    (controller as any).metaTrackManager = {
      subscribe: (_trackId: string, cb: (event: any) => void) => {
        callback = cb;
        return vi.fn();
      },
      getServerSeekableRange: () => null,
    };

    (controller as any).syncMetadataSubtitleRenderer("7");
    callback?.({ type: "unknown", trackId: "7", timestamp: 1000, data: "Hello", durationMs: 500 });

    expect(overlay.textContent).toBe("Hello");
    vi.advanceTimersByTime(500);
    expect(overlay.textContent).toBe("Hello");
    playbackTime = 1_500;
    vi.advanceTimersByTime(100);
    expect(overlay.textContent).toBe("");
    (globalThis as any).document = originalDocument;
    vi.useRealTimers();
  });
});
