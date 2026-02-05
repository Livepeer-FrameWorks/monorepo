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
});
