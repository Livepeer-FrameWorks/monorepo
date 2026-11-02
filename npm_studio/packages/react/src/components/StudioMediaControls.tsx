/**
 * StudioMediaControls Component
 *
 * Local media files in the mix, with their playback controls. Files are added
 * from the action bar; volume, mute and removal live with every other source in
 * the mixer. Reads sources and actions from the StreamCrafter context unless
 * they are given.
 */

import React from "react";
import type { MediaSource } from "@livepeer-frameworks/streamcrafter-core";
import { useStudioTranslate } from "../context/StudioI18nContext";
import { useStreamCrafterContextOptional } from "../context/StreamCrafterContext";

export interface StudioMediaActions {
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
  const ctx = useStreamCrafterContextOptional();
  const sources = sourcesProp ?? ctx?.sources ?? [];
  const media = mediaProp ?? ctx ?? null;

  if (!media) return null;
  const files = sources.filter((source) => source.media);

  return (
    <div className={`fw-sc-producer-section ${className}`}>
      <div className="fw-sc-producer-section-header">
        <span className="fw-sc-producer-section-title">{t("media")}</span>
      </div>

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
