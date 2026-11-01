import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { DeEsser } from "../src/audio/DeEsser";
import { LevelTap } from "../src/audio/LevelTap";
import { NoiseGate } from "../src/audio/NoiseGate";
import { SidechainDucker } from "../src/audio/SidechainDucker";

const param = () => ({ value: 1, setTargetAtTime: vi.fn() });
const node = () => ({ connect: vi.fn(), disconnect: vi.fn() });

const tapNodes: { port: { onmessage: ((e: { data: number }) => void) | null } }[] = [];

class FakeAudioWorkletNode {
  port = { onmessage: null };
  disconnect = vi.fn();
  constructor() {
    tapNodes.push(this);
  }
}

function fakeContext() {
  return {
    currentTime: 1,
    audioWorklet: { addModule: vi.fn(async () => {}) },
    createGain: vi.fn(() => ({ ...node(), gain: param() })),
    createAnalyser: vi.fn(() => ({ ...node(), fftSize: 256, getFloatTimeDomainData: vi.fn() })),
    createBiquadFilter: vi.fn(() => ({
      ...node(),
      type: "",
      frequency: param(),
      Q: param(),
      gain: param(),
    })),
  } as unknown as AudioContext;
}

async function report(level: number) {
  await vi.waitFor(() => expect(tapNodes).toHaveLength(1));
  tapNodes[0].port.onmessage?.({ data: level });
}

describe("audio processors while the tab is in the background", () => {
  beforeEach(() => {
    tapNodes.length = 0;
    vi.stubGlobal("AudioWorkletNode", FakeAudioWorkletNode);
    // Background tabs stop animation frames, so the loop must not depend on them.
    vi.stubGlobal("requestAnimationFrame", vi.fn());
    vi.stubGlobal("cancelAnimationFrame", vi.fn());
    URL.createObjectURL = vi.fn(() => "blob:level-tap");
    URL.revokeObjectURL = vi.fn();
  });
  afterEach(() => {
    vi.unstubAllGlobals();
  });

  it("opens the noise gate on signal", async () => {
    const gate = new NoiseGate(fakeContext());
    await report(0.5);
    expect(gate.currentState).toBe("open");
    gate.destroy();
  });

  it("reduces sibilance", async () => {
    const context = fakeContext();
    const deEsser = new DeEsser(context, { threshold: -20 });
    await report(0.5);
    const shelf = vi.mocked(context.createBiquadFilter).mock.results[1].value;
    expect(shelf.gain.setTargetAtTime.mock.calls[0][0]).toBeLessThan(0);
    deEsser.destroy();
  });

  it("ducks target sources while the key signal is active", async () => {
    const ducker = new SidechainDucker(fakeContext(), { ratio: 0.2 });
    const target = { gain: param() } as unknown as GainNode;
    ducker.addTarget("music", target);
    await report(0.5);
    expect(target.gain.setTargetAtTime).toHaveBeenCalledWith(0.2, 1, expect.any(Number));
    ducker.destroy();
  });
});

describe("the level tap processor", () => {
  afterEach(() => {
    vi.unstubAllGlobals();
  });

  /** Run the source LevelTap loads into the audio thread. */
  async function processor() {
    let source: Blob | undefined;
    vi.stubGlobal("AudioWorkletNode", FakeAudioWorkletNode);
    URL.createObjectURL = vi.fn((blob: Blob) => {
      source = blob;
      return "blob:level-tap";
    }) as typeof URL.createObjectURL;
    URL.revokeObjectURL = vi.fn();
    const tap = new LevelTap(fakeContext(), () => {});
    await vi.waitFor(() => expect(source).toBeDefined());
    tap.destroy();

    const text = await source!.text();
    const levels: number[] = [];
    class AudioWorkletProcessor {
      port = { postMessage: (level: number) => levels.push(level) };
    }
    let Processor: new () => { process(inputs: Float32Array[][]): boolean } = class {
      process() {
        return true;
      }
    };
    new Function("AudioWorkletProcessor", "registerProcessor", text)(
      AudioWorkletProcessor,
      (_name: string, registered: typeof Processor) => {
        Processor = registered;
      }
    );
    return { node: new Processor(), levels };
  }

  it("hears a stereo source that only has signal on the right", async () => {
    const { node, levels } = await processor();
    const silent = new Float32Array(512);
    const loud = new Float32Array(512).fill(0.5);

    node.process([[silent, loud]]);

    expect(levels).toHaveLength(1);
    expect(levels[0]).toBeGreaterThan(0.3);
  });

  it("reads a mono source at its own level", async () => {
    const { node, levels } = await processor();

    node.process([[new Float32Array(512).fill(0.5)]]);

    expect(levels[0]).toBeCloseTo(0.5);
  });

  it("reads the same signal on both channels at that level", async () => {
    const { node, levels } = await processor();
    const signal = new Float32Array(512).fill(0.5);

    node.process([[signal, signal]]);

    expect(levels[0]).toBeCloseTo(0.5);
  });

  it("survives an input with nothing connected", async () => {
    const { node, levels } = await processor();

    expect(node.process([[]])).toBe(true);
    expect(levels).toEqual([]);
  });
});

describe("audio processors without AudioWorklet", () => {
  afterEach(() => {
    vi.useRealTimers();
    vi.unstubAllGlobals();
  });

  it("measures the level on a timer", async () => {
    vi.useFakeTimers();
    const context = fakeContext();
    vi.mocked(context.audioWorklet.addModule).mockRejectedValue(new Error("blocked by CSP"));
    URL.createObjectURL = vi.fn(() => "blob:level-tap");
    URL.revokeObjectURL = vi.fn();
    vi.mocked(context.createAnalyser).mockImplementation(
      () =>
        ({
          ...node(),
          fftSize: 256,
          getFloatTimeDomainData: (data: Float32Array) => data.fill(0.5),
        }) as unknown as AnalyserNode
    );

    const gate = new NoiseGate(context);
    await vi.advanceTimersByTimeAsync(100);

    expect(gate.currentState).toBe("open");
    gate.destroy();
  });
});
