/**
 * Core types for FrameWorks player - framework agnostic
 */

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

/**
 * Content type (used for Gateway resolution + UI presets).
 *
 * Notes:
 * - `dvr` can be effectively-live when `metadata.dvrStatus === 'recording'`
 * - `vod`/`clip`/completed `dvr` should use VOD-like presets and controls
 */
export type ContentType = 'live' | 'dvr' | 'clip' | 'vod';

export interface PlayerStateContext {
  reason?: string;
  gatewayStatus?: 'idle' | 'loading' | 'ready' | 'error';
  selectedPlayer?: string;
  selectedProtocol?: string;
  nodeId?: string;
  endpointUrl?: string;
  error?: string;
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

  /** HLS.js configuration override (merged with defaults) */
  hlsConfig?: HlsJsConfig;
  /** DASH.js configuration override (merged with defaults) */
  dashConfig?: DashJsConfig;
  /** Video.js VHS configuration override (merged with defaults) */
  vhsConfig?: VhsConfig;
  /** WebRTC configuration (ICE servers, etc.) */
  rtcConfig?: RTCConfiguration;
  /** String to append to all request URLs (auth tokens, tracking params) */
  urlAppend?: string;
  /** Low latency mode for HLS (enables LL-HLS features) */
  lowLatencyMode?: boolean;
}

/** HLS.js configuration subset */
export interface HlsJsConfig {
  debug?: boolean;
  autoStartLoad?: boolean;
  startPosition?: number;
  maxBufferLength?: number;
  maxBufferSize?: number;
  maxMaxBufferLength?: number;
  lowLatencyMode?: boolean;
  abrEwmaDefaultEstimate?: number;
  abrEwmaFastLive?: number;
  abrEwmaSlowLive?: number;
  fragLoadingMaxRetry?: number;
  fragLoadingTimeOut?: number;
  levelLoadingTimeOut?: number;
  manifestLoadingTimeOut?: number;
  [key: string]: unknown;
}

/** DASH.js configuration subset */
export interface DashJsConfig {
  debug?: boolean;
  autoPlay?: boolean;
  streaming?: {
    lowLatencyEnabled?: boolean;
    liveDelay?: number;
    liveDelayFragmentCount?: number;
    buffer?: {
      stableBufferTime?: number;
      fastSwitchEnabled?: boolean;
    };
    abr?: {
      autoSwitchBitrate?: { video: boolean; audio: boolean };
      ABRStrategy?: 'abrDynamic' | 'abrBola' | 'abrL2A' | 'abrLoLP' | 'abrThroughput';
      useDefaultABRRules?: boolean;
    };
  };
  [key: string]: unknown;
}

/** Video.js VHS (http-streaming) configuration subset */
export interface VhsConfig {
  /** Start with lowest quality for faster initial playback */
  enableLowInitialPlaylist?: boolean;
  /** Initial bandwidth estimate in bits per second (e.g., 5_000_000 for 5 Mbps) */
  bandwidth?: number;
  /** Persist bandwidth estimate in localStorage across sessions */
  useBandwidthFromLocalStorage?: boolean;
  /** Enable partial segment appends for lower latency */
  handlePartialData?: boolean;
  /** Time delta for live range safety calculations (seconds) */
  liveRangeSafeTimeDelta?: number;
  /** Pass-through for other VHS options */
  [key: string]: unknown;
}

// Gateway/Foghorn viewer resolution types
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
  contentId?: string;
  contentType?: ContentType;
  durationSeconds?: number;
  thumbnailUrl?: string;
  createdAt?: string;
  status?:
    | 'AVAILABLE'
    | 'PROCESSING'
    | 'ERROR'
    | 'OFFLINE'
    | 'ONLINE'
    | 'INITIALIZING'
    | 'BOOTING'
    | 'WAITING_FOR_DATA'
    | 'SHUTTING_DOWN'
    | 'INVALID';
  viewers?: number;
  isLive?: boolean;
  recordingSizeBytes?: number;
  clipSource?: string;
  /** DVR recording status: 'recording' = in progress (treat as live), 'completed' = finished (treat as VOD) */
  dvrStatus?: 'recording' | 'completed';
  /** Native container format: mp4, m3u8, webm, etc. */
  format?: string;
  /** MistServer authoritative snapshot (merged into this metadata) */
  mist?: MistStreamInfo;
  /** Parsed track summary (derived from Mist metadata when available) */
  tracks?: Array<{
    type: 'video' | 'audio' | 'meta';
    codec?: string;
    width?: number;
    height?: number;
    bitrate?: number;
    fps?: number;
    channels?: number;
    sampleRate?: number;
  }>;
}

export interface ContentEndpoints {
  primary: EndpointInfo;
  fallbacks: EndpointInfo[];
  metadata?: ContentMetadata;
}

// Stream State Types (MistServer native polling)
export type StreamStatus =
  | 'ONLINE'
  | 'OFFLINE'
  | 'INITIALIZING'
  | 'BOOTING'
  | 'WAITING_FOR_DATA'
  | 'SHUTTING_DOWN'
  | 'INVALID'
  | 'ERROR';

export interface MistStreamInfo {
  error?: string;
  on_error?: string;
  perc?: number;
  type?: 'live' | 'vod';
  hasVideo?: boolean;
  hasAudio?: boolean;
  unixoffset?: number;
  lastms?: number;
  source?: MistStreamSource[];
  meta?: MistStreamMeta;
}

export interface MistStreamMeta {
  tracks?: Record<string, MistTrackInfo>;
  buffer_window?: number;
  duration?: number;
  /** Optional MistServer base URL hint (present in some gateway-merged payloads) */
  mistUrl?: string;
}

export interface MistStreamSource {
  url: string;
  type: string;
  priority?: number;
  simul_tracks?: number;
  relurl?: string;
  RTCIceServers?: RTCIceServer[];
}

export interface MistTrackInfo {
  type: 'video' | 'audio' | 'meta';
  codec: string;
  width?: number;
  height?: number;
  bps?: number;
  fpks?: number;
  init?: string;
  codecstring?: string;
  firstms?: number;
  lastms?: number;
  lang?: string;
  idx?: number;
  channels?: number;
  rate?: number;
  /** Track payload size in bytes (present on some MistServer builds) */
  size?: number;
}

export interface StreamStateOptions {
  mistBaseUrl: string;
  streamName: string;
  pollInterval?: number;
  enabled?: boolean;
  useWebSocket?: boolean;
}

export interface StreamState {
  status: StreamStatus;
  isOnline: boolean;
  message: string;
  percentage?: number;
  lastUpdate: number;
  streamInfo?: MistStreamInfo;
  error?: string;
}

// Playback Quality Types
export interface PlaybackQuality {
  score: number;
  bitrate: number;
  bufferedAhead: number;
  stallCount: number;
  frameDropRate: number;
  latency: number;
  timestamp: number;
}

export interface QualityThresholds {
  minScore: number;
  maxStalls: number;
  minBuffer: number;
}

// Backwards-compat alias (older wrappers used this name)
export type PlaybackThresholds = QualityThresholds;

export interface PlaybackQualityOptions {
  videoElement: HTMLVideoElement | null;
  enabled?: boolean;
  sampleInterval?: number;
  thresholds?: Partial<QualityThresholds>;
  onQualityDegraded?: (quality: PlaybackQuality) => void;
}

// Meta Track Types
export type MetaTrackEventType = 'subtitle' | 'score' | 'event' | 'chapter' | 'unknown';

export interface MetaTrackEvent {
  type: MetaTrackEventType;
  timestamp: number;
  trackId: string;
  data: unknown;
}

export interface SubtitleCue {
  id: string;
  startTime: number;
  endTime: number;
  text: string;
  lang?: string;
}

export interface ScoreUpdate {
  key: string;
  value: number | string;
  team?: string;
}

export interface TimedEvent {
  id: string;
  name: string;
  description?: string;
}

export interface ChapterMarker {
  id: string;
  title: string;
  startTime: number;
  endTime?: number;
  thumbnailUrl?: string;
}

export interface MetaTrackOptions {
  mistBaseUrl: string;
  streamName: string;
  subscriptions?: Array<{
    trackId: string;
    callback: (event: MetaTrackEvent) => void;
  }>;
  enabled?: boolean;
}

// Telemetry Types
export interface TelemetryPayload {
  timestamp: number;
  sessionId: string;
  contentId: string;
  contentType: ContentType;
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

export interface TelemetryOptions {
  enabled: boolean;
  endpoint: string;
  interval?: number;
  authToken?: string;
  batchSize?: number;
}

/**
 * Playback mode affects protocol selection:
 * - 'low-latency': Prefer WebRTC/WHEP for minimal delay (sub-second)
 * - 'quality': Prefer MP4/WS for stable playback, HLS/DASH as fallback
 * - 'vod': VOD/clip content - prefer seekable protocols (HLS/MP4), exclude WHEP
 * - 'auto': Balanced selection (MP4/WS → WHEP → HLS)
 */
export type PlaybackMode = 'low-latency' | 'quality' | 'vod' | 'auto';

/** ABR mode configuration */
export type ABRMode = 'auto' | 'resize' | 'bitrate' | 'manual';

/** ABR controller options */
export interface ABROptions {
  mode: ABRMode;
  maxResolution?: { width: number; height: number };
  maxBitrate?: number;
  minBufferForUpgrade?: number;
  downgradeThreshold?: number;
}

/**
 * Combined metadata from Gateway and MistServer
 * Emitted via onMetadata callback when player resolves content
 */
export interface PlayerMetadata {
  // From Gateway (resolveViewerEndpoint)
  title?: string;
  description?: string;
  contentId?: string;
  contentType?: ContentType;
  isLive?: boolean;
  viewers?: number;
  durationSeconds?: number;
  status?: string;
  createdAt?: string;

  // From endpoint resolution
  nodeId?: string;
  protocol?: string;
  geoDistance?: number;

  // From MistServer (real-time)
  tracks?: Array<{
    type: 'video' | 'audio' | 'meta';
    codec?: string;
    width?: number;
    height?: number;
    bitrate?: number;
    fps?: number;
    channels?: number;
    sampleRate?: number;
  }>;
  mist?: MistStreamInfo;
}
