/**
 * StudioMediaControls Component
 *
 * Local media files in the mix, with their playback controls. Volume, mute and
 * removal live with every other source in the mixer. Reads sources and actions
 * from the StreamCrafter context unless they are given.
 */

import React, { useState } from "react";
import type { MediaSource } from "@livepeer-frameworks/streamcrafter-core";
import { useStudioTranslate } from "../context/StudioI18nContext";
import { useStreamCrafterContextOptional } from "../context/StreamCrafterContext";
import { useStudioSupport } from "../hooks/useStudioSupport";
import { takeFile } from "./StudioOverlayControls";

export interface StudioMediaActions {
  addMediaFile: (file: File) => Promise<unknown>;
  playMediaSource: (sourceId: string) => void;
  pauseMediaSource: (sourceId: string) => void;
  setMediaSourceLoop: (sourceId: string, loop: boolean) => void;
}

export interface StudioMediaControlsProps {
  /** Defaults to the sources of the parent StreamCrafter. */
  sources?: MediaSource[];
  /** Defaults to the media actions of the parent StreamCrafter. */
  media?: StudioMediaActions;
  className?: string;
}

export function StudioMediaControls({
  sources: sourcesProp,
  media: mediaProp,
  className = "",
}: StudioMediaControlsProps) {
  const t = useStudioTranslate();
  const support = useStudioSupport();
  const ctx = useStreamCrafterContextOptional();
  const [fileFailed, setFileFailed] = useState(false);
  const sources = sourcesProp ?? ctx?.sources ?? [];
  const media = mediaProp ?? ctx ?? null;

  if (!media) return null;
  const files = sources.filter((source) => source.media);

  const handleFile = async (e: React.ChangeEvent<HTMLInputElement>) => {
    const file = takeFile(e);
    if (!file) return;

    setFileFailed(false);
    try {
      await media.addMediaFile(file);
    } catch (err) {
      console.error("Failed to add media file:", err);
      setFileFailed(true);
    }
  };

  return (
    <div className={`fw-sc-producer-section ${className}`}>
      <div className="fw-sc-producer-section-header">
        <span className="fw-sc-producer-section-title">{t("media")}</span>
        {support.mediaFile && (
          <div className="fw-sc-producer-section-actions">
            <label className="fw-sc-producer-btn">
              {t("addMediaFile")}
              <input
                className="fw-sc-producer-file"
                type="file"
                accept="video/*,audio/*"
                onChange={handleFile}
              />
            </label>
          </div>
        )}
      </div>

      {!support.mediaFile && <p className="fw-sc-producer-notice">{t("mediaFileUnsupported")}</p>}

      {fileFailed && (
        <p className="fw-sc-producer-error" role="alert">
          {t("mediaFileError")}
        </p>
      )}

      {files.length === 0 ? (
        <p className="fw-sc-producer-empty">{t("noMedia")}</p>
      ) : (
        <div className="fw-sc-media-list">
          {files.map((source) => (
            <div key={source.id} className="fw-sc-media-item">
              <span className="fw-sc-overlay-name">{source.label}</span>
              <button
                type="button"
                className="fw-sc-producer-btn"
                onClick={() =>
                  source.media?.playing
                    ? media.pauseMediaSource(source.id)
                    : media.playMediaSource(source.id)
                }
              >
                {source.media?.playing ? t("pauseMedia") : t("playMedia")}
              </button>
              <label className="fw-sc-media-loop">
                <input
                  type="checkbox"
                  checked={source.media?.loop ?? false}
                  onChange={(e) => media.setMediaSourceLoop(source.id, e.target.checked)}
                />
                {t("loopMedia")}
              </label>
            </div>
          ))}
        </div>
      )}

      <p className="fw-sc-producer-notice">{t("mediaTabNotice")}</p>
    </div>
  );
}
