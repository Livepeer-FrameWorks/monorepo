/**
 * Svelte stores for player state management.
 *
 * These stores provide reactive state management for:
 * - MistServer stream state polling (WebSocket/HTTP)
 * - Gateway endpoint resolution (GraphQL)
 * - Player instance context sharing
 * - Playback quality monitoring
 */

// Stream state (MistServer polling)
export {
  createStreamStateManager,
  createDerivedStreamStatus,
  createDerivedIsOnline,
  createDerivedStreamInfo,
  type StreamStateOptions,
  type StreamStateStore,
} from "./streamState";

// Viewer endpoints (Gateway resolution)
export {
  createEndpointResolver,
  createDerivedEndpoints,
  createDerivedPrimaryEndpoint,
  createDerivedMetadata,
  createDerivedStatus,
  type ViewerEndpointsOptions,
  type ViewerEndpointsStore,
  type ViewerEndpointsState,
  type EndpointStatus,
} from "./viewerEndpoints";

// Player context (instance sharing)
export {
  createPlayerContext,
  setPlayerContextInComponent,
  getPlayerContextFromComponent,
  getPlayerContextOrFallback,
  createDerivedVideoElement,
  createDerivedIsReady,
  createDerivedPlayerInfo,
  type PlayerContextState,
  type PlayerContextStore,
} from "./playerContext";

// Playback quality monitoring
export {
  createPlaybackQualityMonitor,
  createDerivedQualityScore,
  createDerivedStallCount,
  createDerivedFrameDropRate,
  createDerivedBitrate,
  createDerivedLatency,
  type PlaybackQualityOptions,
  type PlaybackQualityStore,
} from "./playbackQuality";

// Player selection (event-driven, cached)
export {
  createPlayerSelectionStore,
  createDerivedSelection,
  createDerivedCombinations,
  createDerivedReady,
  createDerivedSelectedPlayer,
  createDerivedSelectedSourceType,
  createDerivedCompatibleCombinations,
  createDerivedIncompatibleCombinations,
  type PlayerSelectionOptions,
  type PlayerSelectionState,
  type PlayerSelectionStore,
} from "./playerSelection";

// i18n (locale + translator)
export { localeStore, translatorStore } from "./i18n";

// PlayerController store (central orchestrator)
export {
  createPlayerControllerStore,
  createDerivedState,
  createDerivedIsPlaying,
  createDerivedCurrentTime,
  createDerivedDuration,
  createDerivedError,
  createDerivedVideoElement as createDerivedControllerVideoElement,
  createDerivedShouldShowControls,
  createDerivedShouldShowIdleScreen,
  type PlayerControllerStoreConfig,
  type PlayerControllerState,
  type PlayerControllerStore,
} from "./playerController";
