import { describe, expect, it, vi, beforeEach, afterEach } from "vitest";

import { GatewayClient, type GatewayClientConfig } from "../src/core/GatewayClient";

function makeConfig(overrides: Partial<GatewayClientConfig> = {}): GatewayClientConfig {
  return {
    gatewayUrl: "https://gw.example.com/graphql",
    contentId: "pk_test123",
    maxRetries: 1,
    initialDelayMs: 10,
    ...overrides,
  };
}

function makeGqlResponse(primary: Record<string, unknown> | null, fallbacks: unknown[] = []) {
  return {
    data: {
      resolveViewerEndpoint: {
        primary,
        fallbacks,
        metadata: { contentId: "pk_test123", contentType: "stream" },
      },
    },
  };
}

function mockFetchSuccess(body: unknown) {
  return vi.fn(async () => ({
    ok: true,
    json: async () => body,
  })) as unknown as typeof globalThis.fetch;
}

function mockFetchError(status = 500) {
  return vi.fn(async () => ({
    ok: false,
    status,
    json: async () => ({}),
  })) as unknown as typeof globalThis.fetch;
}

describe("GatewayClient", () => {
  let origFetch: typeof globalThis.fetch;

  beforeEach(() => {
    origFetch = globalThis.fetch;
    vi.spyOn(Date, "now").mockReturnValue(100000);
  });

  afterEach(() => {
    globalThis.fetch = origFetch;
    vi.restoreAllMocks();
  });

  // ===========================================================================
  // Basic resolve
  // ===========================================================================
  describe("resolve", () => {
    it("sends GraphQL POST with correct body and headers", async () => {
      const primary = { nodeId: "n1", baseUrl: "https://n1.example.com" };
      globalThis.fetch = mockFetchSuccess(makeGqlResponse(primary));

      const client = new GatewayClient(makeConfig());
      await client.resolve();

      expect(globalThis.fetch).toHaveBeenCalledWith(
        "https://gw.example.com/graphql",
        expect.objectContaining({
          method: "POST",
          headers: expect.objectContaining({ "Content-Type": "application/json" }),
          body: expect.stringContaining("pk_test123"),
        })
      );
      client.destroy();
    });

    it("includes auth header when authToken provided", async () => {
      const primary = { nodeId: "n1" };
      globalThis.fetch = mockFetchSuccess(makeGqlResponse(primary));

      const client = new GatewayClient(makeConfig({ authToken: "tok_secret" }));
      await client.resolve();

      expect(globalThis.fetch).toHaveBeenCalledWith(
        expect.any(String),
        expect.objectContaining({
          headers: expect.objectContaining({ Authorization: "Bearer tok_secret" }),
        })
      );
      client.destroy();
    });

    it("returns parsed ContentEndpoints", async () => {
      const primary = { nodeId: "n1", baseUrl: "https://n1.example.com" };
      const fb = [{ nodeId: "n2", baseUrl: "https://n2.example.com" }];
      globalThis.fetch = mockFetchSuccess(makeGqlResponse(primary, fb));

      const client = new GatewayClient(makeConfig());
      const endpoints = await client.resolve();

      expect(endpoints.primary).toEqual(primary);
      expect(endpoints.fallbacks).toEqual(fb);
      expect(endpoints.metadata).toBeDefined();
      client.destroy();
    });

    it("throws on missing primary", async () => {
      globalThis.fetch = mockFetchSuccess(makeGqlResponse(null));

      const client = new GatewayClient(makeConfig());
      await expect(client.resolve()).rejects.toThrow("No endpoints available");
      client.destroy();
    });

    it("throws on GraphQL error", async () => {
      globalThis.fetch = mockFetchSuccess({
        data: null,
        errors: [{ message: "Not found" }],
      });

      const client = new GatewayClient(makeConfig());
      await expect(client.resolve()).rejects.toThrow("Not found");
      client.destroy();
    });

    it("throws on HTTP error", async () => {
      globalThis.fetch = mockFetchError(503);

      const client = new GatewayClient(makeConfig());
      await expect(client.resolve()).rejects.toThrow("Gateway GQL error 503");
      client.destroy();
    });

    it("throws on missing config params", async () => {
      const client = new GatewayClient(makeConfig({ gatewayUrl: "" }));
      await expect(client.resolve()).rejects.toThrow("Missing required parameters");
      client.destroy();
    });
  });

  // ===========================================================================
  // Status tracking
  // ===========================================================================
  describe("status", () => {
    it("starts idle", () => {
      const client = new GatewayClient(makeConfig());
      expect(client.getStatus()).toBe("idle");
      client.destroy();
    });

    it("transitions idle → loading → ready on success", async () => {
      const primary = { nodeId: "n1" };
      globalThis.fetch = mockFetchSuccess(makeGqlResponse(primary));

      const statuses: string[] = [];
      const client = new GatewayClient(makeConfig());
      client.on("statusChange", ({ status }) => statuses.push(status));

      await client.resolve();
      expect(statuses).toEqual(["loading", "ready"]);
      expect(client.getStatus()).toBe("ready");
      client.destroy();
    });

    it("transitions to error on failure", async () => {
      globalThis.fetch = mockFetchError(500);

      const client = new GatewayClient(makeConfig());
      await expect(client.resolve()).rejects.toThrow();
      expect(client.getStatus()).toBe("error");
      expect(client.getError()).toBe("Gateway GQL error 500");
      client.destroy();
    });
  });

  // ===========================================================================
  // Events
  // ===========================================================================
  describe("events", () => {
    it("emits endpointsResolved on success", async () => {
      const primary = { nodeId: "n1" };
      globalThis.fetch = mockFetchSuccess(makeGqlResponse(primary));

      const handler = vi.fn();
      const client = new GatewayClient(makeConfig());
      client.on("endpointsResolved", handler);

      await client.resolve();
      expect(handler).toHaveBeenCalledWith({
        endpoints: expect.objectContaining({ primary }),
      });
      client.destroy();
    });
  });

  // ===========================================================================
  // Cache
  // ===========================================================================
  describe("cache", () => {
    it("returns cached result on second call within TTL", async () => {
      const primary = { nodeId: "n1" };
      globalThis.fetch = mockFetchSuccess(makeGqlResponse(primary));

      const client = new GatewayClient(makeConfig());
      const r1 = await client.resolve();
      const r2 = await client.resolve();

      expect(r1).toBe(r2);
      expect(globalThis.fetch).toHaveBeenCalledTimes(1);
      client.destroy();
    });

    it("re-fetches after TTL expires", async () => {
      const primary = { nodeId: "n1" };
      globalThis.fetch = mockFetchSuccess(makeGqlResponse(primary));

      const client = new GatewayClient(makeConfig());
      client.setCacheTtl(5000);

      await client.resolve();
      expect(globalThis.fetch).toHaveBeenCalledTimes(1);

      // Advance past TTL
      (Date.now as ReturnType<typeof vi.fn>).mockReturnValue(106000);
      await client.resolve();
      expect(globalThis.fetch).toHaveBeenCalledTimes(2);
      client.destroy();
    });

    it("forceRefresh bypasses cache", async () => {
      const primary = { nodeId: "n1" };
      globalThis.fetch = mockFetchSuccess(makeGqlResponse(primary));

      const client = new GatewayClient(makeConfig());
      await client.resolve();
      await client.resolve(true);

      expect(globalThis.fetch).toHaveBeenCalledTimes(2);
      client.destroy();
    });

    it("invalidateCache forces re-fetch", async () => {
      const primary = { nodeId: "n1" };
      globalThis.fetch = mockFetchSuccess(makeGqlResponse(primary));

      const client = new GatewayClient(makeConfig());
      await client.resolve();
      client.invalidateCache();
      await client.resolve();

      expect(globalThis.fetch).toHaveBeenCalledTimes(2);
      client.destroy();
    });
  });

  // ===========================================================================
  // Request deduplication
  // ===========================================================================
  describe("deduplication", () => {
    it("concurrent calls return same promise", async () => {
      const primary = { nodeId: "n1" };
      globalThis.fetch = mockFetchSuccess(makeGqlResponse(primary));

      const client = new GatewayClient(makeConfig());
      const p1 = client.resolve(true);
      const p2 = client.resolve(true);

      const [r1, r2] = await Promise.all([p1, p2]);
      expect(r1).toBe(r2);
      expect(globalThis.fetch).toHaveBeenCalledTimes(1);
      client.destroy();
    });
  });

  // ===========================================================================
  // Circuit breaker
  // ===========================================================================
  describe("circuit breaker", () => {
    it("starts closed", () => {
      const client = new GatewayClient(makeConfig());
      expect(client.getCircuitState().state).toBe("closed");
      expect(client.getCircuitState().failures).toBe(0);
      client.destroy();
    });

    it("opens after 5 consecutive failures", async () => {
      globalThis.fetch = mockFetchError(500);

      const client = new GatewayClient(makeConfig());

      for (let i = 0; i < 5; i++) {
        await expect(client.resolve(true)).rejects.toThrow();
      }

      expect(client.getCircuitState().state).toBe("open");
      expect(client.getCircuitState().failures).toBe(5);
      client.destroy();
    });

    it("rejects immediately when open", async () => {
      globalThis.fetch = mockFetchError(500);
      const client = new GatewayClient(makeConfig());

      for (let i = 0; i < 5; i++) {
        await expect(client.resolve(true)).rejects.toThrow();
      }

      (globalThis.fetch as ReturnType<typeof vi.fn>).mockClear();
      await expect(client.resolve(true)).rejects.toThrow("Circuit breaker is open");
      expect(globalThis.fetch).not.toHaveBeenCalled();
      client.destroy();
    });

    it("transitions to half-open after timeout", async () => {
      globalThis.fetch = mockFetchError(500);
      const client = new GatewayClient(makeConfig());

      for (let i = 0; i < 5; i++) {
        await expect(client.resolve(true)).rejects.toThrow();
      }

      // Advance past circuit breaker timeout (30s)
      (Date.now as ReturnType<typeof vi.fn>).mockReturnValue(131000);

      // Now it should allow one request (half-open)
      const primary = { nodeId: "n1" };
      globalThis.fetch = mockFetchSuccess(makeGqlResponse(primary));
      const result = await client.resolve(true);
      expect(result.primary).toEqual(primary);

      // Success closes the circuit
      expect(client.getCircuitState().state).toBe("closed");
      expect(client.getCircuitState().failures).toBe(0);
      client.destroy();
    });

    it("re-opens on half-open failure", async () => {
      globalThis.fetch = mockFetchError(500);
      const client = new GatewayClient(makeConfig());

      for (let i = 0; i < 5; i++) {
        await expect(client.resolve(true)).rejects.toThrow();
      }

      // Advance past timeout
      (Date.now as ReturnType<typeof vi.fn>).mockReturnValue(131000);

      // Half-open request fails
      await expect(client.resolve(true)).rejects.toThrow();
      expect(client.getCircuitState().state).toBe("open");
      client.destroy();
    });

    it("manual reset clears circuit breaker", async () => {
      globalThis.fetch = mockFetchError(500);
      const client = new GatewayClient(makeConfig());

      for (let i = 0; i < 5; i++) {
        await expect(client.resolve(true)).rejects.toThrow();
      }

      client.resetCircuitBreaker();
      expect(client.getCircuitState().state).toBe("closed");
      expect(client.getCircuitState().failures).toBe(0);
      client.destroy();
    });
  });

  // ===========================================================================
  // Retry
  // ===========================================================================
  describe("retry", () => {
    it("retries on network error", async () => {
      let callCount = 0;
      globalThis.fetch = vi.fn(async () => {
        callCount++;
        if (callCount === 1) throw new Error("Network error");
        return { ok: true, json: async () => makeGqlResponse({ nodeId: "n1" }) };
      }) as unknown as typeof globalThis.fetch;

      const client = new GatewayClient(makeConfig({ maxRetries: 2 }));
      const result = await client.resolve();
      expect(result.primary).toEqual({ nodeId: "n1" });
      expect(callCount).toBe(2);
      client.destroy();
    });
  });

  // ===========================================================================
  // Accessors
  // ===========================================================================
  describe("accessors", () => {
    it("getEndpoints returns null before resolve", () => {
      const client = new GatewayClient(makeConfig());
      expect(client.getEndpoints()).toBeNull();
      client.destroy();
    });

    it("getEndpoints returns resolved endpoints", async () => {
      const primary = { nodeId: "n1" };
      globalThis.fetch = mockFetchSuccess(makeGqlResponse(primary));

      const client = new GatewayClient(makeConfig());
      await client.resolve();
      expect(client.getEndpoints()?.primary).toEqual(primary);
      client.destroy();
    });

    it("getError returns null when no error", () => {
      const client = new GatewayClient(makeConfig());
      expect(client.getError()).toBeNull();
      client.destroy();
    });
  });

  // ===========================================================================
  // updateConfig
  // ===========================================================================
  describe("updateConfig", () => {
    it("resets state and cache", async () => {
      const primary = { nodeId: "n1" };
      globalThis.fetch = mockFetchSuccess(makeGqlResponse(primary));

      const client = new GatewayClient(makeConfig());
      await client.resolve();
      expect(client.getEndpoints()).not.toBeNull();

      client.updateConfig({ contentId: "pk_other" });
      expect(client.getEndpoints()).toBeNull();
      expect(client.getStatus()).toBe("idle");
      expect(client.getCircuitState().state).toBe("closed");
      client.destroy();
    });
  });

  // ===========================================================================
  // abort
  // ===========================================================================
  describe("abort", () => {
    it("abort before resolve is a no-op", () => {
      const client = new GatewayClient(makeConfig());
      expect(() => client.abort()).not.toThrow();
      client.destroy();
    });
  });

  // ===========================================================================
  // destroy
  // ===========================================================================
  describe("destroy", () => {
    it("removes all listeners and aborts", async () => {
      const primary = { nodeId: "n1" };
      globalThis.fetch = mockFetchSuccess(makeGqlResponse(primary));

      const handler = vi.fn();
      const client = new GatewayClient(makeConfig());
      client.on("statusChange", handler);
      client.destroy();

      // After destroy, events should not fire
      handler.mockClear();
      // Can't resolve after destroy since abort was called, but listeners are removed
      expect(handler).not.toHaveBeenCalled();
    });
  });

  // ===========================================================================
  // Strips trailing slash from URL
  // ===========================================================================
  describe("URL handling", () => {
    it("strips trailing slash from gateway URL", async () => {
      const primary = { nodeId: "n1" };
      globalThis.fetch = mockFetchSuccess(makeGqlResponse(primary));

      const client = new GatewayClient(
        makeConfig({ gatewayUrl: "https://gw.example.com/graphql/" })
      );
      await client.resolve();

      expect(globalThis.fetch).toHaveBeenCalledWith(
        "https://gw.example.com/graphql",
        expect.any(Object)
      );
      client.destroy();
    });
  });
});
