import React, { useEffect, useMemo, useState, useRef, useCallback } from "react";
import { usePlayerContextOptional } from "../context/PlayerContext";
import {
  cn,
  // Seeking utilities from core
  SPEED_PRESETS,
  isMediaStreamSource,
  supportsPlaybackRate as coreSupportsPlaybackRate,
  calculateSeekableRange,
  canSeekStream,
  calculateLiveThresholds,
  calculateIsNearLive,
  isLiveContent,
  // Time formatting from core
  formatTimeDisplay,
} from "@livepeer-frameworks/player-core";
import { Slider } from "../ui/slider";
import SeekBar from "./SeekBar";
import {
  FullscreenToggleIcon,
  PlayPauseIcon,
  SeekToLiveIcon,
  SkipBackIcon,
  SkipForwardIcon,
  VolumeIcon,
  SettingsIcon
} from "./Icons";
import type { MistStreamInfo, PlaybackMode } from "../types";

interface PlayerControlsProps {
  currentTime: number;
  duration: number;
  isVisible?: boolean;
  className?: string;
  onSeek?: (time: number) => void;
  showStatsButton?: boolean;
  isStatsOpen?: boolean;
  onStatsToggle?: () => void;
  /** Live MistServer stream info - drives control visibility based on server metadata */
  mistStreamInfo?: MistStreamInfo;
  /** Disable all controls (e.g., while player is initializing) */
  disabled?: boolean;
  /** Current playback mode */
  playbackMode?: PlaybackMode;
  /** Callback when playback mode changes */
  onModeChange?: (mode: PlaybackMode) => void;
  /** Current source protocol type (e.g., 'whep', 'ws/video/mp4', 'html5/application/vnd.apple.mpegurl') */
  sourceType?: string;
  /** Content-type based live flag (for mode selector visibility, separate from seek bar isLive) */
  isContentLive?: boolean;
  /** Video element - passed from parent hook */
  videoElement?: HTMLVideoElement | null;
  /** Available quality levels - passed from parent hook */
  qualities?: Array<{ id: string; label: string; bitrate?: number; width?: number; height?: number; isAuto?: boolean; active?: boolean }>;
  /** Callback to select quality */
  onSelectQuality?: (id: string) => void;
  /** Is player muted */
  isMuted?: boolean;
  /** Current volume (0-1) */
  volume?: number;
  /** Callback for volume change */
  onVolumeChange?: (volume: number) => void;
  /** Toggle mute callback */
  onToggleMute?: () => void;
  /** Is playing */
  isPlaying?: boolean;
  /** Toggle play/pause callback */
  onTogglePlay?: () => void;
  /** Toggle fullscreen callback */
  onToggleFullscreen?: () => void;
  /** Is fullscreen */
  isFullscreen?: boolean;
  /** Is loop enabled */
  isLoopEnabled?: boolean;
  /** Toggle loop callback */
  onToggleLoop?: () => void;
  /** Jump to live edge callback */
  onJumpToLive?: () => void;
}


const PlayerControls: React.FC<PlayerControlsProps> = ({
  currentTime,
  duration,
  isVisible = true,
  _className,
  onSeek,
  mistStreamInfo,
  disabled = false,
  playbackMode = 'auto',
  onModeChange,
  sourceType,
  isContentLive,
  videoElement: propVideoElement,
  qualities: propQualities = [],
  onSelectQuality,
  isMuted: propIsMuted,
  volume: propVolume,
  onVolumeChange,
  onToggleMute,
  isPlaying: propIsPlaying,
  onTogglePlay,
  onToggleFullscreen,
  isFullscreen: propIsFullscreen,
  isLoopEnabled: _propIsLoopEnabled,
  onToggleLoop: _onToggleLoop,
  onJumpToLive,
}) => {
  // Context fallback - prefer props passed from parent over context
  // Context provides UsePlayerControllerReturn which has state.videoElement and controller
  const ctx = usePlayerContextOptional();
  const contextVideo = ctx?.state?.videoElement;
  const player = ctx?.controller;

  // Robust video element detection - prefer prop, then context, then DOM query
  const [video, setVideo] = useState<HTMLVideoElement | null>(null);
  const videoCheckIntervalRef = useRef<ReturnType<typeof setInterval> | null>(null);

  const findVideoElement = useCallback((): HTMLVideoElement | null => {
    if (propVideoElement) return propVideoElement;
    if (contextVideo) return contextVideo;
    if (player?.getVideoElement?.()) return player.getVideoElement();
    const domVideo = document.querySelector('.fw-player-video') as HTMLVideoElement | null
      ?? document.querySelector('[data-player-container="true"] video') as HTMLVideoElement | null
      ?? document.querySelector('.fw-player-container video') as HTMLVideoElement | null;
    return domVideo;
  }, [propVideoElement, contextVideo, player]);

  useEffect(() => {
    const updateVideo = () => {
      const v = findVideoElement();
      if (v && v !== video) {
        setVideo(v);
      }
    };
    updateVideo();
    if (!video) {
      videoCheckIntervalRef.current = setInterval(() => {
        const v = findVideoElement();
        if (v) {
          setVideo(v);
          if (videoCheckIntervalRef.current) {
            clearInterval(videoCheckIntervalRef.current);
            videoCheckIntervalRef.current = null;
          }
        }
      }, 100);
      setTimeout(() => {
        if (videoCheckIntervalRef.current) {
          clearInterval(videoCheckIntervalRef.current);
          videoCheckIntervalRef.current = null;
        }
      }, 5000);
    }
    return () => {
      if (videoCheckIntervalRef.current) {
        clearInterval(videoCheckIntervalRef.current);
        videoCheckIntervalRef.current = null;
      }
    };
  }, [contextVideo, player, findVideoElement, video]);

  const mistTracks = mistStreamInfo?.meta?.tracks;

  // Quality selection priority:
  // 1. Player-provided qualities (HLS.js/DASH.js levels with correct numeric indices)
  // 2. Mist track metadata (for players that don't provide quality API)
  // This fixes a critical bug where Mist track IDs (e.g., "a1", "v0") were passed to
  // HLS/DASH players which expect numeric indices (e.g., "0", "1", "2")
  // Quality levels - prefer props, then player API, then Mist tracks
  const qualities = useMemo(() => {
    // Priority 1: Props from parent (usePlayerController hook)
    if (propQualities && propQualities.length > 0) {
      return propQualities;
    }

    // Priority 2: Player's quality API
    const playerQualities = player?.getQualities?.();
    if (playerQualities && playerQualities.length > 0) {
      return playerQualities;
    }

    // Fallback to Mist track metadata for players without quality API
    if (mistTracks) {
      return Object.entries(mistTracks)
        .filter(([, t]) => t.type === 'video')
        .map(([id, t]) => ({
          id,
          label: t.height ? `${t.height}p` : t.codec,
          width: t.width,
          height: t.height,
          bitrate: t.bps,
        }))
        .sort((a, b) => (b.height || 0) - (a.height || 0));
    }
    return [];
  }, [propQualities, player, mistTracks]);

  const textTracks = player?.getTextTracks?.() ?? [];

  // Internal state - used as fallback when props not provided
  const [internalIsPlaying, setInternalIsPlaying] = useState(false);
  const [internalIsMuted, setInternalIsMuted] = useState(false);
  const [internalIsFullscreen, setInternalIsFullscreen] = useState(false);
  const [hasAudio, setHasAudio] = useState(true);
  const [buffered, setBuffered] = useState<TimeRanges | undefined>(undefined);
  const [internalVolume, setInternalVolume] = useState<number>(() => {
    if (!video) return 100;
    return Math.round(video.volume * 100);
  });
  const [playbackRate, setPlaybackRate] = useState<number>(() => video?.playbackRate ?? 1);

  // Derived state - prefer props over internal state
  const isPlaying = propIsPlaying ?? internalIsPlaying;
  const isMuted = propIsMuted ?? internalIsMuted;
  const isFullscreen = propIsFullscreen ?? internalIsFullscreen;
  const volumeValue = propVolume !== undefined ? Math.round(propVolume * 100) : internalVolume;
  const [qualityValue, setQualityValue] = useState<string>("auto");
  const [captionValue, setCaptionValue] = useState<string>("none");
  const [isSettingsOpen, setIsSettingsOpen] = useState(false);
  // Hysteresis state for Live badge - prevents flip-flopping
  const [isNearLiveState, setIsNearLiveState] = useState(true);

  // Close settings menu when clicking outside
  useEffect(() => {
    if (!isSettingsOpen) return;

    const handleWindowClick = (event: MouseEvent) => {
      const target = event.target as HTMLElement;
      if (target && !target.closest('.fw-settings-menu')) {
        setIsSettingsOpen(false);
      }
    };

    // Use setTimeout to avoid immediate close from the same click that opened it
    const timeoutId = setTimeout(() => {
      window.addEventListener('click', handleWindowClick);
    }, 0);

    return () => {
      clearTimeout(timeoutId);
      window.removeEventListener('click', handleWindowClick);
    };
  }, [isSettingsOpen]);

  // Core utility-based calculations
  const deriveBufferWindowMs = useCallback((tracks?: Record<string, { firstms?: number; lastms?: number }>) => {
    if (!tracks) return undefined;
    const list = Object.values(tracks);
    if (list.length === 0) return undefined;
    const firstmsValues = list.map(t => t.firstms).filter((v): v is number => v !== undefined);
    const lastmsValues = list.map(t => t.lastms).filter((v): v is number => v !== undefined);
    if (firstmsValues.length === 0 || lastmsValues.length === 0) return undefined;
    const firstms = Math.max(...firstmsValues);
    const lastms = Math.min(...lastmsValues);
    const window = lastms - firstms;
    if (!Number.isFinite(window) || window <= 0) return undefined;
    return window;
  }, []);

  const bufferWindowMs = mistStreamInfo?.meta?.buffer_window
    ?? deriveBufferWindowMs(mistStreamInfo?.meta?.tracks as Record<string, { firstms?: number; lastms?: number }> | undefined);

  const isLive = useMemo(() => isLiveContent(isContentLive, mistStreamInfo, duration),
    [isContentLive, mistStreamInfo, duration]);

  const isWebRTC = useMemo(() => isMediaStreamSource(video), [video]);

  const supportsPlaybackRate = useMemo(() => coreSupportsPlaybackRate(video), [video]);

  // Seekable range using core calculation (allow controller override)
  const allowMediaStreamDvr = isMediaStreamSource(video) &&
    (bufferWindowMs !== undefined && bufferWindowMs > 0) &&
    (sourceType !== 'whep' && sourceType !== 'webrtc');
  const { seekableStart: calcSeekableStart, liveEdge: calcLiveEdge } = useMemo(() => calculateSeekableRange({
    isLive,
    video,
    mistStreamInfo,
    currentTime,
    duration,
    allowMediaStreamDvr,
  }), [isLive, video, mistStreamInfo, currentTime, duration, allowMediaStreamDvr]);
  const controllerSeekableStart = player?.getSeekableStart?.();
  const controllerLiveEdge = player?.getLiveEdge?.();
  const useControllerRange = Number.isFinite(controllerSeekableStart) &&
    Number.isFinite(controllerLiveEdge) &&
    (controllerLiveEdge as number) >= (controllerSeekableStart as number) &&
    ((controllerLiveEdge as number) > 0 || (controllerSeekableStart as number) > 0);
  const seekableStart = useControllerRange ? (controllerSeekableStart as number) : calcSeekableStart;
  const liveEdge = useControllerRange ? (controllerLiveEdge as number) : calcLiveEdge;

  const hasDvrWindow = isLive && Number.isFinite(liveEdge) && Number.isFinite(seekableStart) && liveEdge > seekableStart;
  const commitOnRelease = isLive;

  // Live thresholds with buffer window scaling
  const liveThresholds = useMemo(() =>
    calculateLiveThresholds(sourceType, isWebRTC, bufferWindowMs),
    [sourceType, isWebRTC, bufferWindowMs]);

  // Can seek - prefer PlayerController's computed value (includes player-specific canSeek)
  // Fall back to utility function when controller not available
  const baseCanSeek = useMemo(() => {
    // PlayerController already computes canSeek with player-specific logic
    if (player && typeof (player as any).canSeekStream === 'function') {
      return (player as any).canSeekStream();
    }
    // Fallback when no controller
    return canSeekStream({
      video,
      isLive,
      duration,
      bufferWindowMs,
    });
  }, [video, isLive, duration, bufferWindowMs, player]);
  const canSeek = baseCanSeek && (!isLive || hasDvrWindow);

  // Hysteresis for live badge - using core calculation
  useEffect(() => {
    if (!isLive) {
      setIsNearLiveState(true);
      return;
    }
    const newState = calculateIsNearLive(currentTime, liveEdge, liveThresholds, isNearLiveState);
    if (newState !== isNearLiveState) {
      setIsNearLiveState(newState);
    }
  }, [isLive, liveEdge, currentTime, liveThresholds, isNearLiveState]);

  // Track if we've already seeked to live on initial playback
  const hasSeekToLiveRef = useRef(false);

  // Sync internal state from video element (only when props not provided)
  useEffect(() => {
    if (!video) return;
    const updatePlayingState = () => setInternalIsPlaying(!video.paused);
    const updateMutedState = () => {
      const muted = video.muted || video.volume === 0;
      setInternalIsMuted(muted);
      setInternalVolume(Math.round(video.volume * 100));
    };
    const updateFullscreenState = () => {
      if (typeof document !== "undefined") setInternalIsFullscreen(!!document.fullscreenElement);
    };
    const updatePlaybackRate = () => setPlaybackRate(video.playbackRate);

    updatePlayingState();
    updateMutedState();
    updateFullscreenState();
    updatePlaybackRate();

    video.addEventListener("play", updatePlayingState);
    video.addEventListener("pause", updatePlayingState);
    video.addEventListener("playing", updatePlayingState);
    video.addEventListener("volumechange", updateMutedState);
    video.addEventListener("ratechange", updatePlaybackRate);
    if (typeof document !== "undefined") document.addEventListener("fullscreenchange", updateFullscreenState);

    return () => {
      video.removeEventListener("play", updatePlayingState);
      video.removeEventListener("pause", updatePlayingState);
      video.removeEventListener("playing", updatePlayingState);
      video.removeEventListener("volumechange", updateMutedState);
      video.removeEventListener("ratechange", updatePlaybackRate);
      if (typeof document !== "undefined") document.removeEventListener("fullscreenchange", updateFullscreenState);
    };
  }, [video, isLive]);

  // Reset the seek-to-live flag when video element changes (new stream)
  useEffect(() => {
    hasSeekToLiveRef.current = false;
  }, [video]);

  useEffect(() => {
    const activeTrack = textTracks.find((track) => track.active);
    setCaptionValue(activeTrack ? activeTrack.id : "none");
  }, [textTracks]);

  // Track buffered ranges for SeekBar
  useEffect(() => {
    if (!video) return;
    const updateBuffered = () => {
      const next = player?.getBufferedRanges?.() ?? video.buffered;
      setBuffered(next);
    };
    updateBuffered();
    video.addEventListener("progress", updateBuffered);
    video.addEventListener("loadeddata", updateBuffered);
    return () => {
      video.removeEventListener("progress", updateBuffered);
      video.removeEventListener("loadeddata", updateBuffered);
    };
  }, [video]);

  useEffect(() => {
    if (!video) { setHasAudio(true); return; }
    const checkAudio = () => {
      if (video.srcObject instanceof MediaStream) {
        const audioTracks = video.srcObject.getAudioTracks();
        setHasAudio(audioTracks.length > 0);
        return;
      }
      const videoAny = video as any;
      if (videoAny.audioTracks && videoAny.audioTracks.length !== undefined) {
        setHasAudio(videoAny.audioTracks.length > 0);
        return;
      }
      setHasAudio(true);
    };
    checkAudio();
    video.addEventListener("loadedmetadata", checkAudio);
    return () => video.removeEventListener("loadedmetadata", checkAudio);
  }, [video]);

  const handlePlayPause = () => {
    if (disabled) return;
    // Prefer prop callback from usePlayerController
    if (onTogglePlay) {
      onTogglePlay();
      return;
    }
    // Fallback: direct video/player manipulation
    if (!video && !player) return;
    const isPaused = player?.isPaused?.() ?? video?.paused ?? true;
    if (isPaused) {
      if (player?.play) player.play().catch(() => {});
      else if (video) video.play().catch(() => {});
    } else {
      if (player?.pause) player.pause();
      else if (video) video.pause();
    }
  };

  const handleSkipBack = () => {
    const newTime = Math.max(0, currentTime - 10);
    if (onSeek) {
      onSeek(newTime);
      return;
    }
    const v = findVideoElement();
    if (v) v.currentTime = newTime;
  };

  const handleSkipForward = () => {
    const maxTime = Number.isFinite(duration) ? duration : currentTime + 10;
    const newTime = Math.min(maxTime, currentTime + 10);
    if (onSeek) {
      onSeek(newTime);
      return;
    }
    const v = findVideoElement();
    if (v) v.currentTime = newTime;
  };

  const handleMute = () => {
    if (disabled) return;
    // Prefer prop callback from usePlayerController
    if (onToggleMute) {
      onToggleMute();
      return;
    }
    // Fallback: direct video/player manipulation
    const v = video ?? document.querySelector('.fw-player-video') as HTMLVideoElement | null;
    if (!v) return;
    const nextMuted = !(player?.isMuted?.() ?? v.muted);
    player?.setMuted?.(nextMuted);
    v.muted = nextMuted;
    setInternalIsMuted(nextMuted);
    if (nextMuted) setInternalVolume(0);
    else setInternalVolume(Math.round(v.volume * 100));
  };

  const handleVolumeChange = (value: number[]) => {
    if (disabled) return;
    const next = Math.max(0, Math.min(100, value[0] ?? 0));
    // Prefer prop callback from usePlayerController
    if (onVolumeChange) {
      onVolumeChange(next / 100);
      return;
    }
    // Fallback: direct video manipulation
    const v = video ?? document.querySelector('.fw-player-video') as HTMLVideoElement | null;
    if (!v) return;
    v.volume = next / 100;
    v.muted = next === 0;
    setInternalVolume(next);
    setInternalIsMuted(next === 0);
  };

  const handleFullscreen = () => {
    if (disabled) return;
    // Prefer prop callback from usePlayerController
    if (onToggleFullscreen) {
      onToggleFullscreen();
      return;
    }
    // Fallback: direct DOM manipulation
    if (typeof document === "undefined") return;
    const container = document.querySelector('[data-player-container="true"]') as HTMLElement | null;
    if (!container) return;
    if (document.fullscreenElement) document.exitFullscreen().catch(() => {});
    else container.requestFullscreen().catch(() => {});
  };

  const handleGoLive = () => {
    if (disabled) return;
    if (onJumpToLive) {
      onJumpToLive();
      return;
    }
    player?.jumpToLive?.();
  };

  const handleSpeedChange = (value: string) => {
    if (disabled) return;
    const rate = Number(value);
    setPlaybackRate(rate);
    // Use player API if available, fall back to direct video element
    if (player?.setPlaybackRate) {
      player.setPlaybackRate(rate);
    } else {
      const v = findVideoElement();
      if (v) v.playbackRate = rate;
    }
  };

  const handleQualityChange = (value: string) => {
    if (disabled) return;
    setQualityValue(value);
    // Prefer prop callback from usePlayerController
    if (onSelectQuality) {
      onSelectQuality(value);
      return;
    }
    // Fallback: direct player manipulation
    player?.selectQuality?.(value);
  };

  const handleCaptionChange = (value: string) => {
    if (disabled) return;
    setCaptionValue(value);
    if (value === "none") player?.selectTextTrack?.(null);
    else player?.selectTextTrack?.(value);
  };

  // Time display - using core formatTimeDisplay
  const timeDisplay = useMemo(() => formatTimeDisplay({
    isLive,
    currentTime,
    duration,
    liveEdge,
    seekableStart,
    unixoffset: mistStreamInfo?.unixoffset,
  }), [isLive, currentTime, duration, liveEdge, seekableStart, mistStreamInfo?.unixoffset]);

  const [isVolumeHovered, setIsVolumeHovered] = useState(false);
  const [isVolumeFocused, setIsVolumeFocused] = useState(false);
  const isVolumeExpanded = isVolumeHovered || isVolumeFocused;

  return (
    <div className={cn(
      "fw-player-surface fw-controls-wrapper",
      isVisible ? "fw-controls-wrapper--visible" : "fw-controls-wrapper--hidden"
    )}>
      {/* Bottom Row: Controls with SeekBar on top */}
      <div
        className="fw-control-bar pointer-events-auto"
        onClick={(e) => e.stopPropagation()}
      >
        {/* SeekBar - sits directly on top of control buttons */}
        {canSeek && (
          <div className="fw-seek-wrapper">
            <SeekBar
              currentTime={currentTime}
              duration={duration}
              buffered={buffered}
              disabled={disabled}
              isLive={isLive}
              seekableStart={seekableStart}
              liveEdge={liveEdge}
              commitOnRelease={commitOnRelease}
              onSeek={(time) => {
                if (onSeek) {
                  onSeek(time);
                } else if (video) {
                  video.currentTime = time;
                }
              }}
            />
          </div>
        )}

        {/* Control buttons row */}
        <div className="fw-controls-row">
        {/* Left: Controls & Time */}
        <div className="fw-controls-left">
          <div className="fw-control-group">
            <button type="button" className="fw-btn-flush" aria-label={isPlaying ? "Pause" : "Play"} onClick={handlePlayPause}>
              <PlayPauseIcon isPlaying={isPlaying} size={18} />
            </button>
            {canSeek && (
              <>
                <button type="button" className="fw-btn-flush hidden sm:flex" aria-label="Skip back 10 seconds" onClick={handleSkipBack}>
                  <SkipBackIcon size={16} />
                </button>
                <button type="button" className="fw-btn-flush hidden sm:flex" aria-label="Skip forward 10 seconds" onClick={handleSkipForward}>
                  <SkipForwardIcon size={16} />
                </button>
              </>
            )}
          </div>

          {/* Volume pill - cohesive hover element (slab style) */}
          <div
            className={cn(
              "fw-volume-group",
              isVolumeExpanded && "fw-volume-group--expanded",
              !hasAudio && "fw-volume-group--disabled"
            )}
            onMouseEnter={() => hasAudio && setIsVolumeHovered(true)}
            onMouseLeave={() => {
              setIsVolumeHovered(false);
              setIsVolumeFocused(false);
            }}
            onFocusCapture={() => hasAudio && setIsVolumeFocused(true)}
            onBlurCapture={(e) => {
              if (!e.currentTarget.contains(e.relatedTarget as Node)) setIsVolumeFocused(false);
            }}
            onClick={(e) => {
              // Click on the pill (not slider) toggles mute
              if (hasAudio && e.target === e.currentTarget) {
                handleMute();
              }
            }}
          >
            {/* Volume icon - part of the pill */}
            <button
              type="button"
              className="fw-volume-btn"
              aria-label={!hasAudio ? "No audio" : (isMuted ? "Unmute" : "Mute")}
              onClick={hasAudio ? handleMute : undefined}
              disabled={!hasAudio}
            >
              <VolumeIcon isMuted={isMuted || !hasAudio} size={16} />
            </button>
            {/* Slider - expands within the pill */}
            <div className={cn(
              "fw-volume-slider-wrapper",
              isVolumeExpanded ? "fw-volume-slider-wrapper--expanded" : "fw-volume-slider-wrapper--collapsed"
            )}>
              <Slider
                orientation="horizontal"
                aria-label="Volume"
                max={100}
                step={1}
                value={[volumeValue]}
                onValueChange={handleVolumeChange}
                className="w-full"
                disabled={!hasAudio}
              />
            </div>
          </div>

          <div className="fw-control-group">
            <span className="fw-time-display">
              {timeDisplay}
            </span>
          </div>

          {isLive && (
            <div className="fw-control-group">
              <button
                type="button"
                onClick={handleGoLive}
                disabled={!hasDvrWindow || isNearLiveState}
                className={cn(
                  "fw-live-badge",
                  (!hasDvrWindow || isNearLiveState) ? "fw-live-badge--active" : "fw-live-badge--behind"
                )}
                title={!hasDvrWindow ? "Live only" : (isNearLiveState ? "At live edge" : "Jump to live")}
              >
                LIVE
                {!isNearLiveState && hasDvrWindow && <SeekToLiveIcon size={10} />}
              </button>
            </div>
          )}
        </div>

        {/* Right Group: Settings, Fullscreen */}
        <div className="fw-controls-right">
          <div className="fw-control-group relative">
            <button
              type="button"
              className={cn("fw-btn-flush group", isSettingsOpen && "fw-btn-flush--active")}
              aria-label="Settings"
              title="Settings"
              onClick={() => setIsSettingsOpen(!isSettingsOpen)}
            >
              <SettingsIcon size={16} className="transition-transform group-hover:rotate-90" />
            </button>

            {/* Settings Popup */}
            {isSettingsOpen && (
              <div className="fw-player-surface fw-settings-menu">
                {/* Playback Mode - only show for live content (not VOD/clips) */}
                {onModeChange && isContentLive !== false && (
                  <div className="fw-settings-section">
                    <div className="fw-settings-label">Mode</div>
                    <div className="fw-settings-options">
                      {(['auto', 'low-latency', 'quality'] as const).map((mode) => (
                        <button
                          key={mode}
                          className={cn(
                            "fw-settings-btn",
                            playbackMode === mode && "fw-settings-btn--active"
                          )}
                          onClick={() => { onModeChange(mode); setIsSettingsOpen(false); }}
                        >
                          {mode === 'low-latency' ? 'Fast' : mode === 'quality' ? 'Stable' : 'Auto'}
                        </button>
                      ))}
                    </div>
                  </div>
                )}
                {supportsPlaybackRate && (
                  <div className="fw-settings-section">
                    <div className="fw-settings-label">Speed</div>
                    <div className="fw-settings-options fw-settings-options--wrap">
                      {SPEED_PRESETS.map((rate) => (
                        <button
                          key={rate}
                          className={cn(
                            "fw-settings-btn",
                            playbackRate === rate && "fw-settings-btn--active"
                          )}
                          onClick={() => { handleSpeedChange(String(rate)); setIsSettingsOpen(false); }}
                        >
                          {rate}x
                        </button>
                      ))}
                    </div>
                  </div>
                )}
                {qualities.length > 0 && (
                  <div className="fw-settings-section">
                    <div className="fw-settings-label">Quality</div>
                    <div className="fw-settings-list">
                      <button
                        className={cn(
                          "fw-settings-list-item",
                          qualityValue === 'auto' && "fw-settings-list-item--active"
                        )}
                        onClick={() => { handleQualityChange('auto'); setIsSettingsOpen(false); }}
                      >
                        Auto
                      </button>
                      {qualities.map((q) => (
                        <button
                          key={q.id}
                          className={cn(
                            "fw-settings-list-item",
                            qualityValue === q.id && "fw-settings-list-item--active"
                          )}
                          onClick={() => { handleQualityChange(q.id); setIsSettingsOpen(false); }}
                        >
                          {q.label}
                        </button>
                      ))}
                    </div>
                  </div>
                )}
                {textTracks.length > 0 && (
                  <div className="fw-settings-section">
                    <div className="fw-settings-label">Captions</div>
                    <div className="fw-settings-list">
                      <button
                        className={cn(
                          "fw-settings-list-item",
                          captionValue === 'none' && "fw-settings-list-item--active"
                        )}
                        onClick={() => { handleCaptionChange('none'); setIsSettingsOpen(false); }}
                      >
                        Off
                      </button>
                      {textTracks.map((t) => (
                        <button
                          key={t.id}
                          className={cn(
                            "fw-settings-list-item",
                            captionValue === t.id && "fw-settings-list-item--active"
                          )}
                          onClick={() => { handleCaptionChange(t.id); setIsSettingsOpen(false); }}
                        >
                          {t.label || t.id}
                        </button>
                      ))}
                    </div>
                  </div>
                )}
              </div>
            )}
          </div>

          <div className="fw-control-group">
            <button type="button" className="fw-btn-flush" aria-label="Toggle fullscreen" onClick={handleFullscreen}>
              <FullscreenToggleIcon isFullscreen={isFullscreen} size={16} />
            </button>
          </div>
        </div>
        </div>
      </div>
    </div>
  );
};

export default PlayerControls;
