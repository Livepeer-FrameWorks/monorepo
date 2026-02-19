import React from "react";
import { cn } from "@livepeer-frameworks/player-core";

interface LogoOverlayProps {
  src: string;
  show?: boolean;
  position?: "top-left" | "top-right" | "bottom-left" | "bottom-right";
  width?: number;
  height?: number | "auto";
  clickUrl?: string;
}

const POSITION_MAP: Record<NonNullable<LogoOverlayProps["position"]>, string> = {
  "top-left": "left-3 top-3 sm:left-4 sm:top-4",
  "top-right": "right-3 top-3 sm:right-4 sm:top-4",
  "bottom-left": "left-3 bottom-3 sm:left-4 sm:bottom-4",
  "bottom-right": "right-3 bottom-3 sm:right-4 sm:bottom-4",
};

const LogoOverlay: React.FC<LogoOverlayProps> = ({
  src,
  show = true,
  position = "bottom-right",
  width = 96,
  height = "auto",
  clickUrl,
}) => {
  if (!show) return null;

  const content = (
    <img
      src={src}
      alt="FrameWorks logo"
      width={width}
      height={height === "auto" ? undefined : height}
      className={cn(
        "max-h-[72px] rounded-md border border-white/10 bg-black/40 p-2 shadow-lg backdrop-blur transition",
        clickUrl ? "hover:bg-black/60" : ""
      )}
      style={{ width, height: height === "auto" ? undefined : height }}
    />
  );

  if (clickUrl) {
    return (
      <a
        href={clickUrl}
        target="_blank"
        rel="noreferrer"
        className={cn(
          "absolute z-40 inline-flex items-center justify-center opacity-90",
          POSITION_MAP[position]
        )}
      >
        {content}
      </a>
    );
  }

  return (
    <div
      className={cn(
        "absolute z-40 inline-flex items-center justify-center opacity-90",
        POSITION_MAP[position]
      )}
    >
      {content}
    </div>
  );
};

export default LogoOverlay;
