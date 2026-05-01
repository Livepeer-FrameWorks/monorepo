import type { LiveCatchupConfig } from "../delivery/live-catchup";
import type { MistOnTime, MistPlayRate } from "./protocol";
import type { MistMediaTransport } from "./transport";

export type LiveEdgeRateDecision = { kind: "noop" } | { kind: "set_speed"; playRate: 1 | "auto" };

export class LiveEdgeRateController {
  private config: LiveCatchupConfig;

  constructor(
    private readonly opts: {
      transport: MistMediaTransport;
      config: LiveCatchupConfig;
      isLive: () => boolean;
    }
  ) {
    this.config = opts.config;
  }

  ingestOnTime(t: MistOnTime): void {
    if (!this.opts.isLive() || t.play_rate_curr === undefined) return;
    const decision = this.decide({
      playRateCurr: t.play_rate_curr,
      distanceToLiveMs: t.end - t.current,
    });
    if (decision.kind === "set_speed") {
      this.opts.transport.send({ type: "set_speed", play_rate: decision.playRate });
    }
  }

  decide(input: { playRateCurr: MistPlayRate; distanceToLiveMs: number }): LiveEdgeRateDecision {
    if (input.playRateCurr === "fast-forward") {
      return { kind: "noop" };
    }

    if (!this.config.enabled) {
      return input.playRateCurr === "auto" ? { kind: "set_speed", playRate: 1 } : { kind: "noop" };
    }

    if (input.playRateCurr === "auto" && input.distanceToLiveMs > this.config.thresholdMs) {
      return { kind: "set_speed", playRate: 1 };
    }

    if (input.playRateCurr === 1 && input.distanceToLiveMs < this.config.thresholdMs) {
      return { kind: "set_speed", playRate: "auto" };
    }

    return { kind: "noop" };
  }

  setConfig(config: LiveCatchupConfig): void {
    this.config = config;
  }
}
