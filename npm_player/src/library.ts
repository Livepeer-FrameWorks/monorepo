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
export { QualityMonitor } from "./core/QualityMonitor";
export { TelemetryReporter } from "./core/TelemetryReporter";
export { ABRController } from "./core/ABRController";
export { MetaTrackManager } from "./core/MetaTrackManager";

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
} from "./types";

// =====================================================
// Styles & Core Exports
// =====================================================
export { ensurePlayerStyles, injectPlayerStyles } from "./styles";
export { globalPlayerManager, createPlayerManager, ensurePlayersRegistered } from "./core";
export type { StreamInfo, StreamSource } from "./core";
