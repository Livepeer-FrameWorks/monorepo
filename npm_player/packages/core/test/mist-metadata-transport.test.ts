import { afterEach, beforeEach, describe, expect, expectTypeOf, it, vi } from "vitest";

import { MistMetadataWsTransport } from "../src/core/mist/transports/metadata-transport";
import type { MistMetadataTransport } from "../src/core/mist/transport";

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

describe("MistMetadataWsTransport", () => {
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

  it("implements MistMetadataTransport contract", () => {
    expectTypeOf<MistMetadataWsTransport>().toMatchTypeOf<MistMetadataTransport>();
  });

  it("preserves metadata ff_to commands and raw cue events", async () => {
    const transport = new MistMetadataWsTransport("ws://mist.test/json_stream.js?rate=1", {
      maxReconnectAttempts: -1,
    });
    const received: unknown[] = [];
    transport.on("event", ({ event }) => received.push(event));

    const connect = transport.connect();
    const ws = MockWebSocket.instances[0];
    ws.onopen?.();
    await connect;

    transport.send({ type: "seek", seek_time: 1000, ff_to: 6000 });
    transport.send({ type: "fast_forward", ff_to: 7000 });

    expect(ws.send).toHaveBeenNthCalledWith(
      1,
      JSON.stringify({ type: "seek", seek_time: 1000, ff_to: 6000 })
    );
    expect(ws.send).toHaveBeenNthCalledWith(
      2,
      JSON.stringify({ type: "fast_forward", ff_to: 7000 })
    );

    ws.onmessage?.({ data: JSON.stringify({ time: 1000, track: 1, data: { text: "Hello" } }) });
    expect(received).toEqual([{ type: "metadata", time: 1000, track: 1, data: { text: "Hello" } }]);
  });
});
