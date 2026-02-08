import { describe, expect, it, vi, beforeEach, afterEach } from "vitest";

import { DeviceManager } from "../src/core/DeviceManager";

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

function makeDevice(overrides: Partial<MediaDeviceInfo> = {}): MediaDeviceInfo {
  return {
    deviceId: `dev-${Math.random().toString(36).slice(2, 10)}`,
    groupId: "group-1",
    kind: "videoinput",
    label: "",
    toJSON: () => ({}),
    ...overrides,
  } as MediaDeviceInfo;
}

function makeTrack(kind = "video"): MediaStreamTrack {
  return {
    kind,
    id: `track-${Math.random().toString(36).slice(2, 10)}`,
    stop: vi.fn(),
    enabled: true,
  } as unknown as MediaStreamTrack;
}

function makeStream(videoTracks: MediaStreamTrack[] = [], audioTracks: MediaStreamTrack[] = []) {
  const allTracks = [...videoTracks, ...audioTracks];
  return {
    getTracks: () => [...allTracks],
    getVideoTracks: () => [...videoTracks],
    getAudioTracks: () => [...audioTracks],
    addTrack: vi.fn((t: MediaStreamTrack) => allTracks.push(t)),
    removeTrack: vi.fn((t: MediaStreamTrack) => {
      const idx = allTracks.indexOf(t);
      if (idx >= 0) allTracks.splice(idx, 1);
      const vidIdx = videoTracks.indexOf(t);
      if (vidIdx >= 0) videoTracks.splice(vidIdx, 1);
      const audIdx = audioTracks.indexOf(t);
      if (audIdx >= 0) audioTracks.splice(audIdx, 1);
    }),
  } as unknown as MediaStream;
}

// ---------------------------------------------------------------------------
// Test suite
// ---------------------------------------------------------------------------

describe("DeviceManager", () => {
  let origNavigator: PropertyDescriptor | undefined;
  let deviceChangeListeners: Map<string, Function>;
  let mockEnumerateDevices: ReturnType<typeof vi.fn>;
  let mockGetUserMedia: ReturnType<typeof vi.fn>;

  function setNavigator(value: unknown) {
    Object.defineProperty(globalThis, "navigator", {
      value,
      writable: true,
      configurable: true,
    });
  }

  beforeEach(() => {
    deviceChangeListeners = new Map();
    mockEnumerateDevices = vi.fn(async () => []);
    mockGetUserMedia = vi.fn(async () => makeStream());

    origNavigator = Object.getOwnPropertyDescriptor(globalThis, "navigator");
    setNavigator({
      mediaDevices: {
        enumerateDevices: mockEnumerateDevices,
        getUserMedia: mockGetUserMedia,
        addEventListener: vi.fn((event: string, handler: Function) => {
          deviceChangeListeners.set(event, handler);
        }),
        removeEventListener: vi.fn(),
      },
    });
  });

  afterEach(() => {
    if (origNavigator) {
      Object.defineProperty(globalThis, "navigator", origNavigator);
    }
    vi.restoreAllMocks();
  });

  // ===========================================================================
  // enumerateDevices
  // ===========================================================================
  describe("enumerateDevices", () => {
    it("returns filtered and mapped devices", async () => {
      mockEnumerateDevices.mockResolvedValue([
        makeDevice({ kind: "videoinput", deviceId: "cam1", label: "Webcam" }),
        makeDevice({ kind: "audioinput", deviceId: "mic1", label: "Mic" }),
        makeDevice({ kind: "audiooutput", deviceId: "spk1", label: "Speaker" }),
      ]);

      const mgr = new DeviceManager();
      const devices = await mgr.enumerateDevices();

      expect(devices).toHaveLength(3);
      expect(devices[0]).toEqual({
        deviceId: "cam1",
        kind: "videoinput",
        label: "Webcam",
        groupId: "group-1",
      });
    });

    it("generates label fallback when label is empty", async () => {
      mockEnumerateDevices.mockResolvedValue([
        makeDevice({ kind: "audioinput", deviceId: "abcdef1234567890", label: "" }),
      ]);

      const mgr = new DeviceManager();
      const devices = await mgr.enumerateDevices();

      expect(devices[0].label).toBe("audioinput (abcdef12...)");
    });

    it("throws when enumerateDevices not supported", async () => {
      setNavigator({ mediaDevices: { addEventListener: vi.fn() } });
      const mgr = new DeviceManager();
      await expect(mgr.enumerateDevices()).rejects.toThrow("enumerateDevices not supported");
    });

    it("throws when navigator.mediaDevices is undefined", async () => {
      setNavigator({});
      const mgr = new DeviceManager();
      await expect(mgr.enumerateDevices()).rejects.toThrow();
    });

    it("filters out non-input/output kinds", async () => {
      mockEnumerateDevices.mockResolvedValue([
        makeDevice({ kind: "videoinput" }),
        { deviceId: "x", kind: "other", label: "", groupId: "", toJSON: () => ({}) },
      ]);

      const mgr = new DeviceManager();
      const devices = await mgr.enumerateDevices();
      expect(devices).toHaveLength(1);
    });
  });

  // ===========================================================================
  // getVideoInputs / getAudioInputs / getAudioOutputs
  // ===========================================================================
  describe("device filtering helpers", () => {
    it("getVideoInputs returns only video devices", async () => {
      mockEnumerateDevices.mockResolvedValue([
        makeDevice({ kind: "videoinput", label: "Cam" }),
        makeDevice({ kind: "audioinput", label: "Mic" }),
      ]);

      const mgr = new DeviceManager();
      const cams = await mgr.getVideoInputs();
      expect(cams).toHaveLength(1);
      expect(cams[0].kind).toBe("videoinput");
    });

    it("getAudioInputs returns only audio input devices", async () => {
      mockEnumerateDevices.mockResolvedValue([
        makeDevice({ kind: "videoinput", label: "Cam" }),
        makeDevice({ kind: "audioinput", label: "Mic" }),
        makeDevice({ kind: "audiooutput", label: "Speaker" }),
      ]);

      const mgr = new DeviceManager();
      const mics = await mgr.getAudioInputs();
      expect(mics).toHaveLength(1);
      expect(mics[0].kind).toBe("audioinput");
    });

    it("getAudioOutputs returns only audio output devices", async () => {
      mockEnumerateDevices.mockResolvedValue([
        makeDevice({ kind: "audioinput", label: "Mic" }),
        makeDevice({ kind: "audiooutput", label: "Speaker" }),
      ]);

      const mgr = new DeviceManager();
      const speakers = await mgr.getAudioOutputs();
      expect(speakers).toHaveLength(1);
      expect(speakers[0].kind).toBe("audiooutput");
    });
  });

  // ===========================================================================
  // requestPermissions
  // ===========================================================================
  describe("requestPermissions", () => {
    it("requests permissions and stops temp stream", async () => {
      const stopFn = vi.fn();
      const tempTrack = { stop: stopFn } as unknown as MediaStreamTrack;
      mockGetUserMedia.mockResolvedValue({
        getTracks: () => [tempTrack],
        getVideoTracks: () => [],
        getAudioTracks: () => [],
      });
      mockEnumerateDevices.mockResolvedValue([]);

      const mgr = new DeviceManager();
      const result = await mgr.requestPermissions({ video: true, audio: true });

      expect(mockGetUserMedia).toHaveBeenCalledWith({ video: true, audio: true });
      expect(stopFn).toHaveBeenCalled();
      expect(result).toEqual({ video: true, audio: true });
      expect(mgr.hasPermission("video")).toBe(true);
      expect(mgr.hasPermission("audio")).toBe(true);
    });

    it("emits permissionChanged on success", async () => {
      mockGetUserMedia.mockResolvedValue({
        getTracks: () => [],
        getVideoTracks: () => [],
        getAudioTracks: () => [],
      });
      mockEnumerateDevices.mockResolvedValue([]);

      const mgr = new DeviceManager();
      const handler = vi.fn();
      mgr.on("permissionChanged", handler);

      await mgr.requestPermissions();
      expect(handler).toHaveBeenCalledWith({ granted: true, denied: false });
    });

    it("emits permissionChanged denied on NotAllowedError", async () => {
      const err = new Error("Not allowed");
      err.name = "NotAllowedError";
      mockGetUserMedia.mockRejectedValue(err);

      const mgr = new DeviceManager();
      const permHandler = vi.fn();
      const errHandler = vi.fn();
      mgr.on("permissionChanged", permHandler);
      mgr.on("error", errHandler);

      await expect(mgr.requestPermissions()).rejects.toThrow("Not allowed");
      expect(permHandler).toHaveBeenCalledWith({ granted: false, denied: true });
      expect(errHandler).toHaveBeenCalledWith(
        expect.objectContaining({ message: expect.stringContaining("Permission request failed") })
      );
    });

    it("emits permissionChanged denied on PermissionDeniedError", async () => {
      const err = new Error("Denied");
      err.name = "PermissionDeniedError";
      mockGetUserMedia.mockRejectedValue(err);

      const mgr = new DeviceManager();
      const permHandler = vi.fn();
      mgr.on("permissionChanged", permHandler);

      await expect(mgr.requestPermissions()).rejects.toThrow();
      expect(permHandler).toHaveBeenCalledWith({ granted: false, denied: true });
    });

    it("does not emit denied for other errors", async () => {
      mockGetUserMedia.mockRejectedValue(new Error("hardware failure"));

      const mgr = new DeviceManager();
      const permHandler = vi.fn();
      mgr.on("permissionChanged", permHandler);

      await expect(mgr.requestPermissions()).rejects.toThrow("hardware failure");
      expect(permHandler).not.toHaveBeenCalled();
    });

    it("handles non-Error rejection", async () => {
      mockGetUserMedia.mockRejectedValue("string error");

      const mgr = new DeviceManager();
      const errHandler = vi.fn();
      mgr.on("error", errHandler);

      await expect(mgr.requestPermissions()).rejects.toBe("string error");
      expect(errHandler).toHaveBeenCalledWith(
        expect.objectContaining({ message: expect.stringContaining("string error") })
      );
    });

    it("only updates requested permission types", async () => {
      mockGetUserMedia.mockResolvedValue({
        getTracks: () => [],
        getVideoTracks: () => [],
        getAudioTracks: () => [],
      });
      mockEnumerateDevices.mockResolvedValue([]);

      const mgr = new DeviceManager();
      await mgr.requestPermissions({ video: true, audio: false });

      expect(mgr.hasPermission("video")).toBe(true);
      expect(mgr.hasPermission("audio")).toBe(false);
    });
  });

  // ===========================================================================
  // getUserMedia
  // ===========================================================================
  describe("getUserMedia", () => {
    it("returns stream with default profile", async () => {
      const stream = makeStream([makeTrack("video")], [makeTrack("audio")]);
      mockGetUserMedia.mockResolvedValue(stream);

      const mgr = new DeviceManager();
      const result = await mgr.getUserMedia();

      expect(result).toBe(stream);
      expect(mockGetUserMedia).toHaveBeenCalled();
      expect(mgr.getStream()).toBe(stream);
    });

    it("updates permission status from returned tracks", async () => {
      const stream = makeStream([makeTrack("video")], [makeTrack("audio")]);
      mockGetUserMedia.mockResolvedValue(stream);

      const mgr = new DeviceManager();
      await mgr.getUserMedia();

      expect(mgr.hasPermission("video")).toBe(true);
      expect(mgr.hasPermission("audio")).toBe(true);
    });

    it("uses custom constraints when provided", async () => {
      const stream = makeStream();
      mockGetUserMedia.mockResolvedValue(stream);

      const custom = { video: { width: 320 }, audio: false };
      const mgr = new DeviceManager();
      await mgr.getUserMedia({ customConstraints: custom });

      expect(mockGetUserMedia).toHaveBeenCalledWith(custom);
    });

    it("falls back on OverconstrainedError", async () => {
      const overErr = new Error("Overconstrained");
      overErr.name = "OverconstrainedError";
      const fallbackStream = makeStream([makeTrack("video")]);

      mockGetUserMedia.mockRejectedValueOnce(overErr).mockResolvedValueOnce(fallbackStream);

      const mgr = new DeviceManager();
      const result = await mgr.getUserMedia({ videoDeviceId: "specific-cam" });

      expect(result).toBe(fallbackStream);
      expect(mockGetUserMedia).toHaveBeenCalledTimes(2);
      // Second call should use relaxed constraints (true/true instead of specific device)
      const secondCall = mockGetUserMedia.mock.calls[1][0];
      expect(secondCall.video).toBe(true);
    });

    it("emits error and re-throws on non-overconstrained failure", async () => {
      mockGetUserMedia.mockRejectedValue(new Error("hardware error"));

      const mgr = new DeviceManager();
      const errHandler = vi.fn();
      mgr.on("error", errHandler);

      await expect(mgr.getUserMedia()).rejects.toThrow("hardware error");
      expect(errHandler).toHaveBeenCalledWith(
        expect.objectContaining({ message: expect.stringContaining("getUserMedia failed") })
      );
    });
  });

  // ===========================================================================
  // stopAllTracks
  // ===========================================================================
  describe("stopAllTracks", () => {
    it("stops all tracks and nullifies stream", async () => {
      const vt = makeTrack("video");
      const at = makeTrack("audio");
      const stream = makeStream([vt], [at]);
      mockGetUserMedia.mockResolvedValue(stream);

      const mgr = new DeviceManager();
      await mgr.getUserMedia();
      expect(mgr.getStream()).not.toBeNull();

      mgr.stopAllTracks();
      expect(vt.stop).toHaveBeenCalled();
      expect(at.stop).toHaveBeenCalled();
      expect(mgr.getStream()).toBeNull();
    });

    it("is a no-op when no stream", () => {
      const mgr = new DeviceManager();
      expect(() => mgr.stopAllTracks()).not.toThrow();
    });
  });

  // ===========================================================================
  // replaceVideoTrack / replaceAudioTrack
  // ===========================================================================
  describe("replaceVideoTrack", () => {
    it("throws when no active stream", async () => {
      const mgr = new DeviceManager();
      await expect(mgr.replaceVideoTrack("dev-1")).rejects.toThrow(
        "No active stream to replace track in"
      );
    });

    it("stops old video track and adds new one", async () => {
      const oldTrack = makeTrack("video");
      const newTrack = makeTrack("video");
      const stream = makeStream([oldTrack], [makeTrack("audio")]);
      mockGetUserMedia
        .mockResolvedValueOnce(stream) // initial getUserMedia
        .mockResolvedValueOnce(makeStream([newTrack])); // replacement getUserMedia

      const mgr = new DeviceManager();
      await mgr.getUserMedia();

      const result = await mgr.replaceVideoTrack("new-cam-id");
      expect(oldTrack.stop).toHaveBeenCalled();
      expect(stream.removeTrack).toHaveBeenCalledWith(oldTrack);
      expect(stream.addTrack).toHaveBeenCalledWith(newTrack);
      expect(result).toBe(newTrack);
    });

    it("returns null when new stream has no video track", async () => {
      const oldTrack = makeTrack("video");
      const stream = makeStream([oldTrack]);
      mockGetUserMedia.mockResolvedValueOnce(stream).mockResolvedValueOnce(makeStream([]));

      const mgr = new DeviceManager();
      await mgr.getUserMedia();

      const result = await mgr.replaceVideoTrack("no-track-device");
      expect(result).toBeNull();
      expect(mgr.getStream()).toBe(stream);
    });
  });

  describe("replaceAudioTrack", () => {
    it("throws when no active stream", async () => {
      const mgr = new DeviceManager();
      await expect(mgr.replaceAudioTrack("dev-1")).rejects.toThrow(
        "No active stream to replace track in"
      );
    });

    it("stops old audio track and adds new one", async () => {
      const oldTrack = makeTrack("audio");
      const newTrack = makeTrack("audio");
      const stream = makeStream([makeTrack("video")], [oldTrack]);
      mockGetUserMedia
        .mockResolvedValueOnce(stream)
        .mockResolvedValueOnce(makeStream([], [newTrack]));

      const mgr = new DeviceManager();
      await mgr.getUserMedia();

      const result = await mgr.replaceAudioTrack("new-mic-id");
      expect(oldTrack.stop).toHaveBeenCalled();
      expect(stream.removeTrack).toHaveBeenCalledWith(oldTrack);
      expect(stream.addTrack).toHaveBeenCalledWith(newTrack);
      expect(result).toBe(newTrack);
    });

    it("returns null when new stream has no audio track", async () => {
      const stream = makeStream([], [makeTrack("audio")]);
      mockGetUserMedia.mockResolvedValueOnce(stream).mockResolvedValueOnce(makeStream([], []));

      const mgr = new DeviceManager();
      await mgr.getUserMedia();

      const result = await mgr.replaceAudioTrack("no-track-device");
      expect(result).toBeNull();
      expect(mgr.getStream()).toBe(stream);
    });
  });

  // ===========================================================================
  // Device change events
  // ===========================================================================
  describe("device change events", () => {
    it("re-enumerates and emits on devicechange", async () => {
      mockEnumerateDevices.mockResolvedValue([
        makeDevice({ kind: "videoinput", label: "New Cam" }),
      ]);

      const mgr = new DeviceManager();
      const handler = vi.fn();
      mgr.on("devicesChanged", handler);

      const listener = deviceChangeListeners.get("devicechange");
      expect(listener).toBeDefined();

      await listener!();
      expect(mockEnumerateDevices).toHaveBeenCalled();
      expect(handler).toHaveBeenCalledWith({
        devices: expect.arrayContaining([expect.objectContaining({ label: "New Cam" })]),
      });
    });
  });

  // ===========================================================================
  // getAllDevices
  // ===========================================================================
  describe("getAllDevices", () => {
    it("returns a copy of cached devices", async () => {
      mockEnumerateDevices.mockResolvedValue([makeDevice({ kind: "videoinput", label: "Cam" })]);

      const mgr = new DeviceManager();
      await mgr.enumerateDevices();

      const d1 = mgr.getAllDevices();
      const d2 = mgr.getAllDevices();
      expect(d1).toEqual(d2);
      expect(d1).not.toBe(d2);
    });

    it("returns empty array before enumeration", () => {
      const mgr = new DeviceManager();
      expect(mgr.getAllDevices()).toEqual([]);
    });
  });

  // ===========================================================================
  // destroy
  // ===========================================================================
  describe("destroy", () => {
    it("stops tracks and removes listeners", async () => {
      const vt = makeTrack("video");
      const stream = makeStream([vt]);
      mockGetUserMedia.mockResolvedValue(stream);

      const mgr = new DeviceManager();
      await mgr.getUserMedia();
      const handler = vi.fn();
      mgr.on("error", handler);

      mgr.destroy();
      expect(vt.stop).toHaveBeenCalled();
      expect(mgr.getStream()).toBeNull();
    });
  });
});
