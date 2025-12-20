import React from 'react';

export interface PlayerProps {
  /** Content identifier or stream name */
  contentId: string;
  /** Content type */
  contentType: 'live' | 'dvr' | 'clip';
  /** Pre-resolved endpoints/capabilities from Gateway/Foghorn */
  endpoints?: ContentEndpoints;
  /** Optional thumbnail/poster image */
  thumbnailUrl?: string | null;
  /** Unified options (branding, playback prefs, etc.) */
  options?: Partial<PlayerOptions>;
  /** Detailed state updates for UI (booting, gateway, connecting, playing, etc.) */
  onStateChange?: (state: PlayerState, context?: PlayerStateContext) => void;
}

/** High-level player state machine for UI */
export type PlayerState =
  | 'booting'
  | 'gateway_loading'
  | 'gateway_ready'
  | 'gateway_error'
  | 'no_endpoint'
  | 'selecting_player'
  | 'connecting'
  | 'buffering'
  | 'playing'
  | 'paused'
  | 'ended'
  | 'error'
  | 'destroyed';

export interface PlayerStateContext {
  reason?: string;
  gatewayStatus?: 'idle' | 'loading' | 'ready' | 'error';
  selectedPlayer?: string; // shortname
  selectedProtocol?: string;
  nodeId?: string;
  endpointUrl?: string;
  error?: string;
}

export interface MistPlayerProps {
  /** Name of the stream to display */
  streamName: string;
  /** Optional direct viewer HTML URL; if provided, iframe is used */
  htmlUrl?: string;
  /** Optional direct player.js URL for embedded mode */
  playerJsUrl?: string;
  /** Whether to use development mode - uses 'dev' skin (default: false) */
  developmentMode?: boolean;
  /** Whether to start muted (default: true) */
  muted?: boolean;
  /** URL to poster/thumbnail image (optional) */
  poster?: string;
}

export interface LoadingScreenProps {
  /** Loading message to display (default: "Waiting for source...") */
  message?: string;
}

export interface ThumbnailOverlayProps {
  /** URL to thumbnail image (optional) */
  thumbnailUrl?: string | null | undefined;
  /** Callback when user clicks to play */
  onPlay?: () => void;
  /** Message to display (used instead of streamName) */
  message?: string | null;
  /** Whether to show unmute message instead of play button (default: false) */
  showUnmuteMessage?: boolean;
  /** Additional styles for the overlay */
  style?: React.CSSProperties;
  /** Optional className for styling */
  className?: string;
}

// LoadingScreen internal component types
export interface AnimatedBubbleProps {
  /** Index for staggered animation timing */
  index: number;
}

export interface CenterLogoProps {
  /** Reference to container element */
  containerRef: React.RefObject<HTMLDivElement>;
  /** Scale factor for logo size (default: 0.2) */
  scale?: number;
  /** Callback when logo is clicked */
  onHitmarker?: (event: React.MouseEvent) => void;
}

export interface HitmarkerData {
  /** Unique identifier */
  id: number;
  /** X position */
  x: number;
  /** Y position */
  y: number;
}

export interface DvdLogoProps {
  /** Reference to parent container element */
  parentRef: React.RefObject<HTMLDivElement>;
  /** Scale factor for logo size (default: 0.15) */
  scale?: number;
} 

// FrameWorks Player options
export interface PlayerOptions {
  gatewayUrl?: string;
  /** Direct MistServer base URL (e.g. "http://localhost:8080") - bypasses Gateway resolution */
  mistUrl?: string;
  authToken?: string;
  autoplay?: boolean;
  muted?: boolean;
  controls?: boolean;
  stockControls?: boolean;
  debug?: boolean;
  /** Show dev mode overlay panel (player/source selection, stats) */
  devMode?: boolean;
  /** Force a specific player (e.g., 'videojs', 'hlsjs', 'dashjs', 'direct', 'mist') */
  forcePlayer?: string;
  /** Force a specific MIME type (e.g., 'html5/application/vnd.apple.mpegurl') */
  forceType?: string;
  /** Force a specific source index */
  forceSource?: number;
  /**
   * Playback mode affects protocol selection:
   * - 'low-latency': Prefer WebRTC/WHEP for minimal delay
   * - 'quality': Prefer HLS/DASH for ABR and quality
   * - 'dvr': Prefer protocols that support seeking in live buffer
   * - 'auto': Score-based selection (default)
   */
  playbackMode?: PlaybackMode;

  // =====================================================
  // Library Configuration (MistMetaPlayer feature backport)
  // =====================================================

  /** HLS.js configuration override (merged with defaults) */
  hlsConfig?: HlsJsConfig;
  /** DASH.js configuration override (merged with defaults) */
  dashConfig?: DashJsConfig;
  /** WebRTC configuration (ICE servers, etc.) */
  rtcConfig?: RTCConfiguration;
  /** String to append to all request URLs (auth tokens, tracking params) */
  urlAppend?: string;
  /** Low latency mode for HLS (enables LL-HLS features) */
  lowLatencyMode?: boolean;
}

// =====================================================
// Library Configuration Types
// =====================================================

/** HLS.js configuration subset (most common options) */
export interface HlsJsConfig {
  /** Enable debug logging */
  debug?: boolean;
  /** Auto start loading after attaching media */
  autoStartLoad?: boolean;
  /** Start position override (seconds) */
  startPosition?: number;
  /** Max buffer length in seconds */
  maxBufferLength?: number;
  /** Max buffer size in bytes */
  maxBufferSize?: number;
  /** Maximum max buffer length for back buffer */
  maxMaxBufferLength?: number;
  /** Low latency mode */
  lowLatencyMode?: boolean;
  /** Enable ABR bandwidth estimation */
  abrEwmaDefaultEstimate?: number;
  /** ABR bandwidth estimation fast half-life */
  abrEwmaFastLive?: number;
  /** ABR bandwidth estimation slow half-life */
  abrEwmaSlowLive?: number;
  /** Max number of retries for loading */
  fragLoadingMaxRetry?: number;
  /** Fragment loading timeout in ms */
  fragLoadingTimeOut?: number;
  /** Level loading timeout in ms */
  levelLoadingTimeOut?: number;
  /** Manifest loading timeout in ms */
  manifestLoadingTimeOut?: number;
  /** Allow any additional HLS.js config options */
  [key: string]: unknown;
}

/** DASH.js configuration subset (most common options) */
export interface DashJsConfig {
  /** Enable debug logging */
  debug?: boolean;
  /** Auto play on attach */
  autoPlay?: boolean;
  /** Streaming buffer configuration */
  streaming?: {
    /** Low latency settings */
    lowLatencyEnabled?: boolean;
    /** Live delay (seconds from live edge) */
    liveDelay?: number;
    /** Live delay fragment count */
    liveDelayFragmentCount?: number;
    /** Buffer settings */
    buffer?: {
      /** Stable buffer time (seconds) */
      stableBufferTime?: number;
      /** Fast switch enabled */
      fastSwitchEnabled?: boolean;
    };
    /** ABR settings */
    abr?: {
      /** Auto switch bitrate for video */
      autoSwitchBitrate?: { video: boolean; audio: boolean };
      /** ABR strategy */
      ABRStrategy?: 'abrDynamic' | 'abrBola' | 'abrL2A' | 'abrLoLP' | 'abrThroughput';
      /** Use default ABR rules */
      useDefaultABRRules?: boolean;
    };
  };
  /** Allow any additional DASH.js config options */
  [key: string]: unknown;
}

// To-be Gateway/Foghorn viewer resolution types
export type StreamProtocol = 'WHEP' | 'HLS' | 'DASH' | 'MP4' | 'WEBM' | 'RTMP' | 'MIST_HTML';

export interface OutputCapabilities {
  supportsSeek: boolean;
  supportsQualitySwitch: boolean;
  maxBitrate?: number;
  hasAudio: boolean;
  hasVideo: boolean;
  codecs?: string[];
}

export interface QualityLevel {
  id: string;
  label: string;
  width?: number;
  height?: number;
  bitrate?: number;
}

export interface OutputEndpoint {
  protocol: StreamProtocol | string;
  url: string;
  capabilities?: OutputCapabilities;
}

export interface EndpointInfo {
  nodeId: string;
  baseUrl?: string;
  protocol: StreamProtocol | string;
  url: string;
  quality?: QualityLevel;
  capabilities?: OutputCapabilities;
  outputs?: Record<string, OutputEndpoint>;
  geoDistance?: number;
  loadScore?: number;
}

export interface ContentMetadata {
  title?: string;
  description?: string;
  durationSeconds?: number;
  thumbnailUrl?: string;
  createdAt?: string;
  status?: 'AVAILABLE' | 'PROCESSING' | 'ERROR' | 'OFFLINE';
  viewers?: number;
  isLive?: boolean;
  recordingSizeBytes?: number;
  clipSource?: string;
}

export interface ContentEndpoints {
  primary: EndpointInfo;
  fallbacks: EndpointInfo[];
  metadata?: ContentMetadata;
}

// =====================================================
// Stream State Types (MistServer native polling)
// =====================================================

/** Stream status from MistServer info.js endpoint */
export type StreamStatus =
  | 'ONLINE'
  | 'OFFLINE'
  | 'INITIALIZING'
  | 'BOOTING'
  | 'WAITING_FOR_DATA'
  | 'SHUTTING_DOWN'
  | 'INVALID'
  | 'ERROR';

/** MistServer info.js response structure - matches reference MistMetaPlayer */
export interface MistStreamInfo {
  /** Error message if stream is not online */
  error?: string;
  /** Custom error message to display */
  on_error?: string;
  /** Initialization percentage (0-100) */
  perc?: number;
  /** Stream type: "live" or "vod" */
  type?: 'live' | 'vod';
  /** Whether stream has video tracks */
  hasVideo?: boolean;
  /** Whether stream has audio tracks */
  hasAudio?: boolean;
  /** Unix timestamp offset for datetime display (ms) */
  unixoffset?: number;
  /** Last millisecond timestamp of the stream */
  lastms?: number;
  /** Available playback sources/protocols */
  source?: MistStreamSource[];
  /** Stream metadata including tracks */
  meta?: MistStreamMeta;
}

/** MistServer stream metadata */
export interface MistStreamMeta {
  /** Available tracks keyed by track ID */
  tracks?: Record<string, MistTrackInfo>;
  /** DVR buffer window in milliseconds (live streams) - updated by signaling */
  buffer_window?: number;
  /** VOD duration in milliseconds */
  duration?: number;
}

export interface MistStreamSource {
  url: string;
  type: string;
  priority?: number;
  simul_tracks?: number;
  relurl?: string;
  /** ICE servers for WebRTC */
  RTCIceServers?: RTCIceServer[];
}

export interface MistTrackInfo {
  /** Track type */
  type: 'video' | 'audio' | 'meta';
  /** Codec short name (H264, AAC, etc.) */
  codec: string;
  /** Video width in pixels */
  width?: number;
  /** Video height in pixels */
  height?: number;
  /** Bitrate in bits per second */
  bps?: number;
  /** Frames per kilosecond (fps * 1000) */
  fpks?: number;
  /** Initialization data (hex string) */
  init?: string;
  /** Full codec string for canPlayType (e.g., "avc1.64001f") */
  codecstring?: string;
  /** First millisecond timestamp */
  firstms?: number;
  /** Last millisecond timestamp */
  lastms?: number;
  /** Language code (for subtitles) */
  lang?: string;
  /** Track index/ID */
  idx?: number;
  /** Number of channels (audio) */
  channels?: number;
  /** Sample rate in Hz (audio) */
  rate?: number;
}

/** Options for useStreamState hook */
export interface UseStreamStateOptions {
  /** MistServer base URL (from Gateway endpoint.baseUrl) */
  mistBaseUrl: string;
  /** Stream name/identifier */
  streamName: string;
  /** Poll interval in ms (default: 3000) */
  pollInterval?: number;
  /** Enable polling (default: true) */
  enabled?: boolean;
  /** Use WebSocket instead of HTTP polling (default: true) */
  useWebSocket?: boolean;
}

/** Stream state returned by useStreamState hook */
export interface StreamState {
  /** Current stream status */
  status: StreamStatus;
  /** Whether stream is live and playable */
  isOnline: boolean;
  /** Human-readable status message */
  message: string;
  /** Processing percentage (if initializing) */
  percentage?: number;
  /** Last update timestamp */
  lastUpdate: number;
  /** Full MistServer stream info (when online) */
  streamInfo?: MistStreamInfo;
  /** Error message if status is ERROR */
  error?: string;
}

// =====================================================
// Playback Quality Types
// =====================================================

/** Playback quality metrics */
export interface PlaybackQuality {
  /** Composite quality score (0-100) */
  score: number;
  /** Current bitrate in bps */
  bitrate: number;
  /** Seconds of buffered content ahead */
  bufferedAhead: number;
  /** Total stall count */
  stallCount: number;
  /** Frame drop rate as percentage (0-100) */
  frameDropRate: number;
  /** End-to-end latency in ms (for live streams) */
  latency: number;
  /** Timestamp of measurement */
  timestamp: number;
}

/** Thresholds for quality degradation triggers */
export interface QualityThresholds {
  /** Trigger ABR downgrade below this score (default: 60) */
  minScore: number;
  /** Max stalls before downgrade (default: 3) */
  maxStalls: number;
  /** Critical buffer threshold in seconds (default: 2) */
  minBuffer: number;
}

/** Options for usePlaybackQuality hook */
export interface UsePlaybackQualityOptions {
  /** Video element to monitor */
  videoElement: HTMLVideoElement | null;
  /** Enable monitoring (default: true) */
  enabled?: boolean;
  /** Sample interval in ms (default: 500) */
  sampleInterval?: number;
  /** Quality thresholds for triggers */
  thresholds?: Partial<QualityThresholds>;
  /** Callback when quality degrades below threshold */
  onQualityDegraded?: (quality: PlaybackQuality) => void;
}

// =====================================================
// Meta Track Subscription Types
// =====================================================

/** Types of meta track events */
export type MetaTrackEventType = 'subtitle' | 'score' | 'event' | 'chapter' | 'unknown';

/** Base meta track event */
export interface MetaTrackEvent {
  /** Event type */
  type: MetaTrackEventType;
  /** Event timestamp in ms */
  timestamp: number;
  /** Track ID this event belongs to */
  trackId: string;
  /** Raw event data */
  data: unknown;
}

/** Subtitle cue event */
export interface SubtitleCue {
  id: string;
  startTime: number;
  endTime: number;
  text: string;
  lang?: string;
}

/** Score/stat update event */
export interface ScoreUpdate {
  key: string;
  value: number | string;
  team?: string;
}

/** Timed event (generic) */
export interface TimedEvent {
  id: string;
  name: string;
  description?: string;
}

/** Chapter marker */
export interface ChapterMarker {
  id: string;
  title: string;
  startTime: number;
  endTime?: number;
  thumbnailUrl?: string;
}

/** Options for useMetaTrack hook */
export interface UseMetaTrackOptions {
  /** MistServer base URL */
  mistBaseUrl: string;
  /** Stream name */
  streamName: string;
  /** Track subscriptions with callbacks */
  subscriptions?: Array<{
    trackId: string;
    callback: (event: MetaTrackEvent) => void;
  }>;
  /** Enable subscriptions (default: true) */
  enabled?: boolean;
}

// =====================================================
// Telemetry Types
// =====================================================

/** Telemetry payload sent to server */
export interface TelemetryPayload {
  timestamp: number;
  sessionId: string;
  contentId: string;
  contentType: 'live' | 'dvr' | 'clip';
  metrics: {
    currentTime: number;
    duration: number;
    bufferedSeconds: number;
    stallCount: number;
    totalStallMs: number;
    bitrate: number;
    qualityScore: number;
    framesDecoded: number;
    framesDropped: number;
    playerType: string;
    protocol: string;
    resolution?: { width: number; height: number };
  };
  errors?: Array<{ code: string; message: string; timestamp: number }>;
}

/** Options for telemetry reporting */
export interface TelemetryOptions {
  /** Enable telemetry (default: false) */
  enabled: boolean;
  /** Telemetry endpoint URL */
  endpoint: string;
  /** Report interval in ms (default: 5000) */
  interval?: number;
  /** Auth token for endpoint */
  authToken?: string;
  /** Batch size before flush (default: 1) */
  batchSize?: number;
}

// =====================================================
// Playback Mode Types
// =====================================================

/**
 * Playback mode affects protocol selection:
 * - 'low-latency': Prefer WebRTC/WHEP for minimal delay (sub-second)
 * - 'quality': Prefer MP4/WS for stable playback, HLS/DASH as fallback
 * - 'auto': Balanced selection (MP4/WS → WHEP → HLS)
 */
export type PlaybackMode = 'low-latency' | 'quality' | 'auto';

// =====================================================
// ABR Types
// =====================================================

/** ABR mode configuration */
export type ABRMode = 'auto' | 'resize' | 'bitrate' | 'manual';

/** ABR controller options */
export interface ABROptions {
  /** ABR mode (default: 'auto') */
  mode: ABRMode;
  /** Max resolution constraint */
  maxResolution?: { width: number; height: number };
  /** Max bitrate constraint in bps */
  maxBitrate?: number;
  /** Min buffer before switching up (default: 10s) */
  minBufferForUpgrade?: number;
  /** Quality score threshold for downgrade (default: 60) */
  downgradeThreshold?: number;
}
