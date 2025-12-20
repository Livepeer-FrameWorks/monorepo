import React from "react";
import { cn } from "@livepeer-frameworks/player-core";

interface SpeedIndicatorProps {
  isVisible: boolean;
  speed: number;
  className?: string;
}

/**
 * Speed indicator overlay that appears when holding for fast-forward.
 * Shows the current playback speed (e.g., "2x") in a pill overlay.
 */
const SpeedIndicator: React.FC<SpeedIndicatorProps> = ({
  isVisible,
  speed,
  className,
}) => {
  return (
    <div
      className={cn(
        "fw-speed-indicator absolute top-3 right-3 z-30 pointer-events-none",
        "transition-opacity duration-150",
        isVisible ? "opacity-100" : "opacity-0",
        className
      )}
    >
      <div
        className={cn(
          "bg-black/60 text-white px-2.5 py-1 rounded-md",
          "text-xs font-semibold tabular-nums",
          "flex items-center gap-2",
          "border border-white/15",
          "transform transition-transform duration-150",
          isVisible ? "scale-100" : "scale-90"
        )}
      >
        <FastForwardIcon className="w-4 h-4" />
        <span>{speed}x</span>
      </div>
    </div>
  );
};

// Simple fast-forward icon
const FastForwardIcon: React.FC<{ className?: string }> = ({ className }) => (
  <svg
    viewBox="0 0 24 24"
    fill="currentColor"
    className={className}
    aria-hidden="true"
  >
    <path d="M4 18l8.5-6L4 6v12zm9-12v12l8.5-6L13 6z" />
  </svg>
);

export default SpeedIndicator;
