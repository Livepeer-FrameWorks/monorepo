import { afterEach, describe, expect, it, vi } from "vitest";

import { PlayerController } from "../src/core/PlayerController";
import type { BootTrace } from "../src/core/BootTracer";

function fakeTrace(): BootTrace {
  return {
    traceId: "t1",
    sessionId: "s1",
    contentId: "demo",
    outcome: "success",
    totalTtfMs: 1234,
    spans: {},
    resources: [],
  };
}

function stubBeacon() {
  const sendBeacon = vi.fn(() => true);
  vi.stubGlobal("navigator", { sendBeacon });
  return sendBeacon;
}

describe("boot telemetry beacon", () => {
  afterEach(() => {
    vi.unstubAllGlobals();
    vi.restoreAllMocks();
  });

  it("does not beacon when telemetry.boot is unset", () => {
    const sendBeacon = stubBeacon();
    const ctrl = new PlayerController({
      contentId: "demo",
      gatewayUrl: "https://bridge.example/graphql",
    });
    (ctrl as unknown as { beaconBootTrace: (t: BootTrace) => void }).beaconBootTrace(fakeTrace());
    expect(sendBeacon).not.toHaveBeenCalled();
  });

  it("beacons to the gateway-derived boot endpoint when opted in", () => {
    const sendBeacon = stubBeacon();
    const ctrl = new PlayerController({
      contentId: "demo",
      gatewayUrl: "https://bridge.example/graphql",
      telemetry: { boot: true },
    });
    (ctrl as unknown as { beaconBootTrace: (t: BootTrace) => void }).beaconBootTrace(fakeTrace());
    expect(sendBeacon).toHaveBeenCalledTimes(1);
    expect(sendBeacon.mock.calls[0][0]).toBe("https://bridge.example/playback/telemetry/boot");
    expect(sendBeacon.mock.calls[0][1]).toBeInstanceOf(Blob);
  });

  it("honours an explicit telemetryUrl override", () => {
    const sendBeacon = stubBeacon();
    const ctrl = new PlayerController({
      contentId: "demo",
      gatewayUrl: "https://bridge.example/graphql",
      telemetry: { boot: true },
      telemetryUrl: "https://collector.example/boot",
    });
    (ctrl as unknown as { beaconBootTrace: (t: BootTrace) => void }).beaconBootTrace(fakeTrace());
    expect(sendBeacon.mock.calls[0][0]).toBe("https://collector.example/boot");
  });
});
