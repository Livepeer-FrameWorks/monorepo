/**
 * <fw-player> — Main player web component.
 * Port of Player.tsx / PlayerInner from player-react.
 */
import { LitElement, html, css, nothing, type PropertyValues } from "lit";
import { customElement, property, state, query } from "lit/decorators.js";
import { classMap } from "lit/directives/class-map.js";
import { PlayerControllerHost } from "../controllers/player-controller-host.js";
import { sharedStyles } from "../styles/shared-styles.js";
import { utilityStyles } from "../styles/utility-styles.js";
import {
  closeIcon,
  statsIcon,
  settingsIcon,
  pictureInPictureIcon,
  loopIcon,
} from "../icons/index.js";
import type { ContentEndpoints, PlaybackMode } from "@livepeer-frameworks/player-core";

@customElement("fw-player")
export class FwPlayer extends LitElement {
  // ---- Public attributes (reflected) ----
  @property({ attribute: "content-id" }) contentId = "";
  @property({ attribute: "content-type" }) contentType?: "live" | "dvr" | "clip" | "vod";
  @property({ attribute: "gateway-url" }) gatewayUrl?: string;
  @property({ attribute: "mist-url" }) mistUrl?: string;
  @property({ attribute: "auth-token" }) authToken?: string;
  @property({ type: Boolean }) autoplay = true;
  @property({ type: Boolean }) muted = true;
  @property({ type: Boolean }) controls = false;
  @property({ type: Boolean }) debug = false;
  @property({ type: Boolean, attribute: "dev-mode" }) devMode = false;
  @property({ attribute: "thumbnail-url" }) thumbnailUrl?: string;
  @property({ attribute: "playback-mode" }) playbackMode: PlaybackMode = "auto";

  // ---- JS-only properties (not reflected) ----
  @property({ attribute: false }) endpoints?: ContentEndpoints;

  // ---- Internal state ----
  @state() private _isStatsOpen = false;
  @state() private _isDevPanelOpen = false;
  @state() private _skipDirection: "back" | "forward" | null = null;
  @state() private _contextMenuOpen = false;
  @state() private _contextMenuX = 0;
  @state() private _contextMenuY = 0;

  // ---- Refs ----
  @query("#container") private _containerEl!: HTMLDivElement;

  // ---- Controller ----
  pc = new PlayerControllerHost(this);

  static styles = [
    sharedStyles,
    utilityStyles,
    css`
      :host {
        display: block;
        position: relative;
        width: 100%;
        height: 100%;
        contain: layout style;
      }
      :host([hidden]) {
        display: none;
      }
      .player-area {
        position: relative;
        width: 100%;
        height: 100%;
      }
      .player-area--dev {
        flex: 1;
        min-width: 0;
      }
      .context-menu {
        position: fixed;
        z-index: 200;
        min-width: 180px;
        border-radius: 0.5rem;
        border: 1px solid rgb(255 255 255 / 0.1);
        background: rgb(0 0 0 / 0.9);
        backdrop-filter: blur(8px);
        padding: 0.25rem;
        box-shadow: 0 10px 15px -3px rgb(0 0 0 / 0.3);
      }
      .context-menu-item {
        display: flex;
        align-items: center;
        gap: 0.5rem;
        width: 100%;
        padding: 0.375rem 0.5rem;
        border: none;
        background: none;
        color: rgb(255 255 255 / 0.8);
        font-size: 0.8125rem;
        cursor: pointer;
        border-radius: 0.25rem;
        text-align: left;
      }
      .context-menu-item:hover {
        background: rgb(255 255 255 / 0.1);
        color: white;
      }
      .context-menu-sep {
        height: 1px;
        background: rgb(255 255 255 / 0.1);
        margin: 0.25rem 0;
      }
    `,
  ];

  // ---- Lifecycle ----

  protected willUpdate(changed: PropertyValues) {
    if (
      changed.has("contentId") ||
      changed.has("contentType") ||
      changed.has("gatewayUrl") ||
      changed.has("mistUrl") ||
      changed.has("authToken") ||
      changed.has("autoplay") ||
      changed.has("muted") ||
      changed.has("controls") ||
      changed.has("debug") ||
      changed.has("thumbnailUrl") ||
      changed.has("endpoints")
    ) {
      this.pc.configure({
        contentId: this.contentId,
        contentType: this.contentType,
        endpoints: this.endpoints,
        gatewayUrl: this.gatewayUrl,
        mistUrl: this.mistUrl,
        authToken: this.authToken,
        autoplay: this.autoplay,
        muted: this.muted,
        controls: this.controls,
        poster: this.thumbnailUrl,
        debug: this.debug,
      });
    }
  }

  protected firstUpdated() {
    this.pc.attach(this._containerEl);

    // Close context menu on outside click
    document.addEventListener("pointerdown", this._handleDocumentClick);
    document.addEventListener("contextmenu", this._handleDocumentContextMenu);
  }

  disconnectedCallback() {
    super.disconnectedCallback();
    document.removeEventListener("pointerdown", this._handleDocumentClick);
    document.removeEventListener("contextmenu", this._handleDocumentContextMenu);
  }

  // ---- Context Menu ----

  private _handleContextMenu = (e: MouseEvent) => {
    e.preventDefault();
    const rect = this.getBoundingClientRect();
    this._contextMenuX = e.clientX;
    this._contextMenuY = e.clientY;
    this._contextMenuOpen = true;
  };

  private _handleDocumentClick = () => {
    if (this._contextMenuOpen) this._contextMenuOpen = false;
  };

  private _handleDocumentContextMenu = (e: MouseEvent) => {
    if (!this.contains(e.target as Node)) {
      this._contextMenuOpen = false;
    }
  };

  // ---- Toast auto-dismiss ----

  private _toastTimer?: ReturnType<typeof setTimeout>;

  protected updated(changed: PropertyValues) {
    if (this.pc.s.toast) {
      clearTimeout(this._toastTimer);
      this._toastTimer = setTimeout(() => this.pc.dismissToast(), 3000);
    }
  }

  // ---- Derived state ----

  private get _showTitleOverlay() {
    const s = this.pc.s;
    return (s.isHovering || s.isPaused) && !s.shouldShowIdleScreen && !s.isBuffering && !s.error;
  }

  private get _showBufferingSpinner() {
    const s = this.pc.s;
    return !s.shouldShowIdleScreen && s.isBuffering && !s.error && s.hasPlaybackStarted;
  }

  private get _showWaitingForEndpoint() {
    const s = this.pc.s;
    return !s.endpoints && s.state !== "booting";
  }

  private get _useStockControls() {
    return this.controls || this.pc.s.currentPlayerInfo?.shortname === "mist-legacy";
  }

  // ---- Public API methods ----

  async play() {
    await this.pc.play();
  }
  pause() {
    this.pc.pause();
  }
  togglePlay() {
    this.pc.togglePlay();
  }
  seek(time: number) {
    this.pc.seek(time);
  }
  seekBy(delta: number) {
    this.pc.seekBy(delta);
  }
  jumpToLive() {
    this.pc.jumpToLive();
  }
  setVolume(volume: number) {
    this.pc.setVolume(volume);
  }
  toggleMute() {
    this.pc.toggleMute();
  }
  toggleLoop() {
    this.pc.toggleLoop();
  }
  async toggleFullscreen() {
    await this.pc.toggleFullscreen();
  }
  async togglePiP() {
    await this.pc.togglePiP();
  }
  toggleSubtitles() {
    this.pc.toggleSubtitles();
  }
  async retry() {
    await this.pc.retry();
  }
  async reload() {
    await this.pc.reload();
  }
  getQualities() {
    return this.pc.getQualities();
  }
  selectQuality(id: string) {
    this.pc.selectQuality(id);
  }
  destroy() {
    this.pc.hostDisconnected();
  }

  // ---- Render ----

  protected render() {
    const s = this.pc.s;

    return html`
      <div
        part="root"
        class=${classMap({
          "fw-player-surface": true,
          "fw-player-root": true,
          "w-full": true,
          "h-full": true,
          "overflow-hidden": true,
          flex: this.devMode,
        })}
        tabindex="0"
        @mouseenter=${() => this.pc.handleMouseEnter()}
        @mouseleave=${() => this.pc.handleMouseLeave()}
        @mousemove=${() => this.pc.handleMouseMove()}
        @touchstart=${() => this.pc.handleTouchStart()}
        @contextmenu=${this._handleContextMenu}
      >
        <!-- Player area -->
        <div
          class=${classMap({
            "player-area": true,
            "player-area--dev": this.devMode,
          })}
        >
          <!-- Video container -->
          <div id="container" part="video-container" class="fw-player-container"></div>

          <!-- Title overlay -->
          ${this._showTitleOverlay
            ? html`
                <fw-title-overlay
                  .title=${s.metadata?.title ?? null}
                  .description=${s.metadata?.description ?? null}
                ></fw-title-overlay>
              `
            : nothing}

          <!-- Stats panel -->
          ${this._isStatsOpen
            ? html`
                <fw-stats-panel
                  part="stats-panel"
                  .pc=${this.pc}
                  @fw-close=${() => {
                    this._isStatsOpen = false;
                  }}
                ></fw-stats-panel>
              `
            : nothing}

          <!-- Speed indicator -->
          ${s.isHoldingSpeed
            ? html` <fw-speed-indicator .speed=${s.holdSpeed}></fw-speed-indicator> `
            : nothing}

          <!-- Skip indicator -->
          <fw-skip-indicator
            .direction=${this._skipDirection}
            @fw-hide=${() => {
              this._skipDirection = null;
            }}
          ></fw-skip-indicator>

          <!-- Waiting for endpoint -->
          ${this._showWaitingForEndpoint
            ? html`
                <fw-idle-screen status="OFFLINE" message="Waiting for endpoint..."></fw-idle-screen>
              `
            : nothing}

          <!-- Idle screen -->
          ${!this._showWaitingForEndpoint && s.shouldShowIdleScreen
            ? html`
                <fw-idle-screen
                  .status=${s.isEffectivelyLive ? s.streamState?.status : undefined}
                  .message=${s.isEffectivelyLive ? s.streamState?.message : "Loading video..."}
                  .percentage=${s.isEffectivelyLive ? s.streamState?.percentage : undefined}
                ></fw-idle-screen>
              `
            : nothing}

          <!-- Buffering spinner -->
          ${this._showBufferingSpinner
            ? html`
                <div
                  role="status"
                  aria-live="polite"
                  class="fw-player-surface absolute inset-0 flex items-center justify-center bg-black/40 backdrop-blur-sm z-20"
                >
                  <div
                    class="flex items-center gap-3 rounded-lg border border-white/10 bg-black/70 px-4 py-3 text-sm text-white shadow-lg"
                  >
                    <div
                      class="w-4 h-4 border-2 border-white/10 rounded-full animate-spin"
                      style="border-top-color: white;"
                    ></div>
                    <span>Buffering...</span>
                  </div>
                </div>
              `
            : nothing}

          <!-- Error overlay -->
          ${!s.shouldShowIdleScreen && s.error
            ? html`
                <div
                  role="alert"
                  aria-live="assertive"
                  class=${classMap({
                    "fw-error-overlay": true,
                    "fw-error-overlay--passive": s.isPassiveError,
                    "fw-error-overlay--fullscreen": !s.isPassiveError,
                  })}
                >
                  <div
                    class=${classMap({
                      "fw-error-popup": true,
                      "fw-error-popup--passive": s.isPassiveError,
                      "fw-error-popup--fullscreen": !s.isPassiveError,
                    })}
                  >
                    <div
                      class=${classMap({
                        "fw-error-header": true,
                        "fw-error-header--warning": s.isPassiveError,
                        "fw-error-header--error": !s.isPassiveError,
                      })}
                    >
                      <span
                        class=${classMap({
                          "fw-error-title": true,
                          "fw-error-title--warning": s.isPassiveError,
                          "fw-error-title--error": !s.isPassiveError,
                        })}
                        >${s.isPassiveError ? "Warning" : "Error"}</span
                      >
                      <button
                        type="button"
                        class="fw-error-close"
                        @click=${() => this.pc.clearError()}
                        aria-label="Dismiss"
                      >
                        ${closeIcon()}
                      </button>
                    </div>
                    <div class="fw-error-body">
                      <p class="fw-error-message">Playback issue</p>
                    </div>
                    <div class="fw-error-actions">
                      <button
                        type="button"
                        class="fw-error-btn"
                        aria-label="Retry playback"
                        @click=${() => {
                          this.pc.clearError();
                          this.pc.retry();
                        }}
                      >
                        Retry
                      </button>
                    </div>
                  </div>
                </div>
              `
            : nothing}

          <!-- Toast notification -->
          ${s.toast
            ? html`
                <div
                  class="absolute bottom-20 left-1/2 -translate-x-1/2 z-30"
                  role="status"
                  aria-live="polite"
                >
                  <div
                    class="flex items-center gap-2 rounded-lg border border-white/10 bg-black/80 px-4 py-2 text-sm text-white shadow-lg backdrop-blur-sm"
                  >
                    <span>${s.toast.message}</span>
                    <button
                      type="button"
                      @click=${() => this.pc.dismissToast()}
                      class="ml-0.5 text-white/60 hover\\:text-white cursor-pointer"
                      aria-label="Dismiss"
                    >
                      ${closeIcon()}
                    </button>
                  </div>
                </div>
              `
            : nothing}

          <!-- Player controls -->
          ${!this._useStockControls
            ? html`
                <fw-player-controls
                  part="controls"
                  .pc=${this.pc}
                  .playbackMode=${this.playbackMode}
                  .isContentLive=${s.isEffectivelyLive}
                  .isStatsOpen=${this._isStatsOpen}
                  @fw-stats-toggle=${() => {
                    this._isStatsOpen = !this._isStatsOpen;
                  }}
                ></fw-player-controls>
              `
            : nothing}
        </div>

        <!-- Dev mode side panel -->
        ${this.devMode && this._isDevPanelOpen
          ? html`
              <fw-dev-mode-panel
                .pc=${this.pc}
                .playbackMode=${this.playbackMode}
                @fw-close=${() => {
                  this._isDevPanelOpen = false;
                }}
              ></fw-dev-mode-panel>
            `
          : nothing}
      </div>

      <!-- Context menu -->
      ${this._contextMenuOpen
        ? html`
            <div
              class="context-menu"
              style="left: ${this._contextMenuX}px; top: ${this._contextMenuY}px;"
            >
              <button
                class="context-menu-item"
                @click=${() => {
                  this._isStatsOpen = !this._isStatsOpen;
                  this._contextMenuOpen = false;
                }}
              >
                <span class="opacity-70 shrink-0">${statsIcon(14)}</span>
                <span>${this._isStatsOpen ? "Hide Stats" : "Stats"}</span>
              </button>
              ${this.devMode
                ? html`
                    <div class="context-menu-sep"></div>
                    <button
                      class="context-menu-item"
                      @click=${() => {
                        this._isDevPanelOpen = !this._isDevPanelOpen;
                        this._contextMenuOpen = false;
                      }}
                    >
                      <span class="opacity-70 shrink-0">${settingsIcon(14)}</span>
                      <span>${this._isDevPanelOpen ? "Hide Settings" : "Settings"}</span>
                    </button>
                  `
                : nothing}
              <div class="context-menu-sep"></div>
              <button
                class="context-menu-item"
                @click=${() => {
                  this.pc.togglePiP();
                  this._contextMenuOpen = false;
                }}
              >
                <span class="opacity-70 shrink-0">${pictureInPictureIcon(14)}</span>
                <span>Picture-in-Picture</span>
              </button>
              <button
                class="context-menu-item"
                @click=${() => {
                  this.pc.toggleLoop();
                  this._contextMenuOpen = false;
                }}
              >
                <span class="opacity-70 shrink-0">${loopIcon(14)}</span>
                <span>${s.isLoopEnabled ? "Disable Loop" : "Enable Loop"}</span>
              </button>
            </div>
          `
        : nothing}
    `;
  }
}

declare global {
  interface HTMLElementTagNameMap {
    "fw-player": FwPlayer;
  }
}
