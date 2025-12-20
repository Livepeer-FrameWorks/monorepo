/**
 * LiveDurationProxy - Wraps video element to provide meaningful duration for live streams
 *
 * Live streams report `Infinity` or `NaN` for duration, breaking seek bars and time display.
 * This proxy intercepts the duration getter and returns a calculated value based on:
 * - Buffer end position
 * - Elapsed time since last progress event
 *
 * Based on MistMetaPlayer reference implementation (wrappers/html5.js, dashjs.js)
 */

export interface LiveDurationProxyOptions {
  /** Whether to constrain seeking to buffered range (default: true) */
  constrainSeek?: boolean;
  /** Live offset from buffer end in seconds (default: 0) */
  liveOffset?: number;
  /** Callback when duration changes */
  onDurationChange?: (duration: number) => void;
}

export interface LiveDurationState {
  /** Calculated duration */
  duration: number;
  /** Whether stream is live */
  isLive: boolean;
  /** Buffer end position */
  bufferEnd: number;
  /** Time since last progress */
  elapsed: number;
}

/**
 * Creates a proxy wrapper around a video element that provides meaningful
 * duration values for live streams.
 */
export class LiveDurationProxy {
  private video: HTMLVideoElement;
  private options: Required<LiveDurationProxyOptions>;
  private lastProgressTime: number = 0;
  private lastBufferEnd: number = 0;
  private listeners: Array<() => void> = [];
  private _calculatedDuration: number = 0;

  constructor(video: HTMLVideoElement, options: LiveDurationProxyOptions = {}) {
    this.video = video;
    this.options = {
      constrainSeek: options.constrainSeek ?? true,
      liveOffset: options.liveOffset ?? 0,
      onDurationChange: options.onDurationChange ?? (() => {}),
    };

    this.setupListeners();
    this.updateDuration();
  }

  /**
   * Check if the stream is live
   */
  isLive(): boolean {
    const nativeDuration = this.video.duration;
    return !isFinite(nativeDuration) || nativeDuration === Infinity;
  }

  /**
   * Get the calculated duration
   * For live: bufferEnd + elapsedSinceLastProgress
   * For VOD: native duration
   */
  getDuration(): number {
    if (!this.isLive()) {
      return this.video.duration;
    }
    return this._calculatedDuration;
  }

  /**
   * Get the current buffer end position
   */
  getBufferEnd(): number {
    const buffered = this.video.buffered;
    if (buffered.length === 0) return 0;

    // Find the buffer range containing current time, or the last one
    for (let i = 0; i < buffered.length; i++) {
      if (buffered.start(i) <= this.video.currentTime && buffered.end(i) > this.video.currentTime) {
        return buffered.end(i);
      }
    }

    // Return the end of the last buffer range
    return buffered.end(buffered.length - 1);
  }

  /**
   * Get the live edge position (where live is)
   */
  getLiveEdge(): number {
    return this.getBufferEnd() - this.options.liveOffset;
  }

  /**
   * Get the current latency (distance from live edge)
   */
  getLatency(): number {
    if (!this.isLive()) return 0;
    return Math.max(0, this.getLiveEdge() - this.video.currentTime);
  }

  /**
   * Seek to a position, respecting live constraints
   */
  seek(time: number): void {
    if (!this.isLive() || !this.options.constrainSeek) {
      this.video.currentTime = time;
      return;
    }

    // Constrain to buffered range for live
    const buffered = this.video.buffered;
    if (buffered.length === 0) {
      this.video.currentTime = time;
      return;
    }

    // Find valid seek range
    const bufferStart = buffered.start(0);
    const bufferEnd = this.getBufferEnd();
    const liveEdge = this.getLiveEdge();

    // Clamp to valid range
    const clampedTime = Math.max(bufferStart, Math.min(time, liveEdge));
    this.video.currentTime = clampedTime;
  }

  /**
   * Jump to live edge
   */
  jumpToLive(): void {
    if (!this.isLive()) return;
    this.video.currentTime = this.getLiveEdge();
  }

  /**
   * Check if currently at live edge (within threshold)
   */
  isAtLiveEdge(threshold: number = 2): boolean {
    if (!this.isLive()) return false;
    return this.getLatency() <= threshold;
  }

  /**
   * Get current state
   */
  getState(): LiveDurationState {
    return {
      duration: this.getDuration(),
      isLive: this.isLive(),
      bufferEnd: this.getBufferEnd(),
      elapsed: this.lastProgressTime > 0 ? (Date.now() - this.lastProgressTime) / 1000 : 0,
    };
  }

  /**
   * Update the calculated duration
   */
  private updateDuration(): void {
    if (!this.isLive()) {
      this._calculatedDuration = this.video.duration;
      return;
    }

    const bufferEnd = this.getBufferEnd();
    const now = Date.now();
    const elapsedSinceProgress = this.lastProgressTime > 0
      ? (now - this.lastProgressTime) / 1000
      : 0;

    // MistPlayer formula: buffer_end + elapsed_since_last_progress
    const newDuration = bufferEnd + elapsedSinceProgress;

    // Only update if changed significantly (avoid constant updates)
    if (Math.abs(newDuration - this._calculatedDuration) > 0.1) {
      this._calculatedDuration = newDuration;
      this.options.onDurationChange(newDuration);
    }

    this.lastBufferEnd = bufferEnd;
  }

  /**
   * Setup event listeners for tracking
   */
  private setupListeners(): void {
    const onProgress = () => {
      this.lastProgressTime = Date.now();
      this.updateDuration();
    };

    const onTimeUpdate = () => {
      this.updateDuration();
    };

    const onDurationChange = () => {
      this.updateDuration();
    };

    const onLoadedMetadata = () => {
      this.updateDuration();
    };

    this.video.addEventListener('progress', onProgress);
    this.video.addEventListener('timeupdate', onTimeUpdate);
    this.video.addEventListener('durationchange', onDurationChange);
    this.video.addEventListener('loadedmetadata', onLoadedMetadata);

    this.listeners = [
      () => this.video.removeEventListener('progress', onProgress),
      () => this.video.removeEventListener('timeupdate', onTimeUpdate),
      () => this.video.removeEventListener('durationchange', onDurationChange),
      () => this.video.removeEventListener('loadedmetadata', onLoadedMetadata),
    ];
  }

  /**
   * Cleanup
   */
  destroy(): void {
    this.listeners.forEach(cleanup => cleanup());
    this.listeners = [];
  }
}

/**
 * Create a Proxy wrapper for a video element that intercepts duration/currentTime
 * This allows existing code to work transparently with live streams.
 *
 * Note: This is an advanced feature - for most cases, use LiveDurationProxy directly.
 */
export function createLiveVideoProxy(
  video: HTMLVideoElement,
  options: LiveDurationProxyOptions = {}
): { proxy: HTMLVideoElement; controller: LiveDurationProxy } {
  const controller = new LiveDurationProxy(video, options);

  const proxy = new Proxy(video, {
    get(target, prop, receiver) {
      if (prop === 'duration') {
        return controller.getDuration();
      }

      const value = Reflect.get(target, prop, receiver);
      if (typeof value === 'function') {
        return value.bind(target);
      }
      return value;
    },

    set(target, prop, value, receiver) {
      if (prop === 'currentTime' && controller.isLive()) {
        controller.seek(value as number);
        return true;
      }
      return Reflect.set(target, prop, value, receiver);
    },
  });

  return { proxy: proxy as HTMLVideoElement, controller };
}

export default LiveDurationProxy;
