import { describe, expect, it, vi, beforeEach, afterEach } from "vitest";

import { PlayerManager } from "../src/core/PlayerManager";
import type { IPlayer, StreamSource, StreamInfo, PlayerOptions } from "../src/core/PlayerInterface";

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

function makePlayer(overrides: Partial<IPlayer["capability"]> & { shortname: string }): IPlayer {
  const { shortname, ...rest } = overrides;
  return {
    capability: {
      name: rest.name ?? shortname,
      shortname,
      priority: rest.priority ?? 1,
      mimes: rest.mimes ?? ["application/x-mpegURL"],
    },
    isMimeSupported: vi.fn((mime: string) => {
      return (rest.mimes ?? ["application/x-mpegURL"]).includes(mime);
    }),
    isBrowserSupported: vi.fn(() => ["video", "audio"] as Array<"video" | "audio">),
    initialize: vi.fn(async (_c, _s, _o) => {
      return {
        play: vi.fn(),
        pause: vi.fn(),
        load: vi.fn(),
        currentTime: 0,
        duration: 0,
        muted: false,
        volume: 1,
        paused: true,
        readyState: 0,
        buffered: { length: 0, start: vi.fn(), end: vi.fn() },
        addEventListener: vi.fn(),
        removeEventListener: vi.fn(),
      } as unknown as HTMLVideoElement;
    }),
    destroy: vi.fn(),
    play: vi.fn(),
    pause: vi.fn(),
    seek: vi.fn(),
    setVolume: vi.fn(),
    setMuted: vi.fn(),
  } as unknown as IPlayer;
}

function makeStreamInfo(sources?: Partial<StreamSource>[]): StreamInfo {
  return {
    source: (
      sources ?? [{ url: "https://cdn.example.com/hls/live.m3u8", type: "application/x-mpegURL" }]
    ).map((s, i) => ({
      url: s.url ?? `https://cdn.example.com/source${i}`,
      type: s.type ?? "application/x-mpegURL",
      ...s,
    })),
    meta: {
      tracks: [
        { type: "video" as const, codec: "H264" },
        { type: "audio" as const, codec: "AAC" },
      ],
    },
    type: "live",
  };
}

// ---------------------------------------------------------------------------
// Tests
// ---------------------------------------------------------------------------

describe("PlayerManager", () => {
  beforeEach(() => {
    vi.spyOn(console, "log").mockImplementation(() => {});
    vi.spyOn(console, "error").mockImplementation(() => {});
    vi.spyOn(console, "warn").mockImplementation(() => {});
  });

  afterEach(() => {
    vi.restoreAllMocks();
  });

  // ===========================================================================
  // Player registration
  // ===========================================================================
  describe("player registration", () => {
    it("registers and retrieves players", () => {
      const mgr = new PlayerManager();
      const player = makePlayer({ shortname: "hls" });

      mgr.registerPlayer(player);
      expect(mgr.getRegisteredPlayers()).toHaveLength(1);
      expect(mgr.getRegisteredPlayers()[0].capability.shortname).toBe("hls");
    });

    it("unregisterPlayer removes and destroys", () => {
      const mgr = new PlayerManager();
      const player = makePlayer({ shortname: "hls" });

      mgr.registerPlayer(player);
      mgr.unregisterPlayer("hls");

      expect(mgr.getRegisteredPlayers()).toHaveLength(0);
      expect(player.destroy).toHaveBeenCalled();
    });

    it("unregisterPlayer no-op for unknown shortname", () => {
      const mgr = new PlayerManager();
      expect(() => mgr.unregisterPlayer("nope")).not.toThrow();
    });

    it("registration invalidates cache", () => {
      const mgr = new PlayerManager();
      const player = makePlayer({ shortname: "hls" });
      const info = makeStreamInfo();

      mgr.registerPlayer(player);
      mgr.getAllCombinations(info);
      expect(mgr.getCachedCombinations()).not.toBeNull();

      // Register another player -> cache invalidated
      mgr.registerPlayer(makePlayer({ shortname: "webrtc", mimes: ["video/webrtc"] }));
      expect(mgr.getCachedCombinations()).toBeNull();
    });
  });

  // ===========================================================================
  // getAllCombinations
  // ===========================================================================
  describe("getAllCombinations", () => {
    it("returns compatible combinations sorted by score", () => {
      const mgr = new PlayerManager();
      mgr.registerPlayer(makePlayer({ shortname: "hls", priority: 2 }));
      mgr.registerPlayer(
        makePlayer({ shortname: "dash", priority: 1, mimes: ["application/dash+xml"] })
      );

      const info = makeStreamInfo([
        { url: "https://cdn.example.com/live.m3u8", type: "application/x-mpegURL" },
        { url: "https://cdn.example.com/live.mpd", type: "application/dash+xml" },
      ]);

      const combos = mgr.getAllCombinations(info);
      const compatible = combos.filter((c) => c.compatible);
      expect(compatible.length).toBeGreaterThanOrEqual(2);
      // Compatible combos should be sorted by score descending
      for (let i = 1; i < compatible.length; i++) {
        expect(compatible[i - 1].score).toBeGreaterThanOrEqual(compatible[i].score);
      }
    });

    it("skips unsupported MIME types entirely", () => {
      const mgr = new PlayerManager();
      mgr.registerPlayer(makePlayer({ shortname: "hls" }));

      const info = makeStreamInfo([
        { url: "https://cdn.example.com/live.mpd", type: "application/dash+xml" },
      ]);

      const combos = mgr.getAllCombinations(info);
      expect(combos).toHaveLength(0);
    });

    it("marks codec-incompatible combinations", () => {
      const player = makePlayer({ shortname: "hls" });
      (player.isBrowserSupported as ReturnType<typeof vi.fn>).mockReturnValue(false);

      const mgr = new PlayerManager();
      mgr.registerPlayer(player);

      const info = makeStreamInfo();
      const combos = mgr.getAllCombinations(info);
      expect(combos[0].compatible).toBe(false);
      expect(combos[0].codecIncompatible).toBe(true);
    });

    it("caches results for same stream content", () => {
      const mgr = new PlayerManager();
      mgr.registerPlayer(makePlayer({ shortname: "hls" }));

      const info = makeStreamInfo();
      const combos1 = mgr.getAllCombinations(info);
      const combos2 = mgr.getAllCombinations(info);

      expect(combos1).toBe(combos2); // Same reference = cache hit
    });

    it("recomputes on content change", () => {
      const mgr = new PlayerManager();
      mgr.registerPlayer(makePlayer({ shortname: "hls" }));

      const info1 = makeStreamInfo([{ type: "application/x-mpegURL" }]);
      const combos1 = mgr.getAllCombinations(info1);

      const info2 = makeStreamInfo([{ type: "application/x-mpegURL" }, { type: "video/webrtc" }]);
      const combos2 = mgr.getAllCombinations(info2);

      expect(combos1).not.toBe(combos2);
    });

    it("emits selection-changed when winner changes", () => {
      const mgr = new PlayerManager();
      const handler = vi.fn();
      mgr.on("selection-changed", handler);

      mgr.registerPlayer(makePlayer({ shortname: "hls" }));
      mgr.getAllCombinations(makeStreamInfo());

      expect(handler).toHaveBeenCalledTimes(1);
      expect(handler).toHaveBeenCalledWith(expect.objectContaining({ player: "hls" }));
    });

    it("emits combinations-updated on every call that computes", () => {
      const mgr = new PlayerManager();
      const handler = vi.fn();
      mgr.on("combinations-updated", handler);

      mgr.registerPlayer(makePlayer({ shortname: "hls" }));
      mgr.getAllCombinations(makeStreamInfo());

      expect(handler).toHaveBeenCalledTimes(1);
    });

    it("marks missing tracks as partially compatible with penalty", () => {
      const player = makePlayer({ shortname: "hls" });
      // Returns only audio (missing video)
      (player.isBrowserSupported as ReturnType<typeof vi.fn>).mockReturnValue(["audio"]);

      const mgr = new PlayerManager();
      mgr.registerPlayer(player);

      const info = makeStreamInfo();
      const combos = mgr.getAllCombinations(info);
      expect(combos[0].compatible).toBe(true);
      expect(combos[0].missingTracks).toContain("video");
    });
  });

  // ===========================================================================
  // selectBestPlayer
  // ===========================================================================
  describe("selectBestPlayer", () => {
    it("returns best compatible player", () => {
      const mgr = new PlayerManager();
      mgr.registerPlayer(makePlayer({ shortname: "hls" }));

      const result = mgr.selectBestPlayer(makeStreamInfo());
      expect(result).not.toBe(false);
      if (result) {
        expect(result.player).toBe("hls");
        expect(result.score).toBeGreaterThan(0);
      }
    });

    it("returns false when no compatible player", () => {
      const mgr = new PlayerManager();
      mgr.registerPlayer(makePlayer({ shortname: "hls" }));

      const info = makeStreamInfo([{ type: "video/webrtc" }]);
      const result = mgr.selectBestPlayer(info);
      expect(result).toBe(false);
    });

    it("emits playerSelected event", () => {
      const mgr = new PlayerManager();
      const handler = vi.fn();
      mgr.on("playerSelected", handler);

      mgr.registerPlayer(makePlayer({ shortname: "hls" }));
      mgr.selectBestPlayer(makeStreamInfo());

      expect(handler).toHaveBeenCalledWith(expect.objectContaining({ player: "hls" }));
    });

    it("respects forcePlayer filter", () => {
      const mgr = new PlayerManager();
      mgr.registerPlayer(makePlayer({ shortname: "hls" }));
      mgr.registerPlayer(
        makePlayer({
          shortname: "webrtc",
          mimes: ["application/x-mpegURL", "video/webrtc"],
        })
      );

      const info = makeStreamInfo();
      const result = mgr.selectBestPlayer(info, { forcePlayer: "webrtc" });
      expect(result).not.toBe(false);
      if (result) {
        expect(result.player).toBe("webrtc");
      }
    });

    it("respects forceType filter", () => {
      const mgr = new PlayerManager();
      const hlsPlayer = makePlayer({
        shortname: "hls",
        mimes: ["application/x-mpegURL"],
      });
      const dashPlayer = makePlayer({
        shortname: "dash",
        mimes: ["application/dash+xml"],
      });
      mgr.registerPlayer(hlsPlayer);
      mgr.registerPlayer(dashPlayer);

      const info = makeStreamInfo([
        { type: "application/x-mpegURL" },
        { type: "application/dash+xml" },
      ]);

      const result = mgr.selectBestPlayer(info, { forceType: "application/dash+xml" });
      expect(result).not.toBe(false);
      if (result) {
        expect(result.source.type).toBe("application/dash+xml");
      }
    });

    it("respects forceSource filter", () => {
      const mgr = new PlayerManager();
      mgr.registerPlayer(makePlayer({ shortname: "hls" }));

      const info = makeStreamInfo([
        { type: "application/x-mpegURL", url: "https://cdn.example.com/a.m3u8" },
        { type: "application/x-mpegURL", url: "https://cdn.example.com/b.m3u8" },
      ]);

      const result = mgr.selectBestPlayer(info, { forceSource: 1 });
      expect(result).not.toBe(false);
      if (result) {
        expect(result.source_index).toBe(1);
      }
    });
  });

  // ===========================================================================
  // Cache management
  // ===========================================================================
  describe("caching", () => {
    it("invalidateCache clears cached data", () => {
      const mgr = new PlayerManager();
      mgr.registerPlayer(makePlayer({ shortname: "hls" }));
      mgr.getAllCombinations(makeStreamInfo());

      expect(mgr.getCachedCombinations()).not.toBeNull();
      expect(mgr.getCurrentSelection()).not.toBeNull();

      mgr.invalidateCache();
      expect(mgr.getCachedCombinations()).toBeNull();
      expect(mgr.getCurrentSelection()).toBeNull();
    });
  });

  // ===========================================================================
  // Error handling
  // ===========================================================================
  describe("error handling", () => {
    it("reportError classifies and returns action", () => {
      const mgr = new PlayerManager();
      const action = mgr.reportError(new Error("network timeout"));
      expect(action.type).toBeDefined();
    });

    it("reportQualityChange emits qualityChanged", () => {
      const mgr = new PlayerManager();
      const handler = vi.fn();
      mgr.on("qualityChanged", handler);

      mgr.reportQualityChange("down", "bandwidth dropped");
      expect(handler).toHaveBeenCalledWith({
        direction: "down",
        reason: "bandwidth dropped",
      });
    });

    it("getErrorClassifier returns classifier", () => {
      const mgr = new PlayerManager();
      expect(mgr.getErrorClassifier()).toBeDefined();
    });
  });

  // ===========================================================================
  // Fallback state
  // ===========================================================================
  describe("fallback state", () => {
    it("canAttemptFallback is false without prior init", () => {
      const mgr = new PlayerManager();
      expect(mgr.canAttemptFallback()).toBe(false);
    });

    it("getRemainingFallbackAttempts defaults to Infinity (exhaust all combos)", () => {
      const mgr = new PlayerManager();
      expect(mgr.getRemainingFallbackAttempts()).toBe(Infinity);
    });

    it("getRemainingFallbackAttempts uses custom max", () => {
      const mgr = new PlayerManager({ maxFallbackAttempts: 5 });
      expect(mgr.getRemainingFallbackAttempts()).toBe(5);
    });
  });

  // ===========================================================================
  // Event system
  // ===========================================================================
  describe("event system", () => {
    it("on returns unsubscribe function", () => {
      const mgr = new PlayerManager();
      const handler = vi.fn();
      const unsub = mgr.on("playerSelected", handler);

      mgr.registerPlayer(makePlayer({ shortname: "hls" }));
      mgr.selectBestPlayer(makeStreamInfo());
      expect(handler).toHaveBeenCalledTimes(1);

      unsub();
      handler.mockClear();
      mgr.invalidateCache();
      mgr.selectBestPlayer(makeStreamInfo());
      expect(handler).not.toHaveBeenCalled();
    });

    it("removeAllListeners clears all", () => {
      const mgr = new PlayerManager();
      const handler = vi.fn();
      mgr.on("playerSelected", handler);

      mgr.removeAllListeners();
      mgr.registerPlayer(makePlayer({ shortname: "hls" }));
      mgr.selectBestPlayer(makeStreamInfo());
      expect(handler).not.toHaveBeenCalled();
    });

    it("handler error does not crash emit", () => {
      const mgr = new PlayerManager();
      mgr.on("playerSelected", () => {
        throw new Error("boom");
      });

      mgr.registerPlayer(makePlayer({ shortname: "hls" }));
      expect(() => mgr.selectBestPlayer(makeStreamInfo())).not.toThrow();
    });
  });

  // ===========================================================================
  // getCurrentPlayer
  // ===========================================================================
  describe("getCurrentPlayer", () => {
    it("returns null before initialization", () => {
      const mgr = new PlayerManager();
      expect(mgr.getCurrentPlayer()).toBeNull();
    });
  });

  // ===========================================================================
  // Browser capabilities
  // ===========================================================================
  describe("getBrowserCapabilities", () => {
    it("returns browser info and available players", () => {
      // getBrowserInfo/getBrowserCompatibility access window + navigator
      const origWindow = (globalThis as any).window;
      const origNavigator = Object.getOwnPropertyDescriptor(globalThis, "navigator");

      (globalThis as any).window = {
        location: { protocol: "https:", hostname: "localhost" },
      };
      Object.defineProperty(globalThis, "navigator", {
        value: { userAgent: "Mozilla/5.0 (Macintosh) AppleWebKit/537.36 Chrome/120" },
        writable: true,
        configurable: true,
      });

      try {
        const mgr = new PlayerManager();
        mgr.registerPlayer(makePlayer({ shortname: "hls" }));

        const caps = mgr.getBrowserCapabilities();
        expect(caps.browser).toBeDefined();
        expect(caps.availablePlayers).toHaveLength(1);
        expect(caps.supportedMimeTypes).toContain("application/x-mpegURL");
      } finally {
        (globalThis as any).window = origWindow;
        if (origNavigator) {
          Object.defineProperty(globalThis, "navigator", origNavigator);
        }
      }
    });
  });

  // ===========================================================================
  // Destroy
  // ===========================================================================
  describe("destroy", () => {
    it("can be called without error when no player", async () => {
      const mgr = new PlayerManager();
      await expect(mgr.destroy()).resolves.toBeUndefined();
    });
  });

  // ===========================================================================
  // Fallback playhead preservation (D03-HIGH-1 / D13-HIGH-1)
  // ===========================================================================
  describe("fallback playhead preservation", () => {
    it("VOD fallback restores currentTime", async () => {
      const mgr = new PlayerManager({ debug: false });
      // Use different MIME types so each player maps to a different source,
      // avoiding the source-filter issue where only the "best" player per source
      // is checked against excludedCombos.
      // Lower priority number = higher score (selected first), so player1 is selected first.
      const player1 = makePlayer({
        shortname: "hls",
        mimes: ["application/x-mpegURL"],
        priority: 1,
      });
      const player2 = makePlayer({ shortname: "native", mimes: ["video/mp4"], priority: 2 });
      (player1 as any).getCurrentTime = vi.fn(() => 60000);

      mgr.registerPlayer(player1);
      mgr.registerPlayer(player2);

      const container = { innerHTML: "", classList: { add: vi.fn() } } as any;
      const vodStreamInfo = makeStreamInfo([
        { url: "https://cdn.example.com/hls/live.m3u8", type: "application/x-mpegURL" },
        { url: "https://cdn.example.com/live.mp4", type: "video/mp4" },
      ]);
      vodStreamInfo.type = "vod";

      await mgr.initializePlayer(container, vodStreamInfo, {});

      const result = await mgr.tryPlaybackFallback();
      expect(result).toBe(true);

      const newPlayer = mgr.getCurrentPlayer();
      if (newPlayer?.seek) {
        expect(newPlayer.seek).toHaveBeenCalledWith(60000);
      }
    });

    it("live fallback does NOT restore stale time", async () => {
      const mgr = new PlayerManager({ debug: false });
      const player1 = makePlayer({
        shortname: "hls",
        mimes: ["application/x-mpegURL"],
        priority: 1,
      });
      const player2 = makePlayer({ shortname: "native", mimes: ["video/mp4"], priority: 2 });
      (player1 as any).getCurrentTime = vi.fn(() => 30000);

      mgr.registerPlayer(player1);
      mgr.registerPlayer(player2);

      const container = { innerHTML: "", classList: { add: vi.fn() } } as any;
      const liveStreamInfo = makeStreamInfo([
        { url: "https://cdn.example.com/hls/live.m3u8", type: "application/x-mpegURL" },
        { url: "https://cdn.example.com/live.mp4", type: "video/mp4" },
      ]);
      liveStreamInfo.type = "live";

      await mgr.initializePlayer(container, liveStreamInfo, {});

      const result = await mgr.tryPlaybackFallback();
      expect(result).toBe(true);

      const newPlayer = mgr.getCurrentPlayer();
      if (newPlayer?.seek) {
        expect(newPlayer.seek).not.toHaveBeenCalled();
      }
    });
  });
});
