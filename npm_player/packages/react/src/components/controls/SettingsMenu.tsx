import React, { useState } from "react";
import { usePlayerContextOptional } from "../../context/player";
import { useTranslate } from "../../context/i18n";
import {
  cn,
  SPEED_PRESETS,
  getAvailableLocales,
  getLocaleDisplayName,
} from "@livepeer-frameworks/player-core";
import type { FwLocale } from "@livepeer-frameworks/player-core";
import { SettingsIcon } from "../Icons";

export interface Quality {
  id: string;
  label: string;
  bitrate?: number;
  width?: number;
  height?: number;
  isAuto?: boolean;
  active?: boolean;
}

export interface TextTrack {
  id: string;
  label: string;
  lang?: string;
  active?: boolean;
}

export interface SettingsMenuProps {
  /** Whether the menu is open (controlled). Falls back to internal state. */
  open?: boolean;
  /** Callback when open state changes */
  onOpenChange?: (open: boolean) => void;

  /** Playback speed (e.g. 1, 1.5, 2). Falls back to context. */
  playbackRate?: number;
  /** Callback when speed changes */
  onSpeedChange?: (rate: number) => void;
  /** Whether playback rate control is supported */
  supportsSpeed?: boolean;

  /** Available quality levels. Falls back to context. */
  qualities?: Quality[];
  /** Currently active quality id or "auto" */
  activeQuality?: string;
  /** Callback when quality is selected */
  onSelectQuality?: (id: string) => void;

  /** Available text tracks. Falls back to context. */
  textTracks?: TextTrack[];
  /** Currently active caption id or "none" */
  activeCaption?: string;
  /** Callback when caption is selected */
  onSelectCaption?: (id: string) => void;

  /** Playback mode for live content */
  playbackMode?: "auto" | "low-latency" | "quality";
  /** Callback when mode changes */
  onModeChange?: (mode: "auto" | "low-latency" | "quality") => void;
  /** Whether to show the mode selector */
  showModeSelector?: boolean;

  activeLocale?: FwLocale;
  onLocaleChange?: (locale: FwLocale) => void;

  /** Icon size */
  size?: number;
  /** Custom class name for the button */
  className?: string;
}

export const SettingsMenu: React.FC<SettingsMenuProps> = ({
  open: propOpen,
  onOpenChange,
  playbackRate: propRate,
  onSpeedChange,
  supportsSpeed,
  qualities: propQualities,
  activeQuality: propActiveQuality,
  onSelectQuality,
  textTracks: propTextTracks,
  activeCaption: propActiveCaption,
  onSelectCaption,
  playbackMode,
  onModeChange,
  showModeSelector,
  activeLocale,
  onLocaleChange,
  size = 16,
  className,
}) => {
  const ctx = usePlayerContextOptional();
  const t = useTranslate();
  const [internalOpen, setInternalOpen] = useState(false);

  const isOpen = propOpen ?? internalOpen;
  const setOpen = (v: boolean) => {
    onOpenChange?.(v);
    setInternalOpen(v);
  };

  const rate = propRate ?? 1;
  const qualities: Quality[] = propQualities ?? (ctx?.getQualities?.() as Quality[]) ?? [];
  const qualityValue = propActiveQuality ?? qualities.find((q) => q.active)?.id ?? "auto";
  const textTracks = propTextTracks ?? [];
  const captionValue = propActiveCaption ?? textTracks.find((tt) => tt.active)?.id ?? "none";

  const handleSpeedChange = (r: number) => {
    onSpeedChange?.(r);
    setOpen(false);
  };

  const handleQualityChange = (id: string) => {
    if (onSelectQuality) onSelectQuality(id);
    else ctx?.selectQuality?.(id);
    setOpen(false);
  };

  const handleCaptionChange = (id: string) => {
    onSelectCaption?.(id);
    setOpen(false);
  };

  return (
    <div className="fw-control-group relative">
      <button
        type="button"
        className={cn(className ?? "fw-btn-flush group", isOpen && "fw-btn-flush--active")}
        aria-label={t("settings")}
        title={t("settings")}
        onClick={() => setOpen(!isOpen)}
      >
        <SettingsIcon size={size} className="transition-transform group-hover:rotate-90" />
      </button>

      {isOpen && (
        <div
          className="fw-settings-menu"
          role="menu"
          aria-label={t("settings")}
          onKeyDown={(e) => {
            if (e.key === "Escape") {
              setOpen(false);
              e.preventDefault();
              return;
            }
            if (e.key === "ArrowDown" || e.key === "ArrowUp") {
              e.preventDefault();
              const items = (e.currentTarget as HTMLElement).querySelectorAll<HTMLButtonElement>(
                "button"
              );
              if (!items.length) return;
              const current = Array.from(items).indexOf(
                document.activeElement as HTMLButtonElement
              );
              const next =
                e.key === "ArrowDown"
                  ? (current + 1) % items.length
                  : (current - 1 + items.length) % items.length;
              items[next]?.focus();
            }
          }}
        >
          {showModeSelector && onModeChange && (
            <div className="fw-settings-section">
              <div className="fw-settings-label">{t("mode")}</div>
              <div className="fw-settings-options">
                {(["auto", "low-latency", "quality"] as const).map((m) => (
                  <button
                    key={m}
                    className={cn(
                      "fw-settings-btn",
                      playbackMode === m && "fw-settings-btn--active"
                    )}
                    onClick={() => {
                      onModeChange(m);
                      setOpen(false);
                    }}
                  >
                    {m === "low-latency" ? t("fast") : m === "quality" ? t("stable") : t("auto")}
                  </button>
                ))}
              </div>
            </div>
          )}

          {(supportsSpeed ?? true) && (
            <div className="fw-settings-section">
              <div className="fw-settings-label">{t("speed")}</div>
              <div className="fw-settings-options fw-settings-options--wrap">
                {SPEED_PRESETS.map((r) => (
                  <button
                    key={r}
                    className={cn("fw-settings-btn", rate === r && "fw-settings-btn--active")}
                    onClick={() => handleSpeedChange(r)}
                  >
                    {r}x
                  </button>
                ))}
              </div>
            </div>
          )}

          {qualities.length > 0 && (
            <div className="fw-settings-section">
              <div className="fw-settings-label">{t("quality")}</div>
              <div className="fw-settings-list">
                <button
                  className={cn(
                    "fw-settings-list-item",
                    qualityValue === "auto" && "fw-settings-list-item--active"
                  )}
                  onClick={() => handleQualityChange("auto")}
                >
                  {t("auto")}
                </button>
                {qualities.map((q) => (
                  <button
                    key={q.id}
                    className={cn(
                      "fw-settings-list-item",
                      qualityValue === q.id && "fw-settings-list-item--active"
                    )}
                    onClick={() => handleQualityChange(q.id)}
                  >
                    {q.label}
                  </button>
                ))}
              </div>
            </div>
          )}

          {textTracks.length > 0 && (
            <div className="fw-settings-section">
              <div className="fw-settings-label">{t("captions")}</div>
              <div className="fw-settings-list">
                <button
                  className={cn(
                    "fw-settings-list-item",
                    captionValue === "none" && "fw-settings-list-item--active"
                  )}
                  onClick={() => handleCaptionChange("none")}
                >
                  {t("captionsOff")}
                </button>
                {textTracks.map((tt) => (
                  <button
                    key={tt.id}
                    className={cn(
                      "fw-settings-list-item",
                      captionValue === tt.id && "fw-settings-list-item--active"
                    )}
                    onClick={() => handleCaptionChange(tt.id)}
                  >
                    {tt.label || tt.id}
                  </button>
                ))}
              </div>
            </div>
          )}

          {onLocaleChange && (
            <div className="fw-settings-section">
              <div className="fw-settings-label">{t("language")}</div>
              <div className="fw-settings-list">
                {getAvailableLocales().map((loc) => (
                  <button
                    key={loc}
                    className={cn(
                      "fw-settings-list-item",
                      activeLocale === loc && "fw-settings-list-item--active"
                    )}
                    onClick={() => {
                      onLocaleChange(loc);
                    }}
                  >
                    {getLocaleDisplayName(loc)}
                  </button>
                ))}
              </div>
            </div>
          )}
        </div>
      )}
    </div>
  );
};
