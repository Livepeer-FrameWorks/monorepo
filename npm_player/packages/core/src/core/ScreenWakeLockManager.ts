/**
 * ScreenWakeLockManager - Prevents device sleep during video playback
 *
 * Uses the Screen Wake Lock API to keep the screen awake during:
 * - Fullscreen video playback
 * - Active video playback (optional)
 *
 * Gracefully falls back to no-op on unsupported browsers.
 */

export interface ScreenWakeLockConfig {
  /** Acquire wake lock on any playback, not just fullscreen (default: false) */
  acquireOnPlay?: boolean;
  /** Callback when wake lock is acquired */
  onAcquire?: () => void;
  /** Callback when wake lock is released */
  onRelease?: () => void;
  /** Callback on error */
  onError?: (error: Error) => void;
}

export class ScreenWakeLockManager {
  private wakeLock: WakeLockSentinel | null = null;
  private config: ScreenWakeLockConfig;
  private isSupported: boolean;
  private isPlaying = false;
  private isFullscreen = false;
  private isDestroyed = false;

  // Bound handlers for visibility change
  private boundVisibilityChange: () => void;

  constructor(config: ScreenWakeLockConfig = {}) {
    this.config = config;
    this.isSupported = "wakeLock" in navigator;

    this.boundVisibilityChange = this.handleVisibilityChange.bind(this);

    // Re-acquire wake lock when page becomes visible again
    if (this.isSupported) {
      document.addEventListener("visibilitychange", this.boundVisibilityChange);
    }
  }

  /**
   * Check if Screen Wake Lock API is supported
   */
  static isSupported(): boolean {
    return "wakeLock" in navigator;
  }

  /**
   * Update playing state
   */
  setPlaying(isPlaying: boolean): void {
    if (this.isDestroyed) return;

    this.isPlaying = isPlaying;
    this.updateWakeLock();
  }

  /**
   * Update fullscreen state
   */
  setFullscreen(isFullscreen: boolean): void {
    if (this.isDestroyed) return;

    this.isFullscreen = isFullscreen;
    this.updateWakeLock();
  }

  /**
   * Check if wake lock is currently held
   */
  isHeld(): boolean {
    return this.wakeLock !== null;
  }

  /**
   * Manually acquire wake lock
   */
  async acquire(): Promise<void> {
    if (this.isDestroyed) return;
    if (!this.isSupported) return;
    if (this.wakeLock) return;

    try {
      this.wakeLock = await navigator.wakeLock.request("screen");
      this.wakeLock.addEventListener("release", this.handleRelease.bind(this));
      this.config.onAcquire?.();
    } catch (err) {
      // Wake lock request can fail if:
      // - Document is not visible
      // - Low battery mode
      // - Permission denied
      this.config.onError?.(err instanceof Error ? err : new Error(String(err)));
    }
  }

  /**
   * Manually release wake lock
   */
  release(): void {
    if (this.wakeLock) {
      this.wakeLock.release().catch(() => {
        // Ignore release errors
      });
      this.wakeLock = null;
    }
  }

  /**
   * Destroy the manager and release wake lock
   */
  destroy(): void {
    if (this.isDestroyed) return;
    this.isDestroyed = true;

    this.release();

    if (this.isSupported) {
      document.removeEventListener("visibilitychange", this.boundVisibilityChange);
    }
  }

  /**
   * Update wake lock based on current state
   */
  private updateWakeLock(): void {
    const shouldHold = this.isPlaying && (this.isFullscreen || this.config.acquireOnPlay);

    if (shouldHold && !this.wakeLock) {
      this.acquire();
    } else if (!shouldHold && this.wakeLock) {
      this.release();
    }
  }

  /**
   * Handle wake lock release event
   */
  private handleRelease(): void {
    this.wakeLock = null;
    this.config.onRelease?.();

    // Try to re-acquire if conditions still met
    if (!this.isDestroyed) {
      this.updateWakeLock();
    }
  }

  /**
   * Handle visibility change - re-acquire if page becomes visible
   */
  private handleVisibilityChange(): void {
    if (document.visibilityState === "visible" && !this.wakeLock) {
      this.updateWakeLock();
    }
  }
}

export default ScreenWakeLockManager;
