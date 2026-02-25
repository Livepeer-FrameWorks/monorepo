import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import { MetaTrackManager } from "../src/core/MetaTrackManager";
import type { MetaTrackEvent } from "../src/types";

class MockWebSocket {
  static OPEN = 1;
  static instances: MockWebSocket[] = [];

  readyState = MockWebSocket.OPEN;
  onopen: (() => void) | null = null;
  onmessage: ((e: { data: string }) => void) | null = null;
  onclose: (() => void) | null = null;
  onerror: ((e: any) => void) | null = null;
  send = vi.fn();
  close = vi.fn(() => {
    this.onclose?.();
  });

  constructor(public url: string) {
    MockWebSocket.instances.push(this);
  }
}

describe("MetaTrackManager", () => {
  let originalWebSocket: typeof globalThis.WebSocket;

  beforeEach(() => {
    vi.useFakeTimers();
    vi.setSystemTime(0);
    originalWebSocket = globalThis.WebSocket;
    (globalThis as any).WebSocket = MockWebSocket;
    MockWebSocket.instances = [];
  });

  afterEach(() => {
    vi.useRealTimers();
    (globalThis as any).WebSocket = originalWebSocket;
    vi.restoreAllMocks();
  });

  it("uses defaults and builds websocket URL", () => {
    const manager = new MetaTrackManager({ mistBaseUrl: "https://mist.test", streamName: "abc" });

    expect((manager as any).bufferAhead).toBe(5);
    expect((manager as any).maxMessageAge).toBe(5);
    expect((manager as any).fastForwardInterval).toBe(5);

    expect((manager as any).buildWsUrl()).toBe("wss://mist.test/json_abc.js?rate=1");
  });

  it("connects with debounce, sends tracks and seek", () => {
    const manager = new MetaTrackManager({ mistBaseUrl: "http://mist.test", streamName: "abc" });
    manager.subscribe("1", vi.fn());

    manager.connect();
    manager.connect();

    expect(MockWebSocket.instances).toHaveLength(0);
    vi.advanceTimersByTime(100);

    expect(MockWebSocket.instances).toHaveLength(1);
    const ws = MockWebSocket.instances[0];
    ws.onopen?.();

    expect(ws.send).toHaveBeenCalledTimes(2);
    const [tracks, seek] = ws.send.mock.calls.map((call) => JSON.parse(call[0]));
    expect(tracks).toEqual({ type: "tracks", meta: "1" });
    expect(seek).toEqual({ type: "seek", seek_time: 0, ff_to: 5000 });
  });

  it("disconnect closes socket and clears timers", () => {
    const manager = new MetaTrackManager({ mistBaseUrl: "http://mist.test", streamName: "abc" });
    manager.connect();
    vi.advanceTimersByTime(100);

    const ws = MockWebSocket.instances[0];
    ws.onopen?.();

    manager.disconnect();
    expect(ws.close).toHaveBeenCalledTimes(1);
    expect(manager.getState()).toBe("disconnected");
  });

  it("updates subscriptions and sends track updates", () => {
    const manager = new MetaTrackManager({ mistBaseUrl: "http://mist.test", streamName: "abc" });
    const ws = new MockWebSocket("ws://mock");
    (manager as any).ws = ws as unknown as WebSocket;
    (manager as any).state = "connected";

    const cb = vi.fn();
    const unsub = manager.subscribe("2", cb);
    expect(ws.send).toHaveBeenCalledWith(JSON.stringify({ type: "tracks", meta: "2" }));

    unsub();
    expect(ws.send).toHaveBeenCalledWith(JSON.stringify({ type: "tracks", meta: "" }));
  });

  it("buffers subtitle events and dispatches when due", () => {
    const manager = new MetaTrackManager({ mistBaseUrl: "http://mist.test", streamName: "abc" });
    const cb = vi.fn();
    manager.subscribe("1", cb);

    (manager as any).handleMessage(
      JSON.stringify({ time: 1000, track: 1, data: { text: "Hello", startTime: 1 } })
    );

    expect(cb).not.toHaveBeenCalled();

    manager.setPlaybackTime(1);
    expect(cb).toHaveBeenCalledWith(
      expect.objectContaining({ type: "subtitle", trackId: "1", timestamp: 1000 })
    );
  });

  it("dispatches non-timed events after playback time advances past them", () => {
    const manager = new MetaTrackManager({ mistBaseUrl: "http://mist.test", streamName: "abc" });
    const cb = vi.fn();
    manager.subscribe("1", cb);

    (manager as any).handleMessage(
      JSON.stringify({ time: 2000, track: 1, data: { key: "score", value: 1 } })
    );

    // All timed events are now buffered (upstream parity), so advance playback time
    expect(cb).not.toHaveBeenCalled();
    manager.setPlaybackTime(2);
    expect(cb).toHaveBeenCalledWith(
      expect.objectContaining({ type: "score", trackId: "1", timestamp: 2000 })
    );
  });

  it("handles on_time, seek and live control messages", () => {
    const manager = new MetaTrackManager({ mistBaseUrl: "http://mist.test", streamName: "abc" });
    const ws = new MockWebSocket("ws://mock");
    (manager as any).ws = ws as unknown as WebSocket;
    (manager as any).state = "connected";

    (manager as any).handleMessage(JSON.stringify({ type: "on_time", data: { current: 40000 } }));
    expect(ws.send).toHaveBeenCalledWith(JSON.stringify({ type: "hold" }));

    (manager as any).addToTimedBuffer({
      type: "subtitle",
      timestamp: 1000,
      trackId: "1",
      data: {},
    } as MetaTrackEvent);

    expect((manager as any).timedEventBuffer.size).toBe(1);
    (manager as any).handleMessage(JSON.stringify({ type: "seek" }));
    expect((manager as any).timedEventBuffer.size).toBe(0);

    // Some MistServer builds emit {type:"live"} status messages.
    // They should be treated as benign (no warning/no state mutation).
    (manager as any).handleMessage(JSON.stringify({ type: "live" }));
    expect((manager as any).timedEventBuffer.size).toBe(0);
  });

  it("detects event types", () => {
    const manager = new MetaTrackManager({ mistBaseUrl: "http://mist.test", streamName: "abc" });

    expect((manager as any).detectEventType({ text: "hi", startTime: 1 })).toBe("subtitle");
    expect((manager as any).detectEventType({ key: "k", value: 2 })).toBe("score");
    expect((manager as any).detectEventType({ title: "Chap", startTime: 1 })).toBe("chapter");
    expect((manager as any).detectEventType({ name: "evt" })).toBe("event");
    expect((manager as any).detectEventType(123)).toBe("unknown");
  });

  it("manages timed buffer and fast-forward", () => {
    const manager = new MetaTrackManager({ mistBaseUrl: "http://mist.test", streamName: "abc" });
    const ws = new MockWebSocket("ws://mock");
    (manager as any).ws = ws as unknown as WebSocket;
    (manager as any).state = "connected";

    (manager as any).addToTimedBuffer({
      type: "subtitle",
      timestamp: 5000,
      trackId: "1",
      data: {},
    } as MetaTrackEvent);

    (manager as any).addToTimedBuffer({
      type: "subtitle",
      timestamp: 2000,
      trackId: "1",
      data: {},
    } as MetaTrackEvent);

    expect(manager.needsMoreData("1")).toBe(false);
    manager.setPlaybackTime(1);
    expect(manager.needsMoreData("1")).toBe(true);

    vi.setSystemTime(6000);
    manager.fastForward();
    expect(ws.send).toHaveBeenCalledWith(JSON.stringify({ type: "fast_forward", ff_to: 6000 }));

    vi.setSystemTime(7000);
    manager.fastForward();
    expect(ws.send).toHaveBeenCalledTimes(1);
  });

  it("reconnects with backoff after close", () => {
    const manager = new MetaTrackManager({ mistBaseUrl: "http://mist.test", streamName: "abc" });
    manager.connect();
    vi.advanceTimersByTime(100);

    const ws = MockWebSocket.instances[0];
    ws.onopen?.();

    ws.onclose?.();
    expect(manager.getState()).toBe("reconnecting");

    vi.advanceTimersByTime(1100);
    expect(MockWebSocket.instances.length).toBe(2);
  });
});
