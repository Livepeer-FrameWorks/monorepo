import { describe, expect, it, vi, beforeEach } from "vitest";

import { createStudioReactiveState, type StudioStateMap } from "../src/vanilla/StudioReactiveState";

type MockHandler = (...args: any[]) => void;

function createMockController() {
  const handlers = new Map<string, Set<MockHandler>>();

  const ctrl: any = {
    getState: vi.fn().mockReturnValue("idle"),
    getStateContext: vi.fn().mockReturnValue({}),
    isStreaming: vi.fn().mockReturnValue(false),
    isCapturing: vi.fn().mockReturnValue(false),
    isReconnecting: vi.fn().mockReturnValue(false),
    getSources: vi.fn().mockReturnValue([]),
    getPrimaryVideoSource: vi.fn().mockReturnValue(null),
    getMasterVolume: vi.fn().mockReturnValue(1),
    getQualityProfile: vi.fn().mockReturnValue("broadcast"),
    isCompositorEnabled: vi.fn().mockReturnValue(false),
    isWebCodecsActive: vi.fn().mockReturnValue(false),
    isRecordingActive: vi.fn().mockReturnValue(false),
    getVideoCodecFamily: vi.fn().mockReturnValue("h264"),
    getCurrentBitrate: vi.fn().mockReturnValue(null),
    getCongestionLevel: vi.fn().mockReturnValue(null),

    on(event: string, handler: MockHandler): () => void {
      let set = handlers.get(event);
      if (!set) {
        set = new Set();
        handlers.set(event, set);
      }
      set.add(handler);
      return () => set!.delete(handler);
    },

    _emit(event: string) {
      const set = handlers.get(event);
      if (set) for (const h of set) h();
    },
  };

  return ctrl;
}

describe("StudioReactiveState", () => {
  let ctrl: ReturnType<typeof createMockController>;

  beforeEach(() => {
    ctrl = createMockController();
  });

  describe("on()", () => {
    it("fires immediately with current value", () => {
      const rs = createStudioReactiveState(ctrl);
      const cb = vi.fn();
      rs.on("streaming", cb);
      expect(cb).toHaveBeenCalledWith(false);
      rs.destroy();
    });

    it("fires when controller event triggers a change", () => {
      const rs = createStudioReactiveState(ctrl);
      const cb = vi.fn();
      rs.on("streaming", cb);
      cb.mockClear();

      ctrl.isStreaming.mockReturnValue(true);
      ctrl._emit("stateChange");

      expect(cb).toHaveBeenCalledWith(true);
      rs.destroy();
    });

    it("does not fire when value is unchanged (shallow equality)", () => {
      const rs = createStudioReactiveState(ctrl);
      const cb = vi.fn();
      rs.on("streaming", cb);
      cb.mockClear();

      // Emit event but value stays the same
      ctrl._emit("stateChange");
      expect(cb).not.toHaveBeenCalled();
      rs.destroy();
    });

    it("supports multiple subscribers on same property", () => {
      const rs = createStudioReactiveState(ctrl);
      const cb1 = vi.fn();
      const cb2 = vi.fn();
      rs.on("state", cb1);
      rs.on("state", cb2);

      ctrl.getState.mockReturnValue("streaming");
      ctrl._emit("stateChange");

      expect(cb1).toHaveBeenCalledWith("streaming");
      expect(cb2).toHaveBeenCalledWith("streaming");
      rs.destroy();
    });

    it("returns an unsubscribe function", () => {
      const rs = createStudioReactiveState(ctrl);
      const cb = vi.fn();
      const unsub = rs.on("streaming", cb);
      cb.mockClear();

      unsub();
      ctrl.isStreaming.mockReturnValue(true);
      ctrl._emit("stateChange");

      expect(cb).not.toHaveBeenCalled();
      rs.destroy();
    });
  });

  describe("get()", () => {
    it("returns current value from controller", () => {
      const rs = createStudioReactiveState(ctrl);
      expect(rs.get("state")).toBe("idle");
      expect(rs.get("streaming")).toBe(false);
      expect(rs.get("masterVolume")).toBe(1);
      rs.destroy();
    });

    it("reflects updated controller values", () => {
      const rs = createStudioReactiveState(ctrl);
      ctrl.getQualityProfile.mockReturnValue("professional");
      expect(rs.get("profile")).toBe("professional");
      rs.destroy();
    });
  });

  describe("property event mappings", () => {
    it("sources reacts to sourceAdded", () => {
      const rs = createStudioReactiveState(ctrl);
      const cb = vi.fn();
      rs.on("sources", cb);
      cb.mockClear();

      const mockSources = [{ id: "s1" }];
      ctrl.getSources.mockReturnValue(mockSources);
      ctrl._emit("sourceAdded");

      expect(cb).toHaveBeenCalledWith(mockSources);
      rs.destroy();
    });

    it("profile reacts to qualityChanged", () => {
      const rs = createStudioReactiveState(ctrl);
      const cb = vi.fn();
      rs.on("profile", cb);
      cb.mockClear();

      ctrl.getQualityProfile.mockReturnValue("professional");
      ctrl._emit("qualityChanged");

      expect(cb).toHaveBeenCalledWith("professional");
      rs.destroy();
    });

    it("reconnecting reacts to reconnectionAttempt", () => {
      const rs = createStudioReactiveState(ctrl);
      const cb = vi.fn();
      rs.on("reconnecting", cb);
      cb.mockClear();

      ctrl.isReconnecting.mockReturnValue(true);
      ctrl._emit("reconnectionAttempt");

      expect(cb).toHaveBeenCalledWith(true);
      rs.destroy();
    });

    it("webCodecsActive reacts to webCodecsActive event", () => {
      const rs = createStudioReactiveState(ctrl);
      const cb = vi.fn();
      rs.on("webCodecsActive", cb);
      cb.mockClear();

      ctrl.isWebCodecsActive.mockReturnValue(true);
      ctrl._emit("webCodecsActive");

      expect(cb).toHaveBeenCalledWith(true);
      rs.destroy();
    });
  });

  describe("new reactive properties", () => {
    it("recording reacts to recordingStarted/recordingStopped", () => {
      const rs = createStudioReactiveState(ctrl);
      const cb = vi.fn();
      rs.on("recording", cb);
      expect(cb).toHaveBeenCalledWith(false);
      cb.mockClear();

      ctrl.isRecordingActive.mockReturnValue(true);
      ctrl._emit("recordingStarted");
      expect(cb).toHaveBeenCalledWith(true);
      cb.mockClear();

      ctrl.isRecordingActive.mockReturnValue(false);
      ctrl._emit("recordingStopped");
      expect(cb).toHaveBeenCalledWith(false);
      rs.destroy();
    });

    it("codecFamily reacts to webCodecsActive event", () => {
      const rs = createStudioReactiveState(ctrl);
      const cb = vi.fn();
      rs.on("codecFamily", cb);
      expect(cb).toHaveBeenCalledWith("h264");
      cb.mockClear();

      ctrl.getVideoCodecFamily.mockReturnValue("vp9");
      ctrl._emit("webCodecsActive");
      expect(cb).toHaveBeenCalledWith("vp9");
      rs.destroy();
    });

    it("currentBitrate reacts to bitrateChanged", () => {
      const rs = createStudioReactiveState(ctrl);
      const cb = vi.fn();
      rs.on("currentBitrate", cb);
      expect(cb).toHaveBeenCalledWith(null);
      cb.mockClear();

      ctrl.getCurrentBitrate.mockReturnValue(3_600_000);
      ctrl._emit("bitrateChanged");
      expect(cb).toHaveBeenCalledWith(3_600_000);
      rs.destroy();
    });

    it("congestionLevel reacts to congestionChanged", () => {
      const rs = createStudioReactiveState(ctrl);
      const cb = vi.fn();
      rs.on("congestionLevel", cb);
      expect(cb).toHaveBeenCalledWith(null);
      cb.mockClear();

      ctrl.getCongestionLevel.mockReturnValue("mild");
      ctrl._emit("congestionChanged");
      expect(cb).toHaveBeenCalledWith("mild");
      rs.destroy();
    });
  });

  describe("error property", () => {
    it("returns null when no error", () => {
      const rs = createStudioReactiveState(ctrl);
      expect(rs.get("error")).toBeNull();
      rs.destroy();
    });

    it("extracts error from state context", () => {
      ctrl.getStateContext.mockReturnValue({ error: "Connection lost" });
      ctrl.getState.mockReturnValue("reconnecting");
      const rs = createStudioReactiveState(ctrl);
      const err = rs.get("error");
      expect(err).toEqual({ error: "Connection lost", recoverable: true });
      rs.destroy();
    });

    it("marks non-recoverable when state is error", () => {
      ctrl.getStateContext.mockReturnValue({ error: "Fatal" });
      ctrl.getState.mockReturnValue("error");
      const rs = createStudioReactiveState(ctrl);
      const err = rs.get("error");
      expect(err).toEqual({ error: "Fatal", recoverable: false });
      rs.destroy();
    });
  });

  describe("destroy()", () => {
    it("stops all subscriptions", () => {
      const rs = createStudioReactiveState(ctrl);
      const cb = vi.fn();
      rs.on("streaming", cb);
      cb.mockClear();

      rs.destroy();

      ctrl.isStreaming.mockReturnValue(true);
      ctrl._emit("stateChange");
      expect(cb).not.toHaveBeenCalled();
    });
  });

  describe("shallow equality for arrays/objects", () => {
    it("detects array changes by identity", () => {
      const rs = createStudioReactiveState(ctrl);
      const cb = vi.fn();
      const src1 = { id: "a" };
      ctrl.getSources.mockReturnValue([src1]);
      rs.on("sources", cb);
      cb.mockClear();

      // Same array contents but different reference (same element identity)
      ctrl.getSources.mockReturnValue([src1]);
      ctrl._emit("sourceAdded");
      expect(cb).not.toHaveBeenCalled();

      // Different element
      ctrl.getSources.mockReturnValue([{ id: "b" }]);
      ctrl._emit("sourceAdded");
      expect(cb).toHaveBeenCalled();
      rs.destroy();
    });
  });
});
