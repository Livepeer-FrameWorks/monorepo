import { describe, expect, it, vi } from "vitest";
import { PlayerController } from "../src/core/PlayerController";
import type { PlayerEvents } from "../src/core/PlayerInterface";

function makeController(): PlayerController {
  return new PlayerController({
    contentId: "test-stream",
    contentType: "live",
    playerManager: {
      on: vi.fn(() => () => {}),
    } as any,
  });
}

describe("PlayerController idle screen", () => {
  it("keeps live idle visible while playback has not started and stream state is offline", () => {
    const controller = makeController();
    (controller as any).streamState = { isOnline: false, status: "OFFLINE" };

    expect(controller.shouldShowIdleScreen()).toBe(true);
  });

  it("does not let stale live stream-state errors cover active playback", () => {
    const controller = makeController();
    (controller as any)._hasPlaybackStarted = true;
    (controller as any).streamState = { isOnline: false, status: "ERROR" };

    expect(controller.shouldShowIdleScreen()).toBe(false);
  });

  it("marks direct-rendering player timeupdate as playback progress", () => {
    const controller = makeController();
    const listeners = new Map<keyof PlayerEvents, Set<(data: any) => void>>();
    const player = {
      isDirectRendering: true,
      isPaused: () => false,
      getCurrentTime: () => 492_000,
      getDuration: () => Infinity,
      on: vi.fn((event: keyof PlayerEvents, listener: (data: any) => void) => {
        if (!listeners.has(event)) listeners.set(event, new Set());
        listeners.get(event)!.add(listener);
      }),
      off: vi.fn(),
    };
    (controller as any).currentPlayer = player;
    (controller as any).streamState = { isOnline: false, status: "ERROR" };
    const timeUpdate = vi.fn();
    controller.on("timeUpdate", timeUpdate);

    (controller as any).bindCurrentPlayerEvents(player);
    listeners.get("timeupdate")?.forEach((listener) => listener(492_000));

    expect(controller.hasPlaybackStarted()).toBe(true);
    expect(controller.shouldShowIdleScreen()).toBe(false);
    expect(timeUpdate).toHaveBeenCalledWith({ currentTime: 492_000, duration: Infinity });
  });
});
