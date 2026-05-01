import { describe, expect, it, vi } from "vitest";

import { PlayerController } from "../src/core/PlayerController";

function makeController(muted = false): PlayerController {
  return new PlayerController({
    contentId: "test-stream",
    contentType: "live",
    muted,
    playerManager: {
      on: vi.fn(() => () => {}),
    } as any,
  });
}

describe("PlayerController mute state", () => {
  it("reports configured muted state before a video element is ready", () => {
    expect(makeController(true).isMuted()).toBe(true);
    expect(makeController(false).isMuted()).toBe(false);
  });

  it("toggleMute uses the effective player mute state", () => {
    const controller = makeController();
    const video = { muted: false, volume: 1 };
    (controller as any).videoElement = video;
    (controller as any).currentPlayer = {
      isMuted: () => true,
      setMuted: vi.fn((muted: boolean) => {
        video.muted = muted;
      }),
    };

    controller.toggleMute();

    expect(video.muted).toBe(false);
    expect((controller as any).currentPlayer.setMuted).toHaveBeenCalledWith(false);
  });
});
