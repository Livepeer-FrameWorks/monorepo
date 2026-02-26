import { describe, expect, it, vi } from "vitest";

import { MistControlChannel } from "../src/core/MistControlChannel";

class MockDataChannel {
  readyState: "connecting" | "open" | "closing" | "closed" = "connecting";
  send = vi.fn();
  close = vi.fn(() => {
    this.readyState = "closed";
    this.dispatch("close");
  });

  private listeners = new Map<string, Set<(event?: any) => void>>();

  addEventListener(event: string, listener: (event?: any) => void): void {
    if (!this.listeners.has(event)) {
      this.listeners.set(event, new Set());
    }
    this.listeners.get(event)!.add(listener);
  }

  removeEventListener(event: string, listener: (event?: any) => void): void {
    this.listeners.get(event)?.delete(listener);
  }

  dispatch(event: string, data?: any): void {
    this.listeners.get(event)?.forEach((listener) => listener(data));
  }
}

describe("MistControlChannel", () => {
  it("queues commands while connecting and flushes on open", () => {
    const dc = new MockDataChannel();
    const control = new MistControlChannel(dc as unknown as RTCDataChannel);

    control.seek(1234);
    expect(dc.send).not.toHaveBeenCalled();

    dc.readyState = "open";
    dc.dispatch("open");
    expect(dc.send).toHaveBeenCalledWith(JSON.stringify({ type: "seek", seek_time: 1234 }));
  });

  it("routes on_time payload (top-level)", () => {
    const dc = new MockDataChannel();
    const control = new MistControlChannel(dc as unknown as RTCDataChannel);
    const handler = vi.fn();
    control.on("time_update", handler);

    dc.dispatch("message", {
      data: JSON.stringify({ type: "on_time", current: 1000, begin: 100, end: 2000 }),
    });

    expect(handler).toHaveBeenCalledWith({
      current: 1000,
      end: 2000,
      begin: 100,
      tracks: undefined,
      paused: undefined,
      live_point: undefined,
      play_rate_curr: undefined,
    });
  });

  it("routes on_time payload nested in data", () => {
    const dc = new MockDataChannel();
    const control = new MistControlChannel(dc as unknown as RTCDataChannel);
    const handler = vi.fn();
    control.on("time_update", handler);

    dc.dispatch("message", {
      data: JSON.stringify({
        type: "on_time",
        data: { current: 2000, begin: 250, end: 3500, paused: false, tracks: ["v1"] },
      }),
    });

    expect(handler).toHaveBeenCalledWith({
      current: 2000,
      end: 3500,
      begin: 250,
      tracks: ["v1"],
      paused: false,
      live_point: undefined,
      play_rate_curr: undefined,
    });
  });

  it("routes pause payload nested in data", () => {
    const dc = new MockDataChannel();
    const control = new MistControlChannel(dc as unknown as RTCDataChannel);
    const handler = vi.fn();
    control.on("pause", handler);

    dc.dispatch("message", {
      data: JSON.stringify({
        type: "pause",
        data: { paused: true, reason: "at_dead_point", begin: 10, end: 90 },
      }),
    });

    expect(handler).toHaveBeenCalledWith({
      paused: true,
      reason: "at_dead_point",
      begin: 10,
      end: 90,
    });
  });
});
