import type { PlaybackQuality, QualityThresholds } from '../types';

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

export interface QualityMonitorOptions {
  /** Sample interval in ms */
  sampleInterval?: number;
  /** Quality thresholds */
  thresholds?: Partial<QualityThresholds>;
  /** Callback when quality degrades */
  onQualityDegraded?: (quality: PlaybackQuality) => void;
  /** Callback on every sample */
  onSample?: (quality: PlaybackQuality) => void;
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
  private options: Required<QualityMonitorOptions>;
  private thresholds: QualityThresholds;
  private intervalId: ReturnType<typeof setInterval> | null = null;
  private stallCount = 0;
  private lastStallTime = 0;
  private totalStallMs = 0;
  private history: PlaybackQuality[] = [];
  private lastBytesLoaded = 0;
  private lastBytesTime = 0;
  private listeners: Array<() => void> = [];

  constructor(options: QualityMonitorOptions = {}) {
    this.options = {
      sampleInterval: options.sampleInterval ?? 500,
      thresholds: options.thresholds ?? {},
      onQualityDegraded: options.onQualityDegraded ?? (() => {}),
      onSample: options.onSample ?? (() => {}),
    };
    this.thresholds = { ...DEFAULT_THRESHOLDS, ...options.thresholds };
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

    videoElement.addEventListener('waiting', onWaiting);
    videoElement.addEventListener('playing', onPlaying);
    videoElement.addEventListener('canplay', onCanPlay);

    this.listeners = [
      () => videoElement.removeEventListener('waiting', onWaiting),
      () => videoElement.removeEventListener('playing', onPlaying),
      () => videoElement.removeEventListener('canplay', onCanPlay),
    ];

    // Start sampling interval
    this.intervalId = setInterval(() => this.sample(), this.options.sampleInterval);

    // Take initial sample
    this.sample();
  }

  /**
   * Stop monitoring
   */
  stop(): void {
    if (this.intervalId) {
      clearInterval(this.intervalId);
      this.intervalId = null;
    }

    this.listeners.forEach(cleanup => cleanup());
    this.listeners = [];

    this.videoElement = null;
  }

  /**
   * Take a quality sample
   */
  private sample(): void {
    const video = this.videoElement;
    if (!video) return;

    const quality = this.calculateQuality(video);
    this.history.push(quality);

    // Keep rolling window
    if (this.history.length > ROLLING_WINDOW_SIZE) {
      this.history.shift();
    }

    // Notify listeners
    this.options.onSample(quality);

    // Check for quality degradation
    if (quality.score < this.thresholds.minScore ||
        quality.stallCount > this.thresholds.maxStalls ||
        quality.bufferedAhead < this.thresholds.minBuffer) {
      this.options.onQualityDegraded(quality);
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
        if (video.buffered.start(i) <= video.currentTime && video.buffered.end(i) > video.currentTime) {
          bufferedAhead = video.buffered.end(i) - video.currentTime;
          break;
        }
      }
    }

    // Get frame stats if available
    let framesDecoded = 0;
    let framesDropped = 0;
    let frameDropRate = 0;

    if ('getVideoPlaybackQuality' in video) {
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

    // Calculate composite quality score (0-100)
    const score = this.calculateScore({
      bufferedAhead,
      stallCount: this.stallCount,
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
   */
  private calculateScore(metrics: {
    bufferedAhead: number;
    stallCount: number;
    frameDropRate: number;
    latency: number;
  }): number {
    let score = 100;

    // Buffer penalty (max -40 points)
    if (metrics.bufferedAhead < this.thresholds.minBuffer) {
      const bufferPenalty = Math.min(40, (this.thresholds.minBuffer - metrics.bufferedAhead) * 20);
      score -= bufferPenalty;
    }

    // Stall penalty (max -30 points)
    const stallPenalty = Math.min(30, metrics.stallCount * 10);
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
    return this.intervalId !== null;
  }
}

export default QualityMonitor;
