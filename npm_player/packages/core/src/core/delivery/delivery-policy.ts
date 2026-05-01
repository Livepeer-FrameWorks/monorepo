import { TypedEventEmitter } from "../EventEmitter";
import { decideDeadPointRecovery } from "../mist/dead-point-recovery";
import { LiveEdgeRateController } from "../mist/live-edge-rate-controller";
import type { MistEvent, MistOnTime, MistPlayRate } from "../mist/protocol";
import type { MistMediaTransport } from "../mist/transport";
import type { BufferProbe } from "./buffer-probe";
import type { DesiredBufferModel } from "./desired-buffer";
import type { LiveCatchupConfig } from "./live-catchup";
import { nextSpeedBucket, type SpeedBucket } from "./speed-bucket";

export interface DeliveryPolicyOptions {
  transport: MistMediaTransport;
  probe: BufferProbe;
  desired: DesiredBufferModel;
  liveCatchup: LiveCatchupConfig;
  isLive: () => boolean;
  speedDownThreshold: number;
  speedUpThreshold: number;
  maxSpeedUp: number;
  minSpeedDown: number;
  serverRateMode: "none" | "vod-only" | "live-and-vod";
  localRateMode: "none" | "always";
  liveSetSpeedToggle: boolean;
  bucketHysteresis: boolean;
  pendingFastForward: boolean;
  applyLocalRate?: (rate: number) => void;
  tickSource: "on_time" | { intervalMs: number } | "external";
  now?: () => number;
}

export interface DeliveryPolicyEvents {
  speedchange: { speed: number; reason: "catchup" | "slowdown" | "normal" };
  bufferlow: { current: number; desired: number };
  bufferhigh: { current: number; desired: number };
  underrun: Record<string, never>;
  livecatchup: { fastForwardMs: number };
  bucketchange: { from: SpeedBucket; to: SpeedBucket };
  recovery_seek: { targetMs: number; reason: "at_dead_point" };
  serverratesuggest: { rate: number | "auto"; reason: "low" | "high" | "normal" };
}

export class DeliveryPolicy extends TypedEventEmitter<DeliveryPolicyEvents> {
  private bucket: SpeedBucket = "normal";
  private lastLiveCatchupAt = 0;
  private pending: {
    startedAt: number;
    bufferAt: number;
    desiredAt: number;
    serverCurrentAt: number | null;
    gotSetSpeed: boolean;
    sawFastForward: boolean;
    onTimeTicks: number;
  } | null = null;
  private readonly liveEdge: LiveEdgeRateController;
  private tickTimer: ReturnType<typeof setInterval> | null = null;

  constructor(private readonly options: DeliveryPolicyOptions) {
    super();
    this.liveEdge = new LiveEdgeRateController({
      transport: options.transport,
      config: options.liveCatchup,
      isLive: options.isLive,
    });
    if (typeof options.tickSource === "object") {
      this.tickTimer = setInterval(() => {
        this.evaluate();
      }, options.tickSource.intervalMs);
    }
  }

  evaluate(): SpeedBucket {
    const sample = this.options.probe.sample();
    const desired = sample.targetMs ?? this.options.desired.getDesiredMs();
    const current = sample.currentMs;

    if (this.pending) {
      this.evaluatePending(current, desired, sample.serverCurrentMs ?? null);
      return this.bucket;
    }

    const next = this.options.bucketHysteresis
      ? nextSpeedBucket({
          bucket: this.bucket,
          currentMs: current,
          desiredMs: desired,
          speedDownThreshold: this.options.speedDownThreshold,
          speedUpThreshold: this.options.speedUpThreshold,
        })
      : this.bucket;

    if (next !== this.bucket) {
      const from = this.bucket;
      this.bucket = next;
      this.emit("bucketchange", { from, to: next });
      this.applyBucket(next, current, desired, sample.serverCurrentMs ?? null);
    }

    this.maybeLiveCatchup(current, desired, sample);
    return this.bucket;
  }

  ingestOnTime(t: MistOnTime): void {
    if (this.options.liveSetSpeedToggle) {
      this.liveEdge.ingestOnTime(t);
    }

    if (this.pending) {
      this.pending.onTimeTicks += 1;
      this.evaluatePending(
        this.options.probe.sample().currentMs,
        this.options.desired.getDesiredMs(),
        t.current
      );
    }

    if (this.options.tickSource === "on_time") {
      this.evaluate();
    }
  }

  ingestSetSpeedAck(event: Extract<MistEvent, { type: "set_speed" }>): void {
    if (!this.pending) return;
    this.pending.gotSetSpeed = true;
    if (event.play_rate_prev === "fast-forward") {
      this.pending.sawFastForward = true;
    } else {
      this.pending = null;
    }
  }

  ingestPause(
    event: Extract<MistEvent, { type: "pause" }>,
    currentPlayRate: MistPlayRate | undefined
  ): void {
    const recovery = decideDeadPointRecovery(event, currentPlayRate);
    if (recovery.kind !== "seek_recover") {
      if (recovery.kind === "pause_only") {
        this.emit("underrun", {});
      }
      return;
    }

    if (recovery.resetSpeedToAuto) {
      this.options.transport.send({ type: "set_speed", play_rate: "auto" });
    }
    this.options.transport.send({ type: "seek", seek_time: recovery.seekToMs });
    this.emit("recovery_seek", { targetMs: recovery.seekToMs, reason: "at_dead_point" });
  }

  reset(): void {
    this.pending = null;
    this.bucket = "normal";
    this.lastLiveCatchupAt = 0;
    this.options.desired.reset();
  }

  setTuning(
    tuning: Pick<
      DeliveryPolicyOptions,
      "speedDownThreshold" | "speedUpThreshold" | "maxSpeedUp" | "minSpeedDown"
    >
  ): void {
    this.options.speedDownThreshold = tuning.speedDownThreshold;
    this.options.speedUpThreshold = tuning.speedUpThreshold;
    this.options.maxSpeedUp = tuning.maxSpeedUp;
    this.options.minSpeedDown = tuning.minSpeedDown;
  }

  destroy(): void {
    if (this.tickTimer) {
      clearInterval(this.tickTimer);
      this.tickTimer = null;
    }
    this.pending = null;
  }

  getBucket(): SpeedBucket {
    return this.bucket;
  }

  private applyBucket(
    bucket: SpeedBucket,
    current: number,
    desired: number,
    serverCurrentMs: number | null
  ): void {
    if (bucket === "low") {
      this.emit("bufferlow", { current, desired });
      if (this.options.isLive() && desired > 0 && current / desired < 0.3) {
        this.emit("underrun", {});
      }
      if (this.canFastForward(serverCurrentMs)) {
        this.options.transport.send({ type: "fast_forward", ff_add: Math.round(desired) });
        this.pending = {
          startedAt: this.now(),
          bufferAt: current,
          desiredAt: desired,
          serverCurrentAt: serverCurrentMs,
          gotSetSpeed: false,
          sawFastForward: false,
          onTimeTicks: 0,
        };
        return;
      }
      this.applyLocalRate(this.options.minSpeedDown, "slowdown");
      this.suggestServerRate(2, "low");
      return;
    }

    if (bucket === "high") {
      this.emit("bufferhigh", { current, desired });
      this.applyLocalRate(this.options.maxSpeedUp, "catchup");
      this.suggestServerRate(0.5, "high");
      return;
    }

    this.options.desired.relax();
    this.applyLocalRate(1, "normal");
    this.suggestServerRate("auto", "normal");
  }

  private evaluatePending(current: number, desired: number, serverCurrentMs: number | null): void {
    if (!this.pending) return;
    const pending = this.pending;
    const timedOut = pending.onTimeTicks >= 2 || this.now() - pending.startedAt > 2000;
    const acked = pending.gotSetSpeed && pending.sawFastForward;
    if (!acked && !timedOut) return;

    const serverIncrease =
      serverCurrentMs !== null && pending.serverCurrentAt !== null
        ? serverCurrentMs - pending.serverCurrentAt - (this.now() - pending.startedAt)
        : current - pending.bufferAt;

    const hasEnough =
      pending.bufferAt + Math.max(0, serverIncrease) >=
      pending.desiredAt * this.options.speedDownThreshold;

    if (!hasEnough) {
      this.options.desired.penalize();
      this.applyLocalRate(this.options.minSpeedDown, "slowdown");
      this.suggestServerRate(2, "low");
    }

    void desired;
    this.pending = null;
  }

  private maybeLiveCatchup(
    current: number,
    desired: number,
    sample: ReturnType<BufferProbe["sample"]>
  ): void {
    if (
      !this.options.liveCatchup.enabled ||
      !this.options.isLive() ||
      this.bucket !== "normal" ||
      sample.serverCurrentMs === undefined ||
      sample.serverEndMs === undefined ||
      sample.playRateCurr === "fast-forward" ||
      current - desired >= 1000
    ) {
      return;
    }

    const now = this.now();
    const jitter = sample.jitterMs ?? 0;
    const distance = sample.serverEndMs - sample.serverCurrentMs;
    const jitterFloor = Math.max(jitter * 1.1, jitter + 250);
    if (
      distance < this.options.liveCatchup.thresholdMs &&
      distance > jitterFloor &&
      now - this.lastLiveCatchupAt > this.options.liveCatchup.cooldownMs
    ) {
      this.lastLiveCatchupAt = now;
      this.options.transport.send({
        type: "fast_forward",
        ff_add: this.options.liveCatchup.requestMs,
      });
      this.emit("livecatchup", { fastForwardMs: this.options.liveCatchup.requestMs });
    }
  }

  private applyLocalRate(speed: number, reason: "catchup" | "slowdown" | "normal"): void {
    if (this.options.localRateMode !== "always") return;
    this.options.applyLocalRate?.(speed);
    this.emit("speedchange", { speed, reason });
  }

  private suggestServerRate(rate: MistPlayRate, reason: "low" | "high" | "normal"): void {
    const mode = this.options.serverRateMode;
    if (mode === "none") return;
    if (mode === "vod-only" && this.options.isLive()) return;
    if (rate === "fast-forward") return;
    this.emit("serverratesuggest", { rate, reason });
  }

  private now(): number {
    return this.options.now?.() ?? Date.now();
  }

  private canFastForward(serverCurrentMs: number | null): boolean {
    if (!this.options.pendingFastForward) return false;
    const sample = this.options.probe.sample();
    if (sample.playRateCurr === "fast-forward") return false;
    if (sample.serverEndMs === undefined || serverCurrentMs === null) return false;
    return serverCurrentMs < sample.serverEndMs;
  }
}
