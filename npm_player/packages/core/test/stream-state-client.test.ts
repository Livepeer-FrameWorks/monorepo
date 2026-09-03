import { describe, expect, it, vi, beforeEach, afterEach } from "vitest";

import { StreamStateClient, type StreamStateClientConfig } from "../src/core/StreamStateClient";

function makeConfig(overrides: Partial<StreamStateClientConfig> = {}): StreamStateClientConfig {
  return {
    mistBaseUrl: "https://mist.example.com",
    streamName: "test-stream",
    pollInterval: 3000,
    useWebSocket: false,
    ...overrides,
  };
}

function makeMistResponse(online: boolean, extra: Record<string, unknown> = {}) {
  if (online) {
    return {
      type: "live",
      hasVideo: true,
      hasAudio: true,
      meta: { tracks: { video1: { type: "video", codec: "H264" } } },
      source: [{ url: "https://mist.example.com/hls/test.m3u8", relurl: "/hls/test.m3u8" }],
      ...extra,
    };
  }
  return {
    error: "Stream is offline",
    ...extra,
  };
}

function mockFetch(body: unknown) {
  return vi.fn(async () => ({
    ok: true,
    text: async () => JSON.stringify(body),
  })) as unknown as typeof globalThis.fetch;
}

function mockFetchJsonp(body: unknown) {
  return vi.fn(async () => ({
    ok: true,
    text: async () => `mistCallback(${JSON.stringify(body)});`,
  })) as unknown as typeof globalThis.fetch;
}

function mockFetchError(status = 500) {
  return vi.fn(async () => ({
    ok: false,
    status,
    text: async () => "Server Error",
  })) as unknown as typeof globalThis.fetch;
}

describe("StreamStateClient", () => {
  let origFetch: typeof globalThis.fetch;

  beforeEach(() => {
    vi.useFakeTimers();
    origFetch = globalThis.fetch;
  });

  afterEach(() => {
    globalThis.fetch = origFetch;
    vi.useRealTimers();
    vi.restoreAllMocks();
  });

  // ===========================================================================
  // Initial state
  // ===========================================================================
  describe("initial state", () => {
    it("starts offline", () => {
      const client = new StreamStateClient(makeConfig());
      expect(client.getState().status).toBe("OFFLINE");
      expect(client.isOnline()).toBe(false);
    });
  });

  // ===========================================================================
  // HTTP polling
  // ===========================================================================
  describe("HTTP polling", () => {
    it("does not apply an HTTP response from a superseded stream configuration", async () => {
      let resolveFirst!: (response: { ok: boolean; text: () => Promise<string> }) => void;
      const first = new Promise<{ ok: boolean; text: () => Promise<string> }>((resolve) => {
        resolveFirst = resolve;
      });
      globalThis.fetch = vi
        .fn()
        .mockImplementationOnce(() => first)
        .mockResolvedValueOnce({
          ok: true,
          text: async () => JSON.stringify(makeMistResponse(true, { streamName: "stream-b" })),
        }) as unknown as typeof globalThis.fetch;
      const client = new StreamStateClient(makeConfig({ streamName: "stream-a" }));

      client.start();
      await vi.advanceTimersByTimeAsync(150);
      client.updateConfig({ streamName: "stream-b" });
      await vi.advanceTimersByTimeAsync(150);
      expect(client.getState().streamInfo).toMatchObject({ streamName: "stream-b" });

      resolveFirst({
        ok: true,
        text: async () => JSON.stringify(makeMistResponse(false, { error: "stale stream-a" })),
      });
      await Promise.resolve();
      await Promise.resolve();

      expect(client.getState().status).toBe("ONLINE");
      expect(client.getState().streamInfo).toMatchObject({ streamName: "stream-b" });
      client.destroy();
    });

    it("polls stream info on start", async () => {
      globalThis.fetch = mockFetch(makeMistResponse(true));
      const client = new StreamStateClient(makeConfig());

      client.start();
      // Advance past debounce (100ms)
      await vi.advanceTimersByTimeAsync(150);

      expect(globalThis.fetch).toHaveBeenCalledWith(
        expect.stringContaining("json_test-stream.js"),
        expect.any(Object)
      );
      expect(client.isOnline()).toBe(true);
      expect(client.getState().status).toBe("ONLINE");
      client.destroy();
    });

    it("stamps the optional client session parameter on HTTP info requests", async () => {
      globalThis.fetch = mockFetch(makeMistResponse(true));
      const client = new StreamStateClient(
        makeConfig({ sessionParam: () => ({ name: "fwsid", value: "attach-1" }) })
      );
      client.start();
      await vi.advanceTimersByTimeAsync(150);

      expect(globalThis.fetch).toHaveBeenCalledWith(
        expect.stringContaining("fwsid=attach-1"),
        expect.any(Object)
      );
      client.destroy();
    });

    it("parses JSONP response", async () => {
      globalThis.fetch = mockFetchJsonp(makeMistResponse(true));
      const client = new StreamStateClient(makeConfig());

      client.start();
      await vi.advanceTimersByTimeAsync(150);

      expect(client.isOnline()).toBe(true);
      client.destroy();
    });

    it("schedules repeat polls", async () => {
      globalThis.fetch = mockFetch(makeMistResponse(true));
      const client = new StreamStateClient(makeConfig({ pollInterval: 1000 }));

      client.start();
      await vi.advanceTimersByTimeAsync(150);
      expect(globalThis.fetch).toHaveBeenCalledTimes(1);

      await vi.advanceTimersByTimeAsync(1000);
      expect(globalThis.fetch).toHaveBeenCalledTimes(2);

      client.destroy();
    });

    it("handles HTTP error", async () => {
      globalThis.fetch = mockFetchError(500);
      const client = new StreamStateClient(makeConfig());
      const errorHandler = vi.fn();
      client.on("error", errorHandler);

      client.start();
      await vi.advanceTimersByTimeAsync(150);

      expect(client.getState().status).toBe("ERROR");
      expect(errorHandler).toHaveBeenCalledWith(
        expect.objectContaining({ error: expect.any(String) })
      );
      client.destroy();
    });

    it("handles network error", async () => {
      globalThis.fetch = vi.fn(async () => {
        throw new Error("Network failure");
      }) as unknown as typeof globalThis.fetch;

      const client = new StreamStateClient(makeConfig());
      client.start();
      await vi.advanceTimersByTimeAsync(150);

      expect(client.getState().status).toBe("ERROR");
      expect(client.getState().error).toBe("Network failure");
      client.destroy();
    });
  });

  // ===========================================================================
  // Stream state parsing
  // ===========================================================================
  describe("stream state parsing", () => {
    it("parses online stream", async () => {
      globalThis.fetch = mockFetch(makeMistResponse(true));
      const client = new StreamStateClient(makeConfig());

      client.start();
      await vi.advanceTimersByTimeAsync(150);

      const state = client.getState();
      expect(state.status).toBe("ONLINE");
      expect(state.isOnline).toBe(true);
      expect(state.streamInfo?.source).toBeDefined();
      client.destroy();
    });

    it("parses offline stream", async () => {
      globalThis.fetch = mockFetch(makeMistResponse(false));
      const client = new StreamStateClient(makeConfig());

      client.start();
      await vi.advanceTimersByTimeAsync(150);

      expect(client.getState().status).toBe("OFFLINE");
      expect(client.isOnline()).toBe(false);
      client.destroy();
    });

    it("parses initializing stream with percentage", async () => {
      globalThis.fetch = mockFetch({
        error: "Stream is initializing",
        perc: 42.5,
      });
      const client = new StreamStateClient(makeConfig());

      client.start();
      await vi.advanceTimersByTimeAsync(150);

      const state = client.getState();
      expect(state.status).toBe("INITIALIZING");
      expect(state.percentage).toBe(42.5);
      client.destroy();
    });

    it.each([
      ["Stream is offline", "OFFLINE"],
      ["Stream is initializing", "INITIALIZING"],
      ["booting", "BOOTING"],
      ["waiting for data", "WAITING_FOR_DATA"],
      ["shutting down", "SHUTTING_DOWN"],
      ["invalid config", "INVALID"],
      ["some random error", "ERROR"],
    ])("parses error '%s' → status %s", async (error, expected) => {
      globalThis.fetch = mockFetch({ error });
      const client = new StreamStateClient(makeConfig());

      client.start();
      await vi.advanceTimersByTimeAsync(150);

      expect(client.getState().status).toBe(expected);
      client.destroy();
    });
  });

  // ===========================================================================
  // Events
  // ===========================================================================
  describe("events", () => {
    it("emits stateChange", async () => {
      globalThis.fetch = mockFetch(makeMistResponse(true));
      const handler = vi.fn();
      const client = new StreamStateClient(makeConfig());
      client.on("stateChange", handler);

      client.start();
      await vi.advanceTimersByTimeAsync(150);

      // First stateChange is "Connecting...", second is ONLINE
      expect(handler).toHaveBeenCalled();
      const lastCall = handler.mock.calls[handler.mock.calls.length - 1][0];
      expect(lastCall.state.status).toBe("ONLINE");
      client.destroy();
    });

    it("emits online on OFFLINE→ONLINE transition", async () => {
      globalThis.fetch = mockFetch(makeMistResponse(true));
      const onlineHandler = vi.fn();
      const client = new StreamStateClient(makeConfig());
      client.on("online", onlineHandler);

      client.start();
      await vi.advanceTimersByTimeAsync(150);

      expect(onlineHandler).toHaveBeenCalledTimes(1);
      client.destroy();
    });

    it("emits offline on ONLINE→OFFLINE transition", async () => {
      globalThis.fetch = mockFetch(makeMistResponse(true));
      const offlineHandler = vi.fn();
      const client = new StreamStateClient(makeConfig({ pollInterval: 500 }));
      client.on("offline", offlineHandler);

      client.start();
      await vi.advanceTimersByTimeAsync(150);
      expect(client.isOnline()).toBe(true);

      // Next poll returns offline
      globalThis.fetch = mockFetch(makeMistResponse(false));
      await vi.advanceTimersByTimeAsync(500);

      expect(offlineHandler).toHaveBeenCalledTimes(1);
      client.destroy();
    });
  });

  // ===========================================================================
  // Stream info merging
  // ===========================================================================
  describe("stream info merging", () => {
    it("preserves source from initial fetch when update lacks it", async () => {
      // First poll has source
      globalThis.fetch = mockFetch(makeMistResponse(true));
      const client = new StreamStateClient(makeConfig({ pollInterval: 500 }));

      client.start();
      await vi.advanceTimersByTimeAsync(150);

      const source = client.getState().streamInfo?.source;
      expect(source).toBeDefined();
      expect(source!.length).toBeGreaterThan(0);

      // Second poll has no source but is still online
      globalThis.fetch = mockFetch({ type: "live", hasVideo: true });
      await vi.advanceTimersByTimeAsync(500);

      // Source preserved from first fetch
      expect(client.getState().streamInfo?.source).toEqual(source);
      client.destroy();
    });
  });

  // ===========================================================================
  // Connection debouncing
  // ===========================================================================
  describe("debouncing", () => {
    it("rapid start/stop does not connect", async () => {
      globalThis.fetch = mockFetch(makeMistResponse(true));
      const client = new StreamStateClient(makeConfig());

      client.start();
      client.stop();
      await vi.advanceTimersByTimeAsync(200);

      expect(globalThis.fetch).not.toHaveBeenCalled();
      client.destroy();
    });
  });

  // ===========================================================================
  // stop
  // ===========================================================================
  describe("stop", () => {
    it("stops polling", async () => {
      globalThis.fetch = mockFetch(makeMistResponse(true));
      const client = new StreamStateClient(makeConfig({ pollInterval: 500 }));

      client.start();
      await vi.advanceTimersByTimeAsync(150);
      expect(globalThis.fetch).toHaveBeenCalledTimes(1);

      client.stop();
      await vi.advanceTimersByTimeAsync(2000);
      expect(globalThis.fetch).toHaveBeenCalledTimes(1);
    });

    it("double start is idempotent", async () => {
      globalThis.fetch = mockFetch(makeMistResponse(true));
      const client = new StreamStateClient(makeConfig());

      client.start();
      client.start();
      await vi.advanceTimersByTimeAsync(150);

      expect(globalThis.fetch).toHaveBeenCalledTimes(1);
      client.destroy();
    });
  });

  // ===========================================================================
  // getState returns copy
  // ===========================================================================
  describe("getState", () => {
    it("returns a copy of state", () => {
      const client = new StreamStateClient(makeConfig());
      const s1 = client.getState();
      const s2 = client.getState();
      expect(s1).toEqual(s2);
      expect(s1).not.toBe(s2);
    });
  });

  // ===========================================================================
  // updateConfig
  // ===========================================================================
  describe("updateConfig", () => {
    it("restarts when running", async () => {
      globalThis.fetch = mockFetch(makeMistResponse(true));
      const client = new StreamStateClient(makeConfig());

      client.start();
      await vi.advanceTimersByTimeAsync(150);
      expect(client.isOnline()).toBe(true);

      client.updateConfig({ streamName: "new-stream" });
      await vi.advanceTimersByTimeAsync(150);

      expect(globalThis.fetch).toHaveBeenLastCalledWith(
        expect.stringContaining("json_new-stream.js"),
        expect.any(Object)
      );
      client.destroy();
    });
  });

  // ===========================================================================
  // Missing config
  // ===========================================================================
  describe("missing config", () => {
    it("warns and does not fetch when mistBaseUrl missing", async () => {
      const warnSpy = vi.spyOn(console, "warn").mockImplementation(() => {});
      const client = new StreamStateClient(makeConfig({ mistBaseUrl: "" }));
      globalThis.fetch = mockFetch(makeMistResponse(true));

      client.start();
      await vi.advanceTimersByTimeAsync(200);

      expect(globalThis.fetch).not.toHaveBeenCalled();
      warnSpy.mockRestore();
      client.destroy();
    });
  });

  // ===========================================================================
  // destroy
  // ===========================================================================
  describe("destroy", () => {
    it("stops and removes listeners", async () => {
      const handler = vi.fn();
      const client = new StreamStateClient(makeConfig());
      client.on("stateChange", handler);
      client.destroy();

      globalThis.fetch = mockFetch(makeMistResponse(true));
      client.start();
      await vi.advanceTimersByTimeAsync(200);

      // Listeners removed, so handler should not be called after destroy
      // (start after destroy may still run, but events won't fire to removed listeners)
      expect(handler).not.toHaveBeenCalled();
    });
  });

  // ===========================================================================
  // WebSocket connection + HTTP fallback (StreamStateClient.ts:283-341)
  // ===========================================================================
  describe("WebSocket", () => {
    class MockWebSocket {
      static CONNECTING = 0;
      static OPEN = 1;
      static CLOSED = 3;
      static instances: MockWebSocket[] = [];
      static throwOnConstruct = false;

      readyState = MockWebSocket.CONNECTING;
      url: string;
      onopen: (() => void) | null = null;
      onmessage: ((event: { data: string }) => void) | null = null;
      onerror: (() => void) | null = null;
      onclose: (() => void) | null = null;
      close = vi.fn(() => {
        this.readyState = MockWebSocket.CLOSED;
        this.onclose?.();
      });

      constructor(url: string) {
        if (MockWebSocket.throwOnConstruct) throw new Error("ws construct boom");
        this.url = url;
        MockWebSocket.instances.push(this);
      }
    }

    beforeEach(() => {
      MockWebSocket.instances = [];
      MockWebSocket.throwOnConstruct = false;
      vi.stubGlobal("WebSocket", MockWebSocket);
    });

    afterEach(() => {
      vi.unstubAllGlobals();
    });

    // Drives start() → debounce → initial HTTP poll → connectWebSocket().
    async function startWithWs(online = true) {
      globalThis.fetch = online
        ? mockFetch(makeMistResponse(true))
        : mockFetch(makeMistResponse(false));
      const client = new StreamStateClient(makeConfig({ useWebSocket: true, pollInterval: 500 }));
      client.start();
      await vi.advanceTimersByTimeAsync(150);
      if (MockWebSocket.instances[0]) MockWebSocket.instances[0].readyState = MockWebSocket.OPEN;
      MockWebSocket.instances[0]?.onopen?.();
      return client;
    }

    it("connects a ws(s) socket after the initial poll and exposes it", async () => {
      const client = await startWithWs();
      expect(MockWebSocket.instances).toHaveLength(1);
      const sock = MockWebSocket.instances[0];
      // http(s) base is rewritten to ws(s); stream name is in the path.
      expect(sock.url).toMatch(/^wss:\/\//);
      expect(sock.url).toContain("json_test-stream.js");
      expect(client.getSocket()).toBe(sock as unknown as WebSocket);
      expect(client.isSocketReady()).toBe(true);
      client.destroy();
    });

    it("stamps the optional client session parameter on WebSocket info requests", async () => {
      globalThis.fetch = mockFetch(makeMistResponse(true));
      const client = new StreamStateClient(
        makeConfig({
          useWebSocket: true,
          sessionParam: () => ({ name: "fwsid", value: "attach-ws" }),
        })
      );
      client.start();
      await vi.advanceTimersByTimeAsync(150);

      expect(MockWebSocket.instances[0].url).toContain("fwsid=attach-ws");
      client.destroy();
    });

    it("applies stream info pushed over the socket", async () => {
      // Initial poll is offline so the ONLINE state can only come from the WS message.
      const client = await startWithWs(false);
      expect(client.isOnline()).toBe(false);

      MockWebSocket.instances[0].onmessage!({ data: JSON.stringify(makeMistResponse(true)) });
      expect(client.isOnline()).toBe(true);
      expect(client.getState().status).toBe("ONLINE");
      client.destroy();
    });

    it("swallows a malformed socket message without changing state", async () => {
      const warnSpy = vi.spyOn(console, "warn").mockImplementation(() => {});
      const client = await startWithWs();
      const before = client.getState();

      MockWebSocket.instances[0].onmessage!({ data: "{ not json" });
      expect(client.getState()).toEqual(before);
      expect(warnSpy).toHaveBeenCalled();
      warnSpy.mockRestore();
      client.destroy();
    });

    it("closes the socket on error", async () => {
      const client = await startWithWs();
      const sock = MockWebSocket.instances[0];
      sock.onerror!();
      expect(sock.close).toHaveBeenCalled();
      client.destroy();
    });

    it("falls back to HTTP polling when the socket closes", async () => {
      const client = await startWithWs();
      expect(globalThis.fetch).toHaveBeenCalledTimes(1); // initial poll only

      MockWebSocket.instances[0].onclose!();
      await vi.advanceTimersByTimeAsync(0); // flush the fallback poll
      expect(globalThis.fetch).toHaveBeenCalledTimes(2);
      expect(client.getSocket()).toBeNull();

      // HTTP polling remains active while WebSocket reconnect is pending.
      await vi.advanceTimersByTimeAsync(500);
      expect(globalThis.fetch).toHaveBeenCalledTimes(3);
      client.destroy();
    });

    it("does not let a stale fallback poll overwrite a reconnected socket", async () => {
      vi.spyOn(Math, "random").mockReturnValue(0.5);
      const client = await startWithWs();
      let resolvePoll!: (response: { ok: boolean; text: () => Promise<string> }) => void;
      const stalePoll = new Promise<{ ok: boolean; text: () => Promise<string> }>((resolve) => {
        resolvePoll = resolve;
      });
      vi.mocked(globalThis.fetch).mockImplementationOnce(() => stalePoll as Promise<Response>);

      MockWebSocket.instances[0].onclose!();
      await vi.advanceTimersByTimeAsync(1000);
      const reconnected = MockWebSocket.instances[1];
      reconnected.readyState = MockWebSocket.OPEN;
      reconnected.onopen!();
      reconnected.onmessage!({ data: JSON.stringify(makeMistResponse(true)) });

      resolvePoll({
        ok: true,
        text: async () => JSON.stringify(makeMistResponse(false, { error: "stale poll" })),
      });
      await Promise.resolve();
      await Promise.resolve();

      expect(client.getState().status).toBe("ONLINE");
      client.destroy();
    });

    it("does not emit a stale fallback error after socket reconnect", async () => {
      vi.spyOn(Math, "random").mockReturnValue(0.5);
      const client = await startWithWs();
      const errorHandler = vi.fn();
      client.on("error", errorHandler);
      let rejectPoll!: (error: Error) => void;
      const stalePoll = new Promise<Response>((_resolve, reject) => {
        rejectPoll = reject;
      });
      vi.mocked(globalThis.fetch).mockImplementationOnce(() => stalePoll);

      MockWebSocket.instances[0].onclose!();
      await vi.advanceTimersByTimeAsync(1000);
      const reconnected = MockWebSocket.instances[1];
      reconnected.readyState = MockWebSocket.OPEN;
      reconnected.onopen!();
      reconnected.onmessage!({ data: JSON.stringify(makeMistResponse(true)) });
      rejectPoll(new Error("stale network failure"));
      await Promise.resolve();
      await Promise.resolve();

      expect(errorHandler).not.toHaveBeenCalled();
      expect(client.getState().status).toBe("ONLINE");
      client.destroy();
    });

    it("reconnects with exponential backoff while polling continues", async () => {
      vi.spyOn(Math, "random").mockReturnValue(0.5);
      const client = await startWithWs();

      MockWebSocket.instances[0].onclose!();
      await vi.advanceTimersByTimeAsync(999);
      expect(MockWebSocket.instances).toHaveLength(1);

      await vi.advanceTimersByTimeAsync(1);
      expect(MockWebSocket.instances).toHaveLength(2);

      MockWebSocket.instances[1].onclose!();
      await vi.advanceTimersByTimeAsync(1999);
      expect(MockWebSocket.instances).toHaveLength(2);

      await vi.advanceTimersByTimeAsync(1);
      expect(MockWebSocket.instances).toHaveLength(3);
      expect(globalThis.fetch).toHaveBeenCalled();
      client.destroy();
    });

    it("does not add an immediate HTTP request for every failed reconnect", async () => {
      vi.spyOn(Math, "random").mockReturnValue(0.5);
      const client = await startWithWs();

      MockWebSocket.instances[0].onclose!();
      await vi.advanceTimersByTimeAsync(0);
      expect(globalThis.fetch).toHaveBeenCalledTimes(2);

      await vi.advanceTimersByTimeAsync(1000);
      const requestsBeforeFailedReconnect = vi.mocked(globalThis.fetch).mock.calls.length;
      MockWebSocket.instances[1].onclose!();

      expect(globalThis.fetch).toHaveBeenCalledTimes(requestsBeforeFailedReconnect);
      client.destroy();
    });

    it("resets reconnect backoff only after a healthy socket interval", async () => {
      vi.spyOn(Math, "random").mockReturnValue(0.5);
      const client = await startWithWs();

      MockWebSocket.instances[0].onclose!();
      await vi.advanceTimersByTimeAsync(1000);
      const reconnected = MockWebSocket.instances[1];
      reconnected.readyState = MockWebSocket.OPEN;
      reconnected.onopen!();
      await vi.advanceTimersByTimeAsync(60000);

      reconnected.onclose!();
      await vi.advanceTimersByTimeAsync(999);
      expect(MockWebSocket.instances).toHaveLength(2);
      await vi.advanceTimersByTimeAsync(1);
      expect(MockWebSocket.instances).toHaveLength(3);
      client.destroy();
    });

    it("does not reconnect after stop", async () => {
      vi.spyOn(Math, "random").mockReturnValue(0.5);
      const client = await startWithWs();
      MockWebSocket.instances[0].onclose!();
      client.stop();

      await vi.advanceTimersByTimeAsync(30000);
      expect(MockWebSocket.instances).toHaveLength(1);
      client.destroy();
    });

    it("falls back to HTTP when the socket constructor throws", async () => {
      MockWebSocket.throwOnConstruct = true;
      const warnSpy = vi.spyOn(console, "warn").mockImplementation(() => {});
      const client = await startWithWs();

      expect(client.getSocket()).toBeNull();
      expect(client.isSocketReady()).toBe(false);
      // The initial HTTP fallback timer remains authoritative; a failed socket
      // construction must not add a duplicate immediate request.
      expect(globalThis.fetch).toHaveBeenCalledTimes(1);
      warnSpy.mockRestore();
      client.destroy();
    });

    it("refresh() is a no-op while the socket is open, but polls otherwise", async () => {
      const client = await startWithWs();
      (globalThis.fetch as ReturnType<typeof vi.fn>).mockClear();

      client.refresh(); // socket open → updates arrive passively
      expect(globalThis.fetch).not.toHaveBeenCalled();

      // Drop the socket: refresh must now actively poll.
      MockWebSocket.instances[0].onclose!();
      await vi.advanceTimersByTimeAsync(0);
      (globalThis.fetch as ReturnType<typeof vi.fn>).mockClear();
      client.refresh();
      expect(globalThis.fetch).toHaveBeenCalledTimes(1);
      client.destroy();
    });
  });
});
