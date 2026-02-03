import { describe, expect, it } from "vitest";

import type { StreamInfo, StreamSource } from "../src/core/PlayerInterface";
import { PlayerManager } from "../src/core/PlayerManager";
import type { IPlayer, PlayerCapability, PlayerOptions } from "../src/core/PlayerInterface";

class TestPlayer implements IPlayer {
  readonly capability: PlayerCapability;
  private readonly supportedTypes: Set<string>;
  private readonly failurePredicate: (source: StreamSource) => boolean;
  public readonly initializedSources: StreamSource[] = [];

  constructor(options: {
    shortname: string;
    priority: number;
    supportedTypes: string[];
    shouldFail: (source: StreamSource) => boolean;
  }) {
    this.capability = {
      name: options.shortname,
      shortname: options.shortname,
      priority: options.priority,
      mimes: options.supportedTypes,
    };
    this.supportedTypes = new Set(options.supportedTypes);
    this.failurePredicate = options.shouldFail;
  }

  isMimeSupported(mimetype: string): boolean {
    return this.supportedTypes.has(mimetype);
  }

  isBrowserSupported(): boolean | string[] {
    return ["video", "audio"];
  }

  async initialize(
    _container: HTMLElement,
    source: StreamSource,
    _options: PlayerOptions
  ): Promise<HTMLVideoElement> {
    this.initializedSources.push(source);
    if (this.failurePredicate(source)) {
      throw new Error("not supported");
    }
    return {} as HTMLVideoElement;
  }

  destroy(): void {
    return;
  }

  getVideoElement(): HTMLVideoElement | null {
    return null;
  }

  on(): void {
    return;
  }

  off(): void {
    return;
  }
}

const buildStreamInfo = (sources: StreamSource[]): StreamInfo => ({
  source: sources,
  meta: { tracks: [{ type: "video", codec: "H264", codecstring: "avc1.42E01E" }] },
  type: "live",
});

const createContainer = () =>
  ({
    innerHTML: "",
  }) as HTMLElement;

describe("PlayerManager fallback exclusions", () => {
  it("allows same player to try a different protocol after a failure", async () => {
    const sources: StreamSource[] = [
      { url: "https://example.com/stream.m3u8", type: "html5/application/vnd.apple.mpegurl" },
      { url: "https://example.com/stream.mpd", type: "dash/video/mp4" },
    ];
    const player = new TestPlayer({
      shortname: "tester",
      priority: 1,
      supportedTypes: sources.map((source) => source.type),
      shouldFail: (source) => source.type === "html5/application/vnd.apple.mpegurl",
    });

    const manager = new PlayerManager({ autoFallback: true, maxFallbackAttempts: 2 });
    manager.registerPlayer(player);

    await manager.initializePlayer(createContainer(), buildStreamInfo(sources));

    expect(player.initializedSources.map((source) => source.type)).toEqual([
      "html5/application/vnd.apple.mpegurl",
      "dash/video/mp4",
    ]);
  });

  it("allows same protocol on a different URL after a failure", async () => {
    const sources: StreamSource[] = [
      { url: "https://cdn-a.example.com/stream.m3u8", type: "html5/application/vnd.apple.mpegurl" },
      { url: "https://cdn-b.example.com/stream.m3u8", type: "html5/application/vnd.apple.mpegurl" },
    ];
    const player = new TestPlayer({
      shortname: "tester",
      priority: 1,
      supportedTypes: ["html5/application/vnd.apple.mpegurl"],
      shouldFail: (source) => source.url.includes("cdn-a"),
    });

    const manager = new PlayerManager({ autoFallback: true, maxFallbackAttempts: 2 });
    manager.registerPlayer(player);

    await manager.initializePlayer(createContainer(), buildStreamInfo(sources));

    expect(player.initializedSources.map((source) => source.url)).toEqual([
      "https://cdn-a.example.com/stream.m3u8",
      "https://cdn-b.example.com/stream.m3u8",
    ]);
  });
});
