import { describe, it, expect } from "vitest";
import { SyncController } from "../src/players/WebCodecsPlayer/SyncController";

function sleep(ms: number): Promise<void> {
  return new Promise((resolve) => setTimeout(resolve, ms));
}

describe("SyncController fast-forward handshake parity", () => {
  it("does not force slowdown when set_speed ack arrives without play_rate_prev", async () => {
    const ffRequests: number[] = [];
    const speedReasons: Array<"catchup" | "slowdown" | "normal"> = [];

    const sync = new SyncController({
      isLive: true,
      onFastForwardRequest: (ms) => ffRequests.push(ms),
    });
    sync.on("speedchange", ({ reason }) => speedReasons.push(reason));

    const desired = sync.getDesiredBuffer();

    // Trigger low-buffer request path.
    sync.evaluateBuffer(desired * 0.2, {
      playRateCurr: "auto",
      serverCurrentMs: 1_000,
      serverEndMs: 2_000,
      serverJitterMs: 100,
    });
    expect(ffRequests.length).toBe(1);

    // Server acknowledges set_speed but does not include play_rate_prev.
    // Upstream behavior: this should end the request state without forced slowdown.
    sync.setServerPlayRate("auto", undefined, { fromSetSpeed: true });

    await sleep(120);
    sync.evaluateBuffer(desired * 0.2, {
      playRateCurr: "auto",
      serverCurrentMs: 1_100,
      serverEndMs: 2_100,
      serverJitterMs: 100,
    });

    expect(speedReasons.includes("slowdown")).toBe(false);
  });

  it("slows down when no set_speed response arrives after fast_forward request", async () => {
    const speedReasons: Array<"catchup" | "slowdown" | "normal"> = [];

    const sync = new SyncController({
      isLive: true,
      onFastForwardRequest: () => {},
    });
    sync.on("speedchange", ({ reason }) => speedReasons.push(reason));

    const desired = sync.getDesiredBuffer();

    // First on_time: request extra buffer.
    sync.evaluateBuffer(desired * 0.2, {
      playRateCurr: "auto",
      serverCurrentMs: 5_000,
      serverEndMs: 6_000,
      serverJitterMs: 100,
    });

    // Next on_time: still no set_speed, not in fast-forward => should slow down.
    await sleep(120);
    sync.evaluateBuffer(desired * 0.2, {
      playRateCurr: "auto",
      serverCurrentMs: 5_050,
      serverEndMs: 6_050,
      serverJitterMs: 100,
    });

    expect(speedReasons.includes("slowdown")).toBe(true);
  });
});
