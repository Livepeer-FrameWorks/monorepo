import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

interface FakeTransport {
  url: string;
  handlers: Record<string, Array<(payload: any) => void>>;
  send: ReturnType<typeof vi.fn>;
  connect: ReturnType<typeof vi.fn>;
  destroy: ReturnType<typeof vi.fn>;
  addSendDecorator: ReturnType<typeof vi.fn>;
  on(type: string, cb: (payload: any) => void): () => void;
  emit(type: string, payload: any): void;
}

// Capture every fake transport the manager constructs so tests can drive its
// events. The class lives inside the factory because vi.mock is hoisted above
// all module-level declarations.
const txState = vi.hoisted(() => ({ instances: [] as FakeTransport[] }));

vi.mock("../src/core/mist/transports/websocket-transport", () => {
  class FakeTransportImpl {
    handlers: Record<string, Array<(payload: any) => void>> = {};
    send = vi.fn(() => true);
    connect = vi.fn(async () => {});
    destroy = vi.fn();
    addSendDecorator = vi.fn(() => () => {});
    constructor(
      public url: string,
      public opts: unknown
    ) {
      txState.instances.push(this as unknown as FakeTransport);
    }
    on(type: string, cb: (payload: any) => void): () => void {
      (this.handlers[type] ??= []).push(cb);
      return () => {};
    }
    emit(type: string, payload: any): void {
      for (const cb of this.handlers[type] ?? []) cb(payload);
    }
  }
  return { MistWebSocketTransport: FakeTransportImpl };
});

import { WebSocketManager } from "../src/players/MewsWsPlayer/WebSocketManager";
import type { MewsMessageListener } from "../src/players/MewsWsPlayer/types";

describe("WebSocketManager", () => {
  let onMessage: ReturnType<typeof vi.fn>;
  let onOpen: ReturnType<typeof vi.fn>;
  let onClose: ReturnType<typeof vi.fn>;
  let onError: ReturnType<typeof vi.fn>;

  function make(
    opts: Partial<{ maxReconnectAttempts: number; shouldReconnect: () => boolean }> = {}
  ) {
    return new WebSocketManager({
      url: "wss://mist.example.com/stream",
      onMessage,
      onOpen,
      onClose,
      onError,
      ...opts,
    });
  }

  const tx = () => txState.instances[txState.instances.length - 1];

  beforeEach(() => {
    vi.useFakeTimers();
    txState.instances = [];
    onMessage = vi.fn();
    onOpen = vi.fn();
    onClose = vi.fn();
    onError = vi.fn();
  });

  afterEach(() => {
    vi.useRealTimers();
    vi.clearAllMocks();
  });

  describe("transport state → callbacks", () => {
    it("connected flips isConnected and fires onOpen", () => {
      const m = make();
      expect(m.isConnected()).toBe(false);
      tx().emit("statechange", { state: "connected" });
      expect(m.isConnected()).toBe(true);
      expect(onOpen).toHaveBeenCalledOnce();
    });

    it("disconnected with reconnection allowed does NOT fire onClose", () => {
      const m = make();
      tx().emit("statechange", { state: "connected" });
      tx().emit("statechange", { state: "disconnected" });
      expect(m.isConnected()).toBe(false);
      expect(onClose).not.toHaveBeenCalled();
    });

    it("disconnected fires onClose once reconnection is disabled", () => {
      const m = make();
      m.disableReconnection();
      tx().emit("statechange", { state: "disconnected" });
      expect(onClose).toHaveBeenCalledOnce();
    });

    it("disconnected fires onClose when shouldReconnect() is false", () => {
      make({ shouldReconnect: () => false });
      tx().emit("statechange", { state: "disconnected" });
      expect(onClose).toHaveBeenCalledOnce();
    });

    it("forwards binary, event and error messages", () => {
      make();
      const data = new ArrayBuffer(4);
      tx().emit("binary", { data });
      expect(onMessage).toHaveBeenCalledWith(data);

      tx().emit("event", { event: { type: "on_time", current: 1 } });
      expect(onMessage).toHaveBeenCalledWith(JSON.stringify({ type: "on_time", current: 1 }));

      tx().emit("error", { message: "boom" });
      expect(onError).toHaveBeenCalledWith("boom");
    });
  });

  describe("send", () => {
    it("sends immediately through the transport when connected", () => {
      const m = make();
      tx().emit("statechange", { state: "connected" });
      expect(m.send({ type: "play" })).toBe(true);
      expect(tx().send).toHaveBeenCalledWith({ type: "play" });
    });

    it("buffers a command sent while disconnected and replays it on connect", () => {
      const m = make();
      expect(m.send({ type: "play" })).toBe(false);
      expect(tx().send).not.toHaveBeenCalled();

      tx().emit("statechange", { state: "connected" });
      // No timer involved — the buffered command flushes as part of connect.
      expect(tx().send).toHaveBeenCalledWith({ type: "play" });
    });

    it("replays buffered commands in their original order", () => {
      const m = make();
      m.send({ type: "a" });
      m.send({ type: "b" });
      tx().emit("statechange", { state: "connected" });
      expect(tx().send.mock.calls.map((c) => c[0])).toEqual([{ type: "a" }, { type: "b" }]);
    });

    it("bounds the pending buffer, dropping the oldest on overflow", () => {
      const m = make();
      for (let i = 0; i < 40; i++) m.send({ i }); // MAX_PENDING_SENDS is 32
      tx().emit("statechange", { state: "connected" });
      const sent = tx().send.mock.calls.map((c) => c[0]);
      expect(sent).toHaveLength(32);
      expect(sent[0]).toEqual({ i: 8 }); // oldest 8 dropped
      expect(sent[31]).toEqual({ i: 39 });
    });

    it("sendDirect only transmits while connected", () => {
      const m = make();
      expect(m.sendDirect({ type: "ping" })).toBe(false);
      tx().emit("statechange", { state: "connected" });
      expect(m.sendDirect({ type: "ping" })).toBe(true);
      expect(tx().send).toHaveBeenCalledWith({ type: "ping" });
    });
  });

  describe("listener registry", () => {
    it("notifies only the listeners registered for the message type", () => {
      const m = make();
      const onTime = vi.fn();
      const onCodec = vi.fn();
      m.addListener("on_time", onTime as MewsMessageListener);
      m.addListener("codec_data", onCodec as MewsMessageListener);

      m.notifyListeners({ type: "on_time" } as never);
      expect(onTime).toHaveBeenCalledOnce();
      expect(onCodec).not.toHaveBeenCalled();
    });

    it("removeListener detaches a specific callback and reports success", () => {
      const m = make();
      const cb = vi.fn();
      m.addListener("seek", cb as MewsMessageListener);
      expect(m.removeListener("seek", cb as MewsMessageListener)).toBe(true);
      expect(m.removeListener("seek", cb as MewsMessageListener)).toBe(false);
      m.notifyListeners({ type: "seek" } as never);
      expect(cb).not.toHaveBeenCalled();
    });

    it("isolates a throwing listener from the rest", () => {
      const m = make();
      const boom = vi.fn(() => {
        throw new Error("listener boom");
      });
      const ok = vi.fn();
      const errSpy = vi.spyOn(console, "error").mockImplementation(() => {});
      m.addListener("x", ok as MewsMessageListener);
      m.addListener("x", boom as MewsMessageListener); // fired first (backwards iteration)
      m.notifyListeners({ type: "x" } as never);
      expect(boom).toHaveBeenCalled();
      expect(ok).toHaveBeenCalled();
      errSpy.mockRestore();
    });
  });

  describe("destroy", () => {
    it("tears down the transport, clears listeners and drops buffered sends", () => {
      const m = make();
      const cb = vi.fn();
      m.addListener("on_time", cb as MewsMessageListener);
      m.send({ type: "play" }); // buffered while disconnected

      m.destroy();
      expect(tx().destroy).toHaveBeenCalledOnce();
      expect(m.isConnected()).toBe(false);

      // Listeners cleared: a post-destroy notify does nothing.
      m.notifyListeners({ type: "on_time" } as never);
      expect(cb).not.toHaveBeenCalled();

      // Buffered sends dropped: a later connect replays nothing.
      tx().emit("statechange", { state: "connected" });
      expect(tx().send).not.toHaveBeenCalled();
    });
  });
});
