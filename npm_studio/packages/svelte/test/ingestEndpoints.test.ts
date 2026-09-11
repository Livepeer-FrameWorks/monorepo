import { afterEach, describe, expect, it, vi } from "vitest";
import { get } from "svelte/store";
import { createIngestEndpointsStore } from "../src/stores/ingestEndpoints";

afterEach(() => {
  vi.unstubAllGlobals();
});
const options = { gatewayUrl: "https://gateway.example/graphql", streamKey: "key" };
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

describe("ingest store request identity", () => {
  it("does not let an old cancelled request overwrite the new ready state", async () => {
    vi.stubGlobal(
      "fetch",
      vi
        .fn()
        .mockImplementationOnce(() => new Promise(() => {}))
        .mockResolvedValue(response())
    );
    const store = createIngestEndpointsStore();
    const first = store.resolve(options);
    await store.resolve(options);
    await first;
    expect(get(store).endpoints?.primary.nodeId).toBe("new-node");
    expect(get(store).status).toBe("ready");
    expect(get(store).error).toBeNull();
    store.destroy();
  });

  it("reset remains idle after a cancelled request settles", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn(() => new Promise(() => {}))
    );
    const store = createIngestEndpointsStore();
    const pending = store.resolve(options);
    store.reset();
    await pending;
    expect(get(store)).toEqual({ endpoints: null, status: "idle", error: null });
    store.destroy();
  });

  it("missing credentials clear the prior recommendation", async () => {
    vi.stubGlobal("fetch", vi.fn().mockResolvedValue(response()));
    const store = createIngestEndpointsStore();
    await store.resolve(options);
    expect(get(store.whipUrl)).not.toBeNull();
    await store.resolve({});
    expect(get(store)).toEqual({ endpoints: null, status: "idle", error: null });
    store.destroy();
  });
});
