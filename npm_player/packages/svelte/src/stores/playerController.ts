/**
 * Svelte store for PlayerController - wraps the core PlayerController
 * for declarative usage in Svelte 5 components.
 */

import { writable, derived, type Readable, type Writable } from 'svelte/store';
import {
  PlayerController,
  type PlayerControllerConfig,
  type PlayerState,
  type StreamState,
  type StreamSource,
  type PlaybackQuality,
  type ContentEndpoints,
  type ContentMetadata,
} from '@livepeer-frameworks/player-core';

// ============================================================================
// Types
// ============================================================================

export interface PlayerControllerStoreConfig extends Omit<PlayerControllerConfig, 'playerManager'> {
  /** Enable/disable the store */
  enabled?: boolean;
}

export interface PlayerControllerState {
  /** Current player state */
  state: PlayerState;
  /** Stream state (for live streams) */
  streamState: StreamState | null;
  /** Resolved endpoints */
  endpoints: ContentEndpoints | null;
  /** Content metadata */
  metadata: ContentMetadata | null;
  /** Video element (null if not ready) */
  videoElement: HTMLVideoElement | null;
  /** Current time */
  currentTime: number;
  /** Duration */
  duration: number;
  /** Is playing */
  isPlaying: boolean;
  /** Is paused */
  isPaused: boolean;
  /** Is buffering */
  isBuffering: boolean;
  /** Is muted */
  isMuted: boolean;
  /** Volume (0-1) */
  volume: number;
  /** Error text */
  error: string | null;
  /** Is passive error */
  isPassiveError: boolean;
  /** Has playback ever started */
  hasPlaybackStarted: boolean;
  /** Is holding speed (2x gesture) */
  isHoldingSpeed: boolean;
  /** Current hold speed */
  holdSpeed: number;
  /** Is hovering (controls visible) */
  isHovering: boolean;
  /** Should show controls */
  shouldShowControls: boolean;
  /** Is loop enabled */
  isLoopEnabled: boolean;
  /** Is fullscreen */
  isFullscreen: boolean;
  /** Is PiP active */
  isPiPActive: boolean;
  /** Is effectively live (live or DVR recording) */
  isEffectivelyLive: boolean;
  /** Should show idle screen */
  shouldShowIdleScreen: boolean;
  /** Current player info */
  currentPlayerInfo: { name: string; shortname: string } | null;
  /** Current source info */
  currentSourceInfo: { url: string; type: string } | null;
  /** Playback quality metrics */
  playbackQuality: PlaybackQuality | null;
  /** Subtitles enabled */
  subtitlesEnabled: boolean;
}

export interface PlayerControllerStore extends Readable<PlayerControllerState> {
  /** Get controller instance */
  getController: () => PlayerController | null;
  /** Attach to a container element */
  attach: (container: HTMLElement) => Promise<void>;
  /** Detach from container */
  detach: () => void;
  /** Destroy the store and controller */
  destroy: () => void;
  /** Play */
  play: () => Promise<void>;
  /** Pause */
  pause: () => void;
  /** Toggle play/pause */
  togglePlay: () => void;
  /** Seek to time */
  seek: (time: number) => void;
  /** Seek by delta */
  seekBy: (delta: number) => void;
  /** Set volume */
  setVolume: (volume: number) => void;
  /** Toggle mute */
  toggleMute: () => void;
  /** Toggle loop */
  toggleLoop: () => void;
  /** Toggle fullscreen */
  toggleFullscreen: () => Promise<void>;
  /** Toggle PiP */
  togglePiP: () => Promise<void>;
  /** Toggle subtitles */
  toggleSubtitles: () => void;
  /** Clear error */
  clearError: () => void;
  /** Retry playback */
  retry: () => Promise<void>;
  /** Reload player */
  reload: () => Promise<void>;
  /** Get qualities */
  getQualities: () => Array<{ id: string; label: string; bitrate?: number }>;
  /** Select quality */
  selectQuality: (id: string) => void;
  /** Handle mouse enter (for controls visibility) */
  handleMouseEnter: () => void;
  /** Handle mouse leave (for controls visibility) */
  handleMouseLeave: () => void;
  /** Handle mouse move (for controls visibility) */
  handleMouseMove: () => void;
  /** Handle touch start (for controls visibility) */
  handleTouchStart: () => void;
  /** Set dev mode options (force player, type, source, playback mode) */
  setDevModeOptions: (options: {
    forcePlayer?: string;
    forceType?: string;
    forceSource?: number;
    playbackMode?: 'auto' | 'low-latency' | 'quality' | 'vod';
  }) => Promise<void>;
}

// ============================================================================
// Initial State
// ============================================================================

const initialState: PlayerControllerState = {
  state: 'booting',
  streamState: null,
  endpoints: null,
  metadata: null,
  videoElement: null,
  currentTime: 0,
  duration: NaN,
  isPlaying: false,
  isPaused: true,
  isBuffering: false,
  isMuted: true,
  volume: 1,
  error: null,
  isPassiveError: false,
  hasPlaybackStarted: false,
  isHoldingSpeed: false,
  holdSpeed: 2,
  isHovering: false,
  shouldShowControls: false,
  isLoopEnabled: false,
  isFullscreen: false,
  isPiPActive: false,
  isEffectivelyLive: false,
  shouldShowIdleScreen: true,
  currentPlayerInfo: null,
  currentSourceInfo: null,
  playbackQuality: null,
  subtitlesEnabled: false,
};

// ============================================================================
// Store Factory
// ============================================================================

/**
 * Create a PlayerController store for managing player lifecycle.
 *
 * @example
 * ```svelte
 * <script>
 *   import { createPlayerControllerStore } from './stores/playerController';
 *   import { onMount, onDestroy } from 'svelte';
 *
 *   let containerEl: HTMLElement;
 *
 *   const playerStore = createPlayerControllerStore({
 *     contentId: 'my-stream',
 *     contentType: 'live',
 *     gatewayUrl: 'https://gateway.example.com/graphql',
 *   });
 *
 *   onMount(() => {
 *     playerStore.attach(containerEl);
 *   });
 *
 *   onDestroy(() => {
 *     playerStore.destroy();
 *   });
 *
 *   // Access state reactively
 *   $: isPlaying = $playerStore.isPlaying;
 *   $: currentTime = $playerStore.currentTime;
 * </script>
 *
 * <div bind:this={containerEl}></div>
 * ```
 */
export function createPlayerControllerStore(
  config: PlayerControllerStoreConfig
): PlayerControllerStore {
  const { enabled = true, ...controllerConfig } = config;

  // Internal state
  const store = writable<PlayerControllerState>(initialState);
  let controller: PlayerController | null = null;
  let unsubscribers: Array<() => void> = [];

  /**
   * Sync state from controller to store
   */
  function syncState() {
    if (!controller) return;

    store.update(prev => ({
      ...prev,
      isPlaying: controller!.isPlaying(),
      isPaused: controller!.isPaused(),
      isBuffering: controller!.isBuffering(),
      isMuted: controller!.isMuted(),
      volume: controller!.getVolume(),
      hasPlaybackStarted: controller!.hasPlaybackStarted(),
      shouldShowControls: controller!.shouldShowControls(),
      shouldShowIdleScreen: controller!.shouldShowIdleScreen(),
      playbackQuality: controller!.getPlaybackQuality(),
      isLoopEnabled: controller!.isLoopEnabled(),
      subtitlesEnabled: controller!.isSubtitlesEnabled(),
    }));
  }

  /**
   * Attach to a container element
   */
  async function attach(container: HTMLElement): Promise<void> {
    if (!enabled) return;

    // Clean up existing controller
    if (controller) {
      unsubscribers.forEach(fn => fn());
      unsubscribers = [];
      controller.destroy();
    }

    // Create new controller
    controller = new PlayerController(controllerConfig);

    // Subscribe to events
    unsubscribers.push(controller.on('stateChange', ({ state }) => {
      store.update(prev => ({ ...prev, state }));
    }));

    unsubscribers.push(controller.on('streamStateChange', ({ state: streamState }) => {
      store.update(prev => ({
        ...prev,
        streamState,
        isEffectivelyLive: controller!.isEffectivelyLive(),
        shouldShowIdleScreen: controller!.shouldShowIdleScreen(),
      }));
    }));

    unsubscribers.push(controller.on('timeUpdate', ({ currentTime, duration }) => {
      store.update(prev => ({ ...prev, currentTime, duration }));
    }));

    unsubscribers.push(controller.on('error', ({ error }) => {
      store.update(prev => ({
        ...prev,
        error,
        isPassiveError: controller!.isPassiveError(),
      }));
    }));

    unsubscribers.push(controller.on('errorCleared', () => {
      store.update(prev => ({ ...prev, error: null, isPassiveError: false }));
    }));

    unsubscribers.push(controller.on('ready', ({ videoElement }) => {
      store.update(prev => ({
        ...prev,
        videoElement,
        endpoints: controller!.getEndpoints(),
        metadata: controller!.getMetadata(),
        isEffectivelyLive: controller!.isEffectivelyLive(),
        shouldShowIdleScreen: controller!.shouldShowIdleScreen(),
        currentPlayerInfo: controller!.getCurrentPlayerInfo(),
        currentSourceInfo: controller!.getCurrentSourceInfo(),
      }));

      // Add video event listeners for state sync
      const video = videoElement;
      const handleVideoEvent = () => {
        if (controller?.shouldSuppressVideoEvents?.()) return;
        syncState();
      };
      video.addEventListener('play', handleVideoEvent);
      video.addEventListener('pause', handleVideoEvent);
      video.addEventListener('waiting', handleVideoEvent);
      video.addEventListener('playing', handleVideoEvent);
      unsubscribers.push(() => {
        video.removeEventListener('play', handleVideoEvent);
        video.removeEventListener('pause', handleVideoEvent);
        video.removeEventListener('waiting', handleVideoEvent);
        video.removeEventListener('playing', handleVideoEvent);
      });
    }));

    unsubscribers.push(controller.on('playerSelected', ({ player, source }) => {
      store.update(prev => ({
        ...prev,
        currentPlayerInfo: controller!.getCurrentPlayerInfo(),
        currentSourceInfo: { url: source.url, type: source.type },
      }));
    }));

    unsubscribers.push(controller.on('volumeChange', ({ volume, muted }) => {
      store.update(prev => ({ ...prev, volume, isMuted: muted }));
    }));

    unsubscribers.push(controller.on('loopChange', ({ isLoopEnabled }) => {
      store.update(prev => ({ ...prev, isLoopEnabled }));
    }));

    unsubscribers.push(controller.on('fullscreenChange', ({ isFullscreen }) => {
      store.update(prev => ({ ...prev, isFullscreen }));
    }));

    unsubscribers.push(controller.on('pipChange', ({ isPiP }) => {
      store.update(prev => ({ ...prev, isPiPActive: isPiP }));
    }));

    unsubscribers.push(controller.on('holdSpeedStart', ({ speed }) => {
      store.update(prev => ({ ...prev, isHoldingSpeed: true, holdSpeed: speed }));
    }));

    unsubscribers.push(controller.on('holdSpeedEnd', () => {
      store.update(prev => ({ ...prev, isHoldingSpeed: false }));
    }));

    unsubscribers.push(controller.on('hoverStart', () => {
      store.update(prev => ({ ...prev, isHovering: true, shouldShowControls: true }));
    }));

    unsubscribers.push(controller.on('hoverEnd', () => {
      store.update(prev => ({
        ...prev,
        isHovering: false,
        shouldShowControls: controller!.shouldShowControls(),
      }));
    }));

    unsubscribers.push(controller.on('captionsChange', ({ enabled }) => {
      store.update(prev => ({ ...prev, subtitlesEnabled: enabled }));
    }));

    // Set initial loop state
    store.update(prev => ({
      ...prev,
      isLoopEnabled: controller!.isLoopEnabled(),
    }));

    // Attach controller to container
    await controller.attach(container);
  }

  /**
   * Detach from container
   */
  function detach(): void {
    if (controller) {
      controller.detach();
    }
    store.set(initialState);
  }

  /**
   * Destroy the store and controller
   */
  function destroy(): void {
    unsubscribers.forEach(fn => fn());
    unsubscribers = [];

    if (controller) {
      controller.destroy();
      controller = null;
    }

    store.set(initialState);
  }

  // Action methods
  async function play(): Promise<void> {
    await controller?.play();
  }

  function pause(): void {
    controller?.pause();
  }

  function togglePlay(): void {
    controller?.togglePlay();
  }

  function seek(time: number): void {
    controller?.seek(time);
  }

  function seekBy(delta: number): void {
    controller?.seekBy(delta);
  }

  function setVolume(volume: number): void {
    controller?.setVolume(volume);
  }

  function toggleMute(): void {
    controller?.toggleMute();
  }

  function toggleLoop(): void {
    controller?.toggleLoop();
  }

  async function toggleFullscreen(): Promise<void> {
    await controller?.toggleFullscreen();
  }

  async function togglePiP(): Promise<void> {
    await controller?.togglePictureInPicture();
  }

  function toggleSubtitles(): void {
    controller?.toggleSubtitles();
  }

  function clearError(): void {
    controller?.clearError();
    store.update(prev => ({ ...prev, error: null, isPassiveError: false }));
  }

  async function retry(): Promise<void> {
    await controller?.retry();
  }

  async function reload(): Promise<void> {
    await controller?.reload();
  }

  function getQualities() {
    return controller?.getQualities() ?? [];
  }

  function selectQuality(id: string): void {
    controller?.selectQuality(id);
  }

  function handleMouseEnter(): void {
    controller?.handleMouseEnter();
  }

  function handleMouseLeave(): void {
    controller?.handleMouseLeave();
  }

  function handleMouseMove(): void {
    controller?.handleMouseMove();
  }

  function handleTouchStart(): void {
    controller?.handleTouchStart();
  }

  async function setDevModeOptions(options: {
    forcePlayer?: string;
    forceType?: string;
    forceSource?: number;
    playbackMode?: 'auto' | 'low-latency' | 'quality' | 'vod';
  }): Promise<void> {
    await controller?.setDevModeOptions(options);
  }

  function getController(): PlayerController | null {
    return controller;
  }

  return {
    subscribe: store.subscribe,
    getController,
    attach,
    detach,
    destroy,
    play,
    pause,
    togglePlay,
    seek,
    seekBy,
    setVolume,
    toggleMute,
    toggleLoop,
    toggleFullscreen,
    togglePiP,
    toggleSubtitles,
    clearError,
    retry,
    reload,
    getQualities,
    selectQuality,
    handleMouseEnter,
    handleMouseLeave,
    handleMouseMove,
    handleTouchStart,
    setDevModeOptions,
  };
}

// ============================================================================
// Derived Stores (convenience)
// ============================================================================

export function createDerivedState(store: PlayerControllerStore) {
  return derived(store, $state => $state.state);
}

export function createDerivedIsPlaying(store: PlayerControllerStore) {
  return derived(store, $state => $state.isPlaying);
}

export function createDerivedCurrentTime(store: PlayerControllerStore) {
  return derived(store, $state => $state.currentTime);
}

export function createDerivedDuration(store: PlayerControllerStore) {
  return derived(store, $state => $state.duration);
}

export function createDerivedError(store: PlayerControllerStore) {
  return derived(store, $state => $state.error);
}

export function createDerivedVideoElement(store: PlayerControllerStore) {
  return derived(store, $state => $state.videoElement);
}

export function createDerivedShouldShowControls(store: PlayerControllerStore) {
  return derived(store, $state => $state.shouldShowControls);
}

export function createDerivedShouldShowIdleScreen(store: PlayerControllerStore) {
  return derived(store, $state => $state.shouldShowIdleScreen);
}

export default createPlayerControllerStore;
