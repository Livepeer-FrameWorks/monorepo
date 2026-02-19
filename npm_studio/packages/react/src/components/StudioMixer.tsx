import React, { useState, useCallback } from "react";
import { useStreamCrafterContext } from "../context/StreamCrafterContext";
import { useStudioTranslate } from "../context/StudioI18nContext";
import { VolumeSlider } from "./VolumeSlider";
import type { MediaSource } from "@livepeer-frameworks/streamcrafter-core";

function cn(...classes: (string | undefined | false)[]): string {
  return classes.filter(Boolean).join(" ");
}

const CameraIcon = ({ size = 16 }: { size?: number }) => (
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

const MonitorIcon = ({ size = 16 }: { size?: number }) => (
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

const MicIcon = ({ size = 14, muted = false }: { size?: number; muted?: boolean }) =>
  muted ? (
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
      <line x1="1" y1="1" x2="23" y2="23" />
      <path d="M9 9v3a3 3 0 0 0 5.12 2.12M15 9.34V4a3 3 0 0 0-5.94-.6" />
      <path d="M17 16.95A7 7 0 0 1 5 12v-2m14 0v2a7 7 0 0 1-.11 1.23" />
      <line x1="12" y1="19" x2="12" y2="23" />
      <line x1="8" y1="23" x2="16" y2="23" />
    </svg>
  ) : (
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
      <path d="M12 1a3 3 0 0 0-3 3v8a3 3 0 0 0 6 0V4a3 3 0 0 0-3-3z" />
      <path d="M19 10v2a7 7 0 0 1-14 0v-2" />
      <line x1="12" y1="19" x2="12" y2="23" />
      <line x1="8" y1="23" x2="16" y2="23" />
    </svg>
  );

const XIcon = ({ size = 14 }: { size?: number }) => (
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
    <line x1="18" y1="6" x2="6" y2="18" />
    <line x1="6" y1="6" x2="18" y2="18" />
  </svg>
);

const VideoIcon = ({ size = 14, active = false }: { size?: number; active?: boolean }) => (
  <svg
    width={size}
    height={size}
    viewBox="0 0 24 24"
    fill={active ? "currentColor" : "none"}
    stroke="currentColor"
    strokeWidth="2"
    strokeLinecap="round"
    strokeLinejoin="round"
  >
    <polygon points="23 7 16 12 23 17 23 7" />
    <rect x="1" y="5" width="15" height="14" rx="2" ry="2" />
  </svg>
);

const ChevronsRightIcon = ({ size = 14 }: { size?: number }) => (
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
    <polyline points="13 17 18 12 13 7" />
    <polyline points="6 17 11 12 6 7" />
  </svg>
);

const ChevronsLeftIcon = ({ size = 14 }: { size?: number }) => (
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
    <polyline points="11 17 6 12 11 7" />
    <polyline points="18 17 13 12 18 7" />
  </svg>
);

export interface StudioMixerProps {
  /** Override sources (falls back to context) */
  sources?: MediaSource[];
  /** Whether compositor mode is enabled */
  enableCompositor?: boolean;
  /** Initially collapsed */
  defaultCollapsed?: boolean;
}

export const StudioMixer: React.FC<StudioMixerProps> = ({
  sources: propSources,
  enableCompositor = true,
  defaultCollapsed = false,
}) => {
  const ctx = useStreamCrafterContext();
  const t = useStudioTranslate();
  const [showSources, setShowSources] = useState(!defaultCollapsed);
  const sources = propSources ?? ctx.sources;

  const toggleMute = useCallback(
    (sourceId: string, currentMuted: boolean) => ctx.setSourceMuted(sourceId, !currentMuted),
    [ctx]
  );

  if (sources.length === 0) return null;

  return (
    <div className={cn("fw-sc-section fw-sc-mixer", !showSources && "fw-sc-section--collapsed")}>
      <div
        className="fw-sc-section-header"
        onClick={() => setShowSources(!showSources)}
        title={showSources ? t("collapseMixer") : t("expandMixer")}
      >
        <span>
          {t("mixer")} ({sources.length})
        </span>
        {showSources ? <ChevronsRightIcon size={14} /> : <ChevronsLeftIcon size={14} />}
      </div>
      {showSources && (
        <div className="fw-sc-section-body--flush">
          <div className="fw-sc-sources">
            {sources.map((source: MediaSource) => (
              <div key={source.id} className="fw-sc-source">
                <div className="fw-sc-source-icon">
                  {source.type === "camera" && <CameraIcon size={16} />}
                  {source.type === "screen" && <MonitorIcon size={16} />}
                </div>
                <div className="fw-sc-source-info">
                  <div className="fw-sc-source-label">
                    {source.label}
                    {source.primaryVideo && !enableCompositor && (
                      <span className="fw-sc-primary-badge">{t("primary")}</span>
                    )}
                  </div>
                  <div className="fw-sc-source-type">{source.type}</div>
                </div>
                <div className="fw-sc-source-controls">
                  {source.stream.getVideoTracks().length > 0 && !enableCompositor && (
                    <button
                      type="button"
                      className={cn(
                        "fw-sc-icon-btn",
                        source.primaryVideo && "fw-sc-icon-btn--primary"
                      )}
                      onClick={() => ctx.setPrimaryVideoSource(source.id)}
                      disabled={source.primaryVideo}
                      title={source.primaryVideo ? t("primaryVideoSource") : t("setAsPrimary")}
                    >
                      <VideoIcon size={14} active={source.primaryVideo} />
                    </button>
                  )}
                  <span className="fw-sc-volume-label">{Math.round(source.volume * 100)}%</span>
                  <VolumeSlider
                    value={source.volume}
                    onChange={(vol) => ctx.setSourceVolume(source.id, vol)}
                    compact={true}
                  />
                  <button
                    type="button"
                    className={cn("fw-sc-icon-btn", source.muted && "fw-sc-icon-btn--active")}
                    onClick={() => toggleMute(source.id, source.muted)}
                    title={source.muted ? t("unmute") : t("mute")}
                  >
                    <MicIcon size={14} muted={source.muted} />
                  </button>
                  <button
                    type="button"
                    className="fw-sc-icon-btn fw-sc-icon-btn--destructive"
                    onClick={() => ctx.removeSource(source.id)}
                    disabled={ctx.isStreaming}
                    title={ctx.isStreaming ? t("cannotRemoveWhileStreaming") : t("removeSource")}
                  >
                    <XIcon size={14} />
                  </button>
                </div>
              </div>
            ))}
          </div>
        </div>
      )}
    </div>
  );
};
