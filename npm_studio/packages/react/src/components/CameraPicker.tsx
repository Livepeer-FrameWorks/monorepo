/**
 * CameraPicker Component
 *
 * The Add Camera action. With more than one camera to choose from it lists the
 * video inputs; the first camera on a single-camera machine starts straight away.
 */

import React, { useCallback, useEffect, useRef, useState } from "react";
import type { CameraChoice, MediaSource } from "@livepeer-frameworks/streamcrafter-core";
import { useStudioTranslate } from "../context/StudioI18nContext";

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

export interface CameraPickerProps {
  sources: MediaSource[];
  disabled?: boolean;
  getChoices: () => Promise<CameraChoice[]>;
  onPick: (deviceId?: string) => void;
}

export function CameraPicker({ sources, disabled = false, getChoices, onPick }: CameraPickerProps) {
  const t = useStudioTranslate();
  const [choices, setChoices] = useState<CameraChoice[] | null>(null);
  const [listFailed, setListFailed] = useState(false);
  const rootRef = useRef<HTMLDivElement>(null);
  const isOpen = choices !== null;

  useEffect(() => {
    if (!isOpen && !listFailed) return;

    const close = () => {
      setChoices(null);
      setListFailed(false);
    };
    const handleOutsideClick = (e: MouseEvent) => {
      if (!rootRef.current?.contains(e.target as Node)) close();
    };
    const handleEscape = (e: KeyboardEvent) => {
      if (e.key === "Escape") close();
    };
    document.addEventListener("mousedown", handleOutsideClick);
    document.addEventListener("keydown", handleEscape);
    return () => {
      document.removeEventListener("mousedown", handleOutsideClick);
      document.removeEventListener("keydown", handleEscape);
    };
  }, [isOpen, listFailed]);

  const handleClick = useCallback(async () => {
    setListFailed(false);
    if (isOpen) {
      setChoices(null);
      return;
    }
    let available: CameraChoice[];
    try {
      available = await getChoices();
    } catch (err) {
      console.error("Failed to list cameras:", err);
      setListFailed(true);
      return;
    }
    const hasCamera = sources.some((source) => source.type === "camera");
    if (!hasCamera && available.length <= 1) {
      onPick(available[0]?.deviceId);
      return;
    }
    setChoices(available);
  }, [getChoices, isOpen, onPick, sources]);

  return (
    <div className="fw-sc-camera-picker" ref={rootRef}>
      <button
        type="button"
        className="fw-sc-action-secondary"
        onClick={handleClick}
        disabled={disabled}
        title={t("addCamera")}
        aria-haspopup="menu"
        aria-expanded={isOpen}
      >
        <CameraIcon size={18} />
      </button>
      {listFailed && (
        <div className="fw-sc-camera-menu" role="alert">
          <div className="fw-sc-camera-menu-note">{t("cameraListError")}</div>
        </div>
      )}
      {choices && (
        <div className="fw-sc-camera-menu" role="menu" aria-label={t("chooseCamera")}>
          <div className="fw-sc-camera-menu-title">{t("chooseCamera")}</div>
          {choices.map((choice) => (
            <button
              key={choice.deviceId}
              type="button"
              role="menuitem"
              className="fw-sc-context-menu-item"
              disabled={choice.inUse}
              onClick={() => {
                setChoices(null);
                onPick(choice.deviceId);
              }}
            >
              <span>{choice.label}</span>
              {choice.inUse && <span className="fw-sc-camera-menu-note">{t("cameraActive")}</span>}
            </button>
          ))}
        </div>
      )}
    </div>
  );
}
