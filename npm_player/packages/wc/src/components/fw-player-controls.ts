/**
 * <fw-player-controls> — Player controls with seek, volume, live state, and settings.
 * Parity port of React/Svelte control behavior.
 */
import { LitElement, html, css, nothing, type PropertyValues } from "lit";
import { customElement, property, query, state } from "lit/decorators.js";
import { classMap } from "lit/directives/class-map.js";
import { sharedStyles } from "../styles/shared-styles.js";
import { utilityStyles } from "../styles/utility-styles.js";
import {
  playIcon,
  pauseIcon,
  fullscreenIcon,
  fullscreenExitIcon,
  settingsIcon,
  seekToLiveIcon,
  skipBackIcon,
  skipForwardIcon,
  statsIcon,
} from "../icons/index.js";
import {
  calculateIsNearLive,
  calculateLiveThresholds,
  calculateSeekableRange,
  canSeekStream,
  formatTimeDisplay,
  isLiveContent,
  isMediaStreamSource,
  type MistStreamInfo,
  type PlaybackMode,
  type FwLocale,
} from "@livepeer-frameworks/player-core";
import type { PlayerControllerHost } from "../controllers/player-controller-host.js";

interface SeekingContext {
  mistStreamInfo?: MistStreamInfo;
  isLive: boolean;
  sourceType?: string;
  seekableStart: number;
  liveEdge: number;
  hasDvrWindow: boolean;
  canSeek: boolean;
  commitOnRelease: boolean;
  liveThresholds: ReturnType<typeof calculateLiveThresholds>;
}

@customElement("fw-player-controls")
export class FwPlayerControls extends LitElement {
  @property({ attribute: false }) pc!: PlayerControllerHost;
  @property({ type: String }) playbackMode: PlaybackMode = "auto";
  @property({ type: Boolean, attribute: "is-content-live" }) isContentLive = false;
  @property({ type: Boolean, attribute: "dev-mode" }) devMode = false;
  @property({ type: Boolean, attribute: "show-stats-button" }) showStatsButton = false;
  @property({ type: Boolean, attribute: "is-stats-open" }) isStatsOpen = false;
  @property({ attribute: "active-locale" }) activeLocale?: FwLocale;

  @state() private _settingsOpen = false;
  @state() private _isNearLiveState = true;
  @state() private _buffered: TimeRanges | null = null;
  @query(".fw-settings-anchor") private _settingsAnchorEl!: HTMLElement | null;

  private _boundVideo: HTMLVideoElement | null = null;
  private _onBufferedUpdate: (() => void) | null = null;

  static styles = [
    sharedStyles,
    utilityStyles,
    css`
      :host {
        display: contents;
      }

      .fw-settings-anchor {
        position: relative;
      }
    `,
  ];

  connectedCallback(): void {
    super.connectedCallback();
  }

  disconnectedCallback(): void {
    super.disconnectedCallback();
    this._unbindVideoEvents();
    this._detachWindowClickListener();
  }

  protected updated(changed: PropertyValues<this>): void {
    this._bindVideoEvents();
    this._reconcileNearLiveState();

    if (changed.has("_settingsOpen" as keyof FwPlayerControls)) {
      if (this._settingsOpen) {
        this._attachWindowClickListener();
      } else {
        this._detachWindowClickListener();
      }
    }
  }

  private _bindVideoEvents(): void {
    const video = this.pc?.s.videoElement ?? null;
    if (video === this._boundVideo) {
      return;
    }

    this._unbindVideoEvents();
    this._boundVideo = video;

    if (!video) {
      this._buffered = null;
      return;
    }

    const updateBuffered = () => {
      this._buffered = this.pc.getBufferedRanges() ?? video.buffered;
    };

    updateBuffered();
    video.addEventListener("progress", updateBuffered);
    video.addEventListener("loadeddata", updateBuffered);
    this._onBufferedUpdate = updateBuffered;
  }

  private _unbindVideoEvents(): void {
    if (!this._boundVideo) {
      return;
    }

    const updateBuffered = this._onBufferedUpdate;
    if (updateBuffered) {
      this._boundVideo.removeEventListener("progress", updateBuffered);
      this._boundVideo.removeEventListener("loadeddata", updateBuffered);
    }

    this._boundVideo = null;
    this._onBufferedUpdate = null;
  }

  private _attachWindowClickListener(): void {
    window.setTimeout(() => {
      if (!this._settingsOpen) {
        return;
      }
      window.addEventListener("click", this._onWindowClick);
    }, 0);
  }

  private _detachWindowClickListener(): void {
    window.removeEventListener("click", this._onWindowClick);
  }

  private _onWindowClick = (event: MouseEvent): void => {
    const path = event.composedPath();
    const anchor = this._settingsAnchorEl;
    const insideControls =
      anchor !== null &&
      path.some((entry) => {
        if (!(entry instanceof Node)) {
          return false;
        }
        return anchor.contains(entry);
      });

    if (!insideControls) {
      this._settingsOpen = false;
    }
  };

  private _deriveBufferWindowMs(
    tracks?: Record<string, { type?: string; firstms?: number; lastms?: number }>
  ): number | undefined {
    if (!tracks) {
      return undefined;
    }

    // Filter out meta tracks and tracks with lastms <= 0 (same as PlayerController)
    const trackValues = Object.values(tracks).filter(
      (t) => t.type !== "meta" && (t.lastms === undefined || t.lastms > 0)
    );
    if (trackValues.length === 0) {
      return undefined;
    }

    const firstmsValues = trackValues
      .map((track) => track.firstms)
      .filter((value): value is number => typeof value === "number");
    const lastmsValues = trackValues
      .map((track) => track.lastms)
      .filter((value): value is number => typeof value === "number");

    if (firstmsValues.length === 0 || lastmsValues.length === 0) {
      return undefined;
    }

    const firstms = Math.min(...firstmsValues);
    const lastms = Math.max(...lastmsValues);
    const window = lastms - firstms;

    if (!Number.isFinite(window) || window <= 0) {
      return undefined;
    }

    return window;
  }

  private _getSeekingContext(): SeekingContext {
    const state = this.pc.s;
    const controller = this.pc.getController();
    const sourceType = state.currentSourceInfo?.type;
    const mistStreamInfo = state.streamState?.streamInfo as MistStreamInfo | undefined;

    const isLive = isLiveContent(this.isContentLive, mistStreamInfo, state.duration);
    const bufferWindowMs =
      mistStreamInfo?.meta?.buffer_window ??
      this._deriveBufferWindowMs(
        mistStreamInfo?.meta?.tracks as
          | Record<string, { type?: string; firstms?: number; lastms?: number }>
          | undefined
      );

    const isWebRTC = isMediaStreamSource(state.videoElement);

    const allowMediaStreamDvr =
      isMediaStreamSource(state.videoElement) && bufferWindowMs !== undefined && bufferWindowMs > 0;

    const calculatedRange = calculateSeekableRange({
      isLive,
      video: state.videoElement,
      mistStreamInfo,
      currentTime: state.currentTime,
      duration: state.duration,
      allowMediaStreamDvr,
    });

    const controllerSeekableStart = this.pc.getSeekableStart();
    const controllerLiveEdge = this.pc.getLiveEdge();

    const useControllerRange =
      Number.isFinite(controllerSeekableStart) &&
      Number.isFinite(controllerLiveEdge) &&
      controllerLiveEdge >= controllerSeekableStart &&
      (controllerLiveEdge > 0 || controllerSeekableStart > 0);

    const seekableStart = useControllerRange
      ? controllerSeekableStart
      : calculatedRange.seekableStart;
    const liveEdge = useControllerRange ? controllerLiveEdge : calculatedRange.liveEdge;

    const hasDvrWindow =
      isLive &&
      Number.isFinite(liveEdge) &&
      Number.isFinite(seekableStart) &&
      liveEdge > seekableStart;

    const baseCanSeek =
      controller?.canSeekStream?.() ??
      canSeekStream({
        video: state.videoElement,
        isLive,
        duration: state.duration,
        bufferWindowMs,
      });

    const liveThresholds = calculateLiveThresholds(sourceType, isWebRTC, bufferWindowMs);

    return {
      mistStreamInfo,
      isLive,
      sourceType,
      seekableStart,
      liveEdge,
      hasDvrWindow,
      canSeek: baseCanSeek && (!isLive || hasDvrWindow),
      commitOnRelease: isLive,
      liveThresholds,
    };
  }

  private _reconcileNearLiveState(): void {
    const context = this._getSeekingContext();

    if (!context.isLive) {
      if (!this._isNearLiveState) {
        this._isNearLiveState = true;
      }
      return;
    }

    const next = calculateIsNearLive(
      this.pc.s.currentTime,
      context.liveEdge,
      context.liveThresholds,
      this._isNearLiveState
    );

    if (next !== this._isNearLiveState) {
      this._isNearLiveState = next;
    }
  }

  private _handleModeChange(
    event: CustomEvent<{ mode: "auto" | "low-latency" | "quality" }>
  ): void {
    const { mode } = event.detail;
    this.dispatchEvent(
      new CustomEvent("fw-mode-change", {
        detail: { mode },
        bubbles: true,
        composed: true,
      })
    );
  }

  protected render() {
    const state = this.pc.s;
    const disabled = !state.videoElement;
    const context = this._getSeekingContext();
    const shouldShowControls =
      state.shouldShowControls ||
      state.isPaused ||
      !state.hasPlaybackStarted ||
      state.shouldShowIdleScreen ||
      !!state.error ||
      this._settingsOpen;

    const timeDisplay = formatTimeDisplay({
      isLive: context.isLive,
      currentTime: state.currentTime,
      duration: state.duration,
      liveEdge: context.liveEdge,
      seekableStart: context.seekableStart,
      unixoffset: context.mistStreamInfo?.unixoffset,
    });
    const showTimeDisplay = !(context.isLive && timeDisplay === "LIVE");

    const liveButtonDisabled = !context.hasDvrWindow || this._isNearLiveState;

    return html`
      <div
        class=${classMap({
          "fw-controls-wrapper": true,
          "fw-controls-wrapper--visible": shouldShowControls,
          "fw-controls-wrapper--hidden": !shouldShowControls,
        })}
      >
        <div class="fw-control-bar" @click=${(event: Event) => event.stopPropagation()}>
          ${context.canSeek
            ? html`
                <div class="fw-seek-wrapper">
                  <fw-seek-bar
                    .currentTime=${state.currentTime}
                    .duration=${state.duration}
                    .buffered=${this._buffered}
                    .disabled=${disabled}
                    .isLive=${context.isLive}
                    .seekableStart=${context.seekableStart}
                    .liveEdge=${context.liveEdge}
                    .commitOnRelease=${context.commitOnRelease}
                    @fw-seek=${(event: CustomEvent<{ time: number }>) =>
                      this.pc.seek(event.detail.time)}
                  ></fw-seek-bar>
                </div>
              `
            : nothing}

          <div class="fw-controls-row">
            <div class="fw-controls-left">
              <div class="fw-control-group">
                <button
                  type="button"
                  class="fw-btn-flush"
                  ?disabled=${disabled}
                  aria-label=${state.isPlaying ? this.pc.t("pause") : this.pc.t("play")}
                  @click=${() => this.pc.togglePlay()}
                >
                  ${state.isPlaying ? pauseIcon(18) : playIcon(18)}
                </button>

                ${context.canSeek
                  ? html`
                      <button
                        type="button"
                        class="fw-btn-flush hidden sm:flex"
                        ?disabled=${disabled}
                        aria-label=${this.pc.t("skipBackward")}
                        @click=${() => this.pc.seekBy(-10000)}
                      >
                        ${skipBackIcon(16)}
                      </button>
                      <button
                        type="button"
                        class="fw-btn-flush hidden sm:flex"
                        ?disabled=${disabled}
                        aria-label=${this.pc.t("skipForward")}
                        @click=${() => this.pc.seekBy(10000)}
                      >
                        ${skipForwardIcon(16)}
                      </button>
                    `
                  : nothing}
              </div>

              <div class="fw-control-group">
                <fw-volume-control .pc=${this.pc}></fw-volume-control>
              </div>

              ${showTimeDisplay
                ? html`
                    <div class="fw-control-group">
                      <span class="fw-time-display">${timeDisplay}</span>
                    </div>
                  `
                : nothing}
              ${context.isLive
                ? html`
                    <div class="fw-control-group">
                      <button
                        type="button"
                        @click=${() => this.pc.jumpToLive()}
                        ?disabled=${liveButtonDisabled}
                        class=${classMap({
                          "fw-live-badge": true,
                          "fw-live-badge--active": liveButtonDisabled,
                          "fw-live-badge--behind": !liveButtonDisabled,
                        })}
                        title=${!context.hasDvrWindow
                          ? this.pc.t("live")
                          : this._isNearLiveState
                            ? this.pc.t("live")
                            : this.pc.t("live")}
                      >
                        ${this.pc.t("live").toUpperCase()}
                        ${!this._isNearLiveState && context.hasDvrWindow
                          ? seekToLiveIcon(10)
                          : nothing}
                      </button>
                    </div>
                  `
                : nothing}
            </div>

            <div class="fw-controls-right">
              ${this.showStatsButton
                ? html`
                    <div class="fw-control-group">
                      <button
                        type="button"
                        class=${classMap({
                          "fw-btn-flush": true,
                          "fw-btn-flush--active": this.isStatsOpen,
                        })}
                        aria-label=${this.pc.t("showStats")}
                        title=${this.pc.t("showStats")}
                        @click=${() =>
                          this.dispatchEvent(
                            new CustomEvent("fw-stats-toggle", {
                              bubbles: true,
                              composed: true,
                            })
                          )}
                      >
                        ${statsIcon(16)}
                      </button>
                    </div>
                  `
                : nothing}
              <div class="fw-control-group fw-settings-anchor">
                <button
                  type="button"
                  class=${classMap({
                    "fw-btn-flush": true,
                    group: true,
                    "fw-btn-flush--active": this._settingsOpen,
                  })}
                  aria-label=${this.pc.t("settings")}
                  title=${this.pc.t("settings")}
                  ?disabled=${disabled}
                  @click=${(event: MouseEvent) => {
                    event.stopPropagation();
                    if (disabled) {
                      return;
                    }
                    this._settingsOpen = !this._settingsOpen;
                  }}
                >
                  <span class="transition-transform group-hover:rotate-90"
                    >${settingsIcon(16)}</span
                  >
                </button>

                <fw-settings-menu
                  .pc=${this.pc}
                  .open=${this._settingsOpen}
                  .playbackMode=${this.playbackMode}
                  .isContentLive=${this.isContentLive}
                  .activeLocale=${this.activeLocale}
                  @click=${(event: MouseEvent) => event.stopPropagation()}
                  @fw-close=${() => {
                    this._settingsOpen = false;
                  }}
                  @fw-mode-change=${this._handleModeChange}
                ></fw-settings-menu>
              </div>

              <div class="fw-control-group">
                <button
                  type="button"
                  class="fw-btn-flush"
                  ?disabled=${disabled}
                  aria-label=${state.isFullscreen
                    ? this.pc.t("exitFullscreen")
                    : this.pc.t("fullscreen")}
                  @click=${() => this.pc.toggleFullscreen()}
                >
                  ${state.isFullscreen ? fullscreenExitIcon(16) : fullscreenIcon(16)}
                </button>
              </div>
            </div>
          </div>
        </div>
      </div>
    `;
  }
}

declare global {
  interface HTMLElementTagNameMap {
    "fw-player-controls": FwPlayerControls;
  }
}
