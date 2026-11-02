import { describe, expect, it, vi } from "vitest";
import { fireEvent, render, screen } from "@testing-library/svelte";
import type { IngestStats } from "@livepeer-frameworks/streamcrafter-core";
import StudioAudioControls from "../src/components/StudioAudioControls.svelte";
import StudioConnectionHealth from "../src/components/StudioConnectionHealth.svelte";

function controller() {
  const processing: Record<string, object> = {};
  return {
    setAudioProcessing: vi.fn((config: Record<string, object>) => {
      for (const [key, value] of Object.entries(config)) {
        processing[key] = { ...processing[key], ...value };
      }
    }),
    getAudioProcessing: () => ({ ...processing }),
    setAudioDucking: vi.fn(),
    isAudioDuckingEnabled: () => false,
  };
}

function renderControls(ctrl = controller()) {
  render(StudioAudioControls, {
    context: new Map([
      ["fw-sc-controller", { qualityProfile: "broadcast", sources: [], getController: () => ctrl }],
    ]),
  });
  return ctrl;
}

describe("StudioAudioControls", () => {
  it("turns on the noise gate and moves its threshold", async () => {
    const ctrl = renderControls();

    await fireEvent.click(screen.getByLabelText("Noise gate"));
    expect(ctrl.setAudioProcessing).toHaveBeenLastCalledWith({
      noiseGate: { enabled: true, threshold: -40 },
    });

    await fireEvent.input(screen.getByLabelText(/Gate threshold/), { target: { value: "-55" } });
    expect(ctrl.setAudioProcessing).toHaveBeenLastCalledWith({
      noiseGate: { enabled: true, threshold: -55 },
    });
  });

  it("sets the de-esser, the EQ bands and ducking", async () => {
    const ctrl = renderControls();

    await fireEvent.click(screen.getByLabelText("De-esser"));
    await fireEvent.click(screen.getByLabelText("EQ"));
    await fireEvent.input(screen.getByLabelText(/^High/), { target: { value: "4" } });
    await fireEvent.click(screen.getByLabelText(/Lower other sources/));

    expect(ctrl.setAudioProcessing).toHaveBeenCalledWith({ deEsser: { enabled: true } });
    expect(ctrl.setAudioProcessing).toHaveBeenLastCalledWith({ eq: { enabled: true, high: 4 } });
    expect(ctrl.setAudioDucking).toHaveBeenCalledWith(true);
  });
});

describe("StudioConnectionHealth", () => {
  const stats = {
    video: { packetsSent: 1000, packetsLost: 30, bitrate: 2_000_000 },
    audio: { packetsSent: 0, packetsLost: 0, bitrate: 0 },
    connection: { rtt: 40 },
  } as unknown as IngestStats;

  it("grades the connection while live", () => {
    render(StudioConnectionHealth, { props: { stats, isStreaming: true } });

    expect(screen.getByRole("status").textContent?.trim()).toBe("Connection unstable");
  });

  it("stays away when not live", () => {
    render(StudioConnectionHealth, { props: { stats, isStreaming: false } });

    expect(screen.queryByRole("status")).toBeNull();
  });
});
