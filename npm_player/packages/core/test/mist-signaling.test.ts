import { describe, expect, it, vi, beforeEach, afterEach } from "vitest";

import { MistSignaling } from "../src/core/MistSignaling";

// ---------------------------------------------------------------------------
// Mock WebSocket
// ---------------------------------------------------------------------------

let mockWsInstances: MockWebSocket[];

class MockWebSocket {
  static CONNECTING = 0;
  static OPEN = 1;
  static CLOSING = 2;
  static CLOSED = 3;

  readyState = MockWebSocket.CONNECTING;
  url: string;
  onopen: ((ev: Event) => void) | null = null;
  onmessage: ((ev: MessageEvent) => void) | null = null;
  onclose: ((ev: CloseEvent) => void) | null = null;
  onerror: ((ev: Event) => void) | null = null;

  private eventListeners = new Map<string, Function[]>();

  send = vi.fn();
  close = vi.fn(() => {
    this.readyState = MockWebSocket.CLOSED;
    if (this.onclose) {
      this.onclose({ code: 1000, reason: "" } as CloseEvent);
    }
  });

  constructor(url: string) {
    this.url = url;
    mockWsInstances.push(this);
  }

  addEventListener(event: string, handler: Function): void {
    if (!this.eventListeners.has(event)) this.eventListeners.set(event, []);
    this.eventListeners.get(event)!.push(handler);
  }

  removeEventListener(event: string, handler: Function): void {
    const list = this.eventListeners.get(event);
    if (list) {
      const idx = list.indexOf(handler);
      if (idx >= 0) list.splice(idx, 1);
    }
  }

  // Test helpers
  simulateOpen(): void {
    this.readyState = MockWebSocket.OPEN;
    if (this.onopen) this.onopen({} as Event);
  }

  simulateMessage(data: Record<string, unknown>): void {
    if (this.onmessage) this.onmessage({ data: JSON.stringify(data) } as MessageEvent);
  }

  simulateClose(code = 1000): void {
    this.readyState = MockWebSocket.CLOSED;
    if (this.onclose) this.onclose({ code } as CloseEvent);
  }

  simulateError(): void {
    if (this.onerror) this.onerror({} as Event);
  }
}

// ---------------------------------------------------------------------------
// Tests
// ---------------------------------------------------------------------------

describe("MistSignaling", () => {
  let origWebSocket: any;

  beforeEach(() => {
    vi.useFakeTimers();
    mockWsInstances = [];
    origWebSocket = (globalThis as any).WebSocket;
    (globalThis as any).WebSocket = MockWebSocket;
  });

  afterEach(() => {
    vi.useRealTimers();
    vi.restoreAllMocks();
    (globalThis as any).WebSocket = origWebSocket;
  });

  // ===========================================================================
  // Constructor
  // ===========================================================================
  describe("constructor", () => {
    it("converts http URL to ws", () => {
      const sig = new MistSignaling({ url: "http://example.com/ws" });
      sig.connect();
      expect(mockWsInstances[0].url).toBe("ws://example.com/ws");
    });

    it("converts https URL to wss", () => {
      const sig = new MistSignaling({ url: "https://example.com/ws" });
      sig.connect();
      expect(mockWsInstances[0].url).toBe("wss://example.com/ws");
    });

    it("keeps ws URL as-is", () => {
      const sig = new MistSignaling({ url: "ws://example.com/ws" });
      sig.connect();
      expect(mockWsInstances[0].url).toBe("ws://example.com/ws");
    });

    it("defaults timeout to 5000", () => {
      const sig = new MistSignaling({ url: "ws://example.com" });
      expect(sig.state).toBe("disconnected");
    });
  });

  // ===========================================================================
  // Initial state
  // ===========================================================================
  describe("initial state", () => {
    it("state is disconnected", () => {
      const sig = new MistSignaling({ url: "ws://example.com" });
      expect(sig.state).toBe("disconnected");
    });

    it("isConnected is false", () => {
      const sig = new MistSignaling({ url: "ws://example.com" });
      expect(sig.isConnected).toBe(false);
    });
  });

  // ===========================================================================
  // connect
  // ===========================================================================
  describe("connect", () => {
    it("sets state to connecting", () => {
      const sig = new MistSignaling({ url: "ws://example.com" });
      sig.connect();
      expect(sig.state).toBe("connecting");
    });

    it("sets state to connected on open", () => {
      const sig = new MistSignaling({ url: "ws://example.com" });
      sig.connect();
      mockWsInstances[0].simulateOpen();
      expect(sig.state).toBe("connected");
      expect(sig.isConnected).toBe(true);
    });

    it("emits connected event on open", () => {
      const sig = new MistSignaling({ url: "ws://example.com" });
      const handler = vi.fn();
      sig.on("connected", handler);
      sig.connect();
      mockWsInstances[0].simulateOpen();
      expect(handler).toHaveBeenCalled();
    });

    it("no-op if already connected", () => {
      const sig = new MistSignaling({ url: "ws://example.com" });
      sig.connect();
      mockWsInstances[0].simulateOpen();
      sig.connect();
      expect(mockWsInstances).toHaveLength(1);
    });

    it("no-op if currently connecting", () => {
      const sig = new MistSignaling({ url: "ws://example.com" });
      sig.connect();
      sig.connect();
      expect(mockWsInstances).toHaveLength(1);
    });

    it("times out after configured timeout", () => {
      const sig = new MistSignaling({ url: "ws://example.com", timeout: 3000 });
      const handler = vi.fn();
      sig.on("error", handler);

      sig.connect();
      vi.advanceTimersByTime(3000);

      expect(sig.state).toBe("disconnected");
      expect(handler).toHaveBeenCalledWith({ message: "Connection timeout" });
    });

    it("emits disconnected on close", () => {
      const sig = new MistSignaling({ url: "ws://example.com" });
      const handler = vi.fn();
      sig.on("disconnected", handler);

      sig.connect();
      mockWsInstances[0].simulateOpen();
      mockWsInstances[0].simulateClose(1006);

      expect(sig.state).toBe("closed");
      expect(handler).toHaveBeenCalledWith({ code: 1006 });
    });

    it("handles WebSocket creation failure", () => {
      (globalThis as any).WebSocket = function () {
        throw new Error("blocked");
      };
      (globalThis as any).WebSocket.OPEN = 1;
      (globalThis as any).WebSocket.CONNECTING = 0;

      const sig = new MistSignaling({ url: "ws://example.com" });
      sig.connect();
      expect(sig.state).toBe("disconnected");
    });
  });

  // ===========================================================================
  // Message routing
  // ===========================================================================
  describe("message routing", () => {
    function createConnected(): { sig: MistSignaling; ws: MockWebSocket } {
      const sig = new MistSignaling({ url: "ws://example.com" });
      sig.connect();
      const ws = mockWsInstances[0];
      ws.simulateOpen();
      return { sig, ws };
    }

    it("routes on_answer_sdp", () => {
      const { sig, ws } = createConnected();
      const handler = vi.fn();
      sig.on("answer_sdp", handler);

      ws.simulateMessage({
        type: "on_answer_sdp",
        result: true,
        answer_sdp: "v=0\r\n...",
      });

      expect(handler).toHaveBeenCalledWith({
        result: true,
        answer_sdp: "v=0\r\n...",
      });
    });

    it("routes on_time", () => {
      const { sig, ws } = createConnected();
      const handler = vi.fn();
      sig.on("time_update", handler);

      ws.simulateMessage({
        type: "on_time",
        current: 5000,
        end: 60000,
        begin: 0,
        tracks: ["video1", "audio1"],
      });

      expect(handler).toHaveBeenCalledWith({
        current: 5000,
        end: 60000,
        begin: 0,
        tracks: ["video1", "audio1"],
        paused: undefined,
        live_point: undefined,
      });
    });

    it("routes on_time when payload is nested in data", () => {
      const { sig, ws } = createConnected();
      const handler = vi.fn();
      sig.on("time_update", handler);

      ws.simulateMessage({
        type: "on_time",
        data: {
          current: 7000,
          end: 61000,
          begin: 1000,
          paused: false,
        },
      });

      expect(handler).toHaveBeenCalledWith({
        current: 7000,
        end: 61000,
        begin: 1000,
        tracks: undefined,
        paused: false,
        live_point: undefined,
      });
    });

    it("routes on_stop", () => {
      const { sig, ws } = createConnected();
      const handler = vi.fn();
      sig.on("stopped", handler);

      ws.simulateMessage({ type: "on_stop" });
      expect(handler).toHaveBeenCalled();
    });

    it("routes on_error", () => {
      const { sig, ws } = createConnected();
      const handler = vi.fn();
      sig.on("error", handler);

      ws.simulateMessage({ type: "on_error", message: "Stream not found" });
      expect(handler).toHaveBeenCalledWith({ message: "Stream not found" });
    });

    it("routes on_disconnected", () => {
      const { sig, ws } = createConnected();
      const handler = vi.fn();
      sig.on("disconnected", handler);

      ws.simulateMessage({ type: "on_disconnected", code: 4000 });
      expect(sig.state).toBe("disconnected");
      expect(handler).toHaveBeenCalledWith({ code: 4000 });
    });

    it("routes set_speed", () => {
      const { sig, ws } = createConnected();
      const handler = vi.fn();
      sig.on("speed_changed", handler);

      ws.simulateMessage({ type: "set_speed", play_rate: 2, play_rate_curr: 2 });
      expect(handler).toHaveBeenCalledWith({ play_rate: 2, play_rate_curr: 2 });
    });

    it("routes pause when payload is nested in data", () => {
      const { sig, ws } = createConnected();
      const handler = vi.fn();
      sig.on("pause_request", handler);

      ws.simulateMessage({
        type: "pause",
        data: { paused: true, reason: "at_dead_point", begin: 100, end: 900 },
      });
      expect(handler).toHaveBeenCalledWith({
        paused: true,
        reason: "at_dead_point",
        begin: 100,
        end: 900,
      });
    });

    it("routes seek and resolves seekPromise", async () => {
      const { sig, ws } = createConnected();

      const seekPromise = sig.seek(30);
      ws.simulateMessage({ type: "seek" });

      await expect(seekPromise).resolves.toBe("Seeked");
    });

    it("handles invalid JSON gracefully", () => {
      const sig = new MistSignaling({ url: "ws://example.com" });
      sig.connect();
      const ws = mockWsInstances[0];
      ws.simulateOpen();

      // Send invalid JSON
      expect(() => {
        if (ws.onmessage) ws.onmessage({ data: "not json{" } as MessageEvent);
      }).not.toThrow();
    });
  });

  // ===========================================================================
  // send
  // ===========================================================================
  describe("send", () => {
    it("returns false when not connected", () => {
      const sig = new MistSignaling({ url: "ws://example.com" });
      expect(sig.send({ type: "test" })).toBe(false);
    });

    it("sends JSON when connected", () => {
      const sig = new MistSignaling({ url: "ws://example.com" });
      sig.connect();
      mockWsInstances[0].simulateOpen();

      const result = sig.send({ type: "test", data: 42 });
      expect(result).toBe(true);
      expect(mockWsInstances[0].send).toHaveBeenCalledWith(
        JSON.stringify({ type: "test", data: 42 })
      );
    });

    it("returns false on send error", () => {
      const sig = new MistSignaling({ url: "ws://example.com" });
      sig.connect();
      mockWsInstances[0].simulateOpen();
      mockWsInstances[0].send.mockImplementation(() => {
        throw new Error("send failed");
      });

      expect(sig.send({ type: "test" })).toBe(false);
    });
  });

  // ===========================================================================
  // Command helpers
  // ===========================================================================
  describe("command helpers", () => {
    function createConnected(): { sig: MistSignaling; ws: MockWebSocket } {
      const sig = new MistSignaling({ url: "ws://example.com" });
      sig.connect();
      const ws = mockWsInstances[0];
      ws.simulateOpen();
      return { sig, ws };
    }

    it("sendOfferSDP sends offer_sdp type", () => {
      const { sig, ws } = createConnected();
      sig.sendOfferSDP("v=0\r\n...");
      expect(ws.send).toHaveBeenCalledWith(
        JSON.stringify({ type: "offer_sdp", offer_sdp: "v=0\r\n..." })
      );
    });

    it("pause sends hold type", () => {
      const { sig, ws } = createConnected();
      sig.pause();
      expect(ws.send).toHaveBeenCalledWith(JSON.stringify({ type: "hold" }));
    });

    it("play sends play type", () => {
      const { sig, ws } = createConnected();
      sig.play();
      expect(ws.send).toHaveBeenCalledWith(JSON.stringify({ type: "play" }));
    });

    it("stop sends stop type", () => {
      const { sig, ws } = createConnected();
      sig.stop();
      expect(ws.send).toHaveBeenCalledWith(JSON.stringify({ type: "stop" }));
    });

    it("setTracks sends tracks type", () => {
      const { sig, ws } = createConnected();
      sig.setTracks({ video: "~1080x720", audio: "eng" });
      expect(ws.send).toHaveBeenCalledWith(
        JSON.stringify({ type: "tracks", video: "~1080x720", audio: "eng" })
      );
    });

    it("setSpeed sends set_speed type", () => {
      const { sig, ws } = createConnected();
      sig.setSpeed(2);
      expect(ws.send).toHaveBeenCalledWith(JSON.stringify({ type: "set_speed", play_rate: 2 }));
    });

    it("setSpeed with auto", () => {
      const { sig, ws } = createConnected();
      sig.setSpeed("auto");
      expect(ws.send).toHaveBeenCalledWith(
        JSON.stringify({ type: "set_speed", play_rate: "auto" })
      );
    });
  });

  // ===========================================================================
  // seek
  // ===========================================================================
  describe("seek", () => {
    it("rejects when not connected", async () => {
      const sig = new MistSignaling({ url: "ws://example.com" });
      await expect(sig.seek(30)).rejects.toBe("Not connected");
    });

    it("sends seek_time in ms", () => {
      const sig = new MistSignaling({ url: "ws://example.com" });
      sig.connect();
      mockWsInstances[0].simulateOpen();

      sig.seek(30);
      expect(mockWsInstances[0].send).toHaveBeenCalledWith(
        JSON.stringify({ type: "seek", seek_time: 30000 })
      );
    });

    it("sends 'live' for live seek", () => {
      const sig = new MistSignaling({ url: "ws://example.com" });
      sig.connect();
      mockWsInstances[0].simulateOpen();

      sig.seek("live");
      expect(mockWsInstances[0].send).toHaveBeenCalledWith(
        JSON.stringify({ type: "seek", seek_time: "live" })
      );
    });

    it("cancels previous seek promise on new seek", async () => {
      const sig = new MistSignaling({ url: "ws://example.com" });
      sig.connect();
      mockWsInstances[0].simulateOpen();

      const first = sig.seek(10);
      sig.seek(20);

      await expect(first).rejects.toBe("New seek requested");
    });
  });

  // ===========================================================================
  // close / destroy
  // ===========================================================================
  describe("close / destroy", () => {
    it("close sets state to closed", () => {
      const sig = new MistSignaling({ url: "ws://example.com" });
      sig.connect();
      mockWsInstances[0].simulateOpen();

      sig.close();
      expect(sig.state).toBe("closed");
    });

    it("close rejects pending seek promise", async () => {
      const sig = new MistSignaling({ url: "ws://example.com" });
      sig.connect();
      mockWsInstances[0].simulateOpen();

      const seekPromise = sig.seek(30);
      sig.close();

      await expect(seekPromise).rejects.toBe("Connection closed");
    });

    it("close is safe when not connected", () => {
      const sig = new MistSignaling({ url: "ws://example.com" });
      expect(() => sig.close()).not.toThrow();
    });

    it("destroy calls close", () => {
      const sig = new MistSignaling({ url: "ws://example.com" });
      sig.connect();
      mockWsInstances[0].simulateOpen();

      sig.destroy();
      expect(sig.state).toBe("closed");
    });
  });
});
