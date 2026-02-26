/**
 * PlayerController.ts
 *
 * Main headless orchestrator for the player. This class encapsulates all business logic
 * (gateway resolution, stream state polling, player selection/initialization) in a
 * framework-agnostic manner.
 *
 * Both React and Vanilla wrappers use this class internally.
 */

import { TypedEventEmitter } from "./EventEmitter";
import { GatewayClient } from "./GatewayClient";
import { StreamStateClient } from "./StreamStateClient";
import type { PlayerManager, PlayerManagerEvents } from "./PlayerManager";
import { globalPlayerManager, ensurePlayersRegistered } from "./PlayerRegistry";
import { ABRController } from "./ABRController";
import { InteractionController } from "./InteractionController";
import { MistReporter } from "./MistReporter";
import { QualityMonitor, type PlayerProtocol } from "./QualityMonitor";
import { MetaTrackManager } from "./MetaTrackManager";
import { attemptAutoplay, type AutoplayResult } from "./AutoplayRecovery";
import {
  calculateSeekableRange,
  calculateLiveThresholds,
  calculateIsNearLive,
  canSeekStream,
  isMediaStreamSource,
  supportsPlaybackRate,
  getLatencyTier,
  type LatencyTier,
  type LiveThresholds,
} from "./SeekingUtils";
import type { ABRMode, PlaybackQuality, ContentType } from "../types";
import type {
  ContentEndpoints,
  ContentMetadata,
  EndpointInfo,
  MistStreamInfo,
  OutputEndpoint,
  OutputCapabilities,
  PlayerState,
  PlayerStateContext,
  StreamState,
} from "../types";
import type {
  StreamInfo,
  StreamSource,
  StreamTrack,
  IPlayer,
  PlayerOptions as CorePlayerOptions,
} from "./PlayerInterface";

// ============================================================================
// Types
// ============================================================================

export interface PlayerControllerConfig {
  /** Content identifier (stream name) */
  contentId: string;
  /** Content type */
  contentType?: ContentType;

  /** Pre-resolved endpoints (skip gateway) */
  endpoints?: ContentEndpoints;

  /** Gateway URL (for FrameWorks Gateway resolution) */
  gatewayUrl?: string;
  /** Direct MistServer base URL (bypasses Gateway, fetches json_{contentId}.js directly) */
  mistUrl?: string;
  /** Auth token for private streams */
  authToken?: string;

  /** Playback options */
  autoplay?: boolean;
  muted?: boolean;
  controls?: boolean;
  poster?: string;

  /** Debug logging */
  debug?: boolean;

  /** Custom PlayerManager instance (optional, uses global by default) */
  playerManager?: PlayerManager;

  // Dev mode overrides - passed to PlayerManager during player selection
  /** Force a specific player (e.g., 'hlsjs', 'dashjs', 'native') */
  forcePlayer?: string;
  /** Force a specific MIME type (e.g., 'html5/application/vnd.apple.mpegurl') */
  forceType?: string;
  /** Force a specific source index */
  forceSource?: number;
  /** Playback mode preference */
  playbackMode?: "auto" | "low-latency" | "quality" | "vod";
}

export interface PlayerControllerEvents {
  /** Player state changed */
  stateChange: { state: PlayerState; context?: PlayerStateContext };
  /** Stream state changed (for live streams) */
  streamStateChange: { state: StreamState };
  /** Time update during playback */
  timeUpdate: { currentTime: number; duration: number };
  /** Error occurred */
  error: { error: string; code?: string };
  /** Error was cleared (auto-cleared or manually) */
  errorCleared: void;
  /** Player ready with video element */
  ready: { videoElement: HTMLVideoElement };
  /** Controller destroyed */
  destroyed: void;

  // ============================================================================
  // Playback Events (Phase A5)
  // ============================================================================

  /** Player/source was selected */
  playerSelected: { player: string; source: StreamSource; score: number };
  /** Quality level changed (ABR switch) */
  qualityChanged: { fromLevel?: string; toLevel: string };
  /** Volume or mute state changed */
  volumeChange: { volume: number; muted: boolean };
  /** Fullscreen state changed */
  fullscreenChange: { isFullscreen: boolean };
  /** Picture-in-Picture state changed */
  pipChange: { isPiP: boolean };
  /** Loop mode changed */
  loopChange: { isLoopEnabled: boolean };
  /** Playback rate changed */
  speedChange: { rate: number };
  /** User skipped forward */
  skipForward: { seconds: number };
  /** User skipped backward */
  skipBackward: { seconds: number };
  /** Speed hold started (hold-for-2x gesture) */
  holdSpeedStart: { speed: number };
  /** Speed hold ended */
  holdSpeedEnd: void;
  /** Captions/subtitles toggled */
  captionsChange: { enabled: boolean };
  /** Mute state changed (e.g. autoplay muted fallback) */
  muteChange: { muted: boolean };
  /** Autoplay attempt resolved */
  autoplayResult: { status: AutoplayResult };

  // ============================================================================
  // Seeking & Live State Events (Centralized from wrappers)
  // ============================================================================

  /** Seeking/live state changed - emitted on timeupdate when values change */
  seekingStateChange: {
    seekableStart: number;
    liveEdge: number;
    canSeek: boolean;
    isNearLive: boolean;
    isLive: boolean;
    isWebRTC: boolean;
    latencyTier: LatencyTier;
    buffered: TimeRanges | null;
    hasAudio: boolean;
    supportsPlaybackRate: boolean;
  };

  // ============================================================================
  // Interaction Events (Phase A5)
  // ============================================================================

  /** User started hovering over player */
  hoverStart: void;
  /** User stopped hovering (after timeout) */
  hoverEnd: void;
  /** User became idle (no interaction for N seconds) */
  interactionIdle: void;
  /** User resumed interaction after being idle */
  interactionActive: void;

  // ============================================================================
  // Metadata Events (Phase A5)
  // ============================================================================

  /** Playback metadata updated */
  metadataUpdate: {
    currentTime: number;
    duration: number;
    bufferedAhead: number;
    qualityScore?: number;
    playerInfo?: { name: string; shortname: string };
    sourceInfo?: { url: string; type: string };
    isLive: boolean;
    isBuffering: boolean;
    isPaused: boolean;
    volume: number;
    muted: boolean;
  };

  // ============================================================================
  // Error Handling Events (from PlayerManager)
  // ============================================================================

  /** Protocol/player swap occurred - show toast */
  protocolSwapped: PlayerManagerEvents["protocolSwapped"];
  /** Playback failed after all recovery attempts - show error modal */
  playbackFailed: PlayerManagerEvents["playbackFailed"];
}

// ============================================================================
// Content Type Resolution Helpers
// ============================================================================

// ============================================================================
// MistServer Source Type Mapping
// ============================================================================

/**
 * Complete MistServer source type mapping
 * Maps MistServer's `source[].type` field to player selection info
 *
 * type field = MIME type used for player selection
 * hrn = human readable name for UI
 * player = recommended player implementation
 * supported = whether we have a working player for it
 */
export const MIST_SOURCE_TYPES: Record<
  string,
  { hrn: string; player: string; supported: boolean }
> = {
  // ===== VIDEO STREAMING (Primary) =====
  "html5/application/vnd.apple.mpegurl": { hrn: "HLS (TS)", player: "hlsjs", supported: true },
  "html5/application/vnd.apple.mpegurl;version=7": {
    hrn: "HLS (CMAF)",
    player: "hlsjs",
    supported: true,
  },
  "dash/video/mp4": { hrn: "DASH", player: "dashjs", supported: true },
  "html5/video/mp4": { hrn: "MP4 progressive", player: "native", supported: true },
  "html5/video/webm": { hrn: "WebM progressive", player: "native", supported: true },

  // ===== WEBSOCKET STREAMING =====
  "ws/video/mp4": { hrn: "MP4 WebSocket", player: "mews", supported: true },
  "wss/video/mp4": { hrn: "MP4 WebSocket (SSL)", player: "mews", supported: true },
  "ws/video/webm": { hrn: "WebM WebSocket", player: "mews", supported: true },
  "wss/video/webm": { hrn: "WebM WebSocket (SSL)", player: "mews", supported: true },
  "ws/video/raw": { hrn: "Raw WebSocket", player: "webcodecs", supported: true },
  "wss/video/raw": { hrn: "Raw WebSocket (SSL)", player: "webcodecs", supported: true },
  "ws/video/h264": { hrn: "Annex B WebSocket", player: "webcodecs", supported: true },
  "wss/video/h264": { hrn: "Annex B WebSocket (SSL)", player: "webcodecs", supported: true },

  // ===== WEBRTC =====
  whep: { hrn: "WebRTC (WHEP)", player: "native", supported: true },
  webrtc: { hrn: "WebRTC (WebSocket)", player: "mist-webrtc", supported: true },
  "mist/webrtc": { hrn: "MistServer WebRTC", player: "mist-webrtc", supported: true },

  // ===== AUDIO ONLY =====
  "html5/audio/aac": { hrn: "AAC progressive", player: "native", supported: true },
  "html5/audio/mp3": { hrn: "MP3 progressive", player: "native", supported: true },
  "html5/audio/flac": { hrn: "FLAC progressive", player: "native", supported: true },
  "html5/audio/wav": { hrn: "WAV progressive", player: "native", supported: true },

  // ===== SUBTITLES/TEXT =====
  "html5/text/vtt": { hrn: "WebVTT subtitles", player: "track", supported: true },
  "html5/text/plain": { hrn: "SRT subtitles", player: "track", supported: true },

  // ===== IMAGES =====
  "html5/image/jpeg": { hrn: "JPEG thumbnail", player: "image", supported: true },

  // ===== METADATA =====
  "html5/text/javascript": { hrn: "JSON metadata", player: "fetch", supported: true },

  // ===== LEGACY/UNSUPPORTED =====
  "html5/video/mpeg": { hrn: "TS progressive", player: "none", supported: false },
  "html5/video/h264": { hrn: "Annex B progressive", player: "none", supported: false },
  "html5/application/sdp": { hrn: "SDP", player: "none", supported: false },
  "html5/application/vnd.ms-sstr+xml": {
    hrn: "Smooth Streaming",
    player: "none",
    supported: false,
  },
  "flash/7": { hrn: "FLV", player: "none", supported: false },
  "flash/10": { hrn: "RTMP", player: "none", supported: false },
  "flash/11": { hrn: "HDS", player: "none", supported: false },

  // ===== SERVER-SIDE ONLY =====
  rtsp: { hrn: "RTSP", player: "none", supported: false },
  srt: { hrn: "SRT", player: "none", supported: false },
  dtsc: { hrn: "DTSC", player: "none", supported: false },
};

/**
 * Map Gateway protocol names to MistServer MIME types
 * Gateway outputs use simplified protocol names like "HLS", "WHEP"
 * while MistServer uses full MIME types
 */
export const PROTOCOL_TO_MIME: Record<string, string> = {
  // Standard protocols
  HLS: "html5/application/vnd.apple.mpegurl",
  DASH: "dash/video/mp4",
  MP4: "html5/video/mp4",
  WEBM: "html5/video/webm",
  WHEP: "whep",
  WebRTC: "webrtc",
  MIST_WEBRTC: "mist/webrtc", // MistServer native WebRTC signaling

  // WebSocket variants
  MEWS: "ws/video/mp4",
  MEWS_WS: "ws/video/mp4",
  MEWS_WSS: "wss/video/mp4",
  MEWS_WEBM: "ws/video/webm",
  MEWS_WEBM_SSL: "wss/video/webm",
  RAW_WS: "ws/video/raw",
  RAW_WSS: "wss/video/raw",
  H264_WS: "ws/video/h264",
  H264_WSS: "wss/video/h264",

  // Audio
  AAC: "html5/audio/aac",
  MP3: "html5/audio/mp3",
  FLAC: "html5/audio/flac",
  WAV: "html5/audio/wav",

  // Subtitles
  VTT: "html5/text/vtt",
  SRT: "html5/text/plain",

  // CMAF variants
  CMAF: "html5/application/vnd.apple.mpegurl;version=7",
  HLS_CMAF: "html5/application/vnd.apple.mpegurl;version=7",

  // Images
  JPEG: "html5/image/jpeg",
  JPG: "html5/image/jpeg",

  // MistServer specific
  HTTP: "html5/video/mp4", // Default HTTP is MP4
  MIST_HTML: "mist/html",
  PLAYER_JS: "mist/html",
};

/**
 * Get the MIME type for a Gateway protocol name
 */
export function getMimeTypeForProtocol(protocol: string): string {
  return PROTOCOL_TO_MIME[protocol] || PROTOCOL_TO_MIME[protocol.toUpperCase()] || protocol;
}

/**
 * Get source type info for a MIME type
 */
export function getSourceTypeInfo(
  mimeType: string
): { hrn: string; player: string; supported: boolean } | undefined {
  return MIST_SOURCE_TYPES[mimeType];
}

// ============================================================================
// Helper Functions
// ============================================================================

function mapCodecLabel(codecstr: string): string {
  const c = codecstr.toLowerCase();
  if (c.startsWith("avc1")) return "H264";
  if (c.startsWith("hev1") || c.startsWith("hvc1")) return "HEVC";
  if (c.startsWith("av01")) return "AV1";
  if (c.startsWith("vp09")) return "VP9";
  if (c.startsWith("vp8")) return "VP8";
  if (c.startsWith("mp4a")) return "AAC";
  if (c.includes("opus")) return "Opus";
  if (c.includes("ec-3") || c.includes("ac3")) return "AC3";
  return codecstr;
}

// ============================================================================
// Standalone Stream Info Builder
// ============================================================================

/**
 * Build StreamInfo from Gateway ContentEndpoints.
 *
 * This function extracts playback sources and track information from
 * the Gateway's resolved endpoint data. It handles:
 * - Parsing `outputs` JSON string (GraphQL returns JSON scalar as string)
 * - Converting output protocols to StreamSource format
 * - Deriving track info from capabilities
 *
 * Use this for VOD/clip content where Gateway data is sufficient,
 * without waiting for MistServer to load the stream.
 *
 * @param endpoints - ContentEndpoints from Gateway resolution
 * @param contentId - Stream/content identifier
 * @returns StreamInfo with sources and tracks, or null if no valid data
 */
export function buildStreamInfoFromEndpoints(
  endpoints: ContentEndpoints,
  contentId: string
): StreamInfo | null {
  const primary = endpoints.primary as EndpointInfo | undefined;
  if (!primary) return null;

  // Parse outputs if it's a JSON string (GraphQL returns JSON scalar as string)
  let outputs: Record<string, OutputEndpoint> = {};
  if (primary.outputs) {
    if (typeof primary.outputs === "string") {
      try {
        outputs = JSON.parse(primary.outputs);
      } catch {
        console.warn("[buildStreamInfoFromEndpoints] Failed to parse outputs JSON");
        outputs = {};
      }
    } else {
      outputs = primary.outputs as Record<string, OutputEndpoint>;
    }
  }

  const sources: StreamSource[] = [];
  const oKeys = Object.keys(outputs);

  const attachMistSource = (html?: string, playerJs?: string) => {
    if (!html && !playerJs) return;
    const src: StreamSource = {
      url: html || playerJs || "",
      type: "mist/html",
      streamName: contentId,
    } as StreamSource;
    if (playerJs) {
      (src as any).mistPlayerUrl = playerJs;
    }
    sources.push(src);
  };

  if (oKeys.length) {
    const html = outputs["MIST_HTML"]?.url;
    const pjs = outputs["PLAYER_JS"]?.url;
    attachMistSource(html, pjs);

    // Process all outputs using PROTOCOL_TO_MIME mapping
    // Skip MIST_HTML and PLAYER_JS (already handled above)
    const skipProtocols = new Set(["MIST_HTML", "PLAYER_JS"]);

    for (const protocol of oKeys) {
      if (skipProtocols.has(protocol)) continue;

      const output = outputs[protocol];
      if (!output?.url) continue;

      // Convert Gateway protocol name to MistServer MIME type
      const mimeType = getMimeTypeForProtocol(protocol);

      // Check if this source type is supported
      const sourceInfo = getSourceTypeInfo(mimeType);
      if (sourceInfo && !sourceInfo.supported) {
        // Skip unsupported source types
        continue;
      }

      sources.push({ url: output.url, type: mimeType });
    }
  } else if (primary) {
    // Fallback: single primary URL
    sources.push({
      url: primary.url,
      type: primary.protocol || "mist/html",
      streamName: contentId,
    } as StreamSource);
  }

  // Derive tracks from capabilities
  const tracks: StreamTrack[] = [];
  const pushCodecTracks = (cap?: OutputCapabilities) => {
    if (!cap) return;
    const codecs = cap.codecs || [];
    const addTrack = (type: "video" | "audio", codecstr: string) => {
      tracks.push({ type, codec: mapCodecLabel(codecstr), codecstring: codecstr });
    };
    codecs.forEach((c) => {
      const lc = c.toLowerCase();
      if (
        lc.startsWith("avc1") ||
        lc.startsWith("hev1") ||
        lc.startsWith("hvc1") ||
        lc.startsWith("vp") ||
        lc.startsWith("av01")
      ) {
        addTrack("video", c);
      } else if (
        lc.startsWith("mp4a") ||
        lc.includes("opus") ||
        lc.includes("vorbis") ||
        lc.includes("ac3") ||
        lc.includes("ec-3")
      ) {
        addTrack("audio", c);
      }
    });
    if (!codecs.length) {
      // Fallback codecs with valid codecstrings for cold-start playback
      if (cap.hasVideo) tracks.push({ type: "video", codec: "H264", codecstring: "avc1.42E01E" });
      if (cap.hasAudio) tracks.push({ type: "audio", codec: "AAC", codecstring: "mp4a.40.2" });
    }
  };
  Object.values(outputs).forEach((out) => pushCodecTracks(out.capabilities));
  if (!tracks.length) {
    // Fallback with valid codecstring for cold-start playback
    tracks.push({ type: "video", codec: "H264", codecstring: "avc1.42E01E" });
  }

  // Determine content type from metadata
  const contentType: "live" | "vod" = endpoints.metadata?.isLive === false ? "vod" : "live";

  return sources.length ? { source: sources, meta: { tracks }, type: contentType } : null;
}

// ============================================================================
// PlayerController Class
// ============================================================================

/**
 * Headless player controller that manages the entire player lifecycle.
 *
 * @example
 * ```typescript
 * const controller = new PlayerController({
 *   contentId: 'pk_...', // playbackId (view key)
 *   contentType: 'live',
 *   gatewayUrl: 'https://gateway.example.com/graphql',
 * });
 *
 * controller.on('stateChange', ({ state }) => console.log('State:', state));
 * controller.on('ready', ({ videoElement }) => console.log('Ready!'));
 *
 * const container = document.getElementById('player');
 * await controller.attach(container);
 *
 * // Later...
 * controller.destroy();
 * ```
 */
export class PlayerController extends TypedEventEmitter<PlayerControllerEvents> {
  private config: PlayerControllerConfig;
  private state: PlayerState = "booting";
  private lastEmittedState: PlayerState | null = null;
  private suppressPlayPauseEventsUntil = 0;

  private gatewayClient: GatewayClient | null = null;
  private streamStateClient: StreamStateClient | null = null;
  private playerManager: PlayerManager;

  private currentPlayer: IPlayer | null = null;
  private videoElement: HTMLVideoElement | null = null;
  private container: HTMLElement | null = null;

  private endpoints: ContentEndpoints | null = null;
  private streamInfo: StreamInfo | null = null;
  private streamState: StreamState | null = null;
  /** Tracks parsed from MistServer JSON response (used for direct MistServer mode) */
  private mistTracks: StreamTrack[] | null = null;
  /** Gateway-seeded metadata (used as base for Mist enrichment) */
  private metadataSeed: ContentMetadata | null = null;
  /** Merged metadata (gateway seed + Mist enrichment) */
  private metadata: ContentMetadata | null = null;

  private cleanupFns: Array<() => void> = [];
  private isDestroyed: boolean = false;
  private isAttached: boolean = false;

  // ============================================================================
  // Internal State Tracking (Phase A1)
  // ============================================================================
  private _isBuffering: boolean = false;
  private _hasPlaybackStarted: boolean = false;
  private _errorText: string | null = null;
  private _isPassiveError: boolean = false;
  private _isHoldingSpeed: boolean = false;
  private _holdSpeed: number = 2;
  private _isLoopEnabled: boolean = false;
  private _currentPlayerInfo: { name: string; shortname: string } | null = null;
  private _currentSourceInfo: { url: string; type: string } | null = null;

  // One-shot force options (used once by selectCombo, then cleared)
  private _pendingForceOptions: {
    forcePlayer?: string;
    forceType?: string;
    forceSource?: number;
  } | null = null;

  // ============================================================================
  // Error Handling State (Phase A3)
  // ============================================================================
  private _errorShownAt: number = 0;
  private _errorCleared: boolean = false;
  private _isTransitioning: boolean = false;
  private _qualityFallbackInProgress: boolean = false;
  private _qualityFallbackLastAt: number = 0;
  private _errorCount: number = 0;
  private _lastErrorTime: number = 0;
  private _retrySuppressUntil: number = 0;
  private _playbackResumedSinceError: boolean = false;

  // ============================================================================
  // Stream State Tracking (Phase A4)
  // ============================================================================
  private _prevStreamIsOnline: boolean | undefined = undefined;

  // ============================================================================
  // Hover/Controls Visibility (Phase A5b)
  // ============================================================================
  private _isHovering: boolean = false;
  private _hoverTimeout: ReturnType<typeof setTimeout> | null = null;
  private static readonly HOVER_HIDE_DELAY_MS = 3000;
  private static readonly HOVER_LEAVE_DELAY_MS = 200;

  // ============================================================================
  // Subtitles/Captions (Phase A5b audit)
  // ============================================================================
  private _subtitlesEnabled: boolean = false;

  // ============================================================================
  // Stall Detection (Phase A5b audit)
  // ============================================================================
  private _stallStartTime: number = 0;
  private static readonly HARD_FAILURE_STALL_THRESHOLD_MS = 30000; // 30 seconds sustained stall

  // ============================================================================
  // Seeking & Live Detection State (Centralized from wrappers)
  // ============================================================================
  private _seekableStart: number = 0;
  private _liveEdge: number = 0;
  private _canSeek: boolean = false;
  private _seekingLoggedOnce: boolean = false;
  private _pendingPlayIntent: boolean = false;
  private _isNearLive: boolean = true;
  private _latencyTier: LatencyTier = "medium";
  private _liveThresholds: LiveThresholds = { exitLive: 15000, enterLive: 5000 };
  private _buffered: TimeRanges | null = null;
  private _hasAudio: boolean = true;
  private _boundMediaStreamForAudio: MediaStream | null = null;
  private _onMediaStreamTrackChange: (() => void) | null = null;
  private _lastVolume: number = 1;
  private _supportsPlaybackRate: boolean = true;
  private _isWebRTC: boolean = false;

  // Error handling constants
  private static readonly AUTO_CLEAR_ERROR_DELAY_MS = 2000;
  private static readonly HARD_FAILURE_ERROR_THRESHOLD = 5;
  private static readonly HARD_FAILURE_ERROR_WINDOW_MS = 60000;
  private static readonly QUALITY_FALLBACK_COOLDOWN_MS = 15000;
  private static readonly FATAL_ERROR_KEYWORDS = [
    "fatal",
    "network error",
    "media error",
    "decode error",
    "source not supported",
  ];

  // ============================================================================
  // Sub-Controllers (Phase A2)
  // ============================================================================
  private abrController: ABRController | null = null;
  private interactionController: InteractionController | null = null;
  private mistReporter: MistReporter | null = null;
  private qualityMonitor: QualityMonitor | null = null;
  private metaTrackManager: MetaTrackManager | null = null;
  private _playbackQuality: PlaybackQuality | null = null;
  private bootMs: number = Date.now();
  private playerManagerUnsubs: Array<() => void> = [];

  constructor(config: PlayerControllerConfig) {
    super();
    this.config = config;
    this.playerManager = config.playerManager || globalPlayerManager;

    // Forward error handling events from PlayerManager
    this.playerManagerUnsubs.push(
      this.playerManager.on("protocolSwapped", (data) => this.emit("protocolSwapped", data)),
      this.playerManager.on("playbackFailed", (data) => this.emit("playbackFailed", data))
    );

    // Load loop state from localStorage
    try {
      if (typeof localStorage !== "undefined") {
        this._isLoopEnabled = localStorage.getItem("frameworks-player-loop") === "true";
      }
    } catch {
      // localStorage not available
    }
  }

  // ============================================================================
  // Lifecycle Methods
  // ============================================================================

  /**
   * Attach to a container element and start the player lifecycle.
   * This is the main entry point after construction.
   */
  async attach(container: HTMLElement): Promise<void> {
    if (this.isDestroyed) {
      throw new Error("PlayerController is destroyed and cannot be reused");
    }
    if (this.isAttached) {
      this.log("Already attached, detaching first");
      this.detach();
    }

    this.container = container;
    this.isAttached = true;
    this.setState("booting");

    try {
      // Ensure players are registered
      ensurePlayersRegistered();

      // Step 1: Resolve endpoints
      await this.resolveEndpoints();

      // Guard against zombie operations (React Strict Mode cleanup)
      if (this.isDestroyed || !this.container) {
        this.log("[attach] Aborted - controller destroyed during endpoint resolution");
        return;
      }

      if (!this.endpoints?.primary) {
        this.setState("no_endpoint", { gatewayStatus: "error" });
        return;
      }

      // Step 2: Start stream state polling (for live content)
      this.startStreamStatePolling();

      if (!this.streamInfo) {
        this.streamInfo = this.buildStreamInfo(this.endpoints);
      }

      if (!this.streamInfo || this.streamInfo.source.length === 0) {
        this.setState("error", { error: "No playable sources found" });
        this.emit("error", { error: "No playable sources found" });
        return;
      }

      // Guard again before player init (async boundary)
      if (this.isDestroyed || !this.container) {
        this.log("[attach] Aborted - controller destroyed before player init");
        return;
      }

      await this.initializePlayer();
    } catch (error) {
      const message = error instanceof Error ? error.message : "Unknown error";
      // Don't emit error for offline streams — the idle screen handles that UX.
      // Emitting here creates stale _displayedError that resurfaces when stream comes online.
      const isOffline = /offline|not found|stream not found/i.test(message);
      if (!isOffline) {
        this.setState("error", { error: message });
        this.emit("error", { error: message });
      }

      // Even if initial resolution failed (e.g., stream offline), start polling
      // so we can detect when the stream comes online and re-initialize
      if (this.config.mistUrl && !this.streamStateClient) {
        this.log("[attach] Starting stream polling despite resolution failure");
        this.startStreamStatePolling();
      }
    }
  }

  /**
   * Detach from the current container and clean up resources.
   * The controller can be re-attached to a new container.
   */
  detach(): void {
    this.cleanup();
    this.clearHoverTimeout();
    this.isAttached = false;
    this.container = null;
    this.endpoints = null;
    this.streamInfo = null;
    this.streamState = null;
    this.metadataSeed = null;
    this.metadata = null;
    this.videoElement = null;
    this.currentPlayer = null;
    this.lastEmittedState = null;
    this._isHovering = false;
    this._hasPlaybackStarted = false;
    this._isBuffering = false;
    this._stallStartTime = 0;
    this._seekableStart = 0;
    this._liveEdge = 0;
    this._canSeek = false;
    this._isNearLive = true;
    this._qualityFallbackInProgress = false;
  }

  /**
   * Fully destroy the controller. Cannot be reused after this.
   */
  destroy(): void {
    if (this.isDestroyed) return;

    this.playerManagerUnsubs.forEach((fn) => fn());
    this.playerManagerUnsubs = [];

    this.detach();
    this.setState("destroyed");
    this.emit("destroyed", undefined as never);
    this.removeAllListeners();
    this.isDestroyed = true;
  }

  // ============================================================================
  // State Getters
  // ============================================================================

  /** Get current player state */
  getState(): PlayerState {
    return this.state;
  }

  /** Get current stream state (for live streams) */
  getStreamState(): StreamState | null {
    return this.streamState;
  }

  /** Get resolved endpoints */
  getEndpoints(): ContentEndpoints | null {
    return this.endpoints;
  }

  /** Get content metadata (title, description, duration, etc.) */
  getMetadata(): ContentMetadata | null {
    return this.metadata ?? null;
  }

  // ============================================================================
  // Metadata Merge (Gateway seed + Mist enrichment)
  // ============================================================================

  private setMetadataSeed(seed: ContentMetadata | null | undefined): void {
    this.metadataSeed = seed ? { ...seed } : null;
    this.refreshMergedMetadata();
  }

  private refreshMergedMetadata(): void {
    const seed = this.metadataSeed ? { ...this.metadataSeed } : null;
    const mist = this.streamState?.streamInfo;
    const streamStatus = this.streamState?.status;

    if (!seed && !mist) {
      this.metadata = null;
      return;
    }

    const merged: ContentMetadata = seed ? { ...seed } : {};

    if (mist) {
      merged.mist = this.sanitizeMistInfo(mist);
      if (mist.type) {
        merged.contentType = mist.type;
        merged.isLive = mist.type === "live";
      }
      if (streamStatus) {
        merged.status = streamStatus;
      }
      if (mist.meta?.duration && (!merged.durationSeconds || merged.durationSeconds <= 0)) {
        merged.durationSeconds = Math.round(mist.meta.duration);
      }
      if (mist.meta?.tracks) {
        merged.tracks = this.buildMetadataTracks(mist.meta.tracks);
      }
    }

    this.metadata = merged;
  }

  private buildMetadataTracks(tracksObj: Record<string, unknown>): ContentMetadata["tracks"] {
    const tracks: NonNullable<ContentMetadata["tracks"]> = [];
    for (const [, trackData] of Object.entries(tracksObj)) {
      const t = trackData as Record<string, unknown>;
      const trackType = t.type as string;
      if (trackType !== "video" && trackType !== "audio" && trackType !== "meta") {
        continue;
      }

      const bitrate = typeof t.bps === "number" ? Math.round(t.bps) : undefined;
      const fps = typeof t.fpks === "number" ? t.fpks / 1000 : undefined;

      tracks.push({
        type: trackType,
        codec: t.codec as string | undefined,
        width: t.width as number | undefined,
        height: t.height as number | undefined,
        bitrate,
        fps,
        channels: t.channels as number | undefined,
        sampleRate: t.rate as number | undefined,
      });
    }
    return tracks.length ? tracks : undefined;
  }

  private sanitizeMistInfo(info: MistStreamInfo): MistStreamInfo {
    const sanitized: MistStreamInfo = {
      error: info.error,
      on_error: info.on_error,
      perc: info.perc,
      type: info.type,
      hasVideo: info.hasVideo,
      hasAudio: info.hasAudio,
      unixoffset: info.unixoffset,
      lastms: info.lastms,
    };

    if (info.source) {
      sanitized.source = info.source.map((src) => ({
        url: src.url,
        type: src.type,
        priority: src.priority,
        simul_tracks: src.simul_tracks,
        relurl: src.relurl,
      }));
    }

    if (info.meta) {
      sanitized.meta = {
        buffer_window: info.meta.buffer_window,
        duration: info.meta.duration,
        mistUrl: info.meta.mistUrl,
      };

      if (info.meta.tracks) {
        const tracks: Record<string, any> = {};
        for (const [key, track] of Object.entries(info.meta.tracks)) {
          tracks[key] = {
            type: track.type,
            codec: track.codec,
            width: track.width,
            height: track.height,
            bps: track.bps,
            fpks: track.fpks,
            codecstring: track.codecstring,
            firstms: track.firstms,
            lastms: track.lastms,
            lang: track.lang,
            idx: track.idx,
            channels: track.channels,
            rate: track.rate,
            size: track.size,
          };
        }
        sanitized.meta.tracks = tracks;
      }
    }

    return sanitized;
  }

  /** Get stream info (sources + tracks for player selection) */
  getStreamInfo(): StreamInfo | null {
    return this.streamInfo;
  }

  /** Get video element (null if not ready) */
  getVideoElement(): HTMLVideoElement | null {
    return this.videoElement;
  }

  /** Get current player instance */
  getPlayer(): IPlayer | null {
    return this.currentPlayer;
  }

  /** Check if player is ready */
  isReady(): boolean {
    return this.videoElement !== null;
  }

  // ============================================================================
  // Extended State Getters (Phase A1)
  // ============================================================================

  /** Check if video is currently playing (not paused) */
  isPlaying(): boolean {
    const paused = this.currentPlayer?.isPaused?.() ?? this.videoElement?.paused ?? true;
    return !paused;
  }

  /** Check if currently buffering */
  isBuffering(): boolean {
    return this._isBuffering;
  }

  /** Get current error message (null if no error) */
  getError(): string | null {
    return this._errorText;
  }

  /** Check if error is passive (video still playing despite error) */
  isPassiveError(): boolean {
    return this._isPassiveError;
  }

  /** Check if playback has ever started (for idle screen logic) */
  hasPlaybackStarted(): boolean {
    return this._hasPlaybackStarted;
  }

  /** Check if currently holding for speed boost */
  isHoldingSpeed(): boolean {
    return this._isHoldingSpeed;
  }

  /** Get current hold speed value */
  getHoldSpeed(): number {
    return this._holdSpeed;
  }

  /** Get current player implementation info */
  getCurrentPlayerInfo(): { name: string; shortname: string } | null {
    return this._currentPlayerInfo;
  }

  /** Get current source info (URL and type) */
  getCurrentSourceInfo(): { url: string; type: string } | null {
    return this._currentSourceInfo;
  }

  /** Get current volume (0-1) */
  getVolume(): number {
    return this.videoElement?.volume ?? 1;
  }

  /** Check if loop mode is enabled */
  isLoopEnabled(): boolean {
    return this._isLoopEnabled;
  }

  /** Check if subtitles/captions are enabled */
  isSubtitlesEnabled(): boolean {
    return this._subtitlesEnabled;
  }

  /** Set subtitles/captions enabled state */
  setSubtitlesEnabled(enabled: boolean): void {
    if (this._subtitlesEnabled === enabled) return;
    this._subtitlesEnabled = enabled;
    // Apply to video text tracks if available
    if (this.videoElement) {
      const tracks = this.videoElement.textTracks;
      for (let i = 0; i < tracks.length; i++) {
        const track = tracks[i];
        if (track.kind === "subtitles" || track.kind === "captions") {
          track.mode = enabled ? "showing" : "hidden";
        }
      }
    }
    this.emit("captionsChange", { enabled });
  }

  /** Toggle subtitles/captions */
  toggleSubtitles(): void {
    this.setSubtitlesEnabled(!this._subtitlesEnabled);
  }

  // ============================================================================
  // Seeking & Live State Getters (Centralized from wrappers)
  // ============================================================================

  /** Get start of seekable range (ms) */
  getSeekableStart(): number {
    return this._seekableStart;
  }

  /** Get live edge / end of seekable range (ms) */
  getLiveEdge(): number {
    return this._liveEdge;
  }

  /** Check if seeking is currently available */
  canSeekStream(): boolean {
    return this._canSeek;
  }

  /** Check if playback is near the live edge (for live badge display) */
  isNearLive(): boolean {
    return this._isNearLive;
  }

  /** Get buffered ranges, preferring player override when available */
  getBufferedRanges(): TimeRanges | null {
    if (this.currentPlayer && typeof this.currentPlayer.getBufferedRanges === "function") {
      return this.currentPlayer.getBufferedRanges();
    }
    return this.videoElement?.buffered ?? null;
  }

  /** Get current latency tier based on protocol */
  getLatencyTier(): LatencyTier {
    return this._latencyTier;
  }

  /** Get live thresholds for entering/exiting "LIVE" state */
  getLiveThresholds(): LiveThresholds {
    return this._liveThresholds;
  }

  /** Get buffered time ranges */
  getBuffered(): TimeRanges | null {
    return this._buffered;
  }

  /** Check if stream has audio track */
  hasAudioTrack(): boolean {
    return this._hasAudio;
  }

  /** Check if playback rate adjustment is supported */
  canAdjustPlaybackRate(): boolean {
    return this._supportsPlaybackRate;
  }

  /** Resolve content type from config override or Gateway metadata */
  private getResolvedContentType(): ContentType | null {
    if (this.config.contentType) {
      return this.config.contentType;
    }
    const metadata = this.getMetadata();
    const metaType = metadata?.contentType?.toLowerCase();
    if (metaType === "live" || metaType === "clip" || metaType === "dvr" || metaType === "vod") {
      return metaType as ContentType;
    }
    const mistType = this.streamState?.streamInfo?.type;
    if (mistType === "live" || mistType === "vod") {
      return mistType;
    }
    if (metadata?.isLive === true) {
      return "live";
    }
    if (metadata?.isLive === false) {
      return "vod";
    }
    return null;
  }

  /** Check if source is WebRTC/MediaStream */
  isWebRTCSource(): boolean {
    return this._isWebRTC;
  }

  /** Check if currently in fullscreen mode */
  isFullscreen(): boolean {
    if (typeof document === "undefined") return false;
    return document.fullscreenElement === this.container;
  }

  /** Check if content is effectively live (live or DVR still recording) */
  isEffectivelyLive(): boolean {
    const contentType = this.getResolvedContentType() ?? "live";
    const metadata = this.getMetadata();

    // Explicit VOD content types are never live
    if (contentType === "vod" || contentType === "clip") {
      return false;
    }

    // If Gateway metadata says it's not live, trust it
    if (metadata?.isLive === false) {
      return false;
    }

    // DVR that's finished recording is not live
    if (contentType === "dvr" && metadata?.dvrStatus === "completed") {
      return false;
    }

    // Default: trust contentType or duration-based detection
    return (
      contentType === "live" ||
      (contentType === "dvr" && metadata?.dvrStatus === "recording") ||
      !Number.isFinite(this.getDuration())
    );
  }

  /** Check if content is strictly live (not DVR/clip/vod) */
  isLive(): boolean {
    return (this.getResolvedContentType() ?? "live") === "live";
  }

  /**
   * Check if content needs cold start (VOD-like loading).
   * True for: clips, DVR (recording OR completed) - any stored/VOD content
   * False for: live streams only (real-time MistServer stream)
   * DVR-while-recording needs cold start because MistServer may not be serving the VOD yet
   */
  needsColdStart(): boolean {
    const contentType = this.getResolvedContentType();
    if (!contentType) return true;
    return contentType !== "live";
  }

  /**
   * Check if we should show idle/loading screen.
   * Logic:
   * - For cold start content (VOD/DVR): Show loading only while waiting for Gateway sources
   * - For live streams: Show loading while waiting for MistServer to come online
   * - Never show idle after playback has started (unless explicit error)
   */
  shouldShowIdleScreen(): boolean {
    // For live streams, always show idle/offline UI when stream state says offline,
    // even if playback started earlier in this controller lifetime.
    if (!this.needsColdStart()) {
      if (this.streamState?.isOnline === false || this.streamState?.status === "OFFLINE") {
        return true;
      }
    }

    // For non-offline states, keep idle hidden once playback started.
    if (this._hasPlaybackStarted) return false;

    if (this.needsColdStart()) {
      // VOD content (clips, DVR recording or completed): DON'T wait for MistServer
      // Use Gateway sources immediately - MistServer will cold start when player requests
      // Show loading only while waiting for Gateway sources (not MistServer)
      const sources = this.streamInfo?.source ?? [];
      return sources.length === 0;
    } else {
      // Live streams: Wait for MistServer online status
      if (!this.streamState?.isOnline || this.streamState?.status !== "ONLINE") {
        return true;
      }
      // Show loading if no stream info or sources
      if (!this.streamInfo || (this.streamInfo.source?.length ?? 0) === 0) {
        return true;
      }
    }

    return false;
  }

  /**
   * Get the effective content type for playback mode selection.
   * This ensures WHEP/WebRTC gets penalized for VOD content (no seek support)
   * while HLS/MP4 are preferred for clips and completed DVR recordings.
   */
  getEffectiveContentType(): "live" | "vod" {
    return this.isEffectivelyLive() ? "live" : "vod";
  }

  // ============================================================================
  // Hover/Controls Visibility (Phase A5b)
  // ============================================================================

  /** Check if user is currently hovering over the player */
  isHovering(): boolean {
    return this._isHovering;
  }

  /**
   * Check if controls should be visible.
   * Controls are visible when:
   * - User is hovering over the player
   * - Video is paused
   * - There's an error
   */
  shouldShowControls(): boolean {
    return this._isHovering || this.isPaused() || this._errorText !== null;
  }

  /**
   * Handle mouse enter event - show controls immediately.
   * Call this from your UI wrapper's onMouseEnter handler.
   */
  handleMouseEnter(): void {
    this.clearHoverTimeout();
    if (!this._isHovering) {
      this._isHovering = true;
      this.emit("hoverStart", undefined as never);
    }
  }

  /**
   * Handle mouse leave event - hide controls after delay.
   * Call this from your UI wrapper's onMouseLeave handler.
   */
  handleMouseLeave(): void {
    this.clearHoverTimeout();
    this._hoverTimeout = setTimeout(() => {
      if (this._isHovering) {
        this._isHovering = false;
        this.emit("hoverEnd", undefined as never);
      }
    }, PlayerController.HOVER_LEAVE_DELAY_MS);
  }

  /**
   * Handle mouse move event - show controls and reset hide timer.
   * Call this from your UI wrapper's onMouseMove handler.
   */
  handleMouseMove(): void {
    if (!this._isHovering) {
      this._isHovering = true;
      this.emit("hoverStart", undefined as never);
    }
    // Reset hide timeout on any movement
    this.clearHoverTimeout();
    this._hoverTimeout = setTimeout(() => {
      if (this._isHovering) {
        this._isHovering = false;
        this.emit("hoverEnd", undefined as never);
      }
    }, PlayerController.HOVER_HIDE_DELAY_MS);
  }

  /**
   * Handle touch start event - show controls.
   * Call this from your UI wrapper's onTouchStart handler.
   */
  handleTouchStart(): void {
    this.handleMouseEnter();
    // Reset hide timer for touch
    this.clearHoverTimeout();
    this._hoverTimeout = setTimeout(() => {
      if (this._isHovering) {
        this._isHovering = false;
        this.emit("hoverEnd", undefined as never);
      }
    }, PlayerController.HOVER_HIDE_DELAY_MS);
  }

  /** Clear hover timeout */
  private clearHoverTimeout(): void {
    if (this._hoverTimeout) {
      clearTimeout(this._hoverTimeout);
      this._hoverTimeout = null;
    }
  }

  /** Get current playback rate */
  getPlaybackRate(): number {
    return this.videoElement?.playbackRate ?? 1;
  }

  /** Get playback quality metrics from QualityMonitor */
  getPlaybackQuality(): PlaybackQuality | null {
    return this._playbackQuality;
  }

  /** Get current ABR mode */
  getABRMode(): ABRMode {
    return this.abrController?.getMode() ?? "auto";
  }

  /** Set ABR mode at runtime */
  setABRMode(mode: ABRMode): void {
    this.abrController?.setMode(mode);
  }

  // ============================================================================
  // Playback Control
  // ============================================================================

  /** Start playback */
  async play(): Promise<void> {
    if (this.currentPlayer?.play) {
      await this.currentPlayer.play();
      return;
    }
    if (this.videoElement) {
      await this.videoElement.play();
      return;
    }
    // No player or video element yet — queue for onReady
    this._pendingPlayIntent = true;
  }

  /** Pause playback */
  pause(): void {
    if (this.currentPlayer?.pause) {
      this.currentPlayer.pause();
      return;
    }
    this.videoElement?.pause();
  }

  /** Seek to time in milliseconds */
  seek(timeMs: number): void {
    this.qualityMonitor?.resetForSeek();
    const targetMs = this.clampSeekTarget(timeMs);

    // Use player-specific seek if available (for WebCodecs, MEWS, etc.)
    if (this.currentPlayer?.seek) {
      this.currentPlayer.seek(targetMs);
      return;
    }
    // Fallback to direct video element seek (convert ms → seconds at browser boundary)
    if (this.videoElement) {
      this.videoElement.currentTime = targetMs / 1000;
    }
  }

  /** Set volume (0-1). Dragging to 0 mutes, dragging above 0 unmutes. */
  setVolume(volume: number): void {
    if (!this.videoElement) return;

    const newVolume = Math.max(0, Math.min(1, volume));

    // Remember non-zero volumes for restore on unmute
    if (newVolume > 0) {
      this._lastVolume = newVolume;
    }

    // Dragging to 0 should mute, dragging above 0 should unmute
    const shouldMute = newVolume === 0;
    if (this.videoElement.muted !== shouldMute) {
      this.videoElement.muted = shouldMute;
      if (this.currentPlayer?.setMuted) {
        this.currentPlayer.setMuted(shouldMute);
      }
    }

    this.videoElement.volume = newVolume;
    this.emit("volumeChange", { volume: newVolume, muted: shouldMute });
  }

  /** Set muted state. Unmuting restores the previous volume. */
  setMuted(muted: boolean): void {
    if (!this.videoElement) return;

    if (muted) {
      // Save current volume before muting (if non-zero)
      if (this.videoElement.volume > 0) {
        this._lastVolume = this.videoElement.volume;
      }
    }

    if (this.currentPlayer?.setMuted) {
      this.currentPlayer.setMuted(muted);
    } else {
      this.videoElement.muted = muted;
    }

    // Restore volume when unmuting
    if (!muted && this.videoElement.volume === 0) {
      this.videoElement.volume = this._lastVolume;
    }

    this.emit("volumeChange", {
      volume: muted ? 0 : this.videoElement.volume,
      muted,
    });
  }

  /** Set playback rate */
  setPlaybackRate(rate: number): void {
    if (this.currentPlayer?.setPlaybackRate) {
      this.currentPlayer.setPlaybackRate(rate);
    } else if (this.videoElement) {
      this.videoElement.playbackRate = rate;
    }
    this.metaTrackManager?.sendSetSpeed(rate);
    this.emit("speedChange", { rate });
  }

  /** Jump to live edge (for live streams) */
  jumpToLive(): void {
    this.qualityMonitor?.resetForSeek();
    // Try player-specific implementation first (WebCodecs uses server time)
    if (this.currentPlayer?.jumpToLive) {
      this.currentPlayer.jumpToLive();
      this._isNearLive = true;
      this.emitSeekingState();
      return;
    }

    const el = this.videoElement;
    if (!el) return;

    // For WebRTC/MediaStream: we're always at live, nothing to do
    if (isMediaStreamSource(el)) {
      this._isNearLive = true;
      this.emitSeekingState();
      return;
    }

    // Try browser's seekable range first (most reliable for HLS/DASH/MEWS)
    if (el.seekable && el.seekable.length > 0) {
      const liveEdgeSec = el.seekable.end(el.seekable.length - 1);
      if (Number.isFinite(liveEdgeSec) && liveEdgeSec > 0) {
        el.currentTime = liveEdgeSec;
        this._isNearLive = true;
        this.emitSeekingState();
        return;
      }
    }

    // Try our computed live edge in ms (from MistServer metadata)
    if (this._liveEdge > 0 && Number.isFinite(this._liveEdge)) {
      el.currentTime = this._liveEdge / 1000;
      this._isNearLive = true;
      this.emitSeekingState();
      return;
    }

    // Fallback: seek to duration (for VOD or finite-duration live)
    if (Number.isFinite(el.duration) && el.duration > 0) {
      el.currentTime = el.duration;
      this._isNearLive = true;
      this.emitSeekingState();
    }
  }

  /** Emit current seeking state */
  private emitSeekingState(): void {
    this.emit("seekingStateChange", {
      seekableStart: this._seekableStart,
      liveEdge: this._liveEdge,
      canSeek: this._canSeek,
      isNearLive: this._isNearLive,
      isLive: this.isEffectivelyLive(),
      isWebRTC: this._isWebRTC,
      latencyTier: this._latencyTier,
      buffered: this._buffered,
      hasAudio: this._hasAudio,
      supportsPlaybackRate: this._supportsPlaybackRate,
    });
  }

  /** Request fullscreen */
  async requestFullscreen(): Promise<void> {
    if (this.container) {
      await this.container.requestFullscreen();
    }
  }

  /** Request Picture-in-Picture */
  async requestPiP(): Promise<void> {
    if (this.currentPlayer?.requestPiP) {
      await this.currentPlayer.requestPiP();
    } else if (this.videoElement && "requestPictureInPicture" in this.videoElement) {
      await (this.videoElement as any).requestPictureInPicture();
    }
  }

  /** Get available quality levels */
  getQualities(): Array<{
    id: string;
    label: string;
    bitrate?: number;
    width?: number;
    height?: number;
    isAuto?: boolean;
    active?: boolean;
  }> {
    return this.currentPlayer?.getQualities?.() ?? [];
  }

  /** Select a quality level */
  selectQuality(id: string): void {
    this.currentPlayer?.selectQuality?.(id);
  }

  /** Get available text tracks */
  getTextTracks(): Array<{ id: string; label: string; lang?: string; active: boolean }> {
    return this.currentPlayer?.getTextTracks?.() ?? [];
  }

  /** Select a text track */
  selectTextTrack(id: string | null): void {
    this.currentPlayer?.selectTextTrack?.(id);
  }

  /** Get available audio tracks */
  getAudioTracks(): Array<{ id: string; label: string; lang?: string; active: boolean }> {
    return this.currentPlayer?.getAudioTracks?.() ?? [];
  }

  /** Select an audio track by id */
  selectAudioTrack(id: string): void {
    this.currentPlayer?.selectAudioTrack?.(id);
  }

  /** Unified track listing: video qualities, audio tracks, and text tracks */
  getTracks(): Array<{
    id: string;
    kind: "video" | "audio" | "text";
    label: string;
    lang?: string;
    active: boolean;
    bitrate?: number;
    width?: number;
    height?: number;
  }> {
    const tracks: Array<{
      id: string;
      kind: "video" | "audio" | "text";
      label: string;
      lang?: string;
      active: boolean;
      bitrate?: number;
      width?: number;
      height?: number;
    }> = [];

    for (const q of this.getQualities()) {
      if (q.isAuto) continue;
      tracks.push({
        id: `video:${q.id}`,
        kind: "video",
        label: q.label,
        active: q.active ?? false,
        bitrate: q.bitrate,
        width: q.width,
        height: q.height,
      });
    }

    for (const a of this.getAudioTracks()) {
      tracks.push({
        id: `audio:${a.id}`,
        kind: "audio",
        label: a.label,
        lang: a.lang,
        active: a.active,
      });
    }

    for (const t of this.getTextTracks()) {
      tracks.push({
        id: `text:${t.id}`,
        kind: "text",
        label: t.label,
        lang: t.lang,
        active: t.active,
      });
    }

    return tracks;
  }

  /** Get current playback position in ms (prefers player override, falls back to video element) */
  private getEffectiveCurrentTime(): number {
    if (this.currentPlayer && typeof this.currentPlayer.getCurrentTime === "function") {
      const t = this.currentPlayer.getCurrentTime();
      if (Number.isFinite(t)) return t;
    }
    return (this.videoElement?.currentTime ?? 0) * 1000;
  }

  /** Get content duration in ms (prefers player override, falls back to video element) */
  private getEffectiveDuration(): number {
    if (this.currentPlayer && typeof this.currentPlayer.getDuration === "function") {
      const d = this.currentPlayer.getDuration();
      if (Number.isFinite(d) || d === Infinity) return d;
    }
    const raw = this.videoElement?.duration ?? NaN;
    if (!Number.isFinite(raw)) return raw; // preserve NaN/Infinity
    return raw * 1000;
  }

  private getPlayerSeekableRange(): { start: number; end: number } | null {
    if (this.currentPlayer && typeof this.currentPlayer.getSeekableRange === "function") {
      const range = this.currentPlayer.getSeekableRange();
      if (
        range &&
        Number.isFinite(range.start) &&
        Number.isFinite(range.end) &&
        range.end >= range.start
      ) {
        return range;
      }
    }
    return null;
  }

  private getFrameStepMsFromTracks(): number | undefined {
    const tracks = this.streamInfo?.meta?.tracks;
    if (!tracks || tracks.length === 0) return undefined;
    const videoTracks = tracks.filter(
      (t) => t.type === "video" && typeof t.fpks === "number" && t.fpks > 0
    );
    if (videoTracks.length === 0) return undefined;
    const fpks = Math.max(...videoTracks.map((t) => t.fpks as number));
    if (!Number.isFinite(fpks) || fpks <= 0) return undefined;
    // fpks = frames per kilosecond => frame duration in ms = 1000000 / fpks
    return 1000000 / fpks;
  }

  /** Get current time */
  getCurrentTime(): number {
    return this.getEffectiveCurrentTime();
  }

  /** Get duration */
  getDuration(): number {
    return this.getEffectiveDuration();
  }

  /** Check if paused */
  isPaused(): boolean {
    return this.currentPlayer?.isPaused?.() ?? this.videoElement?.paused ?? true;
  }

  /** Suppress play/pause-driven UI updates for a short window */
  suppressPlayPauseEvents(ms: number = 200): void {
    this.suppressPlayPauseEventsUntil = Date.now() + ms;
  }

  /** Check if play/pause UI updates should be suppressed */
  shouldSuppressVideoEvents(): boolean {
    return Date.now() < this.suppressPlayPauseEventsUntil;
  }

  /** Check if muted */
  isMuted(): boolean {
    return this.videoElement?.muted ?? false;
  }

  /** Skip backward by specified ms (default 10000ms = 10s) */
  skipBack(ms: number = 10000): void {
    this.seekBy(-ms);
    this.emit("skipBackward", { seconds: ms / 1000 });
  }

  /** Skip forward by specified ms (default 10000ms = 10s) */
  skipForward(ms: number = 10000): void {
    this.seekBy(ms);
    this.emit("skipForward", { seconds: ms / 1000 });
  }

  /** Toggle play/pause */
  togglePlay(): void {
    const isPaused = this.currentPlayer?.isPaused?.() ?? this.videoElement?.paused ?? true;
    this.log(`[togglePlay] isPaused=${isPaused}`);
    if (isPaused) {
      if (this.currentPlayer?.play) {
        this.currentPlayer.play().catch(() => {});
      } else {
        this.videoElement?.play().catch(() => {});
      }
      return;
    }
    if (this.currentPlayer?.pause) {
      this.currentPlayer.pause();
    } else {
      this.videoElement?.pause();
    }
  }

  /** Toggle mute */
  toggleMute(): void {
    if (this.videoElement) {
      this.setMuted(!this.videoElement.muted);
    }
  }

  /** Seek relative to current position (delta in ms) */
  seekBy(deltaMs: number): void {
    const currentTime = this.getEffectiveCurrentTime();
    this.seek(currentTime + deltaMs);
  }

  /** Seek to percentage (0-1) of duration */
  seekPercent(percent: number): void {
    const clampedPercent = Math.max(0, Math.min(1, percent));
    if (
      this._canSeek &&
      Number.isFinite(this._seekableStart) &&
      Number.isFinite(this._liveEdge) &&
      this._liveEdge > this._seekableStart
    ) {
      const span = this._liveEdge - this._seekableStart;
      this.seek(this._seekableStart + clampedPercent * span);
      return;
    }

    const duration = this.getEffectiveDuration();
    if (isFinite(duration)) {
      this.seek(duration * clampedPercent);
    }
  }

  /** Toggle loop mode */
  toggleLoop(): void {
    this._isLoopEnabled = !this._isLoopEnabled;
    if (this.videoElement) {
      this.videoElement.loop = this._isLoopEnabled;
    }
    // Persist to localStorage
    try {
      if (typeof localStorage !== "undefined") {
        localStorage.setItem("frameworks-player-loop", String(this._isLoopEnabled));
      }
    } catch {
      // localStorage not available
    }
    this.emit("loopChange", { isLoopEnabled: this._isLoopEnabled });
  }

  /** Set loop mode */
  setLoopEnabled(enabled: boolean): void {
    if (this._isLoopEnabled === enabled) return;
    this._isLoopEnabled = enabled;
    if (this.videoElement) {
      this.videoElement.loop = enabled;
    }
    try {
      if (typeof localStorage !== "undefined") {
        localStorage.setItem("frameworks-player-loop", String(enabled));
      }
    } catch {}
    this.emit("loopChange", { isLoopEnabled: enabled });
  }

  /** Clear current error */
  clearError(): void {
    this._errorText = null;
    this._isPassiveError = false;
    this._errorCleared = true;
  }

  // ============================================================================
  // Seeking & Live State Update (Centralized from wrappers)
  // ============================================================================

  /**
   * Update seeking and live detection state.
   * Called on timeupdate and progress events.
   * Emits seekingStateChange event when values change.
   */
  private updateSeekingState(): void {
    const el = this.videoElement;
    if (!el) return;

    const currentTime = this.getEffectiveCurrentTime();
    const duration = this.getEffectiveDuration();
    const isLive = this.isEffectivelyLive();
    const sourceType = this._currentSourceInfo?.type;
    const mistStreamInfo = this.streamState?.streamInfo;

    // Update WebRTC detection
    const wasWebRTC = this._isWebRTC;
    this._isWebRTC = isMediaStreamSource(el);

    // Update playback rate support
    this._supportsPlaybackRate = supportsPlaybackRate(el);

    // Update latency tier based on source type
    this._latencyTier = sourceType
      ? getLatencyTier(sourceType)
      : this._isWebRTC
        ? "ultra-low"
        : "medium";

    // Authoritative seek range from MistServer track data (computed once, reused below)
    const mistRange = this.getMistTrackSeekRange();
    const rawBufferWindow = mistStreamInfo?.meta?.buffer_window;
    const bufferWindowMs: number | undefined =
      typeof rawBufferWindow === "number" && rawBufferWindow > 0
        ? rawBufferWindow
        : mistRange
          ? mistRange.end - mistRange.start
          : undefined;
    this._liveThresholds = calculateLiveThresholds(sourceType, this._isWebRTC, bufferWindowMs);

    // Calculate seekable range using centralized logic (allow player overrides)
    const playerRange = this.getPlayerSeekableRange();

    // Log seeking inputs on first calculation and when values change
    if (isLive && !this._seekingLoggedOnce) {
      this._seekingLoggedOnce = true;
      this.log(
        `[Seeking] initial state: isLive=${isLive} sourceType=${sourceType} ` +
          `bufferWindowMs=${bufferWindowMs} ` +
          `playerRange=${JSON.stringify(playerRange)} ` +
          `video.seekable.length=${el.seekable?.length ?? 0} ` +
          `hasMistStreamInfo=${!!mistStreamInfo} ` +
          `trackCount=${mistStreamInfo?.meta?.tracks ? Object.keys(mistStreamInfo.meta.tracks).length : 0}`
      );
    }
    const allowMediaStreamDvr =
      isMediaStreamSource(el) && bufferWindowMs !== undefined && bufferWindowMs > 0;
    // Pass shifted buffered start so the fallback formula uses the same coordinate
    // space as liveEdge (both include lastms shift from NativePlayer)
    const bufferedRanges = this.currentPlayer?.getBufferedRanges?.() ?? null;
    const bufferedStartMs =
      bufferedRanges && bufferedRanges.length > 0 ? bufferedRanges.start(0) * 1000 : undefined;

    const initialSeekRange = playerRange
      ? { seekableStart: playerRange.start, liveEdge: playerRange.end }
      : calculateSeekableRange({
          isLive,
          video: el,
          mistStreamInfo,
          currentTime,
          duration,
          allowMediaStreamDvr,
          bufferedStartMs,
        });
    let seekableStart = initialSeekRange.seekableStart;
    const liveEdge = initialSeekRange.liveEdge;

    // Mist buffer_window is authoritative for DVR size. If player APIs report an
    // ever-growing range (common with some live playlists), clamp to a sliding window.
    if (
      isLive &&
      bufferWindowMs !== undefined &&
      bufferWindowMs > 0 &&
      Number.isFinite(seekableStart) &&
      Number.isFinite(liveEdge) &&
      liveEdge > seekableStart
    ) {
      const reportedWindow = liveEdge - seekableStart;
      const allowedWindow = bufferWindowMs * 1.25;
      if (reportedWindow > allowedWindow) {
        seekableStart = Math.max(0, liveEdge - bufferWindowMs);
      }
    }

    // Push authoritative seek window to the player for anchor-based coordinates.
    // mistRange already incorporates buffer_window for a stable DVR width.
    if (this.currentPlayer?.setSeekableRangeHint) {
      if (isLive && mistRange && mistRange.end > mistRange.start) {
        this.currentPlayer.setSeekableRangeHint(mistRange);
      } else if (
        isLive &&
        Number.isFinite(seekableStart) &&
        Number.isFinite(liveEdge) &&
        liveEdge > seekableStart
      ) {
        this.currentPlayer.setSeekableRangeHint({ start: seekableStart, end: liveEdge });
      } else {
        this.currentPlayer.setSeekableRangeHint(null);
      }
    }

    // Update can seek - pass player's canSeek if available (e.g., WebCodecs uses server commands)
    const playerCanSeek =
      this.currentPlayer && typeof (this.currentPlayer as any).canSeek === "function"
        ? () => (this.currentPlayer as any).canSeek()
        : undefined;
    const prevCanSeek = this._canSeek;
    this._canSeek = canSeekStream({
      video: el,
      isLive,
      duration,
      bufferWindowMs,
      playerCanSeek,
      playerSeekableRange: playerRange,
    });

    if (this._canSeek !== prevCanSeek) {
      this.log(
        `[Seeking] canSeek changed: ${prevCanSeek} -> ${this._canSeek} ` +
          `bufferWindowMs=${bufferWindowMs} seekableRange=[${seekableStart.toFixed(1)}, ${liveEdge.toFixed(1)}] ` +
          `playerCanSeek=${playerCanSeek ? playerCanSeek() : "n/a"}`
      );
    }

    // Update buffered ranges
    const correctedBuffered = this.getBufferedRanges();
    this._buffered = correctedBuffered && correctedBuffered.length > 0 ? correctedBuffered : null;

    // Check if values changed
    const seekableChanged = this._seekableStart !== seekableStart || this._liveEdge !== liveEdge;

    this._seekableStart = seekableStart;
    this._liveEdge = liveEdge;

    // Update interaction controller live-only state (allow DVR shortcuts when seekable window exists)
    const hasDvrWindow =
      isLive &&
      Number.isFinite(liveEdge) &&
      Number.isFinite(seekableStart) &&
      liveEdge > seekableStart;
    const isLiveOnly = isLive && !hasDvrWindow;
    const frameStepMs = this.getFrameStepMsFromTracks();
    this.interactionController?.updateConfig({
      isLive: isLiveOnly,
      frameStepSeconds: frameStepMs !== undefined ? frameStepMs / 1000 : undefined,
    });

    // Update isNearLive using hysteresis
    if (isLive) {
      const newIsNearLive = calculateIsNearLive(
        currentTime,
        liveEdge,
        this._liveThresholds,
        this._isNearLive
      );
      if (newIsNearLive !== this._isNearLive) {
        this._isNearLive = newIsNearLive;
      }
    } else {
      this._isNearLive = true; // Always "at live" for VOD
    }

    // Emit event for wrappers to consume
    // Only emit if something meaningful changed to avoid spam
    if (seekableChanged || wasWebRTC !== this._isWebRTC) {
      this.emit("seekingStateChange", {
        seekableStart: this._seekableStart,
        liveEdge: this._liveEdge,
        canSeek: this._canSeek,
        isNearLive: this._isNearLive,
        isLive,
        isWebRTC: this._isWebRTC,
        latencyTier: this._latencyTier,
        buffered: this._buffered,
        hasAudio: this._hasAudio,
        supportsPlaybackRate: this._supportsPlaybackRate,
      });
    }
  }

  /**
   * Detect audio tracks on the video element.
   * Called after video metadata is loaded.
   */
  private unbindMediaStreamAudioListeners(): void {
    if (this._boundMediaStreamForAudio && this._onMediaStreamTrackChange) {
      this._boundMediaStreamForAudio.removeEventListener(
        "addtrack",
        this._onMediaStreamTrackChange
      );
      this._boundMediaStreamForAudio.removeEventListener(
        "removetrack",
        this._onMediaStreamTrackChange
      );
    }
    this._boundMediaStreamForAudio = null;
    this._onMediaStreamTrackChange = null;
  }

  private detectAudioTracks(): void {
    const el = this.videoElement;
    if (!el) {
      this.unbindMediaStreamAudioListeners();
      return;
    }

    // MediaStream: bind track change listeners (WebRTC tracks arrive async)
    if (el.srcObject instanceof MediaStream) {
      const stream = el.srcObject;
      if (stream !== this._boundMediaStreamForAudio) {
        this.unbindMediaStreamAudioListeners();
        this._boundMediaStreamForAudio = stream;
        this._onMediaStreamTrackChange = () => {
          this._hasAudio = stream.getAudioTracks().length > 0;
          this.emitSeekingState();
        };
        stream.addEventListener("addtrack", this._onMediaStreamTrackChange);
        stream.addEventListener("removetrack", this._onMediaStreamTrackChange);
      }
      this._hasAudio = stream.getAudioTracks().length > 0;
      this.emitSeekingState();
      return;
    }

    this.unbindMediaStreamAudioListeners();

    // Fallback: trust MistServer stream metadata for non-MediaStream sources.
    const mistHasAudio = this.streamState?.streamInfo?.hasAudio;
    if (mistHasAudio !== undefined) {
      this._hasAudio = mistHasAudio;
      this.emitSeekingState();
      return;
    }

    // Check HTML5 audio tracks (if available)
    // audioTracks is only available in some browsers (Safari, Edge)
    const elWithAudio = el as HTMLVideoElement & { audioTracks?: { length: number } };
    if (elWithAudio.audioTracks && elWithAudio.audioTracks.length !== undefined) {
      this._hasAudio = elWithAudio.audioTracks.length > 0;
      this.emitSeekingState();
      return;
    }

    // Default to true if we can't detect
    this._hasAudio = true;
    this.emitSeekingState();
  }

  // ============================================================================
  // Error Handling (Phase A3)
  // ============================================================================

  /**
   * Attempt to clear error automatically if playback is progressing.
   * Called on timeupdate, playing, and canplay events.
   * Mirrors ddvtech: auto-dismiss on playback resume with 2s cooldown.
   */
  private attemptClearError(): void {
    if (!this._errorText || this._errorCleared) return;

    // Mark that playback has resumed since the error was shown
    this._playbackResumedSinceError = true;

    const now = Date.now();
    const elapsed = now - this._errorShownAt;

    // Clear if enough time has passed AND playback resumed
    if (elapsed >= PlayerController.AUTO_CLEAR_ERROR_DELAY_MS && this._playbackResumedSinceError) {
      this._errorCleared = true;
      this._errorText = null;
      this._isPassiveError = false;
      this._playbackResumedSinceError = false;
      this.log("Error auto-cleared after playback resumed");
      this.emit("errorCleared", undefined as never);
    }
  }

  /**
   * Check if we should attempt playback fallback due to hard failure.
   * Returns true if:
   * - Error count exceeds threshold (5+) within time window (60s)
   * - Error contains fatal keywords
   * - Sustained stall for 30+ seconds
   */
  private shouldAttemptFallback(error: string): boolean {
    const now = Date.now();

    // Track error count within window
    if (now - this._lastErrorTime > PlayerController.HARD_FAILURE_ERROR_WINDOW_MS) {
      this._errorCount = 0; // Reset counter if outside window
    }
    this._errorCount++;
    this._lastErrorTime = now;

    // Check for repeated errors (5+ errors within 60s)
    if (this._errorCount >= PlayerController.HARD_FAILURE_ERROR_THRESHOLD) {
      this.log(`Hard failure: repeated errors (${this._errorCount})`);
      return true;
    }

    // Check for fatal error keywords
    const lowerError = error.toLowerCase();
    for (const keyword of PlayerController.FATAL_ERROR_KEYWORDS) {
      if (lowerError.includes(keyword)) {
        this.log(`Hard failure: fatal keyword "${keyword}" detected`);
        return true;
      }
    }

    // Check for sustained stall (30+ seconds of continuous buffering)
    if (this._stallStartTime > 0) {
      const stallDuration = now - this._stallStartTime;
      if (stallDuration >= PlayerController.HARD_FAILURE_STALL_THRESHOLD_MS) {
        this.log(`Hard failure: sustained stall for ${stallDuration}ms`);
        return true;
      }
    }

    return false;
  }

  /**
   * Set error with passive mode support.
   * - Ignores errors during player transitions
   * - Marks error as passive if video is still playing
   * - Attempts automatic fallback on hard failures
   */
  async setPassiveError(error: string): Promise<void> {
    // Ignore errors during player switching transitions (old player cleanup can fire errors)
    if (this._isTransitioning) {
      this.log(`Ignoring error during player transition: ${error}`);
      return;
    }

    // Suppress errors during retry window to prevent popup flash-back
    if (Date.now() < this._retrySuppressUntil) {
      this.log(`Ignoring error during retry suppression window: ${error}`);
      return;
    }

    // When live stream is offline, prefer idle/offline UI over blocking error overlays.
    if (
      this.isEffectivelyLive() &&
      this.streamState?.isOnline === false &&
      /offline|not found|stream not found/i.test(error)
    ) {
      this.log(`Suppressing offline error while stream is offline: ${error}`);
      return;
    }

    // Check if video is still playing (passive error scenario)
    const video = this.videoElement;
    const isVideoPlaying = video && !video.paused && video.currentTime > 0;

    // Attempt fallback on hard failures before showing error UI
    if (this.shouldAttemptFallback(error) && this.playerManager.canAttemptFallback()) {
      this.log("Attempting playback fallback...");
      this._isTransitioning = true;

      const fallbackSucceeded = await this.playerManager.tryPlaybackFallback();

      this._isTransitioning = false;

      if (fallbackSucceeded) {
        // Fallback succeeded - clear error state and reset counters
        this._errorCount = 0;
        this._errorText = null;
        this._isPassiveError = false;
        this.qualityMonitor?.resetFallbackState();
        this.log("Fallback succeeded");
        return;
      }
      // Fallback failed or exhausted - fall through to show error
      this.log("Fallback exhausted, showing error UI");
    }

    // Set error state
    this._errorShownAt = Date.now();
    this._errorCleared = false;
    this._playbackResumedSinceError = false;
    this._errorText = error;
    this._isPassiveError = isVideoPlaying ?? false;

    this.setState("error", { error });
    this.emit("error", { error });
  }

  /** Check if a fallback player/source combo is available */
  canAttemptFallback(): boolean {
    return this.playerManager.canAttemptFallback();
  }

  /**
   * Retry playback with fallback to next player/source.
   * Returns true if a fallback option was available and attempted.
   */
  async retryWithFallback(): Promise<boolean> {
    if (!this.playerManager.canAttemptFallback()) {
      return false;
    }

    this._retrySuppressUntil = Date.now() + PlayerController.RETRY_SUPPRESS_MS;
    this._isTransitioning = true;
    const success = await this.playerManager.tryPlaybackFallback();
    this._isTransitioning = false;

    if (success) {
      this._errorCount = 0;
      this.clearError();
      this.qualityMonitor?.resetFallbackState();
    }

    return success;
  }

  /** Toggle fullscreen (with screen orientation lock on mobile) */
  async toggleFullscreen(): Promise<void> {
    if (typeof document === "undefined") return;

    if (document.fullscreenElement) {
      try {
        (screen.orientation as any).unlock?.();
      } catch {}
      await document.exitFullscreen().catch(() => {});
    } else if (this.container) {
      await this.container.requestFullscreen().catch(() => {});
      try {
        await (screen.orientation as any).lock?.("landscape");
      } catch {}
    }
  }

  /** Toggle Picture-in-Picture */
  async togglePictureInPicture(): Promise<void> {
    if (typeof document === "undefined") return;

    if (document.pictureInPictureElement) {
      await document.exitPictureInPicture().catch(() => {});
    } else if (this.videoElement && "requestPictureInPicture" in this.videoElement) {
      await (this.videoElement as any).requestPictureInPicture().catch(() => {});
    }
  }

  /** Check if Picture-in-Picture is supported */
  isPiPSupported(): boolean {
    if (typeof document === "undefined") return false;
    return document.pictureInPictureEnabled ?? false;
  }

  /** Check if currently in Picture-in-Picture mode */
  isPiPActive(): boolean {
    if (typeof document === "undefined") return false;
    return document.pictureInPictureElement === this.videoElement;
  }

  // ============================================================================
  // Advanced Control
  // ============================================================================

  private static readonly RETRY_SUPPRESS_MS = 2000;

  /** Force a retry of the current playback */
  async retry(): Promise<void> {
    if (!this.container || !this.streamInfo) return;
    this._retrySuppressUntil = Date.now() + PlayerController.RETRY_SUPPRESS_MS;

    try {
      this.playerManager.destroy();
    } catch {
      // Ignore cleanup errors
    }

    this.container.innerHTML = "";
    this.videoElement = null;
    this.currentPlayer = null;

    try {
      await this.initializePlayer();
    } catch (error) {
      const message = error instanceof Error ? error.message : "Retry failed";
      this.setState("error", { error: message });
      this.emit("error", { error: message });
    }
  }

  /** Get playback statistics */
  async getStats(): Promise<unknown> {
    return this.currentPlayer?.getStats?.();
  }

  /** Get current latency (for live streams) */
  async getLatency(): Promise<unknown> {
    return this.currentPlayer?.getLatency?.();
  }

  /** Capture a screenshot as a data URL */
  snapshot(type?: "png" | "jpeg" | "webp", quality?: number): string | null {
    return this.currentPlayer?.snapshot?.(type, quality) ?? null;
  }

  /** Set video rotation (0, 90, 180, 270 degrees) */
  setRotation(degrees: number): void {
    this.currentPlayer?.setRotation?.(degrees);
  }

  /** Set mirror/flip */
  setMirror(horizontal: boolean): void {
    this.currentPlayer?.setMirror?.(horizontal);
  }

  /** Whether the current player uses direct rendering (WebGL/Canvas) */
  isDirectRendering(): boolean {
    return this.currentPlayer?.isDirectRendering ?? false;
  }

  // ============================================================================
  // Runtime Configuration (Phase A5)
  // ============================================================================

  /**
   * Update configuration at runtime without full re-initialization.
   * Only certain options can be updated without re-init.
   */
  updateConfig(
    partialConfig: Partial<Pick<PlayerControllerConfig, "debug" | "autoplay" | "muted">>
  ): void {
    if (partialConfig.debug !== undefined) {
      this.config.debug = partialConfig.debug;
    }
    if (partialConfig.autoplay !== undefined) {
      this.config.autoplay = partialConfig.autoplay;
    }
    if (partialConfig.muted !== undefined) {
      this.config.muted = partialConfig.muted;
      if (this.videoElement) {
        this.videoElement.muted = partialConfig.muted;
      }
    }
  }

  /**
   * Force a complete re-initialization with current config.
   * Stops and re-initializes the entire player.
   */
  async reload(): Promise<void> {
    if (!this.container || this.isDestroyed) return;
    this._retrySuppressUntil = Date.now() + PlayerController.RETRY_SUPPRESS_MS;

    const container = this.container;
    this.detach();
    await this.attach(container);
  }

  /**
   * Select a specific player/source combination (one-shot).
   * Used by DevModePanel to manually pick a combo.
   *
   * Note: This is a ONE-SHOT selection. The force settings are used for
   * the next initialization only. If that player fails, normal fallback
   * logic proceeds without the force settings.
   */
  async selectCombo(options: {
    forcePlayer?: string;
    forceType?: string;
    forceSource?: number;
  }): Promise<void> {
    const container = this.container;
    if (!container) return;

    this.log(
      `[selectCombo] One-shot selection: player=${options.forcePlayer}, type=${options.forceType}, source=${options.forceSource}`
    );

    // Store as one-shot options (will be cleared after use)
    this._pendingForceOptions = {
      forcePlayer: options.forcePlayer,
      forceType: options.forceType,
      forceSource: options.forceSource,
    };

    // Detach and re-attach - initializePlayer will use pending options once
    this.detach();
    await this.attach(container);
  }

  /**
   * Set playback mode preference.
   * Unlike selectCombo, this is a persistent preference that affects scoring.
   */
  setPlaybackMode(mode: "auto" | "low-latency" | "quality" | "vod"): void {
    this.config.playbackMode = mode;
    this.log(`[setPlaybackMode] Mode set to: ${mode}`);
  }

  /**
   * @deprecated Use selectCombo() for one-shot selection or setPlaybackMode() for mode changes.
   * This method exists for backwards compatibility but may override fallback behavior.
   */
  async setDevModeOptions(options: {
    forcePlayer?: string;
    forceType?: string;
    forceSource?: number;
    playbackMode?: "auto" | "low-latency" | "quality" | "vod";
  }): Promise<void> {
    // Update playback mode if provided (this is a persistent preference)
    if (options.playbackMode) {
      this.setPlaybackMode(options.playbackMode);
    }

    // Use selectCombo for the force settings (one-shot)
    if (
      options.forcePlayer !== undefined ||
      options.forceType !== undefined ||
      options.forceSource !== undefined
    ) {
      await this.selectCombo({
        forcePlayer: options.forcePlayer,
        forceType: options.forceType,
        forceSource: options.forceSource,
      });
    } else if (options.playbackMode) {
      // Mode-only change, trigger reload
      const container = this.container;
      if (container) {
        this.detach();
        await this.attach(container);
      }
    }
  }

  /**
   * Get metadata update payload for external consumers.
   * Combines current state into a single metadata object.
   */
  getMetadataPayload(): PlayerControllerEvents["metadataUpdate"] {
    const video = this.videoElement;
    const bufferedAheadMs =
      video && video.buffered.length > 0
        ? (video.buffered.end(video.buffered.length - 1) - video.currentTime) * 1000
        : 0;

    return {
      currentTime: this.getEffectiveCurrentTime(),
      duration: this.getEffectiveDuration(),
      bufferedAhead: Math.max(0, bufferedAheadMs),
      qualityScore: this._playbackQuality?.score,
      playerInfo: this._currentPlayerInfo ?? undefined,
      sourceInfo: this._currentSourceInfo ?? undefined,
      isLive: this.isEffectivelyLive(),
      isBuffering: this._isBuffering,
      isPaused: video?.paused ?? true,
      volume: video?.volume ?? 1,
      muted: video?.muted ?? false,
    };
  }

  /**
   * Emit a metadata update event with current state.
   * Useful for periodic telemetry/reporting.
   */
  emitMetadataUpdate(): void {
    this.emit("metadataUpdate", this.getMetadataPayload());
  }

  // ============================================================================
  // Private Methods
  // ============================================================================

  private async resolveEndpoints(): Promise<void> {
    const { endpoints, gatewayUrl, mistUrl, contentId, authToken } = this.config;

    // Priority 1: Use pre-resolved endpoints if provided
    if (endpoints?.primary) {
      this.endpoints = endpoints;
      this.setMetadataSeed(endpoints.metadata ?? null);
      this.setState("gateway_ready", { gatewayStatus: "ready" });
      return;
    }

    // Priority 2: Direct MistServer resolution (playground/standalone mode)
    if (mistUrl) {
      await this.resolveFromMistServer(mistUrl, contentId);
      return;
    }

    // Priority 3: Gateway resolution
    if (gatewayUrl) {
      await this.resolveFromGateway(gatewayUrl, contentId, authToken);
      return;
    }

    throw new Error("No endpoints provided and no gatewayUrl or mistUrl configured");
  }

  /**
   * Resolve endpoints directly from MistServer (bypasses Gateway)
   * Fetches json_{contentId}.js and builds ContentEndpoints from source array
   */
  private async resolveFromMistServer(mistUrl: string, contentId: string): Promise<void> {
    this.setState("gateway_loading", { gatewayStatus: "loading" });

    try {
      let baseUrl = mistUrl;
      while (baseUrl.endsWith("/")) baseUrl = baseUrl.slice(0, -1);
      const jsonUrl = `${baseUrl}/json_${encodeURIComponent(contentId)}.js?metaeverywhere=1&inclzero=1`;
      this.log(`[resolveFromMistServer] Fetching ${jsonUrl}`);

      const response = await fetch(jsonUrl, { cache: "no-store" });
      if (!response.ok) {
        throw new Error(`MistServer HTTP ${response.status}`);
      }

      // MistServer can return JSONP: callback({...}); - strip wrapper if present
      let text = await response.text();
      const jsonpMatch = text.match(/^[^(]+\(([\s\S]*)\);?$/);
      if (jsonpMatch) {
        text = jsonpMatch[1];
      }
      const data = JSON.parse(text);

      if (data.error) {
        throw new Error(data.error);
      }

      const mistDatachannelsRaw = data?.capa?.datachannels;
      const mistDatachannels =
        mistDatachannelsRaw === undefined ? undefined : Boolean(mistDatachannelsRaw);
      this.log(
        `[resolveFromMistServer] capa.datachannels=${String(mistDatachannelsRaw)} normalized=${String(mistDatachannels)}`
      );
      const rawSources: StreamSource[] = Array.isArray(data.source)
        ? data.source.map((s: { url: string; type: string }) => ({
            ...s,
            mistDatachannels,
          }))
        : [];
      if (rawSources.length === 0) {
        throw new Error("No sources available from MistServer");
      }

      let tracks: StreamTrack[] = [];
      if (data.meta?.tracks && typeof data.meta.tracks === "object") {
        tracks = this.parseMistTracks(data.meta.tracks);
        this.mistTracks = tracks.length > 0 ? tracks : null;
        this.log(`[resolveFromMistServer] Parsed ${tracks.length} tracks from MistServer`);
      }
      if (tracks.length === 0) {
        tracks = [{ type: "video", codec: "H264", codecstring: "avc1.42E01E" }];
      }

      this.streamInfo = {
        source: rawSources as StreamSource[],
        meta: { tracks },
        type: data.type === "vod" ? "vod" : "live",
      };

      const httpSources = rawSources.filter((s) => !s.url.startsWith("ws://"));
      const primarySource =
        httpSources.length > 0 ? this.selectBestSource(httpSources) : rawSources[0];

      this.endpoints = {
        primary: {
          nodeId: `mist-${contentId}`,
          protocol: this.mapMistTypeToProtocol(primarySource.type),
          url: primarySource.url,
          baseUrl: mistUrl,
          outputs: {},
        },
        fallbacks: [],
      };
      this.setMetadataSeed(null);

      this.setState("gateway_ready", { gatewayStatus: "ready" });
      this.log(`[resolveFromMistServer] ${rawSources.length} sources, ${tracks.length} tracks`);
    } catch (error) {
      const message = error instanceof Error ? error.message : "MistServer resolution failed";
      this.setState("gateway_error", { gatewayStatus: "error", error: message });
      throw error;
    }
  }

  /**
   * Map MistServer type to protocol identifier
   */
  private mapMistTypeToProtocol(mistType: string): string {
    // WebCodecs raw streams - check BEFORE generic ws/ catch-all
    if (mistType === "ws/video/raw") return "RAW_WS";
    if (mistType === "wss/video/raw") return "RAW_WSS";
    // Annex B H264 over WebSocket (video-only, uses same 12-byte header as raw)
    if (mistType === "ws/video/h264") return "H264_WS";
    if (mistType === "wss/video/h264") return "H264_WSS";
    // WebM over WebSocket - check BEFORE generic ws/ catch-all
    if (mistType === "ws/video/webm") return "MEWS_WEBM";
    if (mistType === "wss/video/webm") return "MEWS_WEBM_SSL";
    // MEWS MP4 over WebSocket - catch remaining ws/* types (defaults to mp4)
    if (mistType.startsWith("ws/")) return "MEWS_WS";
    if (mistType.startsWith("wss/")) return "MEWS_WSS";
    if (mistType.includes("webrtc")) return "MIST_WEBRTC";
    if (mistType.includes("mpegurl") || mistType.includes("m3u8")) return "HLS";
    if (mistType.includes("dash") || mistType.includes("mpd")) return "DASH";
    if (mistType.includes("whep")) return "WHEP";
    if (mistType.includes("mp4")) return "MP4";
    if (mistType.includes("webm")) return "WEBM";
    return mistType;
  }

  /**
   * Select best source based on protocol priority
   */
  private selectBestSource(sources: Array<{ url: string; type: string }>): {
    url: string;
    type: string;
  } {
    const priority: Record<string, number> = {
      HLS: 1,
      DASH: 2,
      MP4: 3,
      WEBM: 4,
      WHEP: 5,
      MIST_WEBRTC: 6,
      MEWS_WS: 99,
    };
    return sources.sort((a, b) => {
      const pa = priority[this.mapMistTypeToProtocol(a.type)] ?? 50;
      const pb = priority[this.mapMistTypeToProtocol(b.type)] ?? 50;
      return pa - pb;
    })[0];
  }

  /**
   * Resolve endpoints from Gateway GraphQL API
   */
  private async resolveFromGateway(
    gatewayUrl: string,
    contentId: string,
    authToken?: string
  ): Promise<void> {
    this.setState("gateway_loading", { gatewayStatus: "loading" });

    this.gatewayClient = new GatewayClient({
      gatewayUrl,
      contentId,
      authToken,
    });

    // Subscribe to status changes
    const unsub = this.gatewayClient.on("statusChange", ({ status, error }) => {
      if (status === "error") {
        this.setState("gateway_error", { gatewayStatus: status, error });
      }
    });
    this.cleanupFns.push(unsub);
    this.cleanupFns.push(() => this.gatewayClient?.destroy());

    try {
      this.endpoints = await this.gatewayClient.resolve();
      this.setMetadataSeed(this.endpoints?.metadata ?? null);
      this.setState("gateway_ready", { gatewayStatus: "ready" });
    } catch (error) {
      const message = error instanceof Error ? error.message : "Gateway resolution failed";
      this.setState("gateway_error", { gatewayStatus: "error", error: message });
      throw error;
    }
  }

  private startStreamStatePolling(): void {
    const { contentId, mistUrl } = this.config;
    const contentType = this.getResolvedContentType();

    // Only poll for live-like content. DVR should only poll while recording.
    // If contentType is unknown but mistUrl is provided, still poll so we can
    // detect when a stream comes online and initialize playback.
    if (contentType == null) {
      if (!mistUrl) return;
    } else if (contentType !== "live" && contentType !== "dvr") {
      return;
    }
    if (contentType === "dvr") {
      const dvrStatus = this.getMetadata()?.dvrStatus;
      if (dvrStatus && dvrStatus !== "recording") return;
    }

    // Use endpoint baseUrl if available, otherwise fall back to config.mistUrl
    // This allows polling to start even when initial endpoint resolution failed
    const mistBaseUrl = this.endpoints?.primary?.baseUrl || mistUrl;
    if (!mistBaseUrl) return;

    // Use playback ID from metadata if available
    const metadata = this.getMetadata();
    const streamName = metadata?.contentId || contentId;

    // For effectively live content, use WebSocket for real-time updates
    // For completed VOD content, use HTTP polling only
    const useWebSocket = this.isEffectivelyLive();
    const pollInterval = this.isEffectivelyLive() ? 3000 : 5000;

    this.streamStateClient = new StreamStateClient({
      mistBaseUrl,
      streamName,
      useWebSocket,
      pollInterval,
    });

    // Subscribe to state changes
    const unsubState = this.streamStateClient.on("stateChange", ({ state }) => {
      const wasOnline = this._prevStreamIsOnline;
      const isNowOnline = state.isOnline;

      this.streamState = state;
      this._prevStreamIsOnline = isNowOnline;

      // Update track metadata if MistServer provides better data
      // This handles cold-start: Gateway gives fallback codecs, MistServer gives real ones
      if (state.streamInfo?.meta?.tracks && this.streamInfo) {
        const mistTracks = this.parseMistTracks(state.streamInfo.meta.tracks);
        if (mistTracks.length > 0) {
          this.streamInfo.meta.tracks = mistTracks;
          this.log(`[stateChange] Updated ${mistTracks.length} tracks from MistServer`);

          // Recalculate seeking state with new track data — video events may not
          // fire if the video is stalled, so we must trigger this explicitly
          this.updateSeekingState();
        }
      }

      // Merge Mist metadata into the unified metadata surface
      this.refreshMergedMetadata();

      this.emit("streamStateChange", { state });

      // Auto-play when stream transitions from offline to online
      // This handles the case where user is watching IdleScreen and stream comes online
      if (wasOnline === false && isNowOnline === true && this.isEffectivelyLive()) {
        // Clear any stale UI error from the offline phase, including errors that
        // were emitted directly (outside setPassiveError) and only exist in wrapper state.
        this.clearError();
        this.emit("errorCleared", undefined as never);
        this.log("Stream came online, triggering auto-play");
        if (this.videoElement) {
          // Player already initialized - just play
          this.videoElement
            .play()
            .catch((e) => this.log(`Auto-play on online transition failed: ${e}`));
        } else if (this.container && !this.endpoints?.primary) {
          // Player wasn't initialized because stream was offline - re-attempt full initialization
          this.log("Stream came online, attempting late initialization");
          this.initializeLateFromStreamState(state.streamInfo);
        }
      }
    });
    this.cleanupFns.push(unsubState);
    this.cleanupFns.push(() => {
      this.streamStateClient?.destroy();
      this.streamStateClient = null;
    });

    this.streamStateClient.start();
  }

  /**
   * Initialize player late when stream comes online after initial attach failed.
   * Uses MistStreamInfo from stream state polling instead of re-fetching.
   */
  private async initializeLateFromStreamState(
    streamInfo: MistStreamInfo | undefined
  ): Promise<void> {
    if (
      !streamInfo?.source ||
      !Array.isArray(streamInfo.source) ||
      streamInfo.source.length === 0
    ) {
      this.log("[initializeLateFromStreamState] No sources in stream info");
      return;
    }

    if (!this.container || !this.config.mistUrl) {
      this.log("[initializeLateFromStreamState] Missing container or mistUrl");
      return;
    }

    try {
      const sources = streamInfo.source;
      const mistUrl = this.config.mistUrl;
      const contentId = this.config.contentId;

      let tracks: StreamTrack[] = [];
      if (streamInfo.meta?.tracks && typeof streamInfo.meta.tracks === "object") {
        tracks = this.parseMistTracks(streamInfo.meta.tracks);
        this.mistTracks = tracks.length > 0 ? tracks : null;
        this.log(`[initializeLateFromStreamState] Parsed ${tracks.length} tracks`);
      }
      if (tracks.length === 0) {
        tracks = [{ type: "video", codec: "H264", codecstring: "avc1.42E01E" }];
      }

      this.streamInfo = {
        source: sources as StreamSource[],
        meta: { tracks },
        type: streamInfo.type === "vod" ? "vod" : "live",
      };

      const httpSources = sources.filter((s) => !s.url.startsWith("ws://"));
      const primarySource =
        httpSources.length > 0 ? this.selectBestSource(httpSources) : sources[0];

      this.endpoints = {
        primary: {
          nodeId: `mist-${contentId}`,
          protocol: this.mapMistTypeToProtocol(primarySource.type),
          url: primarySource.url,
          baseUrl: mistUrl,
          outputs: {},
        },
        fallbacks: [],
      };
      this.setMetadataSeed(this.endpoints.metadata ?? null);

      this.setState("gateway_ready", { gatewayStatus: "ready" });
      this.log(
        `[initializeLateFromStreamState] ${sources.length} sources, ${tracks.length} tracks`
      );

      if (!this.streamInfo || this.streamInfo.source.length === 0) {
        this.setState("error", { error: "No playable sources found" });
        return;
      }

      await this.initializePlayer();
      this.log("[initializeLateFromStreamState] Player initialized successfully");
    } catch (error) {
      const message = error instanceof Error ? error.message : "Late initialization failed";
      this.log(`[initializeLateFromStreamState] Failed: ${message}`);
      this.setState("error", { error: message });
    }
  }

  private buildStreamInfo(endpoints: ContentEndpoints): StreamInfo | null {
    // Delegate to standalone exported function
    const info = buildStreamInfoFromEndpoints(endpoints, this.config.contentId);

    // If we have tracks from direct MistServer resolution, use those instead
    // (they have accurate codecstring and init data for proper codec detection)
    if (info && this.mistTracks && this.mistTracks.length > 0) {
      info.meta.tracks = this.mistTracks;
      this.log(`[buildStreamInfo] Using ${this.mistTracks.length} tracks from MistServer`);
    }

    return info;
  }

  /**
   * Parse MistServer track metadata from the tracks object.
   * MistServer returns tracks as a Record keyed by track name (e.g., "video_H264_800x600_25fps_1").
   * This converts to our StreamTrack[] format with codecstring and init data.
   */
  private parseMistTracks(tracksObj: Record<string, unknown>): StreamTrack[] {
    const tracks: StreamTrack[] = [];
    for (const [, trackData] of Object.entries(tracksObj)) {
      const t = trackData as Record<string, unknown>;
      const trackType = t.type as string;
      if (trackType === "video" || trackType === "audio" || trackType === "meta") {
        tracks.push({
          type: trackType,
          codec: t.codec as string,
          codecstring: t.codecstring as string | undefined,
          init: t.init as string | undefined,
          idx: t.idx as number | undefined,
          width: t.width as number | undefined,
          height: t.height as number | undefined,
          fpks: t.fpks as number | undefined,
          channels: t.channels as number | undefined,
          rate: t.rate as number | undefined,
          size: t.size as number | undefined,
          firstms: t.firstms as number | undefined,
          lastms: t.lastms as number | undefined,
        });
      }
    }
    return tracks;
  }

  private async initializePlayer(): Promise<void> {
    const container = this.container;
    const streamInfo = this.streamInfo;

    this.log(
      `[initializePlayer] Starting - container: ${!!container}, streamInfo: ${!!streamInfo}, sources: ${streamInfo?.source?.length ?? 0}`
    );

    if (!container || !streamInfo) {
      throw new Error("Container or streamInfo not available");
    }

    // Log source details for debugging
    this.log(
      `[initializePlayer] Sources: ${JSON.stringify(streamInfo.source.map((s) => ({ type: s.type, url: s.url.slice(0, 60) + "..." })))}`
    );
    this.log(
      `[initializePlayer] Tracks: ${streamInfo.meta.tracks.map((t) => `${t.type}:${t.codec}`).join(", ")}`
    );

    const { autoplay, muted, controls, poster } = this.config;

    // Clear container
    container.innerHTML = "";

    // Listen for player selection
    const onSelected = (e: { player: string; source: StreamSource; score: number }) => {
      // Track current player info
      const playerImpl = this.playerManager
        .getRegisteredPlayers()
        .find((p) => p.capability.shortname === e.player);
      if (playerImpl) {
        this._currentPlayerInfo = {
          name: playerImpl.capability.name,
          shortname: playerImpl.capability.shortname,
        };
      }

      // Track current source info
      if (e.source) {
        this._currentSourceInfo = {
          url: e.source.url,
          type: e.source.type,
        };
      }

      this.setState("connecting", {
        selectedPlayer: e.player,
        selectedProtocol: (e.source?.type || "").toString(),
        endpointUrl: e.source?.url,
      });

      // Bubble up playerSelected event
      this.emit("playerSelected", { player: e.player, source: e.source, score: e.score });
    };
    try {
      (this.playerManager as any).on?.("playerSelected", onSelected);
    } catch {}
    this.cleanupFns.push(() => {
      try {
        (this.playerManager as any).off?.("playerSelected", onSelected);
      } catch {}
    });

    this.setState("selecting_player");

    const playerOptions: CorePlayerOptions = {
      autoplay: autoplay !== false,
      muted: muted === true,
      controls: controls !== false,
      poster: poster,
      debug: this.config.debug,
      onReady: (el) => {
        // Guard against zombie callbacks after destroy
        if (this.isDestroyed || !this.container) {
          this.log("[initializePlayer] onReady callback aborted - controller destroyed");
          return;
        }
        // Defensive: some flows (e.g. failed fallback attempt) can temporarily detach
        // the current video element from the container while playback continues.
        // Ensure the element is actually attached for rendering.
        try {
          if (this.container && !this.container.contains(el)) {
            this.log("[initializePlayer] Video element was detached; re-attaching to container");
            this.container.appendChild(el);
          }
        } catch {}
        this.videoElement = el;
        this.currentPlayer = this.playerManager.getCurrentPlayer();
        this._seekingLoggedOnce = false;
        this.setupVideoEventListeners(el);
        // Listen for player-reported seekable range updates (WebRTC/WHEP data channel)
        if (this.currentPlayer) {
          const playerForListener = this.currentPlayer;
          const onSeekableChange = () => {
            this.updateSeekingState();
          };
          playerForListener.on("seekablechange", onSeekableChange);
          this.cleanupFns.push(() => {
            playerForListener.off("seekablechange", onSeekableChange);
          });
        }
        // Initialize sub-controllers after video is ready
        this.initializeSubControllers();
        this.emit("ready", { videoElement: el });

        // Drain queued play intent from pre-boot play() calls
        if (this._pendingPlayIntent) {
          this._pendingPlayIntent = false;
          el.play().catch(() => {});
        }

        // Centralized autoplay recovery (upstream parity: unmuted → muted → give up)
        if (autoplay !== false && el.paused) {
          attemptAutoplay(el, {
            onMutedFallback: () => {
              this.log("[initializePlayer] Autoplay succeeded with muted fallback");
              this.emit("muteChange", { muted: true });
            },
            onFailed: () => {
              this.log("[initializePlayer] Autoplay failed entirely — awaiting user interaction");
            },
          }).then((result: AutoplayResult) => {
            this.mistReporter?.setAutoplayStatus(result);
            this.emit("autoplayResult", { status: result });
          });
        } else if (autoplay !== false) {
          // Already playing (browser autoplay attribute worked)
          this.mistReporter?.setAutoplayStatus("success");
          this.emit("autoplayResult", { status: "success" as AutoplayResult });
        }
      },
      onTimeUpdate: (_t) => {
        if (this.isDestroyed) return;
        // Defensive: keep video element attached even if some other lifecycle cleared the container.
        // (Playback can continue even when detached, which looks like "audio only".)
        try {
          if (this.container && this.videoElement && !this.container.contains(this.videoElement)) {
            this.log(
              "[initializePlayer] Video element was detached during playback; re-attaching to container"
            );
            this.container.appendChild(this.videoElement);
          }
        } catch {}
        this.emit("timeUpdate", {
          currentTime: this.getEffectiveCurrentTime(),
          duration: this.getEffectiveDuration(),
        });
      },
      onError: (err) => {
        if (this.isDestroyed) return;
        const message = typeof err === "string" ? err : String(err);
        // Use setPassiveError for smart error handling with fallback support
        this.setPassiveError(message);
      },
    };

    // Manager options for player selection
    // Use pending force options (one-shot from selectCombo) if available, otherwise use config
    const pendingForce = this._pendingForceOptions;
    this._pendingForceOptions = null; // Clear immediately - one-shot only

    const managerOptions = {
      // One-shot force options take precedence, then fall back to config
      forcePlayer: pendingForce?.forcePlayer ?? this.config.forcePlayer,
      forceType: pendingForce?.forceType ?? this.config.forceType,
      forceSource: pendingForce?.forceSource ?? this.config.forceSource,
      // Playback mode is a persistent preference
      playbackMode: this.config.playbackMode,
    };

    this.log(`[initializePlayer] Calling playerManager.initializePlayer...`);
    this.log(
      `[initializePlayer] Manager options: ${JSON.stringify(managerOptions)} (pending force: ${pendingForce ? "yes" : "no"})`
    );
    try {
      await this.playerManager.initializePlayer(
        container,
        streamInfo,
        playerOptions,
        managerOptions
      );
      this.log(`[initializePlayer] Player initialized successfully`);
    } catch (e) {
      this.log(`[initializePlayer] Player initialization FAILED: ${e}`);
      throw e;
    }
  }

  private setupVideoEventListeners(el: HTMLVideoElement): void {
    // Apply loop setting
    el.loop = this._isLoopEnabled;

    const onWaiting = () => {
      if (this.shouldSuppressVideoEvents()) return;
      this._isBuffering = true;
      // Start stall timer if not already started
      if (this._stallStartTime === 0) {
        this._stallStartTime = Date.now();
        this.log("Stall started");
      }
      this.setState("buffering");
    };
    const onPlaying = () => {
      if (this.shouldSuppressVideoEvents()) return;
      this._isBuffering = false;
      this._hasPlaybackStarted = true;
      // Clear stall timer on successful playback
      if (this._stallStartTime > 0) {
        this.log(`Stall cleared after ${Date.now() - this._stallStartTime}ms`);
        this._stallStartTime = 0;
      }
      this.setState("playing");
      // Attempt to clear error on playback resume
      this.attemptClearError();
    };
    const onCanPlay = () => {
      this._isBuffering = false;
      // Clear stall timer on canplay
      this._stallStartTime = 0;
      this.setState("playing");
      this.metaTrackManager?.setPaused(false);
      // Attempt to clear error on canplay
      this.attemptClearError();
    };
    const onPause = () => {
      if (this.shouldSuppressVideoEvents()) return;
      this.setState("paused");
      this.metaTrackManager?.setPaused(true);
    };
    const onEnded = () => this.setState("ended");
    const onTimeUpdate = () => {
      this.updateSeekingState();
      this.emit("timeUpdate", {
        currentTime: this.getEffectiveCurrentTime(),
        duration: this.getEffectiveDuration(),
      });
      if (this.getEffectiveCurrentTime() > 0) {
        this.attemptClearError();
      }
    };
    const onDurationChange = () => {
      this.updateSeekingState();
      this.emit("timeUpdate", {
        currentTime: this.getEffectiveCurrentTime(),
        duration: this.getEffectiveDuration(),
      });
    };
    const onProgress = () => {
      // Use player-specific override when available (upstream parity)
      this._buffered = this.getBufferedRanges();
      // Recalculate seeking state when buffer updates
      this.updateSeekingState();
    };
    const elWithAudio = el as HTMLVideoElement & {
      audioTracks?: {
        addEventListener?: (type: string, fn: () => void) => void;
        removeEventListener?: (type: string, fn: () => void) => void;
      };
    };
    const onAudioTracksChange = () => {
      this.detectAudioTracks();
    };
    const onLoadedMetadata = () => {
      // Detect audio tracks and WebRTC source
      this.detectAudioTracks();
      this._isWebRTC = isMediaStreamSource(el);
      this._supportsPlaybackRate = !this._isWebRTC;
      // Initial seeking state calculation
      this.updateSeekingState();
      // Safari: audioTracks may be populated after loadedmetadata for HLS streams
      if (elWithAudio.audioTracks?.addEventListener) {
        elWithAudio.audioTracks.addEventListener("addtrack", onAudioTracksChange);
        elWithAudio.audioTracks.addEventListener("removetrack", onAudioTracksChange);
      }
    };

    // Fullscreen change handler
    const onFullscreenChange = () => {
      const isFullscreen = document.fullscreenElement === this.container;
      this.emit("fullscreenChange", { isFullscreen });
    };

    // PiP change handlers
    const onEnterPiP = () => this.emit("pipChange", { isPiP: true });
    const onLeavePiP = () => this.emit("pipChange", { isPiP: false });

    // Volume change handler (for external changes, e.g., via native controls)
    const onVolumeChange = () => {
      this.emit("volumeChange", { volume: el.volume, muted: el.muted });
    };

    el.addEventListener("waiting", onWaiting);
    el.addEventListener("playing", onPlaying);
    el.addEventListener("canplay", onCanPlay);
    el.addEventListener("pause", onPause);
    el.addEventListener("ended", onEnded);
    el.addEventListener("timeupdate", onTimeUpdate);
    el.addEventListener("durationchange", onDurationChange);
    el.addEventListener("progress", onProgress);
    el.addEventListener("loadedmetadata", onLoadedMetadata);
    el.addEventListener("volumechange", onVolumeChange);
    el.addEventListener("enterpictureinpicture", onEnterPiP);
    el.addEventListener("leavepictureinpicture", onLeavePiP);
    document.addEventListener("fullscreenchange", onFullscreenChange);

    this.cleanupFns.push(() => {
      el.removeEventListener("waiting", onWaiting);
      el.removeEventListener("playing", onPlaying);
      el.removeEventListener("canplay", onCanPlay);
      el.removeEventListener("pause", onPause);
      el.removeEventListener("ended", onEnded);
      el.removeEventListener("timeupdate", onTimeUpdate);
      el.removeEventListener("durationchange", onDurationChange);
      el.removeEventListener("progress", onProgress);
      el.removeEventListener("loadedmetadata", onLoadedMetadata);
      el.removeEventListener("volumechange", onVolumeChange);
      el.removeEventListener("enterpictureinpicture", onEnterPiP);
      el.removeEventListener("leavepictureinpicture", onLeavePiP);
      document.removeEventListener("fullscreenchange", onFullscreenChange);
      if (elWithAudio.audioTracks?.removeEventListener) {
        elWithAudio.audioTracks.removeEventListener("addtrack", onAudioTracksChange);
        elWithAudio.audioTracks.removeEventListener("removetrack", onAudioTracksChange);
      }
      this.unbindMediaStreamAudioListeners();
    });
  }

  // ============================================================================
  // Sub-Controller Initialization (Phase A2)
  // ============================================================================

  private initializeSubControllers(): void {
    if (!this.videoElement || !this.container) return;

    // Initialize ABRController
    this.initializeABRController();

    // Initialize QualityMonitor
    this.initializeQualityMonitor();

    // Initialize InteractionController
    this.initializeInteractionController();

    // Initialize MistReporter (needs WebSocket from StreamStateClient)
    this.initializeMistReporter();

    // Initialize MetaTrackManager
    this.initializeMetaTrackManager();
  }

  private initializeABRController(): void {
    const player = this.currentPlayer;
    if (!player || !this.videoElement) return;

    this.abrController = new ABRController({
      options: { mode: "auto" },
      getQualities: () => player.getQualities?.() ?? [],
      selectQuality: (id) => player.selectQuality?.(id),
      getCurrentQuality: () => {
        const qualities = player.getQualities?.() ?? [];
        const currentId = player.getCurrentQuality?.();
        return qualities.find((q) => q.id === currentId) ?? null;
      },
      // Wire up bandwidth estimate from player stats
      getBandwidthEstimate: async () => {
        if (!this.currentPlayer?.getStats) return 0;
        try {
          const stats = await this.currentPlayer.getStats();
          // HLS.js provides bandwidthEstimate directly
          if (stats?.bandwidthEstimate) {
            return stats.bandwidthEstimate;
          }
          // DASH.js provides throughput info
          if (stats?.averageThroughput) {
            return stats.averageThroughput;
          }
          return 0;
        } catch {
          return 0;
        }
      },
      debug: this.config.debug,
    });

    this.abrController.start(this.videoElement);
    this.cleanupFns.push(() => {
      this.abrController?.stop();
      this.abrController = null;
    });
  }

  private initializeQualityMonitor(): void {
    if (!this.videoElement) return;

    // Map player shortname to QualityMonitor protocol for threshold selection
    const shortname =
      this._currentPlayerInfo?.shortname ?? this.currentPlayer?.capability?.shortname ?? "unknown";
    const protocolMap: Record<string, PlayerProtocol> = {
      hlsjs: "hls",
      videojs: "hls",
      dashjs: "dash",
      "mist-webrtc": "webrtc",
      mews: "webrtc",
      native: "html5",
      webcodecs: "html5",
      "mist-legacy": "html5",
    };
    const protocol = protocolMap[shortname] ?? "unknown";

    this.qualityMonitor = new QualityMonitor({
      sampleInterval: 1000,
      protocol,
      onFallbackRequest: ({ score }) => {
        if (this.isDestroyed || this._isTransitioning || this._qualityFallbackInProgress) return;
        const activeShortname =
          this.currentPlayer?.capability?.shortname ??
          this._currentPlayerInfo?.shortname ??
          "unknown";
        // WebCodecs live path has its own catchup/seek logic; aggressive combo fallback here
        // causes teardown/re-init loops and makes near-live behavior worse.
        if (activeShortname === "webcodecs" && this.isEffectivelyLive()) {
          this.log(
            "[QualityMonitor] Poor playback while live WebCodecs active — skipping auto-fallback"
          );
          return;
        }

        const normalizedScore = Math.max(0, Math.min(2, score));
        const pct = Math.round(normalizedScore * 100);
        const now = Date.now();
        if (now - this._qualityFallbackLastAt < PlayerController.QUALITY_FALLBACK_COOLDOWN_MS) {
          this.log(`[QualityMonitor] Poor playback (${pct}%) — cooldown active, skipping fallback`);
          return;
        }

        this.log(`[QualityMonitor] Poor playback (${pct}%) — attempting fallback`);
        if (this.playerManager.canAttemptFallback()) {
          this._qualityFallbackLastAt = now;
          this._qualityFallbackInProgress = true;
          this._isTransitioning = true;
          // Hide stale overlay while transition is in progress.
          this.clearError();
          this.emit("errorCleared", undefined as never);
          this.playerManager
            .tryPlaybackFallback()
            .catch(() => {
              // Fallback failed - show passive warning instead of blocking modal.
              this._errorText = `Poor playback quality (${pct}%)`;
              this._isPassiveError = true;
              this._errorShownAt = Date.now();
              this._errorCleared = false;
              this._playbackResumedSinceError = false;
              this.emit("error", { error: this._errorText });
            })
            .finally(() => {
              this._isTransitioning = false;
              this._qualityFallbackInProgress = false;
            });
        } else {
          // Quality dip without fallback — force passive (toast), never blocking modal.
          this._errorText = `Poor playback quality (${pct}%)`;
          this._isPassiveError = true;
          this._errorShownAt = Date.now();
          this._errorCleared = false;
          this._playbackResumedSinceError = false;
          this.emit("error", { error: this._errorText });
        }
      },
    });
    this.qualityMonitor.start(this.videoElement);

    // Subscribe to quality updates
    const handleQualityUpdate = () => {
      if (this.qualityMonitor) {
        this._playbackQuality = this.qualityMonitor.getCurrentQuality();

        // Feed quality score to MistReporter
        if (this.mistReporter && this._playbackQuality) {
          // Convert 0-100 score to MistPlayer-style 0-2.0 scale
          const mistScore = this._playbackQuality.score / 100;
          this.mistReporter.setPlaybackScore(mistScore);
        }
      }
    };

    // Sample quality periodically
    const qualityInterval = setInterval(handleQualityUpdate, 1000);
    this.cleanupFns.push(() => {
      clearInterval(qualityInterval);
      this.qualityMonitor?.stop();
      this.qualityMonitor = null;
    });
  }

  private initializeInteractionController(): void {
    if (!this.container || !this.videoElement) return;

    const isLive = this.isEffectivelyLive();
    const hasDvrWindow =
      isLive &&
      Number.isFinite(this._liveEdge) &&
      Number.isFinite(this._seekableStart) &&
      this._liveEdge > this._seekableStart;
    const isLiveOnly = isLive && !hasDvrWindow;
    const interactionContainer =
      (this.container.closest('[data-player-container="true"]') as HTMLElement | null) ??
      this.container;

    this.interactionController = new InteractionController({
      container: interactionContainer,
      videoElement: this.videoElement,
      isLive: isLiveOnly,
      isPaused: () => this.currentPlayer?.isPaused?.() ?? this.videoElement?.paused ?? true,
      frameStepSeconds: (() => {
        const ms = this.getFrameStepMsFromTracks();
        return ms !== undefined ? ms / 1000 : undefined;
      })(),
      onFrameStep: (direction, seconds) => {
        const player = this.currentPlayer ?? this.playerManager.getCurrentPlayer();
        const playerName =
          player?.capability?.shortname ?? this._currentPlayerInfo?.shortname ?? "unknown";
        const hasFrameStep = typeof player?.frameStep === "function";
        this.log(
          `[interaction] frameStep dir=${direction} player=${playerName} hasFrameStep=${hasFrameStep}`
        );
        if (playerName === "webcodecs") {
          this.suppressPlayPauseEvents(250);
        }
        if (hasFrameStep && player && player.frameStep) {
          player.frameStep(direction, seconds);
          return true;
        }
        return false;
      },
      onPlayPause: () => this.togglePlay(),
      onSeek: (deltaSec) => {
        // End any speed hold before seeking
        if (this._isHoldingSpeed) {
          this._isHoldingSpeed = false;
          this.emit("holdSpeedEnd", undefined as never);
        }
        // InteractionController passes seconds; seekBy expects ms
        this.seekBy(deltaSec * 1000);
        // Emit skip events (seconds for external consumers)
        if (deltaSec > 0) {
          this.emit("skipForward", { seconds: deltaSec });
        } else {
          this.emit("skipBackward", { seconds: Math.abs(deltaSec) });
        }
      },
      onVolumeChange: (delta) => {
        if (this.videoElement) {
          // Snap to nonlinear volume levels (upstream parity)
          const levels = [0, 0.1, 0.2, 0.4, 0.6, 0.8, 1.0];
          const current = this.videoElement.volume;
          let idx = levels.findIndex((l) => l >= current - 0.01);
          if (idx === -1) idx = levels.length - 1;
          idx = Math.max(0, Math.min(levels.length - 1, idx + (delta > 0 ? 1 : -1)));
          const newVolume = levels[idx];
          this.videoElement.volume = newVolume;
          this.emit("volumeChange", { volume: newVolume, muted: this.videoElement.muted });
        }
      },
      onMuteToggle: () => this.toggleMute(),
      onFullscreenToggle: () => this.toggleFullscreen(),
      onCaptionsToggle: () => {
        this.toggleSubtitles();
      },
      onSpeedChange: (speed, isHolding) => {
        const wasHolding = this._isHoldingSpeed;
        this._isHoldingSpeed = isHolding;
        this._holdSpeed = speed;
        this.setPlaybackRate(speed);

        // Emit holdSpeed events on state transitions
        if (isHolding && !wasHolding) {
          this.emit("holdSpeedStart", { speed });
        } else if (!isHolding && wasHolding) {
          this.emit("holdSpeedEnd", undefined as never);
        }
      },
      onSeekPercent: (percent) => this.seekPercent(percent),
      speedHoldValue: this._holdSpeed,
    });

    this.interactionController.attach();
    this.cleanupFns.push(() => {
      this.interactionController?.detach();
      this.interactionController = null;
    });
  }

  private initializeMistReporter(): void {
    if (!this.streamStateClient) return;

    const socket = this.streamStateClient.getSocket();
    if (!socket) return;

    this.mistReporter = new MistReporter({
      socket,
      bootMs: this.bootMs,
      reportInterval: 5000,
    });

    // Initialize with video element
    if (this.videoElement) {
      this.mistReporter.init(this.videoElement, this.container ?? undefined);
    }

    // Send initial report
    if (this._currentSourceInfo) {
      this.mistReporter.sendInitialReport({
        player: this._currentPlayerInfo?.shortname || "unknown",
        sourceType: this._currentSourceInfo.type,
        sourceUrl: this._currentSourceInfo.url,
        pageUrl: typeof window !== "undefined" ? window.location.href : "",
      });
    }

    this.cleanupFns.push(() => {
      this.mistReporter?.sendFinalReport("unmount");
      this.mistReporter?.destroy();
      this.mistReporter = null;
    });
  }

  private initializeMetaTrackManager(): void {
    const mistUrl = this.endpoints?.primary?.baseUrl;
    if (!mistUrl) return;

    this.metaTrackManager = new MetaTrackManager({
      mistBaseUrl: mistUrl,
      streamName: this.config.contentId,
      debug: this.config.debug,
    });

    // Set initial playback time before connecting so the first seek goes to the
    // correct position (includes lastms offset for NativePlayer live streams)
    const initialTimeSec = this.getEffectiveCurrentTime() / 1000;
    if (initialTimeSec > 0) {
      this.metaTrackManager.setPlaybackTime(initialTimeSec);
    }

    this.metaTrackManager.connect();

    // Wire video timeupdate to MetaTrackManager
    // Use player's effective time (includes lastms offset for NativePlayer) so the
    // metadata WebSocket seeks to the correct absolute stream position, not browser-relative 0.
    if (this.videoElement) {
      const handleTimeUpdate = () => {
        if (this.metaTrackManager) {
          this.metaTrackManager.setPlaybackTime(this.getEffectiveCurrentTime() / 1000);
        }
      };
      const handleSeeking = () => {
        if (this.metaTrackManager) {
          this.metaTrackManager.onSeek(this.getEffectiveCurrentTime() / 1000);
        }
      };

      this.videoElement.addEventListener("timeupdate", handleTimeUpdate);
      this.videoElement.addEventListener("seeking", handleSeeking);

      this.cleanupFns.push(() => {
        this.videoElement?.removeEventListener("timeupdate", handleTimeUpdate);
        this.videoElement?.removeEventListener("seeking", handleSeeking);
      });
    }

    this.cleanupFns.push(() => {
      this.metaTrackManager?.disconnect();
      this.metaTrackManager = null;
    });
  }

  private cleanup(): void {
    // Run all cleanup functions
    this.cleanupFns.forEach((fn) => {
      try {
        fn();
      } catch {}
    });
    this.cleanupFns = [];

    // Destroy player manager's current player
    try {
      this.playerManager.destroy();
    } catch {}
  }

  private setState(state: PlayerState, context?: PlayerStateContext): void {
    this.state = state;

    // Only emit if state actually changed
    if (this.lastEmittedState !== state) {
      this.lastEmittedState = state;
      this.emit("stateChange", { state, context });
    }
  }

  private log(message: string): void {
    if (this.config.debug) {
      console.log(`[PlayerController] ${message}`);
    }
  }

  private clampSeekTarget(timeMs: number): number {
    if (!Number.isFinite(timeMs)) {
      return this.getEffectiveCurrentTime();
    }

    // Use _seekableStart/_liveEdge — they match the active player's coordinate space
    // (absolute ms for anchor-based players, browser-local ms for HLS.js/VideoJS).
    const rangeStart = this._seekableStart;
    const rangeEnd = this._liveEdge;
    if (
      this._canSeek &&
      Number.isFinite(rangeStart) &&
      Number.isFinite(rangeEnd) &&
      rangeEnd > rangeStart
    ) {
      return Math.max(rangeStart, Math.min(rangeEnd, timeMs));
    }

    const duration = this.getEffectiveDuration();
    if (Number.isFinite(duration)) {
      return Math.max(0, Math.min(duration, timeMs));
    }

    return Math.max(0, timeMs);
  }

  private getMistTrackSeekRange(): { start: number; end: number } | null {
    const meta = this.streamState?.streamInfo?.meta;
    const tracks = meta?.tracks as
      | Record<string, { type?: string; firstms?: number; lastms?: number }>
      | undefined;
    if (!tracks) return null;

    const nonMeta = Object.values(tracks).filter(
      (t) => t.type !== "meta" && Number.isFinite(t.lastms) && (t.lastms ?? 0) > 0
    );
    if (nonMeta.length === 0) return null;

    const starts = nonMeta.map((t) => t.firstms).filter((v): v is number => Number.isFinite(v));
    const ends = nonMeta.map((t) => t.lastms).filter((v): v is number => Number.isFinite(v));
    if (starts.length === 0 || ends.length === 0) return null;

    const end = Math.max(...ends);
    // buffer_window gives a stable DVR width; raw firstms fluctuates between prune cycles
    const bw = meta?.buffer_window;
    const start = typeof bw === "number" && bw > 0 ? end - bw : Math.min(...starts);
    if (!Number.isFinite(start) || !Number.isFinite(end) || end <= start) return null;
    return { start, end };
  }
}

export default PlayerController;
