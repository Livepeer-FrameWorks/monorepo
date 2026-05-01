import { describe, expect, it, vi } from "vitest";

import { CallbackMistTransport } from "../src/core/mist/transports/callback-transport";

describe("CallbackMistTransport", () => {
  it("applies decorators/listeners and supports cancellable once listeners", async () => {
    const send = vi.fn(() => true);
    const transport = new CallbackMistTransport(send);
    const listener = vi.fn();

    transport.addSendDecorator((cmd) => (cmd.type === "play" ? { ...cmd, ff_add: 1000 } : cmd));
    transport.addSendListener(listener);

    expect(transport.send({ type: "play" })).toBe(true);
    expect(send).toHaveBeenCalledWith({ type: "play", ff_add: 1000 });
    expect(listener).toHaveBeenCalledWith({ type: "play", ff_add: 1000 });

    const handle = transport.once("set_speed");
    const rejected = expect(handle.promise).rejects.toThrow("cancelled");
    handle.cancel();
    await rejected;
  });
});
