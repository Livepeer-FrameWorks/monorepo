import React from "react";
import { cn } from "@livepeer-frameworks/player-core";

export interface ControlBarProps {
  children: React.ReactNode;
  className?: string;
  visible?: boolean;
}

export const ControlBar: React.FC<ControlBarProps> = ({ children, className, visible }) => {
  return (
    <div
      className={cn(
        "fw-controls-wrapper",
        visible !== false ? "fw-controls-wrapper--visible" : "fw-controls-wrapper--hidden",
        className
      )}
    >
      <div className="fw-control-bar pointer-events-auto" onClick={(e) => e.stopPropagation()}>
        {children}
      </div>
    </div>
  );
};
