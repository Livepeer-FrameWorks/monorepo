import React from "react";
import { cn } from "@livepeer-frameworks/player-core";

interface TitleOverlayProps {
  title?: string | null;
  description?: string | null;
  isVisible: boolean;
  className?: string;
}

/**
 * Title/description overlay that appears at the top of the player.
 * Visible on hover or when paused - controlled by parent via isVisible prop.
 */
const TitleOverlay: React.FC<TitleOverlayProps> = ({
  title,
  description,
  isVisible,
  className,
}) => {
  // Don't render if no content
  if (!title && !description) return null;

  return (
    <div
      className={cn(
        "fw-title-overlay absolute inset-x-0 top-0 z-20 pointer-events-none",
        "bg-gradient-to-b from-black/70 via-black/40 to-transparent",
        "px-4 py-3 transition-opacity duration-300",
        isVisible ? "opacity-100" : "opacity-0",
        className
      )}
    >
      {title && <h2 className="text-white text-sm font-medium truncate max-w-[80%]">{title}</h2>}
      {description && (
        <p className="text-white/70 text-xs mt-0.5 line-clamp-2 max-w-[70%]">{description}</p>
      )}
    </div>
  );
};

export default TitleOverlay;
