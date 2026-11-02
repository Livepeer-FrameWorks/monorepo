// @vitest-environment jsdom

import { afterEach, describe, expect, it, vi } from "vitest";
import { MetaTrackManager } from "../src/core/MetaTrackManager";
import { PlayerController } from "../src/core/PlayerController";
import { StreamStateClient } from "../src/core/StreamStateClient";

const mediaUrl = "https://cdn.example/live/index.m3u8";

function makeController(primary: Record<string, unknown>) {
  const manager = { on: vi.fn(() => () => {}), destroy: vi.fn().mockResolvedValue(undefined) };
  const controller = new PlayerController({
    contentId: "pasted-url",
    contentType: "live",
    playerManager: manager as any,
  });
  const state = controller as any;
  state.endpointMode = "provided";
  state.endpoints = {
    primary: {
      nodeId: "pasted-url",
      protocol: "HLS",
      url: mediaUrl,
      outputs: { HLS: { protocol: "HLS", url: mediaUrl } },
      ...primary,
    },
    fallbacks: [],
  };
  return state;
}

describe("pre-resolved endpoints that are not MistServer", () => {
  afterEach(() => {
    vi.unstubAllGlobals();
    vi.restoreAllMocks();
  });

  it("does not poll stream state on a host that has no Mist JSON", async () => {
    vi.stubGlobal("fetch", vi.fn().mockResolvedValue(new Response("", { status: 404 })));
    const start = vi.spyOn(StreamStateClient.prototype, "start").mockImplementation(() => {});
    const state = makeController({});

    await state.hydrateFromSelectedMistEdge("pasted-url");
    state.startStreamStatePolling();

    expect(start).not.toHaveBeenCalled();
    expect(state.streamStateClient).toBeNull();
  });

  it("keeps polling a Mist edge that says the stream is offline", async () => {
    vi.stubGlobal(
      "fetch",
      vi
        .fn()
        .mockImplementation(
          async () => new Response(JSON.stringify({ error: "Stream is offline" }))
        )
    );
    const start = vi.spyOn(StreamStateClient.prototype, "start").mockImplementation(() => {});
    const state = makeController({});

    await state.hydrateFromSelectedMistEdge("pasted-url");
    state.startStreamStatePolling();

    expect(start).toHaveBeenCalledOnce();
  });

  it("does not poll a host whose reply is not Mist JSON", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn().mockImplementation(async () => new Response("<html>not found</html>"))
    );
    const start = vi.spyOn(StreamStateClient.prototype, "start").mockImplementation(() => {});
    const state = makeController({});

    await state.hydrateFromSelectedMistEdge("pasted-url");
    state.startStreamStatePolling();

    expect(start).not.toHaveBeenCalled();
  });

  it("does not poll a host that cannot be reached while the lookup is retried", async () => {
    vi.stubGlobal("fetch", vi.fn().mockRejectedValue(new TypeError("Failed to fetch")));
    const start = vi.spyOn(StreamStateClient.prototype, "start").mockImplementation(() => {});
    const state = makeController({});

    await state.hydrateFromSelectedMistEdge("pasted-url");
    state.startStreamStatePolling();

    expect(start).not.toHaveBeenCalled();
    expect(state.selectedEdgeMistRetryTimer).not.toBeNull();
    state.clearSelectedEdgeMistRetry();
  });

  it("does not open a Mist metadata socket on a host that has no Mist JSON", async () => {
    vi.stubGlobal("fetch", vi.fn().mockResolvedValue(new Response("", { status: 404 })));
    const connect = vi.spyOn(MetaTrackManager.prototype, "connect").mockImplementation(() => {});
    const state = makeController({});

    await state.hydrateFromSelectedMistEdge("pasted-url");
    state.initializeMetaTrackManager();

    expect(connect).not.toHaveBeenCalled();
    expect(state.metaTrackManager).toBeNull();
  });

  it("keeps polling an edge named by baseUrl through a failed Mist JSON fetch", async () => {
    vi.stubGlobal("fetch", vi.fn().mockResolvedValue(new Response("", { status: 503 })));
    const start = vi.spyOn(StreamStateClient.prototype, "start").mockImplementation(() => {});
    const state = makeController({ baseUrl: "https://edge.example/" });

    await state.hydrateFromSelectedMistEdge("pasted-url");
    state.startStreamStatePolling();

    expect(start).toHaveBeenCalledOnce();
  });
});
