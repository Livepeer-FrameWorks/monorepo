import { afterEach, describe, expect, it, vi } from "vitest";

const refreshAuthSession = vi.fn();
vi.mock("$lib/auth/refresh", () => ({ refreshAuthSession }));

const { realtimeClientOptions } = await import("./realtime-ws");

afterEach(() => {
  vi.useRealTimers();
  refreshAuthSession.mockReset();
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
