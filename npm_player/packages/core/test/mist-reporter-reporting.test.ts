import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import { MistReporter } from "../src/core/MistReporter";

// Covers the E2 batch-flush and E3 offline-queue/reconnect paths via a mock
// socket. WebSocket.OPEN must exist globally for the readyState comparisons.

function openSocket() {
  return { readyState: 1, send: vi.fn() } as unknown as WebSocket;
}

function sentPayloads(socket: WebSocket): any[] {
  return (socket.send as unknown as ReturnType<typeof vi.fn>).mock.calls.map((c) =>
    JSON.parse(c[0] as string)
  );
}

beforeEach(() => {
  vi.spyOn(console, "debug").mockImplementation(() => {});
  vi.stubGlobal(
    "WebSocket",
    Object.assign(function () {}, { OPEN: 1 })
  );
});

afterEach(() => {
  vi.restoreAllMocks();
  vi.unstubAllGlobals();
});

describe("MistReporter — E2 batch flush", () => {
  it("sendFinalReport flushes immediately with the unload reason", () => {
    const r = new MistReporter();
    const socket = openSocket();
    r.setSocket(socket);

    r.sendFinalReport("page-hidden");

    const payloads = sentPayloads(socket);
    expect(payloads.length).toBeGreaterThan(0);
    expect(payloads.some((p) => p.unload === "page-hidden")).toBe(true);
    r.destroy();
  });

  it("flushBatch is a no-op when nothing is pending", () => {
    const r = new MistReporter();
    const socket = openSocket();
    r.setSocket(socket);
    r.flushBatch();
    expect(socket.send).not.toHaveBeenCalled();
    r.destroy();
  });
});

describe("MistReporter — E3 offline queue", () => {
  it("queues reports while disconnected and replays them on reconnect", () => {
    const r = new MistReporter();
    // No socket yet → the final report is queued, not sent.
    r.sendFinalReport("bye");

    const socket = openSocket();
    r.setSocket(socket); // was disconnected → flush the offline queue

    const payloads = sentPayloads(socket);
    expect(payloads.some((p) => p.unload === "bye")).toBe(true);
    r.destroy();
  });

  it("does not flush when the reconnect socket is not OPEN", () => {
    const r = new MistReporter();
    r.sendFinalReport("bye");
    const connecting = { readyState: 0, send: vi.fn() } as unknown as WebSocket;
    r.setSocket(connecting);
    expect(connecting.send).not.toHaveBeenCalled();
    r.destroy();
  });
});
