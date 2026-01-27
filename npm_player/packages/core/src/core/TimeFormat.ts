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
  currentTime: number;
  duration: number;
  liveEdge: number;
  seekableStart: number;
  /** Unix timestamp (ms) at stream time 0 - for wall-clock display */
  unixoffset?: number;
}

// ============================================================================
// Pure Functions
// ============================================================================

/**
 * Format seconds as MM:SS or HH:MM:SS.
 *
 * @param seconds - Time in seconds
 * @returns Formatted time string, or "LIVE" for invalid input
 *
 * @example
 * formatTime(65)    // "01:05"
 * formatTime(3665)  // "1:01:05"
 * formatTime(-1)    // "LIVE"
 * formatTime(NaN)   // "LIVE"
 */
export function formatTime(seconds: number): string {
  if (!Number.isFinite(seconds) || seconds < 0) {
    return "LIVE";
  }

  const total = Math.floor(seconds);
  const hours = Math.floor(total / 3600);
  const minutes = Math.floor((total % 3600) / 60);
  const secs = total % 60;

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
      // currentTime is playback position in seconds
      const actualTimeMs = unixoffset + currentTime * 1000;
      const actualDate = new Date(actualTimeMs);
      return formatClockTime(actualDate);
    }

    // Fallback: show relative time if no unixoffset
    const seekableWindow = liveEdge - seekableStart;
    if (seekableWindow > 0) {
      const behindSeconds = liveEdge - currentTime;
      if (behindSeconds < 1) {
        return "LIVE";
      }
      return `-${formatTime(Math.abs(behindSeconds))}`;
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
 * @param time - Time position in seconds
 * @param isLive - Whether stream is live
 * @param liveEdge - Live edge position (for relative display)
 * @returns Formatted tooltip time
 */
export function formatTooltipTime(time: number, isLive: boolean, liveEdge?: number): string {
  if (isLive && liveEdge !== undefined && Number.isFinite(liveEdge)) {
    const behindSeconds = liveEdge - time;
    if (behindSeconds < 1) {
      return "LIVE";
    }
    return `-${formatTime(Math.abs(behindSeconds))}`;
  }

  return formatTime(time);
}

/**
 * Format duration for display (e.g., in stats panel).
 * Handles edge cases like infinite duration for live streams.
 *
 * @param duration - Duration in seconds
 * @param isLive - Whether content is live
 * @returns Formatted duration string
 */
export function formatDuration(duration: number, isLive?: boolean): string {
  if (isLive || !Number.isFinite(duration)) {
    return "LIVE";
  }

  return formatTime(duration);
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
