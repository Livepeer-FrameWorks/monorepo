/**
 * MediaFileButton Component
 *
 * The Add Media File action: picks a local video or audio file and plays it into
 * the mix. A browser that cannot capture a media element gets the button
 * disabled with the reason as its title.
 */

import React, { useEffect, useRef, useState } from "react";
import { useStudioTranslate } from "../context/StudioI18nContext";
import { useStudioSupport } from "../hooks/useStudioSupport";

const FilmIcon = ({ size = 18 }: { size?: number }) => (
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
    <rect x="3" y="3" width="18" height="18" rx="2" />
    <path d="M7 3v18M17 3v18M3 7.5h4M3 12h18M3 16.5h4M17 7.5h4M17 16.5h4" />
  </svg>
);

export interface MediaFileButtonProps {
  onAdd: (file: File) => Promise<unknown>;
  disabled?: boolean;
}

export function MediaFileButton({ onAdd, disabled = false }: MediaFileButtonProps) {
  const t = useStudioTranslate();
  const support = useStudioSupport();
  const inputRef = useRef<HTMLInputElement>(null);
  const rootRef = useRef<HTMLDivElement>(null);
  const [failed, setFailed] = useState(false);

  // The error note closes like the camera menu: on a click elsewhere or Escape.
  useEffect(() => {
    if (!failed) return;
    const handleOutsideClick = (e: MouseEvent) => {
      if (!rootRef.current?.contains(e.target as Node)) setFailed(false);
    };
    const handleEscape = (e: KeyboardEvent) => {
      if (e.key === "Escape") setFailed(false);
    };
    document.addEventListener("mousedown", handleOutsideClick);
    document.addEventListener("keydown", handleEscape);
    return () => {
      document.removeEventListener("mousedown", handleOutsideClick);
      document.removeEventListener("keydown", handleEscape);
    };
  }, [failed]);
  const label = support.mediaFile ? t("addMediaFile") : t("mediaFileUnsupported");

  const handleChange = async (e: React.ChangeEvent<HTMLInputElement>) => {
    const file = e.target.files?.[0];
    e.target.value = "";
    if (!file) return;
    setFailed(false);
    try {
      await onAdd(file);
    } catch (err) {
      console.error("Failed to add media file:", err);
      setFailed(true);
    }
  };

  return (
    <div className="fw-sc-camera-picker" ref={rootRef}>
      <button
        type="button"
        className="fw-sc-action-secondary"
        onClick={() => {
          setFailed(false);
          inputRef.current?.click();
        }}
        disabled={disabled || !support.mediaFile}
        title={label}
        aria-label={label}
      >
        <FilmIcon size={18} />
      </button>
      <input
        ref={inputRef}
        className="fw-sc-hidden"
        type="file"
        accept="video/*,audio/*"
        tabIndex={-1}
        aria-hidden="true"
        onChange={handleChange}
      />
      {failed && (
        <div className="fw-sc-camera-menu" role="alert">
          <div className="fw-sc-camera-menu-note">{t("mediaFileError")}</div>
        </div>
      )}
    </div>
  );
}
