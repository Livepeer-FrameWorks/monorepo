import { describe, it, expect, vi, beforeEach } from "vitest";
import type { ReactiveControllerHost } from "lit";
import { PlayerControllerHost } from "../src/controllers/player-controller-host.js";
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
});
