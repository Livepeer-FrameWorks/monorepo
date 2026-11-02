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
