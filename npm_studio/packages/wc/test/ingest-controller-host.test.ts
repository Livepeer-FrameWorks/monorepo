import { describe, it, expect, vi, beforeEach } from "vitest";
import type { ReactiveControllerHost } from "lit";

vi.mock("@livepeer-frameworks/streamcrafter-core", async (importOriginal) => {
  const actual = await importOriginal<Record<string, unknown>>();
  return {
    ...actual,
    detectCapabilities: vi.fn().mockReturnValue({ recommended: "webcodecs" }),
    isWebCodecsEncodingPathSupported: vi.fn().mockReturnValue(false),
  };
});

import { IngestControllerHost } from "../src/controllers/ingest-controller-host.js";
import {
  STREAMCRAFTER_WRAPPER_CONTROLLER_NOT_INITIALIZED_ERROR,
  STREAMCRAFTER_WRAPPER_PARITY_ACTION_METHODS,
  STREAMCRAFTER_WRAPPER_PARITY_INITIAL_STATE,
  STREAMCRAFTER_WRAPPER_PARITY_INITIAL_STATE_WC_EXT,
} from "../../test-contract/streamcrafter-wrapper-contract";

function createMockHost(): ReactiveControllerHost & HTMLElement {
  const host = document.createElement("div") as unknown as ReactiveControllerHost & HTMLElement;
  (host as any).addController = vi.fn();
  (host as any).requestUpdate = vi.fn();
  (host as any).removeController = vi.fn();
  (host as any).updateComplete = Promise.resolve(true);
  return host;
}

describe("IngestControllerHost", () => {
  let host: ReturnType<typeof createMockHost>;
  let ic: IngestControllerHost;

  beforeEach(() => {
    host = createMockHost();
    ic = new IngestControllerHost(host);
  });

  it("registers itself with the host", () => {
    expect((host as any).addController).toHaveBeenCalledWith(ic);
  });

  it("has correct initial state", () => {
    for (const [key, expected] of Object.entries(STREAMCRAFTER_WRAPPER_PARITY_INITIAL_STATE)) {
      expect(ic.s[key as keyof typeof ic.s]).toEqual(expected);
    }
    for (const [key, expected] of Object.entries(
      STREAMCRAFTER_WRAPPER_PARITY_INITIAL_STATE_WC_EXT
    )) {
      expect(ic.s[key as keyof typeof ic.s]).toEqual(expected);
    }
  });

  it("action methods are safe to call without controller", async () => {
    for (const actionName of STREAMCRAFTER_WRAPPER_PARITY_ACTION_METHODS) {
      expect(typeof ic[actionName]).toBe("function");
    }

    ic.removeSource("x");
    ic.setSourceVolume("x", 0.5);
    ic.setSourceMuted("x", true);
    ic.setSourceActive("x", true);
    ic.setPrimaryVideoSource("x");
    ic.setMasterVolume(0.8);
    await ic.stopCapture();
    await ic.stopStreaming();
    await ic.getDevices();
    await ic.switchVideoDevice("video-1");
    await ic.switchAudioDevice("audio-1");
    await ic.getStats();
    ic.setUseWebCodecs(true);
    ic.setEncoderOverrides({ maxBitrate: 2_000_000 });
    expect(ic.getMasterVolume()).toBe(1);
    expect(ic.getController()).toBeNull();
  });

  it("throws on startCamera without controller", async () => {
    await expect(ic.startCamera()).rejects.toThrow(
      STREAMCRAFTER_WRAPPER_CONTROLLER_NOT_INITIALIZED_ERROR
    );
  });

  it("throws on startStreaming without controller", async () => {
    await expect(ic.startStreaming()).rejects.toThrow(
      STREAMCRAFTER_WRAPPER_CONTROLLER_NOT_INITIALIZED_ERROR
    );
  });

  it("custom initial profile works", () => {
    const ic2 = new IngestControllerHost(host, "professional");
    expect(ic2.s.qualityProfile).toBe("professional");
  });
});
