// @vitest-environment jsdom

import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { MetaTrackManager } from "../src/core/MetaTrackManager";
import { PlayerController } from "../src/core/PlayerController";
import { StreamStateClient } from "../src/core/StreamStateClient";

const mediaUrl = "https://edge.example/hls/real-name/index.m3u8";

function makeAttachedController(contentId: string, primary: Record<string, unknown> = {}) {
  const video = document.createElement("video");
  const manager = {
    on: vi.fn(() => () => {}),
    off: vi.fn(),
    destroy: vi.fn(),
    getCurrentPlayer: vi.fn(() => null),
    getRegisteredPlayers: vi.fn(() => []),
    canAttemptFallback: vi.fn(() => false),
    initializePlayer: vi.fn(async () => video),
  };
  const controller = new PlayerController({
    contentId,
    endpoints: {
      primary: {
        nodeId: "pasted-url",
        protocol: "HLS",
        url: mediaUrl,
        outputs: { HLS: { protocol: "HLS", url: mediaUrl } },
        ...primary,
      },
      fallbacks: [],
    },
    playerManager: manager as any,
  });
  return { controller, manager, state: controller as any };
}

const mistJson = (body: unknown) => new Response(JSON.stringify(body), { status: 200 });

describe("attach() with caller-supplied endpoints", () => {
  let start: ReturnType<typeof vi.spyOn>;
  let connect: ReturnType<typeof vi.spyOn>;

  beforeEach(() => {
    start = vi.spyOn(StreamStateClient.prototype, "start").mockImplementation(() => {});
    connect = vi.spyOn(MetaTrackManager.prototype, "connect").mockImplementation(() => {});
  });

  afterEach(() => {
    vi.useRealTimers();
    vi.unstubAllGlobals();
    vi.restoreAllMocks();
  });

  it("plays a CDN URL without Mist polling when the host answers 404", async () => {
    const fetchMock = vi.fn().mockResolvedValue(new Response("", { status: 404 }));
    vi.stubGlobal("fetch", fetchMock);
    const { controller, manager, state } = makeAttachedController("pasted-url");

    await controller.attach(document.createElement("div"));
    state.initializeMetaTrackManager();

    expect(manager.initializePlayer).toHaveBeenCalledOnce();
    expect(fetchMock).toHaveBeenCalledOnce();
    expect(start).not.toHaveBeenCalled();
    expect(connect).not.toHaveBeenCalled();
    controller.destroy();
  });

  it("polls a Mist edge that knows the stream but reports it offline", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn().mockImplementation(async () => mistJson({ error: "Stream is offline" }))
    );
    const { controller, state } = makeAttachedController("real-name");

    await controller.attach(document.createElement("div"));

    expect(start).toHaveBeenCalledOnce();
    expect(state.streamStateClient.config.streamName).toBe("real-name");
    controller.destroy();
  });

  it("opens no Mist sockets when the edge does not know the content name", async () => {
    vi.stubGlobal(
      "fetch",
      vi
        .fn()
        .mockImplementation(async () =>
          mistJson({ error: "Could not retrieve stream. Sorry.", error_guru: "Stream not found" })
        )
    );
    const { controller, manager, state } = makeAttachedController("pasted-url");

    await controller.attach(document.createElement("div"));
    state.initializeMetaTrackManager();

    expect(manager.initializePlayer).toHaveBeenCalledOnce();
    expect(start).not.toHaveBeenCalled();
    expect(connect).not.toHaveBeenCalled();
    expect(state.metaTrackManager).toBeNull();
    controller.destroy();
  });

  it("starts polling once a briefly unreachable Mist edge answers", async () => {
    vi.useFakeTimers({ toFake: ["setTimeout", "clearTimeout"] });
    const fetchMock = vi
      .fn()
      .mockRejectedValueOnce(new TypeError("Failed to fetch"))
      .mockImplementation(async () => mistJson({ error: "Stream is offline" }));
    vi.stubGlobal("fetch", fetchMock);
    const { controller, manager } = makeAttachedController("real-name");

    await controller.attach(document.createElement("div"));

    expect(manager.initializePlayer).toHaveBeenCalledOnce();
    expect(start).not.toHaveBeenCalled();

    await vi.advanceTimersByTimeAsync(30_000);

    expect(fetchMock).toHaveBeenCalledTimes(2);
    expect(start).toHaveBeenCalledOnce();
    controller.destroy();
  });

  it("retries a 5xx reply with backoff instead of ruling out Mist", async () => {
    vi.useFakeTimers({ toFake: ["setTimeout", "clearTimeout"] });
    const fetchMock = vi
      .fn()
      .mockResolvedValueOnce(new Response("", { status: 502 }))
      .mockResolvedValueOnce(new Response("", { status: 503 }))
      .mockImplementation(async () => mistJson({ error: "Stream is offline" }));
    vi.stubGlobal("fetch", fetchMock);
    const { controller } = makeAttachedController("real-name");

    await controller.attach(document.createElement("div"));
    expect(start).not.toHaveBeenCalled();

    await vi.advanceTimersByTimeAsync(60_000);

    expect(fetchMock).toHaveBeenCalledTimes(3);
    expect(start).toHaveBeenCalledOnce();
    controller.destroy();
  });

  it("stops retrying an unreachable edge after the controller is destroyed", async () => {
    vi.useFakeTimers({ toFake: ["setTimeout", "clearTimeout"] });
    const fetchMock = vi.fn().mockRejectedValue(new TypeError("Failed to fetch"));
    vi.stubGlobal("fetch", fetchMock);
    const { controller } = makeAttachedController("real-name");

    await controller.attach(document.createElement("div"));
    controller.destroy();
    await vi.advanceTimersByTimeAsync(120_000);

    expect(fetchMock).toHaveBeenCalledOnce();
    expect(start).not.toHaveBeenCalled();
  });
});
