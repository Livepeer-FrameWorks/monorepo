/**
 * AdvancedPanel - Sidebar panel for advanced StreamCrafter settings
 * Matches Player's DevModePanel styling exactly
 *
 * Tabs:
 * - Audio: Master gain, per-source volume, audio processing info
 * - Stats: Connection info, WebRTC stats
 * - Info: WHIP URL, profile, sources
 */

import React, { useState } from 'react';
import type {
  IngestState,
  IngestStats,
  QualityProfile,
  MediaSource,
  RendererType,
  RendererStats,
  EncoderOverrides,
} from '@livepeer-frameworks/streamcrafter-core';
import { createEncoderConfig, getAudioConstraints, getEncoderSettings } from '@livepeer-frameworks/streamcrafter-core';
import { VolumeSlider } from './VolumeSlider';

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
        position: 'relative',
        display: 'inline-flex',
        height: '20px',
        width: '36px',
        flexShrink: 0,
        cursor: disabled ? 'not-allowed' : 'pointer',
        borderRadius: '10px',
        border: '2px solid transparent',
        background: checked ? '#7aa2f7' : '#414868',
        opacity: disabled ? 0.5 : 1,
        transition: 'background-color 0.2s',
      }}
    >
      <span
        style={{
          display: 'inline-block',
          height: '16px',
          width: '16px',
          borderRadius: '50%',
          background: 'white',
          boxShadow: '0 2px 4px rgba(0,0,0,0.3)',
          transition: 'transform 0.2s',
          transform: checked ? 'translateX(16px)' : 'translateX(0)',
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
        const newValue = typeof value === 'number'
          ? Number(e.target.value) as T
          : e.target.value as T;
        onChange(newValue);
      }}
      disabled={disabled}
      style={{
        background: isOverridden ? 'rgba(187, 154, 247, 0.15)' : 'rgba(65, 72, 104, 0.3)',
        border: isOverridden ? '1px solid rgba(187, 154, 247, 0.4)' : '1px solid rgba(65, 72, 104, 0.5)',
        borderRadius: '4px',
        color: isOverridden ? '#bb9af7' : '#c0caf5',
        padding: '4px 8px',
        fontSize: '12px',
        fontFamily: 'inherit',
        cursor: disabled ? 'not-allowed' : 'pointer',
        opacity: disabled ? 0.5 : 1,
        minWidth: '100px',
        textAlign: 'right',
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
  { value: '3840x2160', label: '3840×2160 (4K)' },
  { value: '2560x1440', label: '2560×1440 (1440p)' },
  { value: '1920x1080', label: '1920×1080 (1080p)' },
  { value: '1280x720', label: '1280×720 (720p)' },
  { value: '854x480', label: '854×480 (480p)' },
  { value: '640x360', label: '640×360 (360p)' },
];

const VIDEO_BITRATE_OPTIONS: SettingSelectOption<number>[] = [
  { value: 50_000_000, label: '50 Mbps' },
  { value: 35_000_000, label: '35 Mbps' },
  { value: 25_000_000, label: '25 Mbps' },
  { value: 15_000_000, label: '15 Mbps' },
  { value: 10_000_000, label: '10 Mbps' },
  { value: 8_000_000, label: '8 Mbps' },
  { value: 6_000_000, label: '6 Mbps' },
  { value: 4_000_000, label: '4 Mbps' },
  { value: 2_500_000, label: '2.5 Mbps' },
  { value: 2_000_000, label: '2 Mbps' },
  { value: 1_500_000, label: '1.5 Mbps' },
  { value: 1_000_000, label: '1 Mbps' },
  { value: 500_000, label: '500 kbps' },
];

const FRAMERATE_OPTIONS: SettingSelectOption<number>[] = [
  { value: 120, label: '120 fps' },
  { value: 60, label: '60 fps' },
  { value: 30, label: '30 fps' },
  { value: 24, label: '24 fps' },
  { value: 15, label: '15 fps' },
];

const AUDIO_BITRATE_OPTIONS: SettingSelectOption<number>[] = [
  { value: 320_000, label: '320 kbps' },
  { value: 256_000, label: '256 kbps' },
  { value: 192_000, label: '192 kbps' },
  { value: 128_000, label: '128 kbps' },
  { value: 96_000, label: '96 kbps' },
  { value: 64_000, label: '64 kbps' },
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
  const profileDefaults = getAudioConstraints(profile);

  const toggles = [
    {
      key: 'echoCancellation' as const,
      label: 'Echo Cancellation',
      description: 'Reduce echo from speakers',
      defaultValue: profileDefaults.echoCancellation,
    },
    {
      key: 'noiseSuppression' as const,
      label: 'Noise Suppression',
      description: 'Filter background noise',
      defaultValue: profileDefaults.noiseSuppression,
    },
    {
      key: 'autoGainControl' as const,
      label: 'Auto Gain Control',
      description: 'Normalize audio levels',
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
              padding: '10px 12px',
              borderTop: idx > 0 ? '1px solid rgba(65, 72, 104, 0.2)' : undefined,
            }}
          >
            <div style={{ display: 'flex', justifyContent: 'space-between', alignItems: 'center' }}>
              <div style={{ flex: 1, minWidth: 0 }}>
                <div style={{ display: 'flex', alignItems: 'center', gap: '8px' }}>
                  <span style={{ color: '#c0caf5', fontSize: '12px' }}>{label}</span>
                  {isModified && (
                    <span
                      style={{
                        fontSize: '8px',
                        fontWeight: 600,
                        textTransform: 'uppercase',
                        letterSpacing: '0.05em',
                        color: '#e0af68',
                        background: 'rgba(224, 175, 104, 0.2)',
                        padding: '2px 4px',
                      }}
                    >
                      Modified
                    </span>
                  )}
                </div>
                <div style={{ fontSize: '10px', color: '#565f89', marginTop: '2px' }}>
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
          display: 'flex',
          justifyContent: 'space-between',
          alignItems: 'center',
          padding: '8px 12px',
          borderTop: '1px solid rgba(65, 72, 104, 0.2)',
        }}
      >
        <span style={{ color: '#565f89', fontSize: '12px' }}>Sample Rate</span>
        <span style={{ color: '#c0caf5', fontSize: '12px', fontFamily: 'monospace' }}>
          {profileDefaults.sampleRate} Hz
        </span>
      </div>
      <div
        style={{
          display: 'flex',
          justifyContent: 'space-between',
          alignItems: 'center',
          padding: '8px 12px',
          borderTop: '1px solid rgba(65, 72, 104, 0.2)',
        }}
      >
        <span style={{ color: '#565f89', fontSize: '12px' }}>Channels</span>
        <span style={{ color: '#c0caf5', fontSize: '12px', fontFamily: 'monospace' }}>
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
  useWebCodecs = false,
  isWebCodecsActive = false,
  encoderStats,
  onUseWebCodecsChange,
  isWebCodecsAvailable = true,
  encoderOverrides,
  onEncoderOverridesChange,
}) => {
  const [activeTab, setActiveTab] = useState<'audio' | 'stats' | 'info' | 'compositor'>('audio');

  const profileEncoderSettings = getEncoderSettings(qualityProfile);
  const effectiveEncoderConfig = createEncoderConfig(
    qualityProfile === 'auto' ? 'broadcast' : qualityProfile,
    encoderOverrides
  );
  const videoTrackSettings = mediaStream?.getVideoTracks?.()[0]?.getSettings?.();

  if (!isOpen) return null;

  // Styles matching DevModePanel exactly
  const panelStyle: React.CSSProperties = {
    background: '#1a1b26',
    borderLeft: '1px solid rgba(65, 72, 104, 0.5)',
    color: '#a9b1d6',
    fontSize: '12px',
    fontFamily: 'ui-monospace, SFMono-Regular, SF Mono, Menlo, Consolas, monospace',
    width: '280px',
    display: 'flex',
    flexDirection: 'column',
    height: '100%',
    flexShrink: 0,
    zIndex: 40,
  };

  const tabStyle = (isActive: boolean): React.CSSProperties => ({
    padding: '8px 12px',
    fontSize: '10px',
    textTransform: 'uppercase',
    letterSpacing: '0.05em',
    fontWeight: 600,
    transition: 'all 0.15s',
    borderRight: '1px solid rgba(65, 72, 104, 0.3)',
    background: isActive ? '#1a1b26' : 'transparent',
    color: isActive ? '#c0caf5' : '#565f89',
    cursor: 'pointer',
    border: 'none',
  });

  const sectionHeaderStyle: React.CSSProperties = {
    fontSize: '10px',
    color: '#565f89',
    textTransform: 'uppercase',
    letterSpacing: '0.05em',
    fontWeight: 600,
    marginBottom: '8px',
  };

  const rowStyle: React.CSSProperties = {
    display: 'flex',
    justifyContent: 'space-between',
    padding: '8px 12px',
    borderTop: '1px solid rgba(65, 72, 104, 0.2)',
  };

  return (
    <div style={panelStyle} className="fw-dev-mode-panel">
      {/* Header with tabs - slab-header style */}
      <div
        style={{
          display: 'flex',
          alignItems: 'center',
          borderBottom: '1px solid rgba(65, 72, 104, 0.3)',
          background: '#16161e',
        }}
      >
        <button type="button" onClick={() => setActiveTab('audio')} style={tabStyle(activeTab === 'audio')}>
          Audio
        </button>
        <button type="button" onClick={() => setActiveTab('stats')} style={tabStyle(activeTab === 'stats')}>
          Stats
        </button>
        <button type="button" onClick={() => setActiveTab('info')} style={tabStyle(activeTab === 'info')}>
          Info
        </button>
        {compositorEnabled && (
          <button type="button" onClick={() => setActiveTab('compositor')} style={tabStyle(activeTab === 'compositor')}>
            Comp
          </button>
        )}
        <div style={{ flex: 1 }} />
        <button
          type="button"
          onClick={onClose}
          style={{
            color: '#565f89',
            background: 'transparent',
            border: 'none',
            padding: '8px',
            cursor: 'pointer',
            transition: 'color 0.15s',
          }}
          aria-label="Close advanced panel"
        >
          <svg width="12" height="12" viewBox="0 0 12 12" fill="none" stroke="currentColor" strokeWidth="1.5">
            <path d="M2 2l8 8M10 2l-8 8" />
          </svg>
        </button>
      </div>

      {/* Audio Tab */}
      {activeTab === 'audio' && (
        <div style={{ flex: 1, overflowY: 'auto' }}>
          {/* Master Volume */}
          <div style={{ padding: '12px', borderBottom: '1px solid rgba(65, 72, 104, 0.3)' }}>
            <div style={sectionHeaderStyle}>Master Volume</div>
            <div style={{ display: 'flex', alignItems: 'center', gap: '12px' }}>
              <VolumeSlider
                value={masterVolume}
                onChange={onMasterVolumeChange}
                min={0}
                max={2}
              />
              <span
                style={{
                  fontSize: '14px',
                  fontFamily: 'monospace',
                  minWidth: '48px',
                  textAlign: 'right',
                  color: masterVolume > 1 ? '#e0af68' : masterVolume === 1 ? '#9ece6a' : '#c0caf5',
                }}
              >
                {Math.round(masterVolume * 100)}%
              </span>
            </div>
            {masterVolume > 1 && (
              <div style={{ fontSize: '10px', color: '#e0af68', marginTop: '4px' }}>
                +{((masterVolume - 1) * 100).toFixed(0)}% boost
              </div>
            )}
          </div>

          {/* Audio Level Meter */}
          <div style={{ padding: '12px', borderBottom: '1px solid rgba(65, 72, 104, 0.3)' }}>
            <div style={sectionHeaderStyle}>Output Level</div>
            <div
              style={{
                height: '8px',
                background: 'rgba(65, 72, 104, 0.3)',
                borderRadius: '4px',
                overflow: 'hidden',
              }}
            >
              <div
                style={{
                  height: '100%',
                  transition: 'all 75ms',
                  width: `${audioLevel * 100}%`,
                  background:
                    audioLevel > 0.9 ? '#f7768e' : audioLevel > 0.7 ? '#e0af68' : '#9ece6a',
                }}
              />
            </div>
            <div
              style={{
                display: 'flex',
                justifyContent: 'space-between',
                fontSize: '10px',
                color: '#565f89',
                marginTop: '4px',
              }}
            >
              <span>-60dB</span>
              <span>0dB</span>
            </div>
          </div>

          {/* Audio Mixing Status */}
          <div style={{ padding: '12px', borderBottom: '1px solid rgba(65, 72, 104, 0.3)' }}>
            <div style={{ display: 'flex', justifyContent: 'space-between', alignItems: 'center' }}>
              <span style={sectionHeaderStyle}>Audio Mixing</span>
              <span
                style={{
                  fontSize: '12px',
                  fontFamily: 'monospace',
                  padding: '2px 6px',
                  background: audioMixingEnabled ? 'rgba(158, 206, 106, 0.2)' : 'rgba(65, 72, 104, 0.3)',
                  color: audioMixingEnabled ? '#9ece6a' : '#565f89',
                }}
              >
                {audioMixingEnabled ? 'ON' : 'OFF'}
              </span>
            </div>
            {audioMixingEnabled && (
              <div style={{ fontSize: '10px', color: '#565f89', marginTop: '4px' }}>
                Compressor + Limiter active
              </div>
            )}
          </div>

          {/* Audio Processing Controls */}
          <div style={{ borderBottom: '1px solid rgba(65, 72, 104, 0.3)' }}>
            <div
              style={{
                padding: '8px 12px',
                background: '#16161e',
                display: 'flex',
                alignItems: 'center',
                justifyContent: 'space-between',
              }}
            >
              <span style={sectionHeaderStyle}>Processing</span>
              <span style={{ fontSize: '9px', color: '#565f89', fontFamily: 'monospace' }}>
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
      {activeTab === 'stats' && (
        <div style={{ flex: 1, overflowY: 'auto' }}>
          {/* Connection State */}
          <div style={{ padding: '12px', borderBottom: '1px solid rgba(65, 72, 104, 0.3)' }}>
            <div style={{ ...sectionHeaderStyle, marginBottom: '4px' }}>Connection</div>
            <div
              style={{
                fontSize: '14px',
                fontWeight: 600,
                color:
                  state === 'streaming'
                    ? '#9ece6a'
                    : state === 'connecting'
                    ? '#7aa2f7'
                    : state === 'error'
                    ? '#f7768e'
                    : '#c0caf5',
              }}
            >
              {state.charAt(0).toUpperCase() + state.slice(1)}
            </div>
          </div>

          {/* Stats */}
          {stats && (
            <div>
              <div style={rowStyle}>
                <span style={{ color: '#565f89' }}>Bitrate</span>
                <span style={{ color: '#c0caf5' }}>
                  {formatBitrate(stats.video.bitrate + stats.audio.bitrate)}
                </span>
              </div>
              <div style={rowStyle}>
                <span style={{ color: '#565f89' }}>Video</span>
                <span style={{ color: '#7aa2f7' }}>{formatBitrate(stats.video.bitrate)}</span>
              </div>
              <div style={rowStyle}>
                <span style={{ color: '#565f89' }}>Audio</span>
                <span style={{ color: '#7aa2f7' }}>{formatBitrate(stats.audio.bitrate)}</span>
              </div>
              <div style={rowStyle}>
                <span style={{ color: '#565f89' }}>Frame Rate</span>
                <span style={{ color: '#c0caf5' }}>{stats.video.framesPerSecond.toFixed(0)} fps</span>
              </div>
              <div style={rowStyle}>
                <span style={{ color: '#565f89' }}>Frames Encoded</span>
                <span style={{ color: '#c0caf5' }}>{stats.video.framesEncoded}</span>
              </div>
              {(stats.video.packetsLost > 0 || stats.audio.packetsLost > 0) && (
                <div style={rowStyle}>
                  <span style={{ color: '#565f89' }}>Packets Lost</span>
                  <span style={{ color: '#f7768e' }}>{stats.video.packetsLost + stats.audio.packetsLost}</span>
                </div>
              )}
              <div style={rowStyle}>
                <span style={{ color: '#565f89' }}>RTT</span>
                <span style={{ color: stats.connection.rtt > 200 ? '#e0af68' : '#c0caf5' }}>
                  {stats.connection.rtt.toFixed(0)} ms
                </span>
              </div>
              <div style={rowStyle}>
                <span style={{ color: '#565f89' }}>ICE State</span>
                <span style={{ color: '#c0caf5', textTransform: 'capitalize' }}>
                  {stats.connection.iceState}
                </span>
              </div>
            </div>
          )}

          {!stats && (
            <div style={{ color: '#565f89', textAlign: 'center', padding: '24px' }}>
              {state === 'streaming' ? 'Waiting for stats...' : 'Start streaming to see stats'}
            </div>
          )}

          {/* Error */}
          {error && (
            <div
              style={{
                padding: '12px',
                borderTop: '1px solid rgba(247, 118, 142, 0.3)',
                background: 'rgba(247, 118, 142, 0.1)',
              }}
            >
              <div style={{ ...sectionHeaderStyle, color: '#f7768e', marginBottom: '4px' }}>Error</div>
              <div style={{ fontSize: '12px', color: '#f7768e' }}>{error}</div>
            </div>
          )}
        </div>
      )}

      {/* Info Tab */}
      {activeTab === 'info' && (
        <div style={{ flex: 1, overflowY: 'auto' }}>
          {/* Quality Profile */}
          <div style={{ padding: '12px', borderBottom: '1px solid rgba(65, 72, 104, 0.3)' }}>
            <div style={{ ...sectionHeaderStyle, marginBottom: '4px' }}>Quality Profile</div>
            <div style={{ fontSize: '14px', color: '#c0caf5', textTransform: 'capitalize' }}>
              {qualityProfile}
            </div>
            <div style={{ fontSize: '10px', color: '#565f89', marginTop: '4px' }}>
              {profileEncoderSettings.video.width}x{profileEncoderSettings.video.height} @{' '}
              {formatBitrate(profileEncoderSettings.video.bitrate)}
            </div>
          </div>

          {/* WHIP URL */}
          <div style={{ padding: '12px', borderBottom: '1px solid rgba(65, 72, 104, 0.3)' }}>
            <div style={{ ...sectionHeaderStyle, marginBottom: '4px' }}>WHIP Endpoint</div>
            <div
              style={{
                fontSize: '12px',
                color: '#7aa2f7',
                wordBreak: 'break-all',
              }}
            >
              {whipUrl || 'Not configured'}
            </div>
            {whipUrl && (
              <button
                type="button"
                style={{
                  marginTop: '8px',
                  fontSize: '10px',
                  color: '#565f89',
                  background: 'transparent',
                  border: 'none',
                  cursor: 'pointer',
                  padding: 0,
                  transition: 'color 0.15s',
                }}
                onClick={() => navigator.clipboard.writeText(whipUrl)}
              >
                Copy URL
              </button>
            )}
          </div>

          {/* Encoder Settings */}
          <div style={{ borderBottom: '1px solid rgba(65, 72, 104, 0.3)' }}>
            <div style={{ padding: '8px 12px', background: '#16161e', display: 'flex', justifyContent: 'space-between', alignItems: 'center' }}>
              <span style={sectionHeaderStyle}>Encoder</span>
              {(encoderOverrides?.video || encoderOverrides?.audio) && (
                <button
                  type="button"
                  onClick={() => onEncoderOverridesChange?.({})}
                  style={{
                    fontSize: '10px',
                    color: '#bb9af7',
                    background: 'transparent',
                    border: 'none',
                    cursor: 'pointer',
                    padding: '2px 6px',
                  }}
                >
                  Reset to Profile
                </button>
              )}
            </div>
            <div style={rowStyle}>
              <span style={{ color: '#565f89' }}>Video Codec</span>
              <span style={{ color: '#c0caf5', fontFamily: 'monospace' }}>
                {effectiveEncoderConfig.video.codec}
              </span>
            </div>
            <div style={rowStyle}>
              <span style={{ color: '#565f89' }}>Resolution</span>
              <SettingSelect
                value={`${encoderOverrides?.video?.width ?? profileEncoderSettings.video.width}x${encoderOverrides?.video?.height ?? profileEncoderSettings.video.height}`}
                options={RESOLUTION_OPTIONS}
                isOverridden={!!(encoderOverrides?.video?.width || encoderOverrides?.video?.height)}
                disabled={state === 'streaming'}
                onChange={(value) => {
                  const [w, h] = value.split('x').map(Number);
                  const isProfileDefault = w === profileEncoderSettings.video.width && h === profileEncoderSettings.video.height;
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
                <span style={{ color: '#565f89' }}>Actual Resolution</span>
                <span style={{ color: '#c0caf5', fontFamily: 'monospace' }}>
                  {Math.round(videoTrackSettings.width)}x{Math.round(videoTrackSettings.height)}
                </span>
              </div>
            )}
            <div style={rowStyle}>
              <span style={{ color: '#565f89' }}>Framerate</span>
              <SettingSelect
                value={encoderOverrides?.video?.framerate ?? profileEncoderSettings.video.framerate}
                options={FRAMERATE_OPTIONS}
                isOverridden={!!encoderOverrides?.video?.framerate}
                disabled={state === 'streaming'}
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
                <span style={{ color: '#565f89' }}>Actual Framerate</span>
                <span style={{ color: '#c0caf5', fontFamily: 'monospace' }}>
                  {Math.round(videoTrackSettings.frameRate)} fps
                </span>
              </div>
            )}
            <div style={rowStyle}>
              <span style={{ color: '#565f89' }}>Video Bitrate</span>
              <SettingSelect
                value={encoderOverrides?.video?.bitrate ?? profileEncoderSettings.video.bitrate}
                options={VIDEO_BITRATE_OPTIONS}
                isOverridden={!!encoderOverrides?.video?.bitrate}
                disabled={state === 'streaming'}
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
              <span style={{ color: '#565f89' }}>Audio Codec</span>
              <span style={{ color: '#c0caf5', fontFamily: 'monospace' }}>
                {effectiveEncoderConfig.audio.codec}
              </span>
            </div>
            <div style={rowStyle}>
              <span style={{ color: '#565f89' }}>Audio Bitrate</span>
              <SettingSelect
                value={encoderOverrides?.audio?.bitrate ?? profileEncoderSettings.audio.bitrate}
                options={AUDIO_BITRATE_OPTIONS}
                isOverridden={!!encoderOverrides?.audio?.bitrate}
                disabled={state === 'streaming'}
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
            {state === 'streaming' && (
              <div style={{ padding: '8px 12px', fontSize: '10px', color: '#e0af68' }}>
                Settings locked while streaming
              </div>
            )}
          </div>

          {/* Sources */}
          <div style={{ borderBottom: '1px solid rgba(65, 72, 104, 0.3)' }}>
            <div style={{ padding: '8px 12px', background: '#16161e' }}>
              <span style={sectionHeaderStyle}>Sources ({sources.length})</span>
            </div>
            {sources.length > 0 ? (
              <div>
                {sources.map((source, idx) => (
                  <div
                    key={source.id}
                    style={{
                      padding: '8px 12px',
                      borderTop: idx > 0 ? '1px solid rgba(65, 72, 104, 0.2)' : undefined,
                    }}
                  >
                    <div style={{ display: 'flex', alignItems: 'center', gap: '8px' }}>
                      <span
                        style={{
                          fontSize: '10px',
                          fontFamily: 'monospace',
                          padding: '2px 6px',
                          textTransform: 'uppercase',
                          background:
                            source.type === 'camera'
                              ? 'rgba(122, 162, 247, 0.2)'
                              : source.type === 'screen'
                              ? 'rgba(158, 206, 106, 0.2)'
                              : 'rgba(224, 175, 104, 0.2)',
                          color:
                            source.type === 'camera'
                              ? '#7aa2f7'
                              : source.type === 'screen'
                              ? '#9ece6a'
                              : '#e0af68',
                        }}
                      >
                        {source.type}
                      </span>
                      <span
                        style={{
                          color: '#c0caf5',
                          fontSize: '12px',
                          overflow: 'hidden',
                          textOverflow: 'ellipsis',
                          whiteSpace: 'nowrap',
                        }}
                      >
                        {source.label}
                      </span>
                    </div>
                    <div
                      style={{
                        display: 'flex',
                        gap: '12px',
                        marginTop: '4px',
                        fontSize: '10px',
                        color: '#565f89',
                      }}
                    >
                      <span>Vol: {Math.round(source.volume * 100)}%</span>
                      {source.muted && <span style={{ color: '#f7768e' }}>Muted</span>}
                      {!source.active && <span style={{ color: '#e0af68' }}>Inactive</span>}
                    </div>
                  </div>
                ))}
              </div>
            ) : (
              <div style={{ padding: '16px 12px', color: '#565f89', textAlign: 'center', fontSize: '12px' }}>
                No sources added
              </div>
            )}
          </div>
        </div>
      )}

      {/* Compositor Tab */}
      {activeTab === 'compositor' && compositorEnabled && (
        <div style={{ flex: 1, overflowY: 'auto' }}>
          {/* Renderer Info */}
          <div style={{ padding: '12px', borderBottom: '1px solid rgba(65, 72, 104, 0.3)' }}>
            <div style={sectionHeaderStyle}>Renderer</div>
            <div
              style={{
                fontSize: '14px',
                fontWeight: 600,
                color:
                  compositorRendererType === 'webgpu'
                    ? '#bb9af7'
                    : compositorRendererType === 'webgl'
                    ? '#7aa2f7'
                    : '#9ece6a',
              }}
            >
              {compositorRendererType === 'webgpu' && 'WebGPU'}
              {compositorRendererType === 'webgl' && 'WebGL'}
              {compositorRendererType === 'canvas2d' && 'Canvas2D'}
              {!compositorRendererType && 'Not initialized'}
            </div>
            <div style={{ fontSize: '10px', color: '#565f89', marginTop: '4px' }}>
              Set renderer in config before starting
            </div>
          </div>

          {/* Stats */}
          {compositorStats && (
            <div style={{ borderBottom: '1px solid rgba(65, 72, 104, 0.3)' }}>
              <div style={{ padding: '8px 12px', background: '#16161e' }}>
                <span style={sectionHeaderStyle}>Performance</span>
              </div>
              <div style={rowStyle}>
                <span style={{ color: '#565f89' }}>Frame Rate</span>
                <span style={{ color: '#c0caf5', fontFamily: 'monospace' }}>
                  {compositorStats.fps} fps
                </span>
              </div>
              <div style={rowStyle}>
                <span style={{ color: '#565f89' }}>Frame Time</span>
                <span
                  style={{
                    color: compositorStats.frameTimeMs > 16 ? '#e0af68' : '#c0caf5',
                    fontFamily: 'monospace',
                  }}
                >
                  {compositorStats.frameTimeMs.toFixed(2)} ms
                </span>
              </div>
              {compositorStats.gpuMemoryMB !== undefined && (
                <div style={rowStyle}>
                  <span style={{ color: '#565f89' }}>GPU Memory</span>
                  <span style={{ color: '#c0caf5', fontFamily: 'monospace' }}>
                    {compositorStats.gpuMemoryMB.toFixed(1)} MB
                  </span>
                </div>
              )}
            </div>
          )}

          {/* Scenes & Layers */}
          <div style={{ borderBottom: '1px solid rgba(65, 72, 104, 0.3)' }}>
            <div style={{ padding: '8px 12px', background: '#16161e' }}>
              <span style={sectionHeaderStyle}>Composition</span>
            </div>
            <div style={rowStyle}>
              <span style={{ color: '#565f89' }}>Scenes</span>
              <span style={{ color: '#c0caf5', fontFamily: 'monospace' }}>{sceneCount}</span>
            </div>
            <div style={rowStyle}>
              <span style={{ color: '#565f89' }}>Layers</span>
              <span style={{ color: '#c0caf5', fontFamily: 'monospace' }}>{layerCount}</span>
            </div>
          </div>

          {/* Encoder Section */}
          <div style={{ borderBottom: '1px solid rgba(65, 72, 104, 0.3)' }}>
            <div style={{ padding: '8px 12px', background: '#16161e' }}>
              <span style={sectionHeaderStyle}>Encoder</span>
            </div>
            <div style={rowStyle}>
              <span style={{ color: '#565f89' }}>Type</span>
              <span
                style={{
                  fontSize: '12px',
                  fontFamily: 'monospace',
                  padding: '2px 6px',
                  background: (useWebCodecs && isWebCodecsAvailable)
                    ? 'rgba(187, 154, 247, 0.2)'
                    : 'rgba(122, 162, 247, 0.2)',
                  color: (useWebCodecs && isWebCodecsAvailable) ? '#bb9af7' : '#7aa2f7',
                }}
              >
                {(useWebCodecs && isWebCodecsAvailable) ? 'WebCodecs' : 'Browser'}
                {state === 'streaming' && (
                  <span style={{ opacity: 0.7, marginLeft: '4px' }}>
                    {isWebCodecsActive ? '(active)' : '(pending)'}
                  </span>
                )}
              </span>
            </div>
            <div style={rowStyle}>
              <span style={{ color: '#565f89' }}>Use WebCodecs</span>
              <ToggleSwitch
                checked={useWebCodecs}
                onChange={(checked) => onUseWebCodecsChange?.(checked)}
                disabled={state === 'streaming' || !isWebCodecsAvailable}
              />
            </div>
            {!isWebCodecsAvailable && (
              <div style={{ padding: '8px 12px', fontSize: '10px', color: '#f7768e' }}>
                Not available - RTCRtpScriptTransform unsupported
              </div>
            )}
            {isWebCodecsAvailable && state === 'streaming' && useWebCodecs !== isWebCodecsActive && (
              <div style={{ padding: '8px 12px', fontSize: '10px', color: '#e0af68' }}>
                Change takes effect on next stream
              </div>
            )}
          </div>

          {/* WebCodecs Encoder Stats */}
          {isWebCodecsActive && encoderStats && (
            <div style={{ borderBottom: '1px solid rgba(65, 72, 104, 0.3)' }}>
              <div style={{ padding: '8px 12px', background: '#16161e' }}>
                <span style={sectionHeaderStyle}>Encoder Stats</span>
              </div>
              <div style={rowStyle}>
                <span style={{ color: '#565f89' }}>Video Frames</span>
                <span style={{ color: '#c0caf5', fontFamily: 'monospace' }}>
                  {encoderStats.video.framesEncoded}
                </span>
              </div>
              <div style={rowStyle}>
                <span style={{ color: '#565f89' }}>Video Pending</span>
                <span
                  style={{
                    color: encoderStats.video.framesPending > 5 ? '#e0af68' : '#c0caf5',
                    fontFamily: 'monospace',
                  }}
                >
                  {encoderStats.video.framesPending}
                </span>
              </div>
              <div style={rowStyle}>
                <span style={{ color: '#565f89' }}>Video Bytes</span>
                <span style={{ color: '#c0caf5', fontFamily: 'monospace' }}>
                  {(encoderStats.video.bytesEncoded / 1024 / 1024).toFixed(2)} MB
                </span>
              </div>
              <div style={rowStyle}>
                <span style={{ color: '#565f89' }}>Audio Samples</span>
                <span style={{ color: '#c0caf5', fontFamily: 'monospace' }}>
                  {encoderStats.audio.samplesEncoded}
                </span>
              </div>
              <div style={rowStyle}>
                <span style={{ color: '#565f89' }}>Audio Bytes</span>
                <span style={{ color: '#c0caf5', fontFamily: 'monospace' }}>
                  {(encoderStats.audio.bytesEncoded / 1024).toFixed(1)} KB
                </span>
              </div>
            </div>
          )}

          {/* Info */}
          <div style={{ padding: '12px' }}>
            <div style={{ fontSize: '10px', color: '#565f89', lineHeight: 1.5 }}>
              {(useWebCodecs && isWebCodecsAvailable)
                ? 'WebCodecs encoder via RTCRtpScriptTransform provides lower latency and better encoding control.'
                : 'Browser\'s built-in MediaStream encoder. Enable WebCodecs toggle for advanced encoding.'}
            </div>
          </div>
        </div>
      )}
    </div>
  );
};

export default AdvancedPanel;
export { AdvancedPanel };
