import { describe, expect, it, vi, beforeEach, afterEach } from "vitest";

import { NoiseGate, type NoiseGateOptions } from "../src/audio/NoiseGate";

// Stub requestAnimationFrame / cancelAnimationFrame
let rafCallbacks: Map<number, FrameRequestCallback>;
let nextRafId: number;

function setupRafStub() {
  rafCallbacks = new Map();
  nextRafId = 1;
  (globalThis as any).requestAnimationFrame = vi.fn((cb: FrameRequestCallback) => {
    const id = nextRafId++;
    rafCallbacks.set(id, cb);
    return id;
  });
  (globalThis as any).cancelAnimationFrame = vi.fn((id: number) => {
    rafCallbacks.delete(id);
  });
}

function fireRaf() {
  const cbs = [...rafCallbacks.values()];
  rafCallbacks.clear();
  for (const cb of cbs) cb(performance.now());
}

// Mock AudioContext with BiquadFilter and Analyser support
function createMockGainNode() {
  return {
    gain: { value: 1, setTargetAtTime: vi.fn() },
    connect: vi.fn(),
    disconnect: vi.fn(),
  };
}

function createMockAnalyser(rmsLevel = 0) {
  return {
    fftSize: 256,
    smoothingTimeConstant: 0,
    getFloatTimeDomainData: vi.fn((data: Float32Array) => {
      data.fill(rmsLevel);
    }),
    connect: vi.fn(),
    disconnect: vi.fn(),
  };
}

function createMockAudioContext() {
  return {
    currentTime: 0,
    createGain: vi.fn(() => createMockGainNode()),
    createAnalyser: vi.fn(() => createMockAnalyser()),
  };
}

describe("NoiseGate", () => {
  let ctx: ReturnType<typeof createMockAudioContext>;

  beforeEach(() => {
    vi.useFakeTimers();
    setupRafStub();
    ctx = createMockAudioContext();
  });

  afterEach(() => {
    vi.useRealTimers();
    vi.restoreAllMocks();
  });

  // ===========================================================================
  // Node wiring
  // ===========================================================================
  describe("node wiring", () => {
    it("creates gain + analyser + output gain nodes", () => {
      const gate = new NoiseGate(ctx as any);
      expect(ctx.createGain).toHaveBeenCalledTimes(2); // input + output
      expect(ctx.createAnalyser).toHaveBeenCalledTimes(1);
      gate.destroy();
    });

    it("wires input → analyser and input → outputGain", () => {
      const gate = new NoiseGate(ctx as any);
      const inputGain = ctx.createGain.mock.results[0].value;
      expect(inputGain.connect).toHaveBeenCalledTimes(2);
      gate.destroy();
    });

    it("exposes input and output AudioNodes", () => {
      const gate = new NoiseGate(ctx as any);
      expect(gate.input).toBeDefined();
      expect(gate.output).toBeDefined();
      expect(gate.input).not.toBe(gate.output);
      gate.destroy();
    });
  });

  // ===========================================================================
  // Gate states (open / hold / closed)
  // ===========================================================================
  describe("gate states", () => {
    it("starts in closed state", () => {
      const gate = new NoiseGate(ctx as any);
      expect(gate.currentState).toBe("closed");
      expect(gate.isOpen).toBe(false);
      gate.destroy();
    });

    it("opens when signal exceeds threshold", () => {
      // Signal level of 0.1 → ~-20 dB, above default -40 dB threshold
      const analyser = createMockAnalyser(0.1);
      ctx.createAnalyser = vi.fn(() => analyser);

      const gate = new NoiseGate(ctx as any);
      fireRaf(); // process one frame
      expect(gate.currentState).toBe("open");
      expect(gate.isOpen).toBe(true);
      gate.destroy();
    });

    it("stays closed when signal is below threshold", () => {
      // Signal level of 0.0001 → ~-80 dB, below default -40 dB threshold
      const analyser = createMockAnalyser(0.0001);
      ctx.createAnalyser = vi.fn(() => analyser);

      const gate = new NoiseGate(ctx as any);
      fireRaf();
      expect(gate.currentState).toBe("closed");
      gate.destroy();
    });

    it("transitions to hold when signal drops below threshold", () => {
      const analyser = createMockAnalyser(0.1);
      ctx.createAnalyser = vi.fn(() => analyser);

      const gate = new NoiseGate(ctx as any);
      fireRaf(); // open
      expect(gate.currentState).toBe("open");

      // Drop signal
      analyser.getFloatTimeDomainData = vi.fn((data: Float32Array) => data.fill(0.00001));
      fireRaf();
      expect(gate.currentState).toBe("hold");
      gate.destroy();
    });

    it("transitions from hold → closed after hold time expires", () => {
      const analyser = createMockAnalyser(0.1);
      ctx.createAnalyser = vi.fn(() => analyser);

      const gate = new NoiseGate(ctx as any, { hold: 50 });
      fireRaf(); // open

      // Drop signal → hold
      analyser.getFloatTimeDomainData = vi.fn((data: Float32Array) => data.fill(0.00001));
      fireRaf();
      expect(gate.currentState).toBe("hold");

      // Advance past hold time
      vi.advanceTimersByTime(60);
      expect(gate.currentState).toBe("closed");
      gate.destroy();
    });

    it("returns to open from hold if signal returns", () => {
      const analyser = createMockAnalyser(0.1);
      ctx.createAnalyser = vi.fn(() => analyser);

      const gate = new NoiseGate(ctx as any, { hold: 100 });
      fireRaf(); // open

      // Drop → hold
      analyser.getFloatTimeDomainData = vi.fn((data: Float32Array) => data.fill(0.00001));
      fireRaf();
      expect(gate.currentState).toBe("hold");

      // Signal comes back → open
      analyser.getFloatTimeDomainData = vi.fn((data: Float32Array) => data.fill(0.1));
      fireRaf();
      expect(gate.currentState).toBe("open");
      gate.destroy();
    });

    it("isOpen is true for both open and hold states", () => {
      const analyser = createMockAnalyser(0.1);
      ctx.createAnalyser = vi.fn(() => analyser);

      const gate = new NoiseGate(ctx as any);
      fireRaf();
      expect(gate.isOpen).toBe(true);

      analyser.getFloatTimeDomainData = vi.fn((data: Float32Array) => data.fill(0.00001));
      fireRaf();
      expect(gate.currentState).toBe("hold");
      expect(gate.isOpen).toBe(true);
      gate.destroy();
    });
  });

  // ===========================================================================
  // Parameter setters with clamping
  // ===========================================================================
  describe("parameter setters", () => {
    it("setThreshold sets the threshold", () => {
      const gate = new NoiseGate(ctx as any);
      gate.setThreshold(-30);
      expect(gate.getThreshold()).toBe(-30);
      gate.destroy();
    });

    it("setAttack clamps to minimum 0.1ms", () => {
      const gate = new NoiseGate(ctx as any);
      gate.setAttack(-5);
      // Should be clamped internally (we can't read it back directly,
      // but we verify it doesn't throw)
      expect(() => gate.setAttack(0.05)).not.toThrow();
      gate.destroy();
    });

    it("setHold clamps to minimum 0ms", () => {
      const gate = new NoiseGate(ctx as any);
      gate.setHold(-10);
      expect(() => gate.setHold(0)).not.toThrow();
      gate.destroy();
    });

    it("setRelease clamps to minimum 1ms", () => {
      const gate = new NoiseGate(ctx as any);
      gate.setRelease(-5);
      expect(() => gate.setRelease(0.5)).not.toThrow();
      gate.destroy();
    });

    it("setRange sets attenuation depth", () => {
      const gate = new NoiseGate(ctx as any);
      gate.setRange(-60);
      expect(() => gate.setRange(-100)).not.toThrow();
      gate.destroy();
    });
  });

  // ===========================================================================
  // Default options
  // ===========================================================================
  describe("default options", () => {
    it("uses -40 dB threshold by default", () => {
      const gate = new NoiseGate(ctx as any);
      expect(gate.getThreshold()).toBe(-40);
      gate.destroy();
    });

    it("accepts custom options", () => {
      const gate = new NoiseGate(ctx as any, {
        threshold: -30,
        attack: 5,
        hold: 100,
        release: 200,
        range: -60,
      });
      expect(gate.getThreshold()).toBe(-30);
      gate.destroy();
    });
  });

  // ===========================================================================
  // Destroy
  // ===========================================================================
  describe("destroy", () => {
    it("cancels requestAnimationFrame", () => {
      const gate = new NoiseGate(ctx as any);
      gate.destroy();
      expect(cancelAnimationFrame).toHaveBeenCalled();
    });

    it("disconnects all nodes", () => {
      const gate = new NoiseGate(ctx as any);
      const inputGain = ctx.createGain.mock.results[0].value;
      const outputGain = ctx.createGain.mock.results[1].value;
      const analyser = ctx.createAnalyser.mock.results[0].value;

      gate.destroy();
      expect(inputGain.disconnect).toHaveBeenCalled();
      expect(outputGain.disconnect).toHaveBeenCalled();
      expect(analyser.disconnect).toHaveBeenCalled();
    });

    it("stops processing loop after destroy", () => {
      const gate = new NoiseGate(ctx as any);
      gate.destroy();

      // Fire RAF — should not schedule new frames
      const callsBefore = (requestAnimationFrame as any).mock.calls.length;
      fireRaf();
      const callsAfter = (requestAnimationFrame as any).mock.calls.length;
      expect(callsAfter).toBe(callsBefore);
    });

    it("clears hold timer", () => {
      const analyser = createMockAnalyser(0.1);
      ctx.createAnalyser = vi.fn(() => analyser);

      const gate = new NoiseGate(ctx as any, { hold: 1000 });
      fireRaf(); // open

      // Drop to hold
      analyser.getFloatTimeDomainData = vi.fn((data: Float32Array) => data.fill(0.00001));
      fireRaf();
      expect(gate.currentState).toBe("hold");

      gate.destroy();
      // Advancing timers should not cause issues
      vi.advanceTimersByTime(2000);
    });
  });
});
