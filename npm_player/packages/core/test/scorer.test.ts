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
  calculateModeBonus,
  calculateRoutingBonus,
  isProtocolBlacklisted,
  scorePlayer,
  compareScores,
  scoreAndRankPlayers,
  meetsMinimumScore,
  PROTOCOL_BLACKLIST,
  PROTOCOL_PENALTIES,
  PLAYER_RELIABILITY,
  MODE_PROTOCOL_BONUSES,
  PROTOCOL_ROUTING,
} from "../src/core/scorer";

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

    it("pattern-based: any webm gets 0.5", () => {
      expect(calculateProtocolPenalty("some/video/webm-variant")).toBe(0.5);
    });

    it("pattern-based: dash/ prefix gets 0.4", () => {
      expect(calculateProtocolPenalty("dash/audio/mp4")).toBe(0.4);
    });

    it("pattern-based: cmaf gets 0.2", () => {
      expect(calculateProtocolPenalty("cmaf-stuff")).toBe(0.2);
    });

    it("DASH video/mp4 is highest penalty (0.9)", () => {
      expect(calculateProtocolPenalty("dash/video/mp4")).toBe(0.9);
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
    it("webcodecs and videojs are highest reliability", () => {
      expect(calculateReliabilityScore("webcodecs")).toBe(0.95);
      expect(calculateReliabilityScore("videojs")).toBe(0.95);
    });

    it("dashjs has lowest reliability", () => {
      expect(calculateReliabilityScore("dashjs")).toBe(0.5);
    });

    it("unknown player gets 0.5 default", () => {
      expect(calculateReliabilityScore("nonexistent")).toBe(0.5);
    });
  });

  // =========================================================================
  // calculateModeBonus
  // =========================================================================
  describe("calculateModeBonus", () => {
    it("low-latency favors WebCodecs raw highest", () => {
      expect(calculateModeBonus("ws/video/raw", "low-latency")).toBe(0.55);
    });

    it("low-latency: WHEP > HLS", () => {
      expect(calculateModeBonus("whep", "low-latency")).toBeGreaterThan(
        calculateModeBonus("html5/application/vnd.apple.mpegurl", "low-latency")
      );
    });

    it("vod penalizes WHEP with -1.0", () => {
      expect(calculateModeBonus("whep", "vod")).toBe(-1.0);
    });

    it("vod favors MP4", () => {
      expect(calculateModeBonus("html5/video/mp4", "vod")).toBe(0.5);
    });

    it("returns 0 for unknown protocol in any mode", () => {
      expect(calculateModeBonus("unknown/format", "auto")).toBe(0);
    });

    it("returns 0 for falsy mode", () => {
      expect(calculateModeBonus("whep", undefined as any)).toBe(0);
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

    it("uses default weights when not specified", () => {
      const s = scorePlayer(["video", "audio"], 5, 1, { maxPriority: 10, totalSources: 3 });
      expect(s.total).toBeGreaterThan(0);
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
