/**
 * StudioProducerPanel Component
 *
 * Producer controls for the compositor: scenes with their transition, the layers
 * of the active scene, overlays and media files. Renders nothing until the
 * compositor is running.
 */

import React, { useState } from "react";
import {
  nextStudioSceneName,
  TEXT_OVERLAY_DEFAULTS,
  type MediaSource,
  type StudioOverlay,
} from "@livepeer-frameworks/streamcrafter-core";
import type { UseCompositorReturn } from "../hooks/useCompositor";
import { useStudioTranslate } from "../context/StudioI18nContext";
import { SceneSwitcher } from "./SceneSwitcher";
import { LayerList } from "./LayerList";

export interface StudioMediaActions {
  addMediaFile: (file: File) => Promise<unknown>;
  playMediaSource: (sourceId: string) => void;
  pauseMediaSource: (sourceId: string) => void;
  setMediaSourceLoop: (sourceId: string, loop: boolean) => void;
}

export interface StudioProducerPanelProps {
  compositor: UseCompositorReturn;
  sources: MediaSource[];
  /** Media file actions; the media section appears when they are given. */
  media?: StudioMediaActions;
  className?: string;
}

function takeFile(e: React.ChangeEvent<HTMLInputElement>): File | undefined {
  const file = e.target.files?.[0];
  e.target.value = "";
  return file;
}

function OverlayField({
  label,
  htmlFor,
  children,
}: {
  label: string;
  htmlFor: string;
  children: React.ReactNode;
}) {
  return (
    <div className="fw-sc-overlay-field">
      <label htmlFor={htmlFor}>{label}</label>
      {children}
    </div>
  );
}

function OverlayEditor({
  overlay,
  compositor,
}: {
  overlay: StudioOverlay;
  compositor: UseCompositorReturn;
}) {
  const t = useStudioTranslate();
  const { sourceId, text } = overlay;
  const id = (field: string) => `fw-sc-overlay-${sourceId}-${field}`;
  const percentRange = (
    label: string,
    field: "x" | "y" | "size",
    min: number
  ): React.ReactElement => (
    <OverlayField label={label} htmlFor={id(field)}>
      <input
        id={id(field)}
        type="range"
        min={min}
        max={100}
        value={Math.round(overlay[field] * 100)}
        onChange={(e) =>
          compositor.setOverlayPlacement(sourceId, { [field]: Number(e.target.value) / 100 })
        }
      />
    </OverlayField>
  );

  return (
    <div className="fw-sc-overlay-item">
      <div className="fw-sc-overlay-header">
        <span className="fw-sc-overlay-name">{overlay.label}</span>
        <button
          type="button"
          className="fw-sc-icon-btn fw-sc-icon-btn--destructive"
          title={t("removeOverlay")}
          onClick={() => compositor.removeOverlay(sourceId)}
        >
          ×
        </button>
      </div>

      {text ? (
        <>
          <OverlayField label={t("overlayText")} htmlFor={id("text")}>
            <textarea
              id={id("text")}
              rows={2}
              value={text.text}
              onChange={(e) => compositor.updateTextOverlay(sourceId, { text: e.target.value })}
            />
          </OverlayField>
          <OverlayField label={t("overlayFontSize")} htmlFor={id("font-size")}>
            <input
              id={id("font-size")}
              type="range"
              min={TEXT_OVERLAY_DEFAULTS.minFontSize}
              max={TEXT_OVERLAY_DEFAULTS.maxFontSize}
              value={text.fontSize ?? TEXT_OVERLAY_DEFAULTS.fontSize}
              onChange={(e) =>
                compositor.updateTextOverlay(sourceId, { fontSize: Number(e.target.value) })
              }
            />
          </OverlayField>
          <OverlayField label={t("overlayColor")} htmlFor={id("color")}>
            <input
              id={id("color")}
              type="color"
              value={text.color ?? TEXT_OVERLAY_DEFAULTS.color}
              onChange={(e) => compositor.updateTextOverlay(sourceId, { color: e.target.value })}
            />
          </OverlayField>
        </>
      ) : (
        percentRange(t("overlaySize"), "size", 5)
      )}

      {percentRange(t("overlayPositionX"), "x", 0)}
      {percentRange(t("overlayPositionY"), "y", 0)}
    </div>
  );
}

function OverlaySection({ compositor }: { compositor: UseCompositorReturn }) {
  const t = useStudioTranslate();
  const [imageFailed, setImageFailed] = useState(false);
  const { overlays } = compositor;

  const handleImage = async (e: React.ChangeEvent<HTMLInputElement>) => {
    const file = takeFile(e);
    if (!file) return;

    setImageFailed(false);
    try {
      await compositor.addImageOverlay(file);
    } catch (err) {
      console.error("Failed to add image overlay:", err);
      setImageFailed(true);
    }
  };

  return (
    <div className="fw-sc-producer-section">
      <div className="fw-sc-producer-section-header">
        <span className="fw-sc-producer-section-title">{t("overlays")}</span>
        <div className="fw-sc-producer-section-actions">
          <label className="fw-sc-producer-btn">
            {t("addLogo")}
            <input
              className="fw-sc-producer-file"
              type="file"
              accept="image/*"
              onChange={handleImage}
            />
          </label>
          <button
            type="button"
            className="fw-sc-producer-btn"
            onClick={() => compositor.addTextOverlay({ text: t("overlayDefaultText") })}
          >
            {t("addText")}
          </button>
        </div>
      </div>

      {imageFailed && (
        <p className="fw-sc-producer-error" role="alert">
          {t("overlayImageError")}
        </p>
      )}

      {overlays.length === 0 ? (
        <p className="fw-sc-producer-empty">{t("noOverlays")}</p>
      ) : (
        <div className="fw-sc-overlay-list">
          {overlays.map((overlay) => (
            <OverlayEditor key={overlay.sourceId} overlay={overlay} compositor={compositor} />
          ))}
        </div>
      )}

      {overlays.some((overlay) => overlay.kind === "image") && (
        <p className="fw-sc-producer-notice">{t("overlayLocalNotice")}</p>
      )}
    </div>
  );
}

function MediaSection({ sources, media }: { sources: MediaSource[]; media: StudioMediaActions }) {
  const t = useStudioTranslate();
  const [fileFailed, setFileFailed] = useState(false);
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
    <div className="fw-sc-producer-section">
      <div className="fw-sc-producer-section-header">
        <span className="fw-sc-producer-section-title">{t("media")}</span>
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
      </div>

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

export function StudioProducerPanel({
  compositor,
  sources,
  media,
  className = "",
}: StudioProducerPanelProps) {
  const t = useStudioTranslate();
  const { activeSceneId, activeScene } = compositor;

  if (!compositor.isEnabled || !compositor.isInitialized) return null;

  return (
    <section className={`fw-sc-producer ${className}`} aria-label={t("composition")}>
      <SceneSwitcher
        scenes={compositor.scenes}
        activeSceneId={activeSceneId}
        onSceneSelect={compositor.setActiveScene}
        onSceneCreate={() => compositor.createScene(nextStudioSceneName(compositor.scenes, t))}
        onSceneDelete={compositor.deleteScene}
        onTransitionTo={compositor.transitionTo}
      />
      <LayerList
        layers={activeScene?.layers ?? []}
        sources={sources}
        onVisibilityToggle={(layerId, visible) => {
          if (activeSceneId) compositor.setLayerVisibility(activeSceneId, layerId, visible);
        }}
        onReorder={(layerIds) => {
          if (activeSceneId) compositor.reorderLayers(activeSceneId, layerIds);
        }}
      />
      <OverlaySection compositor={compositor} />
      {media && <MediaSection sources={sources} media={media} />}
    </section>
  );
}
