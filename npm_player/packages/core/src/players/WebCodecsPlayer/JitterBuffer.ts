/**
 * JitterBuffer - Network Jitter Estimation
 *
 * Tracks network jitter to inform buffer sizing decisions.
 * Ported from legacy rawws.js JitterTracker with improvements:
 * - Per-track jitter tracking (audio/video can differ)
 * - TypeScript types
 * - Better edge case handling
 *
 * Algorithm:
 * 1. Track arrival time vs media time for last N chunks
 * 2. Calculate jitter = (mediaTimePassed / speed) - clockTimePassed
 * 3. Maintain sliding window of peak jitter per second
 * 4. Weighted average: (avgPeak + maxPeak * 2) / 3 + 1ms
 * 5. Limit lowering rate to prevent oscillation
 */

import type { JitterState } from "./types";

/** Default sliding window size for chunk tracking */
const DEFAULT_CHUNK_WINDOW = 8;

/** Default sliding window size for peak tracking */
const DEFAULT_PEAK_WINDOW = 8;

/** Interval between peak calculations (ms) */
const PEAK_INTERVAL_MS = 1000;

/** Maximum jitter decrease per interval (ms) */
const MAX_JITTER_DECREASE = 500;

/** Initial jitter estimate (ms) */
const INITIAL_JITTER = 120;

/** Minimum jitter floor (ms) */
const MIN_JITTER = 1;

interface ChunkTiming {
  /** Wall clock time when chunk arrived (performance.now()) */
  receiveTime: number;
  /** Media timestamp from chunk (ms) */
  mediaTime: number;
}

export interface JitterTrackerOptions {
  /** Initial jitter estimate (ms) */
  initialJitter?: number;
  /** Sliding window size for chunks */
  chunkWindowSize?: number;
  /** Sliding window size for peaks */
  peakWindowSize?: number;
}

/**
 * JitterTracker - Estimates network jitter for a single track
 */
export class JitterTracker {
  /** Sliding window of chunk timings */
  private chunks: ChunkTiming[] = [];

  /** Current playback speed (1 = realtime) */
  private speed = 1;

  /** Last time a peak was recorded */
  private lastPeakTime = 0;

  /** Maximum jitter observed in current interval */
  private currentPeak = 0;

  /** Sliding window of peak jitter values */
  private peaks: number[] = [];

  /** Weighted average jitter estimate */
  private maxJitter: number;

  /** Configuration */
  private readonly chunkWindowSize: number;
  private readonly peakWindowSize: number;

  constructor(options: JitterTrackerOptions = {}) {
    this.maxJitter = options.initialJitter ?? INITIAL_JITTER;
    this.chunkWindowSize = options.chunkWindowSize ?? DEFAULT_CHUNK_WINDOW;
    this.peakWindowSize = options.peakWindowSize ?? DEFAULT_PEAK_WINDOW;
  }

  /**
   * Add a received chunk to jitter calculation
   *
   * @param mediaTime - Media timestamp from chunk (ms)
   * @param receiveTime - Wall clock time (performance.now())
   */
  addChunk(mediaTime: number, receiveTime: number = performance.now()): void {
    // Add to sliding window
    this.chunks.push({ receiveTime, mediaTime });
    if (this.chunks.length > this.chunkWindowSize) {
      this.chunks.shift();
    }

    // Calculate instantaneous jitter
    const jitter = this.calculateJitter();
    if (jitter > this.currentPeak) {
      this.currentPeak = jitter;
    }

    // Update peaks every second
    const now = performance.now();
    if (now > this.lastPeakTime + PEAK_INTERVAL_MS) {
      this.recordPeak();
      this.lastPeakTime = now;
    }
  }

  /**
   * Calculate current instantaneous jitter
   */
  private calculateJitter(): number {
    if (this.chunks.length <= 1) {
      return 0;
    }

    // Skip calculation during fast-forward
    if (this.speed === 0 || !isFinite(this.speed)) {
      return 0;
    }

    const oldest = this.chunks[0];
    const newest = this.chunks[this.chunks.length - 1];

    // Time passed on wall clock
    const clockTimePassed = newest.receiveTime - oldest.receiveTime;

    // Time that should have passed based on media timestamps
    const mediaTimePassed = newest.mediaTime - oldest.mediaTime;

    // Jitter = expected - actual
    // Positive jitter means chunks arriving faster than expected (buffering)
    // Negative jitter means chunks arriving slower than expected (starving)
    const jitter = mediaTimePassed / this.speed - clockTimePassed;

    return Math.max(0, jitter);
  }

  /**
   * Record current peak and update weighted average
   */
  private recordPeak(): void {
    // Add current peak to sliding window
    this.peaks.push(this.currentPeak);
    if (this.peaks.length > this.peakWindowSize) {
      this.peaks.shift();
    }

    // Reset for next interval
    this.currentPeak = 0;

    // Calculate new weighted average
    if (this.peaks.length > 0) {
      const maxPeak = Math.max(...this.peaks);
      const avgPeak = this.peaks.reduce((sum, p) => sum + p, 0) / this.peaks.length;

      // Weighted: emphasize max peak for safety
      let weighted = (avgPeak + maxPeak * 2) / 3 + MIN_JITTER;

      // Limit rate of decrease to prevent oscillation
      if (this.maxJitter > weighted + MAX_JITTER_DECREASE) {
        weighted = this.maxJitter - MAX_JITTER_DECREASE;
      }

      // Smooth transition
      this.maxJitter = (this.maxJitter + weighted) / 2;
    }
  }

  /**
   * Get current jitter estimate (ms)
   */
  get(): number {
    return this.maxJitter;
  }

  /**
   * Get detailed jitter state
   */
  getState(): JitterState {
    return {
      current: this.calculateJitter(),
      peak: this.currentPeak,
      weighted: this.maxJitter,
    };
  }

  /**
   * Set playback speed for jitter calculation
   */
  setSpeed(speed: number | "auto"): void {
    const newSpeed = speed === "auto" ? 1 : speed;
    if (newSpeed !== this.speed) {
      this.speed = newSpeed;
      this.reset();
    }
  }

  /**
   * Reset jitter tracking (e.g., after seek)
   */
  reset(): void {
    this.chunks = [];
    this.currentPeak = 0;
    // Don't reset maxJitter - keep the learned estimate
  }

  /**
   * Full reset including learned jitter estimate
   */
  fullReset(): void {
    this.reset();
    this.peaks = [];
    this.maxJitter = INITIAL_JITTER;
    this.lastPeakTime = 0;
  }
}

/**
 * MultiTrackJitterTracker - Manages jitter tracking for multiple tracks
 */
export class MultiTrackJitterTracker {
  private trackers = new Map<number, JitterTracker>();
  private globalSpeed = 1;
  private options: JitterTrackerOptions;

  constructor(options: JitterTrackerOptions = {}) {
    this.options = options;
  }

  /**
   * Add a chunk for a specific track
   */
  addChunk(trackIndex: number, mediaTime: number, receiveTime?: number): void {
    let tracker = this.trackers.get(trackIndex);
    if (!tracker) {
      tracker = new JitterTracker(this.options);
      tracker.setSpeed(this.globalSpeed);
      this.trackers.set(trackIndex, tracker);
    }
    tracker.addChunk(mediaTime, receiveTime);
  }

  /**
   * Get maximum jitter across all tracks
   */
  getMax(): number {
    let max = 0;
    for (const tracker of this.trackers.values()) {
      max = Math.max(max, tracker.get());
    }
    return max;
  }

  /**
   * Get jitter for a specific track
   */
  getForTrack(trackIndex: number): number {
    return this.trackers.get(trackIndex)?.get() ?? INITIAL_JITTER;
  }

  /**
   * Set playback speed for all trackers
   */
  setSpeed(speed: number | "auto"): void {
    this.globalSpeed = speed === "auto" ? 1 : speed;
    for (const tracker of this.trackers.values()) {
      tracker.setSpeed(speed);
    }
  }

  /**
   * Reset all trackers
   */
  reset(): void {
    for (const tracker of this.trackers.values()) {
      tracker.reset();
    }
  }

  /**
   * Remove a track's tracker
   */
  removeTrack(trackIndex: number): void {
    this.trackers.delete(trackIndex);
  }

  /**
   * Clear all trackers
   */
  clear(): void {
    this.trackers.clear();
  }
}
