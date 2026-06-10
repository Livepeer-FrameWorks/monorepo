import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import { PlayerManager } from "../src/core/PlayerManager";
import type { IPlayer, StreamSource, StreamInfo } from "../src/core/PlayerInterface";

const MIME = "application/x-mpegURL";

function makePlayer(
  shortname: string,
  opts: { mimes?: string[]; priority?: number; browserSupported?: boolean; name?: string } = {}
): IPlayer {
  const mimes = opts.mimes ?? [MIME];
  return {
    capability: {
      name: opts.name ?? shortname,
      shortname,
      priority: opts.priority ?? 1,
      mimes,
    },
    isMimeSupported: vi.fn((mime: string) => mimes.includes(mime)),
    isBrowserSupported: vi.fn(() =>
      opts.browserSupported === false ? false : (["video", "audio"] as Array<"video" | "audio">)
    ),
    initialize: vi.fn(),
    destroy: vi.fn(),
    play: vi.fn(),
    pause: vi.fn(),
    seek: vi.fn(),
    setVolume: vi.fn(),
    setMuted: vi.fn(),
  } as unknown as IPlayer;
}

function makeStreamInfo(type = MIME): StreamInfo {
  return {
    source: [{ url: "https://cdn.example/live.m3u8", type }],
    meta: {
      tracks: [
        { type: "video", codec: "H264" },
        { type: "audio", codec: "AAC" },
      ],
    },
    type: "live",
  };
}

describe("PlayerManager — testSource", () => {
  beforeEach(() => {
    vi.spyOn(console, "log").mockImplementation(() => {});
  });
  afterEach(() => vi.restoreAllMocks());

  it("reports canPlay with the capable players for a supported source", async () => {
    const mgr = new PlayerManager();
    mgr.registerPlayer(makePlayer("hls"));
    const info = makeStreamInfo();
    const result = await mgr.testSource(info.source[0] as StreamSource, info);
    expect(result.canPlay).toBe(true);
    expect(result.players).toContain("hls");
  });

  it("excludes players that support the mime but fail browser support", async () => {
    const mgr = new PlayerManager();
    mgr.registerPlayer(makePlayer("hls"));
    mgr.registerPlayer(makePlayer("dash", { browserSupported: false }));
    const info = makeStreamInfo();
    const result = await mgr.testSource(info.source[0] as StreamSource, info);
    expect(result.players).toEqual(["hls"]); // dash filtered out
  });

  it("reports canPlay=false when no player handles the source type", async () => {
    const mgr = new PlayerManager();
    mgr.registerPlayer(makePlayer("hls", { mimes: ["video/other"] }));
    const info = makeStreamInfo(); // type x-mpegURL, unsupported
    const result = await mgr.testSource(info.source[0] as StreamSource, info);
    expect(result).toEqual({ canPlay: false, players: [] });
  });
});

describe("PlayerManager — getBrowserCapabilities", () => {
  beforeEach(() => {
    vi.spyOn(console, "log").mockImplementation(() => {});
    vi.stubGlobal("window", {
      MediaSource: class {},
      RTCPeerConnection: class {},
      WebSocket: class {},
      location: { protocol: "https:" },
    });
    vi.stubGlobal("navigator", { userAgent: "Mozilla/5.0 Chrome/120 Safari/537.36" });
  });
  afterEach(() => {
    vi.restoreAllMocks();
    vi.unstubAllGlobals();
  });

  it("aggregates supported mimes (deduped, sorted) and players (by priority)", () => {
    const mgr = new PlayerManager();
    mgr.registerPlayer(makePlayer("hls", { mimes: ["b/x", "a/x"], priority: 3 }));
    mgr.registerPlayer(makePlayer("native", { mimes: ["a/x"], priority: 1 }));

    const caps = mgr.getBrowserCapabilities();
    expect(caps.supportedMimeTypes).toEqual(["a/x", "b/x"]); // deduped + sorted
    expect(caps.availablePlayers.map((p) => p.shortname)).toEqual(["native", "hls"]); // by priority
    expect(caps.browser).toBeDefined();
    expect(caps.compatibility).toBeDefined();
  });
});

describe("PlayerManager — debug scorer summary", () => {
  it("logs a scorer summary on recompute when debug is enabled", () => {
    const logSpy = vi.spyOn(console, "log").mockImplementation(() => {});
    const mgr = new PlayerManager({ debug: true });
    mgr.registerPlayer(makePlayer("hls"));
    mgr.getAllCombinations(makeStreamInfo());

    const loggedScorer = logSpy.mock.calls.some((c) => String(c[0]).includes("Scorer"));
    expect(loggedScorer).toBe(true);
    logSpy.mockRestore();
  });
});
