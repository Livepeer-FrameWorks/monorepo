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
import { useStudioSupport } from "../hooks/useStudioSupport";
import AdvancedPanel from "./AdvancedPanel";
import { CompositorControls } from "./CompositorControls";
import { CameraPicker } from "./CameraPicker";
import { MediaFileButton } from "./MediaFileButton";
import { StudioSettings } from "./StudioSettings";
import { StudioAudioControls } from "./StudioAudioControls";
import { StudioConnectionHealth } from "./StudioConnectionHealth";
import { StudioProducerPanel } from "./StudioProducerPanel";
import { VolumeSlider } from "./VolumeSlider";
import type {
  AudioProcessingSettings,
  IngestState,
  IngestStateContextV2,
  QualityProfile,
  MediaSource,
  ReconnectionState,
  StudioLayout,
  FwThemePreset,
  StudioThemeOverrides,
} from "@livepeer-frameworks/streamcrafter-core";
import {
  getAudioConstraints,
  isMediaDevicesSupported,
  isSourceVisibleInScene,
  formatStreamInfo,
  handleStudioSceneHotkey,
  resolveStudioTheme,
  studioThemeOverridesToStyle,
  createStudioTranslator,
  type StudioLocale,
  type StudioTranslateFn,
} from "@livepeer-frameworks/streamcrafter-core";
import { StudioI18nProvider } from "../context/StudioI18nContext";
import {
  ContextMenu,
  ContextMenuContent,
  ContextMenuItem,
  ContextMenuSeparator,
  ContextMenuTrigger,
} from "../ui/context-menu";
import { StreamCrafterProvider } from "../context/StreamCrafterContext";

// ============================================================================
// Types
// ============================================================================

export interface StreamCrafterProps {
  /** Direct WHIP endpoint URL */
  whipUrl?: string;
  /** Refresh authorization/placement immediately before each connection without rebuilding capture. */
  resolveWhipUrl?: (signal: AbortSignal) => Promise<string>;
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
  /** Scene, transition, layer, overlay and media controls while the compositor runs. Default true. */
  showProducerPanel?: boolean;
  /** Enable debug logging */
  debug?: boolean;
  /** Enable compositor for multi-source composition. Turning it off and on keeps the layout. */
  enableCompositor?: boolean;
  /** Compositor configuration; a new `renderer` takes effect without a restart. */
  compositorConfig?: {
    renderer?: "auto" | "webgpu" | "webgl" | "canvas2d";
    width?: number;
    height?: number;
    frameRate?: number;
  };
  /** Scenes, overlays and layout preset to start from, as passed to onLayoutChange. */
  initialLayout?: StudioLayout | null;
  /** Called shortly after the scenes, overlays or layout preset change. */
  onLayoutChange?: (layout: StudioLayout) => void;
  /** Theme preset or custom overrides */
  theme?: FwThemePreset | StudioThemeOverrides;
  /** Locale for i18n translations */
  locale?: StudioLocale;
  /** Custom class name */
  className?: string;
  /** State change callback */
  onStateChange?: (state: IngestState, context?: IngestStateContextV2) => void;
  /** Error callback */
  onError?: (error: string) => void;
  /** Composable children — replaces the default UI when provided. */
  children?: React.ReactNode;
}

/** Drags and typing settle before onLayoutChange reports the layout. */
const LAYOUT_CHANGE_DELAY_MS = 300;

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

function getQualityProfiles(
  t: StudioTranslateFn
): { id: QualityProfile; label: string; description: string }[] {
  return [
    { id: "professional", label: t("professional"), description: t("professionalDesc") },
    { id: "broadcast", label: t("broadcast"), description: t("broadcastDesc") },
    { id: "conference", label: t("conference"), description: t("conferenceDesc") },
  ];
}

// ============================================================================
// Helper Functions
// ============================================================================

function cn(...classes: (string | undefined | false)[]): string {
  return classes.filter(Boolean).join(" ");
}

function getStatusText(
  state: IngestState,
  t: StudioTranslateFn,
  reconnectionState?: ReconnectionState | null
): string {
  if (reconnectionState?.isReconnecting) {
    return t("reconnectingAttempt", { attempt: reconnectionState.attemptNumber, max: 5 });
  }
  switch (state) {
    case "idle":
      return t("idle");
    case "requesting_permissions":
      return t("requestingPermissions");
    case "capturing":
      return t("ready");
    case "connecting":
      return t("connecting");
    case "streaming":
      return t("live");
    case "reconnecting":
      return t("reconnecting");
    case "error":
      return t("error");
    case "destroyed":
      return t("destroyed");
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
  t: StudioTranslateFn;
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
  t,
  isCompositorEnabled = false,
  isVisibleInCompositor = true,
  onVisibilityToggle,
}) => (
  <div className={cn("fw-sc-source", !isVisibleInCompositor && "fw-sc-source--hidden")}>
    {/* Visibility toggle for compositor mode */}
    {isCompositorEnabled && hasVideo && onVisibilityToggle && (
      <button
        type="button"
        className={cn("fw-sc-icon-btn", !isVisibleInCompositor && "fw-sc-icon-btn--muted")}
        onClick={onVisibilityToggle}
        title={isVisibleInCompositor ? t("hideFromComposition") : t("showInComposition")}
      >
        <EyeIcon size={14} visible={isVisibleInCompositor} />
      </button>
    )}
    <div className="fw-sc-source-icon">
      {source.type === "camera" && <CameraIcon size={16} />}
      {source.type === "screen" && <MonitorIcon size={16} />}
      {source.type !== "camera" && source.type !== "screen" && <VideoIcon size={16} />}
    </div>
    <div className="fw-sc-source-info">
      <div className="fw-sc-source-label">
        {source.label}
        {source.primaryVideo && !isCompositorEnabled && (
          <span className="fw-sc-primary-badge">{t("primary")}</span>
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
          title={source.primaryVideo ? t("primaryVideoSource") : t("setAsPrimary")}
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
        title={source.muted ? t("unmute") : t("mute")}
      >
        <MicIcon size={14} muted={source.muted} />
      </button>
      <button
        type="button"
        className="fw-sc-icon-btn fw-sc-icon-btn--destructive"
        onClick={onRemove}
        disabled={disabled || isStreaming}
        title={isStreaming ? t("cannotRemoveWhileStreaming") : t("removeSource")}
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
  resolveWhipUrl,
  gatewayUrl,
  streamKey,
  initialProfile = "broadcast",
  autoStartCamera = false,
  showSettings: initialShowSettings = false,
  devMode = false,
  showProducerPanel = true,
  debug = false,
  enableCompositor = true,
  compositorConfig,
  initialLayout,
  onLayoutChange,
  theme,
  locale = "en",
  className,
  onStateChange,
  onError,
  children,
}) => {
  const t = useMemo(() => createStudioTranslator({ locale }), [locale]);
  const themeStyle = useMemo(() => {
    if (!theme) return undefined;
    if (typeof theme === "object") return studioThemeOverridesToStyle(theme);
    const resolved = resolveStudioTheme(theme);
    return resolved ? studioThemeOverridesToStyle(resolved) : undefined;
  }, [theme]);
  const support = useStudioSupport();

  const videoRef = useRef<HTMLVideoElement>(null);
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

  // The controller keeps the resolver it was built with, so it calls through a
  // ref and always reaches the latest prop.
  const resolveWhipUrlRef = useRef(resolveWhipUrl);
  resolveWhipUrlRef.current = resolveWhipUrl;

  // Initialize StreamCrafter hook — capture the full return for context + destructure for local use
  const hookReturn = useStreamCrafterV2({
    whipUrl: resolvedWhipUrl || "",
    resolveWhipUrl: resolveWhipUrl
      ? (signal) =>
          resolveWhipUrlRef.current?.(signal) ??
          Promise.reject(new Error("resolveWhipUrl is no longer set"))
      : undefined,
    profile: initialProfile,
    debug,
    reconnection: { enabled: true, maxAttempts: 5 },
    audioMixing: true,
  });
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
    useWebCodecs,
    isWebCodecsActive,
    isWebCodecsAvailable,
    encoderStats,
    setUseWebCodecs,
    encoderOverrides,
    setEncoderOverrides,
  } = hookReturn;

  // Audio levels for VU meter
  const { levels, isMonitoring } = useAudioLevels({
    controller: getController(),
    autoStart: true,
  });

  // Compositor for multi-source composition. Its layout survives turning it
  // off and on and a new controller for the same stream.
  const compositor = useCompositor({
    controller: getController(),
    autoEnable: enableCompositor,
    config: compositorConfig,
    initialLayout,
  });
  const {
    isEnabled: compositorOn,
    disable: disableCompositor,
    exportLayout,
    scenes: compositorScenes,
    overlays: compositorOverlays,
    currentLayout: compositorLayout,
    activeSceneId: compositorActiveScene,
  } = compositor;

  useEffect(() => {
    if (!enableCompositor && compositorOn) disableCompositor();
  }, [enableCompositor, compositorOn, disableCompositor]);

  const onLayoutChangeRef = useRef(onLayoutChange);
  onLayoutChangeRef.current = onLayoutChange;
  const hasLayoutListener = !!onLayoutChange;
  useEffect(() => {
    if (!compositorOn || !hasLayoutListener) return;
    const timer = setTimeout(() => {
      exportLayout().then(
        (layout) => {
          if (!layout) return;
          try {
            onLayoutChangeRef.current?.(layout);
          } catch (err) {
            console.warn("onLayoutChange failed:", err);
          }
        },
        (err: unknown) => console.warn("Could not export the studio layout:", err)
      );
    }, LAYOUT_CHANGE_DELAY_MS);
    return () => clearTimeout(timer);
  }, [
    compositorOn,
    hasLayoutListener,
    exportLayout,
    compositorScenes,
    compositorOverlays,
    compositorLayout,
    compositorActiveScene,
  ]);
  const contextValue = useMemo(
    () => ({ ...hookReturn, compositor: enableCompositor ? compositor : null }),
    [hookReturn, compositor, enableCompositor]
  );

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

  // Update video preview when stream changes
  useEffect(() => {
    if (videoRef.current && mediaStream) {
      videoRef.current.srcObject = mediaStream;
      videoRef.current.play().catch(() => {});
    } else if (videoRef.current) {
      videoRef.current.srcObject = null;
    }
  }, [mediaStream]);

  // Auto-start camera once per endpoint. A camera that fails leaves the state
  // idle, and retrying from there would prompt the broadcaster forever.
  const autoStartedFor = useRef<string | null>(null);
  useEffect(() => {
    if (!autoStartCamera || !resolvedWhipUrl || state !== "idle") return;
    if (autoStartedFor.current === resolvedWhipUrl || !isMediaDevicesSupported()) return;
    autoStartedFor.current = resolvedWhipUrl;
    startCamera().catch(console.error);
  }, [autoStartCamera, resolvedWhipUrl, state, startCamera]);
  useEffect(
    () => () => {
      autoStartedFor.current = null;
    },
    []
  );

  // Handlers
  const handleStartCamera = useCallback(
    async (videoDeviceId?: string) => {
      try {
        await startCamera(videoDeviceId ? { videoDeviceId } : undefined);
      } catch (err) {
        console.error("Failed to start camera:", err);
      }
    },
    [startCamera]
  );

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

  const qualityProfiles = useMemo(() => getQualityProfiles(t), [t]);

  const handleKeyDown = useCallback(
    (e: React.KeyboardEvent) => {
      if (!enableCompositor) return;
      handleStudioSceneHotkey(e.nativeEvent, getController()?.getSceneManager());
    },
    [enableCompositor, getController]
  );

  // Context menu actions
  const copyWhipUrl = useCallback(() => {
    if (resolvedWhipUrl) {
      navigator.clipboard.writeText(resolvedWhipUrl).catch(console.error);
    }
  }, [resolvedWhipUrl]);

  const copyStreamInfo = useCallback(() => {
    const profile = qualityProfiles.find((p) => p.id === qualityProfile);
    const info = formatStreamInfo({
      state,
      quality: `${profile?.label ?? qualityProfile} (${profile?.description ?? ""})`,
      sourceCount: sources.length,
      whipUrl: resolvedWhipUrl,
    });
    navigator.clipboard.writeText(info).catch(console.error);
  }, [state, qualityProfile, qualityProfiles, sources.length, resolvedWhipUrl]);

  // Computed state
  const canAddSource = state !== "destroyed";
  const canStream = isCapturing && !isStreaming && resolvedWhipUrl && support.publish;
  const browserNotices = [
    !support.camera && t("browserCannotCapture"),
    !support.publish && t("browserCannotPublish"),
    enableCompositor && compositor.error && t("compositorUnavailable"),
  ].filter((notice): notice is string => !!notice);
  const _hasScreen = sources.some((s) => s.type === "screen");
  const statusText = getStatusText(state, t, reconnectionState);
  const statusBadgeClass = getStatusBadgeClass(state, isReconnecting);

  // Composable mode: render children instead of the default UI
  if (children) {
    return (
      <StudioI18nProvider locale={locale}>
        <StreamCrafterProvider value={contextValue}>
          <div
            className={cn("fw-sc-root", className)}
            data-theme={typeof theme === "string" && theme !== "default" ? theme : undefined}
            style={themeStyle}
          >
            {children}
          </div>
        </StreamCrafterProvider>
      </StudioI18nProvider>
    );
  }

  return (
    <StudioI18nProvider locale={locale}>
      <StreamCrafterProvider value={contextValue}>
        <ContextMenu>
          <ContextMenuTrigger asChild>
            <div
              className={cn("fw-sc-root", devMode && "fw-sc-root--devmode", className)}
              data-theme={typeof theme === "string" && theme !== "default" ? theme : undefined}
              style={themeStyle}
              tabIndex={-1}
              onKeyDown={handleKeyDown}
            >
              {/* Main content wrapper - takes remaining space when panel is open */}
              <div className={cn("fw-sc-main", devMode ? "flex-1 min-w-0" : "w-full")}>
                {/* Header */}
                <div className="fw-sc-header">
                  <span className="fw-sc-header-title">{t("streamCrafter")}</span>
                  <div className="fw-sc-header-status">
                    <StudioConnectionHealth stats={stats} isStreaming={isStreaming} />
                    <span className={statusBadgeClass}>{statusText}</span>
                  </div>
                </div>

                {/* Content area (preview + mixer) - responsive layout */}
                <div className="fw-sc-content">
                  {/* Preview wrapper for flex sizing */}
                  <div className="fw-sc-preview-wrapper">
                    {/* Video Preview (flush - no padding) */}
                    <div className="fw-sc-preview">
                      <video
                        ref={videoRef}
                        playsInline
                        muted
                        autoPlay
                        aria-label={t("streamPreview")}
                      />

                      {/* Empty State */}
                      {!mediaStream && (
                        <div className="fw-sc-preview-placeholder">
                          <CameraIcon size={48} />
                          <span>{t("addSourcePrompt")}</span>
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
                      {isStreaming && <div className="fw-sc-live-badge">{t("live")}</div>}

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
                        title={showSources ? t("collapseMixer") : t("expandMixer")}
                      >
                        <span>
                          {t("mixer")} ({sources.length})
                        </span>
                        {showSources ? (
                          <ChevronsRightIcon size={14} />
                        ) : (
                          <ChevronsLeftIcon size={14} />
                        )}
                      </div>
                      {showSources && (
                        <div className="fw-sc-section-body--flush">
                          <div className="fw-sc-sources">
                            {sources.map((source: MediaSource) => {
                              // Get visibility state for compositor mode
                              const layer = compositor.activeScene?.layers.find(
                                (l) => l.sourceId === source.id
                              );
                              const isVisibleInCompositor = isSourceVisibleInScene(
                                source,
                                compositor.activeScene ?? { layers: [] }
                              );

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
                                  t={t}
                                  isCompositorEnabled={enableCompositor}
                                  isVisibleInCompositor={isVisibleInCompositor}
                                  onVisibilityToggle={() => {
                                    if (!compositor.activeSceneId) return;
                                    if (layer) {
                                      compositor.setLayerVisibility(
                                        compositor.activeSceneId,
                                        layer.id,
                                        !layer.visible
                                      );
                                    } else {
                                      compositor.addLayer(compositor.activeSceneId, source.id);
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

                {showProducerPanel && enableCompositor && (
                  <StudioProducerPanel
                    compositor={compositor}
                    sources={sources}
                    media={hookReturn}
                  />
                )}

                {isCapturing && (
                  <StudioAudioControls
                    browserProcessing={audioProcessing}
                    onBrowserProcessingChange={handleAudioProcessingChange}
                  />
                )}

                {/* What this browser cannot do */}
                {browserNotices.map((notice) => (
                  <div key={notice} className="fw-sc-error fw-sc-error--warning" role="alert">
                    <div className="fw-sc-error-title">{t("warning")}</div>
                    <div className="fw-sc-error-message">{notice}</div>
                  </div>
                ))}

                {/* Error Display */}
                {(error || endpointError) && (
                  <div className="fw-sc-error">
                    <div className="fw-sc-error-title">{t("error")}</div>
                    <div className="fw-sc-error-message">{error || endpointError}</div>
                  </div>
                )}

                {/* No Endpoint Warning */}
                {!resolvedWhipUrl && !error && !endpointError && !isResolvingEndpoint && (
                  <div className="fw-sc-error fw-sc-error--warning">
                    <div className="fw-sc-error-title">{t("warning")}</div>
                    <div className="fw-sc-error-message">{t("configureWhipEndpoint")}</div>
                  </div>
                )}

                {/* Resolving Endpoint State */}
                {isResolvingEndpoint && (
                  <div className="fw-sc-error fw-sc-error--info">
                    <div className="fw-sc-error-title">{t("connecting")}</div>
                    <div className="fw-sc-error-message">{t("resolvingEndpoint")}</div>
                  </div>
                )}

                {/* Action Bar */}
                <div className="fw-sc-actions">
                  {/* Secondary actions: Camera, Screen, Settings */}
                  <CameraPicker
                    sources={sources}
                    disabled={!canAddSource || !support.camera}
                    getChoices={hookReturn.getCameraChoices}
                    onPick={handleStartCamera}
                  />
                  <button
                    type="button"
                    className="fw-sc-action-secondary"
                    onClick={handleStartScreenShare}
                    disabled={!canAddSource || !support.screen}
                    title={support.screen ? t("shareScreen") : t("screenShareUnsupported")}
                  >
                    <MonitorIcon size={18} />
                  </button>
                  <MediaFileButton onAdd={hookReturn.addMediaFile} disabled={!canAddSource} />
                  <StudioSettings autoClose={!devMode} defaultOpen={initialShowSettings}>
                    {devMode && (
                      <div className="fw-sc-dropdown-section">
                        <div className="fw-sc-dropdown-label">{t("debug")}</div>
                        <div className="fw-sc-dropdown-info">
                          <div className="fw-sc-dropdown-info-row">
                            <span>{t("state")}</span>
                            <span className="fw-sc-dropdown-info-value">{state}</span>
                          </div>
                          <div className="fw-sc-dropdown-info-row">
                            <span>{t("audio")}</span>
                            <span
                              className={cn(
                                "fw-sc-dropdown-info-value",
                                isMonitoring
                                  ? "fw-sc-dropdown-info-value--ok"
                                  : "fw-sc-dropdown-info-value--off"
                              )}
                            >
                              {isMonitoring ? t("active") : t("inactive")}
                            </span>
                          </div>
                          <div className="fw-sc-dropdown-info-row">
                            <span>{t("whip")}</span>
                            <span
                              className={cn(
                                "fw-sc-dropdown-info-value",
                                resolvedWhipUrl
                                  ? "fw-sc-dropdown-info-value--ok"
                                  : "fw-sc-dropdown-info-value--bad"
                              )}
                            >
                              {resolvedWhipUrl ? t("ok") : t("notSet")}
                            </span>
                          </div>
                        </div>
                      </div>
                    )}
                  </StudioSettings>
                  {/* Primary action: Go Live / Stop */}
                  {!isStreaming && !isReconnecting ? (
                    <button
                      type="button"
                      className="fw-sc-action-primary"
                      onClick={handleGoLive}
                      disabled={!canStream}
                    >
                      {state === "connecting" ? t("connecting") : t("goLive")}
                    </button>
                  ) : (
                    <button
                      type="button"
                      className="fw-sc-action-primary fw-sc-action-stop"
                      onClick={handleStopStreaming}
                    >
                      {t("stopStreaming")}
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
          <ContextMenuContent
            data-theme={typeof theme === "string" && theme !== "default" ? theme : undefined}
            style={themeStyle}
          >
            {resolvedWhipUrl && (
              <ContextMenuItem onClick={copyWhipUrl}>
                <span>{t("copyWhipUrl")}</span>
              </ContextMenuItem>
            )}
            <ContextMenuItem onClick={copyStreamInfo}>
              <span>{t("copyStreamInfo")}</span>
            </ContextMenuItem>
            {devMode && (
              <>
                <ContextMenuSeparator />
                <ContextMenuItem onClick={() => setIsAdvancedPanelOpen(!isAdvancedPanelOpen)}>
                  <SettingsIcon size={14} />
                  <span>{isAdvancedPanelOpen ? t("hideAdvanced") : t("advanced")}</span>
                </ContextMenuItem>
              </>
            )}
          </ContextMenuContent>
        </ContextMenu>
      </StreamCrafterProvider>
    </StudioI18nProvider>
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
