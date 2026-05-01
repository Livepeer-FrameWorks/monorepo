import { afterEach, describe, expect, it, vi } from "vitest";

import { DeliveryPolicy } from "../src/core/delivery/delivery-policy";
import { DesiredBufferModel } from "../src/core/delivery/desired-buffer";
import { normalizeLiveCatchupConfig } from "../src/core/delivery/live-catchup";
import { FakeProbe } from "./_fixtures/FakeProbe";
import { FakeTransport } from "./_fixtures/FakeTransport";

describe("DeliveryPolicy", () => {
  afterEach(() => {
    vi.useRealTimers();
  });

  function createPolicy(opts: Partial<ConstructorParameters<typeof DeliveryPolicy>[0]> = {}) {
    let now = 0;
    const transport = new FakeTransport();
    const probe = new FakeProbe();
    const desired = new DesiredBufferModel({ baseMs: 1000 });
    const localRate = vi.fn();
    const policy = new DeliveryPolicy({
      transport,
      probe,
      desired,
      liveCatchup: normalizeLiveCatchupConfig(5, { undefinedMeans: "off" }),
      isLive: () => true,
      speedDownThreshold: 0.6,
      speedUpThreshold: 1.4,
      maxSpeedUp: 1.02,
      minSpeedDown: 0.98,
      serverRateMode: "none",
      localRateMode: "always",
      liveSetSpeedToggle: true,
      bucketHysteresis: true,
      pendingFastForward: true,
      applyLocalRate: localRate,
      tickSource: "external",
      now: () => now,
      ...opts,
    });
    return { policy, transport, probe, desired, localRate, advance: (ms: number) => (now += ms) };
  }

  it("emits low bucket state and sends pending fast-forward", () => {
    const { policy, transport, probe, localRate } = createPolicy();
    const low = vi.fn();
    policy.on("bufferlow", low);
    probe.current = { currentMs: 500, serverCurrentMs: 1000, serverEndMs: 2000 };

    expect(policy.evaluate()).toBe("low");
    expect(low).toHaveBeenCalledWith({ current: 500, desired: 1000 });
    expect(localRate).not.toHaveBeenCalled();
    expect(transport.sent).toContainEqual({ type: "fast_forward", ff_add: 1000 });
  });

  it("does not fast-forward when the server range is unknown", () => {
    const { policy, transport, probe, localRate } = createPolicy();
    probe.current = { currentMs: 500 };

    expect(policy.evaluate()).toBe("low");
    expect(transport.sent).not.toContainEqual({ type: "fast_forward", ff_add: 1000 });
    expect(localRate).toHaveBeenCalledWith(0.98);
  });

  it("emits underrun for severe live low-buffer state", () => {
    const { policy, probe } = createPolicy();
    const underrun = vi.fn();
    policy.on("underrun", underrun);
    probe.current = { currentMs: 250, serverCurrentMs: 1000, serverEndMs: 2000 };

    policy.evaluate();

    expect(underrun).toHaveBeenCalledWith({});
  });

  it("waits two on_time ticks before penalizing a pending fast-forward without an ack", () => {
    const { policy, probe, desired, advance } = createPolicy();
    probe.current = { currentMs: 500, serverCurrentMs: 1000, serverEndMs: 2000 };
    policy.evaluate();

    advance(500);
    probe.current = { currentMs: 520, serverCurrentMs: 1100, serverEndMs: 2000 };
    policy.ingestOnTime({ current: 1100, end: 5000, begin: 0 });

    expect(desired.getKeepAwayExtraMs()).toBe(0);

    advance(500);
    probe.current = { currentMs: 540, serverCurrentMs: 1200, serverEndMs: 2000 };
    policy.ingestOnTime({ current: 1200, end: 5000, begin: 0 });

    expect(desired.getKeepAwayExtraMs()).toBe(100);
  });

  it("penalizes desired buffer when pending fast-forward ack does not refill enough", () => {
    const { policy, probe, desired, advance } = createPolicy();
    probe.current = { currentMs: 500, serverCurrentMs: 1000, serverEndMs: 2000 };
    policy.evaluate();

    policy.ingestSetSpeedAck({ type: "set_speed", play_rate_prev: "fast-forward" });
    advance(500);
    probe.current = { currentMs: 520, serverCurrentMs: 1100, serverEndMs: 2000 };
    policy.ingestOnTime({ current: 1100, end: 5000, begin: 0 });

    expect(desired.getKeepAwayExtraMs()).toBe(100);
  });

  it("emits live catchup when close to live and buffer is not ahead", () => {
    const { policy, transport, probe, advance } = createPolicy();
    const livecatchup = vi.fn();
    policy.on("livecatchup", livecatchup);
    advance(3000);
    probe.current = {
      currentMs: 900,
      serverCurrentMs: 9000,
      serverEndMs: 12000,
      jitterMs: 100,
    };

    policy.evaluate();

    expect(transport.sent).toContainEqual({ type: "fast_forward", ff_add: 5000 });
    expect(livecatchup).toHaveBeenCalledWith({ fastForwardMs: 5000 });
  });

  it("honors server-rate capability flags for VoD", () => {
    const { policy, probe } = createPolicy({
      isLive: () => false,
      serverRateMode: "vod-only",
      localRateMode: "none",
      pendingFastForward: false,
    });
    const suggested = vi.fn();
    policy.on("serverratesuggest", suggested);
    probe.current = { currentMs: 1500 };

    policy.evaluate();

    expect(suggested).toHaveBeenCalledWith({ rate: 0.5, reason: "high" });
  });

  it("drives dead-point recovery through shared transport commands", () => {
    const { policy, transport } = createPolicy();
    const recovery = vi.fn();
    policy.on("recovery_seek", recovery);

    policy.ingestPause({ type: "pause", reason: "at_dead_point", begin: 5000 }, 0.98);

    expect(transport.sent).toEqual([
      { type: "set_speed", play_rate: "auto" },
      { type: "seek", seek_time: 6000 },
    ]);
    expect(recovery).toHaveBeenCalledWith({ targetMs: 6000, reason: "at_dead_point" });
  });

  it("runs interval ticks when tickSource is configured with an interval", () => {
    vi.useFakeTimers();
    const { policy, probe } = createPolicy({ tickSource: { intervalMs: 1000 } });
    const low = vi.fn();
    policy.on("bufferlow", low);
    probe.current = { currentMs: 500 };

    vi.advanceTimersByTime(1000);

    expect(low).toHaveBeenCalledWith({ current: 500, desired: 1000 });
    policy.destroy();
  });
});
