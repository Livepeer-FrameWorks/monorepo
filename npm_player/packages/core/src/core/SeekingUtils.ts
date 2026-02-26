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

import type { MistStreamInfo } from "../types";

// ============================================================================
// Types
// ============================================================================

export type LatencyTier = "ultra-low" | "low" | "medium" | "high";

export interface LiveThresholds {
  /** Milliseconds behind live edge to exit "LIVE" state (become clickable) */
  exitLive: number;
  /** Milliseconds behind live edge to enter "LIVE" state (become non-clickable) */
  enterLive: number;
}

export interface SeekableRange {
  /** Start of seekable range in milliseconds */
  seekableStart: number;
  /** End of seekable range (live edge) in milliseconds */
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
  /** Absolute timestamp correction in ms (from player's firstms-based proxy) */
  timeCorrectionMs?: number;
  /** Buffered range start in ms, in player coordinate space (for buffer window fallback) */
  bufferedStartMs?: number;
}

export interface CanSeekParams {
  video: HTMLVideoElement | null;
  isLive: boolean;
  duration: number;
  bufferWindowMs?: number;
  playerCanSeek?: () => boolean;
  /** Player-reported seekable range — if valid, seeking is supported regardless of bufferWindowMs */
  playerSeekableRange?: { start: number; end: number } | null;
}

// ============================================================================
// Constants
// ============================================================================

/**
 * Latency tier thresholds for "near live" detection.
 * Different protocols have vastly different latency expectations.
 *
 * exitLive: How far behind (ms) before we show "behind live" indicator
 * enterLive: How close to live (ms) before we show "LIVE" badge again
 *
 * The gap between exitLive and enterLive creates hysteresis to prevent flicker.
 */
export const LATENCY_TIERS: Record<LatencyTier, LiveThresholds> = {
  // WebRTC/WHEP: sub-second latency
  "ultra-low": { exitLive: 2000, enterLive: 500 },
  // MEWS (WebSocket MP4): 2-5s latency
  low: { exitLive: 5000, enterLive: 1500 },
  // HLS/DASH: 10-30s latency (segment-based)
  medium: { exitLive: 15000, enterLive: 5000 },
  // Fallback for unknown protocols
  high: { exitLive: 30000, enterLive: 10000 },
};

/**
 * Playback speed presets for UI controls.
 */
export const SPEED_PRESETS = [0.5, 1, 1.5, 2] as const;

/**
 * Default fallback buffer window when no other info available (in ms).
 * Aligned with MistServer reference player's 60-second default.
 */
export const DEFAULT_BUFFER_WINDOW_MS = 60000;

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
 * MediaStream sources require data channel signaling for seeking
 * (not native browser seeking) and don't support playback rate changes
 * via the HTML5 video element API.
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
  const { isLive, video, mistStreamInfo, currentTime, duration } = params;

  // video.seekable is authoritative — the browser/HLS.js/VHS already parsed the manifest.
  // Covers live sliding window, DVR, and VOD without needing separate computation.
  if (video?.seekable && video.seekable.length > 0) {
    const seekStart = video.seekable.start(0) * 1000;
    const seekEnd = video.seekable.end(video.seekable.length - 1) * 1000;
    if (seekEnd > seekStart) {
      return { seekableStart: seekStart, liveEdge: seekEnd };
    }
  }

  if (!isLive) {
    return { seekableStart: 0, liveEdge: duration };
  }

  // Fallback for live without seekable ranges (progressive, WebRTC, pre-playback)
  const liveEdge = Number.isFinite(duration) ? duration : currentTime;

  // Upstream skins.js getBufferWindow():
  // 1. MistVideo.info.meta.buffer_window
  // 2. (api.duration - api.buffered.start(0)) * 1e3
  // 3. 60e3
  let bufferWindowMs: number | undefined = mistStreamInfo?.meta?.buffer_window;

  if (!bufferWindowMs || bufferWindowMs <= 0) {
    // Upstream: (api.duration - api.buffered.start(0)) * 1e3
    // Both api.duration and api.buffered include the lastms shift, so lastms cancels.
    // Use shifted buffered start when available (from player.getBufferedRanges()) to
    // keep both values in the same coordinate space.
    let bufferedStartSec: number | undefined;
    if (params.bufferedStartMs !== undefined) {
      bufferedStartSec = params.bufferedStartMs / 1000;
    } else if (video?.buffered && video.buffered.length > 0) {
      bufferedStartSec = video.buffered.start(0);
    }
    if (bufferedStartSec !== undefined) {
      const liveEdgeSec = liveEdge / 1000;
      const windowSec = liveEdgeSec - bufferedStartSec;
      if (Number.isFinite(windowSec) && windowSec > 0) {
        bufferWindowMs = windowSec * 1000;
      }
    }
  }

  if (!bufferWindowMs || bufferWindowMs <= 0) {
    bufferWindowMs = DEFAULT_BUFFER_WINDOW_MS;
  }

  // Upstream: range = [duration - bufferWindow, duration]
  if (liveEdge > 0) {
    return { seekableStart: Math.max(0, liveEdge - bufferWindowMs), liveEdge };
  }

  // At startup (currentTime === 0), use buffer_window from metadata if available
  // so DVR seekability is visible before playback starts
  if (bufferWindowMs > 0 && mistStreamInfo?.meta?.buffer_window) {
    return { seekableStart: 0, liveEdge: bufferWindowMs };
  }

  return { seekableStart: currentTime, liveEdge: currentTime };
}

/**
 * Determine if seeking is supported for the current stream.
 *
 * @param params - Check parameters
 * @returns true if seeking is available
 */
export function canSeekStream(params: CanSeekParams): boolean {
  const { video, isLive, duration, bufferWindowMs, playerCanSeek, playerSeekableRange } = params;

  // Player reports a valid seekable range — trust it
  if (playerSeekableRange && playerSeekableRange.end > playerSeekableRange.start) {
    return true;
  }

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

  // For medium/high tiers, scale thresholds based on buffer_window (all in ms)
  if ((tier === "medium" || tier === "high") && bufferWindowMs && bufferWindowMs > 0) {
    return {
      exitLive: Math.max(tierThresholds.exitLive, Math.min(30000, bufferWindowMs / 3)),
      enterLive: Math.max(tierThresholds.enterLive, Math.min(10000, bufferWindowMs / 10)),
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
 * @param currentTimeMs - Current playback position in milliseconds
 * @param liveEdgeMs - Live edge position in milliseconds
 * @param thresholds - Enter/exit thresholds in milliseconds
 * @param currentState - Current isNearLive state
 * @returns New isNearLive state
 */
export function calculateIsNearLive(
  currentTimeMs: number,
  liveEdgeMs: number,
  thresholds: LiveThresholds,
  currentState: boolean
): boolean {
  // Invalid state - assume live
  if (!Number.isFinite(liveEdgeMs) || liveEdgeMs <= 0) {
    return true;
  }

  const behindMs = liveEdgeMs - currentTimeMs;

  // Hysteresis margins for extra stability (in ms)
  const exitMargin = 500;
  const enterMargin = 200;

  if (currentState && behindMs > thresholds.exitLive + exitMargin) {
    // Currently "LIVE" - switch to "behind" when significantly behind
    return false;
  } else if (!currentState && behindMs < thresholds.enterLive - enterMargin) {
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
