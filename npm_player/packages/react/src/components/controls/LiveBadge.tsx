import React from "react";
import { usePlayerContextOptional } from "../../context/player";
import { useTranslate } from "../../context/i18n";
import { cn } from "@livepeer-frameworks/player-core";
import { SeekToLiveIcon } from "../Icons";

export interface LiveBadgeProps {
  isLive?: boolean;
  isNearLive?: boolean;
  onJumpToLive?: () => void;
  className?: string;
}

export const LiveBadge: React.FC<LiveBadgeProps> = ({
  isLive: propIsLive,
  isNearLive: propIsNearLive,
  onJumpToLive,
  className,
}) => {
  const ctx = usePlayerContextOptional();
  const t = useTranslate();

  const isLive = propIsLive ?? ctx?.state.isEffectivelyLive ?? false;
  const isNearLive = propIsNearLive ?? true;
  const handleClick = onJumpToLive ?? (() => ctx?.jumpToLive());

  if (!isLive) return null;

  return (
    <button
      type="button"
      className={cn(
        "fw-live-badge",
        isNearLive ? "fw-live-badge--active" : "fw-live-badge--behind",
        className
      )}
      onClick={handleClick}
      disabled={isNearLive}
      title={t("live")}
    >
      {t("live").toUpperCase()}
      {!isNearLive && <SeekToLiveIcon size={10} />}
    </button>
  );
};
