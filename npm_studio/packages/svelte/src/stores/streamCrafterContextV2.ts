/**
 * StreamCrafter Context Store V2
 * Svelte 5 store with Phase 2 features:
 * - Multi-source support
 * - Audio mixing
 * - Quality switching
 * - Auto-reconnection
 */

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

export interface StreamCrafterV2State {
  state: IngestState;
  stateContext: IngestStateContextV2;
  mediaStream: MediaStream | null;
  sources: MediaSource[];
  isStreaming: boolean;
  isCapturing: boolean;
  isReconnecting: boolean;
  error: string | null;
  stats: IngestStats | null;
  qualityProfile: QualityProfile;
  reconnectionState: ReconnectionState | null;
  // Encoder
  useWebCodecs: boolean;
  isWebCodecsActive: boolean;
  encoderStats: EncoderStats | null;
}

export interface StreamCrafterContextV2Store {
  subscribe: (fn: (state: StreamCrafterV2State) => void) => () => void;
  initialize: (config: IngestControllerConfigV2) => void;

  // Capture
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
  setMasterVolume: (volume: number) => void;
  getMasterVolume: () => number;

  // Quality
  setQualityProfile: (profile: QualityProfile) => Promise<void>;

  // Streaming
  startStreaming: () => Promise<void>;
  stopStreaming: () => Promise<void>;

  // Devices
  getDevices: () => Promise<DeviceInfo[]>;
  switchVideoDevice: (deviceId: string) => Promise<void>;
  switchAudioDevice: (deviceId: string) => Promise<void>;

  // Stats
  getStats: () => Promise<IngestStats | null>;

  // Encoder
  setUseWebCodecs: (enabled: boolean) => void;
  setEncoderOverrides: (overrides: EncoderOverrides) => void;

  // Controller access
  getController: () => IngestControllerV2 | null;

  // Lifecycle
  destroy: () => void;
}

import { writable } from "svelte/store";

export function createStreamCrafterContextV2(): StreamCrafterContextV2Store {
  // Detect capabilities for default useWebCodecs value
  const capabilities = detectCapabilities();
  const defaultUseWebCodecs = capabilities.recommended === "webcodecs";

  const initialState: StreamCrafterV2State = {
    state: "idle",
    stateContext: {},
    mediaStream: null,
    sources: [],
    isStreaming: false,
    isCapturing: false,
    isReconnecting: false,
    error: null,
    stats: null,
    qualityProfile: "broadcast",
    reconnectionState: null,
    // Encoder
    useWebCodecs: defaultUseWebCodecs,
    isWebCodecsActive: false,
    encoderStats: null,
  };

  const { subscribe, update } = writable<StreamCrafterV2State>(initialState);
  let controller: IngestControllerV2 | null = null;
  let encoderStatsCleanup: (() => void) | null = null;

  let _state: StreamCrafterV2State;
  subscribe((s) => (_state = s));

  function updateDerivedState(currentState: StreamCrafterV2State): StreamCrafterV2State {
    return {
      ...currentState,
      isStreaming: currentState.state === "streaming",
      isCapturing: currentState.state === "capturing" || currentState.state === "streaming",
      isReconnecting: currentState.state === "reconnecting",
    };
  }

  function applyUpdate(newStatePartial: Partial<StreamCrafterV2State>) {
    update((s) => updateDerivedState({ ...s, ...newStatePartial }));
  }

  function setupEncoderStatsListener() {
    if (encoderStatsCleanup) {
      encoderStatsCleanup();
      encoderStatsCleanup = null;
    }
    const encoder = controller?.getEncoderManager();
    if (encoder) {
      encoderStatsCleanup = encoder.on("stats", (newStats) => {
        applyUpdate({ encoderStats: newStats as EncoderStats });
      });
    }
  }

  function checkEncoderStatus() {
    const isActive = controller?.isWebCodecsActive() ?? false;
    applyUpdate({ isWebCodecsActive: isActive });
    if (isActive && !encoderStatsCleanup) {
      setupEncoderStatsListener();
    }
  }

  function setupController(config: IngestControllerConfigV2) {
    if (controller) {
      controller.destroy();
    }
    if (encoderStatsCleanup) {
      encoderStatsCleanup();
      encoderStatsCleanup = null;
    }

    // Use useWebCodecs from current state (allows toggling before initialize)
    controller = new IngestControllerV2({ ...config, useWebCodecs: _state.useWebCodecs });
    applyUpdate({ qualityProfile: config.profile || "broadcast" });

    controller.on("stateChange", (event) => {
      const contextAsAny = event.context as any; // Workaround for type mismatch with core
      applyUpdate({
        state: event.state,
        stateContext: event.context ?? {},
        mediaStream: controller?.getMediaStream() ?? null,
        sources: controller?.getSources() ?? [],
        reconnectionState: contextAsAny?.reconnection || null,
      });

      // Check encoder status when streaming starts
      if (event.state === "streaming") {
        setTimeout(checkEncoderStatus, 100);
      } else if (event.state === "idle" || event.state === "capturing") {
        applyUpdate({ isWebCodecsActive: false, encoderStats: null });
        if (encoderStatsCleanup) {
          encoderStatsCleanup();
          encoderStatsCleanup = null;
        }
      }
    });

    controller.on("statsUpdate", (stats) => {
      applyUpdate({ stats });
    });

    controller.on("error", (event) => {
      applyUpdate({ error: event.error });
    });

    controller.on("sourceAdded", () => {
      applyUpdate({
        sources: controller?.getSources() ?? [],
        mediaStream: controller?.getMediaStream() ?? null,
      });
    });

    controller.on("sourceRemoved", () => {
      applyUpdate({
        sources: controller?.getSources() ?? [],
        mediaStream: controller?.getMediaStream() ?? null,
      });
    });

    controller.on("sourceUpdated", () => {
      applyUpdate({
        sources: controller?.getSources() ?? [],
        mediaStream: controller?.getMediaStream() ?? null,
      });
    });

    controller.on("qualityChanged", (event) => {
      applyUpdate({ qualityProfile: event.profile });
    });

    controller.on("reconnectionAttempt", () => {
      applyUpdate({ reconnectionState: controller?.getReconnectionManager().getState() ?? null });
    });
  }

  return {
    subscribe,

    initialize(config: IngestControllerConfigV2) {
      setupController(config);
    },

    async startCamera(options?: CaptureOptions) {
      if (!controller) {
        throw new Error("Controller not initialized. Call initialize() first.");
      }
      applyUpdate({ error: null });
      return controller.startCamera(options);
    },

    async startScreenShare(options?: ScreenCaptureOptions) {
      if (!controller) {
        throw new Error("Controller not initialized. Call initialize() first.");
      }
      applyUpdate({ error: null });
      return controller.startScreenShare(options);
    },

    addCustomSource(stream: MediaStream, label: string) {
      if (!controller) {
        throw new Error("Controller not initialized. Call initialize() first.");
      }
      return controller.addCustomSource(stream, label);
    },

    removeSource(sourceId: string) {
      if (!controller) return;
      controller.removeSource(sourceId);
    },

    async stopCapture() {
      if (!controller) return;
      return controller.stopCapture();
    },

    setSourceVolume(sourceId: string, volume: number) {
      if (!controller) return;
      controller.setSourceVolume(sourceId, volume);
    },

    setSourceMuted(sourceId: string, muted: boolean) {
      if (!controller) return;
      controller.setSourceMuted(sourceId, muted);
    },

    setSourceActive(sourceId: string, active: boolean) {
      if (!controller) return;
      controller.setSourceActive(sourceId, active);
    },

    setPrimaryVideoSource(sourceId: string) {
      if (!controller) return;
      controller.setPrimaryVideoSource(sourceId);
    },

    setMasterVolume(volume: number) {
      if (!controller) return;
      controller.setMasterVolume(volume);
    },

    getMasterVolume() {
      if (!controller) return 1;
      return controller.getMasterVolume();
    },

    async setQualityProfile(profile: QualityProfile) {
      if (!controller) return;
      return controller.setQualityProfile(profile);
    },

    async startStreaming() {
      if (!controller) {
        throw new Error("Controller not initialized. Call initialize() first.");
      }
      applyUpdate({ error: null });
      return controller.startStreaming();
    },

    async stopStreaming() {
      if (!controller) return;
      return controller.stopStreaming();
    },

    async getDevices() {
      if (!controller) return [];
      return controller.getDevices();
    },

    async switchVideoDevice(deviceId: string) {
      if (!controller) return;
      return controller.switchVideoDevice(deviceId);
    },

    async switchAudioDevice(deviceId: string) {
      if (!controller) return;
      return controller.switchAudioDevice(deviceId);
    },

    async getStats() {
      if (!controller) return null;
      return controller.getStats();
    },

    setUseWebCodecs(enabled: boolean) {
      applyUpdate({ useWebCodecs: enabled });
      if (controller) {
        controller.setUseWebCodecs(enabled);
      }
    },

    setEncoderOverrides(overrides: EncoderOverrides) {
      if (!controller) return;
      controller.setEncoderOverrides(overrides);
    },

    getController() {
      return controller;
    },

    destroy() {
      if (encoderStatsCleanup) {
        encoderStatsCleanup();
        encoderStatsCleanup = null;
      }
      controller?.destroy();
      controller = null;
    },
  };
}

// Context API for sharing across components
import { getContext, setContext } from "svelte";

const STREAM_CRAFTER_V2_CONTEXT_KEY = Symbol("streamcrafter-v2-context");

export function setStreamCrafterContextV2(store: StreamCrafterContextV2Store): void {
  setContext(STREAM_CRAFTER_V2_CONTEXT_KEY, store);
}

export function getStreamCrafterContextV2(): StreamCrafterContextV2Store | undefined {
  return getContext<StreamCrafterContextV2Store>(STREAM_CRAFTER_V2_CONTEXT_KEY);
}
