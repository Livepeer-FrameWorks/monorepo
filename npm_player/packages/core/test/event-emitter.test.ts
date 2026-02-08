import { describe, expect, it, vi } from "vitest";

import { TypedEventEmitter } from "../src/core/EventEmitter";

interface TestEvents {
  alpha: { value: number };
  beta: string;
}

class TestEmitter extends TypedEventEmitter<TestEvents> {
  emitPublic<K extends keyof TestEvents>(event: K, data: TestEvents[K]) {
    this.emit(event, data);
  }
}

describe("TypedEventEmitter", () => {
  it("on returns unsubscribe and emits payload", () => {
    const emitter = new TestEmitter();
    const listener = vi.fn();

    const unsub = emitter.on("alpha", listener);
    emitter.emitPublic("alpha", { value: 7 });

    expect(listener).toHaveBeenCalledWith({ value: 7 });

    unsub();
    emitter.emitPublic("alpha", { value: 9 });
    expect(listener).toHaveBeenCalledTimes(1);
  });

  it("once fires only one time", () => {
    const emitter = new TestEmitter();
    const listener = vi.fn();

    emitter.once("beta", listener);
    emitter.emitPublic("beta", "first");
    emitter.emitPublic("beta", "second");

    expect(listener).toHaveBeenCalledTimes(1);
    expect(listener).toHaveBeenCalledWith("first");
  });

  it("off removes specific listener", () => {
    const emitter = new TestEmitter();
    const listener = vi.fn();

    emitter.on("alpha", listener);
    emitter.off("alpha", listener);
    emitter.emitPublic("alpha", { value: 1 });

    expect(listener).not.toHaveBeenCalled();
  });

  it("emit isolates listener errors and logs", () => {
    const emitter = new TestEmitter();
    const good = vi.fn();
    const bad = vi.fn(() => {
      throw new Error("boom");
    });
    const errorSpy = vi.spyOn(console, "error").mockImplementation(() => {});

    emitter.on("beta", bad);
    emitter.on("beta", good);

    emitter.emitPublic("beta", "payload");

    expect(good).toHaveBeenCalledWith("payload");
    expect(errorSpy).toHaveBeenCalledWith(
      "[EventEmitter] Error in beta listener:",
      expect.any(Error)
    );
  });

  it("removeAllListeners and removeListeners clear subscriptions", () => {
    const emitter = new TestEmitter();
    const listener = vi.fn();

    emitter.on("alpha", listener);
    emitter.on("beta", listener);

    emitter.removeListeners("alpha");
    expect(emitter.hasListeners("alpha")).toBe(false);
    expect(emitter.hasListeners("beta")).toBe(true);

    emitter.removeAllListeners();
    expect(emitter.hasListeners("beta")).toBe(false);
  });

  it("hasListeners reports presence", () => {
    const emitter = new TestEmitter();
    const listener = vi.fn();

    expect(emitter.hasListeners("alpha")).toBe(false);
    emitter.on("alpha", listener);
    expect(emitter.hasListeners("alpha")).toBe(true);
  });
});
