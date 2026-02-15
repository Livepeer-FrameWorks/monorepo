/**
 * StreamCrafter React Component
 * Self-contained browser-based WHIP streaming component
 * Uses slab design system with Tokyo Night colors
 *
 * @example
 * import { StreamCrafter } from '@livepeer-frameworks/streamcrafter-react';
 * import '@livepeer-frameworks/streamcrafter-react/streamcrafter.css';
 *
 * <StreamCrafter whipUrl="https://edge-ingest.example.com/webrtc/stream-key" />
 */

import React, { useEffect, useRef, useState, useCallback, useMemo } from "react";
import { useStreamCrafterV2 } from "../hooks/useStreamCrafterV2";
import { useAudioLevels } from "../hooks/useAudioLevels";
import { useCompositor } from "../hooks/useCompositor";
import { useIngestEndpoints } from "../hooks/useIngestEndpoints";
import AdvancedPanel from "./AdvancedPanel";
import { CompositorControls } from "./CompositorControls";
import { VolumeSlider } from "./VolumeSlider";
import type { AudioProcessingSettings } from "./AdvancedPanel";
import type {
  IngestState,
  IngestStateContextV2,
  QualityProfile,
  MediaSource,
  ReconnectionState,
  EncoderOverrides,
} from "@livepeer-frameworks/streamcrafter-core";
import { getAudioConstraints } from "@livepeer-frameworks/streamcrafter-core";
import {
  ContextMenu,
  ContextMenuContent,
  ContextMenuItem,
  ContextMenuSeparator,
  ContextMenuTrigger,
} from "../ui/context-menu";

// ============================================================================
// Types
// ============================================================================

export interface StreamCrafterProps {
  /** Direct WHIP endpoint URL */
  whipUrl?: string;
  /** Gateway URL for endpoint resolution (alternative to whipUrl) */
  gatewayUrl?: string;
  /** Stream key for gateway mode */
  streamKey?: string;
  /** Initial quality profile */
  initialProfile?: QualityProfile;
  /** Auto-start camera on mount */
  autoStartCamera?: boolean;
  /** Show settings panel by default */
  showSettings?: boolean;
  /** Enable dev mode UI */
  devMode?: boolean;
  /** Enable debug logging */
  debug?: boolean;
  /** Enable compositor for multi-source composition */
  enableCompositor?: boolean;
  /** Compositor configuration (renderer preference, resolution, etc.) */
  compositorConfig?: {
    renderer?: "auto" | "webgpu" | "webgl" | "canvas2d";
    width?: number;
    height?: number;
    frameRate?: number;
  };
  /** Custom class name */
  className?: string;
  /** State change callback */
  onStateChange?: (state: IngestState, context?: IngestStateContextV2) => void;
  /** Error callback */
  onError?: (error: string) => void;
}

// ============================================================================
// Icons (inline SVG for zero dependencies)
// ============================================================================

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

const MonitorIcon = ({ size = 18 }: { size?: number }) => (
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
    <rect x="2" y="3" width="20" height="14" rx="2" ry="2" />
    <line x1="8" y1="21" x2="16" y2="21" />
    <line x1="12" y1="17" x2="12" y2="21" />
  </svg>
);

const MicIcon = ({ size = 16, muted = false }: { size?: number; muted?: boolean }) =>
  muted ? (
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
      <line x1="1" y1="1" x2="23" y2="23" />
      <path d="M9 9v3a3 3 0 0 0 5.12 2.12M15 9.34V4a3 3 0 0 0-5.94-.6" />
      <path d="M17 16.95A7 7 0 0 1 5 12v-2m14 0v2a7 7 0 0 1-.11 1.23" />
      <line x1="12" y1="19" x2="12" y2="23" />
      <line x1="8" y1="23" x2="16" y2="23" />
    </svg>
  ) : (
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
      <path d="M12 1a3 3 0 0 0-3 3v8a3 3 0 0 0 6 0V4a3 3 0 0 0-3-3z" />
      <path d="M19 10v2a7 7 0 0 1-14 0v-2" />
      <line x1="12" y1="19" x2="12" y2="23" />
      <line x1="8" y1="23" x2="16" y2="23" />
    </svg>
  );

const XIcon = ({ size = 14 }: { size?: number }) => (
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
    <line x1="18" y1="6" x2="6" y2="18" />
    <line x1="6" y1="6" x2="18" y2="18" />
  </svg>
);

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

const ChevronsRightIcon = ({ size = 14 }: { size?: number }) => (
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
    <polyline points="13 17 18 12 13 7" />
    <polyline points="6 17 11 12 6 7" />
  </svg>
);

const ChevronsLeftIcon = ({ size = 14 }: { size?: number }) => (
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
    <polyline points="11 17 6 12 11 7" />
    <polyline points="18 17 13 12 18 7" />
  </svg>
);

const VideoIcon = ({ size = 14, active = false }: { size?: number; active?: boolean }) => (
  <svg
    width={size}
    height={size}
    viewBox="0 0 24 24"
    fill={active ? "currentColor" : "none"}
    stroke="currentColor"
    strokeWidth="2"
    strokeLinecap="round"
    strokeLinejoin="round"
  >
    <polygon points="23 7 16 12 23 17 23 7" />
    <rect x="1" y="5" width="15" height="14" rx="2" ry="2" />
  </svg>
);

// ============================================================================
// Quality Profile Options
// ============================================================================

const QUALITY_PROFILES: { id: QualityProfile; label: string; description: string }[] = [
  { id: "professional", label: "Professional", description: "1080p @ 8 Mbps" },
  { id: "broadcast", label: "Broadcast", description: "1080p @ 4.5 Mbps" },
  { id: "conference", label: "Conference", description: "720p @ 2.5 Mbps" },
];

// ============================================================================
// Helper Functions
// ============================================================================

function cn(...classes: (string | undefined | false)[]): string {
  return classes.filter(Boolean).join(" ");
}

function getStatusText(state: IngestState, reconnectionState?: ReconnectionState | null): string {
  if (reconnectionState?.isReconnecting) {
    return `Reconnecting (${reconnectionState.attemptNumber}/5)...`;
  }
  switch (state) {
    case "idle":
      return "Idle";
    case "requesting_permissions":
      return "Permissions...";
    case "capturing":
      return "Ready";
    case "connecting":
      return "Connecting...";
    case "streaming":
      return "Live";
    case "reconnecting":
      return "Reconnecting...";
    case "error":
      return "Error";
    case "destroyed":
      return "Destroyed";
    default:
      return state;
  }
}

function getStatusBadgeClass(state: IngestState, isReconnecting: boolean): string {
  if (state === "streaming") return "fw-sc-badge fw-sc-badge--live";
  if (isReconnecting) return "fw-sc-badge fw-sc-badge--connecting";
  if (state === "error") return "fw-sc-badge fw-sc-badge--error";
  if (state === "capturing") return "fw-sc-badge fw-sc-badge--ready";
  return "fw-sc-badge fw-sc-badge--idle";
}

// ============================================================================
// VU Meter Component
// ============================================================================

interface VUMeterProps {
  level: number;
  peakLevel: number;
}

const VUMeter: React.FC<VUMeterProps> = ({ level, peakLevel }) => (
  <div className="fw-sc-vu-meter">
    <div className="fw-sc-vu-meter-fill" style={{ width: `${Math.min(level * 100, 100)}%` }} />
    <div className="fw-sc-vu-meter-peak" style={{ left: `${Math.min(peakLevel * 100, 100)}%` }} />
  </div>
);

// ============================================================================
// Source Row Component
// ============================================================================

interface SourceRowProps {
  source: MediaSource;
  onMuteToggle: () => void;
  onVolumeChange: (volume: number) => void;
  onSetPrimary: () => void;
  onRemove: () => void;
  disabled: boolean;
  isStreaming: boolean;
  hasVideo: boolean;
  // Compositor-specific props
  isCompositorEnabled?: boolean;
  isVisibleInCompositor?: boolean;
  onVisibilityToggle?: () => void;
}

// Eye icon for visibility toggle
const EyeIcon: React.FC<{ size: number; visible: boolean }> = ({ size, visible }) => (
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
    {visible ? (
      <>
        <path d="M1 12s4-8 11-8 11 8 11 8-4 8-11 8-11-8-11-8z" />
        <circle cx="12" cy="12" r="3" />
      </>
    ) : (
      <>
        <path d="M17.94 17.94A10.07 10.07 0 0 1 12 20c-7 0-11-8-11-8a18.45 18.45 0 0 1 5.06-5.94M9.9 4.24A9.12 9.12 0 0 1 12 4c7 0 11 8 11 8a18.5 18.5 0 0 1-2.16 3.19m-6.72-1.07a3 3 0 1 1-4.24-4.24" />
        <line x1="1" y1="1" x2="23" y2="23" />
      </>
    )}
  </svg>
);

const SourceRow: React.FC<SourceRowProps> = ({
  source,
  onMuteToggle,
  onVolumeChange,
  onSetPrimary,
  onRemove,
  disabled,
  isStreaming,
  hasVideo,
  isCompositorEnabled = false,
  isVisibleInCompositor = true,
  onVisibilityToggle,
}) => (
  <div className={cn("fw-sc-source", !isVisibleInCompositor && "fw-sc-source--hidden")}>
    {/* Visibility toggle for compositor mode */}
    {isCompositorEnabled && onVisibilityToggle && (
      <button
        type="button"
        className={cn("fw-sc-icon-btn", !isVisibleInCompositor && "fw-sc-icon-btn--muted")}
        onClick={onVisibilityToggle}
        title={isVisibleInCompositor ? "Hide from composition" : "Show in composition"}
      >
        <EyeIcon size={14} visible={isVisibleInCompositor} />
      </button>
    )}
    <div className="fw-sc-source-icon">
      {source.type === "camera" && <CameraIcon size={16} />}
      {source.type === "screen" && <MonitorIcon size={16} />}
    </div>
    <div className="fw-sc-source-info">
      <div className="fw-sc-source-label">
        {source.label}
        {source.primaryVideo && !isCompositorEnabled && (
          <span className="fw-sc-primary-badge">PRIMARY</span>
        )}
      </div>
      <div className="fw-sc-source-type">{source.type}</div>
    </div>
    <div className="fw-sc-source-controls">
      {/* Primary Video Button - only show when NOT in compositor mode */}
      {hasVideo && !isCompositorEnabled && (
        <button
          type="button"
          className={cn("fw-sc-icon-btn", source.primaryVideo && "fw-sc-icon-btn--primary")}
          onClick={onSetPrimary}
          disabled={disabled || source.primaryVideo}
          title={source.primaryVideo ? "Primary video source" : "Set as primary video"}
        >
          <VideoIcon size={14} active={source.primaryVideo} />
        </button>
      )}
      {/* Volume Slider - supports up to 200% boost with popup and snap */}
      <span className="fw-sc-volume-label">{Math.round(source.volume * 100)}%</span>
      <VolumeSlider value={source.volume} onChange={onVolumeChange} compact={true} />
      <button
        type="button"
        className={cn("fw-sc-icon-btn", source.muted && "fw-sc-icon-btn--active")}
        onClick={onMuteToggle}
        title={source.muted ? "Unmute" : "Mute"}
      >
        <MicIcon size={14} muted={source.muted} />
      </button>
      <button
        type="button"
        className="fw-sc-icon-btn fw-sc-icon-btn--destructive"
        onClick={onRemove}
        disabled={disabled || isStreaming}
        title={isStreaming ? "Cannot remove source while streaming" : "Remove source"}
      >
        <XIcon size={14} />
      </button>
    </div>
  </div>
);

// ============================================================================
// Main Component
// ============================================================================

const StreamCrafterInner: React.FC<StreamCrafterProps> = ({
  whipUrl,
  gatewayUrl,
  streamKey,
  initialProfile = "broadcast",
  autoStartCamera = false,
  showSettings: initialShowSettings = false,
  devMode = false,
  debug = false,
  enableCompositor = false,
  compositorConfig,
  className,
  onStateChange,
  onError,
}) => {
  const videoRef = useRef<HTMLVideoElement>(null);
  const settingsDropdownRef = useRef<HTMLDivElement>(null);
  const settingsButtonRef = useRef<HTMLButtonElement>(null);
  const [showSettings, setShowSettings] = useState(initialShowSettings);
  const [showSources, setShowSources] = useState(true);
  const [isAdvancedPanelOpen, setIsAdvancedPanelOpen] = useState(false);
  const [masterVolume, setMasterVolumeState] = useState(1);

  // Audio processing state - initialized from profile defaults
  const profileDefaults = getAudioConstraints(initialProfile);
  const [audioProcessing, setAudioProcessing] = useState<AudioProcessingSettings>({
    echoCancellation: profileDefaults.echoCancellation,
    noiseSuppression: profileDefaults.noiseSuppression,
    autoGainControl: profileDefaults.autoGainControl,
  });

  // Encoder overrides state - allows overriding profile encoder settings
  const [encoderOverrides, setEncoderOverrides] = useState<EncoderOverrides>({});

  // Gateway-based ingest endpoint resolution (like useViewerEndpoints in player)
  const {
    endpoints: _gatewayEndpoints,
    status: endpointStatus,
    error: endpointError,
    whipUrl: gatewayWhipUrl,
  } = useIngestEndpoints(gatewayUrl && streamKey && !whipUrl ? { gatewayUrl, streamKey } : {});

  // Priority: direct whipUrl prop > gateway-resolved > undefined
  const resolvedWhipUrl = useMemo(() => {
    if (whipUrl) return whipUrl;
    if (gatewayWhipUrl) return gatewayWhipUrl;
    return undefined;
  }, [whipUrl, gatewayWhipUrl]);

  // Track if we're waiting for gateway resolution
  const isResolvingEndpoint = !whipUrl && gatewayUrl && streamKey && endpointStatus === "loading";

  // Click outside handler for settings dropdown
  useEffect(() => {
    if (!showSettings) return;

    const handleClickOutside = (e: MouseEvent) => {
      const target = e.target as Node;
      if (
        settingsDropdownRef.current &&
        !settingsDropdownRef.current.contains(target) &&
        settingsButtonRef.current &&
        !settingsButtonRef.current.contains(target)
      ) {
        setShowSettings(false);
      }
    };

    const handleEscape = (e: KeyboardEvent) => {
      if (e.key === "Escape") setShowSettings(false);
    };

    document.addEventListener("mousedown", handleClickOutside);
    document.addEventListener("keydown", handleEscape);
    return () => {
      document.removeEventListener("mousedown", handleClickOutside);
      document.removeEventListener("keydown", handleEscape);
    };
  }, [showSettings]);

  // Initialize StreamCrafter hook
  const {
    state,
    stateContext,
    isStreaming,
    isCapturing,
    isReconnecting,
    error,
    mediaStream,
    sources,
    qualityProfile,
    setQualityProfile,
    reconnectionState,
    startCamera,
    startScreenShare,
    removeSource,
    setSourceMuted,
    setSourceVolume,
    setPrimaryVideoSource,
    setMasterVolume,
    stats,
    startStreaming,
    stopStreaming,
    getController,
    // WebCodecs encoder
    useWebCodecs,
    isWebCodecsActive,
    isWebCodecsAvailable,
    encoderStats,
    setUseWebCodecs,
    setEncoderOverrides: setEncoderOverridesHook,
  } = useStreamCrafterV2({
    whipUrl: resolvedWhipUrl || "",
    profile: initialProfile,
    debug,
    reconnection: { enabled: true, maxAttempts: 5 },
    audioMixing: true,
  });

  // Audio levels for VU meter
  const { levels, isMonitoring } = useAudioLevels({
    controller: getController(),
    autoStart: true,
  });

  // Compositor for multi-source composition
  const compositor = useCompositor({
    controller: enableCompositor ? getController() : null,
    autoEnable: enableCompositor,
    config: compositorConfig,
  });

  // Notify parent of state changes
  useEffect(() => {
    onStateChange?.(state, stateContext);
  }, [state, stateContext, onStateChange]);

  // Notify parent of errors
  useEffect(() => {
    if (error) {
      onError?.(error);
    }
  }, [error, onError]);

  // Sync encoder overrides to controller
  useEffect(() => {
    setEncoderOverridesHook(encoderOverrides);
  }, [encoderOverrides, setEncoderOverridesHook]);

  // Update video preview when stream changes
  useEffect(() => {
    if (videoRef.current && mediaStream) {
      videoRef.current.srcObject = mediaStream;
      videoRef.current.play().catch(() => {});
    } else if (videoRef.current) {
      videoRef.current.srcObject = null;
    }
  }, [mediaStream]);

  // Auto-start camera if enabled
  useEffect(() => {
    if (autoStartCamera && resolvedWhipUrl && state === "idle") {
      startCamera().catch(console.error);
    }
  }, [autoStartCamera, resolvedWhipUrl, state, startCamera]);

  // Handlers
  const handleStartCamera = useCallback(async () => {
    try {
      await startCamera();
    } catch (err) {
      console.error("Failed to start camera:", err);
    }
  }, [startCamera]);

  const handleStartScreenShare = useCallback(async () => {
    try {
      await startScreenShare({ audio: true });
    } catch (err) {
      console.error("Failed to start screen share:", err);
    }
  }, [startScreenShare]);

  const handleGoLive = useCallback(async () => {
    if (!resolvedWhipUrl) {
      console.error("No WHIP endpoint configured");
      return;
    }
    try {
      await startStreaming();
    } catch (err) {
      console.error("Failed to start streaming:", err);
    }
  }, [resolvedWhipUrl, startStreaming]);

  const handleStopStreaming = useCallback(async () => {
    await stopStreaming();
  }, [stopStreaming]);

  const toggleSourceMute = useCallback(
    (sourceId: string, currentMuted: boolean) => {
      setSourceMuted(sourceId, !currentMuted);
    },
    [setSourceMuted]
  );

  // Master volume handler
  const handleMasterVolumeChange = useCallback(
    (volume: number) => {
      setMasterVolume(volume);
      setMasterVolumeState(volume);
    },
    [setMasterVolume]
  );

  // Audio processing change handler - applies constraints to all audio tracks
  const handleAudioProcessingChange = useCallback(
    (newSettings: Partial<AudioProcessingSettings>) => {
      setAudioProcessing((prev) => {
        const updated = { ...prev, ...newSettings };

        // Apply constraints to all audio tracks across all sources
        sources.forEach((source) => {
          source.stream.getAudioTracks().forEach((track) => {
            track
              .applyConstraints({
                echoCancellation: updated.echoCancellation,
                noiseSuppression: updated.noiseSuppression,
                autoGainControl: updated.autoGainControl,
              })
              .catch((err) => {
                console.warn("Failed to apply audio constraints:", err);
              });
          });
        });

        return updated;
      });
    },
    [sources]
  );

  // Context menu actions
  const copyWhipUrl = useCallback(() => {
    if (resolvedWhipUrl) {
      navigator.clipboard.writeText(resolvedWhipUrl).catch(console.error);
    }
  }, [resolvedWhipUrl]);

  const copyStreamInfo = useCallback(() => {
    const profile = QUALITY_PROFILES.find((p) => p.id === qualityProfile);
    const info = [
      `Status: ${state}`,
      `Quality: ${profile?.label ?? qualityProfile} (${profile?.description ?? ""})`,
      `Sources: ${sources.length}`,
      resolvedWhipUrl ? `WHIP: ${resolvedWhipUrl}` : null,
    ]
      .filter(Boolean)
      .join("\n");
    navigator.clipboard.writeText(info).catch(console.error);
  }, [state, qualityProfile, sources.length, resolvedWhipUrl]);

  // Computed state
  const canAddSource = state !== "destroyed" && state !== "error";
  const canStream = isCapturing && !isStreaming && resolvedWhipUrl;
  const hasCamera = sources.some((s) => s.type === "camera");
  const _hasScreen = sources.some((s) => s.type === "screen");
  const statusText = getStatusText(state, reconnectionState);
  const statusBadgeClass = getStatusBadgeClass(state, isReconnecting);

  return (
    <ContextMenu>
      <ContextMenuTrigger asChild>
        <div className={cn("fw-sc-root", devMode && "fw-sc-root--devmode", className)}>
          {/* Main content wrapper - takes remaining space when panel is open */}
          <div className={cn("fw-sc-main", devMode ? "flex-1 min-w-0" : "w-full")}>
            {/* Header */}
            <div className="fw-sc-header">
              <span className="fw-sc-header-title">StreamCrafter</span>
              <div className="fw-sc-header-status">
                <span className={statusBadgeClass}>{statusText}</span>
              </div>
            </div>

            {/* Content area (preview + mixer) - responsive layout */}
            <div className="fw-sc-content">
              {/* Preview wrapper for flex sizing */}
              <div className="fw-sc-preview-wrapper">
                {/* Video Preview (flush - no padding) */}
                <div className="fw-sc-preview">
                  <video ref={videoRef} playsInline muted autoPlay aria-label="Stream preview" />

                  {/* Empty State */}
                  {!mediaStream && (
                    <div className="fw-sc-preview-placeholder">
                      <CameraIcon size={48} />
                      <span>Add a camera or screen to preview</span>
                    </div>
                  )}

                  {/* Status Overlay - Connecting/Reconnecting */}
                  {(state === "connecting" || state === "reconnecting") && (
                    <div className="fw-sc-status-overlay">
                      <div className="fw-sc-status-spinner" />
                      <span className="fw-sc-status-text">{statusText}</span>
                    </div>
                  )}

                  {/* Live Badge */}
                  {isStreaming && <div className="fw-sc-live-badge">Live</div>}

                  {/* Compositor Controls Overlay (inside preview for positioning) */}
                  {enableCompositor && (
                    <CompositorControls
                      isEnabled={compositor.isEnabled}
                      isInitialized={compositor.isInitialized}
                      rendererType={compositor.rendererType}
                      stats={compositor.stats}
                      sources={sources}
                      layers={compositor.activeScene?.layers ?? []}
                      currentLayout={compositor.currentLayout}
                      onLayoutApply={compositor.applyLayout}
                      onCycleSourceOrder={(direction) => compositor.cycleSourceOrder(direction)}
                    />
                  )}
                </div>
              </div>

              {/* Sources Mixer Section - moves to right on wide screens */}
              {sources.length > 0 && (
                <div
                  className={cn(
                    "fw-sc-section fw-sc-mixer",
                    !showSources && "fw-sc-section--collapsed"
                  )}
                >
                  <div
                    className="fw-sc-section-header"
                    onClick={() => setShowSources(!showSources)}
                    title={showSources ? "Collapse Mixer" : "Expand Mixer"}
                  >
                    <span>Mixer ({sources.length})</span>
                    {showSources ? <ChevronsRightIcon size={14} /> : <ChevronsLeftIcon size={14} />}
                  </div>
                  {showSources && (
                    <div className="fw-sc-section-body--flush">
                      <div className="fw-sc-sources">
                        {sources.map((source: MediaSource) => {
                          // Get visibility state for compositor mode
                          const layer = compositor.activeScene?.layers.find(
                            (l) => l.sourceId === source.id
                          );
                          const isVisibleInCompositor = layer?.visible ?? true;

                          return (
                            <SourceRow
                              key={source.id}
                              source={source}
                              onMuteToggle={() => toggleSourceMute(source.id, source.muted)}
                              onVolumeChange={(vol) => setSourceVolume(source.id, vol)}
                              onSetPrimary={() => setPrimaryVideoSource(source.id)}
                              onRemove={() => removeSource(source.id)}
                              disabled={false}
                              isStreaming={isStreaming}
                              hasVideo={source.stream.getVideoTracks().length > 0}
                              isCompositorEnabled={enableCompositor}
                              isVisibleInCompositor={isVisibleInCompositor}
                              onVisibilityToggle={() => {
                                if (compositor.activeSceneId && layer) {
                                  compositor.setLayerVisibility(
                                    compositor.activeSceneId,
                                    layer.id,
                                    !isVisibleInCompositor
                                  );
                                }
                              }}
                            />
                          );
                        })}
                      </div>
                    </div>
                  )}
                </div>
              )}
            </div>

            {/* VU Meter (horizontal bar under content area) */}
            {isCapturing && <VUMeter level={levels.level} peakLevel={levels.peakLevel} />}

            {/* Error Display */}
            {(error || endpointError) && (
              <div className="fw-sc-error">
                <div className="fw-sc-error-title">Error</div>
                <div className="fw-sc-error-message">{error || endpointError}</div>
              </div>
            )}

            {/* No Endpoint Warning */}
            {!resolvedWhipUrl && !error && !endpointError && !isResolvingEndpoint && (
              <div className="fw-sc-error" style={{ borderLeftColor: "hsl(40 80% 65%)" }}>
                <div className="fw-sc-error-title" style={{ color: "hsl(40 80% 65%)" }}>
                  Warning
                </div>
                <div className="fw-sc-error-message">Configure WHIP endpoint to stream</div>
              </div>
            )}

            {/* Resolving Endpoint State */}
            {isResolvingEndpoint && (
              <div className="fw-sc-error" style={{ borderLeftColor: "hsl(210 80% 65%)" }}>
                <div className="fw-sc-error-title" style={{ color: "hsl(210 80% 65%)" }}>
                  Resolving
                </div>
                <div className="fw-sc-error-message">Resolving ingest endpoint...</div>
              </div>
            )}

            {/* Action Bar */}
            <div className="fw-sc-actions">
              {/* Secondary actions: Camera, Screen, Settings */}
              <button
                type="button"
                className="fw-sc-action-secondary"
                onClick={handleStartCamera}
                disabled={!canAddSource || hasCamera}
                title={hasCamera ? "Camera active" : "Add Camera"}
              >
                <CameraIcon size={18} />
              </button>
              <button
                type="button"
                className="fw-sc-action-secondary"
                onClick={handleStartScreenShare}
                disabled={!canAddSource}
                title="Share Screen"
              >
                <MonitorIcon size={18} />
              </button>
              {/* Settings button in action bar */}
              <div style={{ position: "relative" }}>
                <button
                  ref={settingsButtonRef}
                  type="button"
                  className={cn(
                    "fw-sc-action-secondary",
                    showSettings && "fw-sc-action-secondary--active"
                  )}
                  onClick={() => setShowSettings(!showSettings)}
                  title="Settings"
                  style={{ display: "flex", alignItems: "center", justifyContent: "center" }}
                >
                  <span
                    style={{
                      display: "inline-flex",
                      transition: "transform 0.2s",
                    }}
                    className="settings-icon-wrapper"
                  >
                    <SettingsIcon size={16} />
                  </span>
                </button>
                {/* Settings Popup - positioned above button */}
                {showSettings && (
                  <div
                    ref={settingsDropdownRef}
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
                    {/* Quality Section */}
                    <div
                      style={{ padding: "8px", borderBottom: "1px solid rgba(90, 96, 127, 0.3)" }}
                    >
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
                        Quality
                      </div>
                      <div style={{ display: "flex", flexDirection: "column", gap: "2px" }}>
                        {QUALITY_PROFILES.map((p) => (
                          <button
                            key={p.id}
                            type="button"
                            onClick={() => {
                              if (!isStreaming) {
                                setQualityProfile(p.id);
                                if (!devMode) setShowSettings(false);
                              }
                            }}
                            disabled={isStreaming}
                            style={{
                              width: "100%",
                              padding: "6px 8px",
                              textAlign: "left",
                              fontSize: "12px",
                              borderRadius: "4px",
                              transition: "all 0.15s",
                              border: "none",
                              cursor: isStreaming ? "not-allowed" : "pointer",
                              opacity: isStreaming ? 0.5 : 1,
                              background:
                                qualityProfile === p.id
                                  ? "rgba(122, 162, 247, 0.2)"
                                  : "transparent",
                              color: qualityProfile === p.id ? "#7aa2f7" : "#a9b1d6",
                            }}
                          >
                            <div style={{ fontWeight: 500 }}>{p.label}</div>
                            <div style={{ fontSize: "10px", color: "#565f89" }}>
                              {p.description}
                            </div>
                          </button>
                        ))}
                      </div>
                    </div>

                    {/* Dev Info Section */}
                    {devMode && (
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
                          Debug
                        </div>
                        <div
                          style={{
                            display: "flex",
                            flexDirection: "column",
                            gap: "4px",
                            paddingLeft: "4px",
                            fontSize: "12px",
                            fontFamily:
                              "ui-monospace, SFMono-Regular, SF Mono, Menlo, Consolas, monospace",
                          }}
                        >
                          <div style={{ display: "flex", justifyContent: "space-between" }}>
                            <span style={{ color: "#565f89" }}>State</span>
                            <span style={{ color: "#c0caf5" }}>{state}</span>
                          </div>
                          <div style={{ display: "flex", justifyContent: "space-between" }}>
                            <span style={{ color: "#565f89" }}>Audio</span>
                            <span style={{ color: isMonitoring ? "#9ece6a" : "#565f89" }}>
                              {isMonitoring ? "Active" : "Inactive"}
                            </span>
                          </div>
                          <div style={{ display: "flex", justifyContent: "space-between" }}>
                            <span style={{ color: "#565f89" }}>WHIP</span>
                            <span style={{ color: resolvedWhipUrl ? "#9ece6a" : "#f7768e" }}>
                              {resolvedWhipUrl ? "OK" : "Not set"}
                            </span>
                          </div>
                        </div>
                      </div>
                    )}
                  </div>
                )}
              </div>
              {/* Primary action: Go Live / Stop */}
              {!isStreaming ? (
                <button
                  type="button"
                  className="fw-sc-action-primary"
                  onClick={handleGoLive}
                  disabled={!canStream}
                >
                  {state === "connecting" ? "Connecting..." : "Go Live"}
                </button>
              ) : (
                <button
                  type="button"
                  className="fw-sc-action-primary fw-sc-action-stop"
                  onClick={handleStopStreaming}
                >
                  Stop Streaming
                </button>
              )}
            </div>
          </div>

          {/* Advanced Panel - side panel when open */}
          {devMode && isAdvancedPanelOpen && (
            <AdvancedPanel
              isOpen={isAdvancedPanelOpen}
              onClose={() => setIsAdvancedPanelOpen(false)}
              state={state}
              qualityProfile={qualityProfile}
              whipUrl={resolvedWhipUrl}
              sources={sources}
              stats={stats}
              mediaStream={mediaStream}
              masterVolume={masterVolume}
              onMasterVolumeChange={handleMasterVolumeChange}
              audioLevel={levels.level}
              audioMixingEnabled={true}
              error={error}
              audioProcessing={audioProcessing}
              onAudioProcessingChange={handleAudioProcessingChange}
              compositorEnabled={compositor.isEnabled}
              compositorRendererType={compositor.rendererType}
              compositorStats={compositor.stats}
              sceneCount={compositor.scenes.length}
              layerCount={compositor.activeScene?.layers.length ?? 0}
              useWebCodecs={useWebCodecs}
              isWebCodecsActive={isWebCodecsActive}
              isWebCodecsAvailable={isWebCodecsAvailable}
              encoderStats={encoderStats}
              onUseWebCodecsChange={setUseWebCodecs}
              encoderOverrides={encoderOverrides}
              onEncoderOverridesChange={setEncoderOverrides}
            />
          )}
        </div>
      </ContextMenuTrigger>

      {/* Right-Click Context Menu */}
      <ContextMenuContent>
        {resolvedWhipUrl && (
          <ContextMenuItem onClick={copyWhipUrl} className="gap-2">
            <span>Copy WHIP URL</span>
          </ContextMenuItem>
        )}
        <ContextMenuItem onClick={copyStreamInfo} className="gap-2">
          <span>Copy Stream Info</span>
        </ContextMenuItem>
        {devMode && (
          <>
            <ContextMenuSeparator />
            <ContextMenuItem
              onClick={() => setIsAdvancedPanelOpen(!isAdvancedPanelOpen)}
              className="gap-2"
            >
              <SettingsIcon size={14} />
              <span>{isAdvancedPanelOpen ? "Hide Advanced" : "Advanced"}</span>
            </ContextMenuItem>
          </>
        )}
      </ContextMenuContent>
    </ContextMenu>
  );
};

// ============================================================================
// Main Component Export
// ============================================================================

/**
 * Self-contained StreamCrafter component with slab design system.
 * Requires importing the CSS file separately.
 *
 * @example
 * import { StreamCrafter } from '@livepeer-frameworks/streamcrafter-react';
 * import '@livepeer-frameworks/streamcrafter-react/streamcrafter.css';
 *
 * // Direct WHIP endpoint
 * <StreamCrafter whipUrl="https://edge-ingest.example.com/webrtc/stream-key" />
 *
 * @example
 * // Via Gateway
 * <StreamCrafter gatewayUrl="https://api.example.com" streamKey="abc123" />
 */
const StreamCrafter = StreamCrafterInner;

export default StreamCrafter;
export { StreamCrafter };
