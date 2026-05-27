import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import { VideoJsPlayerImpl } from "../src/players/VideoJsPlayer";
import type { StreamInfo, StreamSource } from "../src/core/PlayerInterface";

describe("VideoJsPlayerImpl", () => {
  const originalDocument = globalThis.document;
  const originalLocation = globalThis.location;
  const originalWindow = globalThis.window;
  let canPlayType: ReturnType<typeof vi.fn>;

  beforeEach(() => {
    vi.spyOn(console, "debug").mockImplementation(() => {});

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
});
