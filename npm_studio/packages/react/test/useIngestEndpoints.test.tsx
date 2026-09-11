import { afterEach, describe, expect, it, vi } from "vitest";
import { act, cleanup, renderHook } from "@testing-library/react";
import { useIngestEndpoints } from "../src/hooks/useIngestEndpoints";

afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
});

const options = {
  gatewayUrl: "https://gateway.example/graphql",
  streamKey: "key",
  autoResolve: false,
};
const response = () => ({
  ok: true,
  json: async () => ({
    data: {
      resolveIngestEndpoint: {
        primary: {
          nodeId: "new-node",
          baseUrl: "https://node.example",
          whipUrl: "https://node.example/webrtc/key",
        },
        fallbacks: [],
      },
    },
  }),
});

describe("ingest endpoint request identity", () => {
  it("retains the newer result when an older request is cancelled", async () => {
    const fetch = vi
      .fn()
      .mockImplementationOnce(() => new Promise(() => {}))
      .mockResolvedValue(response());
    vi.stubGlobal("fetch", fetch);
    const { result } = renderHook(() => useIngestEndpoints(options));
    let first!: ReturnType<typeof result.current.resolve>;
    act(() => {
      first = result.current.resolve();
    });
    await act(async () => {
      await result.current.resolve();
      await first;
    });
    expect(result.current.endpoints?.primary.nodeId).toBe("new-node");
    expect(result.current.status).toBe("ready");
    expect(result.current.error).toBeNull();
  });

  it("clears a resolved URL when credentials are removed without automatic resolution", async () => {
    vi.stubGlobal("fetch", vi.fn().mockResolvedValue(response()));
    const { result, rerender } = renderHook((props) => useIngestEndpoints(props), {
      initialProps: options,
    });
    await act(async () => {
      await result.current.resolve();
    });
    expect(result.current.whipUrl).not.toBeNull();
    rerender({ ...options, streamKey: "" });
    expect(result.current.whipUrl).toBeNull();
    expect(result.current.status).toBe("idle");
  });

  it("reset remains idle after the cancelled promise settles", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn(() => new Promise(() => {}))
    );
    const { result } = renderHook(() => useIngestEndpoints(options));
    let pending!: ReturnType<typeof result.current.resolve>;
    act(() => {
      pending = result.current.resolve();
    });
    await act(async () => {
      result.current.reset();
      await pending;
    });
    expect(result.current.status).toBe("idle");
    expect(result.current.error).toBeNull();
    expect(result.current.whipUrl).toBeNull();
  });
});
