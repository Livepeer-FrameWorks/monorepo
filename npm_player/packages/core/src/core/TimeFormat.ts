/**
 * TimeFormat.ts
 *
 * Time formatting utilities for player controls.
 * Used by React, Svelte, and Vanilla wrappers.
 */

// ============================================================================
// Types
// ============================================================================

export interface TimeDisplayParams {
  isLive: boolean;
  /** Current playback position in milliseconds */
  currentTime: number;
  /** Content duration in milliseconds */
  duration: number;
  /** Live edge position in milliseconds */
  liveEdge: number;
  /** Start of seekable range in milliseconds */
  seekableStart: number;
  /** Unix timestamp (ms) at stream time 0 - for wall-clock display */
  unixoffset?: number;
}

// ============================================================================
// Pure Functions
// ============================================================================

/**
 * Format milliseconds as MM:SS or HH:MM:SS.
 *
 * @param ms - Time in milliseconds
 * @returns Formatted time string, or "LIVE" for invalid input
 *
 * @example
 * formatTime(65000)    // "01:05"
 * formatTime(3665000)  // "1:01:05"
 * formatTime(-1)       // "LIVE"
 * formatTime(NaN)      // "LIVE"
 */
export function formatTime(ms: number): string {
  if (!Number.isFinite(ms) || ms < 0) {
    return "LIVE";
  }

  const totalSeconds = Math.floor(ms / 1000);
  const hours = Math.floor(totalSeconds / 3600);
  const minutes = Math.floor((totalSeconds % 3600) / 60);
  const secs = totalSeconds % 60;

  if (hours > 0) {
    return `${hours}:${String(minutes).padStart(2, "0")}:${String(secs).padStart(2, "0")}`;
  }

  return `${String(minutes).padStart(2, "0")}:${String(secs).padStart(2, "0")}`;
}

/**
 * Format a Date as wall-clock time (HH:MM:SS).
 *
 * @param date - Date object
 * @returns Formatted time string in HH:MM:SS format
 *
 * @example
 * formatClockTime(new Date('2024-01-15T14:30:45'))  // "14:30:45"
 */
export function formatClockTime(date: Date): string {
  const hours = String(date.getHours()).padStart(2, "0");
  const minutes = String(date.getMinutes()).padStart(2, "0");
  const seconds = String(date.getSeconds()).padStart(2, "0");
  return `${hours}:${minutes}:${seconds}`;
}

/**
 * Format time display for player controls.
 *
 * For live streams:
 * - With unixoffset: Shows actual wall-clock time (HH:MM:SS)
 * - With seekable window: Shows time behind live (-MM:SS) or "LIVE"
 * - Fallback: Shows elapsed time
 *
 * For VOD:
 * - Shows "current / duration" (MM:SS / MM:SS)
 *
 * @param params - Display parameters
 * @returns Formatted time display string
 *
 * @example
 * // Live with unixoffset
 * formatTimeDisplay({ isLive: true, currentTime: 60, unixoffset: 1705330245000, ... })
 * // "14:30:45"
 *
 * // Live behind
 * formatTimeDisplay({ isLive: true, currentTime: 50, liveEdge: 60, ... })
 * // "-00:10"
 *
 * // VOD
 * formatTimeDisplay({ isLive: false, currentTime: 65, duration: 300, ... })
 * // "01:05 / 05:00"
 */
export function formatTimeDisplay(params: TimeDisplayParams): string {
  const { isLive, currentTime, duration, liveEdge, seekableStart, unixoffset } = params;

  if (isLive) {
    // For live: show actual wall-clock time using unixoffset
    if (unixoffset && unixoffset > 0) {
      // unixoffset is Unix timestamp in ms at timestamp 0 of the stream
      // currentTime is playback position in ms
      const actualTimeMs = unixoffset + currentTime;
      const actualDate = new Date(actualTimeMs);
      return formatClockTime(actualDate);
    }

    // Fallback: show relative time if no unixoffset
    const seekableWindow = liveEdge - seekableStart;
    if (seekableWindow > 0) {
      const behindMs = liveEdge - currentTime;
      if (behindMs < 1000) {
        return "LIVE";
      }
      return `-${formatTime(Math.abs(behindMs))}`;
    }

    // No DVR window: show LIVE instead of a misleading timestamp
    return "LIVE";
  }

  // VOD: show current / total
  if (Number.isFinite(duration) && duration > 0) {
    return `${formatTime(currentTime)} / ${formatTime(duration)}`;
  }

  return formatTime(currentTime);
}

/**
 * Format time for seek bar tooltip.
 * For live streams, can show time relative to live edge.
 *
 * @param timeMs - Time position in milliseconds
 * @param isLive - Whether stream is live
 * @param liveEdgeMs - Live edge position in ms (for relative display)
 * @returns Formatted tooltip time
 */
export function formatTooltipTime(timeMs: number, isLive: boolean, liveEdgeMs?: number): string {
  if (isLive && liveEdgeMs !== undefined && Number.isFinite(liveEdgeMs)) {
    const behindMs = liveEdgeMs - timeMs;
    if (behindMs < 1000) {
      return "LIVE";
    }
    return `-${formatTime(Math.abs(behindMs))}`;
  }

  return formatTime(timeMs);
}

/**
 * Format duration for display (e.g., in stats panel).
 * Handles edge cases like infinite duration for live streams.
 *
 * @param durationMs - Duration in milliseconds
 * @param isLive - Whether content is live
 * @returns Formatted duration string
 */
export function formatDuration(durationMs: number, isLive?: boolean): string {
  if (isLive || !Number.isFinite(durationMs)) {
    return "LIVE";
  }

  return formatTime(durationMs);
}

/**
 * Parse time string (HH:MM:SS or MM:SS) to seconds.
 *
 * @param timeStr - Time string to parse
 * @returns Time in seconds, or NaN if invalid
 *
 * @example
 * parseTime("01:30")     // 90
 * parseTime("1:30:45")   // 5445
 * parseTime("invalid")   // NaN
 */
export function parseTime(timeStr: string): number {
  const parts = timeStr.split(":").map(Number);

  if (parts.some(isNaN)) {
    return NaN;
  }

  if (parts.length === 2) {
    // MM:SS
    const [minutes, seconds] = parts;
    return minutes * 60 + seconds;
  }

  if (parts.length === 3) {
    // HH:MM:SS
    const [hours, minutes, seconds] = parts;
    return hours * 3600 + minutes * 60 + seconds;
  }

  return NaN;
}

/**
 * Format a quality label from resolution and bitrate.
 * Examples: "1920x1080 8.0 Mbps", "800x600 2.5 Mbps", "1200 kbps"
 */
export function formatQualityLabel(width?: number, height?: number, bitrate?: number): string {
  const res = width && height ? `${width}x${height}` : "";
  if (!bitrate) return res || "Unknown";
  const bps =
    bitrate >= 1_000_000
      ? `${(bitrate / 1_000_000).toFixed(1)} Mbps`
      : `${Math.round(bitrate / 1000)} kbps`;
  return res ? `${res} ${bps}` : bps;
}
