/**
 * Player Scoring System
 * Enhanced from MistMetaPlayer v3.1.0 with reliability and mode-aware scoring
 *
 * Implements the scoring algorithm for player selection with:
 * - Track type scoring
 * - Player priority scoring
 * - Source order scoring
 * - Player reliability scoring (new)
 * - Playback mode bonuses (new)
 * - Protocol-specific routing (new)
 */

import type { PlaybackMode } from "../types";

export interface TrackScore {
  video: number;
  audio: number;
  subtitle: number;
}

export interface PlayerScore {
  base: number;
  trackTypes: string[];
  total: number;
  // Extended score breakdown for debugging
  breakdown?: {
    trackScore: number;
    priorityScore: number;
    sourceScore: number;
    reliabilityScore: number;
    latencyScore: number;
    bootScore: number;
    stabilityScore: number;
    vodScore: number;
    routingBonus: number;
    protocolPenalty: number;
  };
}

/**
 * Default track type scores
 */
export const DEFAULT_TRACK_SCORES: TrackScore = {
  video: 2.0,
  audio: 1.0,
  subtitle: 0.5,
};

/**
 * Calculate score for supported track types
 */
export function calculateTrackScore(
  supportedTracks: string[] | boolean,
  scores: TrackScore = DEFAULT_TRACK_SCORES
): number {
  if (supportedTracks === true) {
    // Player supports something but doesn't specify what
    return 1.9; // Slightly less than perfect video score
  }

  if (supportedTracks === false || supportedTracks.length === 0) {
    return 0;
  }

  let totalScore = 0;
  for (const trackType of supportedTracks) {
    const score = scores[trackType as keyof TrackScore];
    if (score !== undefined) {
      totalScore += score;
    }
  }

  return totalScore;
}

/**
 * Calculate maximum possible score for a stream
 */
export function calculateMaxScore(
  availableTracks: string[],
  scores: TrackScore = DEFAULT_TRACK_SCORES
): number {
  return calculateTrackScore(availableTracks, scores);
}

/**
 * Player priority scoring
 */
export function calculatePriorityScore(priority: number, maxPriority: number): number {
  // Lower priority number = higher score
  // Normalize to 0-1 range, then invert
  return 1 - priority / Math.max(maxPriority, 1);
}

/**
 * Source preference scoring based on MistServer ordering
 */
export function calculateSourceScore(sourceIndex: number, totalSources: number): number {
  // Earlier sources (lower index) get higher scores
  return 1 - sourceIndex / Math.max(totalSources - 1, 1);
}

/**
 * Bandwidth/quality scoring
 */
export function calculateQualityScore(bandwidth?: number, targetBandwidth?: number): number {
  if (!bandwidth || !targetBandwidth) {
    return 1.0; // Neutral score if no bandwidth info
  }

  // Score based on how close to target bandwidth
  const ratio = Math.min(bandwidth, targetBandwidth) / Math.max(bandwidth, targetBandwidth);
  return ratio;
}

/**
 * Protocol blacklist - completely excluded from selection
 * These protocols are truly broken and should never be considered.
 *
 * NOTE: Protocols that ARE supported by reference implementation but unreliable
 * should be in PROTOCOL_PENALTIES instead (heavily penalized but still selectable).
 */
export const PROTOCOL_BLACKLIST: Set<string> = new Set([
  // Flash - browsers removed support in 2020
  "flash/7",
  "flash/10",
  "flash/11",
  // Silverlight - dead technology
  "silverlight",
  // SDP is WebRTC signaling format, not a playback source type
  "html5/application/sdp",
  "sdp",
  // MPEG-TS - no browser support, no wrapper implementation in reference
  "html5/video/mpeg",
  // Smooth Streaming - intentionally disabled in reference (commented out in dashjs.js!)
  "html5/application/vnd.ms-sstr+xml",
  // Server-side only protocols - browsers can't connect to these directly
  "srt",
  "rtsp",
  "rtmp",
  // MistServer internal protocols - not for browser playback
  "dtsc",
  // NOTE: ws/video/raw is supported by WebCodecs player
  // Image formats - not video playback
  "html5/image/jpeg",
  // Script/metadata formats - not playback sources
  "html5/text/javascript",
  // Subtitle-only formats - not standalone playback sources (used as tracks)
  "html5/text/vtt",
  "html5/text/plain",
]);

/**
 * Check if a protocol is blacklisted
 */
export function isProtocolBlacklisted(mimeType: string): boolean {
  return PROTOCOL_BLACKLIST.has(mimeType);
}

/**
 * Readiness penalties: protocols that are reference-supported but not yet
 * production-proven get subtracted from the total so they can be force-selected
 * for dev but never win automatic selection. This is the "not ready" gate; it is
 * distinct from stability, which is scored per-protocol in PROTOCOL_STABILITY_SCORE.
 */
export const PROTOCOL_PENALTIES: Record<string, number> = {
  // WebCodecs raw-frame path owns its own decode/timing pipeline and is not yet
  // production-ready; selectable explicitly but not as an automatic winner.
  "ws/video/raw": 0.65,
  "wss/video/raw": 0.65,
  "ws/video/h264": 0.65,
  "wss/video/h264": 0.65,
  // WebM - reference supports but unreliable in practice
  "html5/video/webm": 0.8, // Heavy penalty - very broken
  "html5/audio/webm": 0.6,
  "ws/video/webm": 0.8,
  "wss/video/webm": 0.8,
  // MEWS - not production-ready; prefer HLS/WebRTC for now
  "ws/video/mp4": 0.5,
  "wss/video/mp4": 0.5,
  // Native Mist WebRTC signaling - treat like MEWS (legacy/less stable than WHEP)
  webrtc: 0.5,
  "mist/webrtc": 0.5,
};

/**
 * Calculate protocol penalty based on known problematic protocols
 */
export function calculateProtocolPenalty(mimeType: string): number {
  // Direct match. Use `in` so configured penalties are authoritative even when
  // a value is falsy.
  if (mimeType in PROTOCOL_PENALTIES) {
    return PROTOCOL_PENALTIES[mimeType];
  }
  // Pattern-based penalties for protocols not explicitly listed
  const lowerMime = mimeType.toLowerCase();
  if (lowerMime.includes("webm")) {
    return 0.5; // Heavy penalty for any WebM variant (also catches dash/video/webm)
  }
  return 0;
}

/**
 * Player reliability scores — a small per-player tiebreaker (weight ~0.05).
 * Transport stability is scored per-protocol in PROTOCOL_STABILITY_SCORE; this
 * map only separates players serving the same protocol (e.g. hls.js vs videojs).
 */
export const PLAYER_RELIABILITY: Record<string, number> = {
  webcodecs: 0.75, // Low-level decoder path; keep selectable but behind browser transport stacks.
  videojs: 0.95, // Fast loading, built-in HLS via VHS
  hlsjs: 0.96, // Battle-tested HLS/LL-HLS path; slower to load, but stable on CMAF.
  native: 0.85, // Native is lightweight but has edge cases
  "mist-webrtc": 0.85, // Full signaling features
  mews: 0.75, // Custom protocol, less tested
  "mist-legacy": 0.7, // Ultimate fallback, delegates everything
  dashjs: 0.5, // Broken, lowest reliability
};

/**
 * Calculate player reliability score
 */
export function calculateReliabilityScore(playerShortname: string): number {
  return PLAYER_RELIABILITY[playerShortname] ?? 0.5;
}

/**
 * Per-protocol score axes (0–1, higher = better).
 *
 * Playback selection is driven by four independent per-protocol qualities —
 * latency, transport stability, boot/time-to-first-frame, and VOD-suitability —
 * combined per mode via MODE_WEIGHTS. ABR and seeking are NOT axes here: in this
 * player they are provided for every protocol by the Mist control/metadata
 * integration, so they do not differentiate protocols. Stability is per-protocol
 * (not per-player) because hls.js serves both plain HLS-TS and CMAF/LL-HLS yet
 * they differ sharply in robustness.
 */

/** Live glass-to-glass latency (higher score = lower latency). */
export const PROTOCOL_LATENCY_SCORE: Record<string, number> = {
  whep: 1.0,
  webrtc: 1.0,
  "mist/webrtc": 1.0,
  "ws/video/raw": 0.9,
  "wss/video/raw": 0.9,
  "ws/video/h264": 0.9,
  "wss/video/h264": 0.9,
  "ws/video/mp4": 0.8,
  "wss/video/mp4": 0.8,
  "html5/application/vnd.apple.mpegurl;version=7": 0.65,
  "ll-hls": 0.65,
  cmaf: 0.65,
  // LL-DASH sits behind LL-HLS in practice, but still tracks the live edge closely.
  "dash/video/mp4": 0.55,
  // FrameWorks live MP4 is a read-ahead stream that tracks the edge, unlike
  // generic progressive MP4, which has no live edge at all.
  "html5/video/mp4": 0.5,
  // Plain HLS-TS: highest latency (buffer depth × segment length).
  "html5/application/vnd.apple.mpegurl": 0.2,
};

/** Transport stability / robustness (higher score = more stable). */
export const PROTOCOL_STABILITY_SCORE: Record<string, number> = {
  "html5/application/vnd.apple.mpegurl": 1.0, // hls.js: most battle-tested recovery path
  "dash/video/mp4": 0.75,
  "html5/video/mp4": 0.75,
  // CMAF/LL-HLS rides hls.js but the low-latency code path is the fragile part.
  "html5/application/vnd.apple.mpegurl;version=7": 0.55,
  "ll-hls": 0.55,
  cmaf: 0.55,
  "ws/video/mp4": 0.4,
  "wss/video/mp4": 0.4,
  whep: 0.3,
  webrtc: 0.3,
  "mist/webrtc": 0.3,
  "ws/video/raw": 0.2,
  "wss/video/raw": 0.2,
  "ws/video/h264": 0.2,
  "wss/video/h264": 0.2,
};

/**
 * Boot / time-to-first-frame for protocols whose value is fixed (higher = faster).
 * html5/video/mp4 is context-dependent and resolved in calculateBootScore: a live
 * read-ahead MP4 boots slowly, a faststart VOD file boots fast.
 */
export const PROTOCOL_BOOT_SCORE: Record<string, number> = {
  "ws/video/raw": 1.0,
  "wss/video/raw": 1.0,
  "ws/video/h264": 1.0,
  "wss/video/h264": 1.0,
  "html5/application/vnd.apple.mpegurl": 0.95,
  "ws/video/mp4": 0.9,
  "wss/video/mp4": 0.9,
  "dash/video/mp4": 0.85,
  "html5/application/vnd.apple.mpegurl;version=7": 0.85,
  "ll-hls": 0.85,
  cmaf: 0.85,
  // WHEP/WebRTC: cold-start pays ICE/DTLS/SDP before any media.
  whep: 0.45,
  webrtc: 0.45,
  "mist/webrtc": 0.45,
};

/** MP4 boot is contextual: live read-ahead is slow, faststart VOD file is fast. */
const MP4_BOOT_LIVE = 0.4;
const MP4_BOOT_VOD = 0.85;

/**
 * VOD-suitability for long-form VOD (higher = better). Segmented adaptive
 * protocols win on ABR + CDN cacheability + robust startup. Only consulted in
 * `vod` mode. For short clips PROTOCOL_VOD_SCORE_SHORT applies instead.
 */
export const PROTOCOL_VOD_SCORE: Record<string, number> = {
  "html5/application/vnd.apple.mpegurl": 1.0,
  "dash/video/mp4": 0.95,
  "html5/application/vnd.apple.mpegurl;version=7": 0.9,
  "ll-hls": 0.9,
  cmaf: 0.9,
  "html5/video/mp4": 0.5, // no ABR; weak for long content
  "ws/video/mp4": 0.3,
  "wss/video/mp4": 0.3,
  whep: 0.2,
  webrtc: 0.2,
  "mist/webrtc": 0.2,
  "ws/video/raw": 0.2,
  "wss/video/raw": 0.2,
  "ws/video/h264": 0.2,
  "wss/video/h264": 0.2,
};

/**
 * VOD-suitability for short clips (≤ SHORT_CLIP_MS). Segmentation overhead is not
 * worth it for a 2-minute clip, so progressive MP4's simplicity/snappy scrubbing wins.
 */
export const PROTOCOL_VOD_SCORE_SHORT: Record<string, number> = {
  "html5/video/mp4": 0.95,
  "html5/application/vnd.apple.mpegurl": 0.6,
  "dash/video/mp4": 0.55,
  "html5/application/vnd.apple.mpegurl;version=7": 0.55,
  "ll-hls": 0.55,
  cmaf: 0.55,
  "ws/video/mp4": 0.3,
  "wss/video/mp4": 0.3,
  whep: 0.2,
  webrtc: 0.2,
  "mist/webrtc": 0.2,
  "ws/video/raw": 0.2,
  "wss/video/raw": 0.2,
  "ws/video/h264": 0.2,
  "wss/video/h264": 0.2,
};

/** Clips at or below this duration are scored as short content. */
export const SHORT_CLIP_MS = 180_000;

/** Stream context that makes some per-protocol scores content-dependent. */
export interface ScoreContext {
  isLive?: boolean;
  durationMs?: number;
}

export function calculateLatencyScore(mimeType: string): number {
  if (mimeType in PROTOCOL_LATENCY_SCORE) return PROTOCOL_LATENCY_SCORE[mimeType];
  const m = mimeType.toLowerCase();
  if (m.includes("cmaf") || m.includes("ll-hls")) return 0.65;
  if (m.startsWith("dash/")) return 0.55;
  return 0.5;
}

export function calculateStabilityScore(mimeType: string): number {
  if (mimeType in PROTOCOL_STABILITY_SCORE) return PROTOCOL_STABILITY_SCORE[mimeType];
  const m = mimeType.toLowerCase();
  if (m.includes("cmaf") || m.includes("ll-hls")) return 0.55;
  if (m.startsWith("dash/")) return 0.75;
  return 0.5;
}

export function calculateBootScore(mimeType: string, ctx: ScoreContext = {}): number {
  if (mimeType === "html5/video/mp4") {
    return ctx.isLive ? MP4_BOOT_LIVE : MP4_BOOT_VOD;
  }
  if (mimeType in PROTOCOL_BOOT_SCORE) return PROTOCOL_BOOT_SCORE[mimeType];
  const m = mimeType.toLowerCase();
  if (m.includes("cmaf") || m.includes("ll-hls")) return 0.85;
  if (m.startsWith("dash/")) return 0.85;
  return 0.6;
}

export function calculateVodScore(mimeType: string, ctx: ScoreContext = {}): number {
  const isShort =
    ctx.durationMs !== undefined && ctx.durationMs > 0 && ctx.durationMs <= SHORT_CLIP_MS;
  const table = isShort ? PROTOCOL_VOD_SCORE_SHORT : PROTOCOL_VOD_SCORE;
  if (mimeType in table) return table[mimeType];
  const m = mimeType.toLowerCase();
  if (m.includes("cmaf") || m.includes("ll-hls")) return isShort ? 0.55 : 0.9;
  if (m.startsWith("dash/")) return isShort ? 0.55 : 0.95;
  return isShort ? 0.4 : 0.4;
}

/**
 * Per-mode weighting of the four protocol axes. Each mode emphasises a different
 * blend; the ordering tests in scorer.test.ts are the contract these numbers serve.
 * Track compatibility (trackScore, 0.5 weight), readiness penalty (1.0 weight) and
 * routing (0.08) are applied on top of these and are constant across modes.
 */
export interface ModeWeights {
  latency: number;
  boot: number;
  stability: number;
  vod: number;
}

export const MODE_WEIGHTS: Record<PlaybackMode, ModeWeights> = {
  // Latency dominates; readiness penalty keeps not-ready MEWS/WebCodecs out → WHEP wins.
  "low-latency": { latency: 0.3, boot: 0.1, stability: 0.08, vod: 0.0 },
  // Low-ish latency + quick boot + decent stability → CMAF wins (HLS-TS close behind).
  balanced: { latency: 0.2, boot: 0.1, stability: 0.14, vod: 0.04 },
  // Stability dominates → plain HLS-TS (hls.js) wins.
  quality: { latency: 0.05, boot: 0.08, stability: 0.25, vod: 0.1 },
  // VOD-suitability dominates; duration flips MP4 vs segmented (long → HLS-TS, clip → MP4).
  vod: { latency: 0.0, boot: 0.12, stability: 0.16, vod: 0.28 },
  // Fast boot + top stability → plain HLS-TS wins (live MP4 boots slow).
  auto: { latency: 0.1, boot: 0.18, stability: 0.16, vod: 0.06 },
};

/**
 * Protocol routing rules - certain players are preferred for certain protocols
 */
export const PROTOCOL_ROUTING: Record<string, { prefer: string[]; avoid?: string[] }> = {
  whep: { prefer: ["native"] },
  webrtc: { prefer: ["mist-webrtc", "native"] },
  "mist/webrtc": { prefer: ["mist-webrtc"] },

  // Raw WebSocket (12-byte header + AVCC NAL units) - WebCodecs only
  "ws/video/raw": { prefer: ["webcodecs"] },
  "wss/video/raw": { prefer: ["webcodecs"] },

  // Annex B WebSocket (H.264 NAL units) - WebCodecs
  "ws/video/h264": { prefer: ["webcodecs"] },
  "wss/video/h264": { prefer: ["webcodecs"] },

  // MP4-muxed WebSocket - MEWS (uses MSE for demuxing)
  "ws/video/mp4": { prefer: ["mews"] },
  "wss/video/mp4": { prefer: ["mews"] },
  "ws/video/webm": { prefer: ["mews"] },
  "wss/video/webm": { prefer: ["mews"] },

  // HLS
  "html5/application/vnd.apple.mpegurl": {
    prefer: ["videojs", "hlsjs"],
    avoid: ["native"],
  },
  // HLSv7 is CMAF/LL-HLS. hls.js has mature LL-HLS (lowLatencyMode, PART-HOLD-BACK,
  // liveSyncPosition) and holds the live edge; VHS's LL/CMAF path is broken today. hls.js only.
  "html5/application/vnd.apple.mpegurl;version=7": {
    prefer: ["hlsjs"],
    avoid: ["videojs", "native"],
  },

  // DASH
  "dash/video/mp4": { prefer: ["dashjs", "videojs"] },

  // Progressive download
  "html5/video/mp4": { prefer: ["native"] },
  "html5/video/webm": { prefer: ["native"] },

  // Audio-only formats
  "html5/audio/aac": { prefer: ["native"] },
  "html5/audio/mp3": { prefer: ["native"] },
  "html5/audio/flac": { prefer: ["native"] },
  "html5/audio/wav": { prefer: ["native"] },
};

/**
 * Calculate routing bonus based on protocol+player pairing
 */
export function calculateRoutingBonus(mimeType: string, playerShortname: string): number {
  const rules = PROTOCOL_ROUTING[mimeType];
  if (!rules) return 0;

  // Check if player is avoided
  if (rules.avoid?.includes(playerShortname)) {
    return -0.1; // Penalty for avoided players
  }

  // Check if player is preferred
  if (rules.prefer?.includes(playerShortname)) {
    const preferIndex = rules.prefer.indexOf(playerShortname);
    // First preferred player gets 0.15, second gets 0.10, etc.
    return 0.15 - preferIndex * 0.05;
  }

  return 0;
}

/**
 * Comprehensive player scoring with enhanced factors
 */
export function scorePlayer(
  supportedTracks: string[] | boolean,
  priority: number,
  sourceIndex: number,
  options: {
    maxPriority?: number;
    totalSources?: number;
    trackScores?: Partial<TrackScore>;
    bandwidth?: number;
    targetBandwidth?: number;
    // New options for enhanced scoring
    playerShortname?: string;
    mimeType?: string;
    playbackMode?: PlaybackMode;
    /** Stream context (from Mist JSON) — flips MP4 boot/VOD scores. */
    isLive?: boolean;
    durationMs?: number;
    weights?: {
      tracks: number;
      priority: number;
      source: number;
      quality: number;
      reliability?: number;
      routing?: number;
      protocolPenalty?: number;
    };
  } = {}
): PlayerScore {
  const {
    maxPriority = 10,
    totalSources = 1,
    trackScores = {},
    bandwidth,
    targetBandwidth,
    playerShortname,
    mimeType,
    playbackMode = "auto",
    isLive,
    durationMs,
    weights = {
      tracks: 0.5, // Track-type compatibility dominates; equal across full A/V combos.
      priority: 0.1, // Player priority order
      source: 0.05, // MistServer source order
      quality: 0.05, // Bandwidth match
      reliability: 0.05, // Per-player tiebreak only (stability is per-protocol below)
      routing: 0.08, // Protocol→player routing preference
      protocolPenalty: 1.0, // Readiness gate (subtracted)
    },
  } = options;

  const finalTrackScores = { ...DEFAULT_TRACK_SCORES, ...trackScores };
  const ctx: ScoreContext = { isLive, durationMs };

  // Component scores
  const trackScore = calculateTrackScore(supportedTracks, finalTrackScores);
  const priorityScore = calculatePriorityScore(priority, maxPriority);
  const sourceScore = calculateSourceScore(sourceIndex, totalSources);
  const qualityScore = calculateQualityScore(bandwidth, targetBandwidth);
  const reliabilityScore = playerShortname ? calculateReliabilityScore(playerShortname) : 0.5;
  const routingBonus =
    mimeType && playerShortname ? calculateRoutingBonus(mimeType, playerShortname) : 0;
  const protocolPenalty = mimeType ? calculateProtocolPenalty(mimeType) : 0;

  // Per-protocol axes, weighted by playback mode.
  const latencyScore = mimeType ? calculateLatencyScore(mimeType) : 0;
  const bootScore = mimeType ? calculateBootScore(mimeType, ctx) : 0;
  const stabilityScore = mimeType ? calculateStabilityScore(mimeType) : 0;
  const vodScore = mimeType ? calculateVodScore(mimeType, ctx) : 0;
  const modeWeights = MODE_WEIGHTS[playbackMode] ?? MODE_WEIGHTS.auto;

  // Weighted total score (readiness penalty is subtracted)
  const total =
    trackScore * weights.tracks +
    priorityScore * weights.priority +
    sourceScore * weights.source +
    qualityScore * weights.quality +
    reliabilityScore * (weights.reliability ?? 0) +
    routingBonus * (weights.routing ?? 0) +
    latencyScore * modeWeights.latency +
    bootScore * modeWeights.boot +
    stabilityScore * modeWeights.stability +
    vodScore * modeWeights.vod -
    protocolPenalty * (weights.protocolPenalty ?? 1.0);

  return {
    base: trackScore,
    trackTypes: Array.isArray(supportedTracks) ? supportedTracks : [],
    total,
    breakdown: {
      trackScore,
      priorityScore,
      sourceScore,
      reliabilityScore,
      latencyScore,
      bootScore,
      stabilityScore,
      vodScore,
      routingBonus,
      protocolPenalty,
    },
  };
}

/**
 * Compare two player scores
 */
export function compareScores(a: PlayerScore, b: PlayerScore): number {
  return b.total - a.total; // Higher scores first
}

/**
 * Batch score multiple players and return sorted by best score
 */
export function scoreAndRankPlayers<T extends { priority: number }>(
  players: Array<{
    player: T;
    supportedTracks: string[] | boolean;
    sourceIndex: number;
  }>,
  options?: Parameters<typeof scorePlayer>[3]
): Array<{
  player: T;
  score: PlayerScore;
}> {
  const scoredPlayers = players.map(({ player, supportedTracks, sourceIndex }) => ({
    player,
    score: scorePlayer(supportedTracks, player.priority, sourceIndex, options),
  }));

  return scoredPlayers.sort((a, b) => compareScores(a.score, b.score));
}

/**
 * Utility to check if a score meets minimum thresholds
 */
export function meetsMinimumScore(
  score: PlayerScore,
  requirements: {
    minTotal?: number;
    requireVideo?: boolean;
    requireAudio?: boolean;
    minTrackTypes?: number;
  }
): boolean {
  const {
    minTotal = 0,
    requireVideo = false,
    requireAudio = false,
    minTrackTypes = 0,
  } = requirements;

  // Check total score
  if (score.total < minTotal) {
    return false;
  }

  // Check track type requirements
  if (requireVideo && !score.trackTypes.includes("video")) {
    return false;
  }

  if (requireAudio && !score.trackTypes.includes("audio")) {
    return false;
  }

  if (score.trackTypes.length < minTrackTypes) {
    return false;
  }

  return true;
}
