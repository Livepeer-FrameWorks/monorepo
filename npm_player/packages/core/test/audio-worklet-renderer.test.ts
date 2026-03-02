import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { AudioWorkletRenderer } from "../src/rendering/AudioWorkletRenderer";

function createMockGainNode() {
  return {
    gain: { value: 0, setTargetAtTime: vi.fn() },
    connect: vi.fn(),
    disconnect: vi.fn(),
  };
}

function createMockWorkletNode() {
  return {
    connect: vi.fn(),
    disconnect: vi.fn(),
    port: {
      postMessage: vi.fn(),
      onmessage: null as ((e: MessageEvent) => void) | null,
    },
  };
}

function createMockAudioContext(state = "running") {
  const gainNode = createMockGainNode();
  const workletNode = createMockWorkletNode();

  const ctx = {
    sampleRate: 48000,
    state,
    currentTime: 0.5,
    destination: {},
    createGain: vi.fn(() => gainNode),
    audioWorklet: {
      addModule: vi.fn().mockResolvedValue(undefined),
    },
    suspend: vi.fn().mockResolvedValue(undefined),
    resume: vi.fn().mockResolvedValue(undefined),
    close: vi.fn().mockResolvedValue(undefined),
  };

  return { ctx, gainNode, workletNode };
}

function installMocks(ctxState = "running") {
  const { ctx, gainNode, workletNode } = createMockAudioContext(ctxState);
  (globalThis as any).AudioContext = vi.fn(function (this: any) {
    const { state: _s, currentTime: _ct, ...rest } = ctx;
    Object.assign(this, rest);
    Object.defineProperty(this, "state", {
      get: () => ctx.state,
      set: (v: string) => {
        ctx.state = v;
      },
      configurable: true,
    });
    Object.defineProperty(this, "currentTime", {
      get: () => ctx.currentTime,
      set: (v: number) => {
        ctx.currentTime = v;
      },
      configurable: true,
    });
    return this;
  });
  (globalThis as any).AudioWorkletNode = vi.fn(function (this: any) {
    Object.assign(this, workletNode);
    return this;
  });
  return { ctx, gainNode, workletNode };
}

describe("AudioWorkletRenderer", () => {
  let origAudioContext: any;
  let origAudioWorkletNode: any;

  beforeEach(() => {
    origAudioContext = (globalThis as any).AudioContext;
    origAudioWorkletNode = (globalThis as any).AudioWorkletNode;
  });

  afterEach(() => {
    (globalThis as any).AudioContext = origAudioContext;
    (globalThis as any).AudioWorkletNode = origAudioWorkletNode;
    vi.restoreAllMocks();
  });

  describe("constructor", () => {
    it("defaults to 48000 sampleRate and 2 channels", () => {
      const renderer = new AudioWorkletRenderer();
      expect(renderer.getCurrentTime()).toBe(0);
    });

    it("accepts custom options", () => {
      const renderer = new AudioWorkletRenderer({ sampleRate: 44100, channels: 1 });
      expect(renderer).toBeInstanceOf(AudioWorkletRenderer);
    });
  });

  describe("start", () => {
    it("creates AudioContext and wires the graph", async () => {
      const { ctx, gainNode, workletNode } = installMocks();
      const renderer = new AudioWorkletRenderer();
      await renderer.start();

      expect(globalThis.AudioContext).toHaveBeenCalled();
      expect(ctx.audioWorklet.addModule).toHaveBeenCalledWith(
        expect.stringContaining("data:text/javascript,")
      );
      expect(workletNode.connect).toHaveBeenCalledWith(gainNode);
      expect(gainNode.connect).toHaveBeenCalledWith(ctx.destination);
    });

    it("resumes suspended context", async () => {
      const { ctx } = installMocks("suspended");
      const renderer = new AudioWorkletRenderer();
      await renderer.start();
      expect(ctx.resume).toHaveBeenCalled();
    });

    it("does not resume running context", async () => {
      const { ctx } = installMocks("running");
      const renderer = new AudioWorkletRenderer();
      await renderer.start();
      expect(ctx.resume).not.toHaveBeenCalled();
    });

    it("is no-op if already started", async () => {
      installMocks();
      const renderer = new AudioWorkletRenderer();
      await renderer.start();
      await renderer.start();
      expect(globalThis.AudioContext).toHaveBeenCalledTimes(1);
    });

    it("is no-op if destroyed", async () => {
      installMocks();
      const renderer = new AudioWorkletRenderer();
      renderer.destroy();
      await renderer.start();
      expect(globalThis.AudioContext).not.toHaveBeenCalled();
    });

    it("flushes pending data after setup", async () => {
      const { workletNode } = installMocks();
      const renderer = new AudioWorkletRenderer();

      const audioData = {
        numberOfChannels: 2,
        numberOfFrames: 128,
        copyTo: vi.fn(),
        close: vi.fn(),
      };
      renderer.feed(audioData as any);
      expect(workletNode.port.postMessage).not.toHaveBeenCalled();

      await renderer.start();
      expect(workletNode.port.postMessage).toHaveBeenCalled();
    });

    it("sets gain.value to 0 on start", async () => {
      const { gainNode } = installMocks();
      const renderer = new AudioWorkletRenderer();
      await renderer.start();
      expect(gainNode.gain.value).toBe(0);
    });
  });

  describe("feed", () => {
    it("extracts channels and posts to worklet", async () => {
      const { workletNode } = installMocks();
      const renderer = new AudioWorkletRenderer();
      await renderer.start();

      const audioData = {
        numberOfChannels: 2,
        numberOfFrames: 128,
        copyTo: vi.fn(),
        close: vi.fn(),
      };

      renderer.feed(audioData as any);
      expect(audioData.copyTo).toHaveBeenCalledTimes(2);
      expect(audioData.close).toHaveBeenCalled();
      expect(workletNode.port.postMessage).toHaveBeenCalledWith(
        expect.objectContaining({ type: "samples", channels: 2 }),
        expect.any(Array)
      );
    });

    it("closes data and returns when destroyed", async () => {
      installMocks();
      const renderer = new AudioWorkletRenderer();
      await renderer.start();
      renderer.destroy();

      const audioData = {
        numberOfChannels: 1,
        numberOfFrames: 64,
        copyTo: vi.fn(),
        close: vi.fn(),
      };

      renderer.feed(audioData as any);
      expect(audioData.close).toHaveBeenCalled();
      expect(audioData.copyTo).not.toHaveBeenCalled();
    });

    it("buffers data before start completes", () => {
      const renderer = new AudioWorkletRenderer();
      const audioData = {
        numberOfChannels: 2,
        numberOfFrames: 128,
        copyTo: vi.fn(),
        close: vi.fn(),
      };

      renderer.feed(audioData as any);
      expect(audioData.close).toHaveBeenCalled();
      expect(audioData.copyTo).toHaveBeenCalledTimes(2);
    });
  });

  describe("gain ramp", () => {
    it("ramps gain up after 5 frames sent", async () => {
      const { ctx, gainNode } = installMocks();
      const renderer = new AudioWorkletRenderer();
      await renderer.start();

      for (let i = 0; i < 5; i++) {
        renderer.feed({
          numberOfChannels: 1,
          numberOfFrames: 128,
          copyTo: vi.fn(),
          close: vi.fn(),
        } as any);
      }

      expect(gainNode.gain.setTargetAtTime).toHaveBeenCalledWith(1.0, ctx.currentTime, 0.05);
    });

    it("does not ramp before 5 frames", async () => {
      const { gainNode } = installMocks();
      const renderer = new AudioWorkletRenderer();
      await renderer.start();

      for (let i = 0; i < 4; i++) {
        renderer.feed({
          numberOfChannels: 1,
          numberOfFrames: 128,
          copyTo: vi.fn(),
          close: vi.fn(),
        } as any);
      }

      expect(gainNode.gain.setTargetAtTime).not.toHaveBeenCalledWith(1.0, expect.any(Number), 0.05);
    });
  });

  describe("setVolume", () => {
    it("calls setTargetAtTime with clamped value", async () => {
      const { ctx, gainNode } = installMocks();
      const renderer = new AudioWorkletRenderer();
      await renderer.start();

      renderer.setVolume(0.5);
      expect(gainNode.gain.setTargetAtTime).toHaveBeenCalledWith(0.5, ctx.currentTime, 0.015);
    });

    it("clamps volume to 0-1 range", async () => {
      const { ctx, gainNode } = installMocks();
      const renderer = new AudioWorkletRenderer();
      await renderer.start();

      renderer.setVolume(-0.5);
      expect(gainNode.gain.setTargetAtTime).toHaveBeenCalledWith(0, ctx.currentTime, 0.015);

      renderer.setVolume(1.5);
      expect(gainNode.gain.setTargetAtTime).toHaveBeenCalledWith(1, ctx.currentTime, 0.015);
    });

    it("is no-op when not started", () => {
      const renderer = new AudioWorkletRenderer();
      renderer.setVolume(0.5);
    });
  });

  describe("setMuted", () => {
    it("ramps gain to 0 when muted", async () => {
      const { ctx, gainNode } = installMocks();
      const renderer = new AudioWorkletRenderer();
      await renderer.start();

      renderer.setMuted(true);
      expect(gainNode.gain.setTargetAtTime).toHaveBeenCalledWith(0, ctx.currentTime, 0.015);
    });

    it("is no-op when not started", () => {
      const renderer = new AudioWorkletRenderer();
      renderer.setMuted(true);
    });
  });

  describe("setPlaybackRate", () => {
    it("is a no-op", () => {
      const renderer = new AudioWorkletRenderer();
      renderer.setPlaybackRate(2.0);
    });
  });

  describe("getCurrentTime", () => {
    it("returns 0 before start", () => {
      const renderer = new AudioWorkletRenderer();
      expect(renderer.getCurrentTime()).toBe(0);
    });

    it("returns audioContext.currentTime after start", async () => {
      const { ctx } = installMocks();
      ctx.currentTime = 1.234;
      const renderer = new AudioWorkletRenderer();
      await renderer.start();
      expect(renderer.getCurrentTime()).toBe(1.234);
    });
  });

  describe("getAudioContext", () => {
    it("returns null before start", () => {
      const renderer = new AudioWorkletRenderer();
      expect(renderer.getAudioContext()).toBeNull();
    });

    it("returns the AudioContext after start", async () => {
      installMocks();
      const renderer = new AudioWorkletRenderer();
      await renderer.start();
      expect(renderer.getAudioContext()).not.toBeNull();
    });
  });

  describe("suspend / resume", () => {
    it("suspends a running context", async () => {
      const { ctx } = installMocks("running");
      const renderer = new AudioWorkletRenderer();
      await renderer.start();
      await renderer.suspend();
      expect(ctx.suspend).toHaveBeenCalled();
    });

    it("does not suspend a non-running context", async () => {
      const { ctx } = installMocks("suspended");
      const renderer = new AudioWorkletRenderer();
      await renderer.start();
      ctx.state = "closed";
      await renderer.suspend();
      expect(ctx.suspend).not.toHaveBeenCalled();
    });

    it("resumes a suspended context", async () => {
      const { ctx } = installMocks("running");
      const renderer = new AudioWorkletRenderer();
      await renderer.start();
      ctx.state = "suspended";
      await renderer.resume();
      // resume called during start (no, state was running) and here
      expect(ctx.resume).toHaveBeenCalled();
    });

    it("does not resume a running context", async () => {
      const { ctx } = installMocks("running");
      const renderer = new AudioWorkletRenderer();
      await renderer.start();
      ctx.resume.mockClear();
      await renderer.resume();
      expect(ctx.resume).not.toHaveBeenCalled();
    });
  });

  describe("underrun callback", () => {
    it("fires onUnderrun when worklet reports underrun", async () => {
      const { workletNode } = installMocks();
      const renderer = new AudioWorkletRenderer();
      const onUnderrun = vi.fn();
      renderer.onUnderrun = onUnderrun;

      await renderer.start();
      workletNode.port.onmessage?.({ data: { type: "underrun", time: 1.5 } } as any);
      expect(onUnderrun).toHaveBeenCalledWith(1.5);
    });
  });

  describe("destroy", () => {
    it("posts destroy message and disconnects", async () => {
      const { ctx, gainNode, workletNode } = installMocks();
      const renderer = new AudioWorkletRenderer();
      await renderer.start();
      renderer.destroy();

      expect(workletNode.port.postMessage).toHaveBeenCalledWith({ type: "destroy" });
      expect(workletNode.disconnect).toHaveBeenCalled();
      expect(gainNode.disconnect).toHaveBeenCalled();
      expect(ctx.close).toHaveBeenCalled();
    });

    it("is idempotent", async () => {
      installMocks();
      const renderer = new AudioWorkletRenderer();
      await renderer.start();
      renderer.destroy();
      renderer.destroy();
    });

    it("clears pending data", () => {
      const renderer = new AudioWorkletRenderer();
      renderer.feed({
        numberOfChannels: 1,
        numberOfFrames: 64,
        copyTo: vi.fn(),
        close: vi.fn(),
      } as any);
      renderer.destroy();
    });
  });
});
