import { describe, it, expect, vi, beforeEach } from "vitest";
import type { ReactiveControllerHost } from "lit";
import { PlayerControllerHost } from "../src/controllers/player-controller-host.js";
import { PlayerController } from "@livepeer-frameworks/player-core";
import {
  WRAPPER_PARITY_ACTION_METHODS,
  WRAPPER_PARITY_INITIAL_STATE,
} from "../../test-contract/player-wrapper-contract";

// Minimal mock host
function createMockHost(): ReactiveControllerHost & HTMLElement {
  const host = document.createElement("div") as unknown as ReactiveControllerHost & HTMLElement;
  (host as any).addController = vi.fn();
  (host as any).requestUpdate = vi.fn();
  (host as any).removeController = vi.fn();
  (host as any).updateComplete = Promise.resolve(true);
  return host;
}

describe("PlayerControllerHost", () => {
  let host: ReturnType<typeof createMockHost>;
  let pc: PlayerControllerHost;

  beforeEach(() => {
    host = createMockHost();
    pc = new PlayerControllerHost(host);
  });

  it("registers itself with the host on construction", () => {
    expect((host as any).addController).toHaveBeenCalledWith(pc);
  });

  it("updates only changed runtime options without replacing placement", async () => {
    const attach = vi.spyOn(PlayerController.prototype, "attach").mockResolvedValue(undefined);
    const update = vi.spyOn(PlayerController.prototype, "updateConfig");
    const base = { contentId: "playback", viewerProtocol: "HLS" as const };
    pc.configure(base);
    await pc.attach(document.createElement("div"));
    pc.configure({ ...base, muted: true, autoplay: false });
    expect(update).toHaveBeenLastCalledWith({ muted: true, autoplay: false });
    expect(pc.s.isMuted).toBe(true);
    pc.configure({ ...base, muted: true, autoplay: false, debug: true });
    expect(update).toHaveBeenLastCalledWith({ debug: true });
    pc.configure(base);
    expect(update).toHaveBeenLastCalledWith({ muted: false, autoplay: true, debug: false });
    expect(pc.s.isMuted).toBe(false);
    expect(attach).toHaveBeenCalledOnce();
    pc.hostDisconnected();
  });

  it("has correct initial state", () => {
    for (const [key, expected] of Object.entries(WRAPPER_PARITY_INITIAL_STATE)) {
      expect(pc.s[key as keyof typeof pc.s]).toEqual(expected);
    }
    expect(pc.s.duration).toBeNaN();
    expect(pc.s.qualities).toEqual([]);
    expect(pc.s.textTracks).toEqual([]);
  });

  it("resets state on hostDisconnected", () => {
    // Mutate state
    (pc as any).update({ isPlaying: true, currentTime: 42 });
    expect(pc.s.isPlaying).toBe(true);

    pc.hostDisconnected();
    expect(pc.s.state).toBe("booting");
    expect(pc.s.isPlaying).toBe(false);
    expect(pc.s.currentTime).toBe(0);
  });

  it("action methods are safe to call without controller", async () => {
    for (const actionName of WRAPPER_PARITY_ACTION_METHODS) {
      expect(typeof pc[actionName]).toBe("function");
    }

    // These should not throw
    await pc.play();
    pc.pause();
    pc.togglePlay();
    pc.seek(10);
    pc.seekBy(5);
    pc.jumpToLive();
    pc.setVolume(0.5);
    pc.toggleMute();
    pc.toggleLoop();
    await pc.toggleFullscreen();
    await pc.togglePiP();
    pc.toggleSubtitles();
    pc.clearError();
    pc.dismissToast();
    await pc.retry();
    await pc.reload();
    pc.selectQuality("auto");
    pc.handleMouseEnter();
    pc.handleMouseLeave();
    pc.handleMouseMove();
    pc.handleTouchStart();
    await pc.setDevModeOptions({ forcePlayer: "native" });
    expect(pc.getQualities()).toEqual([]);
    expect(pc.getController()).toBeNull();
  });

  it("clearError updates state", () => {
    (pc as any).update({ error: "test error", errorDetails: { code: "E" }, isPassiveError: true });
    expect(pc.s.error).toBe("test error");

    pc.clearError();
    expect(pc.s.error).toBeNull();
    expect(pc.s.errorDetails).toBeNull();
    expect(pc.s.isPassiveError).toBe(false);
  });

  it("dismissToast clears toast", () => {
    (pc as any).update({ toast: { message: "hi", timestamp: Date.now() } });
    expect(pc.s.toast).not.toBeNull();

    pc.dismissToast();
    expect(pc.s.toast).toBeNull();
  });

  it("configure stores config", () => {
    pc.configure({
      contentId: "test",
      contentType: "live" as any,
      autoplay: true,
      muted: true,
      controls: true,
    });
    expect((pc as any).currentConfig).not.toBeNull();
  });

  it("replaces a pending controller and clears stale state when placement inputs change", async () => {
    const attach = vi
      .spyOn(PlayerController.prototype, "attach")
      .mockImplementation(() => new Promise(() => {}));
    pc.configure({ contentId: "playback", viewerProtocol: "HLS", playbackAuth: { token: "old" } });
    void pc.attach(document.createElement("div"));
    const previous = (pc as any).controller;
    pc.s.error = "old error";
    pc.configure({ contentId: "playback", viewerProtocol: "DASH", playbackAuth: { token: "new" } });
    expect(attach).toHaveBeenCalledTimes(2);
    expect(previous.isDestroyed).toBe(true);
    expect((pc as any).controller.config).toMatchObject({
      viewerProtocol: "DASH",
      playbackAuth: { token: "new" },
    });
    expect(pc.s.error).toBeNull();
    pc.hostDisconnected();
  });

  it("updates display settings without reconnecting an equivalent request", async () => {
    const attach = vi.spyOn(PlayerController.prototype, "attach").mockResolvedValue(undefined);
    const update = vi.spyOn(PlayerController.prototype, "updateConfig");
    pc.configure({ contentId: "playback", viewerProtocol: "HLS", playbackAuth: { token: "same" } });
    await pc.attach(document.createElement("div"));
    pc.configure({
      contentId: "playback",
      viewerProtocol: "HLS",
      playbackAuth: { token: "same" },
      debug: true,
    });
    expect(attach).toHaveBeenCalledTimes(1);
    expect(update).toHaveBeenCalledWith(expect.objectContaining({ debug: true }));
    pc.hostDisconnected();
  });

  it("does not reconnect while disconnected and resumes with the latest request", async () => {
    const attach = vi.spyOn(PlayerController.prototype, "attach").mockResolvedValue(undefined);
    pc.configure({ contentId: "old", viewerProtocol: "HLS" });
    await pc.attach(document.createElement("div"));
    pc.hostDisconnected();
    pc.configure({ contentId: "new", viewerProtocol: "DASH" });
    expect(attach).toHaveBeenCalledTimes(1);
    pc.hostConnected();
    expect(attach).toHaveBeenCalledTimes(2);
    expect((pc as any).controller.config).toMatchObject({
      contentId: "new",
      viewerProtocol: "DASH",
    });
    pc.hostDisconnected();
  });

  it("forwards the required viewer format when constructing the controller", async () => {
    vi.spyOn(PlayerController.prototype, "attach").mockResolvedValue(undefined);
    pc.configure({ contentId: "test", viewerProtocol: "HLS" });
    await pc.attach(document.createElement("div"));
    expect((pc as any).controller.config.viewerProtocol).toBe("HLS");
    pc.hostDisconnected();
  });
});
