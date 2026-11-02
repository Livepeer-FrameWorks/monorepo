import { afterEach, describe, expect, it, vi } from "vitest";
import { AudioMixer } from "../src/core/AudioMixer";
import { IngestControllerV2 } from "../src/core/IngestControllerV2";

describe("IngestControllerV2 defaults", () => {
  const controllers: IngestControllerV2[] = [];
  afterEach(() => {
    controllers.splice(0).forEach((controller) => controller.destroy());
  });

  function create(config: { audioMixing?: boolean } = {}) {
    const controller = new IngestControllerV2({
      whipUrl: "https://edge.example/webrtc/key",
      useWebCodecs: false,
      ...config,
    });
    controllers.push(controller);
    return controller;
  }

  it("mixes audio through the mixer unless told not to", () => {
    const setMasterVolume = vi.spyOn(AudioMixer.prototype, "setMasterVolume");

    create().setMasterVolume(0.5);
    expect(setMasterVolume).toHaveBeenCalledWith(0.5);

    setMasterVolume.mockClear();
    create({ audioMixing: false }).setMasterVolume(0.5);
    expect(setMasterVolume).not.toHaveBeenCalled();
  });
});

describe("IngestControllerV2 audio processing", () => {
  function controllerWithMixer() {
    const controller = new IngestControllerV2({
      whipUrl: "https://edge.example/webrtc/key",
      useWebCodecs: false,
    });
    let running = false;
    vi.spyOn(AudioMixer.prototype, "getState").mockImplementation(() =>
      running ? "running" : null
    );
    vi.spyOn(AudioMixer.prototype, "initialize").mockImplementation(async () => {
      running = true;
    });
    vi.spyOn(AudioMixer.prototype, "hasSource").mockReturnValue(true);
    const applied = vi
      .spyOn(AudioMixer.prototype, "setAudioProcessing")
      .mockImplementation(() => {});
    const internals = controller as unknown as {
      ensureAudioMixer(): Promise<void>;
      sources: Map<string, { id: string; type: string; stream: unknown }>;
    };
    return { controller, applied, internals };
  }

  it("keeps settings made before capture and applies them once the mixer runs", async () => {
    const { controller, applied, internals } = controllerWithMixer();

    controller.setAudioProcessing({ noiseGate: { enabled: true, threshold: -50 } });
    controller.setAudioProcessing({ eq: { enabled: true, low: 3 } });
    applied.mockClear();
    await internals.ensureAudioMixer();

    expect(applied).toHaveBeenCalledWith({
      noiseGate: { enabled: true, threshold: -50 },
      eq: { enabled: true, low: 3 },
    });
    expect(controller.getAudioProcessing().noiseGate?.threshold).toBe(-50);
    controller.destroy();
  });

  it("ducks the other sources under the first camera and follows new sources", async () => {
    const { controller, applied, internals } = controllerWithMixer();
    const source = (id: string, type: string) => ({ id, type, stream: { getTracks: () => [] } });
    internals.sources.set("camera-1", source("camera-1", "camera"));
    internals.sources.set("media-1", source("media-1", "media"));
    await internals.ensureAudioMixer();

    controller.setAudioDucking(true);
    expect(applied).toHaveBeenLastCalledWith({
      sidechain: { enabled: true, keySourceId: "camera-1", targetSourceIds: ["media-1"] },
    });

    internals.sources.set("screen-1", source("screen-1", "screen"));
    controller.emit("sourceAdded", { source: internals.sources.get("screen-1") } as never);
    expect(applied).toHaveBeenLastCalledWith({
      sidechain: {
        enabled: true,
        keySourceId: "camera-1",
        targetSourceIds: ["media-1", "screen-1"],
      },
    });

    controller.setAudioDucking(false);
    expect(applied).toHaveBeenLastCalledWith({ sidechain: { enabled: false } });
    controller.destroy();
  });
});
