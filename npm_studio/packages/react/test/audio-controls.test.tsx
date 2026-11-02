import React from "react";
import { afterEach, describe, expect, it, vi } from "vitest";
import { cleanup, fireEvent, render, screen } from "@testing-library/react";
import type { IngestStats } from "@livepeer-frameworks/streamcrafter-core";
import { StreamCrafterProvider } from "../src/context/StreamCrafterContext";
import { StudioAudioControls } from "../src/components/StudioAudioControls";
import { StudioConnectionHealth } from "../src/components/StudioConnectionHealth";

afterEach(cleanup);

function controller() {
  let processing: Record<string, unknown> = {};
  return {
    setAudioProcessing: vi.fn((config: Record<string, object>) => {
      for (const [key, value] of Object.entries(config)) {
        processing[key] = { ...(processing[key] as object), ...value };
      }
    }),
    getAudioProcessing: () => processing,
    setAudioDucking: vi.fn(),
    isAudioDuckingEnabled: () => false,
  };
}

function renderControls(ctrl = controller(), extra: Record<string, unknown> = {}) {
  render(
    <StreamCrafterProvider
      value={
        {
          qualityProfile: "broadcast",
          sources: [],
          getController: () => ctrl,
          ...extra,
        } as never
      }
    >
      <StudioAudioControls />
    </StreamCrafterProvider>
  );
  return ctrl;
}

describe("StudioAudioControls", () => {
  it("turns on the noise gate and moves its threshold", () => {
    const ctrl = renderControls();

    fireEvent.click(screen.getByLabelText("Noise gate"));
    expect(ctrl.setAudioProcessing).toHaveBeenLastCalledWith({
      noiseGate: { enabled: true, threshold: -40 },
    });

    fireEvent.change(screen.getByLabelText(/Gate threshold/), { target: { value: "-55" } });
    expect(ctrl.setAudioProcessing).toHaveBeenLastCalledWith({
      noiseGate: { enabled: true, threshold: -55 },
    });
  });

  it("sets the de-esser, the EQ bands and ducking", () => {
    const ctrl = renderControls();

    fireEvent.click(screen.getByLabelText("De-esser"));
    fireEvent.click(screen.getByLabelText("EQ"));
    fireEvent.change(screen.getByLabelText(/^High/), { target: { value: "4" } });
    fireEvent.click(screen.getByLabelText(/Lower other sources/));

    expect(ctrl.setAudioProcessing).toHaveBeenCalledWith({ deEsser: { enabled: true } });
    expect(ctrl.setAudioProcessing).toHaveBeenLastCalledWith({ eq: { enabled: true, high: 4 } });
    expect(ctrl.setAudioDucking).toHaveBeenCalledWith(true);
  });

  it("applies browser processing to every microphone", () => {
    const applyConstraints = vi.fn(async () => {});
    renderControls(controller(), {
      sources: [{ id: "cam", stream: { getAudioTracks: () => [{ applyConstraints }] } }],
    });

    const toggle = screen.getByLabelText("Echo Cancellation") as HTMLInputElement;
    const before = toggle.checked;
    fireEvent.click(toggle);

    expect(toggle.checked).toBe(!before);
    expect(applyConstraints).toHaveBeenCalledWith(
      expect.objectContaining({ echoCancellation: !before })
    );
  });
});

describe("StudioConnectionHealth", () => {
  const stats = (lossPct: number, rtt: number) =>
    ({
      video: { packetsSent: 1000, packetsLost: 0, bitrate: 2_000_000 },
      audio: { packetsSent: 0, packetsLost: 0, bitrate: 0 },
      connection: { rtt, packetLossPct: lossPct },
    }) as unknown as IngestStats;

  it("grades the connection while live", () => {
    render(<StudioConnectionHealth stats={stats(0, 40)} isStreaming />);
    const pill = screen.getByRole("status");

    expect(pill.textContent).toBe("Connection good");
    expect(pill.title).toBe("2000 kbps, 0.0% packet loss, 40 ms round trip");
  });

  it("warns about a poor connection and stays away when not live", () => {
    const { rerender } = render(<StudioConnectionHealth stats={stats(10, 40)} isStreaming />);
    expect(screen.getByRole("status").textContent).toBe("Connection poor");

    rerender(<StudioConnectionHealth stats={stats(10, 40)} isStreaming={false} />);
    expect(screen.queryByRole("status")).toBeNull();
  });
});
