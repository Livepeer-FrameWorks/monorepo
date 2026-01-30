import { describe, expect, it, vi } from "vitest";

import { ReconnectionManager } from "../src/core/ReconnectionManager";

// Note: ReconnectionManager uses timers + Math.random jitter.
// We use fake timers and stub Math.random for deterministic delays.

describe("ReconnectionManager", () => {
  it("attempts reconnect and succeeds on first try", async () => {
    vi.useFakeTimers();
    vi.spyOn(Math, "random").mockReturnValue(0.5); // no jitter

    const mgr = new ReconnectionManager({ baseDelay: 1000, maxAttempts: 3 });

    const attemptStart = vi.fn();
    const attemptSuccess = vi.fn();
    mgr.on("attemptStart", attemptStart);
    mgr.on("attemptSuccess", attemptSuccess);

    const reconnect = vi.fn(async () => {});

    mgr.start(reconnect);

    // first attempt scheduled at ~1000ms
    await vi.advanceTimersByTimeAsync(1000);

    expect(reconnect).toHaveBeenCalledTimes(1);
    expect(attemptStart).toHaveBeenCalledTimes(1);
    expect(attemptSuccess).toHaveBeenCalledTimes(1);

    vi.useRealTimers();
  });

  it("retries until success", async () => {
    vi.useFakeTimers();
    vi.spyOn(Math, "random").mockReturnValue(0.5);

    const mgr = new ReconnectionManager({ baseDelay: 100, maxAttempts: 5, backoffMultiplier: 2 });

    const attemptFailed = vi.fn();
    const attemptSuccess = vi.fn();
    mgr.on("attemptFailed", attemptFailed);
    mgr.on("attemptSuccess", attemptSuccess);

    const reconnect = vi
      .fn<() => Promise<void>>()
      .mockRejectedValueOnce(new Error("nope"))
      .mockRejectedValueOnce(new Error("still nope"))
      .mockResolvedValueOnce(undefined);

    mgr.start(reconnect);

    // attempt 1 at 100ms
    await vi.advanceTimersByTimeAsync(100);
    // attempt 2 at 200ms
    await vi.advanceTimersByTimeAsync(200);
    // attempt 3 at 400ms
    await vi.advanceTimersByTimeAsync(400);

    expect(reconnect).toHaveBeenCalledTimes(3);
    expect(attemptFailed).toHaveBeenCalledTimes(2);
    expect(attemptSuccess).toHaveBeenCalledTimes(1);

    vi.useRealTimers();
  });

  it("emits exhausted after maxAttempts", async () => {
    vi.useFakeTimers();
    vi.spyOn(Math, "random").mockReturnValue(0.5);

    const mgr = new ReconnectionManager({ baseDelay: 10, maxAttempts: 2, backoffMultiplier: 2 });

    const exhausted = vi.fn();
    mgr.on("exhausted", exhausted);

    const reconnect = vi.fn(async () => {
      throw new Error("always fail");
    });

    mgr.start(reconnect);

    // attempt 1 at 10ms
    await vi.advanceTimersByTimeAsync(10);
    // attempt 2 at 20ms
    await vi.advanceTimersByTimeAsync(20);
    // attempt 3 would trigger exhaustion immediately in scheduleNextAttempt
    // (called after attempt 2 failure)

    expect(reconnect).toHaveBeenCalledTimes(2);
    expect(exhausted).toHaveBeenCalledTimes(1);

    vi.useRealTimers();
  });
});
