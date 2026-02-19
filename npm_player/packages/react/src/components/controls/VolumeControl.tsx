import React, { useState } from "react";
import { usePlayerContextOptional } from "../../context/player";
import { useTranslate } from "../../context/i18n";
import { cn } from "@livepeer-frameworks/player-core";
import { Slider } from "../../ui/slider";
import { VolumeIcon } from "../Icons";

export interface VolumeControlProps {
  volume?: number;
  isMuted?: boolean;
  onVolumeChange?: (v: number) => void;
  onToggleMute?: () => void;
  className?: string;
}

export const VolumeControl: React.FC<VolumeControlProps> = ({
  volume: propVolume,
  isMuted: propIsMuted,
  onVolumeChange,
  onToggleMute,
  className,
}) => {
  const ctx = usePlayerContextOptional();
  const t = useTranslate();
  const [isExpanded, setIsExpanded] = useState(false);

  const volume = propVolume ?? ctx?.state.volume ?? 1;
  const isMuted = propIsMuted ?? ctx?.state.isMuted ?? false;
  const displayValue = isMuted ? 0 : Math.round(volume * 100);

  const handleMute = onToggleMute ?? (() => ctx?.toggleMute());
  const handleChange = (value: number[]) => {
    const next = Math.max(0, Math.min(100, value[0] ?? 0));
    if (onVolumeChange) {
      onVolumeChange(next / 100);
    } else {
      ctx?.setVolume(next / 100);
    }
  };

  return (
    <div
      className={cn("fw-volume-group", isExpanded && "fw-volume-group--expanded", className)}
      onMouseEnter={() => setIsExpanded(true)}
      onMouseLeave={() => setIsExpanded(false)}
      onFocusCapture={() => setIsExpanded(true)}
      onBlurCapture={(e) => {
        if (!e.currentTarget.contains(e.relatedTarget as Node)) {
          setIsExpanded(false);
        }
      }}
    >
      <button
        type="button"
        className="fw-volume-btn"
        aria-label={isMuted ? t("unmute") : t("mute")}
        aria-pressed={isMuted}
        title={isMuted ? `${t("unmute")} (m)` : `${t("mute")} (m)`}
        onClick={handleMute}
      >
        <VolumeIcon isMuted={isMuted} size={16} />
      </button>
      <div
        className={cn(
          "fw-volume-slider-wrapper",
          isExpanded ? "fw-volume-slider-wrapper--expanded" : "fw-volume-slider-wrapper--collapsed"
        )}
      >
        <Slider
          orientation="horizontal"
          aria-label={t("volume")}
          max={100}
          step={1}
          value={[displayValue]}
          onValueChange={handleChange}
          className="w-full"
        />
      </div>
    </div>
  );
};
