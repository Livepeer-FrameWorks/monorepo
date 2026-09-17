// @vitest-environment jsdom

import { afterAll, afterEach, describe, expect, it, vi } from "vitest";
import { PlayerController } from "../src/core/PlayerController";
import { GatewayClient } from "../src/core/GatewayClient";
import { StreamStateClient } from "../src/core/StreamStateClient";
import { PlayerManager } from "../src/core/PlayerManager";
import { ensurePlayersRegistered } from "../src/core/PlayerRegistry";
import type { IPlayer } from "../src/core/PlayerInterface";

const selectedURL = "https://us.example/hls/live/index.m3u8?receipt=selected";
const mistInfo = {
  type: "live",
  source: [
    { type: "html5/application/vnd.apple.mpegurl", url: "https://us.example/from-mist.m3u8" },
    { type: "whep", url: "https://us.example/from-mist-webrtc" },
  ],
  meta: { tracks: { video: { type: "video", codec: "H264", width: 1920, height: 1080 } } },
};

function makeController() {
  const manager = { on: vi.fn(() => () => {}), destroy: vi.fn().mockResolvedValue(undefined) };
  const controller = new PlayerController({
    contentId: "playback-id",
    contentType: "live",
    playerManager: manager as any,
  });
  const state = controller as any;
  state.endpointMode = "provided";
  state.endpoints = {
    primary: {
      nodeId: "us-edge",
      protocol: "hls",
      baseUrl: "https://us.example",
      url: selectedURL,
      outputs: { HLS: { url: selectedURL } },
    },
    fallbacks: [],
    metadata: { contentId: "live+internal", thumbnailAssets: { assetKey: "poster-1" } },
  };
  state.setMetadataSeed(state.endpoints.metadata);
  state.streamInfo = state.buildStreamInfo(state.endpoints);
  state.container = document.createElement("div");
  return { controller, state, manager };
}

function deferred<T>() {
  let resolve!: (value: T) => void;
  let reject!: (error: Error) => void;
  const promise = new Promise<T>((yes, no) => {
    resolve = yes;
    reject = no;
  });
  return { promise, resolve, reject };
}

afterEach(() => {
  vi.restoreAllMocks();
  vi.unstubAllGlobals();
});

afterAll(async () => {
  await ensurePlayersRegistered();
});

describe("controller protocol discovery", () => {
  it("keeps node resolution unqualified when viewer authentication uses headers", async () => {
    const { state } = makeController();
    state.config.endpoints = undefined;
    state.config.playbackAuth = { token: "viewer-jwt", transport: "header" };
    state.resolveFromGateway = vi.fn().mockResolvedValue(undefined);
    await state.resolveEndpoints();
    expect(state.resolveFromGateway).toHaveBeenCalledWith(
      expect.any(String),
      "playback-id",
      undefined,
      undefined
    );
  });

  it("uses every protocol advertised by the selected Mist node without re-resolving placement", async () => {
    vi.spyOn(StreamStateClient.prototype, "start").mockImplementation(() => {});
    const manager = new PlayerManager();
    const video = document.createElement("video");
    const hls = {
      capability: {
        name: "HLS",
        shortname: "test-hls",
        priority: 1,
        mimes: ["html5/application/vnd.apple.mpegurl"],
      },
      isMimeSupported: vi.fn((type) => type === "html5/application/vnd.apple.mpegurl"),
      isBrowserSupported: vi.fn(() => ["video"]),
      initialize: vi.fn().mockResolvedValue(video),
      destroy: vi.fn(),
      on: vi.fn(),
      off: vi.fn(),
    } as unknown as IPlayer;
    manager.registerPlayer(hls);
    const requests: any[] = [];
    vi.stubGlobal(
      "fetch",
      vi.fn(async (_url, options) => {
        const request = JSON.parse(options.body);
        requests.push(request);
        const url = "wss://us.example/webrtc/bootstrap";
        return {
          ok: true,
          json: async () => ({
            data: {
              resolveViewerEndpoint: {
                primary: {
                  nodeId: "us-edge",
                  protocol: "webrtc",
                  url,
                  baseUrl: "https://us.example",
                  outputs: { MIST_WEBRTC: { url } },
                },
                fallbacks: [],
                metadata: { contentType: "live", contentId: "live+internal" },
              },
            },
          }),
        };
      })
    );
    const controller = new PlayerController({
      contentId: "playback-id",
      contentType: "live",
      gatewayUrl: "https://gateway.example/graphql",
      playerManager: manager,
    });
    const state = controller as any;
    state.fetchMistStreamInfo = vi.fn().mockResolvedValue(mistInfo);
    await controller.attach(document.createElement("div"));
    expect(requests.map((request) => request.variables.protocol)).toEqual([undefined]);
    expect(hls.initialize).toHaveBeenCalledOnce();
    expect(hls.initialize).toHaveBeenCalledWith(
      expect.anything(),
      expect.objectContaining({ url: "https://us.example/from-mist.m3u8" }),
      expect.anything(),
      expect.anything()
    );
    expect(state.endpoints.primary.nodeId).toBe("us-edge");
    expect(state.streamInfo.source.map((source: any) => source.url)).toEqual([
      "https://us.example/from-mist.m3u8",
      "https://us.example/from-mist-webrtc",
    ]);
    controller.detach();
    await manager.destroy();
  });

  it("filters Mist discovery when the caller explicitly pins a protocol", () => {
    const { state } = makeController();
    state.config.viewerProtocol = "HLS";
    expect(state.selectPlaybackSources(mistInfo.source)).toEqual([mistInfo.source[0]]);
  });

  it.each([
    ["MEWS", "wss/video/mp4"],
    ["MEWS_WEBM", "wss/video/webm"],
    ["H264_WS", "wss/video/h264"],
    ["RAW_WS", "wss/video/raw"],
  ] as const)("matches the %s pin to Mist's secure WebSocket MIME", (viewerProtocol, type) => {
    const { state } = makeController();
    state.config.viewerProtocol = viewerProtocol;
    const source = { type, url: `wss://us.example/${viewerProtocol.toLowerCase()}` };
    expect(state.selectPlaybackSources([source])).toEqual([source]);
  });
});

describe("PlayerController selected Mist source discovery", () => {
  it("ignores old media readiness, errors and completion after endpoint reselection", async () => {
    const { state } = makeController();
    const pending = deferred<HTMLVideoElement>();
    let options: any;
    state.playerManager.initializePlayer = vi.fn((_container, _info, suppliedOptions) => {
      options = suppliedOptions;
      return pending.promise;
    });
    state.setPassiveError = vi.fn();
    const initializing = state.initializePlayer();
    state.endpointResolutionEpoch++;
    state.container.innerHTML = "new player";
    const obsoleteVideo = document.createElement("video");
    options.onReady(obsoleteVideo);
    options.onError("old player failed");
    pending.resolve(obsoleteVideo);
    await initializing;
    expect(state.container.innerHTML).toBe("new player");
    expect(state.videoElement).toBeNull();
    expect(state.setPassiveError).not.toHaveBeenCalled();
  });

  it("does not mark a new attachment ready when old provided-endpoint hydration settles", async () => {
    const { state } = makeController();
    state.config.endpoints = state.endpoints;
    const pending = deferred<void>();
    state.hydrateFromSelectedMistEdge = vi.fn().mockReturnValue(pending.promise);
    const resolving = state.resolveEndpoints();
    state.endpointResolutionEpoch++;
    state.setState("gateway_loading", { gatewayStatus: "loading" });
    pending.resolve();
    await resolving;
    expect(state.state).toBe("gateway_loading");
  });

  it("hydrates tracks even when Mist advertises no playback URLs", async () => {
    const { state } = makeController();
    state.fetchMistStreamInfo = vi.fn().mockResolvedValue({ ...mistInfo, source: [] });
    await state.hydrateFromSelectedMistEdge("playback-id");
    expect(state.streamInfo.source.map((source: any) => source.url)).toEqual([selectedURL]);
    expect(state.streamInfo.meta.tracks[0]).toMatchObject({ width: 1920, height: 1080 });
  });

  it.each(["reply", "failure"])(
    "ignores a stale hydration %s after a new resolution",
    async (outcome) => {
      const { state } = makeController();
      const pending = deferred<any>();
      state.fetchMistStreamInfo = vi.fn().mockReturnValue(pending.promise);
      const hydration = state.hydrateFromSelectedMistEdge("playback-id");
      state.endpointResolutionEpoch++;
      const replacement = { ...state.streamInfo, marker: "new attachment" };
      state.streamInfo = replacement;
      if (outcome === "reply") pending.resolve(mistInfo);
      else pending.reject(new Error("old edge failed"));
      await hydration;
      expect(state.streamInfo).toBe(replacement);
      expect(state.fetchMistStreamInfo).toHaveBeenCalledTimes(1);
    }
  );

  it("refreshes Mist's protocol catalog through polling and ignores callbacks from replaced pollers", () => {
    vi.spyOn(StreamStateClient.prototype, "start").mockImplementation(() => {});
    const { state } = makeController();
    state.startStreamStatePolling();
    const oldClient = state.streamStateClient;
    oldClient.emit("stateChange", { state: { isOnline: true, streamInfo: mistInfo } });
    expect(state.streamInfo.source.map((source: any) => source.url)).toEqual([
      "https://us.example/from-mist.m3u8",
      "https://us.example/from-mist-webrtc",
    ]);
    expect(state.streamInfo.meta.tracks[0]).toMatchObject({ width: 1920 });
    state.startStreamStatePolling();
    const currentState = state.streamState;
    oldClient.emit("stateChange", { state: { isOnline: false } });
    expect(state.streamState).toBe(currentState);
    state.cleanup();
  });

  it("retains hydrated datachannel capabilities when a later poll omits them", async () => {
    vi.spyOn(StreamStateClient.prototype, "start").mockImplementation(() => {});
    const { state } = makeController();
    state.fetchMistStreamInfo = vi
      .fn()
      .mockResolvedValue({ ...mistInfo, capa: { datachannels: true } });
    await state.hydrateFromSelectedMistEdge("playback-id");
    state.startStreamStatePolling();
    state.streamStateClient.emit("stateChange", {
      state: { isOnline: true, streamInfo: mistInfo },
    });
    expect(state.streamInfo.source).toEqual([
      expect.objectContaining({
        url: "https://us.example/from-mist.m3u8",
        mistDatachannels: true,
      }),
      expect.objectContaining({
        url: "https://us.example/from-mist-webrtc",
        mistDatachannels: true,
      }),
    ]);
    state.cleanup();
  });

  it("recovers using existing placement and track metadata without requiring a Mist URL catalog", async () => {
    const { state } = makeController();
    state.initializePlayer = vi.fn().mockResolvedValue(undefined);
    state.retry = vi.fn();
    await state.recoverPlaybackAfterOnlineTransition({ ...mistInfo, source: [] });
    expect(state.streamInfo.source.map((source: any) => source.url)).toEqual([selectedURL]);
    expect(state.initializePlayer).toHaveBeenCalledOnce();
    expect(state.retry).not.toHaveBeenCalled();
  });

  it("uses Mist's catalog when the placement response has no output catalog", async () => {
    const { state } = makeController();
    state.endpoints.primary.outputs = {};
    state.fetchMistStreamInfo = vi.fn().mockResolvedValue(mistInfo);
    await state.hydrateFromSelectedMistEdge("playback-id");
    expect(state.streamInfo.source).toEqual(mistInfo.source);
  });

  it("keeps the prepared node while refreshing protocols and metadata through cold recovery", async () => {
    const { state } = makeController();
    const endpoints = state.endpoints;
    state.initializePlayer = vi.fn().mockResolvedValue(undefined);
    await state.initializeLateFromStreamState(mistInfo);
    expect(state.endpoints).toBe(endpoints);
    expect(state.endpoints.primary.nodeId).toBe("us-edge");
    expect(state.streamInfo.source).toEqual(mistInfo.source);
    expect(state.getMetadata().contentId).toBe("live+internal");
    expect(state.initializePlayer).toHaveBeenCalledOnce();
  });

  it("does not let recovery overwrite a new attachment while old player destruction is pending", async () => {
    const { state, manager } = makeController();
    const pending = deferred<void>();
    manager.destroy.mockReturnValue(pending.promise);
    state.initializePlayer = vi.fn();
    const recovery = state.initializeLateFromStreamState(mistInfo);
    state.endpointResolutionEpoch++;
    state.container.innerHTML = "new attachment";
    const streamInfo = state.streamInfo;
    pending.resolve();
    await recovery;
    expect(state.container.innerHTML).toBe("new attachment");
    expect(state.streamInfo).toBe(streamInfo);
    expect(state.initializePlayer).not.toHaveBeenCalled();
  });

  it("retains Mist source discovery in direct-Mist mode", async () => {
    const { state } = makeController();
    state.endpointMode = "direct-mist";
    state.initializePlayer = vi.fn().mockResolvedValue(undefined);
    await state.initializeLateFromStreamState(mistInfo);
    expect(state.endpoints.primary.nodeId).toBe("mist-playback-id");
    expect(state.streamInfo.source).toEqual(mistInfo.source);
  });

  it("ignores a direct-Mist result after switching to a placement resolution", async () => {
    const { state } = makeController();
    const pending = deferred<any>();
    state.fetchMistStreamInfo = vi.fn().mockReturnValue(pending.promise);
    const resolving = state.resolveFromMistServer("https://eu.example", "old-stream");
    state.endpointResolutionEpoch++;
    state.endpointMode = "gateway";
    const endpoints = state.endpoints;
    pending.resolve(mistInfo);
    await resolving;
    expect(state.endpoints).toBe(endpoints);
    expect(state.endpointMode).toBe("gateway");
  });

  it.each(["reply", "failure"])(
    "ignores a stale Gateway %s without disturbing the replacement",
    async (outcome) => {
      const { state } = makeController();
      const pending = deferred<any>();
      vi.spyOn(GatewayClient.prototype, "resolve").mockReturnValue(pending.promise);
      state.hydrateFromSelectedMistEdge = vi.fn();
      const resolving = state.resolveFromGateway("https://gateway.example/graphql", "old-stream");
      state.endpointResolutionEpoch++;
      const endpoints = state.endpoints;
      state.setState("gateway_ready", { gatewayStatus: "ready" });
      if (outcome === "reply") pending.resolve({ primary: { nodeId: "obsolete" } });
      else pending.reject(new Error("obsolete request failed"));
      await resolving;
      expect(state.endpoints).toBe(endpoints);
      expect(state.state).toBe("gateway_ready");
      expect(state.hydrateFromSelectedMistEdge).not.toHaveBeenCalled();
      state.cleanup();
    }
  );
});
