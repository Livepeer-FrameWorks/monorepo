import type { ABRMode, ABROptions, PlaybackQuality, QualityLevel } from '../types';

/**
 * Default ABR options
 */
const DEFAULT_OPTIONS: Required<ABROptions> = {
  mode: 'auto',
  maxResolution: { width: 1920, height: 1080 },
  maxBitrate: 8000000, // 8 Mbps
  minBufferForUpgrade: 10,
  downgradeThreshold: 60,
};

export interface ABRControllerConfig {
  /** ABR options */
  options?: Partial<ABROptions>;
  /** Callback to get available qualities */
  getQualities: () => QualityLevel[];
  /** Callback to select a quality */
  selectQuality: (id: string | 'auto') => void;
  /** Callback to get current quality */
  getCurrentQuality?: () => QualityLevel | null;
  /** Debug logging */
  debug?: boolean;
}

export type ABRDecision = 'upgrade' | 'downgrade' | 'maintain' | 'none';

/**
 * ABRController - Adaptive Bitrate Controller
 *
 * Manages automatic quality selection based on:
 * - ABR_resize: Matches video resolution to viewport size
 * - ABR_bitrate: Switches quality based on playback performance
 * - auto: Combines both modes
 * - manual: No automatic switching
 *
 * @example
 * ```ts
 * const abr = new ABRController({
 *   options: { mode: 'auto' },
 *   getQualities: () => player.getQualities(),
 *   selectQuality: (id) => player.selectQuality(id),
 * });
 *
 * abr.start(videoElement);
 * abr.onQualityChange((quality) => console.log('Quality:', quality.score));
 * ```
 */
export class ABRController {
  private options: Required<ABROptions>;
  private config: ABRControllerConfig;
  private videoElement: HTMLVideoElement | null = null;
  private currentQualityId: string | 'auto' = 'auto';
  private lastDecision: ABRDecision = 'none';
  private lastDecisionTime = 0;
  private resizeObserver: ResizeObserver | null = null;
  private qualityChangeCallbacks: Array<(level: QualityLevel) => void> = [];
  private debug: boolean;

  // Cooldown period between decisions (5 seconds)
  private static readonly DECISION_COOLDOWN = 5000;

  constructor(config: ABRControllerConfig) {
    this.options = { ...DEFAULT_OPTIONS, ...config.options };
    this.config = config;
    this.debug = config.debug ?? false;
  }

  /**
   * Start ABR control
   */
  start(videoElement: HTMLVideoElement): void {
    this.stop();
    this.videoElement = videoElement;

    if (this.options.mode === 'manual') {
      this.log('Manual mode - no automatic ABR');
      return;
    }

    // Setup resize observer for ABR_resize mode
    if (this.options.mode === 'resize' || this.options.mode === 'auto') {
      this.setupResizeObserver();
    }
  }

  /**
   * Stop ABR control
   */
  stop(): void {
    if (this.resizeObserver) {
      this.resizeObserver.disconnect();
      this.resizeObserver = null;
    }
    this.videoElement = null;
  }

  /**
   * Setup resize observer for viewport-based quality selection
   */
  private setupResizeObserver(): void {
    const video = this.videoElement;
    if (!video) return;

    this.resizeObserver = new ResizeObserver((entries) => {
      for (const entry of entries) {
        const { width, height } = entry.contentRect;
        this.handleResize(width, height);
      }
    });

    // Observe the video element's container
    const container = video.parentElement;
    if (container) {
      this.resizeObserver.observe(container);
    }

    // Initial resize handling
    const rect = video.getBoundingClientRect();
    this.handleResize(rect.width, rect.height);
  }

  /**
   * Handle viewport resize (ABR_resize mode)
   */
  private handleResize(width: number, height: number): void {
    if (this.options.mode !== 'resize' && this.options.mode !== 'auto') {
      return;
    }

    const qualities = this.config.getQualities();
    if (qualities.length === 0) return;

    // Find best quality for viewport size
    const targetWidth = Math.min(width * window.devicePixelRatio, this.options.maxResolution.width);
    const targetHeight = Math.min(height * window.devicePixelRatio, this.options.maxResolution.height);

    const bestQuality = this.findBestQualityForResolution(qualities, targetWidth, targetHeight);

    if (bestQuality && bestQuality.id !== this.currentQualityId) {
      this.log(`Resize ABR: ${width}x${height} -> selecting ${bestQuality.label}`);
      this.selectQuality(bestQuality.id);
    }
  }

  /**
   * Handle quality degradation (ABR_bitrate mode)
   *
   * Called by QualityMonitor when playback quality drops
   */
  handleQualityDegraded(quality: PlaybackQuality): void {
    if (this.options.mode !== 'bitrate' && this.options.mode !== 'auto') {
      return;
    }

    // Enforce cooldown period
    const now = Date.now();
    if (now - this.lastDecisionTime < ABRController.DECISION_COOLDOWN) {
      return;
    }

    if (quality.score < this.options.downgradeThreshold) {
      const qualities = this.config.getQualities();
      const currentQuality = this.config.getCurrentQuality?.();

      if (currentQuality) {
        // Find a lower quality level
        const lowerQuality = this.findLowerQuality(qualities, currentQuality);

        if (lowerQuality) {
          this.log(`Bitrate ABR: score ${quality.score} -> downgrading to ${lowerQuality.label}`);
          this.lastDecision = 'downgrade';
          this.lastDecisionTime = now;
          this.selectQuality(lowerQuality.id);
        }
      }
    }
  }

  /**
   * Handle quality improvement opportunity
   *
   * Called when conditions are good enough to try higher quality
   */
  handleQualityImproved(quality: PlaybackQuality): void {
    if (this.options.mode !== 'bitrate' && this.options.mode !== 'auto') {
      return;
    }

    // Enforce cooldown period
    const now = Date.now();
    if (now - this.lastDecisionTime < ABRController.DECISION_COOLDOWN) {
      return;
    }

    // Only upgrade if buffer is healthy and quality is good
    if (quality.score >= 90 && quality.bufferedAhead >= this.options.minBufferForUpgrade) {
      const qualities = this.config.getQualities();
      const currentQuality = this.config.getCurrentQuality?.();

      if (currentQuality) {
        // Find a higher quality level
        const higherQuality = this.findHigherQuality(qualities, currentQuality);

        if (higherQuality && this.isWithinConstraints(higherQuality)) {
          this.log(`Bitrate ABR: score ${quality.score} -> upgrading to ${higherQuality.label}`);
          this.lastDecision = 'upgrade';
          this.lastDecisionTime = now;
          this.selectQuality(higherQuality.id);
        }
      }
    }
  }

  /**
   * Find best quality level for given resolution
   */
  private findBestQualityForResolution(
    qualities: QualityLevel[],
    targetWidth: number,
    targetHeight: number
  ): QualityLevel | null {
    // Filter out qualities that exceed constraints
    const validQualities = qualities.filter(q => this.isWithinConstraints(q));

    if (validQualities.length === 0) return null;

    // Sort by resolution (ascending)
    const sorted = [...validQualities].sort((a, b) => {
      const aPixels = (a.width ?? 0) * (a.height ?? 0);
      const bPixels = (b.width ?? 0) * (b.height ?? 0);
      return aPixels - bPixels;
    });

    // Find smallest quality that is >= target resolution
    for (const q of sorted) {
      const qWidth = q.width ?? 0;
      const qHeight = q.height ?? 0;

      if (qWidth >= targetWidth && qHeight >= targetHeight) {
        return q;
      }
    }

    // If no quality is large enough, return the highest available
    return sorted[sorted.length - 1];
  }

  /**
   * Find a lower quality level
   */
  private findLowerQuality(
    qualities: QualityLevel[],
    current: QualityLevel
  ): QualityLevel | null {
    const currentBitrate = current.bitrate ?? 0;

    // Sort by bitrate descending
    const sorted = [...qualities].sort((a, b) => (b.bitrate ?? 0) - (a.bitrate ?? 0));

    // Find next lower bitrate
    for (const q of sorted) {
      if ((q.bitrate ?? 0) < currentBitrate) {
        return q;
      }
    }

    return null;
  }

  /**
   * Find a higher quality level
   */
  private findHigherQuality(
    qualities: QualityLevel[],
    current: QualityLevel
  ): QualityLevel | null {
    const currentBitrate = current.bitrate ?? 0;

    // Sort by bitrate ascending
    const sorted = [...qualities].sort((a, b) => (a.bitrate ?? 0) - (b.bitrate ?? 0));

    // Find next higher bitrate
    for (const q of sorted) {
      if ((q.bitrate ?? 0) > currentBitrate) {
        return q;
      }
    }

    return null;
  }

  /**
   * Check if quality is within configured constraints
   */
  private isWithinConstraints(quality: QualityLevel): boolean {
    const { maxResolution, maxBitrate } = this.options;

    if (quality.width && quality.width > maxResolution.width) return false;
    if (quality.height && quality.height > maxResolution.height) return false;
    if (quality.bitrate && quality.bitrate > maxBitrate) return false;

    return true;
  }

  /**
   * Select a quality level
   */
  private selectQuality(id: string | 'auto'): void {
    this.currentQualityId = id;
    this.config.selectQuality(id);

    // Notify callbacks
    const qualities = this.config.getQualities();
    const selected = qualities.find(q => q.id === id);
    if (selected) {
      this.qualityChangeCallbacks.forEach(cb => cb(selected));
    }
  }

  /**
   * Register callback for quality changes
   */
  onQualityChange(callback: (level: QualityLevel) => void): () => void {
    this.qualityChangeCallbacks.push(callback);
    return () => {
      const idx = this.qualityChangeCallbacks.indexOf(callback);
      if (idx >= 0) {
        this.qualityChangeCallbacks.splice(idx, 1);
      }
    };
  }

  /**
   * Manually set quality (switches to manual mode temporarily)
   */
  setQuality(id: string | 'auto'): void {
    this.selectQuality(id);
  }

  /**
   * Get current ABR mode
   */
  getMode(): ABRMode {
    return this.options.mode;
  }

  /**
   * Update ABR options
   */
  updateOptions(options: Partial<ABROptions>): void {
    this.options = { ...this.options, ...options };
  }

  /**
   * Get last ABR decision
   */
  getLastDecision(): ABRDecision {
    return this.lastDecision;
  }

  /**
   * Debug logging
   */
  private log(message: string): void {
    if (this.debug) {
      console.debug(`[ABRController] ${message}`);
    }
  }
}

export default ABRController;
