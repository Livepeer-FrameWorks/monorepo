/**
 * Core Player Management System
 *
 * Exports all core functionality for the FrameWorks player system
 */

// Browser and codec detection
export * from "./detector";

// Scoring system
export * from "./scorer";

// Player interfaces and base classes
export * from "./PlayerInterface";

// Main player manager (single source of truth for selection)
export {
  PlayerManager,
  type PlayerSelection,
  type PlayerCombination,
  type PlayerManagerOptions,
  type PlayerManagerEvents,
} from "./PlayerManager";

// Player registry with all implementations
export * from "./PlayerRegistry";

// Re-export for convenience
export {
  globalPlayerManager,
  createPlayerManager,
  ensurePlayersRegistered,
} from "./PlayerRegistry";
export type { IPlayer, PlayerOptions } from "./PlayerInterface";

// New core classes (MistMetaPlayer feature backport)
export { QualityMonitor, PROTOCOL_THRESHOLDS } from "./QualityMonitor";
export type { QualityMonitorOptions, QualityMonitorState, PlayerProtocol } from "./QualityMonitor";
export { TelemetryReporter } from "./TelemetryReporter";
export type { TelemetryReporterConfig } from "./TelemetryReporter";
export { ABRController } from "./ABRController";
export type { ABRControllerConfig, ABRDecision } from "./ABRController";
export { MetaTrackManager } from "./MetaTrackManager";
export type { MetaTrackManagerConfig, MetaTrackSubscription } from "./MetaTrackManager";

// Headless core classes (framework-agnostic)
export { TypedEventEmitter } from "./EventEmitter";
export { GatewayClient } from "./GatewayClient";
export type { GatewayClientConfig, GatewayClientEvents, GatewayStatus } from "./GatewayClient";
export { StreamStateClient } from "./StreamStateClient";
export type { StreamStateClientConfig, StreamStateClientEvents } from "./StreamStateClient";
export {
  PlayerController,
  buildStreamInfoFromEndpoints,
  MIST_SOURCE_TYPES,
  PROTOCOL_TO_MIME,
  getMimeTypeForProtocol,
  getSourceTypeInfo,
} from "./PlayerController";
export type { PlayerControllerConfig, PlayerControllerEvents } from "./PlayerController";

// MistServer reporting (MistMetaPlayer feature backport)
export { MistReporter } from "./MistReporter";
export type {
  MistReporterStats,
  MistReporterOptions,
  MistReporterInitialReport,
} from "./MistReporter";

// MistServer WebRTC signaling (MistMetaPlayer feature backport)
export { MistSignaling } from "./MistSignaling";
export type {
  MistSignalingConfig,
  MistSignalingEvents,
  MistTimeUpdate,
  MistSignalingState,
} from "./MistSignaling";

// Live duration handling for live streams
export { LiveDurationProxy, createLiveVideoProxy } from "./LiveDurationProxy";
export type { LiveDurationProxyOptions, LiveDurationState } from "./LiveDurationProxy";

// Timer management for memory leak prevention
export { TimerManager } from "./TimerManager";

// Disposable interface for consistent cleanup
export { BaseDisposable, disposeAll, createCompositeDisposable } from "./Disposable";
export type { Disposable } from "./Disposable";

// URL utilities (MistMetaPlayer feature backport)
export {
  appendUrlParams,
  parseUrlParams,
  stripUrlParams,
  buildUrl,
  isSecureUrl,
  httpToWs,
  wsToHttp,
  matchPageProtocol,
} from "./UrlUtils";

// Codec utilities (MistMetaPlayer feature backport)
export { translateCodec, isCodecSupported, getBestSupportedTrack } from "./CodecUtils";
export type { TrackInfo } from "./CodecUtils";

// Subtitle management (MistMetaPlayer feature backport)
export { SubtitleManager } from "./SubtitleManager";
export type { SubtitleTrackInfo, SubtitleManagerConfig } from "./SubtitleManager";

// Interaction controller for modern player gestures + keyboard (VOD/Clip features)
export { InteractionController } from "./InteractionController";
export type { InteractionControllerConfig, InteractionState } from "./InteractionController";

// Screen Wake Lock for preventing device sleep during video playback
export { ScreenWakeLockManager } from "./ScreenWakeLockManager";
export type { ScreenWakeLockConfig } from "./ScreenWakeLockManager";

// Seeking utilities - centralized seeking/live detection logic
export {
  LATENCY_TIERS,
  SPEED_PRESETS,
  DEFAULT_BUFFER_WINDOW_SEC,
  getLatencyTier,
  isMediaStreamSource,
  supportsPlaybackRate,
  calculateSeekableRange,
  canSeekStream,
  calculateLiveThresholds,
  calculateIsNearLive,
  isLiveContent,
} from "./SeekingUtils";
export type {
  LatencyTier,
  LiveThresholds,
  SeekableRange,
  SeekableRangeParams,
  CanSeekParams,
} from "./SeekingUtils";

// Time formatting utilities
export {
  formatTime,
  formatClockTime,
  formatTimeDisplay,
  formatTooltipTime,
  formatDuration,
  parseTime,
} from "./TimeFormat";
export type { TimeDisplayParams } from "./TimeFormat";
