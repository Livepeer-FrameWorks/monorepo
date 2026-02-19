import { describe, expect, it, vi, beforeEach, afterEach } from "vitest";

import { HighPassFilter, type HighPassFilterOptions } from "../src/audio/HighPassFilter";

function createMockBiquadFilter() {
  return {
    type: "" as BiquadFilterType,
    frequency: { value: 0, setTargetAtTime: vi.fn() },
    Q: { value: 0, setTargetAtTime: vi.fn() },
    connect: vi.fn(),
    disconnect: vi.fn(),
    context: { currentTime: 0 },
  };
}

function createMockAudioContext() {
  return {
    currentTime: 0,
    createBiquadFilter: vi.fn(() => createMockBiquadFilter()),
  };
}

describe("HighPassFilter", () => {
  let ctx: ReturnType<typeof createMockAudioContext>;

  beforeEach(() => {
    ctx = createMockAudioContext();
  });

  afterEach(() => {
    vi.restoreAllMocks();
  });

  // ===========================================================================
  // Filter type
  // ===========================================================================
  describe("filter configuration", () => {
    it("creates a BiquadFilterNode with type highpass", () => {
      const hpf = new HighPassFilter(ctx as any);
      const filter = ctx.createBiquadFilter.mock.results[0].value;
      expect(filter.type).toBe("highpass");
      hpf.destroy();
    });

    it("defaults to 80 Hz cutoff frequency", () => {
      const hpf = new HighPassFilter(ctx as any);
      const filter = ctx.createBiquadFilter.mock.results[0].value;
      expect(filter.frequency.value).toBe(80);
      hpf.destroy();
    });

    it("defaults to 0.707 Q (Butterworth)", () => {
      const hpf = new HighPassFilter(ctx as any);
      const filter = ctx.createBiquadFilter.mock.results[0].value;
      expect(filter.Q.value).toBeCloseTo(0.707);
      hpf.destroy();
    });

    it("accepts custom frequency and Q", () => {
      const hpf = new HighPassFilter(ctx as any, { frequency: 120, q: 1.5 });
      const filter = ctx.createBiquadFilter.mock.results[0].value;
      expect(filter.frequency.value).toBe(120);
      expect(filter.Q.value).toBe(1.5);
      hpf.destroy();
    });
  });

  // ===========================================================================
  // Input / output (same node for pass-through filter)
  // ===========================================================================
  describe("input and output", () => {
    it("input and output are the same filter node", () => {
      const hpf = new HighPassFilter(ctx as any);
      expect(hpf.input).toBe(hpf.output);
      hpf.destroy();
    });

    it("input is the BiquadFilterNode", () => {
      const hpf = new HighPassFilter(ctx as any);
      const filter = ctx.createBiquadFilter.mock.results[0].value;
      expect(hpf.input).toBe(filter);
      hpf.destroy();
    });
  });

  // ===========================================================================
  // Setters
  // ===========================================================================
  describe("setters", () => {
    it("setFrequency uses setTargetAtTime", () => {
      const hpf = new HighPassFilter(ctx as any);
      const filter = ctx.createBiquadFilter.mock.results[0].value;

      hpf.setFrequency(150);
      expect(filter.frequency.setTargetAtTime).toHaveBeenCalledWith(150, expect.any(Number), 0.01);
      hpf.destroy();
    });

    it("setQ uses setTargetAtTime", () => {
      const hpf = new HighPassFilter(ctx as any);
      const filter = ctx.createBiquadFilter.mock.results[0].value;

      hpf.setQ(2.0);
      expect(filter.Q.setTargetAtTime).toHaveBeenCalledWith(2.0, expect.any(Number), 0.01);
      hpf.destroy();
    });
  });

  // ===========================================================================
  // Getters
  // ===========================================================================
  describe("getters", () => {
    it("getFrequency returns current frequency value", () => {
      const hpf = new HighPassFilter(ctx as any);
      const filter = ctx.createBiquadFilter.mock.results[0].value;
      filter.frequency.value = 200;
      expect(hpf.getFrequency()).toBe(200);
      hpf.destroy();
    });
  });

  // ===========================================================================
  // Destroy
  // ===========================================================================
  describe("destroy", () => {
    it("disconnects the filter node", () => {
      const hpf = new HighPassFilter(ctx as any);
      const filter = ctx.createBiquadFilter.mock.results[0].value;
      hpf.destroy();
      expect(filter.disconnect).toHaveBeenCalled();
    });
  });
});
