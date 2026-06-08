import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import { HlsJsPlayerImpl } from "../src/players/HlsJsPlayer";

const hlsState = vi.hoisted(() => ({
  instances: [] as any[],
  isSupported: vi.fn(() => true),
  loadSource: vi.fn(),
  recoverMediaError: vi.fn(),
}));

vi.mock("hls.js", () => {
  class MockHls {
    static Events = {
      MEDIA_ATTACHED: "mediaAttached",
      ERROR: "error",
      MANIFEST_PARSED: "manifestParsed",
    };

    static isSupported = hlsState.isSupported;

    handlers = new Map<string, Array<(event: string, data?: unknown) => void>>();

    constructor(readonly config: unknown) {
      hlsState.instances.push(this);
    }

    attachMedia(_video: HTMLVideoElement): void {}

    loadSource(url: string): void {
      hlsState.loadSource(url);
    }

    recoverMediaError(): void {
      hlsState.recoverMediaError();
    }

    on(event: string, handler: (event: string, data?: unknown) => void): void {
      const handlers = this.handlers.get(event) ?? [];
      handlers.push(handler);
      this.handlers.set(event, handlers);
    }

    emit(event: string, data?: unknown): void {
      for (const handler of this.handlers.get(event) ?? []) {
        handler(event, data);
      }
    }

    destroy(): void {}
  }

  return { default: MockHls };
});

describe("HlsJsPlayerImpl", () => {
  const originalDocument = globalThis.document;

  beforeEach(() => {
    hlsState.instances.length = 0;
    hlsState.isSupported.mockReturnValue(true);
    hlsState.loadSource.mockClear();
    hlsState.recoverMediaError.mockClear();

    Object.defineProperty(globalThis, "document", {
      configurable: true,
      value: {
        createElement: vi.fn((tagName: string) => {
          const children: any[] = [];
          const listeners = new Map<string, Array<(event?: Event) => void>>();
          return {
            tagName: tagName.toUpperCase(),
            children,
            classList: { add: vi.fn() },
            setAttribute: vi.fn(),
            addEventListener: vi.fn((event: string, handler: (event?: Event) => void) => {
              const eventHandlers = listeners.get(event) ?? [];
              eventHandlers.push(handler);
              listeners.set(event, eventHandlers);
            }),
            removeEventListener: vi.fn(),
            dispatchEvent: vi.fn((event: Event) => {
              for (const handler of listeners.get(event.type) ?? []) handler(event);
              return true;
            }),
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
            duration: Infinity,
            currentTime: 0,
            readyState: 0,
          };
        }),
      },
    });
  });

  afterEach(() => {
    Object.defineProperty(globalThis, "document", {
      configurable: true,
      value: originalDocument,
    });
  });

  it("returns the video element after HLS is attached and emits ready immediately", async () => {
    const player = new HlsJsPlayerImpl();
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

    await vi.waitFor(() => expect(hlsState.instances).toHaveLength(1));
    const hls = hlsState.instances[0];
    hls.emit("mediaAttached");

    await vi.waitFor(() =>
      expect(hlsState.loadSource).toHaveBeenCalledWith("https://edge.example/live/index.m3u8")
    );
    const video = container.querySelector("video") as HTMLVideoElement;
    expect(video.autoplay).toBe(false);

    const initializedVideo = await initialization;

    expect(initializedVideo).toBe(video);
    expect(onReady).toHaveBeenCalledTimes(1);
    expect(onReady).toHaveBeenCalledWith(video);
    await player.destroy();
  });

  it("only recovers fatal HLS media errors when the media element is in error", async () => {
    const player = new HlsJsPlayerImpl();
    const container = document.createElement("div");
    const onError = vi.fn();
    const playerErrors: string[] = [];
    player.on("error", (error) => playerErrors.push(String(error)));

    await player.initialize(
      container,
      {
        type: "html5/application/vnd.apple.mpegurl;version=7",
        url: "https://edge.example/live/index.m3u8",
      },
      { autoplay: true, muted: true, onError },
      { source: [], meta: { tracks: [] }, type: "live" }
    );

    await vi.waitFor(() => expect(hlsState.instances).toHaveLength(1));
    const hls = hlsState.instances[0];
    hls.emit("mediaAttached");
    const video = container.querySelector("video") as HTMLVideoElement & { error: MediaError };
    video.error = { code: 3, message: "decode" } as MediaError;

    hls.emit("error", { fatal: true, type: "mediaError", details: "bufferAppendError" });
    expect(hlsState.recoverMediaError).toHaveBeenCalledTimes(1);
    expect(onError).not.toHaveBeenCalled();

    hls.emit("error", { fatal: true, type: "mediaError", details: "bufferAppendError" });

    expect(playerErrors).toContain("HLS fatal error: mediaError:bufferAppendError");
    expect(onError).not.toHaveBeenCalled();
    await player.destroy();
  });

  it("keeps native HLS seekable range instead of controller range hints", () => {
    const player = new HlsJsPlayerImpl();
    const video = document.createElement("video") as any;
    video.seekable = { length: 1, start: () => 4, end: () => 64 };
    (player as any).videoElement = video;

    player.setSeekableRangeHint({ start: 100_000, end: 160_000 });

    expect(player.getSeekableRange()).toEqual({ start: 4_000, end: 64_000 });
    expect(player.getDuration()).toBe(64_000);
  });

  it("uses hls.js liveSyncPosition for jump-to-live", () => {
    const player = new HlsJsPlayerImpl();
    const video = document.createElement("video") as any;
    video.currentTime = 12;
    video.seekable = { length: 1, start: () => 4, end: () => 64 };
    (player as any).videoElement = video;
    (player as any).hls = { liveSyncPosition: 58 };

    player.jumpToLive();

    expect(video.currentTime).toBe(58);
  });

  it("does not override manifest-driven live-edge settings by default", async () => {
    const player = new HlsJsPlayerImpl();
    const container = document.createElement("div");

    await player.initialize(
      container,
      {
        type: "html5/application/vnd.apple.mpegurl;version=7",
        url: "https://edge.example/live/index.m3u8",
      },
      { autoplay: false, muted: true },
      { source: [], meta: { tracks: [] }, type: "live" }
    );

    await vi.waitFor(() => expect(hlsState.instances).toHaveLength(1));
    const hls = hlsState.instances[0];
    hls.emit("mediaAttached");

    expect(hls.config).toEqual(
      expect.objectContaining({
        lowLatencyMode: true,
        abrEwmaDefaultEstimate: 5_000_000,
      })
    );
    expect(hls.config).not.toHaveProperty("liveSyncDuration");
    expect(hls.config).not.toHaveProperty("liveMaxLatencyDuration");
    expect(hls.config).not.toHaveProperty("maxBufferLength");
    expect(hls.config).not.toHaveProperty("maxMaxBufferLength");
    await player.destroy();
  });
});
