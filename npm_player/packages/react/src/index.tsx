/**
 * @livepeer-frameworks/player-react
 *
 * React components for FrameWorks streaming player.
 */

// Main player component
export { default as Player } from "./components/Player";
export { default as PlayerControls } from "./components/PlayerControls";

// Overlay components
export { default as LoadingScreen } from "./components/LoadingScreen";
export { default as IdleScreen } from "./components/IdleScreen";
export { default as ThumbnailOverlay } from "./components/ThumbnailOverlay";
export { default as TitleOverlay } from "./components/TitleOverlay";
export { default as StreamStateOverlay } from "./components/StreamStateOverlay";
export { default as StatsPanel } from "./components/StatsPanel";
export { default as DevModePanel } from "./components/DevModePanel";
export { default as PlayerErrorBoundary } from "./components/PlayerErrorBoundary";

// Composable control components
export {
  PlayButton,
  SkipButton,
  VolumeControl,
  TimeDisplay,
  LiveBadge,
  FullscreenButton,
  ControlBar,
  SettingsMenu,
} from "./components/controls";
export type {
  PlayButtonProps,
  SkipButtonProps,
  VolumeControlProps,
  TimeDisplayProps,
  LiveBadgeProps,
  FullscreenButtonProps,
  ControlBarProps,
  SettingsMenuProps,
} from "./components/controls";

// Icon components
export * from "./components/Icons";

// UI primitives
export { Button } from "./ui/button";
export { Badge } from "./ui/badge";
export { Slider } from "./ui/slider";

// Context
export { PlayerProvider } from "./context/PlayerContext";
export type { PlayerProviderProps } from "./context/PlayerContext";
export { usePlayerContext, usePlayerContextOptional } from "./context/player";
export type { PlayerContextValue } from "./context/player";
export { I18nProvider } from "./context/I18nContext";
export { useTranslate } from "./context/i18n";

// Hooks
export { useStreamState } from "./hooks/useStreamState";
export { usePlaybackQuality } from "./hooks/usePlaybackQuality";
export { useViewerEndpoints } from "./hooks/useViewerEndpoints";
export { useMetaTrack } from "./hooks/useMetaTrack";
export { useTelemetry } from "./hooks/useTelemetry";
export { usePlayerSelection } from "./hooks/usePlayerSelection";
export type {
  UsePlayerSelectionOptions,
  UsePlayerSelectionReturn,
} from "./hooks/usePlayerSelection";
export { usePlayerController } from "./hooks/usePlayerController";
export type {
  UsePlayerControllerConfig,
  UsePlayerControllerReturn,
  PlayerControllerState,
} from "./hooks/usePlayerController";

// Types
export * from "./types";

// Re-export commonly used core items
export {
  PlayerManager,
  globalPlayerManager,
  PlayerController,
  GatewayClient,
  StreamStateClient,
  QualityMonitor,
  cn,
} from "@livepeer-frameworks/player-core";

export type {
  PlayerState,
  PlayerStateContext,
  StreamState,
  StreamStatus,
  MistStreamInfo,
  PlaybackQuality,
  PlaybackMode,
  ContentEndpoints,
  EndpointInfo,
  PlayerSelection,
  PlayerCombination,
  PlayerManagerEvents,
} from "@livepeer-frameworks/player-core";
