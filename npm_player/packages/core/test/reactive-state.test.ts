import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { createReactiveState, type ReactiveState } from "../src/vanilla/ReactiveState";

function makeMockController() {
  const eventListeners = new Map<string, Set<() => void>>();

  const ctrl: any = {
    isPaused: vi.fn().mockReturnValue(true),
    isPlaying: vi.fn().mockReturnValue(false),
    getCurrentTime: vi.fn().mockReturnValue(0),
    getDuration: vi.fn().mockReturnValue(0),
    getVolume: vi.fn().mockReturnValue(1),
    isMuted: vi.fn().mockReturnValue(false),
    getPlaybackRate: vi.fn().mockReturnValue(1),
    isLoopEnabled: vi.fn().mockReturnValue(false),
    isBuffering: vi.fn().mockReturnValue(false),
    isFullscreen: vi.fn().mockReturnValue(false),
    isPiPActive: vi.fn().mockReturnValue(false),
    getTracks: vi.fn().mockReturnValue([]),
    getStreamState: vi.fn().mockReturnValue(null),
    getError: vi.fn().mockReturnValue(null),
    getState: vi.fn().mockReturnValue("idle"),
    getVideoElement: vi.fn().mockReturnValue(null),

    on: vi.fn((event: string, listener: () => void) => {
      if (!eventListeners.has(event)) {
        eventListeners.set(event, new Set());
      }
      eventListeners.get(event)!.add(listener);
      return () => {
        eventListeners.get(event)?.delete(listener);
      };
    }),
  };

  function fireEvent(event: string) {
    const listeners = eventListeners.get(event);
    if (listeners) {
      for (const fn of listeners) fn();
    }
  }

  return { ctrl, fireEvent };
}

describe("createReactiveState", () => {
  let ctrl: any;
  let fireEvent: (event: string) => void;
  let state: ReactiveState;

  beforeEach(() => {
    const mock = makeMockController();
    ctrl = mock.ctrl;
    fireEvent = mock.fireEvent;
    state = createReactiveState(ctrl);
  });

  afterEach(() => {
    vi.restoreAllMocks();
  });

  describe("on()", () => {
    it("fires immediately with current value", () => {
      ctrl.isPaused.mockReturnValue(true);
      const cb = vi.fn();
      state.on("paused", cb);
      expect(cb).toHaveBeenCalledTimes(1);
      expect(cb).toHaveBeenCalledWith(true);
    });

    it("subscribes to controller events and fires on change", () => {
      const cb = vi.fn();
      state.on("volume", cb);
      expect(cb).toHaveBeenCalledTimes(1); // initial fire

      ctrl.getVolume.mockReturnValue(0.5);
      fireEvent("volumeChange");
      expect(cb).toHaveBeenCalledTimes(2);
      expect(cb).toHaveBeenLastCalledWith(0.5);
    });

    it("does not fire when value unchanged", () => {
      ctrl.isPaused.mockReturnValue(true);
      const cb = vi.fn();
      state.on("paused", cb);
      expect(cb).toHaveBeenCalledTimes(1);

      // Fire stateChange but value stays the same
      fireEvent("stateChange");
      expect(cb).toHaveBeenCalledTimes(1);
    });

    it("fires when value changes to different value", () => {
      ctrl.isPaused.mockReturnValue(true);
      const cb = vi.fn();
      state.on("paused", cb);

      ctrl.isPaused.mockReturnValue(false);
      fireEvent("stateChange");
      expect(cb).toHaveBeenCalledTimes(2);
      expect(cb).toHaveBeenLastCalledWith(false);
    });

    it("catches subscriber errors silently", () => {
      const cb = vi.fn().mockImplementation(() => {
        throw new Error("subscriber error");
      });
      expect(() => state.on("paused", cb)).not.toThrow();
      expect(cb).toHaveBeenCalledTimes(1);
    });

    it("catches subscriber errors on event fire", () => {
      const errorCb = vi.fn().mockImplementation(() => {
        throw new Error("boom");
      });
      const goodCb = vi.fn();
      state.on("volume", errorCb);
      state.on("volume", goodCb);

      ctrl.getVolume.mockReturnValue(0.7);
      expect(() => fireEvent("volumeChange")).not.toThrow();
      expect(goodCb).toHaveBeenCalledTimes(2);
    });

    it("returns an unsubscribe function", () => {
      const cb = vi.fn();
      const unsub = state.on("volume", cb);
      expect(cb).toHaveBeenCalledTimes(1);

      ctrl.getVolume.mockReturnValue(0.3);
      fireEvent("volumeChange");
      expect(cb).toHaveBeenCalledTimes(2);

      unsub();

      ctrl.getVolume.mockReturnValue(0.8);
      fireEvent("volumeChange");
      expect(cb).toHaveBeenCalledTimes(2); // not called again
    });

    it("returns no-op for unknown property", () => {
      const cb = vi.fn();
      const unsub = state.on("nonexistent" as any, cb);
      expect(cb).not.toHaveBeenCalled();
      expect(typeof unsub).toBe("function");
      unsub(); // should not throw
    });

    it("subscribes to multiple events for multi-event props", () => {
      const cb = vi.fn();
      state.on("error", cb);
      // error maps to ["error", "errorCleared"] events
      expect(ctrl.on).toHaveBeenCalledWith("error", expect.any(Function));
      expect(ctrl.on).toHaveBeenCalledWith("errorCleared", expect.any(Function));
    });

    it("only subscribes to controller event once even with multiple listeners", () => {
      const cb1 = vi.fn();
      const cb2 = vi.fn();
      state.on("volume", cb1);
      state.on("volume", cb2);

      // volumeChange should only be subscribed once
      const volumeChangeCalls = ctrl.on.mock.calls.filter((c: any[]) => c[0] === "volumeChange");
      expect(volumeChangeCalls).toHaveLength(1);
    });
  });

  describe("get()", () => {
    it("returns current value for known property", () => {
      ctrl.getCurrentTime.mockReturnValue(42000);
      expect(state.get("currentTime")).toBe(42000);
    });

    it("returns undefined for unknown property", () => {
      expect(state.get("nonexistent" as any)).toBeUndefined();
    });
  });

  describe("off()", () => {
    it("removes listeners for a specific property", () => {
      const cb = vi.fn();
      state.on("volume", cb);
      state.off("volume");

      ctrl.getVolume.mockReturnValue(0.5);
      fireEvent("volumeChange");
      expect(cb).toHaveBeenCalledTimes(1); // only initial fire
    });

    it("clears all listeners and controller subscriptions when called with no args", () => {
      const cb1 = vi.fn();
      const cb2 = vi.fn();
      state.on("volume", cb1);
      state.on("paused", cb2);

      state.off();

      ctrl.getVolume.mockReturnValue(0.5);
      fireEvent("volumeChange");
      ctrl.isPaused.mockReturnValue(false);
      fireEvent("stateChange");

      expect(cb1).toHaveBeenCalledTimes(1); // only initial
      expect(cb2).toHaveBeenCalledTimes(1); // only initial
    });
  });

  describe("compound properties", () => {
    it("loading is true for booting state", () => {
      ctrl.getState.mockReturnValue("booting");
      expect(state.get("loading")).toBe(true);
    });

    it("loading is true for gateway_loading state", () => {
      ctrl.getState.mockReturnValue("gateway_loading");
      expect(state.get("loading")).toBe(true);
    });

    it("loading is true for connecting state", () => {
      ctrl.getState.mockReturnValue("connecting");
      expect(state.get("loading")).toBe(true);
    });

    it("loading is true for selecting_player state", () => {
      ctrl.getState.mockReturnValue("selecting_player");
      expect(state.get("loading")).toBe(true);
    });

    it("loading is false for playing state", () => {
      ctrl.getState.mockReturnValue("playing");
      expect(state.get("loading")).toBe(false);
    });

    it("ended is true when state is ended", () => {
      ctrl.getState.mockReturnValue("ended");
      expect(state.get("ended")).toBe(true);
    });

    it("ended is false when state is not ended", () => {
      ctrl.getState.mockReturnValue("playing");
      expect(state.get("ended")).toBe(false);
    });

    it("seeking returns false when video element is null", () => {
      ctrl.getVideoElement.mockReturnValue(null);
      expect(state.get("seeking")).toBe(false);
    });

    it("seeking delegates to video.seeking", () => {
      ctrl.getVideoElement.mockReturnValue({ seeking: true });
      expect(state.get("seeking")).toBe(true);
    });
  });

  describe("all property mappings", () => {
    it.each([
      ["paused", "isPaused", "stateChange"],
      ["playing", "isPlaying", "stateChange"],
      ["currentTime", "getCurrentTime", "timeUpdate"],
      ["duration", "getDuration", "timeUpdate"],
      ["volume", "getVolume", "volumeChange"],
      ["muted", "isMuted", "volumeChange"],
      ["playbackRate", "getPlaybackRate", "stateChange"],
      ["loop", "isLoopEnabled", "loopChange"],
      ["buffering", "isBuffering", "stateChange"],
      ["fullscreen", "isFullscreen", "fullscreenChange"],
      ["pip", "isPiPActive", "pipChange"],
      ["streamState", "getStreamState", "streamStateChange"],
    ] as const)("%s maps to %s via %s event", (prop, getter, event) => {
      const cb = vi.fn();
      state.on(prop as any, cb);
      expect(ctrl.on).toHaveBeenCalledWith(event, expect.any(Function));
    });

    it("tracks maps to getTracks via ready and playerSelected events", () => {
      const cb = vi.fn();
      state.on("tracks", cb);
      expect(ctrl.on).toHaveBeenCalledWith("ready", expect.any(Function));
      expect(ctrl.on).toHaveBeenCalledWith("playerSelected", expect.any(Function));
    });
  });
});
