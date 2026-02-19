import { describe, expect, it, vi, beforeEach, afterEach } from "vitest";

import { ThreeBandEQ, type ThreeBandEQOptions } from "../src/audio/ThreeBandEQ";

function createMockBiquadFilter() {
  return {
    type: "" as BiquadFilterType,
    frequency: { value: 0, setTargetAtTime: vi.fn() },
    gain: { value: 0, setTargetAtTime: vi.fn() },
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

describe("ThreeBandEQ", () => {
  let ctx: ReturnType<typeof createMockAudioContext>;

  beforeEach(() => {
    ctx = createMockAudioContext();
  });

  afterEach(() => {
    vi.restoreAllMocks();
  });

  // ===========================================================================
  // Creates 3 BiquadFilterNodes at correct frequencies
  // ===========================================================================
  describe("filter node creation", () => {
    it("creates three BiquadFilterNodes", () => {
      const eq = new ThreeBandEQ(ctx as any);
      expect(ctx.createBiquadFilter).toHaveBeenCalledTimes(3);
      eq.destroy();
    });

    it("configures lowshelf at 200 Hz by default", () => {
      const eq = new ThreeBandEQ(ctx as any);
      const lowFilter = ctx.createBiquadFilter.mock.results[0].value;
      expect(lowFilter.type).toBe("lowshelf");
      expect(lowFilter.frequency.value).toBe(200);
      eq.destroy();
    });

    it("configures peaking at 1000 Hz by default", () => {
      const eq = new ThreeBandEQ(ctx as any);
      const midFilter = ctx.createBiquadFilter.mock.results[1].value;
      expect(midFilter.type).toBe("peaking");
      expect(midFilter.frequency.value).toBe(1000);
      expect(midFilter.Q.value).toBe(1.0);
      eq.destroy();
    });

    it("configures highshelf at 4000 Hz by default", () => {
      const eq = new ThreeBandEQ(ctx as any);
      const highFilter = ctx.createBiquadFilter.mock.results[2].value;
      expect(highFilter.type).toBe("highshelf");
      expect(highFilter.frequency.value).toBe(4000);
      eq.destroy();
    });

    it("accepts custom frequencies", () => {
      const eq = new ThreeBandEQ(ctx as any, {
        lowFreq: 150,
        midFreq: 800,
        highFreq: 5000,
        midQ: 2.0,
      });
      const low = ctx.createBiquadFilter.mock.results[0].value;
      const mid = ctx.createBiquadFilter.mock.results[1].value;
      const high = ctx.createBiquadFilter.mock.results[2].value;
      expect(low.frequency.value).toBe(150);
      expect(mid.frequency.value).toBe(800);
      expect(mid.Q.value).toBe(2.0);
      expect(high.frequency.value).toBe(5000);
      eq.destroy();
    });

    it("chains filters: low → mid → high", () => {
      const eq = new ThreeBandEQ(ctx as any);
      const low = ctx.createBiquadFilter.mock.results[0].value;
      const mid = ctx.createBiquadFilter.mock.results[1].value;

      expect(low.connect).toHaveBeenCalledWith(mid);
      expect(mid.connect).toHaveBeenCalled();
      eq.destroy();
    });
  });

  // ===========================================================================
  // Input / output
  // ===========================================================================
  describe("input and output", () => {
    it("input is the lowshelf filter", () => {
      const eq = new ThreeBandEQ(ctx as any);
      const low = ctx.createBiquadFilter.mock.results[0].value;
      expect(eq.input).toBe(low);
      eq.destroy();
    });

    it("output is the highshelf filter", () => {
      const eq = new ThreeBandEQ(ctx as any);
      const high = ctx.createBiquadFilter.mock.results[2].value;
      expect(eq.output).toBe(high);
      eq.destroy();
    });
  });

  // ===========================================================================
  // Gain clamping ±12 dB
  // ===========================================================================
  describe("gain clamping", () => {
    it("setLow clamps to ±12 dB", () => {
      const eq = new ThreeBandEQ(ctx as any);
      const low = ctx.createBiquadFilter.mock.results[0].value;

      eq.setLow(20);
      expect(low.gain.setTargetAtTime).toHaveBeenCalledWith(
        12,
        expect.any(Number),
        expect.any(Number)
      );

      eq.setLow(-20);
      expect(low.gain.setTargetAtTime).toHaveBeenCalledWith(
        -12,
        expect.any(Number),
        expect.any(Number)
      );
      eq.destroy();
    });

    it("setMid clamps to ±12 dB", () => {
      const eq = new ThreeBandEQ(ctx as any);
      const mid = ctx.createBiquadFilter.mock.results[1].value;

      eq.setMid(15);
      expect(mid.gain.setTargetAtTime).toHaveBeenCalledWith(
        12,
        expect.any(Number),
        expect.any(Number)
      );

      eq.setMid(-15);
      expect(mid.gain.setTargetAtTime).toHaveBeenCalledWith(
        -12,
        expect.any(Number),
        expect.any(Number)
      );
      eq.destroy();
    });

    it("setHigh clamps to ±12 dB", () => {
      const eq = new ThreeBandEQ(ctx as any);
      const high = ctx.createBiquadFilter.mock.results[2].value;

      eq.setHigh(18);
      expect(high.gain.setTargetAtTime).toHaveBeenCalledWith(
        12,
        expect.any(Number),
        expect.any(Number)
      );

      eq.setHigh(-18);
      expect(high.gain.setTargetAtTime).toHaveBeenCalledWith(
        -12,
        expect.any(Number),
        expect.any(Number)
      );
      eq.destroy();
    });

    it("passes through values within range", () => {
      const eq = new ThreeBandEQ(ctx as any);
      const low = ctx.createBiquadFilter.mock.results[0].value;

      eq.setLow(6);
      expect(low.gain.setTargetAtTime).toHaveBeenCalledWith(
        6,
        expect.any(Number),
        expect.any(Number)
      );
      eq.destroy();
    });
  });

  // ===========================================================================
  // Getters
  // ===========================================================================
  describe("getters", () => {
    it("getLow returns lowshelf gain value", () => {
      const eq = new ThreeBandEQ(ctx as any);
      const low = ctx.createBiquadFilter.mock.results[0].value;
      low.gain.value = 5;
      expect(eq.getLow()).toBe(5);
      eq.destroy();
    });

    it("getMid returns peaking gain value", () => {
      const eq = new ThreeBandEQ(ctx as any);
      const mid = ctx.createBiquadFilter.mock.results[1].value;
      mid.gain.value = -3;
      expect(eq.getMid()).toBe(-3);
      eq.destroy();
    });

    it("getHigh returns highshelf gain value", () => {
      const eq = new ThreeBandEQ(ctx as any);
      const high = ctx.createBiquadFilter.mock.results[2].value;
      high.gain.value = 8;
      expect(eq.getHigh()).toBe(8);
      eq.destroy();
    });
  });

  // ===========================================================================
  // reset()
  // ===========================================================================
  describe("reset", () => {
    it("resets all bands to 0 dB", () => {
      const eq = new ThreeBandEQ(ctx as any);
      eq.setLow(6);
      eq.setMid(-4);
      eq.setHigh(10);

      eq.reset();

      const low = ctx.createBiquadFilter.mock.results[0].value;
      const mid = ctx.createBiquadFilter.mock.results[1].value;
      const high = ctx.createBiquadFilter.mock.results[2].value;

      // Last call should be 0
      const lastLow = low.gain.setTargetAtTime.mock.calls.at(-1);
      const lastMid = mid.gain.setTargetAtTime.mock.calls.at(-1);
      const lastHigh = high.gain.setTargetAtTime.mock.calls.at(-1);
      expect(lastLow![0]).toBe(0);
      expect(lastMid![0]).toBe(0);
      expect(lastHigh![0]).toBe(0);
      eq.destroy();
    });
  });

  // ===========================================================================
  // destroy
  // ===========================================================================
  describe("destroy", () => {
    it("disconnects all three filter nodes", () => {
      const eq = new ThreeBandEQ(ctx as any);
      const low = ctx.createBiquadFilter.mock.results[0].value;
      const mid = ctx.createBiquadFilter.mock.results[1].value;
      const high = ctx.createBiquadFilter.mock.results[2].value;

      eq.destroy();
      expect(low.disconnect).toHaveBeenCalled();
      expect(mid.disconnect).toHaveBeenCalled();
      expect(high.disconnect).toHaveBeenCalled();
    });
  });
});
