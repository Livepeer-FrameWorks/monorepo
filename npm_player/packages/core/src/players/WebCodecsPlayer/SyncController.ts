/**
 * SyncController - Buffer Management & Playback Timing
 *
 * Orchestrates:
 * - Buffer level monitoring
 * - Adaptive playback speed (catchup/slowdown)
 * - Jitter tracking integration
 * - Server delay estimation
 * - Seek coordination
 *
 * Based on legacy rawws.js FrameTiming + buffer management with improvements:
 * - Post-decode drift correction
 * - Better seek cancellation
 * - TypeScript types
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

  // Buffer state
  private lastBufferCheck = 0;
  private bufferCheckInterval = 100; // ms

  // Server delay tracking
  private serverDelays: number[] = [];
  private maxServerDelaysSamples = 3;

  // Live catchup tracking
  // Reference: rawws.js:489-503 - proactively request 5s fast forward
  private lastLiveCatchup = 0;
  private liveCatchupCooldown = 2000; // Minimum ms between catchup requests
  private liveCatchupThresholdMs = 5000; // Request catchup when within 5s of live
  private liveCatchupRequestMs = 5000; // Request 5 seconds of fast forward

  // Time tracking
  private serverTime = 0;
  private localTimeAtServerUpdate = 0;

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
    } = {}
  ) {
    this.profile = options.profile ?? getLatencyProfile("low");
    this.isLive = options.isLive ?? true;
    this.onSpeedChange = options.onSpeedChange;
    this.onFastForwardRequest = options.onFastForwardRequest;

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
   * Record a chunk arrival for jitter tracking
   */
  recordChunkArrival(trackIndex: number, mediaTimeMs: number): void {
    this.jitterTracker.addChunk(trackIndex, mediaTimeMs);
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
   * Calculate desired buffer size based on profile + jitter + server delay
   * Matches MistServer mews.js pattern with Chrome-specific handling
   */
  getDesiredBuffer(): number {
    // Chrome needs larger base buffer (per mews.js:482)
    const isChrome =
      typeof navigator !== "undefined" &&
      /Chrome/.test(navigator.userAgent) &&
      !/Edge|Edg/.test(navigator.userAgent);
    const baseBuffer = isChrome ? 1000 : 100;

    const serverDelay = this.getServerDelay();
    const jitter = this.jitterTracker.getMax() * this.profile.jitterMultiplier;

    // Match mews.js formula: Math.max(baseBuffer + serverDelay, serverDelay * 2) + jitter
    const liveBuffer = Math.max(baseBuffer + serverDelay, serverDelay * 2) + jitter;

    // VoD gets extra buffer (mews.js:480)
    return this.isLive ? liveBuffer : liveBuffer + 2000;
  }

  /**
   * Evaluate buffer state and adjust playback speed if needed
   *
   * @param currentBufferMs - Current buffer level in milliseconds
   * @returns Updated sync state
   */
  evaluateBuffer(currentBufferMs: number): SyncState {
    const now = performance.now();

    // Rate limit buffer checks
    if (now - this.lastBufferCheck < this.bufferCheckInterval) {
      return this.getState(currentBufferMs);
    }
    this.lastBufferCheck = now;

    const desired = this.getDesiredBuffer();
    const ratio = currentBufferMs / desired;
    const jitterMs = this.jitterTracker.getMax();

    // Proactive live catchup logic
    // Reference: rawws.js:489-503 - request 5s fast forward when close to live
    // distanceToLive = how much buffer we have (close to live = small buffer)
    // Conditions:
    //   - Within liveCatchupThreshold of live edge (small buffer)
    //   - Buffer is more than jitter + safety margin (not stalled)
    //   - Buffer is less than 1s above desired
    //   - Cooldown period has passed
    if (
      this.isLive &&
      currentBufferMs < this.liveCatchupThresholdMs &&
      currentBufferMs > Math.max(jitterMs * 1.1, jitterMs + 250) &&
      currentBufferMs - desired < 1000 &&
      now - this.lastLiveCatchup > this.liveCatchupCooldown
    ) {
      this.lastLiveCatchup = now;
      this.requestFastForward(this.liveCatchupRequestMs);
      this.emit("livecatchup", { fastForwardMs: this.liveCatchupRequestMs });
    }

    // Determine if speed adjustment needed
    if (ratio > this.profile.speedUpThreshold && this.isLive) {
      // Buffer too high - speed up to catch up to live edge
      this.setTweakSpeed(this.profile.maxSpeedUp, "catchup");
    } else if (ratio < this.profile.speedDownThreshold) {
      // Buffer too low - slow down to build buffer
      this.setTweakSpeed(this.profile.minSpeedDown, "slowdown");

      // Request additional data if critically low
      if (ratio < 0.3 && this.isLive) {
        this.requestFastForward(desired - currentBufferMs);
        this.emit("underrun", undefined);
      }

      this.emit("bufferlow", { current: currentBufferMs, desired });
    } else {
      // Buffer in acceptable range - return to normal speed
      if (this.tweakSpeed !== 1) {
        this.setTweakSpeed(1, "normal");
      }
    }

    return this.getState(currentBufferMs);
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
      avOffset: 0, // Server handles A/V sync via timestamp+offset
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
   * Set tweak speed (automatic adjustment)
   */
  private setTweakSpeed(speed: number, reason: "catchup" | "slowdown" | "normal"): void {
    if (this.tweakSpeed !== speed) {
      this.tweakSpeed = speed;
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
    this.serverDelays = [];
    this.serverTime = 0;
    this.localTimeAtServerUpdate = 0;
    this.lastLiveCatchup = 0;
    this.jitterTracker.reset();
    this.seekState = {
      active: false,
      id: 0,
      targetTime: 0,
      startedAt: 0,
    };
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
