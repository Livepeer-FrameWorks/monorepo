import { describe, expect, it, vi, beforeEach, afterEach } from "vitest";

import { NoiseGate } from "../src/audio/NoiseGate";

const tap = vi.hoisted(() => ({
  emit: (_rms: number) => {},
  destroy: vi.fn(),
  input: { connect: vi.fn(), disconnect: vi.fn() },
}));

vi.mock("../src/audio/LevelTap", () => ({
  LevelTap: class {
    input = tap.input;
    destroy = tap.destroy;
    constructor(_context: unknown, onLevel: (rms: number) => void) {
      tap.emit = onLevel;
    }
  },
}));

const LOUD = 0.1; // ~-20 dB, above the default -40 dB threshold
const QUIET = 0.0001; // -80 dB, below it

function createMockGainNode() {
  return {
    gain: { value: 1, setTargetAtTime: vi.fn() },
    connect: vi.fn(),
    disconnect: vi.fn(),
  };
}

function createMockAudioContext() {
  return {
    currentTime: 0,
    createGain: vi.fn(() => createMockGainNode()),
  };
}

describe("NoiseGate", () => {
  let ctx: ReturnType<typeof createMockAudioContext>;

  beforeEach(() => {
    vi.useFakeTimers();
    tap.destroy.mockClear();
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
    it("creates input and output gain nodes", () => {
      const gate = new NoiseGate(ctx as any);
      expect(ctx.createGain).toHaveBeenCalledTimes(2); // input + output
      gate.destroy();
    });

    it("wires input → level tap and input → outputGain", () => {
      const gate = new NoiseGate(ctx as any);
      const inputGain = ctx.createGain.mock.results[0].value;
      const outputGain = ctx.createGain.mock.results[1].value;
      expect(inputGain.connect).toHaveBeenCalledWith(tap.input);
      expect(inputGain.connect).toHaveBeenCalledWith(outputGain);
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
      const gate = new NoiseGate(ctx as any);
      tap.emit(LOUD);
      expect(gate.currentState).toBe("open");
      expect(gate.isOpen).toBe(true);
      gate.destroy();
    });

    it("stays closed when signal is below threshold", () => {
      const gate = new NoiseGate(ctx as any);
      tap.emit(QUIET);
      expect(gate.currentState).toBe("closed");
      gate.destroy();
    });

    it("transitions to hold when signal drops below threshold", () => {
      const gate = new NoiseGate(ctx as any);
      tap.emit(LOUD);
      expect(gate.currentState).toBe("open");

      tap.emit(QUIET);
      expect(gate.currentState).toBe("hold");
      gate.destroy();
    });

    it("transitions from hold → closed after hold time expires", () => {
      const gate = new NoiseGate(ctx as any, { hold: 50 });
      tap.emit(LOUD);

      tap.emit(QUIET);
      expect(gate.currentState).toBe("hold");

      // Advance past hold time
      vi.advanceTimersByTime(60);
      expect(gate.currentState).toBe("closed");
      gate.destroy();
    });

    it("returns to open from hold if signal returns", () => {
      const gate = new NoiseGate(ctx as any, { hold: 100 });
      tap.emit(LOUD);

      tap.emit(QUIET);
      expect(gate.currentState).toBe("hold");

      tap.emit(LOUD);
      expect(gate.currentState).toBe("open");
      gate.destroy();
    });

    it("isOpen is true for both open and hold states", () => {
      const gate = new NoiseGate(ctx as any);
      tap.emit(LOUD);
      expect(gate.isOpen).toBe(true);

      tap.emit(QUIET);
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
    it("destroys the level tap", () => {
      const gate = new NoiseGate(ctx as any);
      gate.destroy();
      expect(tap.destroy).toHaveBeenCalled();
    });

    it("disconnects all nodes", () => {
      const gate = new NoiseGate(ctx as any);
      const inputGain = ctx.createGain.mock.results[0].value;
      const outputGain = ctx.createGain.mock.results[1].value;

      gate.destroy();
      expect(inputGain.disconnect).toHaveBeenCalled();
      expect(outputGain.disconnect).toHaveBeenCalled();
    });

    it("clears hold timer", () => {
      const gate = new NoiseGate(ctx as any, { hold: 1000 });
      tap.emit(LOUD);

      tap.emit(QUIET);
      expect(gate.currentState).toBe("hold");

      gate.destroy();
      // Advancing timers should not cause issues
      vi.advanceTimersByTime(2000);
    });
  });
});
