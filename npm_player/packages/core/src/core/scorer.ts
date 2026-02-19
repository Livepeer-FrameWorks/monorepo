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
    modeBonus: number;
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
 * Protocol penalties for problematic protocols (not blacklisted, but deprioritized)
 * These get subtracted from the total score to deprioritize certain protocols.
 *
 * Heavy penalties (0.50+): Reference-supported but consistently unreliable
 * Medium penalties (0.20): Experimental or spotty support
 */
export const PROTOCOL_PENALTIES: Record<string, number> = {
  // WebCodecs raw - NOW PREFERRED for low-latency (no penalty)
  // 'ws/video/raw': 0,
  // 'wss/video/raw': 0,
  // 'ws/video/h264': 0,
  // 'wss/video/h264': 0,
  // WebM - reference supports but unreliable in practice
  "html5/video/webm": 0.8, // Heavy penalty - very broken
  "html5/audio/webm": 0.6,
  "ws/video/webm": 0.8,
  "wss/video/webm": 0.8,
  // MEWS - heavy penalty, prefer HLS/WebRTC (reference mews.js has issues)
  "ws/video/mp4": 0.5,
  "wss/video/mp4": 0.5,
  // Native Mist WebRTC signaling - treat like MEWS (legacy/less stable than WHEP)
  webrtc: 0.5,
  "mist/webrtc": 0.5,
  // DASH - heavy penalty, broken implementation
  "dash/video/mp4": 0.9, // Below legacy
  "dash/video/webm": 0.95,
  // CMAF-style protocols (fMP4 over HLS/DASH) - fragmentation issues
  "html5/application/vnd.apple.mpegurl;version=7": 0.2, // HLSv7 is CMAF-based
  // LL-HLS specific - experimental, spotty support
  "ll-hls": 0.2,
  cmaf: 0.2,
};

/**
 * Calculate protocol penalty based on known problematic protocols
 */
export function calculateProtocolPenalty(mimeType: string): number {
  // Direct match
  if (PROTOCOL_PENALTIES[mimeType]) {
    return PROTOCOL_PENALTIES[mimeType];
  }
  // Pattern-based penalties for protocols not explicitly listed
  const lowerMime = mimeType.toLowerCase();
  if (lowerMime.includes("webm")) {
    return 0.5; // Heavy penalty for any WebM variant
  }
  if (lowerMime.startsWith("dash/")) {
    return 0.4; // DASH penalty
  }
  if (lowerMime.includes("cmaf") || lowerMime.includes("ll-hls")) {
    return 0.2;
  }
  return 0;
}

/**
 * Player reliability scores
 * Based on library maturity, error recovery, and overall stability
 */
export const PLAYER_RELIABILITY: Record<string, number> = {
  webcodecs: 0.95, // Stable, lowest latency option
  videojs: 0.95, // Fast loading, built-in HLS via VHS
  hlsjs: 0.9, // Battle-tested but slower to load
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
 * Protocol bonuses for each playback mode
 *
 * IMPORTANT: Track compatibility is enforced by trackScore (50% weight).
 * Mode bonuses (12% weight) only affect ordering among compatible options.
 * A protocol missing required tracks will never be selected regardless of mode bonus.
 *
 * Priority rationale:
 * - Low-latency: WHEP/WebRTC first (<1s), then MP4/WS (2-5s), HLS last (10-30s)
 * - Quality: MP4/WS first (stable + low latency), HLS fallback, WHEP minimal
 * - VOD: MP4/HLS first (seekable), WHEP hard penalty (no seek support)
 * - Auto: MP4/WS balanced choice, WHEP for low latency, HLS last resort
 */
export const MODE_PROTOCOL_BONUSES: Record<PlaybackMode, Record<string, number>> = {
  "low-latency": {
    // WebCodecs raw/h264: HIGHEST PRIORITY - ultra-low latency via WebCodecs API
    "ws/video/raw": 0.55,
    "wss/video/raw": 0.55,
    "ws/video/h264": 0.52,
    "wss/video/h264": 0.52,
    // WHEP/WebRTC: sub-second latency
    whep: 0.5,
    webrtc: 0.25,
    "mist/webrtc": 0.25,
    // MP4/WS (MEWS): 2-5s latency, good fallback
    "ws/video/mp4": 0.3,
    "wss/video/mp4": 0.3,
    // Progressive MP4: lower latency than HLS (2-5s vs 10-30s)
    "html5/video/mp4": 0.35,
    // HLS: high latency, minimal bonus
    "html5/application/vnd.apple.mpegurl": 0.05,
  },
  quality: {
    // MP4/WS: stable + lower latency than HLS, preferred when supported
    "ws/video/mp4": 0.45,
    "wss/video/mp4": 0.45,
    // WebCodecs raw: below MEWS but above HLS - good quality + low latency
    "ws/video/raw": 0.4,
    "wss/video/raw": 0.4,
    "ws/video/h264": 0.38,
    "wss/video/h264": 0.38,
    // HLS: ABR support, universal fallback
    "html5/application/vnd.apple.mpegurl": 0.3,
    "html5/video/mp4": 0.2,
    // WebRTC: minimal for quality mode
    whep: 0.05,
    webrtc: 0.05,
    "mist/webrtc": 0.05,
  },
  vod: {
    // VOD/Clip: Prefer seekable protocols, EXCLUDE WebRTC (no seek support)
    "html5/video/mp4": 0.5, // Progressive MP4 - best for clips
    "html5/application/vnd.apple.mpegurl": 0.45, // HLS - ABR support
    "dash/video/mp4": 0.4, // DASH - ABR support
    "ws/video/mp4": 0.35, // MEWS - seekable via MSE
    "wss/video/mp4": 0.35,
    // WHEP/WebRTC: HARD PENALTY - no seek support, inappropriate for VOD
    whep: -1.0,
    webrtc: -1.0,
    "mist/webrtc": -1.0,
  },
  auto: {
    // WebCodecs raw: highest priority for low-latency live streams
    "ws/video/raw": 0.5,
    "wss/video/raw": 0.5,
    "ws/video/h264": 0.48,
    "wss/video/h264": 0.48,
    // Direct MP4: simple, reliable, preferred over HLS when available
    "html5/video/mp4": 0.42,
    // WHEP/WebRTC: good for low latency
    whep: 0.38,
    webrtc: 0.2,
    "mist/webrtc": 0.2,
    // MP4/WS (MEWS): lower latency than HLS
    "ws/video/mp4": 0.3,
    "wss/video/mp4": 0.3,
    // HLS: high latency, fallback option (but reliable)
    "html5/application/vnd.apple.mpegurl": 0.2,
  },
};

/**
 * Calculate mode-specific bonus for a protocol
 */
export function calculateModeBonus(mimeType: string, mode: PlaybackMode): number {
  if (!mode) return 0;
  return MODE_PROTOCOL_BONUSES[mode]?.[mimeType] ?? 0;
}

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
  "html5/application/vnd.apple.mpegurl;version=7": {
    prefer: ["videojs", "hlsjs"],
    avoid: ["native"],
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
    weights?: {
      tracks: number;
      priority: number;
      source: number;
      quality: number;
      reliability?: number;
      mode?: number;
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
    weights = {
      tracks: 0.5, // Reduced from 0.70 to make room for new factors
      priority: 0.1, // Reduced from 0.15
      source: 0.05, // Reduced from 0.10
      quality: 0.05, // Unchanged
      reliability: 0.1, // NEW: Player stability
      mode: 0.1, // Playback mode bonus (reduced slightly)
      routing: 0.08, // Protocol routing preference
      protocolPenalty: 1.0, // Protocol penalty weight (applied as subtraction)
    },
  } = options;

  const finalTrackScores = { ...DEFAULT_TRACK_SCORES, ...trackScores };

  // Individual component scores
  const trackScore = calculateTrackScore(supportedTracks, finalTrackScores);
  const priorityScore = calculatePriorityScore(priority, maxPriority);
  const sourceScore = calculateSourceScore(sourceIndex, totalSources);
  const qualityScore = calculateQualityScore(bandwidth, targetBandwidth);

  // New enhanced scores
  const reliabilityScore = playerShortname ? calculateReliabilityScore(playerShortname) : 0.5;
  const modeBonus = mimeType ? calculateModeBonus(mimeType, playbackMode) : 0;
  const routingBonus =
    mimeType && playerShortname ? calculateRoutingBonus(mimeType, playerShortname) : 0;
  const protocolPenalty = mimeType ? calculateProtocolPenalty(mimeType) : 0;

  // Weighted total score (penalty is subtracted)
  const total =
    trackScore * weights.tracks +
    priorityScore * weights.priority +
    sourceScore * weights.source +
    qualityScore * weights.quality +
    reliabilityScore * (weights.reliability ?? 0) +
    modeBonus * (weights.mode ?? 0) +
    routingBonus * (weights.routing ?? 0) -
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
      modeBonus,
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
