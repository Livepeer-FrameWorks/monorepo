import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { DeEsser } from "../src/audio/DeEsser";
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
