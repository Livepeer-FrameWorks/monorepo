/**
 * FrameWorksPlayer.ts
 *
 * Vanilla JavaScript wrapper for PlayerController.
 * Use this class in non-React environments (Svelte, Vue, plain HTML, etc.)
 *
 * @example
 * ```typescript
 * import { FrameWorksPlayer } from '@livepeer-frameworks/player-core/vanilla';
 * import '@livepeer-frameworks/player-core/player.css';
 *
 * const player = new FrameWorksPlayer('#player', {
 *   contentId: 'pk_...',
 *   contentType: 'live',
 *   gatewayUrl: 'https://gateway.example.com/graphql',
 *   onStateChange: (state) => console.log('State:', state),
 * });
 *
 * // Control playback
 * player.play();
 * player.setVolume(0.5);
 *
 * // Clean up
 * player.destroy();
 * ```
 */

import {
  PlayerController,
  PlayerControllerConfig,
  PlayerControllerEvents,
} from "../core/PlayerController";
import type {
  PlayerState,
  PlayerStateContext,
  StreamState,
  ContentEndpoints,
  ContentType,
} from "../types";

// ============================================================================
// Types
// ============================================================================

export interface FrameWorksPlayerOptions {
  /** Content identifier (stream name) */
  contentId: string;
  /** Content type */
  contentType?: ContentType;

  /** Pre-resolved endpoints (skip gateway) */
  endpoints?: ContentEndpoints;

  /** Gateway URL (required if endpoints not provided) */
  gatewayUrl?: string;
  /** Auth token for private streams */
  authToken?: string;

  /** Playback options */
  autoplay?: boolean;
  muted?: boolean;
  controls?: boolean;
  poster?: string;

  /** Debug logging */
  debug?: boolean;

  // Event callbacks
  /** Called when player state changes */
  onStateChange?: (state: PlayerState, context?: PlayerStateContext) => void;
  /** Called when stream state changes (for live streams) */
  onStreamStateChange?: (state: StreamState) => void;
  /** Called on time update during playback */
  onTimeUpdate?: (currentTime: number, duration: number) => void;
  /** Called on error */
  onError?: (error: string) => void;
  /** Called when player is ready */
  onReady?: (videoElement: HTMLVideoElement) => void;
}

// Legacy config format for backward compatibility with Svelte wrapper
interface LegacyConfig {
  contentId: string;
  contentType?: ContentType;
  thumbnailUrl?: string | null;
  options?: {
    gatewayUrl?: string;
    autoplay?: boolean;
    muted?: boolean;
    controls?: boolean;
    debug?: boolean;
    authToken?: string;
  };
}

// ============================================================================
// FrameWorksPlayer Class
// ============================================================================

/**
 * Vanilla JavaScript player class.
 *
 * This is a thin wrapper around PlayerController that provides a
 * constructor-based API suitable for non-React frameworks.
 */
export class FrameWorksPlayer {
  private controller: PlayerController;
  private container: HTMLElement;
  private cleanupFns: Array<() => void> = [];
  private isDestroyed: boolean = false;

  /**
   * Create a new player instance.
   *
   * @param container - DOM element or CSS selector to mount the player
   * @param options - Player options and callbacks
   */
  constructor(
    container: HTMLElement | string | null,
    options: FrameWorksPlayerOptions | LegacyConfig
  ) {
    // Resolve container
    if (typeof container === "string") {
      this.container = document.querySelector(container) as HTMLElement;
    } else if (container instanceof HTMLElement) {
      this.container = container;
    } else {
      throw new Error("Container element not found or invalid");
    }

    if (!this.container) {
      throw new Error("Container element not found");
    }

    // Normalize options (support both new and legacy config formats)
    const normalizedOptions = this.normalizeOptions(options);

    // Create controller config
    const config: PlayerControllerConfig = {
      contentId: normalizedOptions.contentId,
      contentType: normalizedOptions.contentType,
      endpoints: normalizedOptions.endpoints,
      gatewayUrl: normalizedOptions.gatewayUrl,
      authToken: normalizedOptions.authToken,
      autoplay: normalizedOptions.autoplay ?? true,
      muted: normalizedOptions.muted ?? true,
      controls: normalizedOptions.controls ?? true,
      poster: normalizedOptions.poster,
      debug: normalizedOptions.debug,
    };

    // Create controller
    this.controller = new PlayerController(config);

    // Wire up callbacks
    this.setupCallbacks(normalizedOptions);

    // Auto-attach to container
    this.controller.attach(this.container).catch((err) => {
      console.error("[FrameWorksPlayer] Failed to attach:", err);
      normalizedOptions.onError?.(err instanceof Error ? err.message : String(err));
    });
  }

  // ============================================================================
  // Playback Control (delegated to controller)
  // ============================================================================

  /** Start playback */
  play(): Promise<void> {
    return this.controller.play();
  }

  /** Pause playback */
  pause(): void {
    this.controller.pause();
  }

  /** Seek to time in seconds */
  seek(time: number): void {
    this.controller.seek(time);
  }

  /** Set volume (0-1) */
  setVolume(volume: number): void {
    this.controller.setVolume(volume);
  }

  /** Set muted state */
  setMuted(muted: boolean): void {
    this.controller.setMuted(muted);
  }

  /** Set playback rate */
  setPlaybackRate(rate: number): void {
    this.controller.setPlaybackRate(rate);
  }

  /** Jump to live edge (for live streams) */
  jumpToLive(): void {
    this.controller.jumpToLive();
  }

  /** Request fullscreen */
  requestFullscreen(): Promise<void> {
    return this.controller.requestFullscreen();
  }

  /** Request Picture-in-Picture */
  requestPiP(): Promise<void> {
    return this.controller.requestPiP();
  }

  // ============================================================================
  // State Getters (delegated to controller)
  // ============================================================================

  /** Get current player state */
  getState(): PlayerState {
    return this.controller.getState();
  }

  /** Get current stream state (for live streams) */
  getStreamState(): StreamState | null {
    return this.controller.getStreamState();
  }

  /** Get video element (null if not ready) */
  getVideoElement(): HTMLVideoElement | null {
    return this.controller.getVideoElement();
  }

  /** Check if player is ready */
  isReady(): boolean {
    return this.controller.isReady();
  }

  /** Get current time in seconds */
  getCurrentTime(): number {
    return this.controller.getCurrentTime();
  }

  /** Get duration in seconds */
  getDuration(): number {
    return this.controller.getDuration();
  }

  /** Check if paused */
  isPaused(): boolean {
    return this.controller.isPaused();
  }

  /** Check if muted */
  isMuted(): boolean {
    return this.controller.isMuted();
  }

  // ============================================================================
  // Advanced Control
  // ============================================================================

  /** Retry playback after error */
  retry(): Promise<void> {
    return this.controller.retry();
  }

  /** Get content metadata (title, description, duration, viewers, etc.) */
  getMetadata() {
    return this.controller.getMetadata();
  }

  /** Get playback statistics */
  getStats(): Promise<unknown> {
    return this.controller.getStats();
  }

  /** Get current latency (for live streams) */
  getLatency(): Promise<unknown> {
    return this.controller.getLatency();
  }

  // ============================================================================
  // Event Subscription
  // ============================================================================

  /**
   * Subscribe to a player event.
   * @param event - Event name
   * @param listener - Callback function
   * @returns Unsubscribe function
   */
  on<K extends keyof PlayerControllerEvents>(
    event: K,
    listener: (data: PlayerControllerEvents[K]) => void
  ): () => void {
    return this.controller.on(event, listener);
  }

  // ============================================================================
  // Cleanup
  // ============================================================================

  /** Destroy the player and clean up resources */
  destroy(): void {
    if (this.isDestroyed) return;

    this.cleanupFns.forEach((fn) => {
      try {
        fn();
      } catch {}
    });
    this.cleanupFns = [];

    this.controller.destroy();
    this.isDestroyed = true;
  }

  // ============================================================================
  // Private Methods
  // ============================================================================

  private normalizeOptions(
    options: FrameWorksPlayerOptions | LegacyConfig
  ): FrameWorksPlayerOptions {
    // Check if it's legacy format (has nested `options` property)
    if ("options" in options && typeof options.options === "object") {
      const legacy = options as LegacyConfig;
      return {
        contentId: legacy.contentId,
        contentType: legacy.contentType,
        poster: legacy.thumbnailUrl || undefined,
        gatewayUrl: legacy.options?.gatewayUrl,
        authToken: legacy.options?.authToken,
        autoplay: legacy.options?.autoplay,
        muted: legacy.options?.muted,
        controls: legacy.options?.controls,
        debug: legacy.options?.debug,
      };
    }

    return options as FrameWorksPlayerOptions;
  }

  private setupCallbacks(options: FrameWorksPlayerOptions): void {
    if (options.onStateChange) {
      const unsub = this.controller.on("stateChange", ({ state, context }) => {
        options.onStateChange!(state, context);
      });
      this.cleanupFns.push(unsub);
    }

    if (options.onStreamStateChange) {
      const unsub = this.controller.on("streamStateChange", ({ state }) => {
        options.onStreamStateChange!(state);
      });
      this.cleanupFns.push(unsub);
    }

    if (options.onTimeUpdate) {
      const unsub = this.controller.on("timeUpdate", ({ currentTime, duration }) => {
        options.onTimeUpdate!(currentTime, duration);
      });
      this.cleanupFns.push(unsub);
    }

    if (options.onError) {
      const unsub = this.controller.on("error", ({ error }) => {
        options.onError!(error);
      });
      this.cleanupFns.push(unsub);
    }

    if (options.onReady) {
      const unsub = this.controller.on("ready", ({ videoElement }) => {
        options.onReady!(videoElement);
      });
      this.cleanupFns.push(unsub);
    }
  }
}

export default FrameWorksPlayer;
