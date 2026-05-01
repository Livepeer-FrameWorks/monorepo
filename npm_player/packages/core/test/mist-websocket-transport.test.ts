import { afterEach, beforeEach, describe, expect, expectTypeOf, it, vi } from "vitest";
import { MistWebSocketTransport } from "../src/core/mist/transports/websocket-transport";
import type { MistMediaTransport } from "../src/core/mist/transport";

class MockWebSocket {
  static OPEN = 1;
  static instances: MockWebSocket[] = [];

  readyState = MockWebSocket.OPEN;
  onopen: (() => void) | null = null;
  onmessage: ((event: { data: string }) => void) | null = null;
  onclose: (() => void) | null = null;
  onerror: (() => void) | null = null;
  binaryType = "";
  send = vi.fn();
  close = vi.fn(() => {
    this.onclose?.();
  });

  constructor(public readonly url: string) {
    MockWebSocket.instances.push(this);
  }
}

describe("MistWebSocketTransport", () => {
  let originalWebSocket: typeof globalThis.WebSocket;

  beforeEach(() => {
    originalWebSocket = globalThis.WebSocket;
    (globalThis as any).WebSocket = MockWebSocket;
    MockWebSocket.instances = [];
  });

  afterEach(() => {
    (globalThis as any).WebSocket = originalWebSocket;
    vi.restoreAllMocks();
  });

  it("implements MistMediaTransport contract", () => {
    expectTypeOf<MistWebSocketTransport>().toMatchTypeOf<MistMediaTransport>();
  });

  it("cleans up once listeners when cancelled", async () => {
    const transport = new MistWebSocketTransport("ws://mist.test/json");
    const connect = transport.connect();
    MockWebSocket.instances[0].onopen?.();
    await connect;

    const handle = transport.once("set_speed");
    const rejected = expect(handle.promise).rejects.toThrow("cancelled");

    handle.cancel();

    await rejected;
    expect((transport as any).listeners.get("event")?.size ?? 0).toBe(0);
    expect((transport as any).listeners.get("statechange")?.size ?? 0).toBe(0);
  });
});
