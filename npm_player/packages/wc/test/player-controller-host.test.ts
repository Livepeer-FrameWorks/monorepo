import { describe, it, expect, vi, beforeEach } from "vitest";
import type { ReactiveControllerHost } from "lit";
import { PlayerControllerHost } from "../src/controllers/player-controller-host.js";

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
    expect(pc.s.state).toBe("booting");
    expect(pc.s.currentTime).toBe(0);
    expect(pc.s.duration).toBeNaN();
    expect(pc.s.isPlaying).toBe(false);
    expect(pc.s.isPaused).toBe(true);
    expect(pc.s.isMuted).toBe(true);
    expect(pc.s.volume).toBe(1);
    expect(pc.s.error).toBeNull();
    expect(pc.s.videoElement).toBeNull();
    expect(pc.s.isFullscreen).toBe(false);
    expect(pc.s.isPiPActive).toBe(false);
    expect(pc.s.shouldShowControls).toBe(false);
    expect(pc.s.isLoopEnabled).toBe(false);
    expect(pc.s.qualities).toEqual([]);
    expect(pc.s.textTracks).toEqual([]);
    expect(pc.s.toast).toBeNull();
    expect(pc.s.playbackQuality).toBeNull();
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

  it("action methods are safe to call without controller", () => {
    // These should not throw
    pc.pause();
    pc.togglePlay();
    pc.seek(10);
    pc.seekBy(5);
    pc.jumpToLive();
    pc.setVolume(0.5);
    pc.toggleMute();
    pc.toggleLoop();
    pc.toggleSubtitles();
    pc.clearError();
    pc.dismissToast();
    pc.selectQuality("auto");
    pc.handleMouseEnter();
    pc.handleMouseLeave();
    pc.handleMouseMove();
    pc.handleTouchStart();
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
