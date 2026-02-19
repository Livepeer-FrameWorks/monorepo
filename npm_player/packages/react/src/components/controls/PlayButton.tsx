import React from "react";
import { usePlayerContextOptional } from "../../context/player";
import { useTranslate } from "../../context/i18n";
import { PlayPauseIcon } from "../Icons";

export interface PlayButtonProps {
  isPlaying?: boolean;
  onToggle?: () => void;
  size?: number;
  className?: string;
}

export const PlayButton: React.FC<PlayButtonProps> = ({
  isPlaying: propIsPlaying,
  onToggle,
  size = 18,
  className,
}) => {
  const ctx = usePlayerContextOptional();
  const t = useTranslate();
  const isPlaying = propIsPlaying ?? ctx?.state.isPlaying ?? false;
  const handleClick = onToggle ?? (() => ctx?.togglePlay());

  return (
    <button
      type="button"
      className={className ?? "fw-btn-flush"}
      aria-label={isPlaying ? t("pause") : t("play")}
      aria-pressed={isPlaying}
      title={isPlaying ? `${t("pause")} (k)` : `${t("play")} (k)`}
      onClick={handleClick}
    >
      <PlayPauseIcon isPlaying={isPlaying} size={size} />
    </button>
  );
};
