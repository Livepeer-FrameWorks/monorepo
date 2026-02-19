/**
 * createStreamCrafter() — property-based facade for StreamCrafter.
 *
 * Follows the same Q/M/S (Queries / Mutations / Subscriptions) pattern
 * as the player's createPlayer() facade.
 *
 * @example
 * ```ts
 * import { createStreamCrafter } from '@livepeer-frameworks/streamcrafter-core/vanilla';
 *
 * const studio = createStreamCrafter({
 *   whipUrl: 'https://ingest.example.com/webrtc/key',
 *   profile: 'broadcast',
 *   theme: 'dracula',
 *   locale: 'es',
 * });
 *
 * // Queries (getters)
 * studio.state           // IngestState
 * studio.streaming       // boolean
 * studio.sources         // MediaSource[]
 * studio.masterVolume    // number
 *
 * // Mutations (setters + methods)
 * studio.masterVolume = 0.8
 * studio.profile = 'professional'
 * studio.theme = 'nord'
 * await studio.startCamera()
 * await studio.goLive()
 *
 * // Subscriptions
 * const unsub = studio.on('stateChange', ({ state }) => { ... })
 * unsub()
 *
 * studio.destroy()
 * ```
 */

import { IngestControllerV2 } from "../core/IngestControllerV2";
import type { SceneManager } from "../core/SceneManager";
import type {
  IngestControllerConfigV2,
  IngestState,
  IngestStateContextV2,
  IngestStats,
  CaptureOptions,
  ScreenCaptureOptions,
  CompositorConfig,
  DeviceInfo,
  MediaSource,
  QualityProfile,
  SourceAddedEvent,
  SourceRemovedEvent,
  SourceUpdatedEvent,
  EncoderOverrides,
} from "../types";
import type { StudioReactiveState } from "./StudioReactiveState";
import { createStudioReactiveState } from "./StudioReactiveState";
import type { FwThemePreset, StudioThemeOverrides } from "../StudioThemeManager";
import {
  applyStudioTheme,
  applyStudioThemeOverrides,
  clearStudioTheme,
} from "../StudioThemeManager";
import type { StudioLocale, StudioTranslationStrings } from "../I18n";
import { createStudioTranslator } from "../I18n";
import type { StudioKeyMap } from "../StudioKeyMap";
import { DEFAULT_STUDIO_KEY_MAP, buildStudioKeyLookup, matchStudioKey } from "../StudioKeyMap";

// ============================================================================
// Config
// ============================================================================

export interface CreateStreamCrafterConfig extends IngestControllerConfigV2 {
  /** CSS selector or element to mount into (optional — headless if omitted) */
  target?: string | HTMLElement;
  /** Theme preset name or custom overrides */
  theme?: FwThemePreset | StudioThemeOverrides;
  /** Locale for built-in translations */
  locale?: StudioLocale;
  /** Custom translation overrides */
  translations?: Partial<StudioTranslationStrings>;
  /** Custom keyboard shortcut bindings */
  keyMap?: Partial<StudioKeyMap>;
}

// ============================================================================
// Instance
// ============================================================================

export interface StreamCrafterInstance {
  /** Per-property reactive subscriptions. `studio.reactiveState.on('streaming', cb)` */
  readonly reactiveState: StudioReactiveState;

  // --- Queries (getters) ---
  readonly state: IngestState;
  readonly stateContext: IngestStateContextV2;
  readonly streaming: boolean;
  readonly capturing: boolean;
  readonly reconnecting: boolean;
  readonly sources: MediaSource[];
  readonly primaryVideo: MediaSource | null;
  readonly stats: Promise<IngestStats | null>;
  readonly devices: Promise<DeviceInfo[]>;
  readonly mediaStream: MediaStream | null;
  readonly compositorEnabled: boolean;
  readonly webCodecsActive: boolean;
  readonly encoderOverrides: EncoderOverrides;
  readonly recording: boolean;
  readonly recordingDuration: number;
  readonly recordingFileSize: number;
  readonly codecFamily: string;
  readonly adaptiveBitrateActive: boolean;
  readonly currentBitrate: number | null;
  readonly congestionLevel: string | null;
  readonly sceneManager: SceneManager | null;

  // --- Read/write properties ---
  masterVolume: number;
  profile: QualityProfile;
  theme: FwThemePreset | StudioThemeOverrides;
  useWebCodecs: boolean;

  // --- Mutations (methods) ---
  startCamera(options?: CaptureOptions): Promise<MediaSource>;
  startScreenShare(options?: ScreenCaptureOptions): Promise<MediaSource | null>;
  addCustomSource(stream: MediaStream, label: string): MediaSource;
  removeSource(id: string): void;
  stopCapture(): Promise<void>;

  setSourceVolume(id: string, volume: number): void;
  setSourceMuted(id: string, muted: boolean): void;
  setSourceActive(id: string, active: boolean): void;
  setPrimaryVideo(id: string): void;

  goLive(): Promise<void>;
  stop(): Promise<void>;

  switchVideoDevice(deviceId: string): Promise<void>;
  switchAudioDevice(deviceId: string): Promise<void>;

  setEncoderOverrides(overrides: EncoderOverrides): void;

  startRecording(): void;
  stopRecording(): Blob | null;
  pauseRecording(): void;
  resumeRecording(): void;

  enableCompositor(config?: Partial<CompositorConfig>): Promise<void>;
  disableCompositor(): void;

  /** Translate a key using the configured locale/translations. */
  t(key: keyof StudioTranslationStrings, vars?: Record<string, string | number>): string;

  destroy(): void;

  // --- Subscriptions ---
  on(
    event: "stateChange",
    handler: (e: { state: IngestState; context?: IngestStateContextV2 }) => void
  ): () => void;
  on(event: "error", handler: (e: { error: string; recoverable: boolean }) => void): () => void;
  on(event: "sourceAdded", handler: (e: SourceAddedEvent) => void): () => void;
  on(event: "sourceRemoved", handler: (e: SourceRemovedEvent) => void): () => void;
  on(event: "sourceUpdated", handler: (e: SourceUpdatedEvent) => void): () => void;
  on(event: "statsUpdate", handler: (stats: IngestStats) => void): () => void;
  on(event: "deviceChange", handler: (e: { devices: DeviceInfo[] }) => void): () => void;
  on(
    event: "qualityChanged",
    handler: (e: { profile: QualityProfile; previousProfile: QualityProfile }) => void
  ): () => void;
  on(
    event: "reconnectionAttempt",
    handler: (e: { attempt: number; maxAttempts: number }) => void
  ): () => void;
  on(event: "reconnectionSuccess", handler: () => void): () => void;
  on(event: "reconnectionFailed", handler: (e: { error: string }) => void): () => void;
  on(event: "webCodecsActive", handler: (e: { active: boolean }) => void): () => void;
  on(
    event: "bitrateChanged",
    handler: (e: { bitrate: number; previousBitrate: number; congestion: string }) => void
  ): () => void;
  on(
    event: "congestionChanged",
    handler: (e: { level: string; packetLoss: number; rtt: number }) => void
  ): () => void;
  on(event: "recordingStarted", handler: () => void): () => void;
  on(
    event: "recordingStopped",
    handler: (e: { blob: Blob; duration: number; fileSize: number }) => void
  ): () => void;
  on(event: "recordingPaused", handler: () => void): () => void;
  on(event: "recordingResumed", handler: () => void): () => void;
  on(
    event: "recordingProgress",
    handler: (e: { duration: number; fileSize: number }) => void
  ): () => void;
  on(event: string, handler: (...args: any[]) => void): () => void;
}

// ============================================================================
// Factory
// ============================================================================

export function createStreamCrafter(config: CreateStreamCrafterConfig): StreamCrafterInstance {
  // Resolve target element
  let container: HTMLElement | null = null;
  if (config.target) {
    if (typeof config.target === "string") {
      const el = document.querySelector<HTMLElement>(config.target);
      if (!el) throw new Error(`createStreamCrafter: element not found for "${config.target}"`);
      container = el;
    } else {
      container = config.target;
    }
  }

  // Create controller
  const ctrl = new IngestControllerV2(config);

  // Theme
  let currentTheme: FwThemePreset | StudioThemeOverrides = config.theme ?? "default";
  function applyCurrentTheme() {
    if (!container) return;
    if (typeof currentTheme === "string") {
      if (currentTheme === "default") {
        clearStudioTheme(container);
      } else {
        applyStudioTheme(container, currentTheme);
      }
    } else {
      clearStudioTheme(container);
      applyStudioThemeOverrides(container, currentTheme as StudioThemeOverrides);
    }
  }
  if (container) applyCurrentTheme();

  // i18n
  const t = createStudioTranslator({
    locale: config.locale,
    translations: config.translations,
  });

  // Hotkeys
  const keyMap: StudioKeyMap = { ...DEFAULT_STUDIO_KEY_MAP, ...config.keyMap };
  const keyLookup = buildStudioKeyLookup(keyMap);

  const handleKeyDown = (e: KeyboardEvent) => {
    // Skip if user is typing in an input
    const tag = (e.target as HTMLElement)?.tagName;
    if (tag === "INPUT" || tag === "TEXTAREA" || tag === "SELECT") return;

    const action = matchStudioKey(e, keyLookup);
    if (!action) return;

    e.preventDefault();
    switch (action) {
      case "toggleStream":
        if (ctrl.isStreaming()) ctrl.stopStreaming();
        else ctrl.startStreaming();
        break;
      case "toggleMute":
        ctrl.setMasterVolume(ctrl.getMasterVolume() > 0 ? 0 : 1);
        break;
      case "addCamera":
        ctrl.startCamera();
        break;
      case "shareScreen":
        ctrl.startScreenShare();
        break;
      case "toggleSettings":
      case "toggleAdvanced":
        // These are UI-level concerns, emit custom events for the wrapper to handle
        container?.dispatchEvent(new CustomEvent(`fw-sc-${action}`, { bubbles: true }));
        break;
      case "nextScene": {
        const sm = ctrl.getSceneManager?.();
        if (sm) {
          const scenes = sm.getAllScenes();
          const active = sm.getActiveScene();
          const idx = active ? scenes.findIndex((s: any) => s.id === active.id) : -1;
          if (idx >= 0 && idx < scenes.length - 1) sm.setActiveScene(scenes[idx + 1].id);
        }
        break;
      }
      case "prevScene": {
        const sm = ctrl.getSceneManager?.();
        if (sm) {
          const scenes = sm.getAllScenes();
          const active = sm.getActiveScene();
          const idx = active ? scenes.findIndex((s: any) => s.id === active.id) : -1;
          if (idx > 0) sm.setActiveScene(scenes[idx - 1].id);
        }
        break;
      }
    }
  };

  if (container) {
    container.addEventListener("keydown", handleKeyDown);
    // Ensure the container is focusable
    if (!container.getAttribute("tabindex")) {
      container.setAttribute("tabindex", "-1");
    }
  }

  // Reactive state
  const rs = createStudioReactiveState(ctrl);

  // Build instance
  const instance: StreamCrafterInstance = {
    reactiveState: rs,

    // Queries
    get state() {
      return ctrl.getState();
    },
    get stateContext() {
      return ctrl.getStateContext();
    },
    get streaming() {
      return ctrl.isStreaming();
    },
    get capturing() {
      return ctrl.isCapturing();
    },
    get reconnecting() {
      return ctrl.isReconnecting();
    },
    get sources() {
      return ctrl.getSources();
    },
    get primaryVideo() {
      return ctrl.getPrimaryVideoSource();
    },
    get stats() {
      return ctrl.getStats();
    },
    get devices() {
      return ctrl.getDevices();
    },
    get mediaStream() {
      return ctrl.getMediaStream();
    },
    get compositorEnabled() {
      return ctrl.isCompositorEnabled();
    },
    get webCodecsActive() {
      return ctrl.isWebCodecsActive();
    },
    get encoderOverrides() {
      return ctrl.getEncoderOverrides();
    },
    get recording() {
      return ctrl.isRecordingActive();
    },
    get recordingDuration() {
      return ctrl.getRecordingDuration();
    },
    get recordingFileSize() {
      return ctrl.getRecordingFileSize();
    },
    get codecFamily() {
      return ctrl.getVideoCodecFamily();
    },
    get adaptiveBitrateActive() {
      return ctrl.isAdaptiveBitrateActive();
    },
    get currentBitrate() {
      return ctrl.getCurrentBitrate();
    },
    get congestionLevel() {
      return ctrl.getCongestionLevel();
    },
    get sceneManager() {
      return ctrl.getSceneManager();
    },

    // Read/write
    get masterVolume() {
      return ctrl.getMasterVolume();
    },
    set masterVolume(v: number) {
      ctrl.setMasterVolume(v);
    },

    get profile() {
      return ctrl.getQualityProfile();
    },
    set profile(p: QualityProfile) {
      ctrl.setQualityProfile(p);
    },

    get theme() {
      return currentTheme;
    },
    set theme(t: FwThemePreset | StudioThemeOverrides) {
      currentTheme = t;
      applyCurrentTheme();
    },

    get useWebCodecs() {
      return ctrl.getUseWebCodecs();
    },
    set useWebCodecs(v: boolean) {
      ctrl.setUseWebCodecs(v);
    },

    // Mutations
    startCamera: (opts?) => ctrl.startCamera(opts),
    startScreenShare: (opts?) => ctrl.startScreenShare(opts),
    addCustomSource: (stream, label) => ctrl.addCustomSource(stream, label),
    removeSource: (id) => ctrl.removeSource(id),
    stopCapture: () => ctrl.stopCapture(),

    setSourceVolume: (id, vol) => ctrl.setSourceVolume(id, vol),
    setSourceMuted: (id, muted) => ctrl.setSourceMuted(id, muted),
    setSourceActive: (id, active) => ctrl.setSourceActive(id, active),
    setPrimaryVideo: (id) => ctrl.setPrimaryVideoSource(id),

    goLive: () => ctrl.startStreaming(),
    stop: () => ctrl.stopStreaming(),

    switchVideoDevice: (id) => ctrl.switchVideoDevice(id),
    switchAudioDevice: (id) => ctrl.switchAudioDevice(id),

    setEncoderOverrides: (overrides) => ctrl.setEncoderOverrides(overrides),

    startRecording: () => ctrl.startRecording(),
    stopRecording: () => ctrl.stopRecording(),
    pauseRecording: () => ctrl.pauseRecording(),
    resumeRecording: () => ctrl.resumeRecording(),

    enableCompositor: (config?) => ctrl.enableCompositor(config),
    disableCompositor: () => ctrl.disableCompositor(),

    t,

    destroy() {
      rs.destroy();
      if (container) {
        container.removeEventListener("keydown", handleKeyDown);
        clearStudioTheme(container);
      }
      ctrl.destroy();
    },

    // Subscriptions
    on(event: string, handler: (...args: any[]) => void): () => void {
      return ctrl.on(event as any, handler as any);
    },
  };

  return instance;
}
