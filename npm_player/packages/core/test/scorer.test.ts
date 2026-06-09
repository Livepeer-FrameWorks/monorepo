import { describe, expect, it } from "vitest";

import {
  DEFAULT_TRACK_SCORES,
  calculateMaxScore,
  calculatePriorityScore,
  calculateSourceScore,
  calculateTrackScore,
  calculateQualityScore,
  calculateProtocolPenalty,
  calculateReliabilityScore,
  calculateLatencyScore,
  calculateStabilityScore,
  calculateBootScore,
  calculateVodScore,
  calculateRoutingBonus,
  isProtocolBlacklisted,
  scorePlayer,
  compareScores,
  scoreAndRankPlayers,
  meetsMinimumScore,
  SHORT_CLIP_MS,
} from "../src/core/scorer";

const ALL_PROTOCOLS = {
  whep: { mime: "whep", player: "native" },
  cmaf: { mime: "html5/application/vnd.apple.mpegurl;version=7", player: "hlsjs" },
  hlsTs: { mime: "html5/application/vnd.apple.mpegurl", player: "hlsjs" },
  dash: { mime: "dash/video/mp4", player: "dashjs" },
  mp4: { mime: "html5/video/mp4", player: "native" },
  mews: { mime: "ws/video/mp4", player: "mews" },
  webcodecs: { mime: "ws/video/raw", player: "webcodecs" },
} as const;

/** Score every protocol for a full A/V stream in a given mode + context, return a name→total map. */
function scoreAllProtocols(
  playbackMode: "low-latency" | "balanced" | "quality" | "vod" | "auto",
  ctx: { isLive?: boolean; durationMs?: number } = {}
): Record<keyof typeof ALL_PROTOCOLS, number> {
  const out = {} as Record<keyof typeof ALL_PROTOCOLS, number>;
  for (const [name, { mime, player }] of Object.entries(ALL_PROTOCOLS)) {
    out[name as keyof typeof ALL_PROTOCOLS] = scorePlayer(["video", "audio"], 0, 0, {
      maxPriority: 100,
      totalSources: 1,
      playerShortname: player,
      mimeType: mime,
      playbackMode,
      ...ctx,
    }).total;
  }
  return out;
}

function winner(scores: Record<string, number>): string {
  return Object.entries(scores).sort((a, b) => b[1] - a[1])[0][0];
}

describe("scorer", () => {
  // =========================================================================
  // calculateTrackScore
  // =========================================================================
  describe("calculateTrackScore", () => {
    it("returns 1.9 for boolean true", () => {
      expect(calculateTrackScore(true)).toBe(1.9);
    });

    it("returns 0 for false or empty array", () => {
      expect(calculateTrackScore(false as any)).toBe(0);
      expect(calculateTrackScore([])).toBe(0);
    });

    it("sums default track scores", () => {
      expect(calculateTrackScore(["video"], DEFAULT_TRACK_SCORES)).toBe(2.0);
      expect(calculateTrackScore(["audio"], DEFAULT_TRACK_SCORES)).toBe(1.0);
      expect(calculateTrackScore(["subtitle"], DEFAULT_TRACK_SCORES)).toBe(0.5);
      expect(calculateTrackScore(["video", "audio"], DEFAULT_TRACK_SCORES)).toBe(3.0);
    });

    it("all three tracks sum to 3.5", () => {
      expect(calculateTrackScore(["video", "audio", "subtitle"])).toBe(3.5);
    });

    it("ignores unknown track types", () => {
      expect(calculateTrackScore(["video", "metadata"])).toBe(2.0);
    });

    it("accepts custom track scores", () => {
      expect(calculateTrackScore(["video"], { video: 5, audio: 1, subtitle: 0.5 })).toBe(5);
    });
  });

  // =========================================================================
  // calculateMaxScore
  // =========================================================================
  describe("calculateMaxScore", () => {
    it("delegates to calculateTrackScore", () => {
      expect(calculateMaxScore(["video", "audio"])).toBe(3.0);
    });
  });

  // =========================================================================
  // calculatePriorityScore
  // =========================================================================
  describe("calculatePriorityScore", () => {
    it("favors lower priority numbers", () => {
      expect(calculatePriorityScore(0, 10)).toBeCloseTo(1);
      expect(calculatePriorityScore(10, 10)).toBeCloseTo(0);
      expect(calculatePriorityScore(5, 10)).toBeCloseTo(0.5);
    });

    it("handles maxPriority=0 without division by zero", () => {
      expect(calculatePriorityScore(0, 0)).toBeCloseTo(1);
    });
  });

  // =========================================================================
  // calculateSourceScore
  // =========================================================================
  describe("calculateSourceScore", () => {
    it("favors earlier sources", () => {
      expect(calculateSourceScore(0, 3)).toBeCloseTo(1);
      expect(calculateSourceScore(1, 3)).toBeCloseTo(0.5);
      expect(calculateSourceScore(2, 3)).toBeCloseTo(0);
    });

    it("single source returns 1", () => {
      expect(calculateSourceScore(0, 1)).toBeCloseTo(1);
    });
  });

  // =========================================================================
  // calculateQualityScore
  // =========================================================================
  describe("calculateQualityScore", () => {
    it("returns 1.0 when no bandwidth info", () => {
      expect(calculateQualityScore()).toBe(1.0);
      expect(calculateQualityScore(undefined, 1000)).toBe(1.0);
      expect(calculateQualityScore(1000, undefined)).toBe(1.0);
    });

    it("perfect match returns 1.0", () => {
      expect(calculateQualityScore(1000, 1000)).toBeCloseTo(1.0);
    });

    it("half bandwidth returns ~0.5", () => {
      expect(calculateQualityScore(500, 1000)).toBeCloseTo(0.5);
    });

    it("double bandwidth still returns 0.5 (clamped ratio)", () => {
      expect(calculateQualityScore(2000, 1000)).toBeCloseTo(0.5);
    });
  });

  // =========================================================================
  // calculateProtocolPenalty
  // =========================================================================
  describe("calculateProtocolPenalty", () => {
    it("returns 0 for unknown protocols", () => {
      expect(calculateProtocolPenalty("html5/video/mp4")).toBe(0);
    });

    it("penalizes WebM heavily", () => {
      expect(calculateProtocolPenalty("html5/video/webm")).toBe(0.8);
    });

    it("penalizes MEWS", () => {
      expect(calculateProtocolPenalty("ws/video/mp4")).toBe(0.5);
    });

    it("penalizes WebCodecs raw transports more than MEWS", () => {
      expect(calculateProtocolPenalty("ws/video/raw")).toBe(0.65);
      expect(calculateProtocolPenalty("ws/video/h264")).toBe(0.65);
      expect(calculateProtocolPenalty("ws/video/raw")).toBeGreaterThan(
        calculateProtocolPenalty("ws/video/mp4")
      );
    });

    it("pattern-based: any webm gets 0.5", () => {
      expect(calculateProtocolPenalty("some/video/webm-variant")).toBe(0.5);
    });

    it("DASH and CMAF/LL-HLS carry no readiness penalty (stability is scored separately)", () => {
      expect(calculateProtocolPenalty("dash/video/mp4")).toBe(0);
      expect(calculateProtocolPenalty("dash/audio/mp4")).toBe(0);
      expect(calculateProtocolPenalty("html5/application/vnd.apple.mpegurl;version=7")).toBe(0);
      expect(calculateProtocolPenalty("ll-hls")).toBe(0);
      expect(calculateProtocolPenalty("cmaf")).toBe(0);
    });

    it("WebM-in-DASH still penalized via the webm pattern", () => {
      expect(calculateProtocolPenalty("dash/video/webm")).toBe(0.5);
    });
  });

  // =========================================================================
  // isProtocolBlacklisted
  // =========================================================================
  describe("isProtocolBlacklisted", () => {
    it("blacklists flash", () => {
      expect(isProtocolBlacklisted("flash/7")).toBe(true);
      expect(isProtocolBlacklisted("flash/10")).toBe(true);
    });

    it("blacklists silverlight", () => {
      expect(isProtocolBlacklisted("silverlight")).toBe(true);
    });

    it("blacklists server-side protocols", () => {
      expect(isProtocolBlacklisted("srt")).toBe(true);
      expect(isProtocolBlacklisted("rtsp")).toBe(true);
      expect(isProtocolBlacklisted("rtmp")).toBe(true);
    });

    it("allows HLS", () => {
      expect(isProtocolBlacklisted("html5/application/vnd.apple.mpegurl")).toBe(false);
    });

    it("allows WHEP", () => {
      expect(isProtocolBlacklisted("whep")).toBe(false);
    });
  });

  // =========================================================================
  // calculateReliabilityScore
  // =========================================================================
  describe("calculateReliabilityScore", () => {
    it("hlsjs and videojs are the top HLS reliability options", () => {
      expect(calculateReliabilityScore("hlsjs")).toBe(0.96);
      expect(calculateReliabilityScore("videojs")).toBe(0.95);
      expect(calculateReliabilityScore("webcodecs")).toBe(0.75);
    });

    it("dashjs has lowest reliability", () => {
      expect(calculateReliabilityScore("dashjs")).toBe(0.5);
    });

    it("unknown player gets 0.5 default", () => {
      expect(calculateReliabilityScore("nonexistent")).toBe(0.5);
    });
  });

  // =========================================================================
  // Per-protocol axes (latency / stability / boot / vod)
  // =========================================================================
  describe("calculateLatencyScore", () => {
    it("WHEP is lowest latency, plain HLS-TS highest", () => {
      expect(calculateLatencyScore("whep")).toBe(1.0);
      expect(calculateLatencyScore("html5/application/vnd.apple.mpegurl")).toBe(0.2);
      expect(calculateLatencyScore("whep")).toBeGreaterThan(
        calculateLatencyScore("html5/application/vnd.apple.mpegurl")
      );
    });

    it("orders CMAF < MP4 < DASH on latency (MP4 lower latency than DASH)", () => {
      const cmaf = calculateLatencyScore("html5/application/vnd.apple.mpegurl;version=7");
      const mp4 = calculateLatencyScore("html5/video/mp4");
      const dash = calculateLatencyScore("dash/video/mp4");
      expect(cmaf).toBeGreaterThan(mp4);
      expect(mp4).toBeGreaterThan(dash);
    });
  });

  describe("calculateStabilityScore", () => {
    it("HLS-TS most stable; DASH ≈ MP4 above CMAF; WebCodecs lowest", () => {
      expect(calculateStabilityScore("html5/application/vnd.apple.mpegurl")).toBe(1.0);
      expect(calculateStabilityScore("dash/video/mp4")).toBe(
        calculateStabilityScore("html5/video/mp4")
      );
      expect(calculateStabilityScore("dash/video/mp4")).toBeGreaterThan(
        calculateStabilityScore("html5/application/vnd.apple.mpegurl;version=7")
      );
      expect(calculateStabilityScore("ws/video/raw")).toBeLessThan(
        calculateStabilityScore("ws/video/mp4")
      );
    });

    it("CMAF less stable than plain HLS-TS even though both are hls.js", () => {
      expect(calculateStabilityScore("html5/application/vnd.apple.mpegurl;version=7")).toBeLessThan(
        calculateStabilityScore("html5/application/vnd.apple.mpegurl")
      );
    });
  });

  describe("calculateBootScore", () => {
    it("WebCodecs and HLS-TS boot fastest; WHEP cold-start is slow", () => {
      expect(calculateBootScore("ws/video/raw")).toBe(1.0);
      expect(calculateBootScore("html5/application/vnd.apple.mpegurl")).toBeGreaterThan(
        calculateBootScore("whep")
      );
    });

    it("MP4 boots slow live (read-ahead) but fast as a faststart VOD file", () => {
      expect(calculateBootScore("html5/video/mp4", { isLive: true })).toBe(0.4);
      expect(calculateBootScore("html5/video/mp4", { isLive: false })).toBe(0.85);
      expect(calculateBootScore("html5/video/mp4", { isLive: true })).toBeLessThan(
        calculateBootScore("html5/video/mp4", { isLive: false })
      );
    });
  });

  describe("calculateVodScore", () => {
    it("long VOD favors segmented HLS-TS > DASH > CMAF over progressive MP4", () => {
      const long = { durationMs: 3_600_000 };
      expect(calculateVodScore("html5/application/vnd.apple.mpegurl", long)).toBeGreaterThan(
        calculateVodScore("dash/video/mp4", long)
      );
      expect(calculateVodScore("dash/video/mp4", long)).toBeGreaterThan(
        calculateVodScore("html5/application/vnd.apple.mpegurl;version=7", long)
      );
      expect(
        calculateVodScore("html5/application/vnd.apple.mpegurl;version=7", long)
      ).toBeGreaterThan(calculateVodScore("html5/video/mp4", long));
    });

    it("short clip flips: progressive MP4 beats segmented", () => {
      const short = { durationMs: 120_000 };
      expect(calculateVodScore("html5/video/mp4", short)).toBeGreaterThan(
        calculateVodScore("html5/application/vnd.apple.mpegurl", short)
      );
      expect(calculateVodScore("html5/video/mp4", short)).toBe(0.95);
      expect(SHORT_CLIP_MS).toBeGreaterThan(120_000);
    });
  });

  // =========================================================================
  // calculateRoutingBonus
  // =========================================================================
  describe("calculateRoutingBonus", () => {
    it("first preferred player gets 0.15", () => {
      expect(calculateRoutingBonus("whep", "native")).toBe(0.15);
    });

    it("second preferred player gets ~0.10", () => {
      expect(calculateRoutingBonus("html5/application/vnd.apple.mpegurl", "hlsjs")).toBeCloseTo(
        0.1,
        5
      );
    });

    it("avoided player gets -0.1", () => {
      expect(calculateRoutingBonus("html5/application/vnd.apple.mpegurl", "native")).toBe(-0.1);
    });

    it("routes CMAF/LL-HLS to hls.js and avoids Video.js (its CMAF path is broken)", () => {
      expect(calculateRoutingBonus("html5/application/vnd.apple.mpegurl;version=7", "hlsjs")).toBe(
        0.15
      );
      expect(
        calculateRoutingBonus("html5/application/vnd.apple.mpegurl;version=7", "videojs")
      ).toBe(-0.1);
    });

    it("no routing rule returns 0", () => {
      expect(calculateRoutingBonus("unknown/format", "native")).toBe(0);
    });

    it("unmatched player for known protocol returns 0", () => {
      expect(calculateRoutingBonus("whep", "dashjs")).toBe(0);
    });
  });

  // =========================================================================
  // scorePlayer
  // =========================================================================
  describe("scorePlayer", () => {
    it("produces higher score for video+audio than audio only", () => {
      const va = scorePlayer(["video", "audio"], 0, 0);
      const a = scorePlayer(["audio"], 0, 0);
      expect(va.total).toBeGreaterThan(a.total);
    });

    it("includes breakdown when shortname/mimeType provided", () => {
      const s = scorePlayer(["video"], 0, 0, {
        playerShortname: "videojs",
        mimeType: "html5/application/vnd.apple.mpegurl",
        playbackMode: "low-latency",
      });
      expect(s.breakdown).toBeDefined();
      expect(s.breakdown!.reliabilityScore).toBe(0.95);
      expect(s.breakdown!.routingBonus).toBe(0.15);
    });

    it("trackTypes is empty array when boolean true passed", () => {
      const s = scorePlayer(true, 0, 0);
      expect(s.trackTypes).toEqual([]);
    });

    it("trackTypes reflects passed tracks", () => {
      const s = scorePlayer(["video", "audio"], 0, 0);
      expect(s.trackTypes).toEqual(["video", "audio"]);
    });

    it("protocol penalty reduces total score", () => {
      const clean = scorePlayer(["video"], 0, 0, { mimeType: "html5/video/mp4" });
      const penalized = scorePlayer(["video"], 0, 0, { mimeType: "html5/video/webm" });
      expect(clean.total).toBeGreaterThan(penalized.total);
    });

    it("ranks hls.js above Video.js for CMAF/LL-HLS", () => {
      const common = {
        maxPriority: 100,
        totalSources: 2,
        mimeType: "html5/application/vnd.apple.mpegurl;version=7",
        playbackMode: "auto" as const,
      };
      const hlsjs = scorePlayer(["video", "audio"], 3, 0, {
        ...common,
        playerShortname: "hlsjs",
      });
      const videojs = scorePlayer(["video", "audio"], 2, 0, {
        ...common,
        playerShortname: "videojs",
      });

      expect(hlsjs.total).toBeGreaterThan(videojs.total);
    });

    it("low-latency ranks WebCodecs below WHEP and MEWS for full A/V", () => {
      const common = {
        maxPriority: 100,
        totalSources: 4,
        playbackMode: "low-latency" as const,
      };
      const webcodecs = scorePlayer(["video", "audio"], 0, 0, {
        ...common,
        playerShortname: "webcodecs",
        mimeType: "ws/video/raw",
      });
      const whep = scorePlayer(["video", "audio"], 1, 1, {
        ...common,
        playerShortname: "native",
        mimeType: "whep",
      });
      const mews = scorePlayer(["video", "audio"], 2, 2, {
        ...common,
        playerShortname: "mews",
        mimeType: "ws/video/mp4",
      });

      expect(webcodecs.total).toBeLessThan(whep.total);
      expect(webcodecs.total).toBeLessThan(mews.total);
    });

    it("quality ranks WebCodecs below HLS for full A/V", () => {
      const common = {
        maxPriority: 100,
        totalSources: 2,
        playbackMode: "quality" as const,
      };
      const webcodecs = scorePlayer(["video", "audio"], 0, 0, {
        ...common,
        playerShortname: "webcodecs",
        mimeType: "ws/video/raw",
      });
      const hls = scorePlayer(["video", "audio"], 2, 1, {
        ...common,
        playerShortname: "videojs",
        mimeType: "html5/application/vnd.apple.mpegurl",
      });

      expect(webcodecs.total).toBeLessThan(hls.total);
    });

    it("quality ranks WebRTC below HLS for video-only streams", () => {
      const common = {
        maxPriority: 100,
        totalSources: 2,
        playbackMode: "quality" as const,
      };
      const webrtc = scorePlayer(["video"], 2, 0, {
        ...common,
        playerShortname: "mist-webrtc",
        mimeType: "mist/webrtc",
      });
      const hls = scorePlayer(["video"], 2, 1, {
        ...common,
        playerShortname: "videojs",
        mimeType: "html5/application/vnd.apple.mpegurl",
      });

      expect(webrtc.total).toBeLessThan(hls.total);
    });

    it("auto ranks WHEP below MP4 and HLS for full A/V", () => {
      const common = {
        maxPriority: 100,
        totalSources: 3,
        playbackMode: "auto" as const,
      };
      const mp4 = scorePlayer(["video", "audio"], 1, 0, {
        ...common,
        playerShortname: "native",
        mimeType: "html5/video/mp4",
      });
      const hls = scorePlayer(["video", "audio"], 2, 1, {
        ...common,
        playerShortname: "videojs",
        mimeType: "html5/application/vnd.apple.mpegurl",
      });
      const whep = scorePlayer(["video", "audio"], 3, 2, {
        ...common,
        playerShortname: "native",
        mimeType: "whep",
      });

      expect(whep.total).toBeLessThan(mp4.total);
      expect(whep.total).toBeLessThan(hls.total);
    });

    it("uses default weights when not specified", () => {
      const s = scorePlayer(["video", "audio"], 5, 1, { maxPriority: 10, totalSources: 3 });
      expect(s.total).toBeGreaterThan(0);
    });
  });

  // =========================================================================
  // Per-mode winners + invariants (the tuning contract)
  // =========================================================================
  describe("per-mode winners", () => {
    it("low-latency → WHEP", () => {
      expect(winner(scoreAllProtocols("low-latency", { isLive: true }))).toBe("whep");
    });

    it("balanced → CMAF", () => {
      expect(winner(scoreAllProtocols("balanced", { isLive: true }))).toBe("cmaf");
    });

    it("quality → plain HLS-TS", () => {
      expect(winner(scoreAllProtocols("quality", { isLive: true }))).toBe("hlsTs");
    });

    it("auto → plain HLS-TS", () => {
      expect(winner(scoreAllProtocols("auto", { isLive: true }))).toBe("hlsTs");
    });

    it("vod long content → plain HLS-TS", () => {
      expect(winner(scoreAllProtocols("vod", { isLive: false, durationMs: 3_600_000 }))).toBe(
        "hlsTs"
      );
    });

    it("vod short clip → progressive MP4", () => {
      expect(winner(scoreAllProtocols("vod", { isLive: false, durationMs: 120_000 }))).toBe("mp4");
    });
  });

  describe("scoring invariants", () => {
    it("not-ready protocols (MEWS, WebCodecs) never win a live mode", () => {
      for (const mode of ["low-latency", "balanced", "quality", "auto"] as const) {
        const scores = scoreAllProtocols(mode, { isLive: true });
        const w = winner(scores);
        expect(w).not.toBe("mews");
        expect(w).not.toBe("webcodecs");
      }
    });

    it("DASH never wins a live mode", () => {
      // DASH's order vs CMAF/MP4 legitimately shifts by mode (CMAF wins latency-weighted
      // modes; DASH's higher stability lifts it in stability-weighted quality), but with
      // LL-DASH off it is never the selected winner.
      for (const mode of ["low-latency", "balanced", "quality", "auto"] as const) {
        expect(winner(scoreAllProtocols(mode, { isLive: true }))).not.toBe("dash");
      }
    });

    it("CMAF via hls.js beats CMAF via videojs (routing avoid)", () => {
      const common = {
        maxPriority: 100,
        totalSources: 1,
        mimeType: "html5/application/vnd.apple.mpegurl;version=7",
        playbackMode: "balanced" as const,
        isLive: true,
      };
      const hlsjs = scorePlayer(["video", "audio"], 0, 0, { ...common, playerShortname: "hlsjs" });
      const videojs = scorePlayer(["video", "audio"], 0, 0, {
        ...common,
        playerShortname: "videojs",
      });
      expect(hlsjs.total).toBeGreaterThan(videojs.total);
    });
  });

  // =========================================================================
  // compareScores
  // =========================================================================
  describe("compareScores", () => {
    it("sorts higher scores first (negative = a wins)", () => {
      const high = { base: 3, trackTypes: ["video"], total: 2.5 };
      const low = { base: 1, trackTypes: ["audio"], total: 0.5 };
      expect(compareScores(high, low)).toBeLessThan(0);
    });

    it("equal scores return 0", () => {
      const a = { base: 1, trackTypes: [], total: 1.0 };
      const b = { base: 1, trackTypes: [], total: 1.0 };
      expect(compareScores(a, b)).toBe(0);
    });
  });

  // =========================================================================
  // scoreAndRankPlayers
  // =========================================================================
  describe("scoreAndRankPlayers", () => {
    it("returns players sorted by score descending", () => {
      const players = [
        {
          player: { name: "audio-only", priority: 5 },
          supportedTracks: ["audio"] as string[],
          sourceIndex: 0,
        },
        {
          player: { name: "full", priority: 0 },
          supportedTracks: ["video", "audio"] as string[],
          sourceIndex: 0,
        },
        {
          player: { name: "sub-only", priority: 10 },
          supportedTracks: ["subtitle"] as string[],
          sourceIndex: 0,
        },
      ];

      const ranked = scoreAndRankPlayers(players);
      expect(ranked[0].player.name).toBe("full");
      expect(ranked[ranked.length - 1].player.name).toBe("sub-only");
    });

    it("empty array returns empty", () => {
      expect(scoreAndRankPlayers([])).toEqual([]);
    });
  });

  // =========================================================================
  // meetsMinimumScore
  // =========================================================================
  describe("meetsMinimumScore", () => {
    it("passes with no requirements", () => {
      const score = { base: 0, trackTypes: [], total: 0 };
      expect(meetsMinimumScore(score, {})).toBe(true);
    });

    it("fails minTotal check", () => {
      const score = { base: 1, trackTypes: ["video"], total: 0.5 };
      expect(meetsMinimumScore(score, { minTotal: 1.0 })).toBe(false);
    });

    it("passes minTotal check", () => {
      const score = { base: 2, trackTypes: ["video"], total: 1.5 };
      expect(meetsMinimumScore(score, { minTotal: 1.0 })).toBe(true);
    });

    it("fails requireVideo when no video track", () => {
      const score = { base: 1, trackTypes: ["audio"], total: 1.0 };
      expect(meetsMinimumScore(score, { requireVideo: true })).toBe(false);
    });

    it("passes requireVideo when video present", () => {
      const score = { base: 2, trackTypes: ["video", "audio"], total: 2.0 };
      expect(meetsMinimumScore(score, { requireVideo: true })).toBe(true);
    });

    it("fails requireAudio when no audio track", () => {
      const score = { base: 2, trackTypes: ["video"], total: 2.0 };
      expect(meetsMinimumScore(score, { requireAudio: true })).toBe(false);
    });

    it("passes requireAudio when audio present", () => {
      const score = { base: 1, trackTypes: ["audio"], total: 1.0 };
      expect(meetsMinimumScore(score, { requireAudio: true })).toBe(true);
    });

    it("fails minTrackTypes", () => {
      const score = { base: 2, trackTypes: ["video"], total: 2.0 };
      expect(meetsMinimumScore(score, { minTrackTypes: 2 })).toBe(false);
    });

    it("passes minTrackTypes", () => {
      const score = { base: 3, trackTypes: ["video", "audio"], total: 3.0 };
      expect(meetsMinimumScore(score, { minTrackTypes: 2 })).toBe(true);
    });
  });
});
