import React, { useState, useEffect } from 'react';
import { globalPlayerManager } from '../core';
import { 
  PlayPauseIcon, 
  SkipBackIcon, 
  SkipForwardIcon, 
  VolumeIcon, 
  FullscreenToggleIcon, 
  PictureInPictureIcon, 
  ClosedCaptionsIcon, 
  LiveIcon 
} from './Icons';

interface PlayerControlsProps {
  currentTime: number;
  duration: number;
  isVisible?: boolean;
  className?: string;
  onSeek?: (time: number) => void;
}

const PlayerControls: React.FC<PlayerControlsProps> = ({ 
  currentTime, 
  duration, 
  isVisible = true,
  className = '',
  onSeek 
}) => {
  if (!isVisible) return null;

  const formatTime = (seconds: number): string => {
    const t = Math.floor(seconds);
    const m = String(Math.floor(t / 60)).padStart(2, '0');
    const s = String(t % 60).padStart(2, '0');
    return `${m}:${s}`;
  };

  const handlePlayPause = () => {
    const player = globalPlayerManager.getCurrentPlayer();
    const video = player?.getVideoElement();
    if (!video) return;
    
    if (video.paused) {
      video.play().catch(() => {});
    } else {
      video.pause();
    }
  };

  const handleSeek = (event: React.ChangeEvent<HTMLInputElement>) => {
    const player = globalPlayerManager.getCurrentPlayer();
    const video = player?.getVideoElement();
    if (!video || !isFinite(video.duration)) return;
    
    const pct = Number(event.target.value) / 1000;
    const newTime = video.duration * pct;
    video.currentTime = newTime;
    onSeek?.(newTime);
  };

  const handleMute = () => {
    const player = globalPlayerManager.getCurrentPlayer();
    if (!player) return;
    player.setMuted?.(!player.isMuted?.());
  };

  const handleVolumeChange = (event: React.ChangeEvent<HTMLInputElement>) => {
    const player = globalPlayerManager.getCurrentPlayer();
    const video = player?.getVideoElement();
    if (!video) return;
    video.volume = Math.max(0, Math.min(1, Number(event.target.value) / 100));
  };

  const handleSkipBack = () => {
    const video = globalPlayerManager.getCurrentPlayer()?.getVideoElement();
    if (!video) return;
    video.currentTime = Math.max(0, video.currentTime - 10);
  };

  const handleSkipForward = () => {
    const video = globalPlayerManager.getCurrentPlayer()?.getVideoElement();
    if (!video) return;
    const maxTime = isFinite(video.duration) ? video.duration : video.currentTime + 10;
    video.currentTime = Math.min(maxTime, video.currentTime + 10);
  };

  const handleFullscreen = () => {
    const container = document.querySelector('[data-player-container="true"]') as HTMLElement;
    if (!container) return;
    
    if (document.fullscreenElement) {
      document.exitFullscreen().catch(() => {});
    } else {
      container.requestFullscreen().catch(() => {});
    }
  };

  const handlePictureInPicture = () => {
    const player = globalPlayerManager.getCurrentPlayer();
    player?.requestPiP?.();
  };

  const handleGoLive = () => {
    const player = globalPlayerManager.getCurrentPlayer();
    player?.jumpToLive?.();
  };

  const handleSpeedChange = (event: React.ChangeEvent<HTMLSelectElement>) => {
    const player = globalPlayerManager.getCurrentPlayer();
    const rate = Number(event.target.value);
    player?.setPlaybackRate?.(rate);
  };

  const handleQualityChange = (event: React.ChangeEvent<HTMLSelectElement>) => {
    const player = globalPlayerManager.getCurrentPlayer();
    player?.selectQuality?.(event.target.value);
  };

  const handleCaptionToggle = () => {
    const player = globalPlayerManager.getCurrentPlayer();
    const tracks = player?.getTextTracks?.();
    if (!player || !tracks?.length) return;
    
    const active = tracks.find(t => t.active);
    player.selectTextTrack?.(active ? null : tracks[0].id);
  };

  const handleCaptionChange = (event: React.ChangeEvent<HTMLSelectElement>) => {
    const player = globalPlayerManager.getCurrentPlayer();
    const val = event.target.value;
    if (val === 'none') {
      player?.selectTextTrack?.(null);
    } else {
      player?.selectTextTrack?.(val);
    }
  };

  // Track state for dynamic icons
  const [isPlaying, setIsPlaying] = useState(false);
  const [isMuted, setIsMuted] = useState(false);
  const [isFullscreen, setIsFullscreen] = useState(false);

  // Get current player state
  const player = globalPlayerManager.getCurrentPlayer();
  const video = player?.getVideoElement();
  const qualities = player?.getQualities?.() || [];
  const textTracks = player?.getTextTracks?.() || [];
  const isLive = player?.isLive?.() || (video ? !isFinite(video.duration) : false);
  const isNearLive = video && isFinite(video.duration) ? (video.duration - video.currentTime) < 2 : false;

  // Update state based on video element
  useEffect(() => {
    if (!video) return;
    
    const updatePlayingState = () => setIsPlaying(!video.paused);
    const updateMutedState = () => setIsMuted(video.muted || video.volume === 0);
    const updateFullscreenState = () => setIsFullscreen(!!document.fullscreenElement);
    
    // Initial state
    updatePlayingState();
    updateMutedState();
    updateFullscreenState();
    
    // Event listeners
    video.addEventListener('play', updatePlayingState);
    video.addEventListener('pause', updatePlayingState);
    video.addEventListener('volumechange', updateMutedState);
    document.addEventListener('fullscreenchange', updateFullscreenState);
    
    return () => {
      video.removeEventListener('play', updatePlayingState);
      video.removeEventListener('pause', updatePlayingState);
      video.removeEventListener('volumechange', updateMutedState);
      document.removeEventListener('fullscreenchange', updateFullscreenState);
    };
  }, [video]);

  // Calculate seek bar value
  const seekValue = (() => {
    if (!isFinite(duration) || duration <= 0) return 0;
    const pct = currentTime / duration;
    return Math.max(0, Math.min(1000, Math.round(pct * 1000)));
  })();

  const seekTitle = (() => {
    if (!isFinite(duration)) return 'Live';
    const currentFormatted = formatTime(currentTime);
    const durationFormatted = formatTime(duration);
    return `${currentFormatted} / ${durationFormatted}`;
  })();

  // Responsive breakpoints
  const isMobile = window.innerWidth < 768;
  const isTablet = window.innerWidth >= 768 && window.innerWidth < 1024;

  const controlsStyles = {
    container: {
      position: 'absolute' as const,
      left: 0,
      right: 0,
      bottom: 0,
      minHeight: isMobile ? '48px' : '56px',
      display: 'flex',
      alignItems: 'center',
      gap: isMobile ? '8px' : '12px',
      padding: isMobile ? '6px 10px' : '8px 14px',
      background: 'var(--fw-controls-bg, linear-gradient(180deg, rgba(0,0,0,0) 0%, rgba(0,0,0,0.55) 100%))',
      color: 'var(--fw-controls-fg, #fff)',
      flexWrap: 'wrap' as const,
      fontSize: isMobile ? '12px' : '14px',
      userSelect: 'none' as const
    },
    button: {
      color: 'inherit',
      background: 'transparent',
      border: 0,
      cursor: 'pointer',
      padding: isMobile ? '2px' : '4px',
      borderRadius: '4px',
      transition: 'background-color 0.2s ease',
      ':hover': {
        backgroundColor: 'rgba(255,255,255,0.1)'
      }
    },
    select: {
      color: 'inherit',
      background: 'rgba(0,0,0,0.5)',
      border: '1px solid rgba(255,255,255,0.3)',
      borderRadius: '4px',
      padding: '2px 4px',
      fontSize: isMobile ? '11px' : '12px'
    },
    slider: {
      height: '4px',
      background: 'rgba(255,255,255,0.3)',
      outline: 'none',
      cursor: 'pointer',
      borderRadius: '2px'
    }
  };

  return (
    <div 
      className={`player-controls ${className}`}
      style={controlsStyles.container}
    >
      {/* Primary Controls Group */}
      <div className="controls-group controls-primary" style={{ display: 'flex', alignItems: 'center', gap: '8px' }}>
        <button
          className="control-button play-pause"
          aria-label="Play/Pause"
          title="Play/Pause (Space)"
          onClick={handlePlayPause}
          style={{
            color: 'inherit',
            background: 'transparent',
            border: 0,
            cursor: 'pointer',
            fontSize: '16px',
            padding: '4px',
            borderRadius: '4px'
          }}
        >
          <PlayPauseIcon isPlaying={isPlaying} size={18} />
        </button>

        <button
          className="control-button skip-back"
          aria-label="Skip Back 10s"
          title="Back 10s (←)"
          onClick={handleSkipBack}
          style={{
            color: 'inherit',
            background: 'transparent',
            border: 0,
            cursor: 'pointer',
            fontSize: '12px',
            padding: '4px 6px',
            borderRadius: '4px'
          }}
        >
          <SkipBackIcon size={18} />
        </button>

        <button
          className="control-button skip-forward"
          aria-label="Skip Forward 10s"
          title="Forward 10s (→)"
          onClick={handleSkipForward}
          style={{
            color: 'inherit',
            background: 'transparent',
            border: 0,
            cursor: 'pointer',
            fontSize: '12px',
            padding: '4px 6px',
            borderRadius: '4px'
          }}
        >
          <SkipForwardIcon size={18} />
        </button>
      </div>

      {/* Time Display */}
      <span 
        className="time-display"
        style={{ 
          color: 'inherit', 
          fontVariantNumeric: 'tabular-nums', 
          fontSize: '12px',
          minWidth: '40px'
        }}
      >
        {formatTime(currentTime)}
      </span>

      {/* Seek Bar */}
      <input
        className="seek-bar"
        aria-label="Seek"
        title={seekTitle}
        type="range"
        min={0}
        max={1000}
        value={seekValue}
        onChange={handleSeek}
        style={{
          flex: 1,
          minWidth: '160px',
          height: '4px',
          background: 'rgba(255,255,255,0.3)',
          outline: 'none',
          cursor: 'pointer'
        }}
      />

      {/* Audio Controls Group */}
      <div className="controls-group controls-audio" style={{ display: 'flex', alignItems: 'center', gap: '8px' }}>
        <button
          className="control-button mute"
          aria-label="Mute"
          title="Mute (M)"
          onClick={handleMute}
          style={{
            color: 'inherit',
            background: 'transparent',
            border: 0,
            cursor: 'pointer',
            fontSize: '14px',
            padding: '4px',
            borderRadius: '4px'
          }}
        >
          <VolumeIcon isMuted={isMuted} size={16} />
        </button>

        <input
          className="volume-slider"
          aria-label="Volume"
          title="Volume (↑/↓)"
          type="range"
          min={0}
          max={100}
          defaultValue={100}
          onChange={handleVolumeChange}
          style={{
            width: '60px',
            height: '4px',
            background: 'rgba(255,255,255,0.3)',
            outline: 'none',
            cursor: 'pointer'
          }}
        />
      </div>

      {/* Settings Group */}
      <div className="controls-group controls-settings" style={{ display: 'flex', alignItems: 'center', gap: '8px' }}>
        {/* Caption Controls */}
        {textTracks.length > 0 && (
          <>
            <button
              className="control-button captions"
              aria-label="Toggle Captions"
              title="Toggle captions"
              onClick={handleCaptionToggle}
              style={{
                color: 'inherit',
                background: 'transparent',
                border: 0,
                cursor: 'pointer',
                fontSize: '12px',
                padding: '4px 6px',
                borderRadius: '4px'
              }}
            >
              <ClosedCaptionsIcon size={16} />
            </button>

            <select
              className="caption-track-select"
              aria-label="Caption Track"
              defaultValue="none"
              onChange={handleCaptionChange}
              style={{
                color: 'inherit',
                background: 'rgba(0,0,0,0.5)',
                border: '1px solid rgba(255,255,255,0.3)',
                borderRadius: '4px',
                padding: '2px 4px',
                fontSize: '12px'
              }}
            >
              <option value="none">None</option>
              {textTracks.map(track => (
                <option key={track.id} value={track.id}>
                  {track.label}
                </option>
              ))}
            </select>
          </>
        )}

        {/* Quality Control */}
        {qualities.length > 0 && (
          <select
            className="quality-select"
            aria-label="Quality"
            defaultValue="auto"
            onChange={handleQualityChange}
            style={{
              color: 'inherit',
              background: 'rgba(0,0,0,0.5)',
              border: '1px solid rgba(255,255,255,0.3)',
              borderRadius: '4px',
              padding: '2px 4px',
              fontSize: '12px'
            }}
          >
            {qualities.map(quality => (
              <option key={quality.id} value={quality.id}>
                {quality.label}
              </option>
            ))}
          </select>
        )}

        {/* Speed Control */}
        <select
          className="speed-select"
          aria-label="Speed"
          defaultValue="1"
          onChange={handleSpeedChange}
          style={{
            color: 'inherit',
            background: 'rgba(0,0,0,0.5)',
            border: '1px solid rgba(255,255,255,0.3)',
            borderRadius: '4px',
            padding: '2px 4px',
            fontSize: '12px'
          }}
        >
          <option value="0.5">0.5x</option>
          <option value="1">1x</option>
          <option value="1.5">1.5x</option>
          <option value="2">2x</option>
        </select>
      </div>

      {/* Live/System Controls Group */}
      <div className="controls-group controls-system" style={{ display: 'flex', alignItems: 'center', gap: '8px' }}>
        {/* Live Button */}
        {isLive && (
          <button
            className="control-button go-live"
            aria-label="Go Live"
            title="Jump to live"
            onClick={handleGoLive}
            style={{
              color: 'inherit',
              background: 'transparent',
              border: 0,
              cursor: 'pointer',
              fontSize: '12px',
              padding: '4px 6px',
              borderRadius: '4px',
              fontWeight: isNearLive ? 'bold' : 'normal'
            }}
          >
            <div style={{ display: 'flex', alignItems: 'center', gap: '4px' }}>
              <LiveIcon size={12} color={isNearLive ? '#ff4444' : 'currentColor'} />
              LIVE
            </div>
          </button>
        )}

        {/* Fullscreen */}
        <button
          className="control-button fullscreen"
          aria-label="Fullscreen"
          title="Fullscreen (F)"
          onClick={handleFullscreen}
          style={{
            color: 'inherit',
            background: 'transparent',
            border: 0,
            cursor: 'pointer',
            fontSize: '14px',
            padding: '4px',
            borderRadius: '4px'
          }}
        >
          <FullscreenToggleIcon isFullscreen={isFullscreen} size={16} />
        </button>

        {/* Picture in Picture */}
        <button
          className="control-button pip"
          aria-label="Picture in Picture"
          title="Picture in Picture"
          onClick={handlePictureInPicture}
          style={{
            color: 'inherit',
            background: 'transparent',
            border: 0,
            cursor: 'pointer',
            fontSize: '14px',
            padding: '4px',
            borderRadius: '4px'
          }}
        >
          <PictureInPictureIcon size={16} />
        </button>
      </div>
    </div>
  );
};

export default PlayerControls;