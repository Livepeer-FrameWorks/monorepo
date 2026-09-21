import { get } from "svelte/store";
import { afterEach, describe, expect, it, vi } from "vitest";
import {
  createEndpointResolver,
  createDerivedPrimaryEndpoint,
  createDerivedMetadata,
} from "../src/stores/viewerEndpoints";
import {
  viewerReply,
  deferredReply,
  supportedGateway,
} from "../../test-contract/viewer-endpoint-fixtures";

afterEach(() => {
  vi.unstubAllGlobals();
  vi.useRealTimers();
});

describe("standalone Svelte viewer placement", () => {
  it("cancels retry backoff when destroyed", async () => {
    vi.useFakeTimers();
    const fetcher = vi.fn(async () => ({ ok: false, status: 503 }));
    vi.stubGlobal("fetch", supportedGateway(fetcher));
    const resolver = createEndpointResolver({ contentId: "playback", protocol: "HLS" });
    await vi.advanceTimersByTimeAsync(0);
    resolver.destroy();
    await vi.runAllTimersAsync();
    expect(fetcher).toHaveBeenCalledOnce();
    expect(get(resolver)).toEqual({ endpoints: null, status: "idle", error: null });
  });

  it("uses typed resolution, credential forwarding and prepared-only output for derived stores", async () => {
    const fetcher = vi.fn(async () => ({ ok: true, json: async () => viewerReply("HLS") }));
    vi.stubGlobal("fetch", supportedGateway(fetcher));
    const resolver = createEndpointResolver({
      contentId: "playback",
      protocol: "HLS",
      authToken: "account",
      playbackAuth: { token: "viewer" },
    });
    await vi.waitFor(() => expect(get(resolver).status).toBe("ready"));
    const options = (fetcher.mock.calls[0] as unknown as [string, RequestInit])[1];
    expect(JSON.parse(options.body as string).variables).toEqual({
      contentId: "playback",
      protocol: "HLS",
    });
    expect(options.headers).toMatchObject({
      Authorization: "Bearer account",
      "X-Frameworks-Playback-JWT": "viewer",
    });
    expect(Object.keys(get(createDerivedPrimaryEndpoint(resolver))!.outputs!)).toEqual(["HLS"]);
    expect(get(resolver).endpoints!.fallbacks).toEqual([]);
    expect(get(createDerivedMetadata(resolver))?.thumbnailAssets?.posterUrl).toBe(
      "https://poster.example/image.jpg"
    );
    resolver.destroy();
  });

  it("replaces pending format resolution without allowing the old body to publish", async () => {
    const old = deferredReply();
    const fetcher = vi
      .fn()
      .mockResolvedValueOnce({ ok: true, json: () => old.promise })
      .mockResolvedValueOnce({ ok: true, json: async () => viewerReply("DASH") });
    vi.stubGlobal("fetch", supportedGateway(fetcher));
    const resolver = createEndpointResolver({ contentId: "playback", protocol: "HLS" });
    await vi.waitFor(() => expect(fetcher).toHaveBeenCalledTimes(1));
    resolver.update({ protocol: "DASH" });
    expect(get(resolver).endpoints).toBeNull();
    await vi.waitFor(() => expect(get(resolver).endpoints?.primary.protocol).toBe("dash"));
    old.resolve(viewerReply("HLS"));
    await old.promise;
    await Promise.resolve();
    expect(get(resolver).endpoints?.primary.protocol).toBe("dash");
    expect(fetcher.mock.calls[0][1].signal.aborted).toBe(true);
    resolver.destroy();
  });

  it("refetches a fresh destination and clears previous data while loading", async () => {
    const fetcher = vi
      .fn()
      .mockResolvedValueOnce({ ok: true, json: async () => viewerReply("HLS", "first") })
      .mockResolvedValueOnce({ ok: true, json: async () => viewerReply("HLS", "second") });
    vi.stubGlobal("fetch", supportedGateway(fetcher));
    const resolver = createEndpointResolver({ contentId: "playback", protocol: "HLS" });
    await vi.waitFor(() => expect(get(resolver).status).toBe("ready"));
    resolver.refetch();
    expect(get(resolver)).toEqual({ endpoints: null, status: "loading", error: null });
    await vi.waitFor(() => expect(get(resolver).endpoints?.primary.nodeId).toBe("second"));
    resolver.update({ contentId: "" });
    expect(get(resolver)).toEqual({ endpoints: null, status: "idle", error: null });
    resolver.destroy();
  });

  it("cannot be resurrected by a late body, refetch or update after destruction", async () => {
    const old = deferredReply();
    const fetcher = vi.fn(async () => ({ ok: true, json: () => old.promise }));
    vi.stubGlobal("fetch", supportedGateway(fetcher));
    const resolver = createEndpointResolver({ contentId: "playback", protocol: "HLS" });
    resolver.destroy();
    old.resolve(viewerReply("HLS"));
    await old.promise;
    resolver.refetch();
    resolver.update({ contentId: "replacement" });
    await Promise.resolve();
    expect(get(resolver)).toEqual({ endpoints: null, status: "idle", error: null });
    // A required format waits for the gateway check, which the synchronous
    // destroy outruns, so no resolve is ever sent.
    expect(fetcher).not.toHaveBeenCalled();
  });

  it("rejects a different output format rather than borrowing its URL", async () => {
    const fetcher = vi.fn(async () => ({ ok: true, json: async () => viewerReply("DASH") }));
    vi.stubGlobal("fetch", supportedGateway(fetcher));
    const resolver = createEndpointResolver({ contentId: "playback", protocol: "HLS" });
    await vi.waitFor(() => expect(get(resolver).status).toBe("error"));
    expect(get(resolver).endpoints).toBeNull();
    expect(fetcher).toHaveBeenCalledOnce();
    resolver.destroy();
  });
});
