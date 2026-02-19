/**
 * useStreamCrafterV2 Hook
 * React hook with Phase 2 features:
 * - Multi-source support
 * - Audio mixing
 * - Quality switching
 * - Auto-reconnection
 */

import { useState, useEffect, useCallback, useRef } from "react";
import {
  IngestControllerV2,
  type IngestControllerConfigV2,
  type IngestState,
  type IngestStateContextV2,
  type IngestStats,
  type CaptureOptions,
  type ScreenCaptureOptions,
  type DeviceInfo,
  type MediaSource,
  type QualityProfile,
  type ReconnectionState,
  type EncoderOverrides,
  detectCapabilities,
  isWebCodecsEncodingPathSupported,
} from "@livepeer-frameworks/streamcrafter-core";

// Encoder stats from EncoderManager
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

export interface UseStreamCrafterV2Options extends IngestControllerConfigV2 {}

export interface UseStreamCrafterV2Return {
  // State
  state: IngestState;
  stateContext: IngestStateContextV2;
  isStreaming: boolean;
  isCapturing: boolean;
  isReconnecting: boolean;
  error: string | null;

  // Media
  mediaStream: MediaStream | null;
  sources: MediaSource[];

  // Quality
  qualityProfile: QualityProfile;
  setQualityProfile: (profile: QualityProfile) => Promise<void>;

  // Reconnection
  reconnectionState: ReconnectionState | null;

  // Capture actions
  startCamera: (options?: CaptureOptions) => Promise<MediaSource>;
  startScreenShare: (options?: ScreenCaptureOptions) => Promise<MediaSource | null>;
  addCustomSource: (stream: MediaStream, label: string) => MediaSource;
  removeSource: (sourceId: string) => void;
  stopCapture: () => Promise<void>;

  // Source management
  setSourceVolume: (sourceId: string, volume: number) => void;
  setSourceMuted: (sourceId: string, muted: boolean) => void;
  setSourceActive: (sourceId: string, active: boolean) => void;
  setPrimaryVideoSource: (sourceId: string) => void;

  // Master audio
  setMasterVolume: (volume: number) => void;
  getMasterVolume: () => number;

  // Streaming actions
  startStreaming: () => Promise<void>;
  stopStreaming: () => Promise<void>;

  // Device actions
  getDevices: () => Promise<DeviceInfo[]>;
  switchVideoDevice: (deviceId: string) => Promise<void>;
  switchAudioDevice: (deviceId: string) => Promise<void>;

  // Stats
  stats: IngestStats | null;
  getStats: () => Promise<IngestStats | null>;

  // Encoder
  useWebCodecs: boolean;
  isWebCodecsActive: boolean;
  isWebCodecsAvailable: boolean;
  encoderStats: EncoderStats | null;
  setUseWebCodecs: (enabled: boolean) => void;
  setEncoderOverrides: (overrides: EncoderOverrides) => void;

  // Controller access (advanced)
  getController: () => IngestControllerV2 | null;
}

export function useStreamCrafterV2(options: UseStreamCrafterV2Options): UseStreamCrafterV2Return {
  const [state, setState] = useState<IngestState>("idle");
  const [stateContext, setStateContext] = useState<IngestStateContextV2>({});
  const [mediaStream, setMediaStream] = useState<MediaStream | null>(null);
  const [sources, setSources] = useState<MediaSource[]>([]);
  const [error, setError] = useState<string | null>(null);
  const [stats, setStats] = useState<IngestStats | null>(null);
  const [qualityProfile, setQualityProfileState] = useState<QualityProfile>(
    options.profile || "broadcast"
  );
  const [reconnectionState, setReconnectionState] = useState<ReconnectionState | null>(null);

  // Encoder state
  const capabilities = detectCapabilities();
  const [useWebCodecs, setUseWebCodecsState] = useState<boolean>(
    options.useWebCodecs ?? capabilities.recommended === "webcodecs"
  );
  const [isWebCodecsActive, setIsWebCodecsActive] = useState<boolean>(false);
  const [encoderStats, setEncoderStats] = useState<EncoderStats | null>(null);

  const controllerRef = useRef<IngestControllerV2 | null>(null);
  // Track initial useWebCodecs value for controller creation (ref avoids dependency)
  const initialUseWebCodecsRef = useRef<boolean>(useWebCodecs);

  // Initialize controller
  useEffect(() => {
    // Use ref value to avoid recreating controller when useWebCodecs changes
    const controller = new IngestControllerV2({
      ...options,
      useWebCodecs: initialUseWebCodecsRef.current,
    });
    controllerRef.current = controller;

    // Set up event listeners
    const unsubState = controller.on("stateChange", (event) => {
      setState(event.state);
      setStateContext(event.context ?? {});
      setMediaStream(controller.getMediaStream());
      setSources(controller.getSources());
      const ctx = event.context as IngestStateContextV2 | undefined;
      if (ctx?.reconnection) {
        setReconnectionState(ctx.reconnection);
      }
    });

    const unsubStats = controller.on("statsUpdate", (newStats) => {
      setStats(newStats);
    });

    const unsubError = controller.on("error", (event) => {
      setError(event.error);
    });

    const unsubSourceAdded = controller.on("sourceAdded", () => {
      setSources(controller.getSources());
      setMediaStream(controller.getMediaStream());
    });

    const unsubSourceRemoved = controller.on("sourceRemoved", () => {
      setSources(controller.getSources());
      setMediaStream(controller.getMediaStream());
    });

    const unsubSourceUpdated = controller.on("sourceUpdated", () => {
      setSources(controller.getSources());
      // Update mediaStream when sources change (e.g., primary video switch)
      setMediaStream(controller.getMediaStream());
    });

    const unsubQualityChanged = controller.on("qualityChanged", (event) => {
      setQualityProfileState(event.profile);
    });

    const unsubReconnectionAttempt = controller.on("reconnectionAttempt", () => {
      setReconnectionState(controller.getReconnectionManager().getState());
    });

    // Listen for encoder stats from EncoderManager
    let encoderStatsCleanup: (() => void) | null = null;
    const setupEncoderStatsListener = () => {
      const encoder = controller.getEncoderManager();
      if (encoder) {
        encoderStatsCleanup = encoder.on("stats", (newStats) => {
          setEncoderStats(newStats as EncoderStats);
        });
      }
    };

    // Check encoder status when state changes
    const checkEncoderStatus = () => {
      setIsWebCodecsActive(controller.isWebCodecsActive());
      // Set up encoder stats listener if encoder is now active
      if (controller.isWebCodecsActive() && !encoderStatsCleanup) {
        setupEncoderStatsListener();
      }
    };

    // Listen for WebCodecs activation event
    const unsubWebCodecs = controller.on("webCodecsActive", (event: { active: boolean }) => {
      setIsWebCodecsActive(event.active);
      if (event.active) {
        setupEncoderStatsListener();
      }
    });

    // Hook into state changes to monitor encoder status
    let encoderCheckTimeout: ReturnType<typeof setTimeout> | null = null;
    const unsubStateForEncoder = controller.on("stateChange", (event) => {
      if (event.state === "streaming") {
        // Check after a delay as fallback (in case event was missed)
        encoderCheckTimeout = setTimeout(checkEncoderStatus, 200);
      } else if (event.state === "idle" || event.state === "capturing") {
        setIsWebCodecsActive(false);
        setEncoderStats(null);
        if (encoderStatsCleanup) {
          encoderStatsCleanup();
          encoderStatsCleanup = null;
        }
      }
    });

    return () => {
      if (encoderCheckTimeout) clearTimeout(encoderCheckTimeout);
      unsubState();
      unsubStats();
      unsubError();
      unsubWebCodecs();
      unsubSourceAdded();
      unsubSourceRemoved();
      unsubSourceUpdated();
      unsubQualityChanged();
      unsubReconnectionAttempt();
      unsubStateForEncoder();
      if (encoderStatsCleanup) {
        encoderStatsCleanup();
      }
      controller.destroy();
    };
  }, [
    options.whipUrl,
    options.iceServers,
    options.profile,
    options.debug,
    options.audioMixing,
    // Note: useWebCodecs intentionally NOT in deps - changing it should not recreate controller
    // The new value is read from useWebCodecsRef when startStreaming() is called
  ]);

  const startCamera = useCallback(async (captureOptions?: CaptureOptions) => {
    if (!controllerRef.current) {
      throw new Error("Controller not initialized");
    }
    setError(null);
    return controllerRef.current.startCamera(captureOptions);
  }, []);

  const startScreenShare = useCallback(async (captureOptions?: ScreenCaptureOptions) => {
    if (!controllerRef.current) {
      throw new Error("Controller not initialized");
    }
    setError(null);
    return controllerRef.current.startScreenShare(captureOptions);
  }, []);

  const addCustomSource = useCallback((stream: MediaStream, label: string) => {
    if (!controllerRef.current) {
      throw new Error("Controller not initialized");
    }
    return controllerRef.current.addCustomSource(stream, label);
  }, []);

  const removeSource = useCallback((sourceId: string) => {
    if (!controllerRef.current) return;
    controllerRef.current.removeSource(sourceId);
  }, []);

  const stopCapture = useCallback(async () => {
    if (!controllerRef.current) return;
    return controllerRef.current.stopCapture();
  }, []);

  const setSourceVolume = useCallback((sourceId: string, volume: number) => {
    if (!controllerRef.current) return;
    controllerRef.current.setSourceVolume(sourceId, volume);
  }, []);

  const setSourceMuted = useCallback((sourceId: string, muted: boolean) => {
    if (!controllerRef.current) return;
    controllerRef.current.setSourceMuted(sourceId, muted);
  }, []);

  const setSourceActive = useCallback((sourceId: string, active: boolean) => {
    if (!controllerRef.current) return;
    controllerRef.current.setSourceActive(sourceId, active);
  }, []);

  const setPrimaryVideoSource = useCallback((sourceId: string) => {
    if (!controllerRef.current) return;
    controllerRef.current.setPrimaryVideoSource(sourceId);
  }, []);

  const setMasterVolume = useCallback((volume: number) => {
    if (!controllerRef.current) return;
    controllerRef.current.setMasterVolume(volume);
  }, []);

  const getMasterVolume = useCallback(() => {
    if (!controllerRef.current) return 1;
    return controllerRef.current.getMasterVolume();
  }, []);

  const setQualityProfile = useCallback(async (profile: QualityProfile) => {
    if (!controllerRef.current) return;
    return controllerRef.current.setQualityProfile(profile);
  }, []);

  const startStreaming = useCallback(async () => {
    if (!controllerRef.current) {
      throw new Error("Controller not initialized");
    }
    setError(null);
    return controllerRef.current.startStreaming();
  }, []);

  const stopStreaming = useCallback(async () => {
    if (!controllerRef.current) return;
    return controllerRef.current.stopStreaming();
  }, []);

  const getDevices = useCallback(async () => {
    if (!controllerRef.current) return [];
    return controllerRef.current.getDevices();
  }, []);

  const switchVideoDevice = useCallback(async (deviceId: string) => {
    if (!controllerRef.current) return;
    return controllerRef.current.switchVideoDevice(deviceId);
  }, []);

  const switchAudioDevice = useCallback(async (deviceId: string) => {
    if (!controllerRef.current) return;
    return controllerRef.current.switchAudioDevice(deviceId);
  }, []);

  const getStats = useCallback(async () => {
    if (!controllerRef.current) return null;
    return controllerRef.current.getStats();
  }, []);

  const getController = useCallback(() => {
    return controllerRef.current;
  }, []);

  // Set useWebCodecs for next stream start
  // This updates UI state immediately and syncs to controller
  // Takes effect on next startStreaming() call (cannot change mid-stream)
  const setUseWebCodecs = useCallback((enabled: boolean) => {
    setUseWebCodecsState(enabled);
    // Also update the controller's internal setting
    if (controllerRef.current) {
      controllerRef.current.setUseWebCodecs(enabled);
    }
  }, []);

  // Set encoder overrides (resolution, bitrate, framerate, etc.)
  // Takes effect on next startStreaming() call (cannot change mid-stream)
  const setEncoderOverrides = useCallback((overrides: EncoderOverrides) => {
    if (controllerRef.current) {
      controllerRef.current.setEncoderOverrides(overrides);
    }
  }, []);

  return {
    // State
    state,
    stateContext,
    isStreaming: state === "streaming",
    isCapturing: state === "capturing" || state === "streaming",
    isReconnecting: state === "reconnecting",
    error,

    // Media
    mediaStream,
    sources,

    // Quality
    qualityProfile,
    setQualityProfile,

    // Reconnection
    reconnectionState,

    // Capture actions
    startCamera,
    startScreenShare,
    addCustomSource,
    removeSource,
    stopCapture,

    // Source management
    setSourceVolume,
    setSourceMuted,
    setSourceActive,
    setPrimaryVideoSource,

    // Master audio
    setMasterVolume,
    getMasterVolume,

    // Streaming actions
    startStreaming,
    stopStreaming,

    // Device actions
    getDevices,
    switchVideoDevice,
    switchAudioDevice,

    // Stats
    stats,
    getStats,

    // Encoder
    useWebCodecs,
    isWebCodecsActive,
    isWebCodecsAvailable: isWebCodecsEncodingPathSupported(),
    encoderStats,
    setUseWebCodecs,
    setEncoderOverrides,

    // Controller access
    getController,
  };
}
