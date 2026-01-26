/**
 * Svelte exports for SvelteKit/Svelte 5 environments.
 *
 * Full-featured player with React parity including:
 * - Gateway URL integration with GraphQL endpoint resolution
 * - MistServer WebSocket/HTTP stream state polling
 * - Quality monitoring and stats panel
 * - DevMode panel for player/source testing
 * - InteractionController for modern gestures
 * - MistReporter for stats reporting
 *
 * @example
 * ```svelte
 * <script>
 *   import { Player } from '@livepeer-frameworks/player-svelte';
 * </script>
 *
 * <Player
 *   contentId="pk_..."
 *   contentType="live"
 *   options={{ gatewayUrl: "https://gateway.example.com/graphql", devMode: true }}
 *   autoplay
 *   muted
 * />
 * ```
 */

// UI primitives
export { default as Button } from './ui/Button.svelte';
export { default as Badge } from './ui/Badge.svelte';
export { default as Slider } from './ui/Slider.svelte';

// Main components
export { default as Player } from './Player.svelte';
export { default as PlayerControls } from './PlayerControls.svelte';
export { default as SeekBar } from './SeekBar.svelte';

// Overlay components
export { default as SpeedIndicator } from './SpeedIndicator.svelte';
export { default as SkipIndicator } from './SkipIndicator.svelte';
export { default as IdleScreen } from './IdleScreen.svelte';
export { default as LoadingScreen } from './LoadingScreen.svelte';
export { default as StreamStateOverlay } from './StreamStateOverlay.svelte';
export { default as SubtitleRenderer } from './SubtitleRenderer.svelte';
export { default as DvdLogo } from './DvdLogo.svelte';
export { default as TitleOverlay } from './TitleOverlay.svelte';
export { default as ThumbnailOverlay } from './ThumbnailOverlay.svelte';
export { default as StatsPanel } from './StatsPanel.svelte';
export { default as DevModePanel } from './DevModePanel.svelte';

// Icon components
export * from './icons';

// Stores
export * from './stores';

// Context menu components
export * from './ui/context-menu';

// Svelte-specific types
export type { SkipDirection } from './types';

// Re-export core types and classes for Svelte users
export { PlayerController, PlayerManager, globalPlayerManager } from '@livepeer-frameworks/player-core';
export type { PlayerControllerConfig, PlayerControllerEvents } from '@livepeer-frameworks/player-core';
export type {
  PlayerState,
  PlayerStateContext,
  StreamState,
  StreamStatus,
  ContentMetadata,
  ContentEndpoints,
  EndpointInfo,
  PlaybackMode,
  PlaybackQuality,
  MistStreamInfo,
  PlayerOptions,
  PlayerMetadata,
  // Player selection types
  PlayerSelection,
  PlayerCombination,
  PlayerManagerOptions,
  PlayerManagerEvents,
} from '@livepeer-frameworks/player-core';
