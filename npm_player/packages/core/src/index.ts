/**
 * @livepeer-frameworks/player-core
 *
 * Framework-agnostic core player logic for FrameWorks streaming.
 * This package provides:
 * - PlayerManager: Intelligent player/protocol selection
 * - PlayerController: High-level player orchestration
 * - GatewayClient: Gateway endpoint resolution
 * - StreamStateClient: MistServer WebSocket/HTTP polling
 * - Quality monitoring, ABR control, interaction handling
 */

// Core classes
export { PlayerManager } from "./core/PlayerManager";
export type {
  PlayerSelection,
  PlayerCombination,
  PlayerManagerOptions,
  PlayerManagerEvents,
} from "./core/PlayerManager";
export { PlayerController, buildStreamInfoFromEndpoints } from "./core/PlayerController";
export type { PlayerControllerConfig, PlayerControllerEvents } from "./core/PlayerController";
export {
  ensurePlayersRegistered,
  registerAllPlayers,
  createPlayerManager,
  globalPlayerManager,
  globalPlayerManager as globalRegistry,
} from "./core/PlayerRegistry";
export { GatewayClient } from "./core/GatewayClient";

// Player implementations (framework-agnostic)
export * from "./players";
export { StreamStateClient } from "./core/StreamStateClient";
export { QualityMonitor } from "./core/QualityMonitor";
export { ABRController } from "./core/ABRController";
export { InteractionController } from "./core/InteractionController";
export { MistSignaling } from "./core/MistSignaling";
export { MistReporter } from "./core/MistReporter";
export { MetaTrackManager } from "./core/MetaTrackManager";
export type { MetaTrackSubscription } from "./core/MetaTrackManager";
export { SubtitleManager } from "./core/SubtitleManager";
export { LiveDurationProxy } from "./core/LiveDurationProxy";
export { TimerManager } from "./core/TimerManager";
export { TypedEventEmitter, TypedEventEmitter as EventEmitter } from "./core/EventEmitter";
export { TelemetryReporter } from "./core/TelemetryReporter";

// Player interface and base class
export type { IPlayer, PlayerCapability, PlayerEvents } from "./core/PlayerInterface";
export { BasePlayer } from "./core/PlayerInterface";
export type {
  StreamSource,
  StreamTrack,
  StreamInfo,
  PlayerOptions as CorePlayerOptions,
} from "./core/PlayerInterface";

// Utilities
export * from "./core/scorer";
export * from "./core/selector";
export * from "./core/detector";
export * from "./core/UrlUtils";
// Note: CodecUtils has overlapping exports with detector (translateCodec), export specific items if needed

// Seeking utilities (centralized from React/Svelte wrappers)
export * from "./core/SeekingUtils";
export type {
  LatencyTier,
  LiveThresholds,
  SeekableRange,
  SeekableRangeParams,
  CanSeekParams,
} from "./core/SeekingUtils";

// Time formatting utilities
export * from "./core/TimeFormat";
export type { TimeDisplayParams } from "./core/TimeFormat";

// Styles
export { ensurePlayerStyles, injectPlayerStyles } from "./styles";

// Utility functions
export { cn } from "./lib/utils";

// Types
export * from "./types";
