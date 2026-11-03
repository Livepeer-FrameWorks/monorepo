import React, { useState, useEffect, useMemo, useRef } from "react";
import { useStreamCrafterContext } from "../context/StreamCrafterContext";
import { useStudioTranslate } from "../context/StudioI18nContext";
import {
  encoderSettingRows,
  sendableVideoCodecs,
  withEncoderSetting,
  type QualityProfile,
  type StudioTranslationStrings,
} from "@livepeer-frameworks/streamcrafter-core";

const QUALITY_PROFILES: {
  id: QualityProfile;
  labelKey: keyof StudioTranslationStrings;
  descKey: keyof StudioTranslationStrings;
}[] = [
  { id: "professional", labelKey: "professional", descKey: "professionalDesc" },
  { id: "broadcast", labelKey: "broadcast", descKey: "broadcastDesc" },
  { id: "conference", labelKey: "conference", descKey: "conferenceDesc" },
];

const SettingsIcon = ({ size = 16 }: { size?: number }) => (
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
    <circle cx="12" cy="12" r="3" />
    <path d="M19.4 15a1.65 1.65 0 0 0 .33 1.82l.06.06a2 2 0 0 1 0 2.83 2 2 0 0 1-2.83 0l-.06-.06a1.65 1.65 0 0 0-1.82-.33 1.65 1.65 0 0 0-1 1.51V21a2 2 0 0 1-2 2 2 2 0 0 1-2-2v-.09A1.65 1.65 0 0 0 9 19.4a1.65 1.65 0 0 0-1.82.33l-.06.06a2 2 0 0 1-2.83 0 2 2 0 0 1 0-2.83l.06-.06a1.65 1.65 0 0 0 .33-1.82 1.65 1.65 0 0 0-1.51-1H3a2 2 0 0 1-2-2 2 2 0 0 1 2-2h.09A1.65 1.65 0 0 0 4.6 9a1.65 1.65 0 0 0-.33-1.82l-.06-.06a2 2 0 0 1 0-2.83 2 2 0 0 1 2.83 0l.06.06a1.65 1.65 0 0 0 1.82.33H9a1.65 1.65 0 0 0 1-1.51V3a2 2 0 0 1 2-2 2 2 0 0 1 2 2v.09a1.65 1.65 0 0 0 1 1.51 1.65 1.65 0 0 0 1.82-.33l.06-.06a2 2 0 0 1 2.83 0 2 2 0 0 1 0 2.83l-.06.06a1.65 1.65 0 0 0-.33 1.82V9a1.65 1.65 0 0 0 1.51 1H21a2 2 0 0 1 2 2 2 2 0 0 1-2 2h-.09a1.65 1.65 0 0 0-1.51 1z" />
  </svg>
);

/** Resolution, frame rate, bitrates and video codec; they apply when streaming starts. */
const EncoderSection: React.FC<{ profile: QualityProfile }> = ({ profile }) => {
  const ctx = useStreamCrafterContext();
  const t = useStudioTranslate();
  const codecs = useMemo(() => sendableVideoCodecs(), []);
  const overrides = ctx.encoderOverrides;
  const rows = encoderSettingRows(profile, overrides, codecs, t("codecAuto"));
  const hasOverrides = rows.some((row) => row.overridden);

  return (
    <div className="fw-sc-dropdown-section">
      <div className="fw-sc-encoder-header">
        <div className="fw-sc-dropdown-label">{t("encoder")}</div>
        {hasOverrides && !ctx.isStreaming && (
          <button
            type="button"
            className="fw-sc-encoder-reset"
            onClick={() => ctx.setEncoderOverrides({})}
          >
            {t("resetToProfile")}
          </button>
        )}
      </div>
      {rows.map((row) => (
        <label key={row.setting} className="fw-sc-encoder-row">
          <span>{t(row.labelKey)}</span>
          <select
            className={`fw-sc-select${row.overridden ? " fw-sc-select--overridden" : ""}`}
            value={row.value}
            disabled={ctx.isStreaming}
            onChange={(e) =>
              ctx.setEncoderOverrides(
                withEncoderSetting(profile, overrides, row.setting, e.target.value)
              )
            }
          >
            {row.options.map((option) => (
              <option key={option.value} value={option.value}>
                {option.label}
              </option>
            ))}
          </select>
        </label>
      ))}
      {ctx.isStreaming && (
        <div className="fw-sc-encoder-note">{t("settingsLockedWhileStreaming")}</div>
      )}
    </div>
  );
};

export interface StudioSettingsProps {
  /** Override quality profile (falls back to context) */
  qualityProfile?: QualityProfile;
  /** Override callback (falls back to context) */
  onProfileChange?: (profile: QualityProfile) => void;
  /** Auto-close dropdown on selection */
  autoClose?: boolean;
  /** Open the popup on mount */
  defaultOpen?: boolean;
  /** Extra sections below the quality profiles and encoder settings */
  children?: React.ReactNode;
}

export const StudioSettings: React.FC<StudioSettingsProps> = ({
  qualityProfile: propProfile,
  onProfileChange,
  autoClose = true,
  defaultOpen = false,
  children,
}) => {
  const ctx = useStreamCrafterContext();
  const t = useStudioTranslate();
  const [isOpen, setIsOpen] = useState(defaultOpen);
  const dropdownRef = useRef<HTMLDivElement>(null);
  const buttonRef = useRef<HTMLButtonElement>(null);

  const profile = propProfile ?? ctx.qualityProfile;

  useEffect(() => {
    if (!isOpen) return;
    const handleClickOutside = (e: MouseEvent) => {
      const target = e.target as Node;
      if (
        dropdownRef.current &&
        !dropdownRef.current.contains(target) &&
        buttonRef.current &&
        !buttonRef.current.contains(target)
      ) {
        setIsOpen(false);
      }
    };
    const handleEscape = (e: KeyboardEvent) => {
      if (e.key === "Escape") setIsOpen(false);
    };
    document.addEventListener("mousedown", handleClickOutside);
    document.addEventListener("keydown", handleEscape);
    return () => {
      document.removeEventListener("mousedown", handleClickOutside);
      document.removeEventListener("keydown", handleEscape);
    };
  }, [isOpen]);

  const handleSelect = (id: QualityProfile) => {
    if (ctx.isStreaming) return;
    if (onProfileChange) onProfileChange(id);
    else ctx.setQualityProfile(id);
    if (autoClose) setIsOpen(false);
  };

  return (
    <div style={{ position: "relative" }}>
      <button
        ref={buttonRef}
        type="button"
        className={`fw-sc-action-secondary${isOpen ? " fw-sc-action-secondary--active" : ""}`}
        onClick={() => setIsOpen(!isOpen)}
        title={t("settings")}
        aria-expanded={isOpen}
      >
        <SettingsIcon size={16} />
      </button>
      {isOpen && (
        <div ref={dropdownRef} className="fw-sc-settings-popup">
          <div className="fw-sc-dropdown-section">
            <div className="fw-sc-dropdown-label">{t("quality")}</div>
            <div className="fw-sc-dropdown-options">
              {QUALITY_PROFILES.map((p) => (
                <button
                  key={p.id}
                  type="button"
                  className={`fw-sc-dropdown-option${profile === p.id ? " fw-sc-dropdown-option--active" : ""}`}
                  onClick={() => handleSelect(p.id)}
                  disabled={ctx.isStreaming}
                >
                  <span className="fw-sc-dropdown-option-label">{t(p.labelKey)}</span>
                  <span className="fw-sc-dropdown-option-desc">{t(p.descKey)}</span>
                </button>
              ))}
            </div>
          </div>
          <EncoderSection profile={profile} />
          {children}
        </div>
      )}
    </div>
  );
};
