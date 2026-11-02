/**
 * StudioOverlayControls Component
 *
 * Logo and text overlays of the active scene, with their position, size and
 * text. Reads the compositor from the StreamCrafter context unless one is given.
 */

import React, { useState } from "react";
import { TEXT_OVERLAY_DEFAULTS, type StudioOverlay } from "@livepeer-frameworks/streamcrafter-core";
import type { UseCompositorReturn } from "../hooks/useCompositor";
import { useStudioTranslate } from "../context/StudioI18nContext";
import { useStreamCrafterContextOptional } from "../context/StreamCrafterContext";

export interface StudioOverlayControlsProps {
  /** Defaults to the compositor of the parent StreamCrafter. */
  compositor?: UseCompositorReturn | null;
  className?: string;
}

export function takeFile(e: React.ChangeEvent<HTMLInputElement>): File | undefined {
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

export function StudioOverlayControls({
  compositor: compositorProp,
  className = "",
}: StudioOverlayControlsProps) {
  const t = useStudioTranslate();
  const ctx = useStreamCrafterContextOptional();
  const compositor = compositorProp ?? ctx?.compositor ?? null;
  const [imageFailed, setImageFailed] = useState(false);

  if (!compositor) return null;
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
    <div className={`fw-sc-producer-section ${className}`}>
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
