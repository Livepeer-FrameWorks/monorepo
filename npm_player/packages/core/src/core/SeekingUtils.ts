/**
 * SeekingUtils.ts
 *
 * Centralized seeking and live detection logic for player controls.
 * Used by React, Svelte, and Vanilla wrappers to ensure consistent behavior.
 *
 * Key concepts:
 * - Seekable range: The portion of the stream that can be seeked to
 * - Live edge: The furthest point in time that can be played (live point)
 * - Near live: Whether playback is close enough to live edge to show "LIVE" badge
 * - Latency tier: Protocol-based classification affecting live detection thresholds
 */

import type { MistStreamInfo, MistTrackInfo } from "../types";

// ============================================================================
// Types
// ============================================================================

export type LatencyTier = "ultra-low" | "low" | "medium" | "high";

export interface LiveThresholds {
  /** Seconds behind live edge to exit "LIVE" state (become clickable) */
  exitLive: number;
  /** Seconds behind live edge to enter "LIVE" state (become non-clickable) */
  enterLive: number;
}

export interface SeekableRange {
  /** Start of seekable range in seconds */
  seekableStart: number;
  /** End of seekable range (live edge) in seconds */
  liveEdge: number;
}

export interface SeekableRangeParams {
  isLive: boolean;
  video: HTMLVideoElement | null;
  mistStreamInfo?: MistStreamInfo;
  currentTime: number;
  duration: number;
  /** Allow Mist track metadata for MediaStream sources (e.g., WebCodecs DVR) */
  allowMediaStreamDvr?: boolean;
}

export interface CanSeekParams {
  video: HTMLVideoElement | null;
  isLive: boolean;
  duration: number;
  bufferWindowMs?: number;
  playerCanSeek?: () => boolean;
}

// ============================================================================
// Constants
// ============================================================================

/**
 * Latency tier thresholds for "near live" detection.
 * Different protocols have vastly different latency expectations.
 *
 * exitLive: How far behind (seconds) before we show "behind live" indicator
 * enterLive: How close to live (seconds) before we show "LIVE" badge again
 *
 * The gap between exitLive and enterLive creates hysteresis to prevent flicker.
 */
export const LATENCY_TIERS: Record<LatencyTier, LiveThresholds> = {
  // WebRTC/WHEP: sub-second latency
  "ultra-low": { exitLive: 2, enterLive: 0.5 },
  // MEWS (WebSocket MP4): 2-5s latency
  low: { exitLive: 5, enterLive: 1.5 },
  // HLS/DASH: 10-30s latency (segment-based)
  medium: { exitLive: 15, enterLive: 5 },
  // Fallback for unknown protocols
  high: { exitLive: 30, enterLive: 10 },
};

/**
 * Playback speed presets for UI controls.
 */
export const SPEED_PRESETS = [0.5, 1, 1.5, 2] as const;

/**
 * Default fallback buffer window when no other info available (in seconds).
 * Aligned with MistServer reference player's 60-second default.
 */
export const DEFAULT_BUFFER_WINDOW_SEC = 60;

// ============================================================================
// Pure Functions
// ============================================================================

/**
 * Detect latency tier from source type string.
 *
 * @param sourceType - MIME type or protocol identifier (e.g., 'whep', 'ws/video/mp4')
 * @returns Latency tier classification
 */
export function getLatencyTier(sourceType?: string): LatencyTier {
  if (!sourceType) return "medium";
  const t = sourceType.toLowerCase();

  // Ultra-low: WebRTC protocols (sub-second latency)
  if (t === "whep" || t === "webrtc" || t.includes("mist/webrtc")) {
    return "ultra-low";
  }

  // Low: WebSocket-based streaming (2-5s latency)
  if (t.startsWith("ws/") || t.startsWith("wss/")) {
    return "low";
  }

  // Medium: HLS/DASH (segment-based, 10-30s latency)
  if (t.includes("mpegurl") || t.includes("dash")) {
    return "medium";
  }

  // Progressive MP4/WebM - use medium defaults
  if (t.includes("video/mp4") || t.includes("video/webm")) {
    return "medium";
  }

  return "medium";
}

/**
 * Check if video element is using WebRTC/MediaStream source.
 * WebRTC streams have special constraints (no seeking, no playback rate changes).
 *
 * @param video - HTML video element
 * @returns true if source is a MediaStream
 */
export function isMediaStreamSource(video: HTMLVideoElement | null): boolean {
  if (!video) return false;
  return video.srcObject instanceof MediaStream;
}

/**
 * Check if playback rate adjustment is supported.
 * WebRTC/MediaStream sources don't support playback rate changes.
 *
 * @param video - HTML video element
 * @returns true if playback rate can be changed
 */
export function supportsPlaybackRate(video: HTMLVideoElement | null): boolean {
  if (!video) return true;
  return !isMediaStreamSource(video);
}

/**
 * Calculate seekable range for live or VOD streams.
 *
 * Priority order:
 * 1. Browser's video.seekable ranges (most accurate for MSE-based players)
 * 2. Track firstms/lastms from MistServer metadata
 * 3. buffer_window from MistServer signaling
 * 4. No fallback (treat as live-only when no reliable data)
 *
 * @param params - Calculation parameters
 * @returns Seekable range with start and live edge
 */
export function calculateSeekableRange(params: SeekableRangeParams): SeekableRange {
  const {
    isLive,
    video,
    mistStreamInfo,
    currentTime,
    duration,
    allowMediaStreamDvr = false,
  } = params;

  // VOD: full duration is seekable
  if (!isLive) {
    return { seekableStart: 0, liveEdge: duration };
  }

  const isMediaStream = isMediaStreamSource(video);

  // PRIORITY 1: Browser's video.seekable (most reliable - reflects actual browser state)
  if (video?.seekable && video.seekable.length > 0) {
    const start = video.seekable.start(0);
    const end = video.seekable.end(video.seekable.length - 1);
    if (Number.isFinite(start) && Number.isFinite(end) && end > start) {
      return { seekableStart: start, liveEdge: end };
    }
  }

  // PRIORITY 2: Track firstms/lastms from MistServer (accurate when available)
  // Skip for MediaStream unless explicitly allowed (e.g., WebCodecs DVR via server)
  if ((allowMediaStreamDvr || !isMediaStream) && mistStreamInfo?.meta?.tracks) {
    const tracks = Object.values(mistStreamInfo.meta.tracks) as MistTrackInfo[];
    if (tracks.length > 0) {
      const firstmsValues = tracks
        .map((t) => t.firstms)
        .filter((v): v is number => v !== undefined);
      const lastmsValues = tracks.map((t) => t.lastms).filter((v): v is number => v !== undefined);

      if (firstmsValues.length > 0 && lastmsValues.length > 0) {
        const firstms = Math.max(...firstmsValues);
        const lastms = Math.min(...lastmsValues);
        return { seekableStart: firstms / 1000, liveEdge: lastms / 1000 };
      }
    }
  }

  // PRIORITY 3: buffer_window from MistServer signaling
  const bufferWindowMs = mistStreamInfo?.meta?.buffer_window;
  if (bufferWindowMs && bufferWindowMs > 0 && currentTime > 0) {
    const bufferWindowSec = bufferWindowMs / 1000;
    return {
      seekableStart: Math.max(0, currentTime - bufferWindowSec),
      liveEdge: currentTime,
    };
  }

  // No seekable range (live only)
  return { seekableStart: currentTime, liveEdge: currentTime };
}

/**
 * Determine if seeking is supported for the current stream.
 *
 * @param params - Check parameters
 * @returns true if seeking is available
 */
export function canSeekStream(params: CanSeekParams): boolean {
  const { video, isLive, duration, bufferWindowMs, playerCanSeek } = params;

  // Player API says no
  if (playerCanSeek && !playerCanSeek()) {
    return false;
  }

  // Player API says yes - trust it for VOD, but require buffer for live
  if (playerCanSeek && playerCanSeek()) {
    if (!isLive) return true;
    return bufferWindowMs !== undefined && bufferWindowMs > 0;
  }

  // No video element
  if (!video) {
    return false;
  }

  // WebRTC/MediaStream: only if buffer_window explicitly configured
  if (isMediaStreamSource(video)) {
    return bufferWindowMs !== undefined && bufferWindowMs > 0;
  }

  // Browser reports seekable ranges
  if (video.seekable && video.seekable.length > 0) {
    return true;
  }

  // VOD with valid duration
  if (!isLive && Number.isFinite(duration) && duration > 0) {
    return true;
  }

  // Live with buffer_window configured
  if (isLive && bufferWindowMs !== undefined && bufferWindowMs > 0) {
    return true;
  }

  return false;
}

/**
 * Calculate live detection thresholds, optionally scaled by buffer_window.
 *
 * For medium/high latency tiers, scales thresholds based on the actual
 * buffer window to provide more appropriate "near live" detection.
 *
 * @param sourceType - Protocol/MIME type for tier detection
 * @param isWebRTC - Whether source is WebRTC (overrides tier to ultra-low)
 * @param bufferWindowMs - Optional buffer window in milliseconds
 * @returns Thresholds for entering/exiting "LIVE" state
 */
export function calculateLiveThresholds(
  sourceType?: string,
  isWebRTC?: boolean,
  bufferWindowMs?: number
): LiveThresholds {
  // Determine tier from source type, or use ultra-low for WebRTC
  const tier = sourceType ? getLatencyTier(sourceType) : isWebRTC ? "ultra-low" : "medium";
  const tierThresholds = LATENCY_TIERS[tier];

  // For medium/high tiers, scale thresholds based on buffer_window
  if ((tier === "medium" || tier === "high") && bufferWindowMs && bufferWindowMs > 0) {
    const bufferWindowSec = bufferWindowMs / 1000;
    // Scale thresholds proportionally to buffer, with reasonable bounds
    return {
      exitLive: Math.max(tierThresholds.exitLive, Math.min(30, bufferWindowSec / 3)),
      enterLive: Math.max(tierThresholds.enterLive, Math.min(10, bufferWindowSec / 10)),
    };
  }

  return tierThresholds;
}

/**
 * Calculate whether playback is "near live" using hysteresis.
 *
 * Hysteresis prevents flip-flopping when hovering near the threshold:
 * - To EXIT "LIVE" state: must be > exitLive + margin behind
 * - To ENTER "LIVE" state: must be < enterLive - margin behind
 *
 * @param currentTime - Current playback position in seconds
 * @param liveEdge - Live edge position in seconds
 * @param thresholds - Enter/exit thresholds
 * @param currentState - Current isNearLive state
 * @returns New isNearLive state
 */
export function calculateIsNearLive(
  currentTime: number,
  liveEdge: number,
  thresholds: LiveThresholds,
  currentState: boolean
): boolean {
  // Invalid state - assume live
  if (!Number.isFinite(liveEdge) || liveEdge <= 0) {
    return true;
  }

  const behindSeconds = liveEdge - currentTime;

  // Hysteresis margins for extra stability
  const exitMargin = 0.5;
  const enterMargin = 0.2;

  if (currentState && behindSeconds > thresholds.exitLive + exitMargin) {
    // Currently "LIVE" - switch to "behind" when significantly behind
    return false;
  } else if (!currentState && behindSeconds < thresholds.enterLive - enterMargin) {
    // Currently "behind" - switch to "LIVE" when close to live edge
    return true;
  }

  // No change
  return currentState;
}

/**
 * Determine if content is live based on available metadata.
 *
 * Priority:
 * 1. Explicit isContentLive flag (highest priority)
 * 2. MistServer stream type
 * 3. Duration check (non-finite = live)
 *
 * @param isContentLive - Explicit live flag from content metadata
 * @param mistStreamInfo - MistServer stream info
 * @param duration - Video duration
 * @returns true if content is live
 */
export function isLiveContent(
  isContentLive?: boolean,
  mistStreamInfo?: MistStreamInfo,
  duration?: number
): boolean {
  // Explicit flag wins
  if (isContentLive !== undefined) {
    return isContentLive;
  }

  // MistServer type
  if (mistStreamInfo?.type) {
    return mistStreamInfo.type === "live";
  }

  // Fallback: non-finite duration indicates live
  return !Number.isFinite(duration);
}
