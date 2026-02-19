import React, { useEffect, useRef } from "react";
import { useStreamCrafterContext } from "../context/StreamCrafterContext";
import { useStudioTranslate } from "../context/StudioI18nContext";
import { useCompositor } from "../hooks/useCompositor";
import { CompositorControls } from "./CompositorControls";
import type { MediaSource } from "@livepeer-frameworks/streamcrafter-core";

const CameraIcon = ({ size = 18 }: { size?: number }) => (
  <svg
    width={size}
    height={size}
    viewBox="0 0 24 24"
    fill="none"
    stroke="currentColor"
    strokeWidth="2"
    strokeLinecap="round"
    strokeLinejoin="round"
  >
    <path d="M23 7l-7 5 7 5V7z" />
    <rect x="1" y="5" width="15" height="14" rx="2" ry="2" />
  </svg>
);

export interface StudioPreviewProps {
  /** Enable compositor overlay controls */
  enableCompositor?: boolean;
  /** Compositor config */
  compositorConfig?: {
    renderer?: "auto" | "webgpu" | "webgl" | "canvas2d";
    width?: number;
    height?: number;
    frameRate?: number;
  };
  /** Override sources (falls back to context) */
  sources?: MediaSource[];
}

export const StudioPreview: React.FC<StudioPreviewProps> = ({
  enableCompositor = true,
  compositorConfig,
  sources: propSources,
}) => {
  const ctx = useStreamCrafterContext();
  const t = useStudioTranslate();
  const videoRef = useRef<HTMLVideoElement>(null);
  const sources = propSources ?? ctx.sources;

  const compositor = useCompositor({
    controller: enableCompositor ? ctx.getController() : null,
    autoEnable: enableCompositor,
    config: compositorConfig,
  });

  useEffect(() => {
    if (videoRef.current && ctx.mediaStream) {
      videoRef.current.srcObject = ctx.mediaStream;
      videoRef.current.play().catch(() => {});
    } else if (videoRef.current) {
      videoRef.current.srcObject = null;
    }
  }, [ctx.mediaStream]);

  const statusText =
    ctx.state === "connecting"
      ? t("connecting")
      : ctx.state === "reconnecting"
        ? t("reconnecting")
        : "";

  return (
    <div className="fw-sc-preview-wrapper">
      <div className="fw-sc-preview">
        <video ref={videoRef} playsInline muted autoPlay aria-label={t("streamPreview")} />

        {!ctx.mediaStream && (
          <div className="fw-sc-preview-placeholder">
            <CameraIcon size={48} />
            <span>{t("addSourcePrompt")}</span>
          </div>
        )}

        {(ctx.state === "connecting" || ctx.state === "reconnecting") && (
          <div className="fw-sc-status-overlay">
            <div className="fw-sc-status-spinner" />
            <span className="fw-sc-status-text">{statusText}</span>
          </div>
        )}

        {ctx.isStreaming && <div className="fw-sc-live-badge">{t("live")}</div>}

        {enableCompositor && (
          <CompositorControls
            isEnabled={compositor.isEnabled}
            isInitialized={compositor.isInitialized}
            rendererType={compositor.rendererType}
            stats={compositor.stats}
            sources={sources}
            layers={compositor.activeScene?.layers ?? []}
            currentLayout={compositor.currentLayout}
            onLayoutApply={compositor.applyLayout}
            onCycleSourceOrder={(direction) => compositor.cycleSourceOrder(direction)}
          />
        )}
      </div>
    </div>
  );
};
