import { describe, expect, expectTypeOf, it, vi } from "vitest";
import { MistDataChannelTransport } from "../src/core/mist/transports/data-channel-transport";
import type { MistMediaTransport } from "../src/core/mist/transport";

class MockDataChannel extends EventTarget {
  readyState: RTCDataChannelState = "open";
  send = vi.fn();
  close = vi.fn(() => {
    this.readyState = "closed";
    this.dispatchEvent(new Event("close"));
  });
}

describe("MistDataChannelTransport", () => {
  it("implements MistMediaTransport contract", () => {
    expectTypeOf<MistDataChannelTransport>().toMatchTypeOf<MistMediaTransport>();
  });

  it("cleans up once listeners when cancelled", async () => {
    const channel = new MockDataChannel();
    const transport = new MistDataChannelTransport(channel as unknown as RTCDataChannel);
    const handle = transport.once("set_speed");
    const rejected = expect(handle.promise).rejects.toThrow("cancelled");

    handle.cancel();

    await rejected;
    expect((transport as any).listeners.get("event")?.size ?? 0).toBe(0);
    expect((transport as any).listeners.get("statechange")?.size ?? 0).toBe(0);
  });
});
