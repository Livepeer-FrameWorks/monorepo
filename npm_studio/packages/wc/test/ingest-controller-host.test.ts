import { describe, it, expect, vi, beforeEach } from "vitest";
import type { ReactiveControllerHost } from "lit";
import { IngestControllerHost } from "../src/controllers/ingest-controller-host.js";

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
    expect(ic.s.state).toBe("idle");
    expect(ic.s.isStreaming).toBe(false);
    expect(ic.s.isCapturing).toBe(false);
    expect(ic.s.isReconnecting).toBe(false);
    expect(ic.s.error).toBeNull();
    expect(ic.s.mediaStream).toBeNull();
    expect(ic.s.sources).toEqual([]);
    expect(ic.s.qualityProfile).toBe("broadcast");
    expect(ic.s.reconnectionState).toBeNull();
    expect(ic.s.stats).toBeNull();
    expect(ic.s.isWebCodecsActive).toBe(false);
    expect(ic.s.encoderStats).toBeNull();
  });

  it("action methods are safe to call without controller", () => {
    ic.removeSource("x");
    ic.setSourceVolume("x", 0.5);
    ic.setSourceMuted("x", true);
    ic.setSourceActive("x", true);
    ic.setPrimaryVideoSource("x");
    ic.setMasterVolume(0.8);
    expect(ic.getMasterVolume()).toBe(1);
    expect(ic.getController()).toBeNull();
  });

  it("throws on startCamera without controller", async () => {
    await expect(ic.startCamera()).rejects.toThrow("Controller not initialized");
  });

  it("throws on startStreaming without controller", async () => {
    await expect(ic.startStreaming()).rejects.toThrow("Controller not initialized");
  });

  it("custom initial profile works", () => {
    const ic2 = new IngestControllerHost(host, "professional");
    expect(ic2.s.qualityProfile).toBe("professional");
  });
});
