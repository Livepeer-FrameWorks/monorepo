import React, { useEffect, useMemo, useRef, useState } from "react";
import type { LoadingPosterInfo } from "@livepeer-frameworks/player-core";

export interface LoadingPosterProps {
  /** Snapshot from the controller; null hides the component. */
  loadingPoster: LoadingPosterInfo | null;
  /** Outer-element class (positioning is supplied by the parent overlay slot). */
  className?: string;
  /** Style overrides forwarded to the outer element. */
  style?: React.CSSProperties;
}

const CYCLE_MS = 2000;
const CROP_INSET_PX = 0.5;
const animationStartTimes = new Map<string, number>();
const completedAnimations = new Set<string>();

const POSTER_ROOT_STYLE: React.CSSProperties = {
  position: "absolute",
  inset: 0,
  width: "100%",
  height: "100%",
  backgroundColor: "#000",
  overflow: "hidden",
  pointerEvents: "none",
};

const STATIC_IMG_STYLE: React.CSSProperties = {
  position: "absolute",
  inset: 0,
  width: "100%",
  height: "100%",
  objectFit: "contain",
  backgroundColor: "#000",
  pointerEvents: "none",
};

const SPRITE_SVG_STYLE: React.CSSProperties = {
  position: "absolute",
  inset: 0,
  width: "100%",
  height: "100%",
  pointerEvents: "none",
  overflow: "hidden",
};

interface SpriteNaturalSize {
  url: string;
  width: number;
  height: number;
}

function shouldCacheBust(p: LoadingPosterInfo): boolean {
  if (!p.staticUrl) return false;
  if (p.staticUrl.startsWith("data:") || p.staticUrl.startsWith("blob:")) return false;
  if (p.staticSource === "thumbnail-prop") return false;
  return true;
}

function withCacheBust(p: LoadingPosterInfo): string | undefined {
  if (!p.staticUrl) return undefined;
  if (!shouldCacheBust(p)) return p.staticUrl;
  const sep = p.staticUrl.includes("?") ? "&" : "?";
  return `${p.staticUrl}${sep}_g=${p.generation}`;
}

function animationKeyFor(p: LoadingPosterInfo | null): string | null {
  if (!p || p.mode !== "animate" || p.geometry !== "measured" || !p.spriteJpgUrl) return null;
  if (p.cues.length < 2) return null;
  return p.prerollKey ?? p.staticUrl ?? p.spriteJpgUrl;
}

function cueIndexFor(tickIdx: number, cueCount: number): number {
  return Math.max(0, Math.min(tickIdx, cueCount - 1));
}

/**
 * Loading-state poster overlay shown while the stream is booting / connecting.
 * Dumb renderer: dispatches on `loadingPoster.mode` and reads the spec's
 * resolved fields. The controller owns the source-priority logic; real VTT
 * cues are the only source of sprite crop geometry.
 *
 * Returns null when the spec is null.
 */
export const LoadingPoster: React.FC<LoadingPosterProps> = ({
  loadingPoster,
  className,
  style,
}) => {
  const [tickIdx, setTickIdx] = useState(0);
  const [animationCompleted, setAnimationCompleted] = useState(false);
  const [spriteSize, setSpriteSize] = useState<SpriteNaturalSize | null>(null);
  const [spriteFailed, setSpriteFailed] = useState(false);
  const measureUrlRef = useRef<string | null>(null);
  const clipId = useMemo(() => `fw-loading-poster-clip-${Math.random().toString(36).slice(2)}`, []);

  const isAnimate = loadingPoster?.mode === "animate";
  const cueCount = loadingPoster?.cues.length ?? 0;
  const tileCount = isAnimate && loadingPoster?.geometry === "measured" ? cueCount : 0;
  const animationKey = useMemo(() => animationKeyFor(loadingPoster), [loadingPoster]);

  // Advance through the loading sequence once, then hold the result across overlay remounts.
  useEffect(() => {
    if (!animationKey || tileCount < 2) {
      setTickIdx(0);
      setAnimationCompleted(false);
      return;
    }

    if (completedAnimations.has(animationKey)) {
      setTickIdx(tileCount - 1);
      setAnimationCompleted(true);
      return;
    }

    const stepMs = Math.max(20, Math.floor(CYCLE_MS / tileCount));
    const now = Date.now();
    const existingStart = animationStartTimes.get(animationKey);
    const startedAt = existingStart !== undefined && existingStart <= now ? existingStart : now;
    animationStartTimes.set(animationKey, startedAt);

    const updateFrame = () => {
      const elapsed = Date.now() - startedAt;
      const current = Math.min(Math.floor(elapsed / stepMs), tileCount - 1);
      setTickIdx(current);
      if (current >= tileCount - 1) {
        completedAnimations.add(animationKey);
        setAnimationCompleted(true);
        return true;
      }
      return false;
    };

    setAnimationCompleted(false);
    if (updateFrame()) return;
    const id = setInterval(() => {
      if (updateFrame()) clearInterval(id);
    }, stepMs);
    return () => clearInterval(id);
  }, [animationKey, tileCount]);

  useEffect(() => {
    if (!isAnimate || loadingPoster?.geometry !== "measured") return;
    const url = loadingPoster?.spriteJpgUrl;
    if (!url) return;
    if (measureUrlRef.current === url && (spriteSize || spriteFailed)) return;
    measureUrlRef.current = url;
    setSpriteSize(null);
    setSpriteFailed(false);
    const img = new Image();
    let cancelled = false;
    img.onload = () => {
      if (cancelled) return;
      setSpriteSize({ url, width: img.naturalWidth, height: img.naturalHeight });
    };
    img.onerror = () => {
      if (cancelled) return;
      setSpriteFailed(true);
    };
    img.src = url;
    return () => {
      cancelled = true;
    };
  }, [isAnimate, loadingPoster?.geometry, loadingPoster?.spriteJpgUrl, spriteFailed, spriteSize]);

  if (!loadingPoster) return null;

  // Static branch — straightforward <img>.
  if (loadingPoster.mode === "static") {
    const src = withCacheBust(loadingPoster);
    if (!src) return null;
    return (
      <div className={className} style={{ ...POSTER_ROOT_STYLE, ...style }} aria-hidden="true">
        <img src={src} alt="" style={STATIC_IMG_STYLE} />
      </div>
    );
  }

  // Animate branch.
  const staticSrc = withCacheBust(loadingPoster);
  if (animationCompleted && staticSrc) {
    return (
      <div className={className} style={{ ...POSTER_ROOT_STYLE, ...style }} aria-hidden="true">
        <img src={staticSrc} alt="" style={STATIC_IMG_STYLE} />
      </div>
    );
  }

  // Resolve current tile's pixel rect.
  let cueRect: { x: number; y: number; width: number; height: number } | null = null;
  let imageWidth = 0;
  let imageHeight = 0;
  if (loadingPoster.geometry === "measured") {
    const cue = loadingPoster.cues[cueIndexFor(tickIdx, loadingPoster.cues.length)];
    if (
      cue &&
      spriteSize &&
      spriteSize.url === loadingPoster.spriteJpgUrl &&
      spriteSize.width > 0 &&
      spriteSize.height > 0
    ) {
      const inset = Math.min(CROP_INSET_PX, cue.width / 4, cue.height / 4);
      cueRect = {
        x: cue.x + inset,
        y: cue.y + inset,
        width: Math.max(1, cue.width - inset * 2),
        height: Math.max(1, cue.height - inset * 2),
      };
      imageWidth = spriteSize.width;
      imageHeight = spriteSize.height;
    }
  }

  // Real VTT cues or the sprite image dimensions are not available yet — show static fallback.
  if (!cueRect || !loadingPoster.spriteJpgUrl) {
    const src = staticSrc;
    if (!src) return null;
    return (
      <div className={className} style={{ ...POSTER_ROOT_STYLE, ...style }} aria-hidden="true">
        <img src={src} alt="" style={STATIC_IMG_STYLE} />
      </div>
    );
  }

  return (
    <div className={className} style={{ ...POSTER_ROOT_STYLE, ...style }} aria-hidden="true">
      <svg
        style={SPRITE_SVG_STYLE}
        viewBox={`0 0 ${cueRect.width} ${cueRect.height}`}
        preserveAspectRatio="xMidYMid meet"
      >
        <defs>
          <clipPath id={clipId} clipPathUnits="userSpaceOnUse">
            <rect x="0" y="0" width={cueRect.width} height={cueRect.height} />
          </clipPath>
        </defs>
        <g clipPath={`url(#${clipId})`}>
          <image
            href={loadingPoster.spriteJpgUrl}
            x={-cueRect.x}
            y={-cueRect.y}
            width={imageWidth}
            height={imageHeight}
            preserveAspectRatio="none"
          />
        </g>
      </svg>
    </div>
  );
};

export default LoadingPoster;
