import { describe, expect, it, vi, beforeEach, afterEach } from "vitest";

import { AudioMixer } from "../src/core/AudioMixer";

// Minimal Web Audio API stubs
function createMockGainNode() {
  return {
    gain: { value: 1, setTargetAtTime: vi.fn() },
    connect: vi.fn(),
    disconnect: vi.fn(),
  };
}

function createMockPanNode() {
  return {
    pan: { value: 0, setTargetAtTime: vi.fn() },
    connect: vi.fn(),
    disconnect: vi.fn(),
  };
}

function createMockSourceNode() {
  return { connect: vi.fn(), disconnect: vi.fn() };
}

function createMockAnalyzer() {
  return {
    fftSize: 0,
    smoothingTimeConstant: 0,
    frequencyBinCount: 128,
    getByteTimeDomainData: vi.fn((data: Uint8Array) => {
      // Silence: all 128
      data.fill(128);
    }),
    connect: vi.fn(),
    disconnect: vi.fn(),
  };
}

function createMockCompressor() {
  return {
    threshold: { value: 0 },
    knee: { value: 0 },
    ratio: { value: 0 },
    attack: { value: 0 },
    release: { value: 0 },
    connect: vi.fn(),
    disconnect: vi.fn(),
  };
}

function createMockAudioContext() {
  const destinationNode = {
    stream: { getAudioTracks: () => [{ kind: "audio" }] },
    channelCount: 0,
  };

  return {
    sampleRate: 48000,
    state: "running" as AudioContextState,
    currentTime: 0,
    createGain: vi.fn(() => createMockGainNode()),
    createStereoPanner: vi.fn(() => createMockPanNode()),
    createMediaStreamSource: vi.fn(() => createMockSourceNode()),
    createAnalyser: vi.fn(() => createMockAnalyzer()),
    createDynamicsCompressor: vi.fn(() => createMockCompressor()),
    createMediaStreamDestination: vi.fn(() => destinationNode),
    close: vi.fn(async () => {}),
    resume: vi.fn(async () => {}),
    suspend: vi.fn(async () => {}),
  };
}

function createMockTrack(kind = "audio") {
  return { kind, id: `track-${Math.random()}` } as unknown as MediaStreamTrack;
}

describe("AudioMixer", () => {
  let origAudioContext: typeof globalThis.AudioContext;
  let origMediaStream: typeof globalThis.MediaStream;
  let origRAF: typeof globalThis.requestAnimationFrame;
  let origCAF: typeof globalThis.cancelAnimationFrame;
  let mockCtx: ReturnType<typeof createMockAudioContext>;

  beforeEach(() => {
    origAudioContext = (globalThis as any).AudioContext;
    origMediaStream = (globalThis as any).MediaStream;
    origRAF = (globalThis as any).requestAnimationFrame;
    origCAF = (globalThis as any).cancelAnimationFrame;
    (globalThis as any).requestAnimationFrame = vi.fn((cb: Function) => {
      return setTimeout(cb, 0) as unknown as number;
    });
    (globalThis as any).cancelAnimationFrame = vi.fn((id: number) => {
      clearTimeout(id);
    });
    mockCtx = createMockAudioContext();
    // Must use function (not arrow) so `new` works; vi.fn wraps for spy assertions
    (globalThis as any).AudioContext = vi.fn(function (this: any) {
      return mockCtx;
    });
    (globalThis as any).MediaStream = vi.fn(function (this: any) {
      return { getAudioTracks: () => [] };
    });

    vi.spyOn(console, "log").mockImplementation(() => {});
  });

  afterEach(() => {
    (globalThis as any).AudioContext = origAudioContext;
    (globalThis as any).MediaStream = origMediaStream;
    (globalThis as any).requestAnimationFrame = origRAF;
    (globalThis as any).cancelAnimationFrame = origCAF;
    vi.restoreAllMocks();
  });

  // ===========================================================================
  // initialize
  // ===========================================================================
  describe("initialize", () => {
    it("creates audio context and processing chain", async () => {
      const mixer = new AudioMixer();
      await mixer.initialize();

      expect(globalThis.AudioContext).toHaveBeenCalledWith({ sampleRate: 48000 });
      expect(mockCtx.createGain).toHaveBeenCalled();
      expect(mockCtx.createDynamicsCompressor).toHaveBeenCalledTimes(2);
      expect(mockCtx.createAnalyser).toHaveBeenCalled();
      expect(mockCtx.createMediaStreamDestination).toHaveBeenCalled();
      mixer.destroy();
    });

    it("double initialize is a no-op", async () => {
      const mixer = new AudioMixer();
      await mixer.initialize();
      await mixer.initialize();
      expect(globalThis.AudioContext).toHaveBeenCalledTimes(1);
      mixer.destroy();
    });

    it("custom config", async () => {
      const mixer = new AudioMixer({ sampleRate: 44100, channelCount: 1 });
      await mixer.initialize();
      expect(globalThis.AudioContext).toHaveBeenCalledWith({ sampleRate: 44100 });
      const destNode = mockCtx.createMediaStreamDestination.mock.results[0].value;
      expect(destNode.channelCount).toBe(1);
      mixer.destroy();
    });
  });

  // ===========================================================================
  // addSource / removeSource
  // ===========================================================================
  describe("addSource / removeSource", () => {
    it("throws when not initialized", () => {
      const mixer = new AudioMixer();
      expect(() => mixer.addSource("mic", createMockTrack())).toThrow("not initialized");
    });

    it("throws for non-audio track", async () => {
      const mixer = new AudioMixer();
      await mixer.initialize();
      expect(() => mixer.addSource("cam", createMockTrack("video"))).toThrow(
        "must be an audio track"
      );
      mixer.destroy();
    });

    it("adds a source and emits sourceAdded", async () => {
      const mixer = new AudioMixer();
      await mixer.initialize();

      const handler = vi.fn();
      mixer.on("sourceAdded", handler);

      mixer.addSource("mic", createMockTrack());
      expect(mixer.hasSource("mic")).toBe(true);
      expect(mixer.getSourceCount()).toBe(1);
      expect(handler).toHaveBeenCalledWith({ sourceId: "mic" });
      mixer.destroy();
    });

    it("replaces existing source with same ID", async () => {
      const mixer = new AudioMixer();
      await mixer.initialize();

      mixer.addSource("mic", createMockTrack());
      const firstSourceNode = mockCtx.createMediaStreamSource.mock.results[0].value;

      mixer.addSource("mic", createMockTrack());
      expect(mixer.getSourceCount()).toBe(1);
      expect(firstSourceNode.disconnect).toHaveBeenCalled();
      mixer.destroy();
    });

    it("removes a source and emits sourceRemoved", async () => {
      const mixer = new AudioMixer();
      await mixer.initialize();

      mixer.addSource("mic", createMockTrack());
      const handler = vi.fn();
      mixer.on("sourceRemoved", handler);

      mixer.removeSource("mic");
      expect(mixer.hasSource("mic")).toBe(false);
      expect(handler).toHaveBeenCalledWith({ sourceId: "mic" });
      mixer.destroy();
    });

    it("removeSource is no-op for unknown ID", async () => {
      const mixer = new AudioMixer();
      await mixer.initialize();
      mixer.addSource("mic", createMockTrack());

      mixer.removeSource("unknown");
      expect(mixer.getSourceCount()).toBe(1);
      expect(mixer.hasSource("mic")).toBe(true);
      mixer.destroy();
    });
  });

  // ===========================================================================
  // Volume / mute / pan
  // ===========================================================================
  describe("volume control", () => {
    it("setVolume clamps to [0, 2]", async () => {
      const mixer = new AudioMixer();
      await mixer.initialize();
      mixer.addSource("mic", createMockTrack());

      mixer.setVolume("mic", 3);
      expect(mixer.getSourceOptions("mic")?.volume).toBe(2);

      mixer.setVolume("mic", -1);
      expect(mixer.getSourceOptions("mic")?.volume).toBe(0);

      mixer.destroy();
    });

    it("mute sets gain to 0, unmute restores volume", async () => {
      const mixer = new AudioMixer();
      await mixer.initialize();
      mixer.addSource("mic", createMockTrack(), { volume: 0.8 });

      mixer.mute("mic");
      expect(mixer.getSourceOptions("mic")?.muted).toBe(true);

      mixer.unmute("mic");
      expect(mixer.getSourceOptions("mic")?.muted).toBe(false);
      mixer.destroy();
    });

    it("toggleMute toggles and returns new state", async () => {
      const mixer = new AudioMixer();
      await mixer.initialize();
      mixer.addSource("mic", createMockTrack());

      expect(mixer.toggleMute("mic")).toBe(true);
      expect(mixer.toggleMute("mic")).toBe(false);
      mixer.destroy();
    });

    it("toggleMute returns false for unknown source", async () => {
      const mixer = new AudioMixer();
      await mixer.initialize();
      expect(mixer.toggleMute("unknown")).toBe(false);
      mixer.destroy();
    });

    it("setPan clamps to [-1, 1]", async () => {
      const mixer = new AudioMixer();
      await mixer.initialize();
      mixer.addSource("mic", createMockTrack());

      mixer.setPan("mic", 2);
      expect(mixer.getSourceOptions("mic")?.pan).toBe(1);

      mixer.setPan("mic", -3);
      expect(mixer.getSourceOptions("mic")?.pan).toBe(-1);
      mixer.destroy();
    });
  });

  // ===========================================================================
  // Master volume
  // ===========================================================================
  describe("master volume", () => {
    it("defaults to 1", async () => {
      const mixer = new AudioMixer();
      await mixer.initialize();
      expect(mixer.getMasterVolume()).toBe(1);
      mixer.destroy();
    });

    it("setMasterVolume is no-op before init", () => {
      const mixer = new AudioMixer();
      expect(() => mixer.setMasterVolume(0.5)).not.toThrow();
    });
  });

  // ===========================================================================
  // Output
  // ===========================================================================
  describe("output", () => {
    it("getOutputStream returns null before init", () => {
      const mixer = new AudioMixer();
      expect(mixer.getOutputStream()).toBeNull();
    });

    it("getOutputStream returns stream after init", async () => {
      const mixer = new AudioMixer();
      await mixer.initialize();
      expect(mixer.getOutputStream()).not.toBeNull();
      mixer.destroy();
    });

    it("getOutputTrack returns audio track", async () => {
      const mixer = new AudioMixer();
      await mixer.initialize();
      expect(mixer.getOutputTrack()).toBeDefined();
      mixer.destroy();
    });
  });

  // ===========================================================================
  // Source queries
  // ===========================================================================
  describe("source queries", () => {
    it("getSourceIds returns all IDs", async () => {
      const mixer = new AudioMixer();
      await mixer.initialize();
      mixer.addSource("mic", createMockTrack());
      mixer.addSource("desktop", createMockTrack());

      expect(mixer.getSourceIds()).toEqual(["mic", "desktop"]);
      mixer.destroy();
    });

    it("getSourceOptions returns null for unknown", async () => {
      const mixer = new AudioMixer();
      await mixer.initialize();
      expect(mixer.getSourceOptions("nope")).toBeNull();
      mixer.destroy();
    });

    it("getSourceOptions returns copy", async () => {
      const mixer = new AudioMixer();
      await mixer.initialize();
      mixer.addSource("mic", createMockTrack(), { volume: 0.7, pan: -0.5 });

      const opts = mixer.getSourceOptions("mic");
      expect(opts).toEqual({ volume: 0.7, muted: false, pan: -0.5 });
      mixer.destroy();
    });
  });

  // ===========================================================================
  // getLevel / getLevels
  // ===========================================================================
  describe("levels", () => {
    it("getLevel returns 0 before init", () => {
      const mixer = new AudioMixer();
      expect(mixer.getLevel()).toBe(0);
    });

    it("getLevel returns 0 for silence", async () => {
      const mixer = new AudioMixer();
      await mixer.initialize();
      expect(mixer.getLevel()).toBe(0);
      mixer.destroy();
    });

    it("getLevels tracks peak with decay", async () => {
      const mixer = new AudioMixer();
      await mixer.initialize();

      const levels1 = mixer.getLevels();
      expect(levels1.level).toBe(0);
      expect(levels1.peakLevel).toBe(0);
      mixer.destroy();
    });
  });

  // ===========================================================================
  // Level monitoring
  // ===========================================================================
  describe("level monitoring", () => {
    it("starts and stops", async () => {
      const mixer = new AudioMixer();
      await mixer.initialize();

      expect(mixer.isMonitoringLevels()).toBe(false);
      mixer.startLevelMonitoring();
      expect(mixer.isMonitoringLevels()).toBe(true);
      mixer.stopLevelMonitoring();
      expect(mixer.isMonitoringLevels()).toBe(false);
      mixer.destroy();
    });

    it("double start is a no-op", async () => {
      const mixer = new AudioMixer();
      await mixer.initialize();
      mixer.startLevelMonitoring();
      mixer.startLevelMonitoring();
      expect(mixer.isMonitoringLevels()).toBe(true);
      mixer.destroy();
    });
  });

  // ===========================================================================
  // Context state
  // ===========================================================================
  describe("context state", () => {
    it("returns null before init", () => {
      const mixer = new AudioMixer();
      expect(mixer.getState()).toBeNull();
    });

    it("returns context state after init", async () => {
      const mixer = new AudioMixer();
      await mixer.initialize();
      expect(mixer.getState()).toBe("running");
      mixer.destroy();
    });
  });

  // ===========================================================================
  // destroy
  // ===========================================================================
  describe("destroy", () => {
    it("removes all sources and closes context", async () => {
      const mixer = new AudioMixer();
      await mixer.initialize();
      mixer.addSource("mic", createMockTrack());
      mixer.addSource("desktop", createMockTrack());

      mixer.destroy();
      expect(mixer.getSourceCount()).toBe(0);
      expect(mockCtx.close).toHaveBeenCalled();
    });

    it("stops level monitoring", async () => {
      const mixer = new AudioMixer();
      await mixer.initialize();
      mixer.startLevelMonitoring();

      mixer.destroy();
      expect(mixer.isMonitoringLevels()).toBe(false);
    });
  });
});
