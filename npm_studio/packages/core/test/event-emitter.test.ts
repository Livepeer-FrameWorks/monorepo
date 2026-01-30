import { describe, expect, it, vi } from "vitest";

import { TypedEventEmitter } from "../src/core/EventEmitter";

type Events = {
  ping: { n: number };
  empty: undefined;
};

class E extends TypedEventEmitter<Events> {
  public emitPing(n: number) {
    this.emit("ping", { n });
  }
  public emitEmpty() {
    this.emit("empty", undefined);
  }
}

describe("TypedEventEmitter", () => {
  it("calls handlers and supports unsubscribe", () => {
    const e = new E();
    const handler = vi.fn();

    const unsub = e.on("ping", handler);
    e.emitPing(1);
    expect(handler).toHaveBeenCalledTimes(1);

    unsub();
    e.emitPing(2);
    expect(handler).toHaveBeenCalledTimes(1);
  });

  it("once only fires once", () => {
    const e = new E();
    const handler = vi.fn();

    e.once("ping", handler);
    e.emitPing(1);
    e.emitPing(2);

    expect(handler).toHaveBeenCalledTimes(1);
    expect(handler).toHaveBeenCalledWith({ n: 1 });
  });

  it("removeAllListeners removes listeners", () => {
    const e = new E();
    const handler = vi.fn();

    e.on("ping", handler);
    expect(e.listenerCount("ping")).toBe(1);

    e.removeAllListeners("ping");
    expect(e.listenerCount("ping")).toBe(0);

    e.emitPing(1);
    expect(handler).not.toHaveBeenCalled();
  });

  it("does not crash when a handler throws", () => {
    const e = new E();
    const good = vi.fn();
    const bad = vi.fn(() => {
      throw new Error("boom");
    });

    e.on("ping", bad);
    e.on("ping", good);

    e.emitPing(1);

    expect(bad).toHaveBeenCalledTimes(1);
    expect(good).toHaveBeenCalledTimes(1);
  });

  it("supports undefined payload events", () => {
    const e = new E();
    const handler = vi.fn();

    e.on("empty", handler);
    e.emitEmpty();

    expect(handler).toHaveBeenCalledWith(undefined);
  });
});
