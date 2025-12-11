import React, { useEffect, useMemo, useState } from "react";
import { usePlayerWithFallback } from "../context/PlayerContext";
import { cn } from "../lib/utils";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "../ui/select";
import { Slider } from "../ui/slider";
import {
  ClosedCaptionsIcon,
  FullscreenToggleIcon,
  LiveIcon,
  PictureInPictureIcon,
  PlayPauseIcon,
  SkipBackIcon,
  SkipForwardIcon,
  VolumeIcon
} from "./Icons";

interface PlayerControlsProps {
  currentTime: number;
  duration: number;
  isVisible?: boolean;
  className?: string;
  onSeek?: (time: number) => void;
}

const SPEED_PRESETS = [0.5, 1, 1.5, 2];

const PlayerControls: React.FC<PlayerControlsProps> = ({
  currentTime,
  duration,
  isVisible = true,
  className,
  onSeek
}) => {
  // Use context with fallback to global for backwards compatibility
  const { player, videoElement: video } = usePlayerWithFallback();

  if (!isVisible) return null;
  const qualities = player?.getQualities?.() ?? [];
  const textTracks = player?.getTextTracks?.() ?? [];

  const [isPlaying, setIsPlaying] = useState(false);
  const [isMuted, setIsMuted] = useState(false);
  const [isFullscreen, setIsFullscreen] = useState(false);
  const [volumeValue, setVolumeValue] = useState<number>(() => {
    if (!video) return 100;
    return Math.round(video.volume * 100);
  });
  const [playbackRate, setPlaybackRate] = useState<number>(() => video?.playbackRate ?? 1);
  const [qualityValue, setQualityValue] = useState<string>("auto");
  const [captionValue, setCaptionValue] = useState<string>("none");

  const formatTime = (seconds: number): string => {
    if (!Number.isFinite(seconds) || seconds < 0) {
      return "LIVE";
    }
    const total = Math.floor(seconds);
    const hours = Math.floor(total / 3600);
    const minutes = Math.floor((total % 3600) / 60);
    const secs = total % 60;
    if (hours > 0) {
      return `${hours}:${String(minutes).padStart(2, "0")}:${String(secs).padStart(2, "0")}`;
    }
    return `${String(minutes).padStart(2, "0")}:${String(secs).padStart(2, "0")}`;
  };

  const isLive = player?.isLive?.() || !Number.isFinite(duration);
  const isNearLive = isLive && video && Number.isFinite(video.duration)
    ? video.duration - video.currentTime < 2
    : false;

  const qualityOptions = useMemo(() => {
    const opts = qualities.map((quality) => ({
      value: quality.id,
      label: quality.label
    }));
    if (!opts.some((opt) => opt.value === "auto")) {
      opts.unshift({ value: "auto", label: "Auto" });
    }
    return opts;
  }, [qualities]);

  const captionOptions = useMemo(() => {
    const base = [{ value: "none", label: "Off" }];
    if (!textTracks.length) return base;
    return base.concat(textTracks.map((track) => ({ value: track.id, label: track.label ?? track.id })));
  }, [textTracks]);

  useEffect(() => {
    if (!video) return;

    const updatePlayingState = () => setIsPlaying(!video.paused);
    const updateMutedState = () => {
      const muted = video.muted || video.volume === 0;
      setIsMuted(muted);
      setVolumeValue(Math.round(video.volume * 100));
    };
    const updateFullscreenState = () => {
      if (typeof document === "undefined") return;
      setIsFullscreen(!!document.fullscreenElement);
    };
    const updatePlaybackRate = () => setPlaybackRate(video.playbackRate);

    updatePlayingState();
    updateMutedState();
    updateFullscreenState();
    updatePlaybackRate();

    video.addEventListener("play", updatePlayingState);
    video.addEventListener("pause", updatePlayingState);
    video.addEventListener("volumechange", updateMutedState);
    video.addEventListener("ratechange", updatePlaybackRate);

    if (typeof document !== "undefined") {
      document.addEventListener("fullscreenchange", updateFullscreenState);
    }

    return () => {
      video.removeEventListener("play", updatePlayingState);
      video.removeEventListener("pause", updatePlayingState);
      video.removeEventListener("volumechange", updateMutedState);
      video.removeEventListener("ratechange", updatePlaybackRate);
      if (typeof document !== "undefined") {
        document.removeEventListener("fullscreenchange", updateFullscreenState);
      }
    };
  }, [video]);

  useEffect(() => {
    const activeTrack = textTracks.find((track) => track.active);
    setCaptionValue(activeTrack ? activeTrack.id : "none");
  }, [textTracks]);

  const handlePlayPause = () => {
    if (!video) return;
    if (video.paused) {
      video.play().catch(() => undefined);
    } else {
      video.pause();
    }
  };

  const handleSkipBack = () => {
    if (!video) return;
    video.currentTime = Math.max(0, video.currentTime - 10);
  };

  const handleSkipForward = () => {
    if (!video) return;
    const maxTime = Number.isFinite(video.duration) ? video.duration : video.currentTime + 10;
    video.currentTime = Math.min(maxTime, video.currentTime + 10);
  };

  const handleSeekChange = (value: number[]) => {
    const pct = value[0] ?? 0;
    if (!video || !Number.isFinite(video.duration)) return;
    const newTime = video.duration * (pct / 1000);
    video.currentTime = newTime;
    onSeek?.(newTime);
  };

  const handleMute = () => {
    if (!player || !video) return;
    const nextMuted = !(player.isMuted?.() ?? video.muted);
    player.setMuted?.(nextMuted);
    video.muted = nextMuted;
    setIsMuted(nextMuted);
    if (nextMuted) {
      setVolumeValue(0);
    } else {
      setVolumeValue(Math.round(video.volume * 100));
    }
  };

  const handleVolumeChange = (value: number[]) => {
    if (!video) return;
    const next = Math.max(0, Math.min(100, value[0] ?? 0));
    const normalized = next / 100;
    video.volume = normalized;
    video.muted = next === 0;
    setVolumeValue(next);
    setIsMuted(next === 0);
  };

  const handleFullscreen = () => {
    if (typeof document === "undefined") return;
    const container = document.querySelector('[data-player-container="true"]') as HTMLElement | null;
    if (!container) return;
    if (document.fullscreenElement) {
      document.exitFullscreen().catch(() => undefined);
    } else {
      container.requestFullscreen().catch(() => undefined);
    }
  };

  const handlePictureInPicture = () => {
    player?.requestPiP?.();
  };

  const handleGoLive = () => {
    player?.jumpToLive?.();
  };

  const handleSpeedChange = (value: string) => {
    const rate = Number(value);
    setPlaybackRate(rate);
    player?.setPlaybackRate?.(rate);
  };

  const handleQualityChange = (value: string) => {
    setQualityValue(value);
    player?.selectQuality?.(value);
  };

  const handleCaptionChange = (value: string) => {
    setCaptionValue(value);
    if (value === "none") {
      player?.selectTextTrack?.(null);
    } else {
      player?.selectTextTrack?.(value);
    }
  };

  const seekValue = useMemo(() => {
    if (!Number.isFinite(duration) || duration <= 0) return 0;
    const pct = currentTime / duration;
    return Math.max(0, Math.min(1000, Math.round(pct * 1000)));
  }, [currentTime, duration]);

  const seekTitle = useMemo(() => {
    if (!Number.isFinite(duration)) return "Live";
    return `${formatTime(currentTime)} / ${formatTime(duration)}`;
  }, [currentTime, duration]);

  const timeDisplay = Number.isFinite(duration) ? `${formatTime(currentTime)} / ${formatTime(duration)}` : formatTime(currentTime);

  return (
    <div className={cn("fw-player-surface pointer-events-none absolute inset-x-0 bottom-0", className)}>
      {/* Slab-based control bar - flush to edges, seams between groups */}
      <div className="fw-control-bar fw-pointer-events-auto w-full">
        {/* Playback controls group */}
        <div className="fw-control-group">
          <button type="button" className="fw-btn-flush" aria-label={isPlaying ? "Pause" : "Play"} onClick={handlePlayPause}>
            <PlayPauseIcon isPlaying={isPlaying} size={18} />
          </button>
          <button type="button" className="fw-btn-flush" aria-label="Skip back 10 seconds" onClick={handleSkipBack}>
            <SkipBackIcon size={16} />
          </button>
          <button type="button" className="fw-btn-flush" aria-label="Skip forward 10 seconds" onClick={handleSkipForward}>
            <SkipForwardIcon size={16} />
          </button>
        </div>

        {/* Seek/timeline group */}
        <div className="fw-control-group flex-1 min-w-[100px]">
          <span className="hidden sm:inline font-mono text-[11px] leading-none text-[hsl(var(--tn-fg-dark))] mr-2 whitespace-nowrap">{timeDisplay}</span>
          <Slider
            aria-label="Seek"
            max={1000}
            step={1}
            value={[seekValue]}
            onValueChange={handleSeekChange}
            className="flex-1"
            title={seekTitle}
          />
        </div>

        {/* Volume group */}
        <div className="fw-control-group hidden sm:flex">
          <button type="button" className="fw-btn-flush" aria-label={isMuted ? "Unmute" : "Mute"} onClick={handleMute}>
            <VolumeIcon isMuted={isMuted} size={16} />
          </button>
          <div className="w-[80px] px-2">
            <Slider
              aria-label="Volume"
              max={100}
              step={1}
              value={[volumeValue]}
              onValueChange={handleVolumeChange}
              className="w-full"
            />
          </div>
        </div>

        {/* Options group - captions, quality, speed */}
        <div className="fw-control-group hidden md:flex">
          {textTracks.length > 0 && (
            <Select value={captionValue} onValueChange={handleCaptionChange}>
              <SelectTrigger className="w-[100px] h-8 rounded-none border-0 bg-transparent text-[hsl(var(--tn-fg))]">
                <div className="flex items-center gap-1.5">
                  <ClosedCaptionsIcon size={14} />
                  <SelectValue />
                </div>
              </SelectTrigger>
              <SelectContent className="rounded-none border-[hsl(var(--tn-fg-gutter)/0.3)]">
                {captionOptions.map((option) => (
                  <SelectItem key={option.value} value={option.value} className="rounded-none">
                    {option.label}
                  </SelectItem>
                ))}
              </SelectContent>
            </Select>
          )}

          {qualityOptions.length > 0 && (
            <Select value={qualityValue} onValueChange={handleQualityChange}>
              <SelectTrigger className="w-[90px] h-8 rounded-none border-0 bg-transparent text-[hsl(var(--tn-fg))]">
                <SelectValue placeholder="Quality" />
              </SelectTrigger>
              <SelectContent className="rounded-none border-[hsl(var(--tn-fg-gutter)/0.3)]">
                {qualityOptions.map((option) => (
                  <SelectItem key={option.value} value={option.value} className="rounded-none">
                    {option.label}
                  </SelectItem>
                ))}
              </SelectContent>
            </Select>
          )}

          <Select value={String(playbackRate)} onValueChange={handleSpeedChange}>
            <SelectTrigger className="w-[70px] h-8 rounded-none border-0 bg-transparent text-[hsl(var(--tn-fg))]">
              <SelectValue placeholder="Speed" />
            </SelectTrigger>
            <SelectContent className="rounded-none border-[hsl(var(--tn-fg-gutter)/0.3)]">
              {SPEED_PRESETS.map((rate) => (
                <SelectItem key={rate} value={String(rate)} className="rounded-none">
                  {rate}x
                </SelectItem>
              ))}
            </SelectContent>
          </Select>
        </div>

        {/* Live indicator + fullscreen group */}
        <div className="fw-control-group">
          {isLive && (
            <button
              type="button"
              className="fw-btn-flush inline-flex items-center gap-1.5 text-xs uppercase tracking-wide"
              onClick={handleGoLive}
            >
              <span className={cn(
                "flex items-center gap-1 px-1.5 py-0.5 text-[10px] font-semibold",
                isNearLive
                  ? "bg-[hsl(var(--tn-red))] text-[hsl(var(--tn-bg-dark))]"
                  : "border border-[hsl(var(--tn-fg-gutter)/0.5)] text-[hsl(var(--tn-fg-dark))]"
              )}>
                <LiveIcon size={8} color={isNearLive ? "currentColor" : "currentColor"} />
                Live
              </span>
              {!isNearLive && <span className="text-[hsl(var(--tn-fg-dark))] text-[10px]">Catch up</span>}
            </button>
          )}
          <button type="button" className="fw-btn-flush" aria-label="Toggle fullscreen" onClick={handleFullscreen}>
            <FullscreenToggleIcon isFullscreen={isFullscreen} size={16} />
          </button>
          <button type="button" className="fw-btn-flush" aria-label="Toggle picture in picture" onClick={handlePictureInPicture}>
            <PictureInPictureIcon size={16} />
          </button>
        </div>
      </div>
    </div>
  );
};

export default PlayerControls;
