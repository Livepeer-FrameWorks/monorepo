import React, { useState, useEffect, useRef } from "react";
import { useStreamCrafterContext } from "../context/StreamCrafterContext";
import { useStudioTranslate } from "../context/StudioI18nContext";
import type {
  QualityProfile,
  StudioTranslationStrings,
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

export interface StudioSettingsProps {
  /** Override quality profile (falls back to context) */
  qualityProfile?: QualityProfile;
  /** Override callback (falls back to context) */
  onProfileChange?: (profile: QualityProfile) => void;
  /** Auto-close dropdown on selection */
  autoClose?: boolean;
}

export const StudioSettings: React.FC<StudioSettingsProps> = ({
  qualityProfile: propProfile,
  onProfileChange,
  autoClose = true,
}) => {
  const ctx = useStreamCrafterContext();
  const t = useStudioTranslate();
  const [isOpen, setIsOpen] = useState(false);
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
        style={{ display: "flex", alignItems: "center", justifyContent: "center" }}
      >
        <SettingsIcon size={16} />
      </button>
      {isOpen && (
        <div
          ref={dropdownRef}
          style={{
            position: "absolute",
            bottom: "100%",
            left: 0,
            marginBottom: "8px",
            width: "192px",
            background: "#1a1b26",
            border: "1px solid rgba(90, 96, 127, 0.3)",
            boxShadow: "0 4px 12px rgba(0, 0, 0, 0.4)",
            borderRadius: "4px",
            overflow: "hidden",
            zIndex: 50,
          }}
        >
          <div style={{ padding: "8px" }}>
            <div
              style={{
                fontSize: "10px",
                color: "#565f89",
                textTransform: "uppercase",
                fontWeight: 600,
                marginBottom: "4px",
                paddingLeft: "4px",
              }}
            >
              {t("quality")}
            </div>
            <div style={{ display: "flex", flexDirection: "column", gap: "2px" }}>
              {QUALITY_PROFILES.map((p) => (
                <button
                  key={p.id}
                  type="button"
                  onClick={() => handleSelect(p.id)}
                  disabled={ctx.isStreaming}
                  style={{
                    width: "100%",
                    padding: "6px 8px",
                    textAlign: "left",
                    fontSize: "12px",
                    borderRadius: "4px",
                    transition: "all 0.15s",
                    border: "none",
                    cursor: ctx.isStreaming ? "not-allowed" : "pointer",
                    opacity: ctx.isStreaming ? 0.5 : 1,
                    background: profile === p.id ? "rgba(122, 162, 247, 0.2)" : "transparent",
                    color: profile === p.id ? "#7aa2f7" : "#a9b1d6",
                  }}
                >
                  <div style={{ fontWeight: 500 }}>{t(p.labelKey)}</div>
                  <div style={{ fontSize: "10px", color: "#565f89" }}>{t(p.descKey)}</div>
                </button>
              ))}
            </div>
          </div>
        </div>
      )}
    </div>
  );
};
