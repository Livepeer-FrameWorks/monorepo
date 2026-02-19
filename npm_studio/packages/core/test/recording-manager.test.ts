import { describe, expect, it, vi, beforeEach, afterEach } from "vitest";

import { RecordingManager, type RecordingManagerOptions } from "../src/recording/RecordingManager";

// Mock EncoderManager with typed event emitter pattern
function createMockEncoder() {
  const listeners: Record<string, Set<Function>> = {};
  return {
    on: vi.fn((event: string, handler: Function) => {
      if (!listeners[event]) listeners[event] = new Set();
      listeners[event].add(handler);
    }),
    off: vi.fn((event: string, handler: Function) => {
      listeners[event]?.delete(handler);
    }),
    // Helper to simulate encoder emitting chunks
    _emit(event: string, data: any) {
      listeners[event]?.forEach((fn) => fn(data));
    },
    _getListenerCount(event: string) {
      return listeners[event]?.size ?? 0;
    },
  };
}

function makeVideoChunkData(timestampUs: number, type: "key" | "delta" = "delta") {
  return {
    data: new ArrayBuffer(100),
    timestamp: timestampUs,
    type,
  };
}

function makeAudioChunkData(timestampUs: number) {
  return {
    data: new ArrayBuffer(50),
    timestamp: timestampUs,
  };
}

describe("RecordingManager", () => {
  const defaultOpts: RecordingManagerOptions = {
    muxerOptions: {
      video: { width: 1920, height: 1080 },
      audio: { sampleRate: 48000, channels: 2, bitDepth: 32 },
    },
    progressInterval: 100,
  };

  beforeEach(() => {
    vi.useFakeTimers();
  });

  afterEach(() => {
    vi.useRealTimers();
    vi.restoreAllMocks();
  });

  // ===========================================================================
  // State machine: idle → recording → paused
  // ===========================================================================
  describe("state machine", () => {
    it("starts in idle state", () => {
      const rm = new RecordingManager(defaultOpts);
      expect(rm.getState()).toBe("idle");
      expect(rm.isRecording).toBe(false);
      expect(rm.isPaused).toBe(false);
    });

    it("transitions to recording on start", () => {
      const rm = new RecordingManager(defaultOpts);
      const encoder = createMockEncoder();
      rm.start(encoder as any);
      expect(rm.getState()).toBe("recording");
      expect(rm.isRecording).toBe(true);
      expect(rm.isPaused).toBe(false);
    });

    it("transitions to paused on pause", () => {
      const rm = new RecordingManager(defaultOpts);
      const encoder = createMockEncoder();
      rm.start(encoder as any);
      rm.pause();
      expect(rm.getState()).toBe("paused");
      expect(rm.isRecording).toBe(false);
      expect(rm.isPaused).toBe(true);
    });

    it("transitions back to recording on resume", () => {
      const rm = new RecordingManager(defaultOpts);
      const encoder = createMockEncoder();
      rm.start(encoder as any);
      rm.pause();
      rm.resume();
      expect(rm.getState()).toBe("recording");
      expect(rm.isRecording).toBe(true);
    });

    it("transitions to idle on stop", () => {
      const rm = new RecordingManager(defaultOpts);
      const encoder = createMockEncoder();
      rm.start(encoder as any);
      rm.stop();
      expect(rm.getState()).toBe("idle");
    });

    it("transitions to idle on stop from paused state", () => {
      const rm = new RecordingManager(defaultOpts);
      const encoder = createMockEncoder();
      rm.start(encoder as any);
      rm.pause();
      rm.stop();
      expect(rm.getState()).toBe("idle");
    });

    it("emits error when starting while already recording", () => {
      const rm = new RecordingManager(defaultOpts);
      const encoder = createMockEncoder();
      const errorHandler = vi.fn();
      rm.on("error", errorHandler);

      rm.start(encoder as any);
      rm.start(encoder as any); // second start
      expect(errorHandler).toHaveBeenCalledWith({ message: "Recording already in progress" });
    });

    it("ignores pause when not recording", () => {
      const rm = new RecordingManager(defaultOpts);
      rm.pause(); // no-op
      expect(rm.getState()).toBe("idle");
    });

    it("ignores resume when not paused", () => {
      const rm = new RecordingManager(defaultOpts);
      const encoder = createMockEncoder();
      rm.start(encoder as any);
      rm.resume(); // already recording, no-op
      expect(rm.getState()).toBe("recording");
    });

    it("returns null when stopping from idle", () => {
      const rm = new RecordingManager(defaultOpts);
      const result = rm.stop();
      expect(result).toBeNull();
    });
  });

  // ===========================================================================
  // Encoder event subscription / unsubscription
  // ===========================================================================
  describe("encoder event wiring", () => {
    it("subscribes to videoChunk and audioChunk on start", () => {
      const rm = new RecordingManager(defaultOpts);
      const encoder = createMockEncoder();
      rm.start(encoder as any);

      expect(encoder.on).toHaveBeenCalledWith("videoChunk", expect.any(Function));
      expect(encoder.on).toHaveBeenCalledWith("audioChunk", expect.any(Function));
    });

    it("unsubscribes from encoder events on stop", () => {
      const rm = new RecordingManager(defaultOpts);
      const encoder = createMockEncoder();
      rm.start(encoder as any);
      rm.stop();

      expect(encoder.off).toHaveBeenCalledWith("videoChunk", expect.any(Function));
      expect(encoder.off).toHaveBeenCalledWith("audioChunk", expect.any(Function));
    });

    it("has no dangling listeners after stop", () => {
      const rm = new RecordingManager(defaultOpts);
      const encoder = createMockEncoder();
      rm.start(encoder as any);

      expect(encoder._getListenerCount("videoChunk")).toBe(1);
      expect(encoder._getListenerCount("audioChunk")).toBe(1);

      rm.stop();

      expect(encoder._getListenerCount("videoChunk")).toBe(0);
      expect(encoder._getListenerCount("audioChunk")).toBe(0);
    });
  });

  // ===========================================================================
  // Chunk forwarding with timestamp conversion
  // ===========================================================================
  describe("chunk forwarding", () => {
    it("forwards video chunks to writer (microseconds → milliseconds)", () => {
      const rm = new RecordingManager(defaultOpts);
      const encoder = createMockEncoder();
      rm.start(encoder as any);

      // Emit a keyframe at 0µs, then a delta at 33333µs (~30fps)
      encoder._emit("videoChunk", makeVideoChunkData(0, "key"));
      encoder._emit("videoChunk", makeVideoChunkData(33_333, "delta"));

      // Duration should reflect the timestamp span (33.333ms)
      expect(rm.duration).toBeCloseTo(33.333, 1);
      // File size should be non-zero (header + blocks)
      expect(rm.fileSize).toBeGreaterThan(0);
    });

    it("forwards audio chunks to writer (verified via duration)", () => {
      const rm = new RecordingManager(defaultOpts);
      const encoder = createMockEncoder();
      rm.start(encoder as any);

      encoder._emit("videoChunk", makeVideoChunkData(0, "key"));
      encoder._emit("audioChunk", makeAudioChunkData(50_000)); // 50ms

      // Audio chunk at 50ms extends lastTimestampMs, so duration should reflect it
      expect(rm.duration).toBeGreaterThan(0);
    });

    it("discards chunks when paused", () => {
      const rm = new RecordingManager(defaultOpts);
      const encoder = createMockEncoder();
      rm.start(encoder as any);

      encoder._emit("videoChunk", makeVideoChunkData(0, "key"));
      const sizeBeforePause = rm.fileSize;

      rm.pause();
      encoder._emit("videoChunk", makeVideoChunkData(33_333, "delta"));
      encoder._emit("audioChunk", makeAudioChunkData(20_000));

      // Size shouldn't change while paused
      expect(rm.fileSize).toBe(sizeBeforePause);
    });

    it("resumes accepting chunks after resume", () => {
      const rm = new RecordingManager(defaultOpts);
      const encoder = createMockEncoder();
      rm.start(encoder as any);

      encoder._emit("videoChunk", makeVideoChunkData(0, "key"));
      rm.pause();
      const durationWhilePaused = rm.duration;

      rm.resume();
      encoder._emit("videoChunk", makeVideoChunkData(100_000, "key")); // 100ms
      // Duration should grow because lastTimestampMs is updated
      expect(rm.duration).toBeGreaterThan(durationWhilePaused);
    });
  });

  // ===========================================================================
  // Events
  // ===========================================================================
  describe("events", () => {
    it("emits started on start", () => {
      const rm = new RecordingManager(defaultOpts);
      const encoder = createMockEncoder();
      const handler = vi.fn();
      rm.on("started", handler);
      rm.start(encoder as any);
      expect(handler).toHaveBeenCalledOnce();
    });

    it("emits stopped with blob, duration, fileSize on stop", () => {
      const rm = new RecordingManager(defaultOpts);
      const encoder = createMockEncoder();
      const handler = vi.fn();
      rm.on("stopped", handler);

      rm.start(encoder as any);
      encoder._emit("videoChunk", makeVideoChunkData(0, "key"));
      encoder._emit("videoChunk", makeVideoChunkData(1_000_000, "delta"));
      rm.stop();

      expect(handler).toHaveBeenCalledOnce();
      const event = handler.mock.calls[0][0];
      expect(event.blob).toBeInstanceOf(Blob);
      expect(event.duration).toBeGreaterThan(0);
      expect(event.fileSize).toBeGreaterThan(0);
    });

    it("emits paused on pause", () => {
      const rm = new RecordingManager(defaultOpts);
      const encoder = createMockEncoder();
      const handler = vi.fn();
      rm.on("paused", handler);

      rm.start(encoder as any);
      rm.pause();
      expect(handler).toHaveBeenCalledOnce();
    });

    it("emits resumed on resume", () => {
      const rm = new RecordingManager(defaultOpts);
      const encoder = createMockEncoder();
      const handler = vi.fn();
      rm.on("resumed", handler);

      rm.start(encoder as any);
      rm.pause();
      rm.resume();
      expect(handler).toHaveBeenCalledOnce();
    });
  });

  // ===========================================================================
  // Progress events
  // ===========================================================================
  describe("progress events", () => {
    it("emits progress at configured interval", () => {
      const rm = new RecordingManager(defaultOpts);
      const encoder = createMockEncoder();
      const handler = vi.fn();
      rm.on("progress", handler);

      rm.start(encoder as any);
      encoder._emit("videoChunk", makeVideoChunkData(0, "key"));

      // Advance past one interval
      vi.advanceTimersByTime(100);
      expect(handler).toHaveBeenCalledOnce();
      const event = handler.mock.calls[0][0];
      expect(event).toHaveProperty("duration");
      expect(event).toHaveProperty("fileSize");
    });

    it("does not emit progress when paused", () => {
      const rm = new RecordingManager(defaultOpts);
      const encoder = createMockEncoder();
      const handler = vi.fn();
      rm.on("progress", handler);

      rm.start(encoder as any);
      rm.pause();

      vi.advanceTimersByTime(500);
      expect(handler).not.toHaveBeenCalled();
    });

    it("stops progress timer on stop", () => {
      const rm = new RecordingManager(defaultOpts);
      const encoder = createMockEncoder();
      const handler = vi.fn();
      rm.on("progress", handler);

      rm.start(encoder as any);
      rm.stop();

      vi.advanceTimersByTime(500);
      expect(handler).not.toHaveBeenCalled();
    });
  });

  // ===========================================================================
  // stop() returns Blob
  // ===========================================================================
  describe("stop returns blob", () => {
    it("returns a valid WebM blob", () => {
      const rm = new RecordingManager(defaultOpts);
      const encoder = createMockEncoder();
      rm.start(encoder as any);

      encoder._emit("videoChunk", makeVideoChunkData(0, "key"));
      encoder._emit("videoChunk", makeVideoChunkData(33_333, "delta"));

      const blob = rm.stop();
      expect(blob).toBeInstanceOf(Blob);
      expect(blob!.type).toBe("video/webm");
      expect(blob!.size).toBeGreaterThan(0);
    });
  });

  // ===========================================================================
  // destroy
  // ===========================================================================
  describe("destroy", () => {
    it("stops recording if active and cleans up", () => {
      const rm = new RecordingManager(defaultOpts);
      const encoder = createMockEncoder();
      const stoppedHandler = vi.fn();
      rm.on("stopped", stoppedHandler);

      rm.start(encoder as any);
      rm.destroy();

      expect(stoppedHandler).toHaveBeenCalledOnce();
      expect(rm.getState()).toBe("idle");
    });

    it("is safe to call from idle state", () => {
      const rm = new RecordingManager(defaultOpts);
      expect(() => rm.destroy()).not.toThrow();
    });

    it("removes all event listeners", () => {
      const rm = new RecordingManager(defaultOpts);
      const handler = vi.fn();
      rm.on("started", handler);
      rm.destroy();

      // After destroy, listeners should be gone (removeAllListeners called)
      const encoder = createMockEncoder();
      rm.start(encoder as any);
      expect(handler).not.toHaveBeenCalled();
    });
  });

  // ===========================================================================
  // duration / fileSize getters
  // ===========================================================================
  describe("duration and fileSize getters", () => {
    it("duration is 0 before any chunks", () => {
      const rm = new RecordingManager(defaultOpts);
      expect(rm.duration).toBe(0);
    });

    it("fileSize is 0 before start", () => {
      const rm = new RecordingManager(defaultOpts);
      expect(rm.fileSize).toBe(0);
    });

    it("duration reflects writer duration", () => {
      const rm = new RecordingManager(defaultOpts);
      const encoder = createMockEncoder();
      rm.start(encoder as any);
      encoder._emit("videoChunk", makeVideoChunkData(1_000_000, "key")); // 1s
      encoder._emit("videoChunk", makeVideoChunkData(3_000_000, "delta")); // 3s
      expect(rm.duration).toBe(2000); // 2s difference
    });
  });
});
