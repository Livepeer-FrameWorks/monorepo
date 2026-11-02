import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

const tap = vi.hoisted(() => ({
  emit: (_rms: number) => {},
}));

vi.mock("../src/audio/LevelTap", () => ({
  LevelTap: class {
    input = { connect: vi.fn(), disconnect: vi.fn() };
    destroy = vi.fn();
    constructor(_context: unknown, onLevel: (rms: number) => void) {
      tap.emit = onLevel;
    }
  },
}));

import { AudioMixer } from "../src/core/AudioMixer";

type FakeGain = {
  gain: { value: number; setTargetAtTime: ReturnType<typeof vi.fn> };
  connect: ReturnType<typeof vi.fn>;
  disconnect: ReturnType<typeof vi.fn>;
};

const node = () => ({ connect: vi.fn(), disconnect: vi.fn() });

function fakeContext(gains: FakeGain[]) {
  return {
    sampleRate: 48000,
    state: "running",
    currentTime: 0,
    createGain: vi.fn(() => {
      const gain: FakeGain = { gain: { value: 1, setTargetAtTime: vi.fn() }, ...node() };
      gains.push(gain);
      return gain;
    }),
    createStereoPanner: vi.fn(() => ({ pan: { value: 0 }, ...node() })),
    createMediaStreamSource: vi.fn(() => node()),
    createAnalyser: vi.fn(() => ({
      fftSize: 0,
      smoothingTimeConstant: 0,
      frequencyBinCount: 128,
      getByteTimeDomainData: vi.fn(),
      ...node(),
    })),
    createDynamicsCompressor: vi.fn(() => ({
      threshold: { value: 0 },
      knee: { value: 0 },
      ratio: { value: 0 },
      attack: { value: 0 },
      release: { value: 0 },
      ...node(),
    })),
    createMediaStreamDestination: vi.fn(() => ({
      stream: { getAudioTracks: () => [{ kind: "audio" }] },
      channelCount: 0,
    })),
    close: vi.fn(async () => {}),
  };
}

const track = () => ({ kind: "audio", id: `t-${Math.random()}` }) as unknown as MediaStreamTrack;

describe("AudioMixer ducking", () => {
  let gains: FakeGain[];
  const original = { AudioContext: globalThis.AudioContext, MediaStream: globalThis.MediaStream };

  beforeEach(() => {
    gains = [];
    const context = fakeContext(gains);
    globalThis.AudioContext = vi.fn(function () {
      return context;
    }) as unknown as typeof AudioContext;
    globalThis.MediaStream = vi.fn(function () {
      return {};
    }) as unknown as typeof MediaStream;
  });

  afterEach(() => {
    globalThis.AudioContext = original.AudioContext;
    globalThis.MediaStream = original.MediaStream;
  });

  async function mixerWithMusic() {
    const mixer = new AudioMixer();
    await mixer.initialize();
    mixer.addSource("mic", track());
    const before = gains.length;
    mixer.addSource("music", track(), { volume: 0.5 });
    const [volume, duck] = gains.slice(before);
    mixer.setAudioProcessing({
      sidechain: { enabled: true, keySourceId: "mic", targetSourceIds: ["music"] },
    });
    return { mixer, volume, duck };
  }

  it("lowers a source under the microphone without touching its volume", async () => {
    const { volume, duck } = await mixerWithMusic();

    tap.emit(0.1);
    tap.emit(0);

    expect(volume.gain.value).toBe(0.5);
    expect(volume.gain.setTargetAtTime).not.toHaveBeenCalled();
    expect(duck.gain.setTargetAtTime).toHaveBeenNthCalledWith(1, 0.2, 0, expect.any(Number));
    expect(duck.gain.setTargetAtTime).toHaveBeenNthCalledWith(2, 1, 0, expect.any(Number));
  });

  it("brings a ducked source back when ducking is turned off", async () => {
    const { mixer, duck } = await mixerWithMusic();

    tap.emit(0.1);
    mixer.setAudioProcessing({ sidechain: { enabled: false } });

    expect(duck.gain.setTargetAtTime).toHaveBeenLastCalledWith(1, 0, expect.any(Number));
  });
});
