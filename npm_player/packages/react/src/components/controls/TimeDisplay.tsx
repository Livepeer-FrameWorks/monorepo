import React, { useMemo } from "react";
import { usePlayerContextOptional } from "../../context/player";
import { formatTimeDisplay } from "@livepeer-frameworks/player-core";

export interface TimeDisplayProps {
  currentTime?: number;
  duration?: number;
  isLive?: boolean;
  className?: string;
}

export const TimeDisplay: React.FC<TimeDisplayProps> = ({
  currentTime: propCurrentTime,
  duration: propDuration,
  isLive: propIsLive,
  className,
}) => {
  const ctx = usePlayerContextOptional();

  const currentTime = propCurrentTime ?? ctx?.state.currentTime ?? 0;
  const duration = propDuration ?? ctx?.state.duration ?? 0;
  const isLive = propIsLive ?? ctx?.state.isEffectivelyLive ?? false;

  const formatted = useMemo(
    () =>
      formatTimeDisplay({
        isLive,
        currentTime,
        duration,
        liveEdge: duration,
        seekableStart: 0,
      }),
    [isLive, currentTime, duration]
  );

  return <span className={className ?? "fw-time-display"}>{formatted}</span>;
};
