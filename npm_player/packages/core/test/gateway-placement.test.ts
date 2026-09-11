import { afterEach, describe, expect, it, vi } from "vitest";
import { GatewayClient } from "../src/core/GatewayClient";
import { viewerProtocols, type ViewerProtocol } from "../src/core/ViewerProtocol";

function reply(protocol: ViewerProtocol) {
  const canonical = viewerProtocols[protocol];
  const url =
    protocol === "WEBRTC"
      ? "wss://us.example/webrtc/public"
      : `https://us.example/${canonical}/public`;
  const key = protocol === "WEBRTC" ? "MIST_WEBRTC" : protocol;
  return {
    data: {
      resolveViewerEndpoint: {
        primary: {
          nodeId: "us-edge",
          protocol: canonical,
          url,
          baseUrl: "https://us.example",
          outputs: { [key]: { protocol: key, url } },
        },
        fallbacks: [],
        metadata: { contentId: "public", contentType: "live" },
      },
    },
  };
}

function client(protocol: ViewerProtocol) {
  return new GatewayClient({
    contentId: "public",
    gatewayUrl: "https://gateway.example/graphql",
    protocol,
    maxRetries: 1,
  });
}

afterEach(() => {
  vi.unstubAllGlobals();
  vi.useRealTimers();
});

describe("protocol-bound gateway placement", () => {
  it("decodes primary and fallback catalogs for automatic-format clients too", async () => {
    const body = reply("HLS");
    const outputs = body.data.resolveViewerEndpoint.primary.outputs;
    (body.data.resolveViewerEndpoint.primary as any).outputs = JSON.stringify(outputs);
    (body.data.resolveViewerEndpoint as any).fallbacks = [
      {
        ...body.data.resolveViewerEndpoint.primary,
        nodeId: "fallback",
      },
    ];
    vi.stubGlobal(
      "fetch",
      vi.fn(async () => ({ ok: true, json: async () => body }))
    );
    const gateway = new GatewayClient({ contentId: "public", maxRetries: 1 });
    const endpoints = await gateway.resolve();
    expect(endpoints.primary.outputs).toEqual(outputs);
    expect(endpoints.fallbacks[0].outputs).toEqual(outputs);
    gateway.destroy();
  });

  it("decodes a JSON scalar output catalog before applying the protocol requirement", async () => {
    const body = reply("HLS");
    const encoded = JSON.stringify(body.data.resolveViewerEndpoint.primary.outputs);
    (body.data.resolveViewerEndpoint.primary as any).outputs = encoded;
    vi.stubGlobal(
      "fetch",
      vi.fn(async () => ({ ok: true, json: async () => body }))
    );
    const gateway = client("HLS");
    const endpoints = await gateway.resolve();
    expect(Object.keys(endpoints.primary.outputs!)).toEqual(["HLS"]);
    expect(body.data.resolveViewerEndpoint.primary.outputs).toBe(encoded);
    gateway.destroy();
  });

  it.each(["broken-json", "null", "[]", "42"])(
    "rejects an invalid encoded catalog: %s",
    async (encoded) => {
      const body = reply("HLS");
      (body.data.resolveViewerEndpoint.primary as any).outputs = encoded;
      const fetcher = vi.fn(async () => ({ ok: true, json: async () => body }));
      vi.stubGlobal("fetch", fetcher);
      const gateway = client("HLS");
      await expect(gateway.resolve()).rejects.toThrow("invalid playback outputs");
      expect(gateway.getEndpoints()).toBeNull();
      expect(fetcher).toHaveBeenCalledOnce();
      gateway.destroy();
    }
  );

  it("sends the typed requirement and retains only the selected prepared output", async () => {
    const body = reply("HLS");
    Object.assign(body.data.resolveViewerEndpoint.primary.outputs, {
      WHEP: { protocol: "WHEP", url: "https://other.example/whep/public" },
    });
    const fetcher = vi.fn(async (_url: RequestInfo | URL, _init?: RequestInit) => ({
      ok: true,
      json: async () => body,
    }));
    vi.stubGlobal("fetch", fetcher);
    const gateway = client("HLS");
    const endpoints = await gateway.resolve();
    const request = JSON.parse(fetcher.mock.calls[0][1]?.body as string);
    expect(request.variables).toEqual({ contentId: "public", protocol: "HLS" });
    expect(request.query).toContain("$protocol: MediaViewerProtocol!");
    expect(request.query).toContain("protocol: $protocol");
    expect(Object.keys(endpoints.primary.outputs!)).toEqual(["HLS"]);
    expect(endpoints.fallbacks).toEqual([]);
    gateway.destroy();
  });

  it.each(["protocol", "scheme", "node", "output"])(
    "rejects a mismatched %s without an unqualified retry",
    async (invalid) => {
      const body = reply("HLS");
      const primary = body.data.resolveViewerEndpoint.primary;
      if (invalid === "protocol") primary.protocol = "whep" as typeof primary.protocol;
      if (invalid === "scheme") primary.url = "javascript://us.example/public";
      if (invalid === "node") primary.nodeId = "";
      if (invalid === "output") primary.outputs = {};
      const fetcher = vi.fn(async () => ({ ok: true, json: async () => body }));
      vi.stubGlobal("fetch", fetcher);
      const gateway = client("HLS");
      await expect(gateway.resolve()).rejects.toThrow("requested playback");
      expect(fetcher).toHaveBeenCalledTimes(1);
      expect(gateway.getEndpoints()).toBeNull();
      gateway.destroy();
    }
  );

  it("does not drop the protocol argument after a schema error", async () => {
    const fetcher = vi.fn(async () => ({
      ok: true,
      json: async () => ({ errors: [{ message: "Unknown argument protocol" }] }),
    }));
    vi.stubGlobal("fetch", fetcher);
    const gateway = client("WHEP");
    await expect(gateway.resolve()).rejects.toThrow();
    expect(fetcher).toHaveBeenCalledTimes(1);
    gateway.destroy();
  });

  it("a late old-format body cannot overwrite or clear the new request", async () => {
    let finishOld!: (body: ReturnType<typeof reply>) => void;
    let finishNew!: (body: ReturnType<typeof reply>) => void;
    const oldBody = new Promise<ReturnType<typeof reply>>((resolve) => {
      finishOld = resolve;
    });
    const newBody = new Promise<ReturnType<typeof reply>>((resolve) => {
      finishNew = resolve;
    });
    const fetcher = vi
      .fn()
      .mockResolvedValueOnce({ ok: true, json: () => oldBody })
      .mockResolvedValueOnce({ ok: true, json: () => newBody });
    vi.stubGlobal("fetch", fetcher);
    const gateway = client("HLS");
    const published = vi.fn();
    gateway.on("endpointsResolved", published);
    const old = gateway.resolve().catch((error: Error) => error.message);
    await vi.waitFor(() => expect(fetcher).toHaveBeenCalledTimes(1));
    gateway.updateConfig({ protocol: "WHEP" });
    const current = gateway.resolve();
    finishOld(reply("HLS"));
    expect(await old).toBe("Request aborted");
    expect(gateway.getStatus()).toBe("loading");
    expect(gateway.getCircuitState().failures).toBe(0);
    const follower = gateway.resolve();
    finishNew(reply("WHEP"));
    expect((await current).primary.protocol).toBe("whep");
    expect((await follower).primary.protocol).toBe("whep");
    expect(fetcher).toHaveBeenCalledTimes(2);
    expect(published).toHaveBeenCalledTimes(1);
    gateway.destroy();
  });

  it("configuration changes cancel pending retry waits", async () => {
    vi.useFakeTimers();
    const fetcher = vi.fn().mockRejectedValue(new Error("network unavailable"));
    vi.stubGlobal("fetch", fetcher);
    const gateway = new GatewayClient({
      contentId: "public",
      protocol: "HLS",
      maxRetries: 3,
      initialDelayMs: 60_000,
    });
    const pending = gateway.resolve().catch((error: Error) => error.message);
    await vi.advanceTimersByTimeAsync(0);
    gateway.updateConfig({ protocol: "WHEP" });
    expect(await pending).toBe("Request aborted");
    expect(vi.getTimerCount()).toBe(0);
    expect(gateway.getStatus()).toBe("idle");
    expect(fetcher).toHaveBeenCalledTimes(1);
    gateway.destroy();
  });
});
