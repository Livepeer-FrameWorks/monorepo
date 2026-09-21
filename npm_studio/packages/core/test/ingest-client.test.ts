import { describe, expect, it, vi, beforeEach, afterEach } from "vitest";

import { ResolveIngestEndpointDocument } from "@livepeer-frameworks/api";
import { IngestClient } from "../src/core/IngestClient";
import { clearServerInfoProbes, probeServerInfo } from "@livepeer-frameworks/api/gateway-probe";

const MOCK_GATEWAY_URL = "https://gateway.example.com/graphql";
const MOCK_STREAM_KEY = "test-stream-key";
const SUPPORTED_GATEWAY = { data: { serverInfo: { version: "v0.3.11", features: [] } } };

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

  // The gateway's serverInfo is probed once and cached, so each test starts
  // with a supported gateway on record and its fetch mock sees only resolves.
  beforeEach(async () => {
    vi.useFakeTimers();
    origFetch = globalThis.fetch;
    globalThis.fetch = vi.fn(async () => new Response(JSON.stringify(SUPPORTED_GATEWAY)));
    await probeServerInfo(MOCK_GATEWAY_URL);
  });

  afterEach(() => {
    vi.useRealTimers();
    vi.restoreAllMocks();
    globalThis.fetch = origFetch;
    clearServerInfoProbes();
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
        })
      );
      const request = JSON.parse(vi.mocked(globalThis.fetch).mock.calls[0][1]!.body as string);
      expect(request.query).toBe(String(ResolveIngestEndpointDocument));
      expect(request.operationName).toBe("ResolveIngestEndpoint");
      expect(request.variables).toEqual({ streamKey: MOCK_STREAM_KEY, protocol: "WHIP" });
      client.destroy();
    });

    it.each([undefined, "", "javascript:alert(1)", "https://user:secret@node.example/whip/key"])(
      "rejects invalid WHIP primary %s without retaining a prior endpoint",
      async (whipUrl) => {
        globalThis.fetch = mockFetchSuccess();
        const client = createClient({ maxRetries: 0 });
        await client.resolve();
        const response = mockEndpointResponse();
        response.primary.whipUrl = whipUrl as string;
        globalThis.fetch = mockFetchSuccess(response);
        await expect(client.resolve()).rejects.toThrow("No valid WHIP destination was confirmed");
        expect(globalThis.fetch).toHaveBeenCalledTimes(1);
        expect(client.getWhipUrl()).toBeNull();
        client.destroy();
      }
    );

    it("does not expose wrong-protocol fallback nodes", async () => {
      const response = mockEndpointResponse();
      response.fallbacks[0].whipUrl = "";
      globalThis.fetch = mockFetchSuccess(response);
      const client = createClient();
      expect((await client.resolve()).fallbacks).toEqual([]);
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
    it("does not emit ready if a resolved-endpoint listener destroys the client", async () => {
      globalThis.fetch = mockFetchSuccess();
      const client = createClient();
      const events = vi.fn();
      client.on("statusChange", events);
      client.on("endpointsResolved", () => client.destroy());
      await expect(client.resolve()).rejects.toMatchObject({ name: "AbortError" });
      expect(events).not.toHaveBeenCalledWith({ status: "ready" });
      expect(client.getEndpoints()).toBeNull();
    });
    it("aborts in-flight request on destroy", async () => {
      globalThis.fetch = vi.fn(() => new Promise(() => {})); // never resolves
      const client = createClient();

      const resolvePromise = client.resolve();
      const rejected = expect(resolvePromise).rejects.toMatchObject({ name: "AbortError" });
      client.destroy();

      // The fetch should have been called with an AbortSignal
      expect(globalThis.fetch).toHaveBeenCalledWith(
        MOCK_GATEWAY_URL,
        expect.objectContaining({
          signal: expect.any(AbortSignal),
        })
      );
      await rejected;
    });

    it("settles a retry wait on destroy without another fetch", async () => {
      globalThis.fetch = vi.fn().mockRejectedValue(new Error("offline"));
      const client = createClient();
      const pending = client.resolve();
      const rejected = expect(pending).rejects.toMatchObject({ name: "AbortError" });
      await vi.advanceTimersByTimeAsync(1);
      client.destroy();
      await rejected;
      await vi.runAllTimersAsync();
      expect(globalThis.fetch).toHaveBeenCalledTimes(1);
      expect(vi.getTimerCount()).toBe(0);
    });

    it("bounds fetch implementations that ignore AbortSignal", async () => {
      globalThis.fetch = vi.fn(() => new Promise(() => {}));
      const client = createClient();
      const rejected = expect(client.resolve()).rejects.toThrow("resolution timed out");
      await vi.advanceTimersByTimeAsync(5000);
      await rejected;
      expect(client.getEndpoints()).toBeNull();
      client.destroy();
    });

    it("does not publish a superseded request's late response or idle state", async () => {
      let finishOld!: (value: unknown) => void;
      globalThis.fetch = vi
        .fn()
        .mockImplementationOnce(
          () =>
            new Promise((resolve) => {
              finishOld = resolve;
            })
        )
        .mockImplementationOnce(mockFetchSuccess());
      const client = createClient();
      const events = vi.fn();
      client.on("statusChange", events);
      const old = expect(client.resolve()).rejects.toMatchObject({ name: "AbortError" });
      const latest = await client.resolve();
      await old;
      finishOld({ ok: true, json: async () => ({ data: { resolveIngestEndpoint: null } }) });
      await vi.advanceTimersByTimeAsync(1);
      expect(client.getEndpoints()).toBe(latest);
      expect(events).not.toHaveBeenCalledWith({ status: "idle" });
      client.destroy();
    });

    it("does not expose gateway error details containing a stream key", async () => {
      globalThis.fetch = vi.fn().mockResolvedValue({
        ok: true,
        json: async () => ({ errors: [{ message: `denied ${MOCK_STREAM_KEY}` }] }),
      });
      const client = createClient({ maxRetries: 0 });
      const events = vi.fn();
      client.on("statusChange", events);
      await expect(client.resolve()).rejects.not.toThrow(MOCK_STREAM_KEY);
      expect(JSON.stringify(events.mock.calls)).not.toContain(MOCK_STREAM_KEY);
      client.destroy();
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
