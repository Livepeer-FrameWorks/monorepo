// @vitest-environment jsdom

import { describe, expect, it, vi } from "vitest";
import { PlayerController } from "../src/core/PlayerController";
import type { IPlayer, PlayerOptions, StreamInfo } from "../src/core/PlayerInterface";

function makeStreamInfo(): StreamInfo {
  return {
    source: [{ type: "dash/video/mp4", url: "https://edge.example/live/index.mpd" }],
    meta: { tracks: [{ type: "video", codec: "H264" }] },
    type: "live",
  };
}

function makeEventedPlayer(shortname = "wrapped"): IPlayer & {
  emit: (event: string, data?: unknown) => void;
} {
  const listeners = new Map<string, Set<(data: unknown) => void>>();
  return {
    capability: { name: shortname, shortname, priority: 1, mimes: ["dash/video/mp4"] },
    isMimeSupported: vi.fn(() => true),
    isBrowserSupported: vi.fn(() => ["video"]),
    initialize: vi.fn(),
    destroy: vi.fn(),
    getVideoElement: vi.fn(() => null),
    on: vi.fn((event: string, listener: (data: unknown) => void) => {
      const set = listeners.get(event) ?? new Set();
      set.add(listener);
      listeners.set(event, set);
    }) as any,
    off: vi.fn((event: string, listener: (data: unknown) => void) => {
      listeners.get(event)?.delete(listener);
    }) as any,
    emit: (event: string, data?: unknown) => {
      listeners.get(event)?.forEach((listener) => listener(data));
    },
  } as IPlayer & { emit: (event: string, data?: unknown) => void };
}

function makeInitializingController() {
  const player = makeEventedPlayer();
  const video = document.createElement("video");
  let capturedOptions: PlayerOptions | null = null;
  const manager = {
    on: vi.fn(() => () => {}),
    off: vi.fn(),
    destroy: vi.fn().mockResolvedValue(undefined),
    getCurrentPlayer: vi.fn(() => player),
    getRegisteredPlayers: vi.fn(() => [player]),
    canAttemptFallback: vi.fn(() => true),
    tryPlaybackFallback: vi.fn().mockResolvedValue(true),
    initializePlayer: vi.fn(async (_container, _streamInfo, options: PlayerOptions) => {
      capturedOptions = options;
      return video;
    }),
  };
  const controller = new PlayerController({
    contentId: "test-stream",
    contentType: "live",
    playerManager: manager as any,
  });
  (controller as any).container = document.createElement("div");
  (controller as any).streamInfo = makeStreamInfo();
  return { controller, manager, player, video, getOptions: () => capturedOptions };
}

function makeController(): PlayerController {
  return new PlayerController({
    contentId: "test-stream",
    contentType: "live",
    playerManager: {
      on: vi.fn(() => () => {}),
      destroy: vi.fn(),
    } as any,
  });
}

describe("PlayerController cold boot recovery", () => {
  it("tracks an initialized wrapper before its media-ready callback fires", async () => {
    const { controller, player, video } = makeInitializingController();
    const ready = vi.fn();
    controller.on("ready", ready);

    await (controller as any).initializePlayer();

    expect((controller as any).currentPlayer).toBe(player);
    expect((controller as any).videoElement).toBe(video);
    expect(player.on).toHaveBeenCalledWith("error", expect.any(Function));
    expect(player.on).toHaveBeenCalledWith("reloadrequested", expect.any(Function));
    expect(ready).not.toHaveBeenCalled();
    (controller as any).cleanup();
  });

  it("cleans stale media attachment state before accepting a replacement onReady", async () => {
    const { controller, video, getOptions } = makeInitializingController();
    await (controller as any).initializePlayer();
    const cleanup = vi.fn();
    (controller as any).mediaCleanupFns.push(cleanup);
    const previousResizeObserver = (globalThis as any).ResizeObserver;
    (globalThis as any).ResizeObserver = class {
      observe() {}
      disconnect() {}
    };

    try {
      getOptions()?.onReady?.(video);
    } finally {
      (globalThis as any).ResizeObserver = previousResizeObserver;
    }

    expect(cleanup).toHaveBeenCalledOnce();
    expect((controller as any).videoElement).toBe(video);
    (controller as any).cleanup();
  });

  it("bridges wrapped player errors into controller passive error handling", async () => {
    const { controller, player } = makeInitializingController();
    const setPassiveError = vi
      .spyOn(controller as any, "setPassiveError")
      .mockResolvedValue(undefined);

    await (controller as any).initializePlayer();
    player.emit("error", new Error("wrapped player failed"));

    expect(setPassiveError).toHaveBeenCalledWith("wrapped player failed");
    (controller as any).cleanup();
  });

  it("routes wrapped reload requests through fallback when available", async () => {
    const { controller, player } = makeInitializingController();
    const retryWithFallback = vi
      .spyOn(controller as any, "retryWithFallback")
      .mockResolvedValue(true);

    await (controller as any).initializePlayer();
    player.emit("reloadrequested", { reason: "segment decode overflow" });

    expect(retryWithFallback).toHaveBeenCalledOnce();
    (controller as any).cleanup();
  });

  it("reinitializes from Mist stream info when an edge-known stream comes online before playback started", async () => {
    const c = makeController();
    (c as any).container = { innerHTML: "" };
    (c as any).videoElement = null;
    (c as any).currentPlayer = null;
    (c as any)._hasPlaybackStarted = false;

    const lateInit = vi
      .spyOn(c as any, "initializeLateFromStreamState")
      .mockResolvedValue(undefined);
    const retry = vi.spyOn(c as any, "retry").mockResolvedValue(undefined);

    await (c as any).recoverPlaybackAfterOnlineTransition({
      source: [
        { type: "html5/application/vnd.apple.mpegurl", url: "https://edge/live/index.m3u8" },
      ],
    });

    expect(lateInit).toHaveBeenCalledOnce();
    expect(retry).not.toHaveBeenCalled();
  });

  it("uses the selected player play path when Mist is online and a player is already attached", async () => {
    const c = makeController();
    const video = {
      paused: true,
      muted: false,
      volume: 1,
      play: vi.fn().mockResolvedValue(undefined),
      pause: vi.fn(),
      addEventListener: vi.fn(),
      removeEventListener: vi.fn(),
    };
    const currentPlayer = {
      play: vi.fn().mockResolvedValue(undefined),
    };

    (c as any).container = { innerHTML: "" };
    (c as any).videoElement = video;
    (c as any).currentPlayer = currentPlayer;
    (c as any)._hasPlaybackStarted = false;

    const lateInit = vi
      .spyOn(c as any, "initializeLateFromStreamState")
      .mockResolvedValue(undefined);

    await (c as any).recoverPlaybackAfterOnlineTransition({
      source: [
        { type: "html5/application/vnd.apple.mpegurl", url: "https://edge/live/index.m3u8" },
      ],
    });

    expect(lateInit).not.toHaveBeenCalled();
    expect(currentPlayer.play).toHaveBeenCalledOnce();
    expect(video.play).not.toHaveBeenCalled();
  });

  it("cancels the stale tracer and attaches the fresh tracer when in-flight init recovers", async () => {
    const c = makeController();
    const video = {
      paused: true,
      muted: false,
      volume: 1,
      play: vi.fn().mockResolvedValue(undefined),
      pause: vi.fn(),
    };
    const staleTracer = {
      cancel: vi.fn(),
    };
    const freshTracer = {
      attachVideo: vi.fn(),
    };
    const currentPlayer = {
      play: vi.fn().mockResolvedValue(undefined),
    };

    (c as any).container = { innerHTML: "" };
    (c as any).videoElement = video;
    (c as any).currentPlayer = currentPlayer;
    (c as any)._hasPlaybackStarted = false;
    (c as any)._initializePlayerInFlight = Promise.resolve();
    (c as any).bootTracer = staleTracer;
    (c as any).state = "connecting";

    vi.spyOn(c as any, "createBootTracer").mockReturnValue(freshTracer);
    const autoplay = vi.spyOn(c as any, "attemptConfiguredAutoplay").mockResolvedValue(true);

    await (c as any).recoverPlaybackAfterOnlineTransition({
      source: [
        { type: "html5/application/vnd.apple.mpegurl", url: "https://edge/live/index.m3u8" },
      ],
    });

    expect(staleTracer.cancel).toHaveBeenCalledOnce();
    expect(freshTracer.attachVideo).toHaveBeenCalledWith(video);
    expect(autoplay).toHaveBeenCalledWith(video, "online transition", 0);
  });

  it("retries selected player playback muted before requiring user interaction", async () => {
    const c = makeController();
    const video = {
      paused: true,
      muted: false,
      volume: 1,
      play: vi.fn().mockResolvedValue(undefined),
      pause: vi.fn(),
    };
    const currentPlayer = {
      play: vi
        .fn()
        .mockRejectedValueOnce(new Error("autoplay blocked"))
        .mockResolvedValueOnce(undefined),
    };

    (c as any).currentPlayer = currentPlayer;
    const result = await (c as any).attemptConfiguredAutoplay(video, "test", 0);

    expect(result).toBe(true);
    expect(currentPlayer.play).toHaveBeenCalledTimes(2);
    expect(video.muted).toBe(true);
    expect(video.play).not.toHaveBeenCalled();
  });
});
