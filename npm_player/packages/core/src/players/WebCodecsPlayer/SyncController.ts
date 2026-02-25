/**
 * SyncController - Buffer Management & Playback Timing
 *
 * Orchestrates:
 * - Buffer level monitoring with variance-based jitter
 * - Adaptive playback speed with smooth rate transitions
 * - Audio-master A/V sync (when AudioWorkletRenderer is active)
 * - Dynamic buffer thresholds
 * - Server delay estimation
 * - Seek coordination
 */

import type { LatencyProfile, SyncState, TrackInfo } from "./types";
import { MultiTrackJitterTracker } from "./JitterBuffer";
import { getLatencyProfile } from "./LatencyProfiles";

/** Events emitted by SyncController */
export interface SyncControllerEvents {
  speedchange: { speed: number; reason: "catchup" | "slowdown" | "normal" };
  bufferlow: { current: number; desired: number };
  bufferhigh: { current: number; desired: number };
  underrun: void;
  livecatchup: { fastForwardMs: number };
  seekstart: { seekId: number; time: number };
  seekcomplete: { seekId: number };
}

type EventListener<K extends keyof SyncControllerEvents> = (data: SyncControllerEvents[K]) => void;

/** Seek state tracking */
interface SeekState {
  active: boolean;
  id: number;
  targetTime: number;
  startedAt: number;
}

interface BufferEvalContext {
  playRateCurr?: number | "auto" | "fast-forward";
  serverCurrentMs?: number;
  serverEndMs?: number;
  serverJitterMs?: number;
}

/**
 * SyncController - Manages playback synchronization
 */
export class SyncController {
  private profile: LatencyProfile;
  private jitterTracker: MultiTrackJitterTracker;
  private listeners = new Map<keyof SyncControllerEvents, Set<Function>>();

  // Playback speed state
  private mainSpeed = 1;
  private tweakSpeed = 1;
  private targetTweakSpeed = 1;
  private serverPlayRateCurr: number | "auto" | "fast-forward" = "auto";
  private rateTransitionTimer: ReturnType<typeof setInterval> | null = null;
  /** Hysteresis bucket: prevents oscillation at threshold boundaries */
  private _speedBucket: "low" | "normal" | "high" = "normal";

  // Buffer state
  private lastBufferCheck = 0;
  private bufferCheckInterval = 100; // ms

  // Adaptive buffer thresholds
  private stutterCount = 0;
  private stutterResetTimer: ReturnType<typeof setTimeout> | null = null;
  private bufferMultiplier = 1; // Dynamic multiplier for desired buffer

  // Variance-based jitter
  private jitterSamples: number[] = [];
  private static readonly JITTER_WINDOW_SIZE = 10;

  // Server delay tracking
  private serverDelays: number[] = [];
  private maxServerDelaysSamples = 3;

  // Live catchup tracking
  private lastLiveCatchup = 0;
  private liveCatchupCooldown = 2000;
  private liveCatchupThresholdMs = 5000;
  private liveCatchupRequestMs = 5000;
  private requestingMoreBuffer = false;
  private requestingMoreBufferAt = 0;
  private requestingMoreBufferBufferAt = 0;
  private requestingMoreBufferDesiredAt = 0;
  private requestingMoreBufferServerCurrentAt: number | null = null;
  private requestingMoreBufferGotSetSpeed = false;
  private requestingMoreBufferSawFastForward = false;
  private keepAwayExtraMs = 0;

  // Time tracking
  private serverTime = 0;
  private localTimeAtServerUpdate = 0;

  // Audio master clock
  private audioClockProvider: (() => number) | null = null;
  private lastAvDrift = 0;

  // Seek state
  private seekState: SeekState = {
    active: false,
    id: 0,
    targetTime: 0,
    startedAt: 0,
  };

  // Stream info
  private isLive = true;

  // Callbacks for external control
  private onSpeedChange?: (main: number, tweak: number) => void;
  private onFastForwardRequest?: (ms: number) => void;

  constructor(
    options: {
      profile?: LatencyProfile;
      isLive?: boolean;
      onSpeedChange?: (main: number, tweak: number) => void;
      onFastForwardRequest?: (ms: number) => void;
      /** Provide audio clock for A/V sync (returns seconds from AudioContext.currentTime) */
      audioClockProvider?: () => number;
      /** Live catch-up tuning (upstream parity: configurable thresholds) */
      liveCatchup?: {
        cooldownMs?: number;
        thresholdMs?: number;
        requestMs?: number;
      };
    } = {}
  ) {
    this.profile = options.profile ?? getLatencyProfile("low");
    this.isLive = options.isLive ?? true;
    this.onSpeedChange = options.onSpeedChange;
    this.onFastForwardRequest = options.onFastForwardRequest;
    this.audioClockProvider = options.audioClockProvider ?? null;
    if (options.liveCatchup) {
      this.liveCatchupCooldown = options.liveCatchup.cooldownMs ?? this.liveCatchupCooldown;
      this.liveCatchupThresholdMs = options.liveCatchup.thresholdMs ?? this.liveCatchupThresholdMs;
      this.liveCatchupRequestMs = options.liveCatchup.requestMs ?? this.liveCatchupRequestMs;
    }

    this.jitterTracker = new MultiTrackJitterTracker({
      initialJitter: 120,
    });
  }

  /**
   * Update latency profile
   */
  setProfile(profile: LatencyProfile): void {
    this.profile = profile;
  }

  /**
   * Update stream type (live vs VOD)
   */
  setLive(isLive: boolean): void {
    this.isLive = isLive;
  }

  /**
   * Record a chunk arrival for jitter tracking.
   * Also feeds the variance-based jitter sliding window.
   */
  recordChunkArrival(trackIndex: number, mediaTimeMs: number): void {
    this.jitterTracker.addChunk(trackIndex, mediaTimeMs);

    // Feed variance sliding window with current jitter value
    const instantJitter = this.jitterTracker.getForTrack(trackIndex);
    this.jitterSamples.push(instantJitter);
    if (this.jitterSamples.length > SyncController.JITTER_WINDOW_SIZE) {
      this.jitterSamples.shift();
    }
  }

  /**
   * Compute variance-based jitter metric.
   * Uses weighted average favoring recent samples, then sqrt(variance).
   */
  getJitterVariance(): number {
    if (this.jitterSamples.length < 2) return this.jitterTracker.getMax();

    const n = this.jitterSamples.length;
    // Weighted average (exponential decay, recent samples weighted higher)
    let weightedSum = 0;
    let weightTotal = 0;
    for (let i = 0; i < n; i++) {
      const weight = 1 + i; // Linear weight (most recent = highest)
      weightedSum += this.jitterSamples[i] * weight;
      weightTotal += weight;
    }
    const mean = weightedSum / weightTotal;

    // Variance
    let varianceSum = 0;
    for (let i = 0; i < n; i++) {
      const weight = 1 + i;
      const diff = this.jitterSamples[i] - mean;
      varianceSum += diff * diff * weight;
    }
    const variance = varianceSum / weightTotal;

    return Math.sqrt(variance);
  }

  /**
   * Record a stutter event (audio underrun, video freeze, etc.)
   * Used for dynamic buffer adjustment.
   */
  recordStutter(): void {
    this.stutterCount++;

    // Reset stutter count after 10 seconds of no stutters
    if (this.stutterResetTimer) clearTimeout(this.stutterResetTimer);
    this.stutterResetTimer = setTimeout(() => {
      this.stutterCount = 0;
    }, 10000);
  }

  /**
   * Set audio clock provider for A/V sync.
   */
  setAudioClockProvider(provider: (() => number) | null): void {
    this.audioClockProvider = provider;
  }

  /**
   * Get A/V sync decision for a video frame.
   * Returns the diff in ms between the frame's PTS and the audio clock.
   * Positive = frame is early (hold), Negative = frame is late (drop/render).
   */
  getFrameSyncDiff(framePtsSeconds: number): { diff: number; action: "render" | "hold" | "drop" } {
    if (!this.audioClockProvider) {
      return { diff: 0, action: "render" };
    }

    const audioTime = this.audioClockProvider();
    const diff = (framePtsSeconds - audioTime) * 1000; // ms
    this.lastAvDrift = diff;

    if (diff > 5) {
      // Frame is early — hold it
      return { diff, action: "hold" };
    } else if (diff < -100) {
      // Frame is > 100ms late — drop
      return { diff, action: "drop" };
    } else {
      // Within acceptable range — render
      return { diff, action: "render" };
    }
  }

  /**
   * Update server time from on_time message
   */
  updateServerTime(currentTime: number): void {
    this.serverTime = currentTime;
    this.localTimeAtServerUpdate = performance.now();
  }

  /**
   * Record server delay measurement
   */
  recordServerDelay(delayMs: number): void {
    this.serverDelays.push(delayMs);
    if (this.serverDelays.length > this.maxServerDelaysSamples) {
      this.serverDelays.shift();
    }
  }

  /**
   * Get current server delay estimate
   */
  getServerDelay(): number {
    if (this.serverDelays.length === 0) return 0;
    return this.serverDelays.reduce((sum, d) => sum + d, 0) / this.serverDelays.length;
  }

  /**
   * Get estimated current server time (interpolated)
   */
  getEstimatedServerTime(): number {
    const elapsed = (performance.now() - this.localTimeAtServerUpdate) / 1000;
    return this.serverTime + elapsed * this.getCombinedSpeed();
  }

  /**
   * Calculate desired buffer size based on profile + jitter + server delay.
   */
  getDesiredBuffer(): number {
    const serverDelay = this.getServerDelay();
    const jitter = this.jitterTracker.getMax() * this.profile.jitterMultiplier;
    // Upstream rawws.js parity:
    // desiredBuffer = keepAway + serverDelay + jitter
    // keepAway is profile-driven (live defaults to 100ms in low profile, VOD higher).
    const base = this.profile.keepAway + this.keepAwayExtraMs + serverDelay + jitter;
    return Math.round(base);
  }

  /**
   * Evaluate buffer state and adjust playback speed if needed.
   * Uses dynamic buffer thresholds and variance-based jitter.
   *
   * @param currentBufferMs - Current buffer level in milliseconds
   * @returns Updated sync state
   */
  evaluateBuffer(currentBufferMs: number, context?: BufferEvalContext): SyncState {
    const now = performance.now();

    // Rate limit buffer checks
    if (now - this.lastBufferCheck < this.bufferCheckInterval) {
      return this.getState(currentBufferMs);
    }
    this.lastBufferCheck = now;

    const desired = this.getDesiredBuffer();
    const ratio = currentBufferMs / desired;
    const jitterMs = this.jitterTracker.getMax();
    const playRateCurr = context?.playRateCurr ?? this.serverPlayRateCurr;
    const serverCurrentMs = context?.serverCurrentMs;
    const serverEndMs = context?.serverEndMs;
    const serverJitterMs = context?.serverJitterMs ?? jitterMs;

    if (this.requestingMoreBuffer) {
      // Upstream rawws.js parity:
      // 1) wait for set_speed response after fast_forward request
      // 2) if no set_speed, next on_time proves no extra data => slow down
      if (!this.requestingMoreBufferGotSetSpeed) {
        if (playRateCurr !== "fast-forward") {
          this.keepAwayExtraMs = Math.min(this.keepAwayExtraMs + 100, 500);
          this.beginSmoothTransition(this.profile.minSpeedDown, "slowdown");
          this.resetBufferRequestState();
        }
        return this.getState(currentBufferMs);
      }

      // set_speed received with play_rate_prev == "fast-forward":
      // evaluate how much extra buffer actually arrived by next on_time.
      if (
        this.requestingMoreBufferSawFastForward &&
        serverCurrentMs !== undefined &&
        this.requestingMoreBufferServerCurrentAt !== null
      ) {
        const increase =
          serverCurrentMs -
          this.requestingMoreBufferServerCurrentAt -
          (now - this.requestingMoreBufferAt);
        if (
          this.requestingMoreBufferBufferAt + increase <
          this.requestingMoreBufferDesiredAt * this.profile.speedDownThreshold
        ) {
          this.keepAwayExtraMs = Math.min(this.keepAwayExtraMs + 100, 500);
          this.beginSmoothTransition(this.profile.minSpeedDown, "slowdown");
        }
      }

      this.resetBufferRequestState();
      return this.getState(currentBufferMs);
    }

    // Determine speed bucket with hysteresis to prevent oscillation.
    // Entering a bucket requires crossing the outer threshold; leaving requires
    // the buffer recovering to 1.0× desired (wider dead-band than entry).
    let newBucket = this._speedBucket;
    if (this._speedBucket === "normal") {
      if (ratio < this.profile.speedDownThreshold) newBucket = "low";
      else if (ratio > this.profile.speedUpThreshold) newBucket = "high";
    } else if (this._speedBucket === "low") {
      if (ratio >= 1.0) newBucket = "normal";
      else if (ratio > this.profile.speedUpThreshold) newBucket = "high";
    } else if (this._speedBucket === "high") {
      if (ratio <= 1.0) newBucket = "normal";
      else if (ratio < this.profile.speedDownThreshold) newBucket = "low";
    }

    if (newBucket !== this._speedBucket) {
      this._speedBucket = newBucket;
      switch (newBucket) {
        case "low":
          // Try fast-forward first (upstream rawws.js parity), fall back to slowdown
          if (
            this.isLive &&
            playRateCurr !== "fast-forward" &&
            !this.requestingMoreBuffer &&
            this.tweakSpeed >= 1 &&
            serverCurrentMs !== undefined &&
            serverEndMs !== undefined &&
            serverCurrentMs < serverEndMs
          ) {
            this.requestingMoreBuffer = true;
            this.requestingMoreBufferAt = now;
            this.requestingMoreBufferBufferAt = currentBufferMs;
            this.requestingMoreBufferDesiredAt = desired;
            this.requestingMoreBufferServerCurrentAt = serverCurrentMs ?? null;
            this.requestingMoreBufferGotSetSpeed = false;
            this.requestingMoreBufferSawFastForward = false;
            this.requestFastForward(Math.max(desired, desired - currentBufferMs));
          } else {
            this.setTweakSpeed(this.profile.minSpeedDown, "slowdown");
          }
          if (ratio < 0.3 && this.isLive) {
            this.emit("underrun", undefined);
          }
          this.emit("bufferlow", { current: currentBufferMs, desired });
          break;
        case "high":
          if (this.isLive && playRateCurr !== "fast-forward") {
            this.setTweakSpeed(this.profile.maxSpeedUp, "catchup");
          }
          break;
        case "normal":
          if (this.tweakSpeed !== 1) {
            this.setTweakSpeed(1, "normal");
          }
          // Decay keepAwayExtraMs when buffer is healthy
          if (this.keepAwayExtraMs > 0) {
            this.keepAwayExtraMs = Math.max(0, this.keepAwayExtraMs - 50);
          }
          break;
      }
    } else if (
      // Proactive live catchup — in else to prevent sending fast_forward more
      // than once per evaluation (upstream rawws.js parity). Low-buffer
      // fast-forward above takes priority; this only fires when bucket stays normal.
      this._speedBucket === "normal" &&
      this.isLive &&
      playRateCurr !== "fast-forward" &&
      serverCurrentMs !== undefined &&
      serverEndMs !== undefined &&
      serverEndMs > serverCurrentMs &&
      serverEndMs - serverCurrentMs < this.liveCatchupThresholdMs &&
      serverEndMs - serverCurrentMs > Math.max(serverJitterMs * 1.1, serverJitterMs + 250) &&
      currentBufferMs - desired < 1000 &&
      now - this.lastLiveCatchup > this.liveCatchupCooldown
    ) {
      this.lastLiveCatchup = now;
      this.requestFastForward(this.liveCatchupRequestMs);
      this.emit("livecatchup", { fastForwardMs: this.liveCatchupRequestMs });
    }

    return this.getState(currentBufferMs);
  }

  /**
   * Update the dynamic buffer multiplier based on stutter count and jitter.
   * Stutters > 3 → double buffer. Low jitter & smooth → decrease.
   */
  private updateBufferMultiplier(): void {
    // Keep desired buffer deterministic (upstream-style).
    this.bufferMultiplier = 1;
  }

  /**
   * Get current sync state
   */
  getState(currentBufferMs?: number): SyncState {
    const buffer = currentBufferMs ?? 0;
    const desired = this.getDesiredBuffer();

    return {
      buffer: {
        current: buffer,
        desired,
        ratio: desired > 0 ? buffer / desired : 1,
      },
      jitter:
        this.jitterTracker.getMax() > 0
          ? {
              current: 0, // Would need per-frame tracking
              peak: 0,
              weighted: this.jitterTracker.getMax(),
            }
          : { current: 0, peak: 0, weighted: 0 },
      playbackSpeed: this.getCombinedSpeed(),
      serverTime: this.serverTime,
      serverDelay: this.getServerDelay(),
      avOffset: this.lastAvDrift,
    };
  }

  /**
   * Get combined playback speed (main * tweak)
   */
  getCombinedSpeed(): number {
    return this.mainSpeed * this.tweakSpeed;
  }

  /**
   * Set main playback speed (user-controlled)
   */
  setMainSpeed(speed: number): void {
    this.mainSpeed = speed;
    this.jitterTracker.setSpeed(speed);
    this.notifySpeedChange();
  }

  /**
   * Update server-reported delivery/play rate state from set_speed/on_time.
   * Upstream rawws.js updates jitter tracking from play_rate_curr.
   */
  setServerPlayRate(
    rate: number | "auto" | "fast-forward" | undefined,
    prevRate?: number | "auto" | "fast-forward",
    opts?: { fromSetSpeed?: boolean }
  ): void {
    if (rate !== undefined) {
      this.serverPlayRateCurr = rate;
      this.jitterTracker.setSpeed(rate);
    }

    if (this.requestingMoreBuffer && opts?.fromSetSpeed) {
      this.requestingMoreBufferGotSetSpeed = true;
      if (prevRate === "fast-forward") {
        this.requestingMoreBufferSawFastForward = true;
      } else {
        // Upstream rawws.js: set_speed without play_rate_prev === "fast-forward"
        // means there is no extra-buffer phase to evaluate.
        this.resetBufferRequestState();
      }
    }
  }

  /**
   * Apply a discrete tweak speed change (upstream-style).
   */
  private beginSmoothTransition(
    targetSpeed: number,
    reason: "catchup" | "slowdown" | "normal"
  ): void {
    this.setTweakSpeed(targetSpeed, reason);
  }

  /**
   * Set tweak speed immediately (bypasses smooth transition).
   */
  private setTweakSpeed(speed: number, reason: "catchup" | "slowdown" | "normal"): void {
    if (this.tweakSpeed !== speed) {
      this.tweakSpeed = speed;
      this.targetTweakSpeed = speed;
      if (this.rateTransitionTimer) {
        clearInterval(this.rateTransitionTimer);
        this.rateTransitionTimer = null;
      }
      this.notifySpeedChange();
      this.emit("speedchange", { speed: this.getCombinedSpeed(), reason });
    }
  }

  /**
   * Notify external listeners of speed change
   */
  private notifySpeedChange(): void {
    this.onSpeedChange?.(this.mainSpeed, this.tweakSpeed);
  }

  /**
   * Request additional data from server
   */
  private requestFastForward(ms: number): void {
    this.onFastForwardRequest?.(Math.max(0, ms));
  }

  // ============================================================================
  // Seek Management
  // ============================================================================

  /**
   * Start a seek operation
   * Returns a seek ID that can be used to check if seek is still active
   */
  startSeek(targetTimeMs: number): number {
    // Cancel any existing seek
    this.seekState.id++;

    this.seekState = {
      active: true,
      id: this.seekState.id,
      targetTime: targetTimeMs,
      startedAt: performance.now(),
    };

    // Reset jitter tracking on seek
    this.jitterTracker.reset();

    this.emit("seekstart", { seekId: this.seekState.id, time: targetTimeMs });

    return this.seekState.id;
  }

  /**
   * Check if a seek is still the active one
   */
  isSeekActive(seekId: number): boolean {
    return this.seekState.active && this.seekState.id === seekId;
  }

  /**
   * Complete a seek operation
   */
  completeSeek(seekId: number): void {
    if (this.seekState.id === seekId) {
      this.seekState.active = false;
      this.emit("seekcomplete", { seekId });
    }
  }

  /**
   * Cancel any active seek
   */
  cancelSeek(): void {
    if (this.seekState.active) {
      this.seekState.active = false;
    }
  }

  /**
   * Check if currently seeking
   */
  isSeeking(): boolean {
    return this.seekState.active;
  }

  // ============================================================================
  // Track Management
  // ============================================================================

  /**
   * Register a new track
   */
  addTrack(_trackIndex: number, _track: TrackInfo): void {
    // Jitter tracking will be initialized on first chunk
  }

  /**
   * Remove a track
   */
  removeTrack(trackIndex: number): void {
    this.jitterTracker.removeTrack(trackIndex);
  }

  // ============================================================================
  // Reset
  // ============================================================================

  /**
   * Reset all state
   */
  reset(): void {
    this.mainSpeed = 1;
    this.tweakSpeed = 1;
    this.targetTweakSpeed = 1;
    this.serverPlayRateCurr = "auto";
    this.serverDelays = [];
    this.serverTime = 0;
    this.localTimeAtServerUpdate = 0;
    this.lastLiveCatchup = 0;
    this.keepAwayExtraMs = 0;
    this._speedBucket = "normal";
    this.stutterCount = 0;
    this.bufferMultiplier = 1;
    this.resetBufferRequestState();
    this.jitterSamples = [];
    this.lastAvDrift = 0;
    this.jitterTracker.reset();

    if (this.rateTransitionTimer) {
      clearInterval(this.rateTransitionTimer);
      this.rateTransitionTimer = null;
    }
    if (this.stutterResetTimer) {
      clearTimeout(this.stutterResetTimer);
      this.stutterResetTimer = null;
    }

    this.seekState = {
      active: false,
      id: 0,
      targetTime: 0,
      startedAt: 0,
    };
  }

  private resetBufferRequestState(): void {
    this.requestingMoreBuffer = false;
    this.requestingMoreBufferAt = 0;
    this.requestingMoreBufferBufferAt = 0;
    this.requestingMoreBufferDesiredAt = 0;
    this.requestingMoreBufferServerCurrentAt = null;
    this.requestingMoreBufferGotSetSpeed = false;
    this.requestingMoreBufferSawFastForward = false;
  }

  // ============================================================================
  // Event Emitter
  // ============================================================================

  on<K extends keyof SyncControllerEvents>(event: K, listener: EventListener<K>): void {
    if (!this.listeners.has(event)) {
      this.listeners.set(event, new Set());
    }
    this.listeners.get(event)!.add(listener);
  }

  off<K extends keyof SyncControllerEvents>(event: K, listener: EventListener<K>): void {
    this.listeners.get(event)?.delete(listener);
  }

  private emit<K extends keyof SyncControllerEvents>(
    event: K,
    data: SyncControllerEvents[K]
  ): void {
    const eventListeners = this.listeners.get(event);
    if (eventListeners) {
      for (const listener of eventListeners) {
        try {
          listener(data);
        } catch (err) {
          console.error(`Error in ${event} listener:`, err);
        }
      }
    }
  }
}
