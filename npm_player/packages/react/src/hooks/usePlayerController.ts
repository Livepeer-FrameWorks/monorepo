/**
 * usePlayerController.ts
 *
 * React hook that wraps PlayerController for declarative usage.
 * Manages the complete player lifecycle and provides reactive state.
 */

import { useState, useEffect, useRef, useCallback, useMemo } from 'react';
import {
  PlayerController,
  type PlayerControllerConfig,
  type PlayerControllerEvents,
  type PlayerState,
  type StreamState,
  type StreamSource,
  type StreamInfo,
  type PlaybackQuality,
  type ContentEndpoints,
  type ContentMetadata,
  type MistStreamInfo,
} from '@livepeer-frameworks/player-core';

// ============================================================================
// Types
// ============================================================================

export interface UsePlayerControllerConfig extends Omit<PlayerControllerConfig, 'playerManager'> {
  /** Enable/disable the hook */
  enabled?: boolean;
  /** Callback when state changes */
  onStateChange?: (state: PlayerState) => void;
  /** Callback when stream state changes */
  onStreamStateChange?: (state: StreamState) => void;
  /** Callback when error occurs */
  onError?: (error: string) => void;
  /** Callback when ready */
  onReady?: (videoElement: HTMLVideoElement) => void;
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
  /** Available quality levels */
  qualities: Array<{ id: string; label: string; bitrate?: number; width?: number; height?: number; isAuto?: boolean; active?: boolean }>;
  /** Available text/caption tracks */
  textTracks: Array<{ id: string; label: string; language?: string; active: boolean }>;
  /** Stream info for player selection (sources + tracks) */
  streamInfo: StreamInfo | null;
}

export interface UsePlayerControllerReturn {
  /** Container ref to attach to your player container div */
  containerRef: React.RefObject<HTMLDivElement | null>;
  /** Current state (reactive) */
  state: PlayerControllerState;
  /** Controller instance (for direct method calls) */
  controller: PlayerController | null;
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
  /** Jump to live edge (for live streams) */
  jumpToLive: () => void;
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
  /** Set dev mode options (force player, type, source) */
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
  qualities: [],
  textTracks: [],
  streamInfo: null,
};

// ============================================================================
// Hook
// ============================================================================

export function usePlayerController(
  config: UsePlayerControllerConfig
): UsePlayerControllerReturn {
  const { enabled = true, onStateChange, onStreamStateChange, onError, onReady, ...controllerConfig } = config;

  const containerRef = useRef<HTMLDivElement>(null);
  const controllerRef = useRef<PlayerController | null>(null);
  const [state, setState] = useState<PlayerControllerState>(initialState);

  // Stable config ref for effect dependencies
  const configRef = useRef(controllerConfig);
  configRef.current = controllerConfig;

  // Create and attach controller
  useEffect(() => {
    if (!enabled) return;

    const container = containerRef.current;
    if (!container) return;

    // Create controller
    const controller = new PlayerController({
      contentId: configRef.current.contentId,
      contentType: configRef.current.contentType,
      endpoints: configRef.current.endpoints,
      gatewayUrl: configRef.current.gatewayUrl,
      mistUrl: configRef.current.mistUrl,
      authToken: configRef.current.authToken,
      autoplay: configRef.current.autoplay,
      muted: configRef.current.muted,
      controls: configRef.current.controls,
      poster: configRef.current.poster,
      debug: configRef.current.debug,
    });

    controllerRef.current = controller;

    // Subscribe to events
    const unsubs: Array<() => void> = [];

    // Sync state from controller - called on video events
    const syncState = () => {
      if (!controllerRef.current) return;
      const c = controllerRef.current;
      setState(prev => ({
        ...prev,
        isPlaying: c.isPlaying(),
        isPaused: c.isPaused(),
        isBuffering: c.isBuffering(),
        isMuted: c.isMuted(),
        volume: c.getVolume(),
        hasPlaybackStarted: c.hasPlaybackStarted(),
        shouldShowControls: c.shouldShowControls(),
        shouldShowIdleScreen: c.shouldShowIdleScreen(),
        playbackQuality: c.getPlaybackQuality(),
        isLoopEnabled: c.isLoopEnabled(),
        subtitlesEnabled: c.isSubtitlesEnabled(),
        qualities: c.getQualities(),
        streamInfo: c.getStreamInfo(),
      }));
    };

    unsubs.push(controller.on('stateChange', ({ state: newState }) => {
      setState(prev => ({ ...prev, state: newState }));
      onStateChange?.(newState);
    }));

    unsubs.push(controller.on('streamStateChange', ({ state: streamState }) => {
      setState(prev => ({
        ...prev,
        streamState,
        isEffectivelyLive: controller.isEffectivelyLive(),
        shouldShowIdleScreen: controller.shouldShowIdleScreen(),
      }));
      onStreamStateChange?.(streamState);
    }));

    unsubs.push(controller.on('timeUpdate', ({ currentTime, duration }) => {
      setState(prev => ({ ...prev, currentTime, duration }));
    }));

    unsubs.push(controller.on('error', ({ error }) => {
      setState(prev => ({
        ...prev,
        error,
        isPassiveError: controller.isPassiveError(),
      }));
      onError?.(error);
    }));

    unsubs.push(controller.on('errorCleared', () => {
      setState(prev => ({ ...prev, error: null, isPassiveError: false }));
    }));

    unsubs.push(controller.on('ready', ({ videoElement }) => {
      setState(prev => ({
        ...prev,
        videoElement,
        endpoints: controller.getEndpoints(),
        metadata: controller.getMetadata(),
        streamInfo: controller.getStreamInfo(),
        isEffectivelyLive: controller.isEffectivelyLive(),
        shouldShowIdleScreen: controller.shouldShowIdleScreen(),
        currentPlayerInfo: controller.getCurrentPlayerInfo(),
        currentSourceInfo: controller.getCurrentSourceInfo(),
        qualities: controller.getQualities(),
      }));
      onReady?.(videoElement);

      // Set up video event listeners AFTER video is ready
      // syncState is defined below - this closure captures it
      const handleVideoEvent = () => {
        if (controllerRef.current?.shouldSuppressVideoEvents?.()) return;
        syncState();
      };
      videoElement.addEventListener('play', handleVideoEvent);
      videoElement.addEventListener('pause', handleVideoEvent);
      videoElement.addEventListener('waiting', handleVideoEvent);
      videoElement.addEventListener('playing', handleVideoEvent);
      unsubs.push(() => {
        videoElement.removeEventListener('play', handleVideoEvent);
        videoElement.removeEventListener('pause', handleVideoEvent);
        videoElement.removeEventListener('waiting', handleVideoEvent);
        videoElement.removeEventListener('playing', handleVideoEvent);
      });
    }));

    unsubs.push(controller.on('playerSelected', ({ player, source }) => {
      setState(prev => ({
        ...prev,
        currentPlayerInfo: controller.getCurrentPlayerInfo(),
        currentSourceInfo: { url: source.url, type: source.type },
        qualities: controller.getQualities(),
      }));
    }));

    unsubs.push(controller.on('volumeChange', ({ volume, muted }) => {
      setState(prev => ({ ...prev, volume, isMuted: muted }));
    }));

    unsubs.push(controller.on('loopChange', ({ isLoopEnabled }) => {
      setState(prev => ({ ...prev, isLoopEnabled }));
    }));

    unsubs.push(controller.on('fullscreenChange', ({ isFullscreen }) => {
      setState(prev => ({ ...prev, isFullscreen }));
    }));

    unsubs.push(controller.on('pipChange', ({ isPiP }) => {
      setState(prev => ({ ...prev, isPiPActive: isPiP }));
    }));

    unsubs.push(controller.on('holdSpeedStart', ({ speed }) => {
      setState(prev => ({ ...prev, isHoldingSpeed: true, holdSpeed: speed }));
    }));

    unsubs.push(controller.on('holdSpeedEnd', () => {
      setState(prev => ({ ...prev, isHoldingSpeed: false }));
    }));

    unsubs.push(controller.on('hoverStart', () => {
      setState(prev => ({ ...prev, isHovering: true, shouldShowControls: true }));
    }));

    unsubs.push(controller.on('hoverEnd', () => {
      setState(prev => ({
        ...prev,
        isHovering: false,
        shouldShowControls: controller.shouldShowControls(),
      }));
    }));

    unsubs.push(controller.on('captionsChange', ({ enabled }) => {
      setState(prev => ({ ...prev, subtitlesEnabled: enabled }));
    }));

    // Attach controller to container
    // Note: Video event listeners are set up in the 'ready' handler above
    controller.attach(container).catch(err => {
      console.warn('[usePlayerController] Attach failed:', err);
    });

    // Set initial state
    setState(prev => ({
      ...prev,
      isLoopEnabled: controller.isLoopEnabled(),
    }));

    return () => {
      unsubs.forEach(fn => fn());
      controller.destroy();
      controllerRef.current = null;
      setState(initialState);
    };
  }, [enabled, config.contentId, config.contentType]); // Re-create on content change

  // Stable action callbacks
  const play = useCallback(async () => {
    await controllerRef.current?.play();
  }, []);

  const pause = useCallback(() => {
    controllerRef.current?.pause();
  }, []);

  const togglePlay = useCallback(() => {
    controllerRef.current?.togglePlay();
  }, []);

  const seek = useCallback((time: number) => {
    controllerRef.current?.seek(time);
  }, []);

  const seekBy = useCallback((delta: number) => {
    controllerRef.current?.seekBy(delta);
  }, []);

  const setVolume = useCallback((volume: number) => {
    controllerRef.current?.setVolume(volume);
  }, []);

  const toggleMute = useCallback(() => {
    controllerRef.current?.toggleMute();
  }, []);

  const toggleLoop = useCallback(() => {
    controllerRef.current?.toggleLoop();
  }, []);

  const toggleFullscreen = useCallback(async () => {
    await controllerRef.current?.toggleFullscreen();
  }, []);

  const togglePiP = useCallback(async () => {
    await controllerRef.current?.togglePictureInPicture();
  }, []);

  const toggleSubtitles = useCallback(() => {
    controllerRef.current?.toggleSubtitles();
  }, []);

  const clearError = useCallback(() => {
    controllerRef.current?.clearError();
    setState(prev => ({ ...prev, error: null, isPassiveError: false }));
  }, []);

  const jumpToLive = useCallback(() => {
    controllerRef.current?.jumpToLive();
  }, []);

  const retry = useCallback(async () => {
    await controllerRef.current?.retry();
  }, []);

  const reload = useCallback(async () => {
    await controllerRef.current?.reload();
  }, []);

  const getQualities = useCallback(() => {
    return controllerRef.current?.getQualities() ?? [];
  }, []);

  const selectQuality = useCallback((id: string) => {
    controllerRef.current?.selectQuality(id);
  }, []);

  const handleMouseEnter = useCallback(() => {
    controllerRef.current?.handleMouseEnter();
  }, []);

  const handleMouseLeave = useCallback(() => {
    controllerRef.current?.handleMouseLeave();
  }, []);

  const handleMouseMove = useCallback(() => {
    controllerRef.current?.handleMouseMove();
  }, []);

  const handleTouchStart = useCallback(() => {
    controllerRef.current?.handleTouchStart();
  }, []);

  const setDevModeOptions = useCallback(async (options: {
    forcePlayer?: string;
    forceType?: string;
    forceSource?: number;
    playbackMode?: 'auto' | 'low-latency' | 'quality' | 'vod';
  }) => {
    await controllerRef.current?.setDevModeOptions(options);
  }, []);

  return {
    containerRef,
    state,
    controller: controllerRef.current,
    play,
    pause,
    togglePlay,
    seek,
    seekBy,
    jumpToLive,
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

export default usePlayerController;
