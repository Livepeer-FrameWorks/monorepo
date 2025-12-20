import React, { useEffect, useState } from "react";
import { cn } from "@livepeer-frameworks/player-core";

export type SkipDirection = "back" | "forward" | null;

interface SkipIndicatorProps {
  direction: SkipDirection;
  seconds?: number;
  className?: string;
  onHide?: () => void;
}

/**
 * Skip indicator overlay that appears when double-tapping to skip.
 * Shows the skip direction and amount (e.g., "-10s" or "+10s") with a ripple effect.
 */
const SkipIndicator: React.FC<SkipIndicatorProps> = ({
  direction,
  seconds = 10,
  className,
  onHide,
}) => {
  const [isAnimating, setIsAnimating] = useState(false);

  useEffect(() => {
    if (direction) {
      setIsAnimating(true);
      const timer = setTimeout(() => {
        setIsAnimating(false);
        onHide?.();
      }, 600);
      return () => clearTimeout(timer);
    }
  }, [direction, onHide]);

  if (!direction) return null;

  const isBack = direction === "back";

  return (
    <div
      className={cn(
        "fw-skip-indicator absolute inset-0 z-30 pointer-events-none",
        "flex items-center",
        isBack ? "justify-start pl-8" : "justify-end pr-8",
        className
      )}
    >
      {/* Ripple background */}
      <div
        className={cn(
          "absolute top-0 bottom-0 w-1/3",
          isBack ? "left-0" : "right-0",
          "bg-white/10",
          isAnimating && "animate-pulse"
        )}
      />

      {/* Skip content */}
      <div
        className={cn(
          "relative flex flex-col items-center gap-1 text-white",
          "transition-all duration-200",
          isAnimating ? "opacity-100 scale-100" : "opacity-0 scale-75"
        )}
      >
        {/* Icon */}
        <div className="flex">
          {isBack ? (
            <>
              <RewindIcon className="w-8 h-8" />
              <RewindIcon className="w-8 h-8 -ml-4" />
            </>
          ) : (
            <>
              <FastForwardIcon className="w-8 h-8" />
              <FastForwardIcon className="w-8 h-8 -ml-4" />
            </>
          )}
        </div>

        {/* Text */}
        <span className="text-sm font-semibold tabular-nums">
          {isBack ? `-${seconds}s` : `+${seconds}s`}
        </span>
      </div>
    </div>
  );
};

const RewindIcon: React.FC<{ className?: string }> = ({ className }) => (
  <svg
    viewBox="0 0 24 24"
    fill="currentColor"
    className={className}
    aria-hidden="true"
  >
    <path d="M11 18V6l-8.5 6 8.5 6zm.5-6l8.5 6V6l-8.5 6z" />
  </svg>
);

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

export default SkipIndicator;
