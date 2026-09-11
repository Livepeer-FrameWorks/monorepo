import { act, cleanup, renderHook, waitFor } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";
import { useViewerEndpoints, type ViewerEndpointsParams } from "../src/hooks/useViewerEndpoints";
import { viewerReply, deferredReply } from "../../test-contract/viewer-endpoint-fixtures";

afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
  vi.useRealTimers();
});

describe("standalone React viewer placement", () => {
  it("uses the typed client, forwards both credentials and decodes only prepared output", async () => {
    const fetcher = vi.fn(async () => ({ ok: true, json: async () => viewerReply("HLS") }));
    vi.stubGlobal("fetch", fetcher);
    const hook = renderHook(() =>
      useViewerEndpoints({
        contentId: "playback",
        protocol: "HLS",
        authToken: "account",
        playbackAuth: { token: "viewer" },
      })
    );
    await waitFor(() => expect(hook.result.current.status).toBe("ready"));
    const options = (fetcher.mock.calls[0] as unknown as [string, RequestInit])[1];
    expect(JSON.parse(options.body as string).variables).toEqual({
      contentId: "playback",
      protocol: "HLS",
    });
    expect(options.headers).toMatchObject({
      Authorization: "Bearer account",
      "X-Frameworks-Playback-JWT": "viewer",
    });
    expect(Object.keys(hook.result.current.endpoints!.primary.outputs!)).toEqual(["HLS"]);
    expect(hook.result.current.endpoints!.fallbacks).toEqual([]);
    expect(hook.result.current.endpoints!.metadata?.thumbnailAssets?.posterUrl).toBe(
      "https://poster.example/image.jpg"
    );
  });

  it("ignores an old decoded body after the requested format changes", async () => {
    const old = deferredReply();
    const fetcher = vi
      .fn()
      .mockResolvedValueOnce({ ok: true, json: () => old.promise })
      .mockResolvedValueOnce({ ok: true, json: async () => viewerReply("DASH") });
    vi.stubGlobal("fetch", fetcher);
    const hook = renderHook((params: ViewerEndpointsParams) => useViewerEndpoints(params), {
      initialProps: { contentId: "playback", protocol: "HLS" } as ViewerEndpointsParams,
    });
    await waitFor(() => expect(fetcher).toHaveBeenCalledTimes(1));
    hook.rerender({ contentId: "playback", protocol: "DASH" });
    await waitFor(() => expect(hook.result.current.endpoints?.primary.protocol).toBe("dash"));
    await act(async () => {
      old.resolve(viewerReply("HLS"));
      await old.promise;
    });
    expect(hook.result.current.endpoints?.primary.protocol).toBe("dash");
    expect(fetcher.mock.calls[0][1].signal.aborted).toBe(true);
  });

  it("clears a selected destination immediately when content is cleared", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn(async () => ({ ok: true, json: async () => viewerReply("HLS") }))
    );
    const hook = renderHook(({ contentId }) => useViewerEndpoints({ contentId, protocol: "HLS" }), {
      initialProps: { contentId: "playback" },
    });
    await waitFor(() => expect(hook.result.current.status).toBe("ready"));
    hook.rerender({ contentId: "" });
    expect(hook.result.current).toEqual({ endpoints: null, status: "idle", error: null });
  });

  it("rejects a mismatched format without an unqualified query", async () => {
    const fetcher = vi.fn(async () => ({ ok: true, json: async () => viewerReply("DASH") }));
    vi.stubGlobal("fetch", fetcher);
    const hook = renderHook(() => useViewerEndpoints({ contentId: "playback", protocol: "HLS" }));
    await waitFor(() => expect(hook.result.current.status).toBe("error"));
    expect(hook.result.current.endpoints).toBeNull();
    expect(fetcher).toHaveBeenCalledOnce();
  });

  it("cancels a pending retry on unmount", async () => {
    vi.useFakeTimers();
    const fetcher = vi.fn(async () => ({ ok: false, status: 503 }));
    vi.stubGlobal("fetch", fetcher);
    const hook = renderHook(() => useViewerEndpoints({ contentId: "playback", protocol: "HLS" }));
    await act(async () => {
      await Promise.resolve();
    });
    hook.unmount();
    await act(async () => {
      await vi.runAllTimersAsync();
    });
    expect(fetcher).toHaveBeenCalledOnce();
  });
});
