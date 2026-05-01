import { describe, expect, it } from "vitest";

import { normalizeLiveCatchupConfig } from "../src/core/delivery/live-catchup";
import { LiveEdgeRateController } from "../src/core/mist/live-edge-rate-controller";
import { FakeTransport } from "./_fixtures/FakeTransport";

describe("LiveEdgeRateController", () => {
  it("toggles set_speed only when crossing the live-catchup window", () => {
    const transport = new FakeTransport();
    const controller = new LiveEdgeRateController({
      transport,
      config: normalizeLiveCatchupConfig(5, { undefinedMeans: "off" }),
      isLive: () => true,
    });

    expect(controller.decide({ playRateCurr: "auto", distanceToLiveMs: 6000 })).toEqual({
      kind: "set_speed",
      playRate: 1,
    });
    expect(controller.decide({ playRateCurr: 1, distanceToLiveMs: 4000 })).toEqual({
      kind: "set_speed",
      playRate: "auto",
    });
    expect(controller.decide({ playRateCurr: "fast-forward", distanceToLiveMs: 6000 })).toEqual({
      kind: "noop",
    });

    controller.ingestOnTime({ current: 0, end: 6000, begin: 0, play_rate_curr: "auto" });
    expect(transport.sent).toEqual([{ type: "set_speed", play_rate: 1 }]);
  });

  it("forces server auto back to normal speed when live catchup is disabled", () => {
    const transport = new FakeTransport();
    const controller = new LiveEdgeRateController({
      transport,
      config: normalizeLiveCatchupConfig(false, { undefinedMeans: "off" }),
      isLive: () => true,
    });

    expect(controller.decide({ playRateCurr: "auto", distanceToLiveMs: 1000 })).toEqual({
      kind: "set_speed",
      playRate: 1,
    });
    expect(controller.decide({ playRateCurr: 1, distanceToLiveMs: 1000 })).toEqual({
      kind: "noop",
    });

    controller.ingestOnTime({ current: 0, end: 1000, begin: 0, play_rate_curr: "auto" });
    expect(transport.sent).toEqual([{ type: "set_speed", play_rate: 1 }]);
  });
});
