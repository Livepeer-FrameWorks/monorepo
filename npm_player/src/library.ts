// Import CSS - rollup-plugin-postcss will inline and auto-inject
import "./styles/tailwind.css";

// =====================================================
// Components
// =====================================================
export { default as Player } from "./components/Player";
export { default as PlayerErrorBoundary } from "./components/PlayerErrorBoundary";
export { default as MistPlayer } from "./components/players/MistPlayer";
export { default as LoadingScreen } from "./components/LoadingScreen";
export { default as ThumbnailOverlay } from "./components/ThumbnailOverlay";
export { default as PlayerControls } from "./components/PlayerControls";
export { default as StreamStateOverlay } from "./components/StreamStateOverlay";
export { default as SubtitleRenderer } from "./components/SubtitleRenderer";
export * from "./components/Icons";

// Export context for advanced usage (multiple players, testing)
export { PlayerProvider, usePlayer, usePlayerWithFallback } from "./context/PlayerContext";
export type { PlayerContextValue } from "./context/PlayerContext";

// =====================================================
// Hooks
// =====================================================
export { useStreamState } from "./hooks/useStreamState";
export { usePlaybackQuality } from "./hooks/usePlaybackQuality";
export { useTelemetry } from "./hooks/useTelemetry";
export { useMetaTrack } from "./hooks/useMetaTrack";
export { default as useViewerEndpoints } from "./hooks/useViewerEndpoints";

// =====================================================
// Core Classes
// =====================================================
export { QualityMonitor, PROTOCOL_THRESHOLDS } from "./core/QualityMonitor";
export type { PlayerProtocol } from "./core/QualityMonitor";
export { TelemetryReporter } from "./core/TelemetryReporter";
export { ABRController } from "./core/ABRController";
export { MetaTrackManager } from "./core/MetaTrackManager";
export { MistReporter } from "./core/MistReporter";
export type {
  MistReporterStats,
  MistReporterOptions,
  MistReporterInitialReport,
} from "./core/MistReporter";
export { MistSignaling } from "./core/MistSignaling";
export type {
  MistSignalingConfig,
  MistSignalingEvents,
  MistTimeUpdate,
  MistSignalingState,
} from "./core/MistSignaling";
export { LiveDurationProxy, createLiveVideoProxy } from "./core/LiveDurationProxy";
export type { LiveDurationProxyOptions, LiveDurationState } from "./core/LiveDurationProxy";
export { TimerManager } from "./core/TimerManager";

// URL utilities
export {
  appendUrlParams,
  parseUrlParams,
  stripUrlParams,
  buildUrl,
  isSecureUrl,
  httpToWs,
  wsToHttp,
  matchPageProtocol,
} from "./core/UrlUtils";

// Codec utilities
export { translateCodec, isCodecSupported, getBestSupportedTrack } from "./core/CodecUtils";
export type { TrackInfo as CodecTrackInfo } from "./core/CodecUtils";

// Subtitle management
export { SubtitleManager } from "./core/SubtitleManager";
export type { SubtitleTrackInfo, SubtitleManagerConfig } from "./core/SubtitleManager";

// =====================================================
// Alternative Player Components
// =====================================================
export { MistWebRTCPlayer } from "./components/players/MistWebRTCPlayer";

// =====================================================
// Types
// =====================================================
export type {
  // Player props
  PlayerProps,
  MistPlayerProps,
  PlayerOptions,
  EndpointInfo,
  OutputEndpoint,
  ContentEndpoints,
  ContentMetadata,
  PlayerState,
  PlayerStateContext,
  // Stream state types
  StreamStatus,
  StreamState,
  UseStreamStateOptions,
  MistStreamInfo,
  MistStreamSource,
  MistTrackInfo,
  // Quality types
  PlaybackQuality,
  QualityThresholds,
  UsePlaybackQualityOptions,
  // Meta track types
  MetaTrackEvent,
  MetaTrackEventType,
  SubtitleCue,
  ScoreUpdate,
  TimedEvent,
  ChapterMarker,
  UseMetaTrackOptions,
  // Telemetry types
  TelemetryPayload,
  TelemetryOptions,
  // ABR types
  ABRMode,
  ABROptions,
  QualityLevel,
  // Library configuration types
  HlsJsConfig,
  DashJsConfig,
} from "./types";

// =====================================================
// Vanilla JS Player (for non-React frameworks)
// =====================================================
export { FrameWorksPlayer } from "./vanilla/FrameWorksPlayer";
export type { FrameWorksPlayerOptions } from "./vanilla/FrameWorksPlayer";

// =====================================================
// Headless Core (framework-agnostic)
// =====================================================
export { TypedEventEmitter } from "./core/EventEmitter";
export { GatewayClient } from "./core/GatewayClient";
export type { GatewayClientConfig, GatewayClientEvents, GatewayStatus } from "./core/GatewayClient";
export { StreamStateClient } from "./core/StreamStateClient";
export type { StreamStateClientConfig, StreamStateClientEvents } from "./core/StreamStateClient";
export { PlayerController } from "./core/PlayerController";
export type { PlayerControllerConfig, PlayerControllerEvents } from "./core/PlayerController";
export { InteractionController } from "./core/InteractionController";
export type { InteractionControllerConfig, InteractionState } from "./core/InteractionController";

// =====================================================
// Styles & Core Exports
// =====================================================
export { ensurePlayerStyles, injectPlayerStyles } from "./styles";
export { globalPlayerManager, createPlayerManager, ensurePlayersRegistered } from "./core";
export type { StreamInfo, StreamSource } from "./core";
