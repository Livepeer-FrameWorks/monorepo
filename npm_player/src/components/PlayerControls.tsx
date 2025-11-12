import React, { useEffect, useMemo, useState } from "react";
import { globalPlayerManager } from "../core";
import { cn } from "../lib/utils";
import { Badge } from "../ui/badge";
import { Button } from "../ui/button";
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
  if (!isVisible) return null;

  const player = globalPlayerManager.getCurrentPlayer();
  const video = player?.getVideoElement();
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
    <div className={cn("fw-player-surface pointer-events-none absolute inset-x-0 bottom-0 px-2 pb-2 sm:px-4 sm:pb-4", className)}>
      <div className="pointer-events-auto flex w-full flex-wrap items-center gap-2 rounded-lg bg-gradient-to-t from-black/80 via-black/60 to-black/5 px-3 py-2 text-foreground sm:gap-3 sm:px-4 sm:py-3">
        <div className="flex items-center gap-1 sm:gap-2">
          <Button type="button" size="icon" variant="ghost" aria-label={isPlaying ? "Pause" : "Play"} onClick={handlePlayPause}>
            <PlayPauseIcon isPlaying={isPlaying} size={18} />
          </Button>
          <Button type="button" size="icon" variant="ghost" aria-label="Skip back 10 seconds" onClick={handleSkipBack}>
            <SkipBackIcon size={16} />
          </Button>
          <Button type="button" size="icon" variant="ghost" aria-label="Skip forward 10 seconds" onClick={handleSkipForward}>
            <SkipForwardIcon size={16} />
          </Button>
        </div>

        <div className="flex min-w-[140px] flex-1 items-center gap-2 sm:min-w-[220px]">
          <span className="hidden font-mono text-[11px] leading-none text-muted-foreground sm:inline">{timeDisplay}</span>
          <Slider
            aria-label="Seek"
            max={1000}
            step={1}
            value={[seekValue]}
            onValueChange={handleSeekChange}
            className="relative flex-1"
            title={seekTitle}
          />
        </div>

        <div className="hidden items-center gap-2 sm:flex">
          <Button type="button" size="icon" variant="ghost" aria-label={isMuted ? "Unmute" : "Mute"} onClick={handleMute}>
            <VolumeIcon isMuted={isMuted} size={16} />
          </Button>
          <div className="w-[104px]">
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

        <div className="flex items-center gap-2 sm:ml-auto sm:gap-3">
          {textTracks.length > 0 && (
            <Select value={captionValue} onValueChange={handleCaptionChange}>
              <SelectTrigger className="w-[120px]">
                <div className="flex items-center gap-2">
                  <ClosedCaptionsIcon size={16} />
                  <SelectValue />
                </div>
              </SelectTrigger>
              <SelectContent>
                {captionOptions.map((option) => (
                  <SelectItem key={option.value} value={option.value}>
                    {option.label}
                  </SelectItem>
                ))}
              </SelectContent>
            </Select>
          )}

          {qualityOptions.length > 0 && (
            <Select value={qualityValue} onValueChange={handleQualityChange}>
              <SelectTrigger className="w-[110px]">
                <SelectValue placeholder="Quality" />
              </SelectTrigger>
              <SelectContent>
                {qualityOptions.map((option) => (
                  <SelectItem key={option.value} value={option.value}>
                    {option.label}
                  </SelectItem>
                ))}
              </SelectContent>
            </Select>
          )}

          <Select value={String(playbackRate)} onValueChange={handleSpeedChange}>
            <SelectTrigger className="w-[90px]">
              <SelectValue placeholder="Speed" />
            </SelectTrigger>
            <SelectContent>
              {SPEED_PRESETS.map((rate) => (
                <SelectItem key={rate} value={String(rate)}>
                  {rate}x
                </SelectItem>
              ))}
            </SelectContent>
          </Select>
        </div>

        <div className="flex items-center gap-2 sm:gap-3">
          {isLive && (
            <Button
              type="button"
              variant="ghost"
              className="inline-flex items-center gap-2 text-xs uppercase tracking-wide"
              onClick={handleGoLive}
            >
              <Badge
                variant={isNearLive ? "default" : "outline"}
                className={cn("flex items-center gap-1 px-2 py-0.5", !isNearLive && "bg-transparent text-foreground")}
              >
                <LiveIcon size={10} color={isNearLive ? "#ffffff" : "currentColor"} />
                Live
              </Badge>
              {!isNearLive && <span className="text-muted-foreground">Catch up</span>}
            </Button>
          )}
          <Button type="button" size="icon" variant="ghost" aria-label="Toggle fullscreen" onClick={handleFullscreen}>
            <FullscreenToggleIcon isFullscreen={isFullscreen} size={16} />
          </Button>
          <Button type="button" size="icon" variant="ghost" aria-label="Toggle picture in picture" onClick={handlePictureInPicture}>
            <PictureInPictureIcon size={16} />
          </Button>
        </div>
      </div>
    </div>
  );
};

export default PlayerControls;
