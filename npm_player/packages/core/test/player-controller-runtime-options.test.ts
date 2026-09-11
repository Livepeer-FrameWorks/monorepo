// @vitest-environment jsdom
import { afterEach, describe, expect, it, vi } from "vitest";
import { PlayerController } from "../src/core/PlayerController";

afterEach(() => vi.restoreAllMocks());

describe("runtime options retain placement", () => {
  it("notifies wrappers of mute changes before a player is attached", () => {
    const controller = new PlayerController({ contentId: "playback", viewerProtocol: "HLS" });
    const listener = vi.fn();
    controller.on("muteChange", listener);
    controller.updateConfig({ muted: true, autoplay: false });
    expect(controller.isMuted()).toBe(true);
    expect(listener).toHaveBeenLastCalledWith({ muted: true });
    controller.updateConfig({ muted: false });
    expect(controller.isMuted()).toBe(false);
    expect(listener).toHaveBeenLastCalledWith({ muted: false });
    controller.destroy();
  });

  it("updates the active player and restores zero volume without reattaching", () => {
    const controller = new PlayerController({ contentId: "playback", viewerProtocol: "HLS" });
    const video = document.createElement("video");
    const setMuted = vi.fn();
    const state = controller as any;
    state.videoElement = video;
    state.currentPlayer = { setMuted };
    video.volume = 0.6;
    controller.updateConfig({ muted: true });
    expect(video.muted).toBe(true);
    expect(setMuted).toHaveBeenLastCalledWith(true);
    video.volume = 0;
    controller.updateConfig({ muted: false });
    expect(video.muted).toBe(false);
    expect(video.volume).toBe(0.6);
    expect(setMuted).toHaveBeenLastCalledWith(false);
    state.currentPlayer = null;
    controller.destroy();
  });
});
