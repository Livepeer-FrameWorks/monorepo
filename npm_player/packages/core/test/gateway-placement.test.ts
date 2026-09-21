import { ResolveViewerEndpointDocument, ServerTooOldError } from "@livepeer-frameworks/api";
import { afterEach, describe, expect, it, vi } from "vitest";
import { GatewayClient } from "../src/core/GatewayClient";
import { clearServerInfoProbes } from "@livepeer-frameworks/api/gateway-probe";
import { viewerProtocols, type ViewerProtocol } from "../src/core/ViewerProtocol";

const SELECTING_SERVER = {
  data: { serverInfo: { version: "v0.3.11", features: ["playback", "viewer-protocol-selection"] } },
};

function isProbe(init?: RequestInit): boolean {
  return typeof init?.body === "string" && init.body.includes("serverInfo");
}

/**
 * Answers the serverInfo probe as a gateway that selects by protocol and hands
 * every other request to the resolve handler.
 */
function selectingGateway(
  resolve: (url: RequestInfo | URL, init?: RequestInit) => Promise<unknown>
): ReturnType<typeof vi.fn> {
  return vi.fn(async (url: RequestInfo | URL, init?: RequestInit) => {
    if (isProbe(init)) return { ok: true, json: async () => SELECTING_SERVER };
    return resolve(url, init);
  });
}

function resolveCalls(fetcher: ReturnType<typeof vi.fn>): unknown[][] {
  return fetcher.mock.calls.filter(([, init]) => !isProbe(init as RequestInit | undefined));
}

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
  clearServerInfoProbes();
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
      const fetcher = selectingGateway(async () => ({ ok: true, json: async () => body }));
      vi.stubGlobal("fetch", fetcher);
      const gateway = client("HLS");
      await expect(gateway.resolve()).rejects.toThrow("invalid playback outputs");
      expect(gateway.getEndpoints()).toBeNull();
      expect(resolveCalls(fetcher)).toHaveLength(1);
      gateway.destroy();
    }
  );

  it("sends the typed requirement and retains only the selected prepared output", async () => {
    const body = reply("HLS");
    Object.assign(body.data.resolveViewerEndpoint.primary.outputs, {
      WHEP: { protocol: "WHEP", url: "https://other.example/whep/public" },
    });
    const fetcher = selectingGateway(async () => ({ ok: true, json: async () => body }));
    vi.stubGlobal("fetch", fetcher);
    const gateway = client("HLS");
    const endpoints = await gateway.resolve();
    const calls = resolveCalls(fetcher);
    expect(calls).toHaveLength(1);
    const request = JSON.parse((calls[0][1] as RequestInit).body as string);
    expect(request.variables).toEqual({ contentId: "public", protocol: "HLS" });
    expect(request.query).toBe(String(ResolveViewerEndpointDocument));
    expect(request.operationName).toBe("ResolveViewerEndpoint");
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
      const fetcher = selectingGateway(async () => ({ ok: true, json: async () => body }));
      vi.stubGlobal("fetch", fetcher);
      const gateway = client("HLS");
      await expect(gateway.resolve()).rejects.toThrow("requested playback");
      expect(resolveCalls(fetcher)).toHaveLength(1);
      expect(gateway.getEndpoints()).toBeNull();
      gateway.destroy();
    }
  );

  it("does not drop the protocol argument after a schema error", async () => {
    const fetcher = selectingGateway(async () => ({
      ok: true,
      json: async () => ({ errors: [{ message: "Unknown argument protocol" }] }),
    }));
    vi.stubGlobal("fetch", fetcher);
    const gateway = client("WHEP");
    await expect(gateway.resolve()).rejects.toThrow();
    const calls = resolveCalls(fetcher);
    expect(calls).toHaveLength(1);
    expect(JSON.parse((calls[0][1] as RequestInit).body as string).variables.protocol).toBe("WHEP");
    gateway.destroy();
  });

  it.each([
    ["is a development build without the slug", { version: "dev", features: ["playback"] }],
    ["is a supported release without the slug", { version: "v0.3.11", features: ["playback"] }],
  ])(
    "omits the protocol and enforces the format locally when the gateway %s",
    async (_case, serverInfo) => {
      const probeAnswer = { data: { serverInfo } };
      const body = reply("HLS");
      Object.assign(body.data.resolveViewerEndpoint.primary.outputs, {
        WHEP: { protocol: "WHEP", url: "https://other.example/whep/public" },
      });
      const fetcher = vi.fn(async (_url: RequestInfo | URL, init?: RequestInit) => ({
        ok: true,
        json: async () => (isProbe(init) ? probeAnswer : body),
      }));
      vi.stubGlobal("fetch", fetcher);
      const gateway = client("HLS");
      const endpoints = await gateway.resolve();
      const calls = resolveCalls(fetcher);
      expect(calls).toHaveLength(1);
      const request = JSON.parse((calls[0][1] as RequestInit).body as string);
      expect(request.variables).toEqual({ contentId: "public" });
      expect(request.query).toBe(String(ResolveViewerEndpointDocument));
      expect(Object.keys(endpoints.primary.outputs!)).toEqual(["HLS"]);
      gateway.destroy();
    }
  );

  it("still rejects an endpoint in the wrong format from a gateway without selection", async () => {
    const body = reply("WHEP");
    const fetcher = vi.fn(async (_url: RequestInfo | URL, init?: RequestInit) => ({
      ok: true,
      json: async () => (isProbe(init) ? { data: { serverInfo: null } } : body),
    }));
    vi.stubGlobal("fetch", fetcher);
    const gateway = client("HLS");
    await expect(gateway.resolve()).rejects.toThrow("requested playback");
    expect(gateway.getEndpoints()).toBeNull();
    gateway.destroy();
  });

  it("probes each gateway once and reuses the answer across clients", async () => {
    const fetcher = selectingGateway(async () => ({ ok: true, json: async () => reply("HLS") }));
    vi.stubGlobal("fetch", fetcher);
    const first = client("HLS");
    const second = client("HLS");
    await first.resolve();
    await second.resolve();
    const probes = fetcher.mock.calls.filter(([, init]) => isProbe(init as RequestInit));
    expect(probes).toHaveLength(1);
    expect(resolveCalls(fetcher)).toHaveLength(2);
    first.destroy();
    second.destroy();
  });

  it("asks again after a probe that could not reach the gateway", async () => {
    let probeReachable = false;
    const fetcher = vi.fn(async (_url: RequestInfo | URL, init?: RequestInit) => {
      if (isProbe(init)) {
        if (!probeReachable) throw new Error("network unavailable");
        return { ok: true, json: async () => SELECTING_SERVER };
      }
      return { ok: true, json: async () => reply("HLS") };
    });
    vi.stubGlobal("fetch", fetcher);
    const gateway = client("HLS");
    await gateway.resolve();
    expect(
      JSON.parse((resolveCalls(fetcher)[0][1] as RequestInit).body as string).variables
    ).toEqual({ contentId: "public" });

    probeReachable = true;
    await gateway.resolve(true);
    const calls = resolveCalls(fetcher);
    expect(JSON.parse((calls[1][1] as RequestInit).body as string).variables).toEqual({
      contentId: "public",
      protocol: "HLS",
    });
    gateway.destroy();
  });

  it("sends the resolve alongside the probe when no format is required", async () => {
    let answerProbe!: () => void;
    const probeHeld = new Promise<void>((resolve) => {
      answerProbe = resolve;
    });
    const fetcher = vi.fn(async (_url: RequestInfo | URL, init?: RequestInit) => {
      if (isProbe(init)) {
        await probeHeld;
        return { ok: true, json: async () => SELECTING_SERVER };
      }
      return { ok: true, json: async () => reply("HLS") };
    });
    vi.stubGlobal("fetch", fetcher);
    const gateway = new GatewayClient({
      contentId: "public",
      gatewayUrl: "https://gateway.example/graphql",
      maxRetries: 1,
    });
    const published = vi.fn();
    gateway.on("endpointsResolved", published);
    const pending = gateway.resolve();
    await vi.waitFor(() => expect(resolveCalls(fetcher)).toHaveLength(1));
    expect(fetcher.mock.calls.filter(([, init]) => isProbe(init as RequestInit))).toHaveLength(1);
    expect(
      JSON.parse((resolveCalls(fetcher)[0][1] as RequestInit).body as string).variables
    ).toEqual({ contentId: "public" });
    // The resolve answer has arrived; it stays unpublished until the probe settles.
    await new Promise((resolve) => setTimeout(resolve, 20));
    expect(published).not.toHaveBeenCalled();
    answerProbe();
    await pending;
    expect(published).toHaveBeenCalledOnce();
    gateway.destroy();
  });

  it.each([
    [
      "a release below the minimum",
      { status: 200, body: { data: { serverInfo: { version: "v0.3.10", features: [] } } } },
      "v0.3.10",
    ],
    [
      "a gateway without serverInfo",
      {
        status: 422,
        body: {
          errors: [
            {
              message: 'Cannot query field "serverInfo" on type "Query".',
              extensions: { code: "GRAPHQL_VALIDATION_FAILED" },
            },
          ],
        },
      },
      null,
    ],
  ])("refuses %s without publishing endpoints", async (_case, probeAnswer, serverVersion) => {
    const fetcher = vi.fn(async (_url: RequestInfo | URL, init?: RequestInit) =>
      isProbe(init)
        ? new Response(JSON.stringify(probeAnswer.body), { status: probeAnswer.status })
        : { ok: true, json: async () => reply("HLS") }
    );
    vi.stubGlobal("fetch", fetcher);
    for (const protocol of [undefined, "HLS"] as const) {
      clearServerInfoProbes();
      const gateway = new GatewayClient({
        contentId: "public",
        gatewayUrl: "https://gateway.example/graphql",
        protocol,
        maxRetries: 1,
      });
      const published = vi.fn();
      gateway.on("endpointsResolved", published);
      const error = await gateway.resolve().catch((e: unknown) => e);
      expect(error).toBeInstanceOf(ServerTooOldError);
      expect((error as ServerTooOldError).serverVersion).toBe(serverVersion);
      expect((error as ServerTooOldError).minimumVersion).toBe("v0.3.11");
      expect(gateway.getStatus()).toBe("error");
      expect(gateway.getEndpoints()).toBeNull();
      expect(published).not.toHaveBeenCalled();
      gateway.destroy();
    }
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
    const resolveBodies = [oldBody, newBody];
    const fetcher = selectingGateway(async () => {
      const body = resolveBodies.shift();
      return { ok: true, json: () => body };
    });
    vi.stubGlobal("fetch", fetcher);
    const gateway = client("HLS");
    const published = vi.fn();
    gateway.on("endpointsResolved", published);
    const old = gateway.resolve().catch((error: Error) => error.message);
    await vi.waitFor(() => expect(resolveCalls(fetcher)).toHaveLength(1));
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
    expect(resolveCalls(fetcher)).toHaveLength(2);
    expect(published).toHaveBeenCalledTimes(1);
    gateway.destroy();
  });

  it("configuration changes cancel pending retry waits", async () => {
    vi.useFakeTimers();
    const fetcher = selectingGateway(async () => {
      throw new Error("network unavailable");
    });
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
    expect(resolveCalls(fetcher)).toHaveLength(1);
    gateway.destroy();
  });
});
