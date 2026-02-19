import type { PlaybackQuality, QualityThresholds } from "../types";
import { TimerManager } from "./TimerManager";

/**
 * Default quality thresholds
 */
const DEFAULT_THRESHOLDS: QualityThresholds = {
  minScore: 60,
  maxStalls: 3,
  minBuffer: 2,
};

/**
 * Rolling average window size
 */
const ROLLING_WINDOW_SIZE = 20;

/**
 * Playback score history entry (for MistPlayer-style 0-2.0 score)
 */
interface PlaybackScoreEntry {
  clock: number; // Wall clock time in seconds
  video: number; // Video currentTime
  score: number; // Calculated score for this sample
}

/** Protocol type for threshold selection */
export type PlayerProtocol = "webrtc" | "hls" | "dash" | "html5" | "unknown";

/** Protocol-specific playback score thresholds (MistMetaPlayer reference) */
export const PROTOCOL_THRESHOLDS: Record<PlayerProtocol, number> = {
  webrtc: 0.95, // Very strict for low-latency
  hls: 0.75, // More lenient for adaptive streaming
  dash: 0.75, // More lenient for adaptive streaming
  html5: 0.75, // Standard threshold
  unknown: 0.75, // Default
};

export interface QualityMonitorOptions {
  /** Sample interval in ms */
  sampleInterval?: number;
  /** Quality thresholds */
  thresholds?: Partial<QualityThresholds>;
  /** Callback when quality degrades */
  onQualityDegraded?: (quality: PlaybackQuality) => void;
  /** Callback on every sample */
  onSample?: (quality: PlaybackQuality) => void;
  /** Current player protocol for threshold selection */
  protocol?: PlayerProtocol;
  /** Custom playback score threshold (overrides protocol default) */
  playbackScoreThreshold?: number;
  /**
   * Callback when sustained poor quality triggers a fallback request
   * Reference: player.js:654-665 - "nextCombo" action
   */
  onFallbackRequest?: (reason: { score: number; consecutivePoorSamples: number }) => void;
  /**
   * Number of consecutive poor samples before requesting fallback
   * Default: 5 (2.5 seconds at 500ms sample interval)
   */
  poorSamplesBeforeFallback?: number;
}

export interface QualityMonitorState {
  isMonitoring: boolean;
  quality: PlaybackQuality | null;
  history: PlaybackQuality[];
}

/**
 * QualityMonitor - Tracks playback quality metrics
 *
 * Monitors:
 * - Buffer health (seconds ahead)
 * - Stall count (waiting events)
 * - Frame drop rate (via video.getVideoPlaybackQuality())
 * - Estimated bitrate
 * - Latency (for live streams)
 *
 * Calculates a composite quality score (0-100) and triggers
 * callbacks when quality degrades below thresholds.
 */
export class QualityMonitor {
  private videoElement: HTMLVideoElement | null = null;
  private options: Required<Omit<QualityMonitorOptions, "protocol" | "playbackScoreThreshold">> & {
    protocol: PlayerProtocol;
    playbackScoreThreshold: number | null;
  };
  private thresholds: QualityThresholds;
  private timers = new TimerManager();
  private stallCount = 0;
  private lastStallTime = 0;
  private totalStallMs = 0;
  private history: PlaybackQuality[] = [];
  private lastBytesLoaded = 0;
  private lastBytesTime = 0;
  private listeners: Array<() => void> = [];

  // MistPlayer-style playback score (0-2.0 scale)
  private playbackScoreHistory: PlaybackScoreEntry[] = [];
  private playbackScore = 1.0;
  private readonly PLAYBACK_SCORE_AVERAGING_STEPS = 10;

  // Automatic fallback tracking
  // Reference: player.js:654-665 - triggers "nextCombo" after sustained poor quality
  private consecutivePoorSamples = 0;
  private fallbackTriggered = false;

  constructor(options: QualityMonitorOptions = {}) {
    this.options = {
      sampleInterval: options.sampleInterval ?? 500,
      thresholds: options.thresholds ?? {},
      onQualityDegraded: options.onQualityDegraded ?? (() => {}),
      onSample: options.onSample ?? (() => {}),
      protocol: options.protocol ?? "unknown",
      playbackScoreThreshold: options.playbackScoreThreshold ?? null,
      onFallbackRequest: options.onFallbackRequest ?? (() => {}),
      poorSamplesBeforeFallback: options.poorSamplesBeforeFallback ?? 5,
    };
    this.thresholds = { ...DEFAULT_THRESHOLDS, ...options.thresholds };
  }

  /**
   * Set the current player protocol for threshold selection
   */
  setProtocol(protocol: PlayerProtocol): void {
    this.options.protocol = protocol;
  }

  /**
   * Get the current player protocol
   */
  getProtocol(): PlayerProtocol {
    return this.options.protocol;
  }

  /**
   * Get the playback score threshold for the current protocol
   */
  getPlaybackScoreThreshold(): number {
    // Custom threshold takes precedence
    if (this.options.playbackScoreThreshold !== null) {
      return this.options.playbackScoreThreshold;
    }
    return PROTOCOL_THRESHOLDS[this.options.protocol];
  }

  /**
   * Set a custom playback score threshold (overrides protocol default)
   */
  setPlaybackScoreThreshold(threshold: number | null): void {
    this.options.playbackScoreThreshold = threshold;
  }

  /**
   * Start monitoring a video element
   */
  start(videoElement: HTMLVideoElement): void {
    this.stop();

    this.videoElement = videoElement;
    this.stallCount = 0;
    this.totalStallMs = 0;
    this.lastStallTime = 0;
    this.history = [];
    this.lastBytesLoaded = 0;
    this.lastBytesTime = 0;
    this.consecutivePoorSamples = 0;
    this.fallbackTriggered = false;
    this.playbackScoreHistory = [];
    this.playbackScore = 1.0;

    // Listen for stall events
    const onWaiting = () => {
      this.stallCount++;
      this.lastStallTime = performance.now();
    };

    const onPlaying = () => {
      if (this.lastStallTime > 0) {
        this.totalStallMs += performance.now() - this.lastStallTime;
        this.lastStallTime = 0;
      }
    };

    const onCanPlay = () => {
      if (this.lastStallTime > 0) {
        this.totalStallMs += performance.now() - this.lastStallTime;
        this.lastStallTime = 0;
      }
    };

    videoElement.addEventListener("waiting", onWaiting);
    videoElement.addEventListener("playing", onPlaying);
    videoElement.addEventListener("canplay", onCanPlay);

    this.listeners = [
      () => videoElement.removeEventListener("waiting", onWaiting),
      () => videoElement.removeEventListener("playing", onPlaying),
      () => videoElement.removeEventListener("canplay", onCanPlay),
    ];

    // Start sampling interval
    this.timers.startInterval(() => this.sample(), this.options.sampleInterval, "sampling");

    // Take initial sample
    this.sample();
  }

  /**
   * Stop monitoring
   */
  stop(): void {
    this.timers.destroy();

    this.listeners.forEach((cleanup) => cleanup());
    this.listeners = [];

    this.videoElement = null;
  }

  /**
   * Take a quality sample
   */
  private sample(): void {
    const video = this.videoElement;
    if (!video) return;

    // Paused/ended video has videoDelta=0 which reads as 0% playback score.
    // Don't let that poison the score or trigger destructive fallback.
    const isActive = !video.paused && !video.ended && video.readyState >= 3;

    if (isActive) {
      this.updatePlaybackScore();
    }

    const quality = this.calculateQuality(video);
    this.history.push(quality);

    // Keep rolling window
    if (this.history.length > ROLLING_WINDOW_SIZE) {
      this.history.shift();
    }

    // Notify listeners
    this.options.onSample(quality);

    // Check for quality degradation
    if (
      quality.score < this.thresholds.minScore ||
      quality.stallCount > this.thresholds.maxStalls ||
      quality.bufferedAhead < this.thresholds.minBuffer
    ) {
      this.options.onQualityDegraded(quality);
    }

    // Track sustained poor quality for automatic fallback
    // Reference: player.js:654-665 - "nextCombo" after sustained poor playback
    if (isActive && this.isPlaybackPoor()) {
      this.consecutivePoorSamples++;

      // Trigger fallback after sustained poor quality
      // Only trigger once until quality improves or reset
      if (
        !this.fallbackTriggered &&
        this.consecutivePoorSamples >= this.options.poorSamplesBeforeFallback
      ) {
        this.fallbackTriggered = true;
        console.warn(
          `[QualityMonitor] Poor playback detected: ${Math.round(this.playbackScore * 100)}% ` +
            `(threshold: ${Math.round(this.getPlaybackScoreThreshold() * 100)}%, ` +
            `protocol: ${this.options.protocol})`
        );
        this.options.onFallbackRequest({
          score: this.playbackScore,
          consecutivePoorSamples: this.consecutivePoorSamples,
        });
      }
    } else if (!isActive) {
      // Don't accumulate poor samples across pause/play boundaries
      this.consecutivePoorSamples = 0;
    } else {
      // Quality recovered - reset counters
      this.consecutivePoorSamples = 0;
      this.fallbackTriggered = false;
    }
  }

  /**
   * Calculate current quality metrics
   */
  private calculateQuality(video: HTMLVideoElement): PlaybackQuality {
    const now = Date.now();

    // Calculate buffered ahead
    let bufferedAhead = 0;
    if (video.buffered.length > 0) {
      for (let i = 0; i < video.buffered.length; i++) {
        if (
          video.buffered.start(i) <= video.currentTime &&
          video.buffered.end(i) > video.currentTime
        ) {
          bufferedAhead = video.buffered.end(i) - video.currentTime;
          break;
        }
      }
    }

    // Get frame stats if available
    let framesDecoded = 0;
    let framesDropped = 0;
    let frameDropRate = 0;

    if ("getVideoPlaybackQuality" in video) {
      const stats = video.getVideoPlaybackQuality();
      framesDecoded = stats.totalVideoFrames;
      framesDropped = stats.droppedVideoFrames;
      frameDropRate = framesDecoded > 0 ? (framesDropped / framesDecoded) * 100 : 0;
    }

    // Estimate bitrate from buffer loading
    let bitrate = 0;
    if (video.buffered.length > 0 && this.lastBytesTime > 0) {
      const timeElapsed = (now - this.lastBytesTime) / 1000;
      if (timeElapsed > 0) {
        // Estimate from buffer growth
        // This is a rough approximation - real bitrate tracking would use MSE
        const bufferEnd = video.buffered.end(video.buffered.length - 1);
        const bufferDuration = bufferEnd - video.currentTime;
        // Assume average bitrate based on buffer size
        bitrate = bufferDuration > 0 ? Math.round((bufferDuration * 1000000) / timeElapsed) : 0;
      }
    }
    this.lastBytesTime = now;

    // Calculate latency for live streams
    let latency = 0;
    if (video.duration === Infinity || !isFinite(video.duration)) {
      // Live stream - estimate latency from buffer
      if (video.buffered.length > 0) {
        const liveEdge = video.buffered.end(video.buffered.length - 1);
        latency = (liveEdge - video.currentTime) * 1000;
      }
    }

    // Calculate composite quality score (0-100) with duration-weighted stalls
    const score = this.calculateScore({
      bufferedAhead,
      stallCount: this.stallCount,
      stallDurationMs: this.totalStallMs,
      frameDropRate,
      latency,
    });

    return {
      score,
      bitrate,
      bufferedAhead,
      stallCount: this.stallCount,
      frameDropRate,
      latency,
      timestamp: now,
    };
  }

  /**
   * Calculate composite quality score
   *
   * D4: Duration-weighted stall tracking - stall penalty considers both
   * count AND duration. 10x 0.1s stalls (1s total) weighs less than 1x 1s stall.
   */
  private calculateScore(metrics: {
    bufferedAhead: number;
    stallCount: number;
    stallDurationMs: number;
    frameDropRate: number;
    latency: number;
  }): number {
    let score = 100;

    // Buffer penalty (max -40 points)
    if (metrics.bufferedAhead < this.thresholds.minBuffer) {
      const bufferPenalty = Math.min(40, (this.thresholds.minBuffer - metrics.bufferedAhead) * 20);
      score -= bufferPenalty;
    }

    // D4: Duration-weighted stall penalty (max -30 points)
    // Base: 5 points per stall + 2 points per second of total stall time
    // This weights duration: 1x 2s stall = 5 + 4 = 9 points
    //                       10x 0.2s stalls = 50 + 4 = 54 points (capped at 30)
    // So many short stalls are penalized more than few long stalls of same duration
    const countPenalty = metrics.stallCount * 5;
    const durationPenalty = (metrics.stallDurationMs / 1000) * 2;
    const stallPenalty = Math.min(30, countPenalty + durationPenalty);
    score -= stallPenalty;

    // Frame drop penalty (max -20 points)
    const framePenalty = Math.min(20, metrics.frameDropRate * 2);
    score -= framePenalty;

    // Latency penalty for live streams (max -10 points)
    if (metrics.latency > 5000) {
      const latencyPenalty = Math.min(10, (metrics.latency - 5000) / 1000);
      score -= latencyPenalty;
    }

    return Math.max(0, Math.round(score));
  }

  /**
   * Get current quality metrics
   */
  getCurrentQuality(): PlaybackQuality | null {
    return this.history.length > 0 ? this.history[this.history.length - 1] : null;
  }

  /**
   * Get rolling average quality
   */
  getAverageQuality(): PlaybackQuality | null {
    if (this.history.length === 0) return null;

    const avg: PlaybackQuality = {
      score: 0,
      bitrate: 0,
      bufferedAhead: 0,
      stallCount: this.stallCount,
      frameDropRate: 0,
      latency: 0,
      timestamp: Date.now(),
    };

    for (const q of this.history) {
      avg.score += q.score;
      avg.bitrate += q.bitrate;
      avg.bufferedAhead += q.bufferedAhead;
      avg.frameDropRate += q.frameDropRate;
      avg.latency += q.latency;
    }

    const len = this.history.length;
    avg.score = Math.round(avg.score / len);
    avg.bitrate = Math.round(avg.bitrate / len);
    avg.bufferedAhead = avg.bufferedAhead / len;
    avg.frameDropRate = avg.frameDropRate / len;
    avg.latency = avg.latency / len;

    return avg;
  }

  /**
   * Get quality history
   */
  getHistory(): PlaybackQuality[] {
    return [...this.history];
  }

  /**
   * Reset stall counters
   */
  resetStallCounters(): void {
    this.stallCount = 0;
    this.totalStallMs = 0;
  }

  /**
   * Get total stall time in ms
   */
  getTotalStallMs(): number {
    return this.totalStallMs;
  }

  /**
   * Check if currently monitoring
   */
  isMonitoring(): boolean {
    return this.videoElement !== null && this.timers.activeCount > 0;
  }

  // ========================================
  // MistPlayer-style Playback Score (0-2.0)
  // ========================================

  /**
   * Calculate playback score entry value
   * Compares video time progress vs wall clock time
   */
  private getPlaybackScoreValue(): PlaybackScoreEntry {
    const video = this.videoElement;
    const clock = performance.now() / 1000;
    const videoTime = video?.currentTime ?? 0;

    const result: PlaybackScoreEntry = {
      clock,
      video: videoTime,
      score: 1.0,
    };

    if (this.playbackScoreHistory.length > 0) {
      const prev = this.playbackScoreHistory[this.playbackScoreHistory.length - 1];
      result.score = this.calculatePlaybackScoreFromEntries(prev, result);
    }

    return result;
  }

  /**
   * Calculate score between two entries
   * Returns 1.0 for normal playback, >1.0 if faster, <1.0 if stalled, <0 if backwards
   */
  private calculatePlaybackScoreFromEntries(a: PlaybackScoreEntry, b: PlaybackScoreEntry): number {
    const video = this.videoElement;
    let rate = 1;
    if (video) {
      rate = video.playbackRate || 1;
    }

    const clockDelta = b.clock - a.clock;
    const videoDelta = b.video - a.video;

    if (clockDelta <= 0) return 1.0;

    return videoDelta / clockDelta / rate;
  }

  /**
   * Calculate and update the playback score
   * Like MistPlayer's calcScore function
   */
  private updatePlaybackScore(): number {
    const entry = this.getPlaybackScoreValue();
    this.playbackScoreHistory.push(entry);

    if (this.playbackScoreHistory.length <= 1) {
      return 1.0;
    }

    // Calculate score from oldest to newest
    const first = this.playbackScoreHistory[0];
    const last = this.playbackScoreHistory[this.playbackScoreHistory.length - 1];
    let score = this.calculatePlaybackScoreFromEntries(first, last);

    // Trim history
    if (this.playbackScoreHistory.length > this.PLAYBACK_SCORE_AVERAGING_STEPS) {
      this.playbackScoreHistory.shift();
    }

    // Final score is max of averaged and current
    score = Math.max(score, entry.score);
    this.playbackScore = score;

    return score;
  }

  /**
   * Get current playback score (MistPlayer-style 0-2.0 scale)
   *
   * - 1.0 = normal playback (video progresses at expected rate)
   * - > 1.0 = faster than expected (catching up)
   * - < 1.0 = slower than expected (stalling/buffering)
   * - < 0 = video went backwards
   *
   * Threshold recommendations:
   * - WebRTC: warn below 0.95
   * - HLS/DASH: warn below 0.75
   */
  getPlaybackScore(): number {
    return this.playbackScore;
  }

  /**
   * Check if playback quality is poor based on score
   * Uses protocol-specific thresholds (MistPlayer-style)
   * WebRTC: 0.95 (strict), HLS/DASH/HTML5: 0.75 (lenient)
   */
  isPlaybackPoor(): boolean {
    return this.playbackScore < this.getPlaybackScoreThreshold();
  }

  /**
   * Reset playback score tracking
   */
  resetPlaybackScore(): void {
    this.playbackScoreHistory = [];
    this.playbackScore = 1.0;
  }

  /**
   * Reset fallback state
   * Call after a player switch to allow fallback to trigger again
   */
  resetFallbackState(): void {
    this.consecutivePoorSamples = 0;
    this.fallbackTriggered = false;
  }

  /**
   * Get consecutive poor sample count (for debugging)
   */
  getConsecutivePoorSamples(): number {
    return this.consecutivePoorSamples;
  }

  /**
   * Check if fallback has been triggered (for debugging)
   */
  hasFallbackTriggered(): boolean {
    return this.fallbackTriggered;
  }
}

export default QualityMonitor;
