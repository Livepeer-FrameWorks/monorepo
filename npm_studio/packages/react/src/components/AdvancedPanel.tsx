/**
 * AdvancedPanel - Sidebar panel for advanced StreamCrafter settings
 * Matches Player's DevModePanel styling exactly
 *
 * Tabs:
 * - Audio: Master gain, per-source volume, audio processing info
 * - Stats: Connection info, WebRTC stats
 * - Info: WHIP URL, profile, sources
 */

import React, { useState } from "react";
import type {
  IngestState,
  IngestStats,
  QualityProfile,
  MediaSource,
  RendererType,
  RendererStats,
  EncoderOverrides,
} from "@livepeer-frameworks/streamcrafter-core";
import {
  createEncoderConfig,
  getAudioConstraints,
  getEncoderSettings,
} from "@livepeer-frameworks/streamcrafter-core";
import { VolumeSlider } from "./VolumeSlider";
import { useStudioTranslate } from "../context/StudioI18nContext";

// ============================================================================
// Types
// ============================================================================

export interface AudioProcessingSettings {
  echoCancellation: boolean;
  noiseSuppression: boolean;
  autoGainControl: boolean;
}

// Encoder stats interface
export interface EncoderStats {
  video: {
    framesEncoded: number;
    framesPending: number;
    bytesEncoded: number;
    lastFrameTime: number;
  };
  audio: {
    samplesEncoded: number;
    samplesPending: number;
    bytesEncoded: number;
    lastSampleTime: number;
  };
  timestamp: number;
}

export interface AdvancedPanelProps {
  /** Whether the panel is open */
  isOpen: boolean;
  /** Callback when panel should close */
  onClose: () => void;
  /** Current ingest state */
  state: IngestState;
  /** Quality profile */
  qualityProfile: QualityProfile;
  /** WHIP URL */
  whipUrl?: string;
  /** Sources */
  sources: MediaSource[];
  /** Stats */
  stats: IngestStats | null;
  /** Media stream for actual track settings */
  mediaStream?: MediaStream | null;
  /** Master volume (0-2) */
  masterVolume: number;
  /** Callback to set master volume */
  onMasterVolumeChange: (volume: number) => void;
  /** Audio level (0-1) */
  audioLevel: number;
  /** Is audio mixing enabled */
  audioMixingEnabled: boolean;
  /** Error */
  error: string | null;
  /** Audio processing overrides (null = use profile defaults) */
  audioProcessing: AudioProcessingSettings;
  /** Callback to change audio processing settings */
  onAudioProcessingChange: (settings: Partial<AudioProcessingSettings>) => void;
  /** Compositor enabled */
  compositorEnabled?: boolean;
  /** Compositor renderer type */
  compositorRendererType?: RendererType | null;
  /** Compositor stats */
  compositorStats?: RendererStats | null;
  /** Scene count */
  sceneCount?: number;
  /** Layer count */
  layerCount?: number;
  /** Encoder: useWebCodecs setting */
  useWebCodecs?: boolean;
  /** Encoder: is WebCodecs actually active (transform attached) */
  isWebCodecsActive?: boolean;
  /** Encoder: stats from WebCodecs encoder */
  encoderStats?: EncoderStats | null;
  /** Encoder: callback to toggle useWebCodecs */
  onUseWebCodecsChange?: (enabled: boolean) => void;
  /** Whether WebCodecs encoding path is available (requires RTCRtpScriptTransform) */
  isWebCodecsAvailable?: boolean;
  /** Encoder settings overrides (partial values override profile defaults) */
  encoderOverrides?: EncoderOverrides;
  /** Callback to change encoder overrides */
  onEncoderOverridesChange?: (overrides: EncoderOverrides) => void;
}

// ============================================================================
// Helper Functions
// ============================================================================

function formatBitrate(bps: number): string {
  if (bps >= 1_000_000) {
    return `${(bps / 1_000_000).toFixed(1)} Mbps`;
  }
  return `${(bps / 1000).toFixed(0)} kbps`;
}

// ============================================================================
// Toggle Switch Component
// ============================================================================

interface ToggleSwitchProps {
  checked: boolean;
  onChange: (checked: boolean) => void;
  disabled?: boolean;
}

const ToggleSwitch: React.FC<ToggleSwitchProps> = ({ checked, onChange, disabled }) => {
  return (
    <button
      type="button"
      role="switch"
      aria-checked={checked}
      disabled={disabled}
      onClick={() => onChange(!checked)}
      style={{
        position: "relative",
        display: "inline-flex",
        height: "20px",
        width: "36px",
        flexShrink: 0,
        cursor: disabled ? "not-allowed" : "pointer",
        borderRadius: "10px",
        border: "2px solid transparent",
        background: checked ? "hsl(var(--fw-sc-accent))" : "hsl(var(--fw-sc-border))",
        opacity: disabled ? 0.5 : 1,
        transition: "background-color 0.2s",
      }}
    >
      <span
        style={{
          display: "inline-block",
          height: "16px",
          width: "16px",
          borderRadius: "50%",
          background: "white",
          boxShadow: "0 2px 4px rgba(0,0,0,0.3)",
          transition: "transform 0.2s",
          transform: checked ? "translateX(16px)" : "translateX(0)",
        }}
      />
    </button>
  );
};

// ============================================================================
// Setting Select Component
// ============================================================================

interface SettingSelectOption<T> {
  value: T;
  label: string;
}

interface SettingSelectProps<T> {
  value: T;
  options: SettingSelectOption<T>[];
  onChange: (value: T) => void;
  disabled?: boolean;
  isOverridden?: boolean;
}

function SettingSelect<T extends string | number>({
  value,
  options,
  onChange,
  disabled = false,
  isOverridden = false,
}: SettingSelectProps<T>) {
  return (
    <select
      value={String(value)}
      onChange={(e) => {
        const newValue =
          typeof value === "number" ? (Number(e.target.value) as T) : (e.target.value as T);
        onChange(newValue);
      }}
      disabled={disabled}
      style={{
        background: isOverridden
          ? "hsl(var(--fw-sc-accent-secondary) / 0.15)"
          : "hsl(var(--fw-sc-border) / 0.3)",
        border: isOverridden
          ? "1px solid hsl(var(--fw-sc-accent-secondary) / 0.4)"
          : "1px solid hsl(var(--fw-sc-border) / 0.5)",
        borderRadius: "4px",
        color: isOverridden ? "hsl(var(--fw-sc-accent-secondary))" : "hsl(var(--fw-sc-text))",
        padding: "4px 8px",
        fontSize: "12px",
        fontFamily: "inherit",
        cursor: disabled ? "not-allowed" : "pointer",
        opacity: disabled ? 0.5 : 1,
        minWidth: "100px",
        textAlign: "right",
      }}
    >
      {options.map((opt) => (
        <option key={String(opt.value)} value={String(opt.value)}>
          {opt.label}
        </option>
      ))}
    </select>
  );
}

// Preset options for encoder settings
const RESOLUTION_OPTIONS: SettingSelectOption<string>[] = [
  { value: "3840x2160", label: "3840×2160 (4K)" },
  { value: "2560x1440", label: "2560×1440 (1440p)" },
  { value: "1920x1080", label: "1920×1080 (1080p)" },
  { value: "1280x720", label: "1280×720 (720p)" },
  { value: "854x480", label: "854×480 (480p)" },
  { value: "640x360", label: "640×360 (360p)" },
];

const VIDEO_BITRATE_OPTIONS: SettingSelectOption<number>[] = [
  { value: 50_000_000, label: "50 Mbps" },
  { value: 35_000_000, label: "35 Mbps" },
  { value: 25_000_000, label: "25 Mbps" },
  { value: 15_000_000, label: "15 Mbps" },
  { value: 10_000_000, label: "10 Mbps" },
  { value: 8_000_000, label: "8 Mbps" },
  { value: 6_000_000, label: "6 Mbps" },
  { value: 4_000_000, label: "4 Mbps" },
  { value: 2_500_000, label: "2.5 Mbps" },
  { value: 2_000_000, label: "2 Mbps" },
  { value: 1_500_000, label: "1.5 Mbps" },
  { value: 1_000_000, label: "1 Mbps" },
  { value: 500_000, label: "500 kbps" },
];

const FRAMERATE_OPTIONS: SettingSelectOption<number>[] = [
  { value: 120, label: "120 fps" },
  { value: 60, label: "60 fps" },
  { value: 30, label: "30 fps" },
  { value: 24, label: "24 fps" },
  { value: 15, label: "15 fps" },
];

const AUDIO_BITRATE_OPTIONS: SettingSelectOption<number>[] = [
  { value: 320_000, label: "320 kbps" },
  { value: 256_000, label: "256 kbps" },
  { value: 192_000, label: "192 kbps" },
  { value: 128_000, label: "128 kbps" },
  { value: 96_000, label: "96 kbps" },
  { value: 64_000, label: "64 kbps" },
];

// ============================================================================
// Audio Processing Controls Component
// ============================================================================

interface AudioProcessingControlsProps {
  profile: QualityProfile;
  settings: AudioProcessingSettings;
  onChange: (settings: Partial<AudioProcessingSettings>) => void;
}

const AudioProcessingControls: React.FC<AudioProcessingControlsProps> = ({
  profile,
  settings,
  onChange,
}) => {
  const t = useStudioTranslate();
  const profileDefaults = getAudioConstraints(profile);

  const toggles = [
    {
      key: "echoCancellation" as const,
      label: t("echoCancellation"),
      description: t("echoCancellationDesc"),
      defaultValue: profileDefaults.echoCancellation,
    },
    {
      key: "noiseSuppression" as const,
      label: t("noiseSuppression"),
      description: t("noiseSuppressionDesc"),
      defaultValue: profileDefaults.noiseSuppression,
    },
    {
      key: "autoGainControl" as const,
      label: t("autoGainControl"),
      description: t("autoGainControlDesc"),
      defaultValue: profileDefaults.autoGainControl,
    },
  ];

  return (
    <div>
      {toggles.map(({ key, label, description, defaultValue }, idx) => {
        const isModified = settings[key] !== defaultValue;
        return (
          <div
            key={key}
            style={{
              padding: "10px 12px",
              borderTop: idx > 0 ? "1px solid hsl(var(--fw-sc-border) / 0.2)" : undefined,
            }}
          >
            <div style={{ display: "flex", justifyContent: "space-between", alignItems: "center" }}>
              <div style={{ flex: 1, minWidth: 0 }}>
                <div style={{ display: "flex", alignItems: "center", gap: "8px" }}>
                  <span style={{ color: "hsl(var(--fw-sc-text))", fontSize: "12px" }}>{label}</span>
                  {isModified && (
                    <span
                      style={{
                        fontSize: "8px",
                        fontWeight: 600,
                        textTransform: "uppercase",
                        letterSpacing: "0.05em",
                        color: "hsl(var(--fw-sc-warning))",
                        background: "hsl(var(--fw-sc-warning) / 0.2)",
                        padding: "2px 4px",
                      }}
                    >
                      {t("modified")}
                    </span>
                  )}
                </div>
                <div
                  style={{
                    fontSize: "10px",
                    color: "hsl(var(--fw-sc-text-faint))",
                    marginTop: "2px",
                  }}
                >
                  {description}
                </div>
              </div>
              <ToggleSwitch
                checked={settings[key]}
                onChange={(checked) => onChange({ [key]: checked })}
              />
            </div>
          </div>
        );
      })}
      <div
        style={{
          display: "flex",
          justifyContent: "space-between",
          alignItems: "center",
          padding: "8px 12px",
          borderTop: "1px solid hsl(var(--fw-sc-border) / 0.2)",
        }}
      >
        <span style={{ color: "hsl(var(--fw-sc-text-faint))", fontSize: "12px" }}>
          {t("sampleRate")}
        </span>
        <span
          style={{ color: "hsl(var(--fw-sc-text))", fontSize: "12px", fontFamily: "monospace" }}
        >
          {profileDefaults.sampleRate} Hz
        </span>
      </div>
      <div
        style={{
          display: "flex",
          justifyContent: "space-between",
          alignItems: "center",
          padding: "8px 12px",
          borderTop: "1px solid hsl(var(--fw-sc-border) / 0.2)",
        }}
      >
        <span style={{ color: "hsl(var(--fw-sc-text-faint))", fontSize: "12px" }}>
          {t("channels")}
        </span>
        <span
          style={{ color: "hsl(var(--fw-sc-text))", fontSize: "12px", fontFamily: "monospace" }}
        >
          {profileDefaults.channelCount}
        </span>
      </div>
    </div>
  );
};

// ============================================================================
// Main Component
// ============================================================================

const AdvancedPanel: React.FC<AdvancedPanelProps> = ({
  isOpen,
  onClose,
  state,
  qualityProfile,
  whipUrl,
  sources,
  stats,
  mediaStream,
  masterVolume,
  onMasterVolumeChange,
  audioLevel,
  audioMixingEnabled,
  error,
  audioProcessing,
  onAudioProcessingChange,
  compositorEnabled = false,
  compositorRendererType,
  compositorStats,
  sceneCount = 0,
  layerCount = 0,
  useWebCodecs = true,
  isWebCodecsActive = false,
  encoderStats,
  onUseWebCodecsChange,
  isWebCodecsAvailable = true,
  encoderOverrides,
  onEncoderOverridesChange,
}) => {
  const t = useStudioTranslate();
  const [activeTab, setActiveTab] = useState<"audio" | "stats" | "info" | "compositor">("audio");

  const profileEncoderSettings = getEncoderSettings(qualityProfile);
  const effectiveEncoderConfig = createEncoderConfig(
    qualityProfile === "auto" ? "broadcast" : qualityProfile,
    encoderOverrides
  );
  const videoTrackSettings = mediaStream?.getVideoTracks?.()[0]?.getSettings?.();

  if (!isOpen) return null;

  // Styles matching DevModePanel exactly
  const panelStyle: React.CSSProperties = {
    background: "hsl(var(--fw-sc-surface-deep))",
    borderLeft: "1px solid hsl(var(--fw-sc-border) / 0.5)",
    color: "hsl(var(--fw-sc-text-muted))",
    fontSize: "12px",
    fontFamily: "ui-monospace, SFMono-Regular, SF Mono, Menlo, Consolas, monospace",
    width: "280px",
    display: "flex",
    flexDirection: "column",
    height: "100%",
    flexShrink: 0,
    zIndex: 40,
  };

  const tabStyle = (isActive: boolean): React.CSSProperties => ({
    padding: "8px 12px",
    fontSize: "10px",
    textTransform: "uppercase",
    letterSpacing: "0.05em",
    fontWeight: 600,
    transition: "all 0.15s",
    borderRight: "1px solid hsl(var(--fw-sc-border) / 0.3)",
    background: isActive ? "hsl(var(--fw-sc-surface-deep))" : "transparent",
    color: isActive ? "hsl(var(--fw-sc-text))" : "hsl(var(--fw-sc-text-faint))",
    cursor: "pointer",
    border: "none",
  });

  const sectionHeaderStyle: React.CSSProperties = {
    fontSize: "10px",
    color: "hsl(var(--fw-sc-text-faint))",
    textTransform: "uppercase",
    letterSpacing: "0.05em",
    fontWeight: 600,
    marginBottom: "8px",
  };

  const rowStyle: React.CSSProperties = {
    display: "flex",
    justifyContent: "space-between",
    padding: "8px 12px",
    borderTop: "1px solid hsl(var(--fw-sc-border) / 0.2)",
  };

  return (
    <div style={panelStyle} className="fw-dev-mode-panel">
      {/* Header with tabs - slab-header style */}
      <div
        style={{
          display: "flex",
          alignItems: "center",
          borderBottom: "1px solid hsl(var(--fw-sc-border) / 0.3)",
          background: "hsl(var(--fw-sc-surface))",
        }}
      >
        <button
          type="button"
          onClick={() => setActiveTab("audio")}
          style={tabStyle(activeTab === "audio")}
        >
          {t("audio")}
        </button>
        <button
          type="button"
          onClick={() => setActiveTab("stats")}
          style={tabStyle(activeTab === "stats")}
        >
          {t("stats")}
        </button>
        <button
          type="button"
          onClick={() => setActiveTab("info")}
          style={tabStyle(activeTab === "info")}
        >
          {t("info")}
        </button>
        {compositorEnabled && (
          <button
            type="button"
            onClick={() => setActiveTab("compositor")}
            style={tabStyle(activeTab === "compositor")}
          >
            {t("comp")}
          </button>
        )}
        <div style={{ flex: 1 }} />
        <button
          type="button"
          onClick={onClose}
          style={{
            color: "hsl(var(--fw-sc-text-faint))",
            background: "transparent",
            border: "none",
            padding: "8px",
            cursor: "pointer",
            transition: "color 0.15s",
          }}
          aria-label={t("closeAdvancedPanel")}
        >
          <svg
            width="12"
            height="12"
            viewBox="0 0 12 12"
            fill="none"
            stroke="currentColor"
            strokeWidth="1.5"
          >
            <path d="M2 2l8 8M10 2l-8 8" />
          </svg>
        </button>
      </div>

      {/* Audio Tab */}
      {activeTab === "audio" && (
        <div style={{ flex: 1, overflowY: "auto" }}>
          {/* Master Volume */}
          <div
            style={{ padding: "12px", borderBottom: "1px solid hsl(var(--fw-sc-border) / 0.3)" }}
          >
            <div style={sectionHeaderStyle}>{t("masterVolume")}</div>
            <div style={{ display: "flex", alignItems: "center", gap: "12px" }}>
              <VolumeSlider value={masterVolume} onChange={onMasterVolumeChange} min={0} max={2} />
              <span
                style={{
                  fontSize: "14px",
                  fontFamily: "monospace",
                  minWidth: "48px",
                  textAlign: "right",
                  color:
                    masterVolume > 1
                      ? "hsl(var(--fw-sc-warning))"
                      : masterVolume === 1
                        ? "hsl(var(--fw-sc-success))"
                        : "hsl(var(--fw-sc-text))",
                }}
              >
                {Math.round(masterVolume * 100)}%
              </span>
            </div>
            {masterVolume > 1 && (
              <div
                style={{ fontSize: "10px", color: "hsl(var(--fw-sc-warning))", marginTop: "4px" }}
              >
                +{((masterVolume - 1) * 100).toFixed(0)}% boost
              </div>
            )}
          </div>

          {/* Audio Level Meter */}
          <div
            style={{ padding: "12px", borderBottom: "1px solid hsl(var(--fw-sc-border) / 0.3)" }}
          >
            <div style={sectionHeaderStyle}>{t("outputLevel")}</div>
            <div
              style={{
                height: "8px",
                background: "hsl(var(--fw-sc-border) / 0.3)",
                borderRadius: "4px",
                overflow: "hidden",
              }}
            >
              <div
                style={{
                  height: "100%",
                  transition: "all 75ms",
                  width: `${audioLevel * 100}%`,
                  background:
                    audioLevel > 0.9
                      ? "hsl(var(--fw-sc-danger))"
                      : audioLevel > 0.7
                        ? "hsl(var(--fw-sc-warning))"
                        : "hsl(var(--fw-sc-success))",
                }}
              />
            </div>
            <div
              style={{
                display: "flex",
                justifyContent: "space-between",
                fontSize: "10px",
                color: "hsl(var(--fw-sc-text-faint))",
                marginTop: "4px",
              }}
            >
              <span>-60dB</span>
              <span>0dB</span>
            </div>
          </div>

          {/* Audio Mixing Status */}
          <div
            style={{ padding: "12px", borderBottom: "1px solid hsl(var(--fw-sc-border) / 0.3)" }}
          >
            <div style={{ display: "flex", justifyContent: "space-between", alignItems: "center" }}>
              <span style={sectionHeaderStyle}>{t("audioMixing")}</span>
              <span
                style={{
                  fontSize: "12px",
                  fontFamily: "monospace",
                  padding: "2px 6px",
                  background: audioMixingEnabled
                    ? "hsl(var(--fw-sc-success) / 0.2)"
                    : "hsl(var(--fw-sc-border) / 0.3)",
                  color: audioMixingEnabled
                    ? "hsl(var(--fw-sc-success))"
                    : "hsl(var(--fw-sc-text-faint))",
                }}
              >
                {audioMixingEnabled ? t("on") : t("off")}
              </span>
            </div>
            {audioMixingEnabled && (
              <div
                style={{
                  fontSize: "10px",
                  color: "hsl(var(--fw-sc-text-faint))",
                  marginTop: "4px",
                }}
              >
                {t("compressorLimiterActive")}
              </div>
            )}
          </div>

          {/* Audio Processing Controls */}
          <div style={{ borderBottom: "1px solid hsl(var(--fw-sc-border) / 0.3)" }}>
            <div
              style={{
                padding: "8px 12px",
                background: "hsl(var(--fw-sc-surface))",
                display: "flex",
                alignItems: "center",
                justifyContent: "space-between",
              }}
            >
              <span style={sectionHeaderStyle}>{t("processing")}</span>
              <span
                style={{
                  fontSize: "9px",
                  color: "hsl(var(--fw-sc-text-faint))",
                  fontFamily: "monospace",
                }}
              >
                profile: {qualityProfile}
              </span>
            </div>
            <AudioProcessingControls
              profile={qualityProfile}
              settings={audioProcessing}
              onChange={onAudioProcessingChange}
            />
          </div>
        </div>
      )}

      {/* Stats Tab */}
      {activeTab === "stats" && (
        <div style={{ flex: 1, overflowY: "auto" }}>
          {/* Connection State */}
          <div
            style={{ padding: "12px", borderBottom: "1px solid hsl(var(--fw-sc-border) / 0.3)" }}
          >
            <div style={{ ...sectionHeaderStyle, marginBottom: "4px" }}>{t("connection")}</div>
            <div
              style={{
                fontSize: "14px",
                fontWeight: 600,
                color:
                  state === "streaming"
                    ? "hsl(var(--fw-sc-success))"
                    : state === "connecting"
                      ? "hsl(var(--fw-sc-accent))"
                      : state === "error"
                        ? "hsl(var(--fw-sc-danger))"
                        : "hsl(var(--fw-sc-text))",
              }}
            >
              {state.charAt(0).toUpperCase() + state.slice(1)}
            </div>
          </div>

          {/* Stats */}
          {stats && (
            <div>
              <div style={rowStyle}>
                <span style={{ color: "hsl(var(--fw-sc-text-faint))" }}>{t("bitrate")}</span>
                <span style={{ color: "hsl(var(--fw-sc-text))" }}>
                  {formatBitrate(stats.video.bitrate + stats.audio.bitrate)}
                </span>
              </div>
              <div style={rowStyle}>
                <span style={{ color: "hsl(var(--fw-sc-text-faint))" }}>{t("video")}</span>
                <span style={{ color: "hsl(var(--fw-sc-accent))" }}>
                  {formatBitrate(stats.video.bitrate)}
                </span>
              </div>
              <div style={rowStyle}>
                <span style={{ color: "hsl(var(--fw-sc-text-faint))" }}>{t("audio")}</span>
                <span style={{ color: "hsl(var(--fw-sc-accent))" }}>
                  {formatBitrate(stats.audio.bitrate)}
                </span>
              </div>
              <div style={rowStyle}>
                <span style={{ color: "hsl(var(--fw-sc-text-faint))" }}>{t("frameRate")}</span>
                <span style={{ color: "hsl(var(--fw-sc-text))" }}>
                  {stats.video.framesPerSecond.toFixed(0)} fps
                </span>
              </div>
              <div style={rowStyle}>
                <span style={{ color: "hsl(var(--fw-sc-text-faint))" }}>{t("framesEncoded")}</span>
                <span style={{ color: "hsl(var(--fw-sc-text))" }}>{stats.video.framesEncoded}</span>
              </div>
              {(stats.video.packetsLost > 0 || stats.audio.packetsLost > 0) && (
                <div style={rowStyle}>
                  <span style={{ color: "hsl(var(--fw-sc-text-faint))" }}>{t("packetsLost")}</span>
                  <span style={{ color: "hsl(var(--fw-sc-danger))" }}>
                    {stats.video.packetsLost + stats.audio.packetsLost}
                  </span>
                </div>
              )}
              <div style={rowStyle}>
                <span style={{ color: "hsl(var(--fw-sc-text-faint))" }}>{t("rtt")}</span>
                <span
                  style={{
                    color:
                      stats.connection.rtt > 200
                        ? "hsl(var(--fw-sc-warning))"
                        : "hsl(var(--fw-sc-text))",
                  }}
                >
                  {stats.connection.rtt.toFixed(0)} ms
                </span>
              </div>
              <div style={rowStyle}>
                <span style={{ color: "hsl(var(--fw-sc-text-faint))" }}>{t("iceState")}</span>
                <span style={{ color: "hsl(var(--fw-sc-text))", textTransform: "capitalize" }}>
                  {stats.connection.iceState}
                </span>
              </div>
            </div>
          )}

          {!stats && (
            <div
              style={{
                color: "hsl(var(--fw-sc-text-faint))",
                textAlign: "center",
                padding: "24px",
              }}
            >
              {state === "streaming" ? t("waitingForStats") : t("startStreamingForStats")}
            </div>
          )}

          {/* Error */}
          {error && (
            <div
              style={{
                padding: "12px",
                borderTop: "1px solid hsl(var(--fw-sc-danger) / 0.3)",
                background: "hsl(var(--fw-sc-danger) / 0.1)",
              }}
            >
              <div
                style={{
                  ...sectionHeaderStyle,
                  color: "hsl(var(--fw-sc-danger))",
                  marginBottom: "4px",
                }}
              >
                {t("error")}
              </div>
              <div style={{ fontSize: "12px", color: "hsl(var(--fw-sc-danger))" }}>{error}</div>
            </div>
          )}
        </div>
      )}

      {/* Info Tab */}
      {activeTab === "info" && (
        <div style={{ flex: 1, overflowY: "auto" }}>
          {/* Quality Profile */}
          <div
            style={{ padding: "12px", borderBottom: "1px solid hsl(var(--fw-sc-border) / 0.3)" }}
          >
            <div style={{ ...sectionHeaderStyle, marginBottom: "4px" }}>{t("qualityProfile")}</div>
            <div
              style={{
                fontSize: "14px",
                color: "hsl(var(--fw-sc-text))",
                textTransform: "capitalize",
              }}
            >
              {qualityProfile}
            </div>
            <div
              style={{ fontSize: "10px", color: "hsl(var(--fw-sc-text-faint))", marginTop: "4px" }}
            >
              {profileEncoderSettings.video.width}x{profileEncoderSettings.video.height} @{" "}
              {formatBitrate(profileEncoderSettings.video.bitrate)}
            </div>
          </div>

          {/* WHIP URL */}
          <div
            style={{ padding: "12px", borderBottom: "1px solid hsl(var(--fw-sc-border) / 0.3)" }}
          >
            <div style={{ ...sectionHeaderStyle, marginBottom: "4px" }}>{t("whipEndpoint")}</div>
            <div
              style={{
                fontSize: "12px",
                color: "hsl(var(--fw-sc-accent))",
                wordBreak: "break-all",
              }}
            >
              {whipUrl || t("notConfigured")}
            </div>
            {whipUrl && (
              <button
                type="button"
                style={{
                  marginTop: "8px",
                  fontSize: "10px",
                  color: "hsl(var(--fw-sc-text-faint))",
                  background: "transparent",
                  border: "none",
                  cursor: "pointer",
                  padding: 0,
                  transition: "color 0.15s",
                }}
                onClick={() => navigator.clipboard.writeText(whipUrl)}
              >
                {t("copyUrl")}
              </button>
            )}
          </div>

          {/* Encoder Settings */}
          <div style={{ borderBottom: "1px solid hsl(var(--fw-sc-border) / 0.3)" }}>
            <div
              style={{
                padding: "8px 12px",
                background: "hsl(var(--fw-sc-surface))",
                display: "flex",
                justifyContent: "space-between",
                alignItems: "center",
              }}
            >
              <span style={sectionHeaderStyle}>{t("encoder")}</span>
              {(encoderOverrides?.video || encoderOverrides?.audio) && (
                <button
                  type="button"
                  onClick={() => onEncoderOverridesChange?.({})}
                  style={{
                    fontSize: "10px",
                    color: "hsl(var(--fw-sc-accent-secondary))",
                    background: "transparent",
                    border: "none",
                    cursor: "pointer",
                    padding: "2px 6px",
                  }}
                >
                  {t("resetToProfile")}
                </button>
              )}
            </div>
            <div style={rowStyle}>
              <span style={{ color: "hsl(var(--fw-sc-text-faint))" }}>{t("videoCodec")}</span>
              <span style={{ color: "hsl(var(--fw-sc-text))", fontFamily: "monospace" }}>
                {effectiveEncoderConfig.video.codec}
              </span>
            </div>
            <div style={rowStyle}>
              <span style={{ color: "hsl(var(--fw-sc-text-faint))" }}>{t("resolution")}</span>
              <SettingSelect
                value={`${encoderOverrides?.video?.width ?? profileEncoderSettings.video.width}x${encoderOverrides?.video?.height ?? profileEncoderSettings.video.height}`}
                options={RESOLUTION_OPTIONS}
                isOverridden={!!(encoderOverrides?.video?.width || encoderOverrides?.video?.height)}
                disabled={state === "streaming"}
                onChange={(value) => {
                  const [w, h] = value.split("x").map(Number);
                  const isProfileDefault =
                    w === profileEncoderSettings.video.width &&
                    h === profileEncoderSettings.video.height;
                  onEncoderOverridesChange?.({
                    ...encoderOverrides,
                    video: {
                      ...encoderOverrides?.video,
                      width: isProfileDefault ? undefined : w,
                      height: isProfileDefault ? undefined : h,
                    },
                  });
                }}
              />
            </div>
            {videoTrackSettings?.width && videoTrackSettings?.height && (
              <div style={rowStyle}>
                <span style={{ color: "hsl(var(--fw-sc-text-faint))" }}>
                  {t("actualResolution")}
                </span>
                <span style={{ color: "hsl(var(--fw-sc-text))", fontFamily: "monospace" }}>
                  {Math.round(videoTrackSettings.width)}x{Math.round(videoTrackSettings.height)}
                </span>
              </div>
            )}
            <div style={rowStyle}>
              <span style={{ color: "hsl(var(--fw-sc-text-faint))" }}>{t("framerate")}</span>
              <SettingSelect
                value={encoderOverrides?.video?.framerate ?? profileEncoderSettings.video.framerate}
                options={FRAMERATE_OPTIONS}
                isOverridden={!!encoderOverrides?.video?.framerate}
                disabled={state === "streaming"}
                onChange={(value) => {
                  const isProfileDefault = value === profileEncoderSettings.video.framerate;
                  onEncoderOverridesChange?.({
                    ...encoderOverrides,
                    video: {
                      ...encoderOverrides?.video,
                      framerate: isProfileDefault ? undefined : value,
                    },
                  });
                }}
              />
            </div>
            {videoTrackSettings?.frameRate && (
              <div style={rowStyle}>
                <span style={{ color: "hsl(var(--fw-sc-text-faint))" }}>
                  {t("actualFramerate")}
                </span>
                <span style={{ color: "hsl(var(--fw-sc-text))", fontFamily: "monospace" }}>
                  {Math.round(videoTrackSettings.frameRate)} fps
                </span>
              </div>
            )}
            <div style={rowStyle}>
              <span style={{ color: "hsl(var(--fw-sc-text-faint))" }}>{t("videoBitrate")}</span>
              <SettingSelect
                value={encoderOverrides?.video?.bitrate ?? profileEncoderSettings.video.bitrate}
                options={VIDEO_BITRATE_OPTIONS}
                isOverridden={!!encoderOverrides?.video?.bitrate}
                disabled={state === "streaming"}
                onChange={(value) => {
                  const isProfileDefault = value === profileEncoderSettings.video.bitrate;
                  onEncoderOverridesChange?.({
                    ...encoderOverrides,
                    video: {
                      ...encoderOverrides?.video,
                      bitrate: isProfileDefault ? undefined : value,
                    },
                  });
                }}
              />
            </div>
            <div style={rowStyle}>
              <span style={{ color: "hsl(var(--fw-sc-text-faint))" }}>{t("audioCodec")}</span>
              <span style={{ color: "hsl(var(--fw-sc-text))", fontFamily: "monospace" }}>
                {effectiveEncoderConfig.audio.codec}
              </span>
            </div>
            <div style={rowStyle}>
              <span style={{ color: "hsl(var(--fw-sc-text-faint))" }}>{t("audioBitrate")}</span>
              <SettingSelect
                value={encoderOverrides?.audio?.bitrate ?? profileEncoderSettings.audio.bitrate}
                options={AUDIO_BITRATE_OPTIONS}
                isOverridden={!!encoderOverrides?.audio?.bitrate}
                disabled={state === "streaming"}
                onChange={(value) => {
                  const isProfileDefault = value === profileEncoderSettings.audio.bitrate;
                  onEncoderOverridesChange?.({
                    ...encoderOverrides,
                    audio: {
                      ...encoderOverrides?.audio,
                      bitrate: isProfileDefault ? undefined : value,
                    },
                  });
                }}
              />
            </div>
            {state === "streaming" && (
              <div
                style={{
                  padding: "8px 12px",
                  fontSize: "10px",
                  color: "hsl(var(--fw-sc-warning))",
                }}
              >
                {t("settingsLockedWhileStreaming")}
              </div>
            )}
          </div>

          {/* Sources */}
          <div style={{ borderBottom: "1px solid hsl(var(--fw-sc-border) / 0.3)" }}>
            <div style={{ padding: "8px 12px", background: "hsl(var(--fw-sc-surface))" }}>
              <span style={sectionHeaderStyle}>
                {t("sources")} ({sources.length})
              </span>
            </div>
            {sources.length > 0 ? (
              <div>
                {sources.map((source, idx) => (
                  <div
                    key={source.id}
                    style={{
                      padding: "8px 12px",
                      borderTop: idx > 0 ? "1px solid hsl(var(--fw-sc-border) / 0.2)" : undefined,
                    }}
                  >
                    <div style={{ display: "flex", alignItems: "center", gap: "8px" }}>
                      <span
                        style={{
                          fontSize: "10px",
                          fontFamily: "monospace",
                          padding: "2px 6px",
                          textTransform: "uppercase",
                          background:
                            source.type === "camera"
                              ? "hsl(var(--fw-sc-accent) / 0.2)"
                              : source.type === "screen"
                                ? "hsl(var(--fw-sc-success) / 0.2)"
                                : "hsl(var(--fw-sc-warning) / 0.2)",
                          color:
                            source.type === "camera"
                              ? "hsl(var(--fw-sc-accent))"
                              : source.type === "screen"
                                ? "hsl(var(--fw-sc-success))"
                                : "hsl(var(--fw-sc-warning))",
                        }}
                      >
                        {source.type}
                      </span>
                      <span
                        style={{
                          color: "hsl(var(--fw-sc-text))",
                          fontSize: "12px",
                          overflow: "hidden",
                          textOverflow: "ellipsis",
                          whiteSpace: "nowrap",
                        }}
                      >
                        {source.label}
                      </span>
                    </div>
                    <div
                      style={{
                        display: "flex",
                        gap: "12px",
                        marginTop: "4px",
                        fontSize: "10px",
                        color: "hsl(var(--fw-sc-text-faint))",
                      }}
                    >
                      <span>Vol: {Math.round(source.volume * 100)}%</span>
                      {source.muted && (
                        <span style={{ color: "hsl(var(--fw-sc-danger))" }}>Muted</span>
                      )}
                      {!source.active && (
                        <span style={{ color: "hsl(var(--fw-sc-warning))" }}>Inactive</span>
                      )}
                    </div>
                  </div>
                ))}
              </div>
            ) : (
              <div
                style={{
                  padding: "16px 12px",
                  color: "hsl(var(--fw-sc-text-faint))",
                  textAlign: "center",
                  fontSize: "12px",
                }}
              >
                {t("noSourcesAdded")}
              </div>
            )}
          </div>
        </div>
      )}

      {/* Compositor Tab */}
      {activeTab === "compositor" && compositorEnabled && (
        <div style={{ flex: 1, overflowY: "auto" }}>
          {/* Renderer Info */}
          <div
            style={{ padding: "12px", borderBottom: "1px solid hsl(var(--fw-sc-border) / 0.3)" }}
          >
            <div style={sectionHeaderStyle}>{t("renderer")}</div>
            <div
              style={{
                fontSize: "14px",
                fontWeight: 600,
                color:
                  compositorRendererType === "webgpu"
                    ? "hsl(var(--fw-sc-accent-secondary))"
                    : compositorRendererType === "webgl"
                      ? "hsl(var(--fw-sc-accent))"
                      : "hsl(var(--fw-sc-success))",
              }}
            >
              {compositorRendererType === "webgpu" && "WebGPU"}
              {compositorRendererType === "webgl" && "WebGL"}
              {compositorRendererType === "canvas2d" && "Canvas2D"}
              {!compositorRendererType && t("notInitialized")}
            </div>
            <div
              style={{ fontSize: "10px", color: "hsl(var(--fw-sc-text-faint))", marginTop: "4px" }}
            >
              {t("setRendererHint")}
            </div>
          </div>

          {/* Stats */}
          {compositorStats && (
            <div style={{ borderBottom: "1px solid hsl(var(--fw-sc-border) / 0.3)" }}>
              <div style={{ padding: "8px 12px", background: "hsl(var(--fw-sc-surface))" }}>
                <span style={sectionHeaderStyle}>{t("performance")}</span>
              </div>
              <div style={rowStyle}>
                <span style={{ color: "hsl(var(--fw-sc-text-faint))" }}>{t("frameRate")}</span>
                <span style={{ color: "hsl(var(--fw-sc-text))", fontFamily: "monospace" }}>
                  {compositorStats.fps} fps
                </span>
              </div>
              <div style={rowStyle}>
                <span style={{ color: "hsl(var(--fw-sc-text-faint))" }}>{t("frameTime")}</span>
                <span
                  style={{
                    color:
                      compositorStats.frameTimeMs > 16
                        ? "hsl(var(--fw-sc-warning))"
                        : "hsl(var(--fw-sc-text))",
                    fontFamily: "monospace",
                  }}
                >
                  {compositorStats.frameTimeMs.toFixed(2)} ms
                </span>
              </div>
              {compositorStats.gpuMemoryMB !== undefined && (
                <div style={rowStyle}>
                  <span style={{ color: "hsl(var(--fw-sc-text-faint))" }}>{t("gpuMemory")}</span>
                  <span style={{ color: "hsl(var(--fw-sc-text))", fontFamily: "monospace" }}>
                    {compositorStats.gpuMemoryMB.toFixed(1)} MB
                  </span>
                </div>
              )}
            </div>
          )}

          {/* Scenes & Layers */}
          <div style={{ borderBottom: "1px solid hsl(var(--fw-sc-border) / 0.3)" }}>
            <div style={{ padding: "8px 12px", background: "hsl(var(--fw-sc-surface))" }}>
              <span style={sectionHeaderStyle}>{t("composition")}</span>
            </div>
            <div style={rowStyle}>
              <span style={{ color: "hsl(var(--fw-sc-text-faint))" }}>{t("scenes")}</span>
              <span style={{ color: "hsl(var(--fw-sc-text))", fontFamily: "monospace" }}>
                {sceneCount}
              </span>
            </div>
            <div style={rowStyle}>
              <span style={{ color: "hsl(var(--fw-sc-text-faint))" }}>{t("layers")}</span>
              <span style={{ color: "hsl(var(--fw-sc-text))", fontFamily: "monospace" }}>
                {layerCount}
              </span>
            </div>
          </div>

          {/* Encoder Section */}
          <div style={{ borderBottom: "1px solid hsl(var(--fw-sc-border) / 0.3)" }}>
            <div style={{ padding: "8px 12px", background: "hsl(var(--fw-sc-surface))" }}>
              <span style={sectionHeaderStyle}>{t("encoder")}</span>
            </div>
            <div style={rowStyle}>
              <span style={{ color: "hsl(var(--fw-sc-text-faint))" }}>{t("type")}</span>
              <span
                style={{
                  fontSize: "12px",
                  fontFamily: "monospace",
                  padding: "2px 6px",
                  background:
                    useWebCodecs && isWebCodecsAvailable
                      ? "hsl(var(--fw-sc-accent-secondary) / 0.2)"
                      : "hsl(var(--fw-sc-accent) / 0.2)",
                  color:
                    useWebCodecs && isWebCodecsAvailable
                      ? "hsl(var(--fw-sc-accent-secondary))"
                      : "hsl(var(--fw-sc-accent))",
                }}
              >
                {useWebCodecs && isWebCodecsAvailable ? t("webCodecs") : t("browser")}
                {state === "streaming" && (
                  <span style={{ opacity: 0.7, marginLeft: "4px" }}>
                    {isWebCodecsActive ? "(active)" : "(pending)"}
                  </span>
                )}
              </span>
            </div>
            <div style={rowStyle}>
              <span style={{ color: "hsl(var(--fw-sc-text-faint))" }}>{t("useWebCodecs")}</span>
              <ToggleSwitch
                checked={useWebCodecs}
                onChange={(checked) => onUseWebCodecsChange?.(checked)}
                disabled={state === "streaming" || !isWebCodecsAvailable}
              />
            </div>
            {!isWebCodecsAvailable && (
              <div
                style={{ padding: "8px 12px", fontSize: "10px", color: "hsl(var(--fw-sc-danger))" }}
              >
                {t("webCodecsUnsupported")}
              </div>
            )}
            {isWebCodecsAvailable &&
              state === "streaming" &&
              useWebCodecs !== isWebCodecsActive && (
                <div
                  style={{
                    padding: "8px 12px",
                    fontSize: "10px",
                    color: "hsl(var(--fw-sc-warning))",
                  }}
                >
                  {t("changeTakesEffect")}
                </div>
              )}
          </div>

          {/* WebCodecs Encoder Stats */}
          {isWebCodecsActive && encoderStats && (
            <div style={{ borderBottom: "1px solid hsl(var(--fw-sc-border) / 0.3)" }}>
              <div style={{ padding: "8px 12px", background: "hsl(var(--fw-sc-surface))" }}>
                <span style={sectionHeaderStyle}>{t("encoderStats")}</span>
              </div>
              <div style={rowStyle}>
                <span style={{ color: "hsl(var(--fw-sc-text-faint))" }}>{t("videoFrames")}</span>
                <span style={{ color: "hsl(var(--fw-sc-text))", fontFamily: "monospace" }}>
                  {encoderStats.video.framesEncoded}
                </span>
              </div>
              <div style={rowStyle}>
                <span style={{ color: "hsl(var(--fw-sc-text-faint))" }}>{t("videoPending")}</span>
                <span
                  style={{
                    color:
                      encoderStats.video.framesPending > 5
                        ? "hsl(var(--fw-sc-warning))"
                        : "hsl(var(--fw-sc-text))",
                    fontFamily: "monospace",
                  }}
                >
                  {encoderStats.video.framesPending}
                </span>
              </div>
              <div style={rowStyle}>
                <span style={{ color: "hsl(var(--fw-sc-text-faint))" }}>Video Bytes</span>
                <span style={{ color: "hsl(var(--fw-sc-text))", fontFamily: "monospace" }}>
                  {(encoderStats.video.bytesEncoded / 1024 / 1024).toFixed(2)} MB
                </span>
              </div>
              <div style={rowStyle}>
                <span style={{ color: "hsl(var(--fw-sc-text-faint))" }}>Audio Samples</span>
                <span style={{ color: "hsl(var(--fw-sc-text))", fontFamily: "monospace" }}>
                  {encoderStats.audio.samplesEncoded}
                </span>
              </div>
              <div style={rowStyle}>
                <span style={{ color: "hsl(var(--fw-sc-text-faint))" }}>Audio Bytes</span>
                <span style={{ color: "hsl(var(--fw-sc-text))", fontFamily: "monospace" }}>
                  {(encoderStats.audio.bytesEncoded / 1024).toFixed(1)} KB
                </span>
              </div>
            </div>
          )}

          {/* Info */}
          <div style={{ padding: "12px" }}>
            <div
              style={{ fontSize: "10px", color: "hsl(var(--fw-sc-text-faint))", lineHeight: 1.5 }}
            >
              {useWebCodecs && isWebCodecsAvailable
                ? "WebCodecs encoder via RTCRtpScriptTransform provides lower latency and better encoding control."
                : "Browser's built-in MediaStream encoder. Enable WebCodecs toggle for advanced encoding."}
            </div>
          </div>
        </div>
      )}
    </div>
  );
};

export default AdvancedPanel;
export { AdvancedPanel };
