import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import { VideoJsPlayerImpl } from "../src/players/VideoJsPlayer";
import type { StreamInfo, StreamSource } from "../src/core/PlayerInterface";

const videoJsState = vi.hoisted(() => ({
  factory: vi.fn(),
  players: [] as any[],
}));

function createFakeElement(tagName: string): any {
  const children: any[] = [];
  return {
    tagName: tagName.toUpperCase(),
    children,
    classList: { add: vi.fn() },
    setAttribute: vi.fn(),
    addEventListener: vi.fn(),
    removeEventListener: vi.fn(),
    dispatchEvent: vi.fn(),
    appendChild(child: any) {
      children.push(child);
      return child;
    },
    querySelector(selector: string) {
      return children.find((child) => child.tagName?.toLowerCase() === selector) ?? null;
    },
    canPlayType: vi.fn(() => ""),
    buffered: { length: 0 },
    controls: false,
    autoplay: false,
    muted: false,
    currentTime: 0,
    readyState: 0,
    networkState: 0,
    videoWidth: 0,
    videoHeight: 0,
  };
}

vi.mock("video.js", () => {
  const videojs = vi.fn((video: HTMLVideoElement, options: Record<string, unknown>) => {
    videoJsState.factory(video, options);
    const handlers = new Map<string, Array<() => void>>();
    const readyHandlers: Array<() => void> = [];
    const element = createFakeElement("div");
    const player = {
      on(event: string, handler: () => void): void {
        const eventHandlers = handlers.get(event) ?? [];
        eventHandlers.push(handler);
        handlers.set(event, eventHandlers);
      },
      emit(event: string): void {
        for (const handler of handlers.get(event) ?? []) handler();
      },
      ready(handler: () => void): void {
        readyHandlers.push(handler);
      },
      emitReady(): void {
        for (const handler of readyHandlers) handler();
      },
      el: () => element,
      tech: () => ({ name: "Html5", vhs: null }),
      duration: () => Infinity,
      currentTime: () => video.currentTime,
      error: () => null,
      dispose: vi.fn(),
    };
    videoJsState.players.push(player);
    return player;
  });

  return { default: videojs };
});

describe("VideoJsPlayerImpl", () => {
  const originalDocument = globalThis.document;
  const originalLocation = globalThis.location;
  const originalWindow = globalThis.window;
  let canPlayType: ReturnType<typeof vi.fn>;

  beforeEach(() => {
    vi.spyOn(console, "debug").mockImplementation(() => {});

    videoJsState.factory.mockClear();
    videoJsState.players.length = 0;

    canPlayType = vi.fn((mimeType: string) => {
      if (mimeType === 'video/mp4;codecs="avc1.42E01E"') return "probably";
      if (mimeType === 'audio/mp4;codecs="mp4a.40.2"') return "probably";
      return "";
    });

    Object.defineProperty(globalThis, "document", {
      configurable: true,
      value: {
        createElement: vi.fn(() => ({ canPlayType })),
      },
    });
    Object.defineProperty(globalThis, "location", {
      configurable: true,
      value: { protocol: "https:" },
    });
    Object.defineProperty(globalThis, "window", {
      configurable: true,
      value: { location: { protocol: "https:" } },
    });
  });

  afterEach(() => {
    vi.restoreAllMocks();
    Object.defineProperty(globalThis, "document", {
      configurable: true,
      value: originalDocument,
    });
    Object.defineProperty(globalThis, "location", {
      configurable: true,
      value: originalLocation,
    });
    Object.defineProperty(globalThis, "window", {
      configurable: true,
      value: originalWindow,
    });
  });

  it("translates generic H264 track metadata before browser codec probing", () => {
    const source: StreamSource = {
      type: "html5/application/vnd.apple.mpegurl",
      url: "https://edge.example.com/view/hls/frameworks-demo/index.m3u8",
    };
    const streamInfo: StreamInfo = {
      source: [source],
      meta: { tracks: [{ type: "video", codec: "H264" }] },
      type: "live",
    };

    const result = new VideoJsPlayerImpl().isBrowserSupported(source.type, source, streamInfo);

    expect(result).toEqual(["video"]);
    expect(canPlayType).toHaveBeenCalledWith('video/mp4;codecs="avc1.42E01E"');
    expect(canPlayType).not.toHaveBeenCalledWith('video/mp4;codecs="H264"');
  });

  it("classifies MediaError.code === 3 as a deterministic decode failure", () => {
    const action = VideoJsPlayerImpl.classifyError({ code: 3, message: "media error" });
    expect(action.kind).toBe("error");
    // Must carry "decode" + the code so ErrorClassifier maps it to CODEC_DECODE_ERROR.
    expect(action.kind === "error" && action.message).toContain("decode");
    expect(action.kind === "error" && action.message).toContain("code=3");
  });

  it("classifies Firefox NS_ERROR overflow as a reload request", () => {
    const action = VideoJsPlayerImpl.classifyError({
      code: 4,
      message: "NS_ERROR_DOM_MEDIA_OVERFLOW_ERR (0x...)",
    });
    expect(action.kind).toBe("reload");
    expect(action.kind === "reload" && action.reason).toBe("NS_ERROR_DOM_MEDIA_OVERFLOW_ERR");
  });

  it("passes through generic playback errors unchanged", () => {
    const action = VideoJsPlayerImpl.classifyError({ code: 2, message: "network glitch" });
    expect(action).toEqual({ kind: "error", message: "network glitch" });
    const empty = VideoJsPlayerImpl.classifyError(null);
    expect(empty).toEqual({ kind: "error", message: "VideoJS playback error" });
  });

  it("keeps native VHS seekable range instead of controller range hints", () => {
    const player = new VideoJsPlayerImpl();
    const video = createFakeElement("video");
    video.duration = Infinity;
    video.seekable = { length: 1, start: () => 8, end: () => 68 };
    (player as any).videoElement = video;

    player.setSeekableRangeHint({ start: 100_000, end: 160_000 });

    expect(player.getSeekableRange()).toEqual({ start: 8_000, end: 68_000 });
    expect(player.getDuration()).toBe(68_000);
  });

  it("does not trust Mist shorthand codecstring values during browser codec probing", () => {
    const source: StreamSource = {
      type: "html5/application/vnd.apple.mpegurl",
      url: "https://edge.example.com/view/hls/frameworks-demo/index.m3u8",
    };
    const streamInfo: StreamInfo = {
      source: [source],
      meta: { tracks: [{ type: "video", codec: "H264", codecstring: "H264" }] },
      type: "live",
    };

    const result = new VideoJsPlayerImpl().isBrowserSupported(source.type, source, streamInfo);

    expect(result).toEqual(["video"]);
    expect(canPlayType).toHaveBeenCalledWith('video/mp4;codecs="avc1.42E01E"');
    expect(canPlayType).not.toHaveBeenCalledWith('video/mp4;codecs="H264"');
  });

  it("waits for playable media before emitting ready", async () => {
    Object.defineProperty(globalThis, "document", {
      configurable: true,
      value: {
        createElement: vi.fn(createFakeElement),
      },
    });

    const player = new VideoJsPlayerImpl();
    const container = document.createElement("div");
    const onReady = vi.fn();

    const initialization = player.initialize(
      container,
      {
        type: "html5/application/vnd.apple.mpegurl;version=7",
        url: "https://edge.example/live/index.m3u8",
      },
      { autoplay: true, muted: true, onReady },
      { source: [], meta: { tracks: [] }, type: "live" }
    );

    await vi.waitFor(() => expect(videoJsState.players).toHaveLength(1));
    expect(onReady).not.toHaveBeenCalled();
    expect(videoJsState.factory.mock.calls[0][1]).toEqual(
      expect.objectContaining({ autoplay: false })
    );
    expect(videoJsState.factory.mock.calls[0][1]).toEqual(
      expect.objectContaining({
        html5: expect.objectContaining({
          vhs: expect.objectContaining({ handlePartialData: false }),
        }),
      })
    );
    expect((videoJsState.factory.mock.calls[0][1] as any).html5.vhs).not.toHaveProperty(
      "liveRangeSafeTimeDelta"
    );
    expect((videoJsState.factory.mock.calls[0][1] as any).html5.vhs).not.toHaveProperty(
      "enableLowInitialPlaylist"
    );
    expect((videoJsState.factory.mock.calls[0][1] as any).html5.vhs).not.toHaveProperty(
      "bandwidth"
    );

    const video = container.querySelector("video") as HTMLVideoElement;
    expect(video.autoplay).toBe(false);

    videoJsState.players[0].emitReady();
    expect(onReady).not.toHaveBeenCalled();

    videoJsState.players[0].emit("loadedmetadata");
    expect(onReady).not.toHaveBeenCalled();

    videoJsState.players[0].emit("canplay");
    await initialization;

    expect(onReady).toHaveBeenCalledTimes(1);
    expect(onReady).toHaveBeenCalledWith(video);
  });
});
