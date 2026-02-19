/**
 * createPlayer() — Property-based facade for PlayerController.
 *
 * Provides a modern, ergonomic API using getters/setters instead of
 * explicit get/set methods. Follows the Q/M/S pattern:
 *
 * - **Queries** (getters): read player state
 * - **Mutations** (setters + methods): change player state
 * - **Subscriptions** (on/off): react to state changes
 *
 * @example
 * ```typescript
 * import { createPlayer } from '@livepeer-frameworks/player-core';
 *
 * const player = createPlayer({
 *   target: '#player',
 *   contentId: 'my-stream',
 *   gatewayUrl: 'https://gateway.example.com/graphql',
 * });
 *
 * // Queries (getters)
 * player.currentTime   // number
 * player.duration       // number
 * player.volume         // number
 * player.muted          // boolean
 * player.paused         // boolean
 * player.state          // PlayerState
 *
 * // Mutations (setters + methods)
 * player.volume = 0.5;
 * player.muted = true;
 * player.play();
 * player.seek(30);
 *
 * // Subscriptions
 * const unsub = player.on('stateChange', ({ state }) => { ... });
 * unsub();
 *
 * // Cleanup
 * player.destroy();
 * ```
 */

import type { PlayerControllerConfig, PlayerControllerEvents } from "../core/PlayerController";
import { PlayerController } from "../core/PlayerController";
import { applyTheme, applyThemeOverrides, clearTheme } from "../core/ThemeManager";
import { createReactiveState, type ReactiveState } from "./ReactiveState";
import { resolveSkin, registerSkin, type SkinDefinition, type ResolvedSkin } from "./SkinRegistry";
import { DEFAULT_BLUEPRINTS } from "./defaultBlueprints";
import { DEFAULT_STRUCTURE } from "./defaultStructure";
import { buildStructure } from "./StructureBuilder";
import type { BlueprintContext } from "./Blueprint";
import { createTranslator, type FwLocale, type TranslationStrings } from "../core/I18n";
import type { FwThemePreset, FwThemeOverrides } from "../core/ThemeManager";
import type {
  PlayerState,
  StreamState,
  ContentEndpoints,
  ContentMetadata,
  ContentType,
  PlaybackQuality,
  ABRMode,
} from "../types";
import type { StreamInfo } from "../core/PlayerInterface";

// ============================================================================
// Config
// ============================================================================

export interface CreatePlayerConfig {
  /** DOM element or CSS selector to mount the player */
  target: HTMLElement | string;

  /** Content identifier (stream name) */
  contentId: string;
  /** Content type */
  contentType?: ContentType;

  /** Pre-resolved endpoints (skip gateway) */
  endpoints?: ContentEndpoints;

  /** Gateway URL (for FrameWorks Gateway resolution) */
  gatewayUrl?: string;
  /** Direct MistServer base URL */
  mistUrl?: string;
  /** Auth token for private streams */
  authToken?: string;

  /** Playback options */
  autoplay?: boolean;
  muted?: boolean;
  controls?: boolean;
  poster?: string;

  /** Theme preset or custom overrides */
  theme?: FwThemePreset;
  themeOverrides?: FwThemeOverrides;

  /** Playback mode preference */
  playbackMode?: "auto" | "low-latency" | "quality" | "vod";

  /** Locale for i18n (default: "en") */
  locale?: FwLocale;

  /**
   * Skin: controls rendering mode.
   * - `string`: skin name from FwSkins registry
   * - `SkinDefinition`: inline skin definition
   * - `false`: headless, no UI rendered
   * - `undefined` (default): render with 'default' skin
   *
   * Set `controls: false` for headless mode (equivalent to `skin: false`).
   * Set `controls: true` or `controls: 'stock'` for native video controls.
   */
  skin?: string | SkinDefinition | false;

  /** Debug logging */
  debug?: boolean;
}

// ============================================================================
// Player Instance (returned object)
// ============================================================================

export interface PlayerInstance {
  // --- Queries (getters) ---

  /** Current player state (string enum: booting, loading, playing, etc.) */
  readonly playerState: PlayerState;
  /** @deprecated Use `playerState` instead. Kept for backwards compatibility. */
  readonly state: PlayerState;
  /** Per-property reactive subscriptions: `player.subscribe.on('currentTime', cb)` */
  readonly subscribe: ReactiveState;
  /** Current stream state (live streams) */
  readonly streamState: StreamState | null;
  /** Resolved endpoints */
  readonly endpoints: ContentEndpoints | null;
  /** Content metadata */
  readonly metadata: ContentMetadata | null;
  /** Stream info (sources, tracks) */
  readonly streamInfo: StreamInfo | null;

  /** Underlying video element (null if not ready) */
  readonly videoElement: HTMLVideoElement | null;
  /** Whether the player is ready */
  readonly ready: boolean;

  /** Current playback time in seconds */
  readonly currentTime: number;
  /** Duration in seconds */
  readonly duration: number;

  /** Volume (0–1, read/write) */
  volume: number;
  /** Muted state (read/write) */
  muted: boolean;
  /** Whether playback is paused */
  readonly paused: boolean;
  /** Whether currently playing */
  readonly playing: boolean;
  /** Whether currently buffering */
  readonly buffering: boolean;
  /** Whether playback has started at least once */
  readonly started: boolean;

  /** Current playback rate (read/write) */
  playbackRate: number;
  /** Whether loop is enabled (read/write) */
  loop: boolean;

  /** Whether content is live */
  readonly live: boolean;
  /** Whether near the live edge */
  readonly nearLive: boolean;
  /** Whether fullscreen is active */
  readonly fullscreen: boolean;
  /** Whether PiP is active */
  readonly pip: boolean;

  /** Current error (null if none) */
  readonly error: string | null;

  /** Playback quality metrics */
  readonly quality: PlaybackQuality | null;
  /** Current ABR mode (read/write) */
  abrMode: ABRMode;

  /** Current player info */
  readonly playerInfo: { name: string; shortname: string } | null;
  /** Current source info */
  readonly sourceInfo: { url: string; type: string } | null;

  /** Theme preset (write-only via setter) */
  theme: FwThemePreset | undefined;

  /** Container size */
  readonly size: { width: number; height: number };

  /** Player capabilities */
  readonly capabilities: PlayerCapabilities;

  // --- Mutations (methods) ---

  play(): Promise<void>;
  pause(): void;
  seek(time: number): void;
  seekBy(delta: number): void;
  jumpToLive(): void;
  skipForward(seconds?: number): void;
  skipBack(seconds?: number): void;

  togglePlay(): void;
  toggleMute(): void;
  toggleLoop(): void;
  toggleFullscreen(): Promise<void>;
  togglePiP(): Promise<void>;

  requestFullscreen(): Promise<void>;
  requestPiP(): Promise<void>;

  /** Get available quality levels */
  getQualities(): Array<{
    id: string;
    label: string;
    bitrate?: number;
    width?: number;
    height?: number;
    isAuto?: boolean;
    active?: boolean;
  }>;
  /** Select a quality level ('auto' for ABR) */
  selectQuality(id: string): void;

  /** Get available text tracks */
  getTextTracks(): Array<{ id: string; label: string; lang?: string; active: boolean }>;
  /** Select a text track (null to disable) */
  selectTextTrack(id: string | null): void;

  /** Get available audio tracks */
  getAudioTracks(): Array<{ id: string; label: string; lang?: string; active: boolean }>;
  /** Select an audio track */
  selectAudioTrack(id: string): void;

  /** Unified track listing (video, audio, text) */
  getTracks(): Array<{
    id: string;
    kind: "video" | "audio" | "text";
    label: string;
    lang?: string;
    active: boolean;
    bitrate?: number;
    width?: number;
    height?: number;
  }>;

  /** Retry playback after error */
  retry(): Promise<void>;
  /** Retry with fallback player */
  retryWithFallback(): Promise<boolean>;
  /** Reload the player entirely */
  reload(): Promise<void>;
  /** Clear current error */
  clearError(): void;

  /** Get player statistics */
  getStats(): Promise<unknown>;

  /** Capture a screenshot as a data URL */
  snapshot(type?: "png" | "jpeg" | "webp", quality?: number): string | null;
  /** Set video rotation (0, 90, 180, 270 degrees) */
  setRotation(degrees: number): void;
  /** Set mirror/flip mode */
  setMirror(horizontal: boolean): void;
  /** Whether the player uses direct rendering (WebGL/Canvas) */
  readonly directRendering: boolean;

  /** Apply custom theme overrides */
  setThemeOverrides(overrides: FwThemeOverrides): void;
  /** Clear all theme settings */
  clearTheme(): void;

  // --- Subscriptions ---

  on<K extends keyof PlayerControllerEvents>(
    event: K,
    listener: (data: PlayerControllerEvents[K]) => void
  ): () => void;

  /** Destroy the player and release all resources */
  destroy(): void;
}

export interface PlayerCapabilities {
  /** Whether fullscreen is supported */
  fullscreen: boolean;
  /** Whether PiP is supported */
  pip: boolean;
  /** Whether seeking is supported */
  seeking: boolean;
  /** Whether playback rate adjustment is supported */
  playbackRate: boolean;
  /** Whether the stream has audio */
  audio: boolean;
  /** Whether quality selection is available */
  qualitySelection: boolean;
  /** Whether text tracks are available */
  textTracks: boolean;
}

// ============================================================================
// Factory
// ============================================================================

export function createPlayer(config: CreatePlayerConfig): PlayerInstance {
  // Resolve container
  let container: HTMLElement;
  if (typeof config.target === "string") {
    const el = document.querySelector<HTMLElement>(config.target);
    if (!el) throw new Error(`createPlayer: element not found for selector "${config.target}"`);
    container = el;
  } else {
    container = config.target;
  }

  // Create controller
  const controllerConfig: PlayerControllerConfig = {
    contentId: config.contentId,
    contentType: config.contentType,
    endpoints: config.endpoints,
    gatewayUrl: config.gatewayUrl,
    mistUrl: config.mistUrl,
    authToken: config.authToken,
    autoplay: config.autoplay ?? true,
    muted: config.muted ?? true,
    controls: config.controls ?? true,
    poster: config.poster,
    debug: config.debug,
    playbackMode: config.playbackMode,
  };

  const ctrl = new PlayerController(controllerConfig);
  const reactiveState = createReactiveState(ctrl);
  let destroyed = false;
  let currentTheme = config.theme;

  // Resolve skin for blueprint rendering
  const shouldRenderSkin = config.skin !== false && config.controls !== false;
  let resolvedSkin: ResolvedSkin | null = null;
  let skinRoot: HTMLElement | null = null;

  if (shouldRenderSkin) {
    if (typeof config.skin === "object" && config.skin !== null) {
      // Inline skin definition — merge with defaults directly
      const inlineDef = config.skin as SkinDefinition;
      if (inlineDef.inherit) {
        // Register temporarily and use inheritance chain
        const tempName = `__inline_${Date.now()}`;
        registerSkin(tempName, inlineDef);
        resolvedSkin = resolveSkin(tempName);
      } else {
        resolvedSkin = {
          structure: inlineDef.structure?.main ?? DEFAULT_STRUCTURE,
          blueprints: { ...DEFAULT_BLUEPRINTS, ...inlineDef.blueprints },
          icons: { ...inlineDef.icons },
          tokens: { ...inlineDef.tokens },
          css: inlineDef.css?.skin ?? "",
        };
      }
    } else {
      resolvedSkin = resolveSkin(typeof config.skin === "string" ? config.skin : "default");
    }
  }

  // Timer tracking for blueprint cleanup
  const activeTimers = new Set<number>();

  // Attach to container
  ctrl
    .attach(container)
    .then(() => {
      // Apply initial theme after attach
      if (currentTheme || config.themeOverrides) {
        const root = container.querySelector<HTMLElement>(".fw-player-surface") ?? container;
        if (currentTheme && currentTheme !== "default") {
          applyTheme(root, currentTheme);
        }
        if (config.themeOverrides) {
          applyThemeOverrides(root, config.themeOverrides);
        }
      }

      // Build skin UI via blueprints
      if (resolvedSkin && shouldRenderSkin) {
        const locale = config.locale ?? "en";
        const t = createTranslator({ locale });
        const blueprintCtx: BlueprintContext = {
          get video() {
            return ctrl.getVideoElement();
          },
          subscribe: reactiveState,
          api: instance,
          fullscreen: {
            get supported() {
              return typeof document.fullscreenEnabled !== "undefined";
            },
            get active() {
              return ctrl.isFullscreen();
            },
            toggle: () => ctrl.toggleFullscreen(),
            request: () => ctrl.requestFullscreen(),
            exit: () => Promise.resolve(document.exitFullscreen?.()),
          },
          pip: {
            get supported() {
              return ctrl.isPiPSupported();
            },
            get active() {
              return ctrl.isPiPActive();
            },
            toggle: () => ctrl.togglePictureInPicture(),
          },
          get info() {
            return ctrl.getStreamInfo();
          },
          options: config,
          container,
          translate: (key, fallback) => t(key as keyof TranslationStrings, fallback),
          buildIcon: () => null,
          log: (msg) => {
            if (config.debug) console.log(`[Blueprint] ${msg}`);
          },
          timers: {
            setTimeout: (fn, ms) => {
              const id = window.setTimeout(fn, ms);
              activeTimers.add(id);
              return id;
            },
            clearTimeout: (id) => {
              window.clearTimeout(id);
              activeTimers.delete(id);
            },
            setInterval: (fn, ms) => {
              const id = window.setInterval(fn, ms);
              activeTimers.add(id);
              return id;
            },
            clearInterval: (id) => {
              window.clearInterval(id);
              activeTimers.delete(id);
            },
          },
        };

        // Apply skin tokens as CSS custom properties
        if (resolvedSkin.tokens) {
          for (const [prop, value] of Object.entries(resolvedSkin.tokens)) {
            container.style.setProperty(prop, value);
          }
        }

        // Inject skin CSS
        if (resolvedSkin.css) {
          const style = document.createElement("style");
          style.textContent = resolvedSkin.css;
          container.appendChild(style);
        }

        // Build the DOM tree
        skinRoot = buildStructure(resolvedSkin.structure, resolvedSkin.blueprints, blueprintCtx);
        if (skinRoot) {
          // The video container blueprint should wrap the existing player container content
          const videoSlot = skinRoot.querySelector(".fw-bp-video-container");
          if (videoSlot) {
            // Move existing container children (the <video> etc.) into the video slot
            while (container.firstChild) {
              videoSlot.appendChild(container.firstChild);
            }
          }
          container.appendChild(skinRoot);
        }
      }
    })
    .catch((err) => {
      console.error("[createPlayer] Failed to attach:", err);
    });

  // Helper to get theme root
  const getRoot = () => container.querySelector<HTMLElement>(".fw-player-surface") ?? container;

  // Build the instance with Object.defineProperties for getters/setters
  const instance: PlayerInstance = {
    // --- Queries (getters) ---
    get playerState() {
      return ctrl.getState();
    },
    get state() {
      return ctrl.getState();
    },
    get subscribe() {
      return reactiveState;
    },
    get streamState() {
      return ctrl.getStreamState();
    },
    get endpoints() {
      return ctrl.getEndpoints();
    },
    get metadata() {
      return ctrl.getMetadata();
    },
    get streamInfo() {
      return ctrl.getStreamInfo();
    },
    get videoElement() {
      return ctrl.getVideoElement();
    },
    get ready() {
      return ctrl.isReady();
    },

    get currentTime() {
      return ctrl.getCurrentTime();
    },
    get duration() {
      return ctrl.getDuration();
    },

    get volume() {
      return ctrl.getVolume();
    },
    set volume(v: number) {
      ctrl.setVolume(v);
    },

    get muted() {
      return ctrl.isMuted();
    },
    set muted(v: boolean) {
      ctrl.setMuted(v);
    },

    get paused() {
      return ctrl.isPaused();
    },
    get playing() {
      return ctrl.isPlaying();
    },
    get buffering() {
      return ctrl.isBuffering();
    },
    get started() {
      return ctrl.hasPlaybackStarted();
    },

    get playbackRate() {
      return ctrl.getPlaybackRate();
    },
    set playbackRate(r: number) {
      ctrl.setPlaybackRate(r);
    },

    get loop() {
      return ctrl.isLoopEnabled();
    },
    set loop(v: boolean) {
      ctrl.setLoopEnabled(v);
    },

    get live() {
      return ctrl.isEffectivelyLive();
    },
    get nearLive() {
      return ctrl.isNearLive();
    },
    get fullscreen() {
      return ctrl.isFullscreen();
    },
    get pip() {
      return ctrl.isPiPActive();
    },

    get error() {
      return ctrl.getError();
    },

    get quality() {
      return ctrl.getPlaybackQuality();
    },
    get abrMode() {
      return ctrl.getABRMode();
    },
    set abrMode(mode: ABRMode) {
      ctrl.setABRMode(mode);
    },

    get playerInfo() {
      return ctrl.getCurrentPlayerInfo();
    },
    get sourceInfo() {
      return ctrl.getCurrentSourceInfo();
    },

    get theme() {
      return currentTheme;
    },
    set theme(preset: FwThemePreset | undefined) {
      currentTheme = preset;
      const root = getRoot();
      clearTheme(root);
      if (preset && preset !== "default") {
        applyTheme(root, preset);
      }
    },

    get size() {
      const el = ctrl.getVideoElement();
      if (el) return { width: el.clientWidth, height: el.clientHeight };
      return { width: container.clientWidth, height: container.clientHeight };
    },

    get capabilities(): PlayerCapabilities {
      return {
        fullscreen: ctrl.isPiPSupported() || typeof document.fullscreenEnabled !== "undefined",
        pip: ctrl.isPiPSupported(),
        seeking: ctrl.canSeekStream(),
        playbackRate: ctrl.canAdjustPlaybackRate(),
        audio: ctrl.hasAudioTrack(),
        qualitySelection: ctrl.getQualities().length > 1,
        textTracks: ctrl.getTextTracks().length > 0,
      };
    },

    // --- Mutations (methods) ---
    play: () => ctrl.play(),
    pause: () => ctrl.pause(),
    seek: (t) => ctrl.seek(t),
    seekBy: (d) => ctrl.seekBy(d),
    jumpToLive: () => ctrl.jumpToLive(),
    skipForward: (s) => ctrl.skipForward(s),
    skipBack: (s) => ctrl.skipBack(s),

    togglePlay: () => ctrl.togglePlay(),
    toggleMute: () => ctrl.toggleMute(),
    toggleLoop: () => ctrl.toggleLoop(),
    toggleFullscreen: () => ctrl.toggleFullscreen(),
    togglePiP: () => ctrl.togglePictureInPicture(),

    requestFullscreen: () => ctrl.requestFullscreen(),
    requestPiP: () => ctrl.requestPiP(),

    getQualities: () => ctrl.getQualities(),
    selectQuality: (id) => ctrl.selectQuality(id),
    getTextTracks: () => ctrl.getTextTracks(),
    selectTextTrack: (id) => ctrl.selectTextTrack(id),
    getAudioTracks: () => ctrl.getAudioTracks(),
    selectAudioTrack: (id) => ctrl.selectAudioTrack(id),
    getTracks: () => ctrl.getTracks(),

    retry: () => ctrl.retry(),
    retryWithFallback: () => ctrl.retryWithFallback(),
    reload: () => ctrl.reload(),
    clearError: () => ctrl.clearError(),

    getStats: () => ctrl.getStats(),

    snapshot: (type?, quality?) => ctrl.snapshot(type, quality),
    setRotation: (degrees) => ctrl.setRotation(degrees),
    setMirror: (horizontal) => ctrl.setMirror(horizontal),
    get directRendering() {
      return ctrl.isDirectRendering();
    },

    setThemeOverrides: (overrides) => {
      applyThemeOverrides(getRoot(), overrides);
    },
    clearTheme: () => {
      currentTheme = undefined;
      clearTheme(getRoot());
    },

    // --- Subscriptions ---
    on: (event, listener) => ctrl.on(event, listener),

    // --- Lifecycle ---
    destroy: () => {
      if (destroyed) return;
      destroyed = true;
      reactiveState.off();
      for (const id of activeTimers) {
        window.clearTimeout(id);
        window.clearInterval(id);
      }
      activeTimers.clear();
      clearTheme(getRoot());
      ctrl.destroy();
    },
  };

  return instance;
}
