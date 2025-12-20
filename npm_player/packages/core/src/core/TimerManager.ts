/**
 * TimerManager - Centralized timer management for memory leak prevention
 *
 * Tracks all setTimeout/setInterval calls and provides bulk cleanup.
 * Based on MistMetaPlayer's MistVideo.timers pattern.
 *
 * Usage:
 * ```ts
 * const timers = new TimerManager();
 *
 * // Start a timeout
 * const id = timers.start(() => console.log('fired'), 1000);
 *
 * // Start an interval
 * const intervalId = timers.startInterval(() => console.log('tick'), 500);
 *
 * // Stop a specific timer
 * timers.stop(id);
 *
 * // Stop all timers (on cleanup/destroy)
 * timers.stopAll();
 * ```
 */

interface TimerEntry {
  /** Timer ID from setTimeout/setInterval */
  id: ReturnType<typeof setTimeout>;
  /** Expected end time (for timeouts) */
  endTime: number;
  /** Whether this is an interval */
  isInterval: boolean;
  /** Optional label for debugging */
  label?: string;
}

export class TimerManager {
  private timers: Map<number, TimerEntry> = new Map();
  private nextId = 1;
  private debug: boolean;

  constructor(options?: { debug?: boolean }) {
    this.debug = options?.debug ?? false;
  }

  /**
   * Start a timeout
   * @param callback Function to call after delay
   * @param delay Delay in milliseconds
   * @param label Optional label for debugging
   * @returns Timer ID (internal, not the native timeout ID)
   */
  start(callback: () => void, delay: number, label?: string): number {
    const internalId = this.nextId++;
    const endTime = Date.now() + delay;

    const nativeId = setTimeout(() => {
      this.timers.delete(internalId);
      try {
        callback();
      } catch (e) {
        console.error('[TimerManager] Callback error:', e);
      }
    }, delay);

    this.timers.set(internalId, {
      id: nativeId,
      endTime,
      isInterval: false,
      label,
    });

    if (this.debug) {
      console.debug(`[TimerManager] Started timeout ${internalId}${label ? ` (${label})` : ''} for ${delay}ms`);
    }

    return internalId;
  }

  /**
   * Start an interval
   * @param callback Function to call repeatedly
   * @param interval Interval in milliseconds
   * @param label Optional label for debugging
   * @returns Timer ID (internal, not the native interval ID)
   */
  startInterval(callback: () => void, interval: number, label?: string): number {
    const internalId = this.nextId++;

    const nativeId = setInterval(() => {
      try {
        callback();
      } catch (e) {
        console.error('[TimerManager] Interval callback error:', e);
      }
    }, interval);

    this.timers.set(internalId, {
      id: nativeId,
      endTime: Infinity, // Intervals don't have an end time
      isInterval: true,
      label,
    });

    if (this.debug) {
      console.debug(`[TimerManager] Started interval ${internalId}${label ? ` (${label})` : ''} every ${interval}ms`);
    }

    return internalId;
  }

  /**
   * Stop a specific timer
   * @param internalId The timer ID returned by start() or startInterval()
   */
  stop(internalId: number): boolean {
    const entry = this.timers.get(internalId);
    if (!entry) {
      return false;
    }

    if (entry.isInterval) {
      clearInterval(entry.id);
    } else {
      clearTimeout(entry.id);
    }

    this.timers.delete(internalId);

    if (this.debug) {
      console.debug(`[TimerManager] Stopped ${entry.isInterval ? 'interval' : 'timeout'} ${internalId}${entry.label ? ` (${entry.label})` : ''}`);
    }

    return true;
  }

  /**
   * Stop all active timers
   * Call this on component unmount/destroy to prevent memory leaks
   */
  stopAll(): void {
    const count = this.timers.size;

    for (const [internalId, entry] of this.timers) {
      if (entry.isInterval) {
        clearInterval(entry.id);
      } else {
        clearTimeout(entry.id);
      }
    }

    this.timers.clear();

    if (this.debug && count > 0) {
      console.debug(`[TimerManager] Stopped all ${count} timers`);
    }
  }

  /**
   * Get count of active timers
   */
  get activeCount(): number {
    return this.timers.size;
  }

  /**
   * Check if a timer is active
   */
  isActive(internalId: number): boolean {
    return this.timers.has(internalId);
  }

  /**
   * Get remaining time for a timeout (0 for intervals or expired)
   */
  getRemainingTime(internalId: number): number {
    const entry = this.timers.get(internalId);
    if (!entry || entry.isInterval) {
      return 0;
    }
    return Math.max(0, entry.endTime - Date.now());
  }

  /**
   * Get debug info about all active timers
   */
  getDebugInfo(): Array<{ id: number; type: 'timeout' | 'interval'; label?: string; remainingMs?: number }> {
    const info: Array<{ id: number; type: 'timeout' | 'interval'; label?: string; remainingMs?: number }> = [];

    for (const [internalId, entry] of this.timers) {
      info.push({
        id: internalId,
        type: entry.isInterval ? 'interval' : 'timeout',
        label: entry.label,
        remainingMs: entry.isInterval ? undefined : Math.max(0, entry.endTime - Date.now()),
      });
    }

    return info;
  }

  /**
   * Cleanup - alias for stopAll()
   */
  destroy(): void {
    this.stopAll();
  }
}

export default TimerManager;
