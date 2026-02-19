import React, { useCallback } from "react";
import { useStreamCrafterContext } from "../context/StreamCrafterContext";
import { useStudioTranslate } from "../context/StudioI18nContext";
import { StudioSettings } from "./StudioSettings";

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

const MonitorIcon = ({ size = 18 }: { size?: number }) => (
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
    <rect x="2" y="3" width="20" height="14" rx="2" ry="2" />
    <line x1="8" y1="21" x2="16" y2="21" />
    <line x1="12" y1="17" x2="12" y2="21" />
  </svg>
);

export interface StudioActionBarProps {
  /** Whether a WHIP endpoint is configured (required to go live) */
  whipUrl?: string;
  /** Show the settings button */
  showSettingsButton?: boolean;
  /** Custom children to render instead of default buttons */
  children?: React.ReactNode;
}

export const StudioActionBar: React.FC<StudioActionBarProps> = ({
  whipUrl,
  showSettingsButton = true,
  children,
}) => {
  const ctx = useStreamCrafterContext();
  const t = useStudioTranslate();
  const canAddSource = ctx.state !== "destroyed" && ctx.state !== "error";
  const hasCamera = ctx.sources.some((s) => s.type === "camera");
  const canStream = ctx.isCapturing && !ctx.isStreaming && whipUrl !== undefined;

  const handleStartCamera = useCallback(async () => {
    try {
      await ctx.startCamera();
    } catch (err) {
      console.error("Failed to start camera:", err);
    }
  }, [ctx]);

  const handleStartScreenShare = useCallback(async () => {
    try {
      await ctx.startScreenShare({ audio: true });
    } catch (err) {
      console.error("Failed to start screen share:", err);
    }
  }, [ctx]);

  const handleGoLive = useCallback(async () => {
    try {
      await ctx.startStreaming();
    } catch (err) {
      console.error("Failed to start streaming:", err);
    }
  }, [ctx]);

  const handleStop = useCallback(async () => {
    await ctx.stopStreaming();
  }, [ctx]);

  if (children) {
    return <div className="fw-sc-actions">{children}</div>;
  }

  return (
    <div className="fw-sc-actions">
      <button
        type="button"
        className="fw-sc-action-secondary"
        onClick={handleStartCamera}
        disabled={!canAddSource || hasCamera}
        title={hasCamera ? t("cameraActive") : t("addCamera")}
      >
        <CameraIcon size={18} />
      </button>
      <button
        type="button"
        className="fw-sc-action-secondary"
        onClick={handleStartScreenShare}
        disabled={!canAddSource}
        title={t("shareScreen")}
      >
        <MonitorIcon size={18} />
      </button>
      {showSettingsButton && <StudioSettings />}
      {!ctx.isStreaming ? (
        <button
          type="button"
          className="fw-sc-action-primary"
          onClick={handleGoLive}
          disabled={!canStream}
        >
          {ctx.state === "connecting" ? t("connecting") : t("goLive")}
        </button>
      ) : (
        <button
          type="button"
          className="fw-sc-action-primary fw-sc-action-stop"
          onClick={handleStop}
        >
          {t("stopStreaming")}
        </button>
      )}
    </div>
  );
};
