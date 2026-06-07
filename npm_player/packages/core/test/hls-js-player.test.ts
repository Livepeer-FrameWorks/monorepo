import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import { HlsJsPlayerImpl } from "../src/players/HlsJsPlayer";

const hlsState = vi.hoisted(() => ({
  instances: [] as any[],
  isSupported: vi.fn(() => true),
  loadSource: vi.fn(),
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

  it("waits for playable media before completing startup", async () => {
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
    expect(onReady).not.toHaveBeenCalled();

    const video = container.querySelector("video") as HTMLVideoElement;
    expect(video.autoplay).toBe(false);

    hls.emit("manifestParsed");
    expect(onReady).not.toHaveBeenCalled();

    video.dispatchEvent(new Event("loadedmetadata"));
    expect(onReady).not.toHaveBeenCalled();

    video.dispatchEvent(new Event("canplay"));
    await initialization;

    expect(onReady).toHaveBeenCalledTimes(1);
    expect(onReady).toHaveBeenCalledWith(video);
  });
});
