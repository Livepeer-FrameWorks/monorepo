import { describe, expect, it, vi } from "vitest";
import { PlayerController } from "../src/core/PlayerController";

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
