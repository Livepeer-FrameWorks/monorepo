/**
 * Common Player Interface
 *
 * All player implementations must implement this interface to ensure
 * consistent behavior and enable the PlayerManager selection system
 */

import { appendUrlParams, parseUrlParams, stripUrlParams } from "./UrlUtils";

export interface StreamSource {
  url: string;
  type: string;
  index?: number;
  streamName?: string;
  mistPlayerUrl?: string;
}

export interface StreamTrack {
  type: "video" | "audio" | "meta";
  codec: string;
  codecstring?: string;
  init?: string;
  /** Track index from MistServer (used for binary chunk routing) */
  idx?: number;
  // Video-specific
  width?: number;
  height?: number;
  fpks?: number; // frames per kilosecond
  // Audio-specific
  channels?: number;
  rate?: number; // sample rate
  size?: number; // bits per sample
  // Timing (from MistServer metadata — defines seekable window for live)
  firstms?: number;
  lastms?: number;
}

export interface StreamInfo {
  source: StreamSource[];
  meta: {
    tracks: StreamTrack[];
    /** MistServer SHM buffer window size in milliseconds */
    buffer_window?: number;
  };
  type?: "live" | "vod";
}

export interface PlayerOptions {
  autoplay?: boolean;
  muted?: boolean;
  controls?: boolean;
  loop?: boolean;
  poster?: string;
  width?: number;
  height?: number;
  /** Enable dev mode - for Legacy player, uses MistServer's dev skin with source selection */
  devMode?: boolean;
  /** Enable debug logging in player implementations */
  debug?: boolean;
  onReady?: (element: HTMLVideoElement) => void;
  onError?: (error: string | Error) => void;
  onPlay?: () => void;
  onPause?: () => void;
  onEnded?: () => void;
  onTimeUpdate?: (currentTime: number) => void;
  // New callbacks for buffering/state management
  onWaiting?: () => void;
  onPlaying?: () => void;
  onCanPlay?: () => void;
  onDurationChange?: (duration: number) => void;
  /** HLS.js configuration override (merged with defaults) */
  hlsConfig?: Record<string, unknown>;
  /** DASH.js configuration override (merged with defaults) */
  dashConfig?: Record<string, unknown>;
  /** Video.js VHS configuration override (merged with defaults) */
  vhsConfig?: Record<string, unknown>;
}

export interface PlayerCapability {
  /** Player name for display */
  name: string;
  /** Unique identifier */
  shortname: string;
  /** Priority (lower number = higher priority) */
  priority: number;
  /** MIME types this player can handle */
  mimes: string[];
  /** Per-mime notes on browser support / known limitations (shown in dev panel) */
  notes?: Record<string, string>;
}

export interface PlayerEvents {
  ready: HTMLVideoElement;
  error: string | Error;
  play: void;
  pause: void;
  ended: void;
  seeked: void;
  timeupdate: number;
  /** Request to reload the player (e.g., Firefox segment error recovery) */
  reloadrequested: { reason: string };
  /** Seekable range changed (values in ms) */
  seekablechange: { start: number; end: number; bufferWindow: number };
}

/**
 * Error severity levels for the tiered error handling system.
 *
 * Tier 1 (TRANSIENT): Silent retry, no UI - network timeouts, brief stalls
 * Tier 2 (RECOVERABLE): Protocol swap with toast - alternatives exist
 * Tier 3 (DEGRADED): Quality drop with toast - playback continues at lower quality
 * Tier 4 (FATAL): Blocking modal - all options exhausted
 */
export enum ErrorSeverity {
  /** Transient issues that self-resolve. User never sees UI. */
  TRANSIENT = 1,
  /** Current protocol failed but alternatives exist. Shows toast on swap. */
  RECOVERABLE = 2,
  /** Quality degraded but playback continues. Shows informational toast. */
  DEGRADED = 3,
  /** Cannot continue playback. Shows blocking error modal. */
  FATAL = 4,
}

/**
 * Error codes for classification. Maps to specific recovery strategies.
 */
export enum ErrorCode {
  // Tier 1: Silent recovery
  NETWORK_TIMEOUT = "NETWORK_TIMEOUT",
  WEBSOCKET_DISCONNECT = "WEBSOCKET_DISCONNECT",
  SEGMENT_LOAD_ERROR = "SEGMENT_LOAD_ERROR",
  ICE_DISCONNECTED = "ICE_DISCONNECTED",
  BUFFER_UNDERRUN = "BUFFER_UNDERRUN",
  CODEC_DECODE_ERROR = "CODEC_DECODE_ERROR",

  // Tier 2: Protocol swap
  PROTOCOL_UNSUPPORTED = "PROTOCOL_UNSUPPORTED",
  CODEC_INCOMPATIBLE = "CODEC_INCOMPATIBLE",
  ICE_FAILED = "ICE_FAILED",
  MANIFEST_STALE = "MANIFEST_STALE",
  PLAYER_INIT_FAILED = "PLAYER_INIT_FAILED",

  // Tier 3: Quality degraded
  QUALITY_DROPPED = "QUALITY_DROPPED",
  BANDWIDTH_LIMITED = "BANDWIDTH_LIMITED",

  // Tier 4: Fatal
  ALL_PROTOCOLS_EXHAUSTED = "ALL_PROTOCOLS_EXHAUSTED",
  ALL_PROTOCOLS_BLACKLISTED = "ALL_PROTOCOLS_BLACKLISTED",
  STREAM_OFFLINE = "STREAM_OFFLINE",
  AUTH_REQUIRED = "AUTH_REQUIRED",
  GEO_BLOCKED = "GEO_BLOCKED",
  DRM_ERROR = "DRM_ERROR",
  CONTENT_UNAVAILABLE = "CONTENT_UNAVAILABLE",
  UNKNOWN = "UNKNOWN",
}

/**
 * Classified error with severity and recovery metadata.
 * Used by ErrorClassifier to track retry state and decide next action.
 */
export interface ClassifiedError {
  /** Severity tier determining UI behavior */
  severity: ErrorSeverity;
  /** Specific error code for recovery strategy lookup */
  code: ErrorCode;
  /** Human-readable error message */
  message: string;
  /** Number of retries remaining for this error type */
  retriesRemaining: number;
  /** Number of alternative protocols/players remaining */
  alternativesRemaining: number;
  /** Original error if available */
  originalError?: Error | string;
  /** Timestamp when error occurred */
  timestamp: number;
  /** Diagnostic details for operators/debugging */
  details?: {
    incompatibilityReasons?: string[];
    blacklistedProtocols?: string[];
    originalCode?: ErrorCode;
    originalMessage?: string;
  };
}

/**
 * Events emitted by the error handling system.
 * UI layers listen to these for toast/modal display.
 */
export interface ErrorHandlingEvents {
  /** Silent recovery attempted (Tier 1) - for telemetry only */
  recoveryAttempted: {
    code: ErrorCode;
    attempt: number;
    maxAttempts: number;
  };
  /** Protocol or player swapped (Tier 2) - shows toast */
  protocolSwapped: {
    fromPlayer: string;
    toPlayer: string;
    fromProtocol: string;
    toProtocol: string;
    reason: string;
  };
  /** Quality changed (Tier 3) - shows toast */
  qualityChanged: {
    direction: "up" | "down";
    reason: string;
  };
  /** All recovery options exhausted (Tier 4) - shows modal */
  playbackFailed: ClassifiedError;
}

/**
 * Base interface all players must implement
 */
export interface IPlayer {
  /** Player metadata */
  readonly capability: PlayerCapability;

  /**
   * Check if this player supports the given MIME type
   */
  isMimeSupported(mimetype: string): boolean;

  /**
   * Check if this player can play in the current browser environment
   * @param mimetype - MIME type to test
   * @param source - Source information
   * @param streamInfo - Stream metadata
   * @returns false if not supported, true if supported (no track info),
   *          or array of supported track types
   */
  isBrowserSupported(
    mimetype: string,
    source: StreamSource,
    streamInfo: StreamInfo
  ): boolean | string[];

  /**
   * Initialize the player with given source and options
   * @param container - Container element to render in
   * @param source - Source to play
   * @param options - Player options
   * @param streamInfo - Full stream metadata (optional, for players that need track details)
   * @returns Promise resolving to video element
   */
  initialize(
    container: HTMLElement,
    source: StreamSource,
    options: PlayerOptions,
    streamInfo?: StreamInfo
  ): Promise<HTMLVideoElement>;

  /**
   * Clean up and destroy the player.
   * May be async if cleanup requires network requests (e.g., WHEP session DELETE).
   */
  destroy(): void | Promise<void>;

  /**
   * Get the underlying video element (if available)
   */
  getVideoElement(): HTMLVideoElement | null;

  /**
   * Set video size
   */
  setSize?(width: number, height: number): void;

  /**
   * Add event listener
   */
  on<K extends keyof PlayerEvents>(event: K, listener: (data: PlayerEvents[K]) => void): void;

  /**
   * Remove event listener
   */
  off<K extends keyof PlayerEvents>(event: K, listener: (data: PlayerEvents[K]) => void): void;

  /**
   * Get current playback state
   */
  /** Get current playback position in milliseconds */
  getCurrentTime?(): number;
  /** Get content duration in milliseconds */
  getDuration?(): number;
  isPaused?(): boolean;
  isMuted?(): boolean;
  /** Optional: provide an override seekable range (milliseconds) */
  getSeekableRange?(): { start: number; end: number } | null;
  /** Optional: push authoritative seekable range hints from controller logic (milliseconds) */
  setSeekableRangeHint?(range: { start: number; end: number } | null): void;
  /** Optional: provide buffered ranges override */
  getBufferedRanges?(): TimeRanges | null;

  /**
   * Control playback
   */
  play?(): Promise<void>;
  pause?(): void;
  /** Seek to position in milliseconds */
  seek?(time: number): void;
  setVolume?(volume: number): void;
  setMuted?(muted: boolean): void;
  setPlaybackRate?(rate: number): void;

  // Optional: captions/text tracks
  getTextTracks?(): Array<{ id: string; label: string; lang?: string; active: boolean }>;
  selectTextTrack?(id: string | null): void;

  // Optional: quality/level selection
  getQualities?(): Array<{
    id: string;
    label: string;
    bitrate?: number;
    width?: number;
    height?: number;
    isAuto?: boolean;
    active?: boolean;
  }>;
  selectQuality?(id: string): void; // use 'auto' to enable ABR
  getCurrentQuality?(): string | null;

  // Optional: audio track selection
  getAudioTracks?(): Array<{ id: string; label: string; lang?: string; active: boolean }>;
  selectAudioTrack?(id: string): void;

  // Optional: live edge helpers
  isLive?(): boolean;
  jumpToLive?(): void;
  /** Optional: frame step (direction -1/1, optional step seconds) */
  frameStep?(direction: -1 | 1, seconds?: number): void;

  // Optional: PiP
  requestPiP?(): Promise<void>;

  /**
   * Optional: Retrieve player-specific stats (e.g., WebRTC inbound-rtp)
   */
  getStats?(): Promise<any>;

  /**
   * Optional: Retrieve approximate playback latency stats
   */
  getLatency?(): Promise<any>;

  /** Capture a screenshot as a data URL */
  snapshot?(type?: "png" | "jpeg" | "webp", quality?: number): string | null;
  /** Set video rotation (degrees: 0, 90, 180, 270) */
  setRotation?(degrees: number): void;
  /** Set mirror/flip mode */
  setMirror?(horizontal: boolean): void;
  /** Whether this player uses direct rendering (WebGL/Canvas) */
  readonly isDirectRendering?: boolean;
}

/**
 * Base class providing common functionality
 */
export abstract class BasePlayer implements IPlayer {
  abstract readonly capability: PlayerCapability;

  protected listeners: Map<string, Set<Function>> = new Map();
  protected videoElement: HTMLVideoElement | null = null;

  // Anchor-based coordinate system: MistServer absolute ms via StreamStateClient.
  // Browser video.currentTime is used only for smooth interpolation between updates.
  protected _anchorStreamEndMs = 0;
  protected _anchorRaw = 0;
  protected _dvrWidthMs = 0;
  protected _hasAnchor = false;

  // Live seeking via startunix URL rewriting (MistServer DVR)
  protected liveSeekEnabled = false;
  protected liveSeekBaseUrl: string | null = null;
  protected liveSeekOffsetSec = 0;
  protected pendingLiveSeekOffset: number | null = null;
  protected liveSeekTimer: ReturnType<typeof setTimeout> | null = null;
  private static readonly LIVE_SEEK_DEBOUNCE_MS = 300;

  abstract isMimeSupported(mimetype: string): boolean;
  abstract isBrowserSupported(
    mimetype: string,
    source: StreamSource,
    streamInfo: StreamInfo
  ): boolean | string[];
  abstract initialize(
    container: HTMLElement,
    source: StreamSource,
    options: PlayerOptions,
    streamInfo?: StreamInfo
  ): Promise<HTMLVideoElement>;
  abstract destroy(): void | Promise<void>;

  getVideoElement(): HTMLVideoElement | null {
    return this.videoElement;
  }

  on<K extends keyof PlayerEvents>(event: K, listener: (data: PlayerEvents[K]) => void): void {
    if (!this.listeners.has(event)) {
      this.listeners.set(event, new Set());
    }
    this.listeners.get(event)!.add(listener);
  }

  off<K extends keyof PlayerEvents>(event: K, listener: (data: PlayerEvents[K]) => void): void {
    const eventListeners = this.listeners.get(event);
    if (eventListeners) {
      eventListeners.delete(listener);
    }
  }

  protected emit<K extends keyof PlayerEvents>(event: K, data: PlayerEvents[K]): void {
    const eventListeners = this.listeners.get(event);
    if (eventListeners) {
      eventListeners.forEach((listener) => {
        try {
          listener(data);
        } catch (e) {
          console.error(`Error in ${event} listener:`, e);
        }
      });
    }
  }

  protected setupVideoEventListeners(video: HTMLVideoElement, options: PlayerOptions): void {
    const handleEvent = (eventName: keyof PlayerEvents, handler: () => void) => {
      const listener = () => {
        handler();
        this.emit(eventName as any, undefined as any);
      };
      video.addEventListener(eventName, listener);
    };

    // Core playback events
    handleEvent("play", () => options.onPlay?.());
    handleEvent("pause", () => options.onPause?.());
    handleEvent("ended", () => options.onEnded?.());

    // Buffering/state events (previously duplicated in Player.tsx onReady)
    video.addEventListener("waiting", () => options.onWaiting?.());
    video.addEventListener("playing", () => options.onPlaying?.());
    video.addEventListener("canplay", () => options.onCanPlay?.());

    video.addEventListener("durationchange", () => {
      const durationMs = Number.isFinite(video.duration) ? video.duration * 1000 : video.duration;
      options.onDurationChange?.(durationMs);
    });

    video.addEventListener("timeupdate", () => {
      const currentTimeMs = video.currentTime * 1000;
      options.onTimeUpdate?.(currentTimeMs);
      this.emit("timeupdate", currentTimeMs);
    });

    video.addEventListener("error", () => {
      const error = video.error ? `Video error: ${video.error.message}` : "Unknown video error";
      options.onError?.(error);
      this.emit("error", error);
    });

    // Call onReady LAST - after all listeners are attached
    // This prevents race conditions where events fire before handlers exist
    this.emit("ready", video);
    if (options.onReady) {
      options.onReady(video);
    }
  }

  // Coordinate getters — anchor-based when live seek is active, browser-local fallback
  getCurrentTime(): number {
    if (this.liveSeekEnabled && this._hasAnchor && this.videoElement) {
      const rawAdvance = this.videoElement.currentTime - this._anchorRaw;
      const offset = this.pendingLiveSeekOffset ?? this.liveSeekOffsetSec;
      return this._anchorStreamEndMs + offset * 1000 + rawAdvance * 1000;
    }
    return (this.videoElement?.currentTime ?? 0) * 1000;
  }

  getDuration(): number {
    if (this.liveSeekEnabled && this._hasAnchor && this.videoElement) {
      const rawAdvance = this.videoElement.currentTime - this._anchorRaw;
      return this._anchorStreamEndMs + rawAdvance * 1000;
    }
    const d = this.videoElement?.duration;
    if (d === undefined || d === null) return 0;
    if (!Number.isFinite(d)) return d;
    return d * 1000;
  }

  getSeekableRange(): { start: number; end: number } | null {
    if (!this.liveSeekEnabled || !this._hasAnchor || this._dvrWidthMs <= 0) return null;
    const durationMs = this.getDuration();
    if (!Number.isFinite(durationMs) || durationMs <= 0) return null;
    return { start: Math.max(0, durationMs - this._dvrWidthMs), end: durationMs };
  }

  setSeekableRangeHint(range: { start: number; end: number } | null): void {
    if (!range || !Number.isFinite(range.end) || range.end <= 0) return;
    if (!this.liveSeekEnabled) return;
    this._anchorStreamEndMs = range.end;
    this._anchorRaw = this.videoElement?.currentTime ?? 0;
    this._dvrWidthMs = Math.max(0, range.end - range.start);
    this._hasAnchor = true;
  }

  getBufferedRanges(): TimeRanges | null {
    const video = this.videoElement;
    if (!video) return null;
    const buffered = video.buffered;
    if (!this.liveSeekEnabled || !this._hasAnchor || !buffered || buffered.length === 0)
      return buffered;

    const offset = this.pendingLiveSeekOffset ?? this.liveSeekOffsetSec;
    const baseSec = this._anchorStreamEndMs / 1000 + offset - this._anchorRaw;
    const shifted: [number, number][] = [];
    for (let i = 0; i < buffered.length; i++) {
      const s = buffered.start(i) + baseSec;
      const e = buffered.end(i) + baseSec;
      if (Number.isFinite(s) && Number.isFinite(e)) shifted.push([s, e]);
    }
    return {
      length: shifted.length,
      start(index: number) {
        if (index < 0 || index >= shifted.length) throw new DOMException("Index out of bounds");
        return shifted[index][0];
      },
      end(index: number) {
        if (index < 0 || index >= shifted.length) throw new DOMException("Index out of bounds");
        return shifted[index][1];
      },
    };
  }

  isPaused(): boolean {
    return this.videoElement?.paused ?? true;
  }

  isMuted(): boolean {
    return this.videoElement?.muted ?? false;
  }

  async play(): Promise<void> {
    if (this.videoElement) {
      return this.videoElement.play();
    }
  }

  pause(): void {
    this.videoElement?.pause();
  }

  seek(timeMs: number): void {
    if (this.liveSeekEnabled) {
      if (this._hasAnchor) {
        const durationMs = this.getDuration();
        const offsetFromLiveSec = (timeMs - durationMs) / 1000;

        // In-buffer seek: try browser seekable range first
        const video = this.videoElement;
        if (video?.seekable && video.seekable.length > 0) {
          const rawEnd = video.seekable.end(video.seekable.length - 1);
          const browserTarget = rawEnd + offsetFromLiveSec;
          const rawStart = video.seekable.start(0);
          if (browserTarget >= rawStart - 0.5) {
            const clamped = Math.max(rawStart, Math.min(rawEnd, browserTarget));
            this.seekInBuffer(clamped);
            this.liveSeekOffsetSec = Math.min(0, offsetFromLiveSec);
            this._anchorRaw = clamped;
            return;
          }
        }

        // Out-of-buffer: startunix URL rewrite
        let offset = offsetFromLiveSec;
        if (offset > 0) offset = 0;
        this.scheduleLiveSeekOffset(offset, false);
        return;
      }

      // Pre-anchor fallback: best-effort offset from getDuration()
      const durationMs = this.getDuration();
      const durationSec =
        Number.isFinite(durationMs) && durationMs > 0 ? durationMs / 1000 : timeMs / 1000;
      let offset = timeMs / 1000 - durationSec;
      if (offset > 0) offset = 0;
      this.scheduleLiveSeekOffset(offset, false);
      return;
    }

    // Non-live: direct seek
    this.seekInBuffer(timeMs / 1000);
  }

  setVolume(volume: number): void {
    if (this.videoElement) {
      this.videoElement.volume = Math.max(0, Math.min(1, volume));
    }
  }

  setMuted(muted: boolean): void {
    if (this.videoElement) {
      this.videoElement.muted = muted;
    }
  }
  setPlaybackRate(rate: number): void {
    if (this.videoElement) {
      this.videoElement.playbackRate = rate;
    }
  }

  // Default captions/text tracks using native TextTrack API
  getTextTracks(): Array<{ id: string; label: string; lang?: string; active: boolean }> {
    const video = this.videoElement;
    if (!video || !video.textTracks) return [];
    const out: Array<{ id: string; label: string; lang?: string; active: boolean }> = [];
    const list = video.textTracks as any as TextTrackList;
    for (let i = 0; i < list.length; i++) {
      const tt = list[i];
      out.push({
        id: String(i),
        label: tt.label || `CC ${i + 1}`,
        lang: (tt as any).language,
        active: tt.mode === "showing",
      });
    }
    return out;
  }

  selectTextTrack(id: string | null): void {
    const video = this.videoElement;
    if (!video || !video.textTracks) return;
    const list = video.textTracks as any as TextTrackList;
    for (let i = 0; i < list.length; i++) {
      const tt = list[i];
      if (id !== null && String(i) === id) {
        tt.mode = "showing";
      } else {
        tt.mode = "disabled";
      }
    }
  }

  // Live helpers
  isLive(): boolean {
    if (this.liveSeekEnabled) return true;
    const v = this.videoElement;
    return v ? !isFinite(v.duration) : false;
  }

  jumpToLive(): void {
    if (this.liveSeekEnabled) {
      const v = this.videoElement;
      if (v?.seekable && v.seekable.length > 0) {
        const rawEnd = v.seekable.end(v.seekable.length - 1);
        this.seekInBuffer(rawEnd);
        this.liveSeekOffsetSec = 0;
        this._anchorRaw = rawEnd;
        this.pendingLiveSeekOffset = null;
        if (this.liveSeekTimer) {
          clearTimeout(this.liveSeekTimer);
          this.liveSeekTimer = null;
        }
        return;
      }
      this.scheduleLiveSeekOffset(0, true);
      return;
    }
    const v = this.videoElement;
    if (v?.seekable && v.seekable.length > 0) {
      try {
        v.currentTime = v.seekable.end(v.seekable.length - 1);
      } catch {}
    }
  }

  // Default PiP helper
  async requestPiP(): Promise<void> {
    const v: any = this.videoElement as any;
    if (!v) return;
    // Exit if already in PiP
    if (document.pictureInPictureElement === v) {
      try {
        await (document as any).exitPictureInPicture?.();
      } catch {}
      return;
    }
    try {
      if (v.requestPictureInPicture) {
        await v.requestPictureInPicture();
      } else if (v.webkitSetPresentationMode) {
        v.webkitSetPresentationMode("picture-in-picture");
      }
    } catch {}
  }

  setSize(width: number, height: number): void {
    if (this.videoElement) {
      this.videoElement.style.width = `${width}px`;
      this.videoElement.style.height = `${height}px`;
    }
  }

  // Default optional stats methods
  async getStats(): Promise<any> {
    return undefined;
  }

  async getLatency(): Promise<any> {
    return undefined;
  }

  // Protected hooks — subclasses override for player-specific behavior
  protected seekInBuffer(timeSec: number): void {
    if (this.videoElement) this.videoElement.currentTime = timeSec;
  }

  protected reloadSource(url: string): void {
    if (!this.videoElement) return;
    const wasPlaying = !this.videoElement.paused;
    this.videoElement.src = url;
    this.videoElement.load();
    this._anchorRaw = 0;
    if (wasPlaying) this.videoElement.play().catch(() => {});
  }

  protected initLiveSeek(sourceUrl: string): void {
    this.liveSeekEnabled = true;
    this.liveSeekOffsetSec = 0;
    this.liveSeekBaseUrl = this.stripStartUnixParam(sourceUrl);
  }

  protected cleanupLiveSeek(): void {
    if (this.liveSeekTimer) {
      clearTimeout(this.liveSeekTimer);
      this.liveSeekTimer = null;
    }
    this.pendingLiveSeekOffset = null;
    this._hasAnchor = false;
    this._anchorStreamEndMs = 0;
    this._anchorRaw = 0;
    this._dvrWidthMs = 0;
    this.liveSeekOffsetSec = 0;
    this.liveSeekBaseUrl = null;
    this.liveSeekEnabled = false;
  }

  // startunix URL rewriting infrastructure (MistServer DVR)
  private stripStartUnixParam(url: string): string {
    const params = parseUrlParams(url);
    delete params.startunix;
    return appendUrlParams(stripUrlParams(url), params);
  }

  private buildLiveSeekUrl(offsetSec: number): string {
    const base = this.liveSeekBaseUrl || "";
    if (!base) return "";
    if (!offsetSec || offsetSec >= 0) return this.stripStartUnixParam(base);
    const params = parseUrlParams(base);
    params.startunix = String(offsetSec);
    return appendUrlParams(stripUrlParams(base), params);
  }

  private scheduleLiveSeekOffset(offsetSec: number, immediate: boolean): void {
    const clamped = Math.min(0, offsetSec);
    if (immediate) {
      if (this.liveSeekTimer) {
        clearTimeout(this.liveSeekTimer);
        this.liveSeekTimer = null;
      }
      this.pendingLiveSeekOffset = null;
      this.applyLiveSeekOffset(clamped);
      return;
    }
    this.pendingLiveSeekOffset = clamped;
    if (this.liveSeekTimer) clearTimeout(this.liveSeekTimer);
    this.liveSeekTimer = setTimeout(() => {
      this.liveSeekTimer = null;
      if (this.pendingLiveSeekOffset !== null) {
        const pending = this.pendingLiveSeekOffset;
        this.pendingLiveSeekOffset = null;
        this.applyLiveSeekOffset(pending);
      }
    }, BasePlayer.LIVE_SEEK_DEBOUNCE_MS);
  }

  private applyLiveSeekOffset(offsetSec: number): void {
    const clamped = Math.min(0, offsetSec);
    if (Math.abs(clamped - this.liveSeekOffsetSec) < 0.05) return;
    this.liveSeekOffsetSec = clamped;
    const nextUrl = this.buildLiveSeekUrl(clamped);
    if (!nextUrl) return;
    this.reloadSource(nextUrl);
  }
}
