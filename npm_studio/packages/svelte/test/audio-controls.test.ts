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
    connection: { rtt: 40, packetLossPct: 3 },
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

describe("AdvancedPanel", () => {
  it("leaves the browser processing toggles to the Audio section", async () => {
    const { default: AdvancedPanel } = await import("../src/components/AdvancedPanel.svelte");
    render(AdvancedPanel, {
      props: {
        isOpen: true,
        onClose: () => {},
        state: "capturing",
        qualityProfile: "broadcast",
        sources: [],
        stats: null,
        masterVolume: 1,
        onMasterVolumeChange: () => {},
        audioLevel: 0,
        audioMixingEnabled: true,
        error: null,
      } as never,
    });

    expect(screen.getByText("Sample Rate")).toBeTruthy();
    expect(screen.queryByText("Echo Cancellation")).toBeNull();
  });

  it("shows the encoder config read-only and marks the overrides", async () => {
    const { default: AdvancedPanel } = await import("../src/components/AdvancedPanel.svelte");
    const { container } = render(AdvancedPanel, {
      props: {
        isOpen: true,
        onClose: () => {},
        ingestState: "capturing",
        qualityProfile: "broadcast",
        sources: [],
        stats: null,
        masterVolume: 1,
        onMasterVolumeChange: () => {},
        audioLevel: 0,
        audioMixingEnabled: true,
        error: null,
        encoderOverrides: { video: { width: 1280, height: 720 } },
      },
    });
    await fireEvent.click(screen.getByText("Info"));

    expect(container.querySelector("select")).toBeNull();
    expect(screen.queryByText("Reset to Profile")).toBeNull();
    const overridden = [...container.querySelectorAll("[data-overridden]")].map((row) =>
      row.textContent?.replace(/\s+/g, "")
    );
    expect(overridden).toEqual(["Resolution1280×720Overridden"]);
    expect(screen.getByText("2.5 Mbps")).toBeTruthy();
    expect(screen.getByText("Change the encoder in Settings")).toBeTruthy();
  });
});
