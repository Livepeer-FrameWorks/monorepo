import { describe, it, expect, beforeEach, afterEach, vi } from "vitest";
import { AudioGainController } from "../src/core/AudioGainController";

// Minimal Web Audio fakes — enough to observe gain writes, node wiring, and
// context lifecycle without a real AudioContext.
class FakeGainNode {
  gain = { value: 1 };
  connect = vi.fn();
  disconnect = vi.fn();
}

class FakeMediaElementSource {
  connect = vi.fn();
  constructor(public readonly media: unknown) {}
}

class FakeAudioContext {
  static instances: FakeAudioContext[] = [];
  destination = {};
  closed = false;
  createGain = vi.fn(() => new FakeGainNode());
  createMediaElementSource = vi.fn(
    (media: unknown) => new FakeMediaElementSource(media) as unknown as MediaElementAudioSourceNode
  );
  close = vi.fn(async () => {
    this.closed = true;
  });
  constructor() {
    FakeAudioContext.instances.push(this);
  }
}

function fakeVideo(): HTMLVideoElement {
  return {} as HTMLVideoElement;
}

beforeEach(() => {
  FakeAudioContext.instances = [];
  vi.stubGlobal("AudioContext", FakeAudioContext);
  vi.stubGlobal("window", { AudioContext: FakeAudioContext });
});

afterEach(() => {
  vi.unstubAllGlobals();
});

describe("isSupported", () => {
  it("true when AudioContext exists", () => {
    expect(new AudioGainController().isSupported()).toBe(true);
  });

  it("false when neither AudioContext nor webkitAudioContext exist", () => {
    vi.stubGlobal("AudioContext", undefined);
    vi.stubGlobal("window", {});
    expect(new AudioGainController().isSupported()).toBe(false);
  });
});

describe("setGain — clamping", () => {
  it("clamps to [0, maxGain] with the default 3.0 ceiling", () => {
    const c = new AudioGainController();
    c.setGain(5);
    expect(c.getGain()).toBe(3);
    c.setGain(-1);
    expect(c.getGain()).toBe(0);
    c.setGain(1.5);
    expect(c.getGain()).toBe(1.5);
  });

  it("honors a custom maxGain ceiling", () => {
    const c = new AudioGainController({ maxGain: 2 });
    c.setGain(10);
    expect(c.getGain()).toBe(2);
  });

  it("writes the clamped value through to the live GainNode", () => {
    const c = new AudioGainController();
    c.attach(fakeVideo());
    c.setGain(2.5);
    const ctx = FakeAudioContext.instances[0];
    const gainNode = ctx.createGain.mock.results[0].value as FakeGainNode;
    expect(gainNode.gain.value).toBe(2.5);
  });
});

describe("attach — wiring and source dedup", () => {
  it("lazily creates the AudioContext on first attach and wires source→gain→destination", () => {
    const c = new AudioGainController();
    expect(FakeAudioContext.instances).toHaveLength(0);
    c.attach(fakeVideo());
    expect(FakeAudioContext.instances).toHaveLength(1);
    const ctx = FakeAudioContext.instances[0];
    expect(ctx.createMediaElementSource).toHaveBeenCalledOnce();
    const gainNode = ctx.createGain.mock.results[0].value as FakeGainNode;
    expect(gainNode.connect).toHaveBeenCalledWith(ctx.destination);
  });

  it("calls createMediaElementSource only once per element across controllers", () => {
    // The module-level WeakMap exists because createMediaElementSource throws
    // if called twice on the same element. Reusing the cached node is the contract.
    const video = fakeVideo();
    new AudioGainController().attach(video);
    new AudioGainController().attach(video);
    const totalCalls = FakeAudioContext.instances.reduce(
      (n, ctx) => n + ctx.createMediaElementSource.mock.calls.length,
      0
    );
    expect(totalCalls).toBe(1);
  });

  it("is a no-op when re-attaching the same video to one controller", () => {
    const c = new AudioGainController();
    const video = fakeVideo();
    c.attach(video);
    c.attach(video);
    expect(FakeAudioContext.instances).toHaveLength(1);
  });
});

describe("destroy — guards", () => {
  it("closes the context and ignores subsequent setGain/attach", () => {
    const c = new AudioGainController();
    c.attach(fakeVideo());
    const ctx = FakeAudioContext.instances[0];
    c.destroy();
    expect(ctx.close).toHaveBeenCalledOnce();

    c.setGain(2);
    expect(c.getGain()).not.toBe(2); // setGain ignored after destroy
    c.attach(fakeVideo());
    expect(FakeAudioContext.instances).toHaveLength(1); // attach ignored after destroy
  });

  it("is idempotent", () => {
    const c = new AudioGainController();
    c.attach(fakeVideo());
    const ctx = FakeAudioContext.instances[0];
    c.destroy();
    c.destroy();
    expect(ctx.close).toHaveBeenCalledOnce();
  });
});
