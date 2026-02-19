import React from "react";
import { usePlayerContextOptional } from "../../context/player";
import { useTranslate } from "../../context/i18n";
import { FullscreenToggleIcon } from "../Icons";

export interface FullscreenButtonProps {
  isFullscreen?: boolean;
  onToggle?: () => void;
  size?: number;
  className?: string;
}

export const FullscreenButton: React.FC<FullscreenButtonProps> = ({
  isFullscreen: propIsFullscreen,
  onToggle,
  size = 16,
  className,
}) => {
  const ctx = usePlayerContextOptional();
  const t = useTranslate();
  const isFullscreen = propIsFullscreen ?? ctx?.state.isFullscreen ?? false;
  const handleClick = onToggle ?? (() => ctx?.toggleFullscreen());

  return (
    <button
      type="button"
      className={className ?? "fw-btn-flush"}
      aria-label={isFullscreen ? t("exitFullscreen") : t("fullscreen")}
      aria-pressed={isFullscreen}
      title={isFullscreen ? `${t("exitFullscreen")} (f)` : `${t("fullscreen")} (f)`}
      onClick={handleClick}
    >
      <FullscreenToggleIcon isFullscreen={isFullscreen} size={size} />
    </button>
  );
};
