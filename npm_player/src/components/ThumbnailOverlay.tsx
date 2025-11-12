import React from "react";
import { cn } from "../lib/utils";
import { Button } from "../ui/button";
import type { ThumbnailOverlayProps } from "../types";

const ThumbnailOverlay: React.FC<ThumbnailOverlayProps> = ({
  thumbnailUrl,
  onPlay,
  message,
  showUnmuteMessage = false,
  style,
  className
}) => {
  const handleClick = () => {
    onPlay?.();
  };

  return (
    <div
      role="button"
      tabIndex={0}
      onClick={handleClick}
      onKeyDown={(event) => {
        if (event.key === "Enter" || event.key === " ") {
          event.preventDefault();
          handleClick();
        }
      }}
      style={style}
      className={cn(
        "fw-player-thumbnail relative flex h-full min-h-[280px] w-full cursor-pointer items-center justify-center overflow-hidden rounded-xl bg-slate-950 text-foreground outline-none transition focus-visible:ring-2 focus-visible:ring-primary focus-visible:ring-offset-2 focus-visible:ring-offset-background"
      , className)}
    >
      {thumbnailUrl && (
        <div
          className="absolute inset-0 bg-cover bg-center"
          style={{ backgroundImage: `url(${thumbnailUrl})` }}
        />
      )}

      <div
        className={cn(
          "absolute inset-0 bg-slate-950/70",
          !thumbnailUrl && "bg-gradient-to-br from-slate-900 via-slate-950 to-slate-900"
        )}
      />

      <div className="relative z-10 flex max-w-[320px] flex-col items-center gap-4 px-6 text-center text-sm sm:gap-6">
        {showUnmuteMessage ? (
          <div className="w-full rounded-lg border border-white/15 bg-black/80 p-4 text-sm text-white shadow-lg backdrop-blur">
            <div className="mb-1 flex items-center justify-center gap-2 text-base font-semibold text-primary">
              <span aria-hidden="true">🔇</span> Click to unmute
            </div>
            <p className="text-xs text-white/80">Stream is playing muted — tap to enable sound.</p>
          </div>
        ) : (
          <>
            <Button
              type="button"
              size="icon"
              variant="secondary"
              className="h-20 w-20 rounded-full bg-primary/90 text-primary-foreground shadow-lg shadow-primary/40 transition hover:bg-primary focus-visible:bg-primary"
              aria-label="Play stream"
            >
              <svg
                viewBox="0 0 24 24"
                fill="currentColor"
                className="ml-0.5 h-8 w-8"
                aria-hidden="true"
              >
                <path d="M8 5v14l11-7z" />
              </svg>
            </Button>
            <div className="w-full rounded-lg border border-white/10 bg-black/70 p-5 text-white shadow-inner backdrop-blur">
              <p className="text-base font-semibold text-primary">
                {message ?? "Click to play"}
              </p>
              <p className="mt-1 text-xs text-white/70">
                {message ? "Start streaming instantly" : "Jump into the live feed"}
              </p>
            </div>
          </>
        )}
      </div>
    </div>
  );
};

export default ThumbnailOverlay;
