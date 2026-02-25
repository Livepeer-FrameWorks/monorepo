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
import type {
  ContentEndpoints,
  PlaybackMode,
  FwThemePreset,
  FwThemeOverrides,
  FwLocale,
} from "@livepeer-frameworks/player-core";
import { applyTheme, applyThemeOverrides, clearTheme } from "@livepeer-frameworks/player-core";

@customElement("fw-player")
export class FwPlayer extends LitElement {
  // ---- Public attributes (reflected) ----
  @property({ attribute: "content-id" }) contentId = "";
  @property({ attribute: "content-type" }) contentType?: "live" | "dvr" | "clip" | "vod";
  @property({ attribute: "gateway-url" }) gatewayUrl?: string;
  @property({ attribute: "mist-url" }) mistUrl?: string;
  @property({ attribute: "auth-token" }) authToken?: string;
  @property({ type: Boolean }) autoplay = true;
  @property({ type: Boolean }) muted = false;
  // React/Svelte use `stockControls` for native controls. Keep `controls` as a
  // compatibility no-op so WC parity does not hide custom controls/seekbar.
  @property({ type: Boolean }) controls = false;
  @property({ type: Boolean, attribute: "stock-controls" }) stockControls = false;
  @property({ type: Boolean, attribute: "native-controls" }) nativeControls = false;
  @property({ type: Boolean }) debug = false;
  @property({ type: Boolean, attribute: "dev-mode" }) devMode = false;
  @property({ attribute: "thumbnail-url" }) thumbnailUrl?: string;
  @property({ attribute: "playback-mode" }) playbackMode: PlaybackMode = "auto";

  // ---- Theme ----
  @property({ attribute: "theme" }) theme?: FwThemePreset;
  @property({ attribute: false }) themeOverrides?: FwThemeOverrides;
  @property({ attribute: "locale" }) locale?: FwLocale;

  // ---- JS-only properties (not reflected) ----
  @property({ attribute: false }) endpoints?: ContentEndpoints;

  // ---- Internal state ----
  @state() private _isStatsOpen = false;
  @state() private _isDevPanelOpen = false;
  @state() private _skipDirection: "back" | "forward" | null = null;
  @state() private _contextMenuOpen = false;
  @state() private _contextMenuMounted = false;

  // Error fade-out
  @state() private _displayedError: string | null = null;
  @state() private _displayedIsPassive = false;
  @state() private _isErrorDismissing = false;
  private _errorDismissTimer: ReturnType<typeof setTimeout> | null = null;
  @state() private _contextMenuState: "open" | "closed" = "closed";
  @state() private _contextMenuSide: "top" | "bottom" | "left" | "right" = "bottom";
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
        min-height: 0;
        overflow: hidden;
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
        min-height: 0;
      }
    `,
  ];

  // ---- Lifecycle ----

  protected willUpdate(changed: PropertyValues) {
    if (changed.has("locale")) {
      this.pc.updateTranslator({ locale: this.locale ?? "en" });
    }

    // Error fade-out: sync displayed error from controller state
    const es = this.pc.s;
    if (es.error) {
      if (this._errorDismissTimer) {
        clearTimeout(this._errorDismissTimer);
        this._errorDismissTimer = null;
      }
      this._displayedError = es.error;
      this._displayedIsPassive = es.isPassiveError;
      this._isErrorDismissing = false;
    } else if (this._displayedError && !this._isErrorDismissing) {
      this._isErrorDismissing = true;
      this._errorDismissTimer = setTimeout(() => {
        this._displayedError = null;
        this._displayedIsPassive = false;
        this._isErrorDismissing = false;
        this._errorDismissTimer = null;
      }, 300);
    }

    if (
      changed.has("contentId") ||
      changed.has("contentType") ||
      changed.has("gatewayUrl") ||
      changed.has("mistUrl") ||
      changed.has("authToken") ||
      changed.has("autoplay") ||
      changed.has("muted") ||
      changed.has("stockControls") ||
      changed.has("nativeControls") ||
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
        controls: this.stockControls || this.nativeControls,
        poster: this.thumbnailUrl,
        debug: this.debug,
      });
    }
  }

  protected firstUpdated() {
    this.pc.attach(this._containerEl);

    // Close context menu on outside click
    document.addEventListener("pointerdown", this._handleDocumentPointerDown);
    document.addEventListener("contextmenu", this._handleDocumentContextMenu);
    document.addEventListener("keydown", this._handleDocumentKeyDown);
  }

  disconnectedCallback() {
    super.disconnectedCallback();
    document.removeEventListener("pointerdown", this._handleDocumentPointerDown);
    document.removeEventListener("contextmenu", this._handleDocumentContextMenu);
    document.removeEventListener("keydown", this._handleDocumentKeyDown);
    if (this._contextMenuCloseTimer) {
      clearTimeout(this._contextMenuCloseTimer);
      this._contextMenuCloseTimer = undefined;
    }
    if (this._errorDismissTimer) {
      clearTimeout(this._errorDismissTimer);
      this._errorDismissTimer = null;
    }
    this._resetContextMenuTypeahead();
  }

  // ---- Context Menu ----

  private _contextMenuCloseTimer?: ReturnType<typeof setTimeout>;
  private _contextMenuTypeahead = "";
  private _contextMenuTypeaheadTimer?: ReturnType<typeof setTimeout>;

  private _resetContextMenuTypeahead = () => {
    this._contextMenuTypeahead = "";
    if (this._contextMenuTypeaheadTimer) {
      clearTimeout(this._contextMenuTypeaheadTimer);
      this._contextMenuTypeaheadTimer = undefined;
    }
  };

  private _resolveContextMenuSide = (
    rawX: number,
    rawY: number,
    clampedX: number,
    clampedY: number
  ) => {
    const deltaX = Math.abs(rawX - clampedX);
    const deltaY = Math.abs(rawY - clampedY);

    if (deltaX === 0 && deltaY === 0) {
      return "bottom";
    }

    if (deltaY >= deltaX) {
      return rawY > clampedY ? "top" : "bottom";
    }

    return rawX > clampedX ? "left" : "right";
  };

  private _closeContextMenu = (restoreFocus = false) => {
    this._contextMenuOpen = false;
    this._contextMenuState = "closed";
    this._resetContextMenuTypeahead();
    if (this._contextMenuCloseTimer) {
      clearTimeout(this._contextMenuCloseTimer);
    }
    this._contextMenuCloseTimer = setTimeout(() => {
      if (!this._contextMenuOpen) {
        this._contextMenuMounted = false;
      }
    }, 170);

    if (restoreFocus) {
      const root = this.shadowRoot?.querySelector<HTMLElement>('[part="root"]');
      root?.focus();
    }
  };

  private _getQueryRoot = (): ShadowRoot | null => {
    return (
      this.shadowRoot ?? (this as unknown as { renderRoot?: ShadowRoot | null }).renderRoot ?? null
    );
  };

  private _getContextMenuElement = () =>
    this._getQueryRoot()?.querySelector<HTMLElement>('[data-context-menu="true"]') ?? null;

  private _getContextMenuBounds = () => {
    const root = this._getQueryRoot()?.querySelector<HTMLElement>('[part="root"]');
    const rect = root?.getBoundingClientRect() ?? this.getBoundingClientRect();

    const width = rect.width > 0 ? rect.width : window.innerWidth;
    const height = rect.height > 0 ? rect.height : window.innerHeight;

    return {
      left: rect.left,
      top: rect.top,
      right: rect.left + width,
      bottom: rect.top + height,
      width,
      height,
    };
  };

  private _toLocalContextMenuPoint = (clientX: number, clientY: number) => {
    const bounds = this._getContextMenuBounds();
    return {
      x: clientX - bounds.left,
      y: clientY - bounds.top,
    };
  };

  private _getContextMenuItems = () =>
    Array.from(
      this._getQueryRoot()?.querySelectorAll<HTMLButtonElement>(
        '[data-context-menu-item="true"][data-context-menu-level="root"]:not([data-disabled="true"])'
      ) ?? []
    );

  private _focusFirstContextMenuItem = () => {
    const [firstItem] = this._getContextMenuItems();
    firstItem?.focus();
  };

  private _clampContextMenuPosition = (x: number, y: number, width: number, height: number) => {
    const viewportPadding = 8;
    const bounds = this._getContextMenuBounds();
    const maxX = Math.max(viewportPadding, bounds.width - width - viewportPadding);
    const maxY = Math.max(viewportPadding, bounds.height - height - viewportPadding);

    return {
      x: Math.max(viewportPadding, Math.min(x, maxX)),
      y: Math.max(viewportPadding, Math.min(y, maxY)),
    };
  };

  private _syncContextMenuPosition = () => {
    if (!this._contextMenuMounted) return;
    const menu = this._getContextMenuElement();
    if (!menu) return;

    const rect = menu.getBoundingClientRect();
    const next = this._clampContextMenuPosition(
      this._contextMenuX,
      this._contextMenuY,
      rect.width,
      rect.height
    );
    this._contextMenuSide = this._resolveContextMenuSide(
      this._contextMenuX,
      this._contextMenuY,
      next.x,
      next.y
    );
    if (next.x !== this._contextMenuX || next.y !== this._contextMenuY) {
      this._contextMenuX = next.x;
      this._contextMenuY = next.y;
    }
  };

  private _openContextMenu = (clientX: number, clientY: number) => {
    const local = this._toLocalContextMenuPoint(clientX, clientY);
    const next = this._clampContextMenuPosition(local.x, local.y, 220, 200);
    this._contextMenuSide = this._resolveContextMenuSide(local.x, local.y, next.x, next.y);
    this._contextMenuX = next.x;
    this._contextMenuY = next.y;
    this._contextMenuMounted = true;
    this._contextMenuState = "open";
    if (this._contextMenuCloseTimer) {
      clearTimeout(this._contextMenuCloseTimer);
      this._contextMenuCloseTimer = undefined;
    }
    this._resetContextMenuTypeahead();
    this._contextMenuOpen = true;
  };

  private _handleContextMenu = (e: MouseEvent) => {
    const target = e.target as HTMLElement | null;
    if (target?.closest('[data-context-menu="true"]')) {
      e.preventDefault();
      return;
    }

    e.preventDefault();
    this._openContextMenu(e.clientX, e.clientY);
  };

  private _handleContextMenuShortcut = (e: KeyboardEvent) => {
    const isContextMenuKey = e.key === "ContextMenu";
    const isShiftF10 = e.key === "F10" && e.shiftKey;
    if (!isContextMenuKey && !isShiftF10) return;

    e.preventDefault();
    const rect = this.getBoundingClientRect();
    const x = rect.left + rect.width / 2;
    const y = rect.top + rect.height / 2;
    this._openContextMenu(x, y);
  };

  private _handleDocumentPointerDown = (e: PointerEvent) => {
    if (!this._contextMenuOpen) return;
    const menu = this._getContextMenuElement();
    const composedPath = e.composedPath();
    if (menu && composedPath.includes(menu)) return;
    this._closeContextMenu();
  };

  private _handleDocumentContextMenu = (e: MouseEvent) => {
    if (!this._contextMenuOpen) return;
    if (!this.contains(e.target as Node)) {
      this._closeContextMenu();
    }
  };

  private _handleDocumentKeyDown = (e: KeyboardEvent) => {
    if (e.key === "Escape" && this._contextMenuOpen) {
      e.preventDefault();
      this._closeContextMenu(true);
    }
  };

  private _handleContextMenuKeyDown = (e: KeyboardEvent) => {
    if (!this._contextMenuOpen) return;
    const activeElement = this.shadowRoot?.activeElement as HTMLButtonElement | null;

    if (e.key === "Escape") {
      e.preventDefault();
      this._closeContextMenu(true);
      return;
    }

    if (e.key === "Tab") {
      this._closeContextMenu();
      return;
    }

    const items = this._getContextMenuItems();
    if (items.length === 0) return;
    const activeIndex = items.findIndex((item) => item === activeElement);

    if (e.key === "Home") {
      e.preventDefault();
      this._focusFirstContextMenuItem();
      return;
    }

    if (e.key === "End") {
      e.preventDefault();
      items[items.length - 1]?.focus();
      return;
    }

    if (e.key === "ArrowDown" || e.key === "ArrowUp") {
      e.preventDefault();
      const direction = e.key === "ArrowDown" ? 1 : -1;
      const startIndex =
        activeIndex === -1 ? (direction === 1 ? 0 : items.length - 1) : activeIndex;
      const nextIndex = (startIndex + direction + items.length) % items.length;
      items[nextIndex]?.focus();
      return;
    }

    if (e.key === "Enter" || e.key === " ") {
      if (activeElement) {
        e.preventDefault();
        activeElement.click();
      }
      return;
    }

    if (e.key.length === 1 && !e.metaKey && !e.ctrlKey && !e.altKey) {
      e.preventDefault();
      this._contextMenuTypeahead += e.key.toLowerCase();
      if (this._contextMenuTypeaheadTimer) {
        clearTimeout(this._contextMenuTypeaheadTimer);
      }
      this._contextMenuTypeaheadTimer = setTimeout(() => {
        this._resetContextMenuTypeahead();
      }, 700);

      const startIndex = activeIndex === -1 ? 0 : activeIndex + 1;
      const orderedItems = [...items.slice(startIndex), ...items.slice(0, startIndex)];
      const match = orderedItems.find((item) =>
        item.textContent?.trim().toLowerCase().startsWith(this._contextMenuTypeahead)
      );
      match?.focus();
    }
  };

  // ---- Toast auto-dismiss ----

  private _toastTimer?: ReturnType<typeof setTimeout>;

  protected updated(changed: PropertyValues) {
    // Apply theme changes (preset or overrides) via JS custom properties
    if (changed.has("theme") || changed.has("themeOverrides")) {
      const root = this.shadowRoot?.querySelector<HTMLElement>('[part="root"]');
      if (root) {
        clearTheme(root);
        if (this.theme && this.theme !== "default") {
          applyTheme(root, this.theme);
        }
        if (this.themeOverrides) applyThemeOverrides(root, this.themeOverrides);
      }
    }

    if (this.pc.s.toast) {
      clearTimeout(this._toastTimer);
      this._toastTimer = setTimeout(() => this.pc.dismissToast(), 3000);
    }

    if (
      (changed.has("_contextMenuOpen") || changed.has("_contextMenuMounted")) &&
      this._contextMenuOpen
    ) {
      queueMicrotask(() => {
        this._syncContextMenuPosition();
        this._focusFirstContextMenuItem();
      });
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
    return !s.endpoints?.primary && s.state !== "booting";
  }

  private get _waitingMessage() {
    const s = this.pc.s;
    if (this.gatewayUrl && s.state === "gateway_loading") {
      return this.pc.t("resolvingEndpoint");
    }
    return this.pc.t("waitingForStream");
  }

  private get _useStockControls() {
    return (
      this.stockControls ||
      this.nativeControls ||
      this.pc.s.currentPlayerInfo?.shortname === "mist-legacy"
    );
  }

  /** Expose the PlayerControllerHost for composable controls */
  get controller(): PlayerControllerHost {
    return this.pc;
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
        data-player-container="true"
        data-theme=${this.theme && this.theme !== "default" ? this.theme : nothing}
        tabindex="0"
        @mouseenter=${() => this.pc.handleMouseEnter()}
        @mouseleave=${() => this.pc.handleMouseLeave()}
        @mousemove=${() => this.pc.handleMouseMove()}
        @touchstart=${() => this.pc.handleTouchStart()}
        @keydown=${this._handleContextMenuShortcut}
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

          <!-- Subtitle renderer -->
          ${s.subtitlesEnabled
            ? html`
                <fw-subtitle-renderer
                  .currentTime=${s.currentTime}
                  .enabled=${s.subtitlesEnabled}
                ></fw-subtitle-renderer>
              `
            : nothing}

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
                <fw-idle-screen
                  status="OFFLINE"
                  .message=${this._waitingMessage}
                  @fw-retry=${() => {
                    this.pc.clearError();
                    this.pc.retry();
                  }}
                ></fw-idle-screen>
              `
            : nothing}

          <!-- Idle screen -->
          ${!this._showWaitingForEndpoint && s.shouldShowIdleScreen
            ? html`
                <fw-idle-screen
                  .status=${s.isEffectivelyLive ? s.streamState?.status : undefined}
                  .message=${s.isEffectivelyLive ? s.streamState?.message : this.pc.t("loading")}
                  .percentage=${s.isEffectivelyLive ? s.streamState?.percentage : undefined}
                  @fw-retry=${() => {
                    this.pc.clearError();
                    this.pc.retry();
                  }}
                ></fw-idle-screen>
              `
            : nothing}

          <!-- Buffering spinner -->
          ${this._showBufferingSpinner
            ? html`
                <div role="status" aria-live="polite" class="fw-buffering-overlay">
                  <div class="fw-buffering-pill">
                    <div class="fw-buffering-spinner"></div>
                    <span>${this.pc.t("buffering")}</span>
                  </div>
                </div>
              `
            : nothing}

          <!-- Passive error toast (non-blocking) -->
          ${!s.shouldShowIdleScreen && this._displayedError && this._displayedIsPassive
            ? html`
                <div
                  class="absolute bottom-20 left-1/2 -translate-x-1/2 z-30"
                  style="transition:opacity 300ms;opacity:${this._isErrorDismissing ? "0" : "1"}"
                  role="status"
                  aria-live="polite"
                >
                  <div
                    class="flex items-center gap-2 rounded-lg border border-yellow-500/30 bg-black/80 px-4 py-2 text-sm text-white shadow-lg backdrop-blur-sm"
                  >
                    <span class="text-yellow-400 text-xs font-semibold uppercase"
                      >${this.pc.t("warning")}</span
                    >
                    <span>${this._displayedError}</span>
                    <button
                      type="button"
                      @click=${() => this.pc.clearError()}
                      class="ml-0.5 text-white/60 hover\\:text-white cursor-pointer"
                      aria-label=${this.pc.t("dismiss")}
                    >
                      ${closeIcon()}
                    </button>
                  </div>
                </div>
              `
            : nothing}

          <!-- Fatal error overlay (blocking) — auto-dismisses on playback resume -->
          ${!s.shouldShowIdleScreen && this._displayedError && !this._displayedIsPassive
            ? html`
                <div
                  role="alert"
                  aria-live="assertive"
                  class="fw-error-overlay fw-error-overlay--fullscreen"
                  style="transition:opacity 300ms;opacity:${this._isErrorDismissing ? "0" : "1"}"
                >
                  <div class="fw-error-popup fw-error-popup--fullscreen">
                    <div class="fw-error-header fw-error-header--error">
                      <span class="fw-error-title fw-error-title--error"
                        >${this.pc.t("error")}</span
                      >
                      <button
                        type="button"
                        class="fw-error-close"
                        @click=${() => this.pc.clearError()}
                        aria-label=${this.pc.t("dismiss")}
                      >
                        ${closeIcon()}
                      </button>
                    </div>
                    <div class="fw-error-body">
                      <p class="fw-error-message">${this._displayedError}</p>
                    </div>
                    <div class="fw-error-actions">
                      <button
                        type="button"
                        class="fw-error-btn"
                        aria-label=${this.pc.t("retry")}
                        @click=${() => {
                          this.pc.clearError();
                          this.pc.retry();
                        }}
                      >
                        ${this.pc.t("retry")}
                      </button>
                      ${this.devMode && this.pc.canAttemptFallback()
                        ? html`
                            <button
                              type="button"
                              class="fw-error-btn fw-error-btn--secondary"
                              aria-label=${this.pc.t("tryNext")}
                              @click=${() => {
                                this.pc.clearError();
                                this.pc.tryNextSource();
                              }}
                            >
                              ${this.pc.t("tryNext")}
                            </button>
                          `
                        : nothing}
                      ${this.devMode
                        ? html`
                            <button
                              type="button"
                              class="fw-error-btn fw-error-btn--secondary"
                              aria-label=${this.pc.t("reloadPlayer")}
                              @click=${() => {
                                this.pc.clearError();
                                this.pc.reload();
                              }}
                            >
                              ${this.pc.t("reloadPlayer")}
                            </button>
                          `
                        : nothing}
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
                      aria-label=${this.pc.t("dismiss")}
                    >
                      ${closeIcon()}
                    </button>
                  </div>
                </div>
              `
            : nothing}

          <!-- Player controls: slot allows custom controls, fallback renders defaults -->
          ${!this._useStockControls
            ? html`
                <slot name="controls">
                  <fw-player-controls
                    part="controls"
                    .pc=${this.pc}
                    .playbackMode=${this.playbackMode}
                    .isContentLive=${s.isEffectivelyLive}
                    .devMode=${this.devMode}
                    .isStatsOpen=${this._isStatsOpen}
                    .activeLocale=${this.locale ?? "en"}
                    @fw-stats-toggle=${() => {
                      this._isStatsOpen = !this._isStatsOpen;
                    }}
                    @fw-mode-change=${(event: CustomEvent<{ mode: PlaybackMode }>) => {
                      this.playbackMode = event.detail.mode;
                    }}
                    @fw-locale-change=${(e: CustomEvent) => {
                      this.locale = e.detail.locale;
                    }}
                  ></fw-player-controls>
                </slot>
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
                @fw-playback-mode-change=${(event: CustomEvent<{ mode: PlaybackMode }>) => {
                  this.playbackMode = event.detail.mode;
                }}
              ></fw-dev-mode-panel>
            `
          : nothing}
      </div>

      <!-- Context menu -->
      <!-- Keep menu in-shadow (no document portal) to preserve host-scoped styling and avoid a global overlay manager. -->
      ${this._contextMenuMounted
        ? html`
            <div
              data-context-menu="true"
              data-state=${this._contextMenuState}
              data-side=${this._contextMenuSide}
              class="fw-context-menu"
              role="menu"
              aria-label="Player options"
              tabindex="-1"
              style="position: absolute; left: ${this._contextMenuX}px; top: ${this
                ._contextMenuY}px;"
              @contextmenu=${(e: MouseEvent) => e.preventDefault()}
              @keydown=${this._handleContextMenuKeyDown}
            >
              <button
                type="button"
                role="menuitem"
                tabindex="-1"
                data-context-menu-item="true"
                data-context-menu-level="root"
                class="fw-context-menu-item gap-2"
                @click=${() => {
                  this._isStatsOpen = !this._isStatsOpen;
                  this._closeContextMenu();
                }}
              >
                <span class="opacity-70 shrink-0">${statsIcon(14)}</span>
                <span>${this._isStatsOpen ? this.pc.t("hideStats") : this.pc.t("showStats")}</span>
              </button>
              ${this.devMode
                ? html`
                    <div class="fw-context-menu-separator"></div>
                    <button
                      type="button"
                      role="menuitem"
                      tabindex="-1"
                      data-context-menu-item="true"
                      data-context-menu-level="root"
                      class="fw-context-menu-item gap-2"
                      @click=${() => {
                        this._isDevPanelOpen = !this._isDevPanelOpen;
                        this._closeContextMenu();
                      }}
                    >
                      <span class="opacity-70 shrink-0">${settingsIcon(14)}</span>
                      <span
                        >${this._isDevPanelOpen
                          ? this.pc.t("hideSettings")
                          : this.pc.t("settings")}</span
                      >
                    </button>
                  `
                : nothing}
              <div class="fw-context-menu-separator"></div>
              <button
                type="button"
                role="menuitemcheckbox"
                aria-checked=${String(s.isPiPActive)}
                tabindex="-1"
                data-context-menu-item="true"
                data-context-menu-level="root"
                class="fw-context-menu-item gap-2"
                @click=${() => {
                  this.pc.togglePiP();
                  this._closeContextMenu();
                }}
              >
                <span class="opacity-70 shrink-0">${pictureInPictureIcon(14)}</span>
                <span>${this.pc.t("pictureInPicture")}</span>
              </button>
              <button
                type="button"
                role="menuitemcheckbox"
                aria-checked=${String(s.isLoopEnabled)}
                tabindex="-1"
                data-context-menu-item="true"
                data-context-menu-level="root"
                class="fw-context-menu-item gap-2"
                @click=${() => {
                  this.pc.toggleLoop();
                  this._closeContextMenu();
                }}
              >
                <span class="opacity-70 shrink-0">${loopIcon(14)}</span>
                <span>${s.isLoopEnabled ? this.pc.t("disableLoop") : this.pc.t("enableLoop")}</span>
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
