import { afterEach, describe, expect, it, vi } from "vitest";

const refreshAuthSession = vi.fn();
vi.mock("$lib/auth/refresh", () => ({ refreshAuthSession }));
const endSession = vi.fn(async () => {});
vi.mock("$lib/houdini/session-end", () => ({
  ACCOUNT_DEACTIVATED_CLOSE_REASON: "account deactivated",
  CLOSE_FORBIDDEN: 4403,
  endSession,
}));

const { realtimeClientOptions } = await import("./realtime-ws");

function closeEvent(code: number, reason: string) {
  return { code, reason, wasClean: true } as CloseEvent;
}

afterEach(() => {
  vi.useRealTimers();
  refreshAuthSession.mockReset();
  endSession.mockClear();
});

describe("realtime WebSocket session end", () => {
  it("signs out without retrying when the gateway closes for a deactivated account", () => {
    const options = realtimeClientOptions("wss://bridge.test/graphql/ws");
    const close = closeEvent(4403, "account deactivated");
    options.on!.closed!(close);
    expect(options.shouldRetry!(close)).toBe(false);
    expect(endSession).toHaveBeenCalledWith("deactivated");
    expect(refreshAuthSession).not.toHaveBeenCalled();
  });

  it("stops retrying and signs out when a 4403 close is followed by a refused refresh", async () => {
    refreshAuthSession.mockResolvedValue("unauthorized");
    const options = realtimeClientOptions("wss://bridge.test/graphql/ws");
    const close = closeEvent(4403, "credential revoked");
    options.on!.closed!(close);
    expect(options.shouldRetry!(close)).toBe(true);
    await expect(options.retryWait!(0)).rejects.toThrow("session ended");
    expect(endSession).toHaveBeenCalledWith("expired");
    expect(options.shouldRetry!(new Error("session ended"))).toBe(false);
  });

  it("keeps reconnecting after a refused refresh that followed a network close", async () => {
    vi.useFakeTimers();
    refreshAuthSession.mockResolvedValue("unauthorized");
    const options = realtimeClientOptions("wss://bridge.test/graphql/ws");
    options.on!.closed!(closeEvent(1006, ""));
    const waiting = options.retryWait!(0);
    await vi.runAllTimersAsync();
    await expect(waiting).resolves.toBeUndefined();
    expect(endSession).not.toHaveBeenCalled();
  });
});

describe("realtime WebSocket options", () => {
  it("sends no token in connectionParams, so a reconnect authenticates from the cookie", async () => {
    const options = realtimeClientOptions("wss://bridge.test/graphql/ws");
    const params =
      typeof options.connectionParams === "function"
        ? await options.connectionParams()
        : options.connectionParams;
    expect(params ?? {}).toEqual({});
  });

  it("refreshes the session before each reconnect so the upgrade carries a live cookie", async () => {
    vi.useFakeTimers();
    const order: string[] = [];
    refreshAuthSession.mockImplementation(async () => {
      order.push("refresh");
      return "ok";
    });
    const options = realtimeClientOptions("wss://bridge.test/graphql/ws");
    const waiting = options.retryWait!(0).then(() => order.push("reconnect"));
    await vi.runAllTimersAsync();
    await waiting;
    expect(refreshAuthSession).toHaveBeenCalledTimes(1);
    expect(order).toEqual(["refresh", "reconnect"]);
  });

  it("still reconnects when the refresh fails", async () => {
    vi.useFakeTimers();
    refreshAuthSession.mockResolvedValue("transient");
    const options = realtimeClientOptions("wss://bridge.test/graphql/ws");
    let reconnected = false;
    const waiting = options.retryWait!(3).then(() => {
      reconnected = true;
    });
    await vi.runAllTimersAsync();
    await waiting;
    expect(reconnected).toBe(true);
  });
});
