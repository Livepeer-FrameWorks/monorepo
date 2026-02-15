/**
 * PlayerControllerHost — Lit ReactiveController wrapping the headless PlayerController.
 * Direct port of usePlayerController.ts from player-react.
 */
import type { ReactiveController, ReactiveControllerHost } from "lit";
import {
  PlayerController,
  type PlayerControllerConfig,
  type PlayerState,
  type StreamState,
  type StreamInfo,
  type PlaybackQuality,
  type ContentEndpoints,
  type ContentMetadata,
  type ClassifiedError,
} from "@livepeer-frameworks/player-core";

export interface PlayerControllerHostState {
  state: PlayerState;
  streamState: StreamState | null;
  endpoints: ContentEndpoints | null;
  metadata: ContentMetadata | null;
  videoElement: HTMLVideoElement | null;
  currentTime: number;
  duration: number;
  isPlaying: boolean;
  isPaused: boolean;
  isBuffering: boolean;
  isMuted: boolean;
  volume: number;
  error: string | null;
  errorDetails: ClassifiedError["details"] | null;
  isPassiveError: boolean;
  hasPlaybackStarted: boolean;
  isHoldingSpeed: boolean;
  holdSpeed: number;
  isHovering: boolean;
  shouldShowControls: boolean;
  isLoopEnabled: boolean;
  isFullscreen: boolean;
  isPiPActive: boolean;
  isEffectivelyLive: boolean;
  shouldShowIdleScreen: boolean;
  currentPlayerInfo: { name: string; shortname: string } | null;
  currentSourceInfo: { url: string; type: string } | null;
  playbackQuality: PlaybackQuality | null;
  subtitlesEnabled: boolean;
  qualities: Array<{
    id: string;
    label: string;
    bitrate?: number;
    width?: number;
    height?: number;
    isAuto?: boolean;
    active?: boolean;
  }>;
  textTracks: Array<{ id: string; label: string; language?: string; active: boolean }>;
  streamInfo: StreamInfo | null;
  toast: { message: string; timestamp: number } | null;
}

const initialState: PlayerControllerHostState = {
  state: "booting",
  streamState: null,
  endpoints: null,
  metadata: null,
  videoElement: null,
  currentTime: 0,
  duration: NaN,
  isPlaying: false,
  isPaused: true,
  isBuffering: false,
  isMuted: true,
  volume: 1,
  error: null,
  errorDetails: null,
  isPassiveError: false,
  hasPlaybackStarted: false,
  isHoldingSpeed: false,
  holdSpeed: 2,
  isHovering: false,
  shouldShowControls: false,
  isLoopEnabled: false,
  isFullscreen: false,
  isPiPActive: false,
  isEffectivelyLive: false,
  shouldShowIdleScreen: true,
  currentPlayerInfo: null,
  currentSourceInfo: null,
  playbackQuality: null,
  subtitlesEnabled: false,
  qualities: [],
  textTracks: [],
  streamInfo: null,
  toast: null,
};

type HostElement = ReactiveControllerHost & HTMLElement;

export class PlayerControllerHost implements ReactiveController {
  host: HostElement;
  private controller: PlayerController | null = null;
  private unsubs: Array<() => void> = [];
  private currentConfig: PlayerControllerConfig | null = null;

  s: PlayerControllerHostState = { ...initialState };

  constructor(host: HostElement) {
    this.host = host;
    host.addController(this);
  }

  // ---- Configuration & Lifecycle ----

  configure(config: PlayerControllerConfig) {
    this.currentConfig = config;
  }

  async attach(container: HTMLDivElement) {
    if (!this.currentConfig) return;
    this.teardown();

    const controller = new PlayerController({
      contentId: this.currentConfig.contentId,
      contentType: this.currentConfig.contentType,
      endpoints: this.currentConfig.endpoints,
      gatewayUrl: this.currentConfig.gatewayUrl,
      mistUrl: this.currentConfig.mistUrl,
      authToken: this.currentConfig.authToken,
      autoplay: this.currentConfig.autoplay,
      muted: this.currentConfig.muted,
      controls: this.currentConfig.controls,
      poster: this.currentConfig.poster,
      debug: this.currentConfig.debug,
    });

    this.controller = controller;
    this.subscribeToEvents(controller);

    this.update({ isLoopEnabled: controller.isLoopEnabled() });

    try {
      await controller.attach(container);
    } catch (err) {
      console.warn("[PlayerControllerHost] Attach failed:", err);
    }
  }

  hostConnected() {
    // Controller attachment happens in firstUpdated of the host element
  }

  hostDisconnected() {
    this.teardown();
    this.s = { ...initialState };
  }

  private teardown() {
    this.unsubs.forEach((fn) => fn());
    this.unsubs = [];
    this.controller?.destroy();
    this.controller = null;
  }

  // ---- State Updates ----

  private update(partial: Partial<PlayerControllerHostState>) {
    Object.assign(this.s, partial);
    this.host.requestUpdate();
  }

  private syncState() {
    if (!this.controller) return;
    const c = this.controller;
    this.update({
      isPlaying: c.isPlaying(),
      isPaused: c.isPaused(),
      isBuffering: c.isBuffering(),
      isMuted: c.isMuted(),
      volume: c.getVolume(),
      hasPlaybackStarted: c.hasPlaybackStarted(),
      shouldShowControls: c.shouldShowControls(),
      shouldShowIdleScreen: c.shouldShowIdleScreen(),
      playbackQuality: c.getPlaybackQuality(),
      isLoopEnabled: c.isLoopEnabled(),
      subtitlesEnabled: c.isSubtitlesEnabled(),
      qualities: c.getQualities(),
      streamInfo: c.getStreamInfo(),
    });
  }

  // ---- Event Subscriptions (mirrors usePlayerController exactly) ----

  private subscribeToEvents(controller: PlayerController) {
    const u = this.unsubs;

    u.push(
      controller.on("stateChange", ({ state }) => {
        this.update({ state });
        this.dispatchEvent("fw-state-change", { state });
      })
    );

    u.push(
      controller.on("streamStateChange", ({ state: streamState }) => {
        this.update({
          streamState,
          metadata: controller.getMetadata(),
          isEffectivelyLive: controller.isEffectivelyLive(),
          shouldShowIdleScreen: controller.shouldShowIdleScreen(),
        });
        this.dispatchEvent("fw-stream-state", { state: streamState });
      })
    );

    u.push(
      controller.on("timeUpdate", ({ currentTime, duration }) => {
        this.update({ currentTime, duration });
        this.dispatchEvent("fw-time-update", { currentTime, duration });
      })
    );

    u.push(
      controller.on("error", ({ error }) => {
        this.update({
          error,
          isPassiveError: controller.isPassiveError(),
        });
        this.dispatchEvent("fw-error", { error });
      })
    );

    u.push(
      controller.on("errorCleared", () => {
        this.update({ error: null, isPassiveError: false });
      })
    );

    u.push(
      controller.on("ready", ({ videoElement }) => {
        this.update({
          videoElement,
          endpoints: controller.getEndpoints(),
          metadata: controller.getMetadata(),
          streamInfo: controller.getStreamInfo(),
          isEffectivelyLive: controller.isEffectivelyLive(),
          shouldShowIdleScreen: controller.shouldShowIdleScreen(),
          currentPlayerInfo: controller.getCurrentPlayerInfo(),
          currentSourceInfo: controller.getCurrentSourceInfo(),
          qualities: controller.getQualities(),
        });
        this.dispatchEvent("fw-ready", { videoElement });

        const handleVideoEvent = () => {
          if (this.controller?.shouldSuppressVideoEvents?.()) return;
          this.syncState();
        };
        videoElement.addEventListener("play", handleVideoEvent);
        videoElement.addEventListener("pause", handleVideoEvent);
        videoElement.addEventListener("waiting", handleVideoEvent);
        videoElement.addEventListener("playing", handleVideoEvent);
        u.push(() => {
          videoElement.removeEventListener("play", handleVideoEvent);
          videoElement.removeEventListener("pause", handleVideoEvent);
          videoElement.removeEventListener("waiting", handleVideoEvent);
          videoElement.removeEventListener("playing", handleVideoEvent);
        });
      })
    );

    u.push(
      controller.on("playerSelected", ({ player: _player, source }) => {
        this.update({
          currentPlayerInfo: controller.getCurrentPlayerInfo(),
          currentSourceInfo: { url: source.url, type: source.type },
          qualities: controller.getQualities(),
        });
      })
    );

    u.push(
      controller.on("volumeChange", ({ volume, muted }) => {
        this.update({ volume, isMuted: muted });
        this.dispatchEvent("fw-volume-change", { volume, muted });
      })
    );

    u.push(
      controller.on("loopChange", ({ isLoopEnabled }) => {
        this.update({ isLoopEnabled });
      })
    );

    u.push(
      controller.on("fullscreenChange", ({ isFullscreen }) => {
        this.update({ isFullscreen });
        this.dispatchEvent("fw-fullscreen-change", { isFullscreen });
      })
    );

    u.push(
      controller.on("pipChange", ({ isPiP }) => {
        this.update({ isPiPActive: isPiP });
        this.dispatchEvent("fw-pip-change", { isPiP });
      })
    );

    u.push(
      controller.on("holdSpeedStart", ({ speed }) => {
        this.update({ isHoldingSpeed: true, holdSpeed: speed });
      })
    );

    u.push(
      controller.on("holdSpeedEnd", () => {
        this.update({ isHoldingSpeed: false });
      })
    );

    u.push(
      controller.on("hoverStart", () => {
        this.update({ isHovering: true, shouldShowControls: true });
      })
    );

    u.push(
      controller.on("hoverEnd", () => {
        this.update({
          isHovering: false,
          shouldShowControls: controller.shouldShowControls(),
        });
      })
    );

    u.push(
      controller.on("captionsChange", ({ enabled }) => {
        this.update({ subtitlesEnabled: enabled });
      })
    );

    u.push(
      controller.on("protocolSwapped", (data) => {
        const message = `Switched to ${data.toProtocol}`;
        this.update({ toast: { message, timestamp: Date.now() } });
        this.dispatchEvent("fw-protocol-swapped", data);
      })
    );

    u.push(
      controller.on("playbackFailed", (data) => {
        this.update({
          error: data.message,
          errorDetails: data.details ?? null,
          isPassiveError: false,
        });
        this.dispatchEvent("fw-playback-failed", {
          code: data.code,
          message: data.message,
        });
      })
    );
  }

  // ---- Event Dispatching ----

  private dispatchEvent(name: string, detail: unknown) {
    this.host.dispatchEvent(new CustomEvent(name, { detail, bubbles: true, composed: true }));
  }

  // ---- Action Methods ----

  async play() {
    await this.controller?.play();
  }
  pause() {
    this.controller?.pause();
  }
  togglePlay() {
    this.controller?.togglePlay();
  }
  seek(time: number) {
    this.controller?.seek(time);
  }
  seekBy(delta: number) {
    this.controller?.seekBy(delta);
  }
  jumpToLive() {
    this.controller?.jumpToLive();
  }
  setVolume(volume: number) {
    this.controller?.setVolume(volume);
  }
  toggleMute() {
    this.controller?.toggleMute();
  }
  toggleLoop() {
    this.controller?.toggleLoop();
  }
  async toggleFullscreen() {
    await this.controller?.toggleFullscreen();
  }
  async togglePiP() {
    await this.controller?.togglePictureInPicture();
  }
  toggleSubtitles() {
    this.controller?.toggleSubtitles();
  }

  clearError() {
    this.controller?.clearError();
    this.update({ error: null, errorDetails: null, isPassiveError: false });
  }

  dismissToast() {
    this.update({ toast: null });
  }

  async retry() {
    await this.controller?.retry();
  }
  async reload() {
    await this.controller?.reload();
  }

  getQualities() {
    return this.controller?.getQualities() ?? [];
  }
  selectQuality(id: string) {
    this.controller?.selectQuality(id);
  }

  handleMouseEnter() {
    this.controller?.handleMouseEnter();
  }
  handleMouseLeave() {
    this.controller?.handleMouseLeave();
  }
  handleMouseMove() {
    this.controller?.handleMouseMove();
  }
  handleTouchStart() {
    this.controller?.handleTouchStart();
  }

  async setDevModeOptions(options: {
    forcePlayer?: string;
    forceType?: string;
    forceSource?: number;
    playbackMode?: "auto" | "low-latency" | "quality" | "vod";
  }) {
    await this.controller?.setDevModeOptions(options);
  }

  getController(): PlayerController | null {
    return this.controller;
  }
}
