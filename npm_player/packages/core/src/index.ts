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
  getAvailablePlayerCapabilities,
} from "./core/PlayerRegistry";
export { GatewayClient } from "./core/GatewayClient";

export { StreamStateClient } from "./core/StreamStateClient";
export { QualityMonitor } from "./core/QualityMonitor";
export { ABRController } from "./core/ABRController";
export { InteractionController, DEFAULT_KEY_MAP } from "./core/InteractionController";
export type { PlayerKeyMap } from "./core/InteractionController";
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
export type {
  IPlayer,
  PlayerCapability,
  PlayerEvents,
  ErrorHandlingEvents,
  ClassifiedError,
} from "./core/PlayerInterface";
export { BasePlayer, ErrorSeverity, ErrorCode } from "./core/PlayerInterface";
export type {
  StreamSource,
  StreamTrack,
  StreamInfo,
  PlayerOptions as CorePlayerOptions,
} from "./core/PlayerInterface";

// Error classification and recovery
export { ErrorClassifier } from "./core/ErrorClassifier";
export type { RecoveryAction, ErrorClassifierOptions } from "./core/ErrorClassifier";

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

// Theming
export {
  applyTheme,
  applyThemeOverrides,
  clearTheme,
  themeOverridesToStyle,
  resolveTheme,
  getAvailableThemes,
  getThemeDisplayName,
} from "./core/ThemeManager";
export type { FwThemePreset, FwThemeOverrides } from "./core/ThemeManager";

// Internationalization
export {
  DEFAULT_TRANSLATIONS,
  createTranslator,
  translate,
  getAvailableLocales,
  getLocaleDisplayName,
  getLocalePack,
} from "./core/I18n";
export type { TranslationStrings, FwLocale, I18nConfig, TranslateFn } from "./core/I18n";

// Audio gain (Web Audio API volume boost)
export { AudioGainController } from "./core/AudioGainController";
export type { AudioGainConfig } from "./core/AudioGainController";

// AirPlay (Safari)
export { AirPlayController } from "./core/AirPlayController";

// Responsive breakpoints (ResizeObserver)
export { BreakpointObserver } from "./core/BreakpointObserver";
export type { BreakpointConfig } from "./core/BreakpointObserver";

// Thumbnail VTT sprite sheet parser
export { parseThumbnailVtt, findCueAtTime, fetchThumbnailVtt } from "./core/ThumbnailVttParser";
export type { ThumbnailCue } from "./core/ThumbnailVttParser";

// Styles
export { ensurePlayerStyles, injectPlayerStyles } from "./styles";

// Vanilla facade
export { createPlayer } from "./vanilla/createPlayer";
export type {
  CreatePlayerConfig,
  PlayerInstance,
  PlayerCapabilities,
} from "./vanilla/createPlayer";

// Reactive state (per-property subscriptions)
export { createReactiveState } from "./vanilla/ReactiveState";
export type { ReactiveState, ReactiveStateProperty } from "./vanilla/ReactiveState";

// Blueprint system
export type {
  BlueprintContext,
  BlueprintFactory,
  BlueprintMap,
  StructureDescriptor,
} from "./vanilla/Blueprint";
export { DEFAULT_BLUEPRINTS } from "./vanilla/defaultBlueprints";
export { DEFAULT_STRUCTURE } from "./vanilla/defaultStructure";
export { buildStructure } from "./vanilla/StructureBuilder";

// Skin registry
export { FwSkins, registerSkin, resolveSkin } from "./vanilla/SkinRegistry";
export type { SkinDefinition, ResolvedSkin } from "./vanilla/SkinRegistry";

// Simplified player registration
export { registerPlayer } from "./vanilla/registerPlayer";
export type { SimplePlayerDefinition } from "./vanilla/registerPlayer";

// Utility functions
export { cn } from "./lib/utils";

// Types
export * from "./types";
