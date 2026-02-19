import { describe, expect, it, vi, beforeEach, afterEach } from "vitest";

import { ScreenCapture } from "../src/core/ScreenCapture";

function createMockTrack(kind = "video", label = "Screen 1") {
  const listeners: Record<string, Function[]> = {};
  return {
    kind,
    label,
    readyState: "live" as MediaStreamTrackState,
    stop: vi.fn(),
    addEventListener: vi.fn((event: string, handler: Function) => {
      if (!listeners[event]) listeners[event] = [];
      listeners[event].push(handler);
    }),
    removeEventListener: vi.fn(),
    _fireEvent(event: string) {
      listeners[event]?.forEach((fn) => fn());
    },
  };
}

function createMockStream(tracks?: ReturnType<typeof createMockTrack>[]) {
  const videoTrack = tracks?.[0] ?? createMockTrack("video");
  const allTracks = tracks ?? [videoTrack];
  return {
    getTracks: vi.fn(() => allTracks),
    getVideoTracks: vi.fn(() => allTracks.filter((t) => t.kind === "video")),
    getAudioTracks: vi.fn(() => allTracks.filter((t) => t.kind === "audio")),
    _tracks: allTracks,
  };
}

describe("ScreenCapture", () => {
  let origNavigator: any;

  beforeEach(() => {
    origNavigator = (globalThis as any).navigator;
    Object.defineProperty(globalThis, "navigator", {
      value: {
        mediaDevices: {
          getDisplayMedia: vi.fn(),
        },
      },
      writable: true,
      configurable: true,
    });
  });

  afterEach(() => {
    Object.defineProperty(globalThis, "navigator", {
      value: origNavigator,
      writable: true,
      configurable: true,
    });
    vi.restoreAllMocks();
  });

  // ===========================================================================
  // getDisplayMedia constraints
  // ===========================================================================
  describe("getDisplayMedia constraints", () => {
    it("calls getDisplayMedia with default video constraints", async () => {
      const stream = createMockStream();
      navigator.mediaDevices.getDisplayMedia = vi.fn().mockResolvedValue(stream);

      const capture = new ScreenCapture();
      await capture.start();

      expect(navigator.mediaDevices.getDisplayMedia).toHaveBeenCalledWith(
        expect.objectContaining({
          video: expect.objectContaining({
            frameRate: { ideal: 30, max: 60 },
            width: { ideal: 1920 },
            height: { ideal: 1080 },
          }),
          audio: false,
        })
      );
      capture.destroy();
    });

    it("passes audio: true when requested", async () => {
      const stream = createMockStream();
      navigator.mediaDevices.getDisplayMedia = vi.fn().mockResolvedValue(stream);

      const capture = new ScreenCapture();
      await capture.start({ audio: true });

      expect(navigator.mediaDevices.getDisplayMedia).toHaveBeenCalledWith(
        expect.objectContaining({ audio: true })
      );
      capture.destroy();
    });

    it("passes cursor option", async () => {
      const stream = createMockStream();
      navigator.mediaDevices.getDisplayMedia = vi.fn().mockResolvedValue(stream);

      const capture = new ScreenCapture();
      await capture.start({ cursor: "always" as any });

      const constraints = (navigator.mediaDevices.getDisplayMedia as any).mock.calls[0][0];
      expect(constraints.video.cursor).toBe("always");
      capture.destroy();
    });

    it("disables video when video: false", async () => {
      const stream = createMockStream();
      navigator.mediaDevices.getDisplayMedia = vi.fn().mockResolvedValue(stream);

      const capture = new ScreenCapture();
      await capture.start({ video: false });

      const constraints = (navigator.mediaDevices.getDisplayMedia as any).mock.calls[0][0];
      expect(constraints.video).toBe(false);
      capture.destroy();
    });
  });

  // ===========================================================================
  // User cancel (NotAllowedError)
  // ===========================================================================
  describe("user cancel", () => {
    it("returns null on NotAllowedError (user cancel)", async () => {
      const error = new Error("Permission denied");
      error.name = "NotAllowedError";
      navigator.mediaDevices.getDisplayMedia = vi.fn().mockRejectedValue(error);

      const capture = new ScreenCapture();
      const endedHandler = vi.fn();
      capture.on("ended", endedHandler);

      const result = await capture.start();
      expect(result).toBeNull();
      expect(endedHandler).toHaveBeenCalledWith(expect.objectContaining({ reason: "cancelled" }));
      capture.destroy();
    });

    it("returns null on AbortError", async () => {
      const error = new Error("Aborted");
      error.name = "AbortError";
      navigator.mediaDevices.getDisplayMedia = vi.fn().mockRejectedValue(error);

      const capture = new ScreenCapture();
      const result = await capture.start();
      expect(result).toBeNull();
      capture.destroy();
    });
  });

  // ===========================================================================
  // Track ended cleanup
  // ===========================================================================
  describe("track ended cleanup", () => {
    it("emits ended event when all tracks end", async () => {
      const track = createMockTrack();
      const stream = createMockStream([track]);
      navigator.mediaDevices.getDisplayMedia = vi.fn().mockResolvedValue(stream);

      const capture = new ScreenCapture();
      const endedHandler = vi.fn();
      capture.on("ended", endedHandler);

      await capture.start();
      expect(capture.isActive()).toBe(true);

      // Simulate track ending
      track.readyState = "ended" as any;
      track._fireEvent("ended");

      expect(endedHandler).toHaveBeenCalledWith(
        expect.objectContaining({ reason: "user_stopped" })
      );
      expect(capture.isActive()).toBe(false);
    });

    it("registers ended listener on all tracks", async () => {
      const videoTrack = createMockTrack("video");
      const audioTrack = createMockTrack("audio", "System Audio");
      const stream = createMockStream([videoTrack, audioTrack]);
      navigator.mediaDevices.getDisplayMedia = vi.fn().mockResolvedValue(stream);

      const capture = new ScreenCapture();
      await capture.start();

      expect(videoTrack.addEventListener).toHaveBeenCalledWith("ended", expect.any(Function));
      expect(audioTrack.addEventListener).toHaveBeenCalledWith("ended", expect.any(Function));
      capture.destroy();
    });
  });

  // ===========================================================================
  // Multiple captures
  // ===========================================================================
  describe("multiple captures", () => {
    it("supports multiple simultaneous captures", async () => {
      navigator.mediaDevices.getDisplayMedia = vi
        .fn()
        .mockResolvedValueOnce(createMockStream([createMockTrack("video", "Screen 1")]))
        .mockResolvedValueOnce(createMockStream([createMockTrack("video", "Screen 2")]));

      const capture = new ScreenCapture();
      await capture.start();
      await capture.start();

      expect(capture.getCaptureCount()).toBe(2);
      expect(capture.getCaptures()).toHaveLength(2);
      capture.destroy();
    });
  });

  // ===========================================================================
  // Stop
  // ===========================================================================
  describe("stop", () => {
    it("stops all captures and emits ended for each", async () => {
      const track1 = createMockTrack();
      const track2 = createMockTrack();
      navigator.mediaDevices.getDisplayMedia = vi
        .fn()
        .mockResolvedValueOnce(createMockStream([track1]))
        .mockResolvedValueOnce(createMockStream([track2]));

      const capture = new ScreenCapture();
      const endedHandler = vi.fn();
      capture.on("ended", endedHandler);

      await capture.start();
      await capture.start();
      capture.stop();

      expect(track1.stop).toHaveBeenCalled();
      expect(track2.stop).toHaveBeenCalled();
      expect(endedHandler).toHaveBeenCalledTimes(2);
      expect(capture.isActive()).toBe(false);
    });
  });

  // ===========================================================================
  // State queries
  // ===========================================================================
  describe("state queries", () => {
    it("isActive is false initially", () => {
      const capture = new ScreenCapture();
      expect(capture.isActive()).toBe(false);
      capture.destroy();
    });

    it("getCaptureCount reflects active captures", async () => {
      const stream = createMockStream();
      navigator.mediaDevices.getDisplayMedia = vi.fn().mockResolvedValue(stream);

      const capture = new ScreenCapture();
      expect(capture.getCaptureCount()).toBe(0);
      await capture.start();
      expect(capture.getCaptureCount()).toBe(1);
      capture.destroy();
    });

    it("legacy getStream returns first capture stream", async () => {
      const stream = createMockStream();
      navigator.mediaDevices.getDisplayMedia = vi.fn().mockResolvedValue(stream);

      const capture = new ScreenCapture();
      expect(capture.getStream()).toBeNull();
      await capture.start();
      expect(capture.getStream()).toBe(stream);
      capture.destroy();
    });
  });

  // ===========================================================================
  // Destroy
  // ===========================================================================
  describe("destroy", () => {
    it("stops all captures and removes listeners", async () => {
      const track = createMockTrack();
      const stream = createMockStream([track]);
      navigator.mediaDevices.getDisplayMedia = vi.fn().mockResolvedValue(stream);

      const capture = new ScreenCapture();
      await capture.start();
      capture.destroy();

      expect(track.stop).toHaveBeenCalled();
      expect(capture.isActive()).toBe(false);
    });
  });
});
