import React, { useEffect, useState } from "react";
import type { LoadingPosterInfo } from "@livepeer-frameworks/player-core";

export type LoadingPosterMode = "animate" | "latest";

export interface LoadingPosterProps {
  /** Snapshot from the controller; null hides the component. */
  loadingPoster: LoadingPosterInfo | null;
  /**
   * Render mode:
   *   - "animate" (default): cycle through all sprite tiles in ~2s
   *   - "latest": full-resolution Chandler poster.jpg, no sprite (sprite tiles are too low-res for static display)
   */
  mode?: LoadingPosterMode;
  /** Optional explicit fallback poster URL (e.g. config.poster). */
  fallbackPosterUrl?: string;
  /** Total animation cycle duration in ms (animate mode only). Default: 2000. */
  cycleMs?: number;
  /** Style/class overrides for the outer element. */
  className?: string;
  style?: React.CSSProperties;
}

const STATIC_IMG_BASE_STYLE: React.CSSProperties = {
  position: "absolute",
  inset: 0,
  width: "100%",
  height: "100%",
  objectFit: "cover",
  pointerEvents: "none",
};

const SPRITE_FRAME_STYLE: React.CSSProperties = {
  position: "absolute",
  inset: 0,
  pointerEvents: "none",
};

/**
 * Loading-state poster overlay shown while the stream is booting / connecting.
 *
 * Source priority (per mode):
 *   - "animate": sprite cycle → Chandler poster.jpg → Mist preview JPEG → fallbackPosterUrl
 *   - "latest":  Chandler poster.jpg → Mist preview JPEG → fallbackPosterUrl (never uses sprite)
 *
 * Returns null when no source is available.
 */
export const LoadingPoster: React.FC<LoadingPosterProps> = ({
  loadingPoster,
  mode = "animate",
  fallbackPosterUrl,
  cycleMs = 2000,
  className,
  style,
}) => {
  const [tickIdx, setTickIdx] = useState(0);

  const cues = loadingPoster?.cues ?? [];
  const spriteJpgUrl = loadingPoster?.spriteJpgUrl;
  const cols = loadingPoster?.columns ?? 0;
  const rows = loadingPoster?.rows ?? 0;
  const canAnimateSprite =
    mode === "animate" && spriteJpgUrl && cues.length >= 2 && cols > 0 && rows > 0;

  useEffect(() => {
    if (!canAnimateSprite) {
      setTickIdx(0);
      return;
    }
    const stepMs = Math.max(20, Math.floor(cycleMs / cues.length));
    const id = setInterval(() => {
      setTickIdx((i) => (i + 1) % cues.length);
    }, stepMs);
    return () => clearInterval(id);
  }, [canAnimateSprite, cues.length, cycleMs]);

  if (canAnimateSprite) {
    const cue = cues[tickIdx % cues.length];
    const spriteWidth = loadingPoster?.spriteWidth || cue.width * cols;
    const spriteHeight = loadingPoster?.spriteHeight || cue.height * rows;
    return (
      <svg
        className={className}
        style={{
          ...SPRITE_FRAME_STYLE,
          ...style,
        }}
        viewBox={`${cue.x} ${cue.y} ${cue.width} ${cue.height}`}
        preserveAspectRatio="xMidYMid slice"
        aria-hidden="true"
      >
        <image href={spriteJpgUrl} x={0} y={0} width={spriteWidth} height={spriteHeight} />
      </svg>
    );
  }

  const staticUrl = loadingPoster?.posterUrl || loadingPoster?.mistPreviewUrl || fallbackPosterUrl;
  if (!staticUrl) return null;

  // Cache-bust on every cue regen (loadingPoster.generation bumps each time) so the
  // browser refetches the latest poster.jpg. Skip on data: / blob: URLs and on the
  // user-provided fallback (no implicit refresh contract there).
  const isRefreshable =
    !!staticUrl &&
    staticUrl !== fallbackPosterUrl &&
    !staticUrl.startsWith("data:") &&
    !staticUrl.startsWith("blob:");
  const finalUrl =
    isRefreshable && loadingPoster
      ? `${staticUrl}${staticUrl.includes("?") ? "&" : "?"}_g=${loadingPoster.generation}`
      : staticUrl;

  return (
    <img
      className={className}
      src={finalUrl}
      alt=""
      style={{ ...STATIC_IMG_BASE_STYLE, ...style }}
      aria-hidden="true"
    />
  );
};

export default LoadingPoster;
