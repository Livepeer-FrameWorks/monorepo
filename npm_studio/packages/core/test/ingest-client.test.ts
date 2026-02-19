import { describe, expect, it, vi, beforeEach, afterEach } from "vitest";

import { IngestClient } from "../src/core/IngestClient";

const MOCK_GATEWAY_URL = "https://gateway.example.com/graphql";
const MOCK_STREAM_KEY = "test-stream-key";

function mockFetchSuccess(data = mockEndpointResponse()) {
  return vi.fn().mockResolvedValue({
    ok: true,
    json: async () => ({ data: { resolveIngestEndpoint: data } }),
  });
}

function mockEndpointResponse() {
  return {
    primary: {
      nodeId: "node-1",
      baseUrl: "https://ingest1.example.com",
      whipUrl: "https://ingest1.example.com/whip/test",
      rtmpUrl: "rtmp://ingest1.example.com/live",
      srtUrl: "srt://ingest1.example.com:9710",
      region: "us-east",
      loadScore: 0.3,
    },
    fallbacks: [
      {
        nodeId: "node-2",
        baseUrl: "https://ingest2.example.com",
        whipUrl: "https://ingest2.example.com/whip/test",
        rtmpUrl: null,
        srtUrl: null,
        region: "us-west",
        loadScore: 0.7,
      },
    ],
    metadata: {
      streamId: "stream-123",
      streamKey: MOCK_STREAM_KEY,
      tenantId: "tenant-abc",
      recordingEnabled: false,
    },
  };
}

describe("IngestClient", () => {
  let origFetch: typeof globalThis.fetch;

  beforeEach(() => {
    vi.useFakeTimers();
    origFetch = globalThis.fetch;
  });

  afterEach(() => {
    vi.useRealTimers();
    vi.restoreAllMocks();
    globalThis.fetch = origFetch;
  });

  function createClient(overrides?: Record<string, any>) {
    return new IngestClient({
      gatewayUrl: MOCK_GATEWAY_URL,
      streamKey: MOCK_STREAM_KEY,
      maxRetries: 3,
      initialDelayMs: 100,
      ...overrides,
    });
  }

  // ===========================================================================
  // GraphQL resolve
  // ===========================================================================
  describe("resolve", () => {
    it("resolves endpoints via GraphQL", async () => {
      globalThis.fetch = mockFetchSuccess();
      const client = createClient();

      const endpoints = await client.resolve();

      expect(endpoints.primary.whipUrl).toBe("https://ingest1.example.com/whip/test");
      expect(endpoints.fallbacks).toHaveLength(1);
      expect(endpoints.metadata.streamId).toBe("stream-123");
      client.destroy();
    });

    it("sends correct GraphQL query with stream key", async () => {
      globalThis.fetch = mockFetchSuccess();
      const client = createClient();

      await client.resolve();

      expect(globalThis.fetch).toHaveBeenCalledWith(
        MOCK_GATEWAY_URL,
        expect.objectContaining({
          method: "POST",
          headers: expect.objectContaining({
            "Content-Type": "application/json",
          }),
          body: expect.stringContaining(MOCK_STREAM_KEY),
        })
      );
      client.destroy();
    });

    it("includes auth token header when provided", async () => {
      globalThis.fetch = mockFetchSuccess();
      const client = createClient({ authToken: "my-token" });

      await client.resolve();

      expect(globalThis.fetch).toHaveBeenCalledWith(
        MOCK_GATEWAY_URL,
        expect.objectContaining({
          headers: expect.objectContaining({
            Authorization: "Bearer my-token",
          }),
        })
      );
      client.destroy();
    });

    it("stores resolved endpoints", async () => {
      globalThis.fetch = mockFetchSuccess();
      const client = createClient();

      expect(client.getEndpoints()).toBeNull();
      await client.resolve();
      expect(client.getEndpoints()).not.toBeNull();
      client.destroy();
    });

    it("emits statusChange and endpointsResolved events", async () => {
      globalThis.fetch = mockFetchSuccess();
      const client = createClient();

      const statusHandler = vi.fn();
      const resolvedHandler = vi.fn();
      client.on("statusChange", statusHandler);
      client.on("endpointsResolved", resolvedHandler);

      await client.resolve();

      expect(statusHandler).toHaveBeenCalledWith({ status: "loading" });
      expect(statusHandler).toHaveBeenCalledWith({ status: "ready" });
      expect(resolvedHandler).toHaveBeenCalledOnce();
      client.destroy();
    });
  });

  // ===========================================================================
  // Retry with backoff
  // ===========================================================================
  describe("retry with backoff", () => {
    it("retries on HTTP error with exponential backoff", async () => {
      let callCount = 0;
      globalThis.fetch = vi.fn(async () => {
        callCount++;
        if (callCount < 3) {
          return { ok: false, status: 503, statusText: "Service Unavailable" } as Response;
        }
        return {
          ok: true,
          json: async () => ({ data: { resolveIngestEndpoint: mockEndpointResponse() } }),
        } as unknown as Response;
      });

      const client = createClient({ initialDelayMs: 100 });
      const resolvePromise = client.resolve();

      // First retry delay: 100ms
      await vi.advanceTimersByTimeAsync(100);
      // Second retry delay: 200ms
      await vi.advanceTimersByTimeAsync(200);

      const endpoints = await resolvePromise;
      expect(endpoints.primary.whipUrl).toBeDefined();
      expect(callCount).toBe(3);
      client.destroy();
    });

    it("retries on GraphQL error", async () => {
      let callCount = 0;
      globalThis.fetch = vi.fn(async () => {
        callCount++;
        if (callCount < 2) {
          return {
            ok: true,
            json: async () => ({ errors: [{ message: "stream not found" }] }),
          };
        }
        return {
          ok: true,
          json: async () => ({ data: { resolveIngestEndpoint: mockEndpointResponse() } }),
        };
      });

      const client = createClient({ initialDelayMs: 50 });
      const resolvePromise = client.resolve();
      await vi.advanceTimersByTimeAsync(100);

      const endpoints = await resolvePromise;
      expect(endpoints).toBeDefined();
      expect(callCount).toBe(2);
      client.destroy();
    });

    it("throws after exhausting retries", async () => {
      globalThis.fetch = vi.fn(async () => ({
        ok: false,
        status: 500,
        statusText: "Internal Server Error",
      }));

      const client = createClient({ maxRetries: 2, initialDelayMs: 50 });
      // Attach catch immediately to prevent unhandled rejection
      let caughtError: Error | null = null;
      const resolvePromise = client.resolve().catch((e) => {
        caughtError = e;
      });

      await vi.runAllTimersAsync();
      await resolvePromise;

      expect(caughtError).not.toBeNull();
      expect(caughtError!.message).toContain("Failed to resolve ingest endpoint");
      client.destroy();
    });

    it("emits error status after exhausting retries", async () => {
      globalThis.fetch = vi.fn(async () => ({
        ok: false,
        status: 500,
        statusText: "Server Error",
      }));

      const client = createClient({ maxRetries: 1, initialDelayMs: 50 });
      const statusHandler = vi.fn();
      client.on("statusChange", statusHandler);

      const p = client.resolve().catch(() => {}); // suppress unhandled rejection
      await vi.runAllTimersAsync();
      await p;

      expect(statusHandler).toHaveBeenCalledWith(expect.objectContaining({ status: "error" }));
      client.destroy();
    });
  });

  // ===========================================================================
  // Abort handling
  // ===========================================================================
  describe("abort handling", () => {
    it("aborts in-flight request on destroy", () => {
      globalThis.fetch = vi.fn(() => new Promise(() => {})); // never resolves
      const client = createClient();

      const resolvePromise = client.resolve();
      client.destroy();

      // The fetch should have been called with an AbortSignal
      expect(globalThis.fetch).toHaveBeenCalledWith(
        MOCK_GATEWAY_URL,
        expect.objectContaining({
          signal: expect.any(AbortSignal),
        })
      );
    });
  });

  // ===========================================================================
  // URL getters
  // ===========================================================================
  describe("URL getters", () => {
    it("getWhipUrl returns null before resolve", () => {
      const client = createClient();
      expect(client.getWhipUrl()).toBeNull();
      client.destroy();
    });

    it("getWhipUrl returns primary WHIP URL after resolve", async () => {
      globalThis.fetch = mockFetchSuccess();
      const client = createClient();
      await client.resolve();
      expect(client.getWhipUrl()).toBe("https://ingest1.example.com/whip/test");
      client.destroy();
    });

    it("getRtmpUrl returns primary RTMP URL", async () => {
      globalThis.fetch = mockFetchSuccess();
      const client = createClient();
      await client.resolve();
      expect(client.getRtmpUrl()).toBe("rtmp://ingest1.example.com/live");
      client.destroy();
    });

    it("getSrtUrl returns primary SRT URL", async () => {
      globalThis.fetch = mockFetchSuccess();
      const client = createClient();
      await client.resolve();
      expect(client.getSrtUrl()).toBe("srt://ingest1.example.com:9710");
      client.destroy();
    });
  });

  // ===========================================================================
  // Destroy
  // ===========================================================================
  describe("destroy", () => {
    it("clears endpoints on destroy", async () => {
      globalThis.fetch = mockFetchSuccess();
      const client = createClient();
      await client.resolve();
      expect(client.getEndpoints()).not.toBeNull();
      client.destroy();
      expect(client.getEndpoints()).toBeNull();
    });
  });
});
