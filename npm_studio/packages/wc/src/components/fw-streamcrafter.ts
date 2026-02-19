/**
 * <fw-streamcrafter> — Main orchestrator for StreamCrafter Web Component.
 * Port of StreamCrafter.tsx from streamcrafter-react.
 */
import { LitElement, html, css, nothing } from "lit";
import { customElement, property, state, query } from "lit/decorators.js";
import { classMap } from "lit/directives/class-map.js";
import { sharedStyles } from "../styles/shared-styles.js";
import { utilityStyles } from "../styles/utility-styles.js";
import {
  cameraIcon,
  monitorIcon,
  settingsIcon,
  chevronsRightIcon,
  chevronsLeftIcon,
  micIcon,
  micMutedIcon,
  videoIcon,
  xIcon,
  eyeIcon,
  eyeOffIcon,
} from "../icons/index.js";
import { IngestControllerHost } from "../controllers/ingest-controller-host.js";
import {
  IngestClient,
  type CompositorConfig,
  getAudioConstraints,
  type EncoderOverrides,
  type IngestClientStatus,
  type IngestState,
  type IngestStateContextV2,
  type LayoutConfig,
  type MediaSource,
  type QualityProfile,
  type ReconnectionState,
  type FwThemePreset,
  type StudioThemeOverrides,
  applyStudioTheme,
  applyStudioThemeOverrides,
  clearStudioTheme,
  createStudioTranslator,
  type StudioTranslateFn,
  type StudioLocale,
} from "@livepeer-frameworks/streamcrafter-core";

interface AudioProcessingSettings {
  echoCancellation: boolean;
  noiseSuppression: boolean;
  autoGainControl: boolean;
}

function getStatusBadgeClass(state: IngestState, isReconnecting: boolean): string {
  if (state === "streaming") return "fw-sc-badge fw-sc-badge--live";
  if (isReconnecting) return "fw-sc-badge fw-sc-badge--connecting";
  if (state === "error") return "fw-sc-badge fw-sc-badge--error";
  if (state === "capturing") return "fw-sc-badge fw-sc-badge--ready";
  return "fw-sc-badge fw-sc-badge--idle";
}

function getInitialAudioProcessing(profile: QualityProfile): AudioProcessingSettings {
  const defaults = getAudioConstraints(profile);
  return {
    echoCancellation: defaults.echoCancellation,
    noiseSuppression: defaults.noiseSuppression,
    autoGainControl: defaults.autoGainControl,
  };
}

const _iifeScriptSrc: string | undefined =
  typeof document !== "undefined"
    ? (document.currentScript?.getAttribute("src") ?? undefined)
    : undefined;

@customElement("fw-streamcrafter")
export class FwStreamCrafter extends LitElement {
  @property({ type: String, attribute: "whip-url" }) whipUrl = "";
  @property({ type: String, attribute: "gateway-url" }) gatewayUrl = "";
  @property({ type: String, attribute: "stream-key" }) streamKey = "";
  @property({ type: String, attribute: "initial-profile" }) initialProfile: QualityProfile =
    "broadcast";
  @property({ type: Boolean, attribute: "auto-start-camera" }) autoStartCamera = false;
  @property({ type: Boolean, attribute: "dev-mode" }) devMode = false;
  @property({ type: Boolean, attribute: "show-settings" }) showSettings = false;
  @property({ type: Boolean }) debug = false;
  @property({ type: Boolean, attribute: "enable-compositor" }) enableCompositor = true;
  @property({ type: String }) theme: FwThemePreset | "" = "";
  @property({ type: String, attribute: "class-name" }) className = "";
  @property({ attribute: false }) onStateChange?:
    | ((state: IngestState, context?: IngestStateContextV2) => void)
    | undefined;
  @property({ attribute: false }) onError?: ((error: string) => void) | undefined;
  @property({ attribute: false }) compositorConfig: Partial<CompositorConfig> = {};
  @property({ type: String, attribute: "compositor-worker-url" }) compositorWorkerUrl = "";
  @property({ type: String, attribute: "encoder-worker-url" }) encoderWorkerUrl = "";
  @property({ type: String, attribute: "rtc-transform-worker-url" }) rtcTransformWorkerUrl = "";
  @property({ type: String }) locale: StudioLocale = "en";
  /** Set to "false" to hide built-in controls. Set to "stock" for minimal controls. */
  @property({ type: String }) controls: string = "";

  @state() private _showSettings = false;
  @state() private _showSources = true;
  @state() private _isAdvancedPanelOpen = false;
  @state() private _contextMenu: { x: number; y: number } | null = null;
  @state() private _resolvedWhipUrl = "";
  @state() private _endpointStatus: IngestClientStatus = "idle";
  @state() private _endpointError: string | null = null;
  @state() private _audioProcessing: AudioProcessingSettings;
  @state() private _encoderOverrides: EncoderOverrides = {};

  @query(".fw-sc-preview video") private _videoEl!: HTMLVideoElement | null;
  @query(".fw-sc-settings-popup") private _settingsPopupEl!: HTMLElement | null;
  @query(".fw-sc-action-secondary--settings") private _settingsButtonEl!: HTMLElement | null;
  @query(".fw-sc-context-menu") private _contextMenuEl!: HTMLElement | null;

  pc: IngestControllerHost;
  private _t: StudioTranslateFn = createStudioTranslator({ locale: "en" });
  private _ingestClient: IngestClient | null = null;
  private _lastInitKey = "";
  private _dismissListenersAttached = false;
  private _showSettingsInitialized = false;
  private _compositorEnabling: Promise<void> | null = null;
  private _handleStateChangeEvent = (event: Event) => {
    const detail = (event as CustomEvent<{ state: IngestState; context?: IngestStateContextV2 }>)
      .detail;
    this.onStateChange?.(detail.state, detail.context);
  };
  private _handleErrorEvent = (event: Event) => {
    const detail = (event as CustomEvent<{ error: string }>).detail;
    this.onError?.(detail.error);
  };

  static styles = [
    sharedStyles,
    utilityStyles,
    css`
      :host {
        display: block;
        height: 100%;
        min-height: 0;
        overflow: hidden;
      }
    `,
  ];

  constructor() {
    super();
    this.pc = new IngestControllerHost(this, this.initialProfile);
    this._audioProcessing = getInitialAudioProcessing(this.initialProfile);
  }

  connectedCallback() {
    super.connectedCallback();
    this.addEventListener("fw-sc-state-change", this._handleStateChangeEvent);
    this.addEventListener("fw-sc-error", this._handleErrorEvent);
    if (!this._showSettingsInitialized) {
      this._showSettings = this.showSettings;
      this._showSettingsInitialized = true;
    }
    this._refreshResolvedWhipUrl();
  }

  disconnectedCallback() {
    this.removeEventListener("fw-sc-state-change", this._handleStateChangeEvent);
    this.removeEventListener("fw-sc-error", this._handleErrorEvent);
    this._teardownDismissListeners();
    this._destroyIngestClient();
    super.disconnectedCallback();
  }

  willUpdate(changed: Map<string, unknown>) {
    if (changed.has("locale")) {
      this._t = createStudioTranslator({ locale: this.locale });
    }

    if (changed.has("whipUrl") || changed.has("gatewayUrl") || changed.has("streamKey")) {
      this._refreshResolvedWhipUrl();
    }

    if (
      changed.has("_resolvedWhipUrl") ||
      changed.has("initialProfile") ||
      changed.has("debug") ||
      changed.has("compositorWorkerUrl") ||
      changed.has("encoderWorkerUrl") ||
      changed.has("rtcTransformWorkerUrl")
    ) {
      this._initController();
    }

    if (changed.has("enableCompositor") || changed.has("compositorConfig")) {
      this._syncCompositor();
    }

    if (changed.has("_showSettings") || changed.has("_contextMenu")) {
      this._syncDismissListeners();
    }
  }

  updated(changed: Map<string, unknown>) {
    this._syncVideoPreview();
    if (changed.has("theme")) this._applyTheme();
  }

  private _applyTheme() {
    const root = this.renderRoot.querySelector<HTMLElement>(".fw-sc-root");
    if (!root) return;
    if (!this.theme || this.theme === "default") {
      clearStudioTheme(root);
    } else {
      applyStudioTheme(root, this.theme as FwThemePreset);
    }
  }

  private _refreshResolvedWhipUrl() {
    if (this.whipUrl) {
      this._destroyIngestClient();
      this._resolvedWhipUrl = this.whipUrl;
      this._endpointStatus = "idle";
      this._endpointError = null;
      return;
    }

    if (this.gatewayUrl && this.streamKey) {
      this._resolveGatewayEndpoint();
      return;
    }

    this._destroyIngestClient();
    this._resolvedWhipUrl = "";
    this._endpointStatus = "idle";
    this._endpointError = null;
    this._lastInitKey = "";
  }

  private _destroyIngestClient() {
    if (!this._ingestClient) return;
    this._ingestClient.destroy();
    this._ingestClient = null;
  }

  private _resolveWorkerUrl(fileName: string, explicitUrl: string): string {
    if (explicitUrl) return explicitUrl;

    if (typeof _iifeScriptSrc === "string" && _iifeScriptSrc) {
      try {
        return new URL(`./workers/${fileName}`, _iifeScriptSrc).href;
      } catch {
        // Fall through.
      }
    }

    return "";
  }

  private _resolveWorkerConfig() {
    const compositor = this._resolveWorkerUrl("compositor.worker.js", this.compositorWorkerUrl);
    const encoder = this._resolveWorkerUrl("encoder.worker.js", this.encoderWorkerUrl);
    const rtcTransform = this._resolveWorkerUrl(
      "rtcTransform.worker.js",
      this.rtcTransformWorkerUrl
    );

    return {
      ...(compositor ? { compositor } : {}),
      ...(encoder ? { encoder } : {}),
      ...(rtcTransform ? { rtcTransform } : {}),
    };
  }

  private async _resolveGatewayEndpoint() {
    if (!this.gatewayUrl || !this.streamKey || this.whipUrl) return;

    this._destroyIngestClient();
    this._endpointStatus = "loading";
    this._endpointError = null;
    this._resolvedWhipUrl = "";

    const client = new IngestClient({
      gatewayUrl: this.gatewayUrl,
      streamKey: this.streamKey,
      maxRetries: 3,
      initialDelayMs: 1000,
    });
    this._ingestClient = client;

    client.on("statusChange", ({ status, error }) => {
      if (this._ingestClient !== client) return;
      this._endpointStatus = status;
      if (error) {
        this._endpointError = error;
      } else if (status !== "error") {
        this._endpointError = null;
      }
    });

    client.on("endpointsResolved", ({ endpoints }) => {
      if (this._ingestClient !== client) return;
      this._resolvedWhipUrl = endpoints.primary?.whipUrl ?? "";
      this._endpointError = null;
    });

    try {
      const endpoints = await client.resolve();
      if (this._ingestClient !== client) return;
      this._resolvedWhipUrl = endpoints.primary?.whipUrl ?? "";
      this._endpointError = null;
    } catch (error) {
      if (this._ingestClient !== client) return;
      if ((error as Error)?.name === "AbortError") return;
      const message = error instanceof Error ? error.message : "Unknown error";
      this._endpointError = message;
      this._endpointStatus = "error";
      this._resolvedWhipUrl = "";
    }
  }

  private _initController() {
    if (!this._resolvedWhipUrl) {
      this._lastInitKey = "";
      return;
    }

    const initKey = [
      this._resolvedWhipUrl,
      this.initialProfile,
      String(this.debug),
      this.compositorWorkerUrl,
      this.encoderWorkerUrl,
      this.rtcTransformWorkerUrl,
    ].join("|");
    if (this._lastInitKey === initKey && this.pc.getController()) return;

    this.pc.initialize({
      whipUrl: this._resolvedWhipUrl,
      profile: this.initialProfile,
      debug: this.debug,
      reconnection: { enabled: true, maxAttempts: 5 },
      audioMixing: true,
      workers: this._resolveWorkerConfig(),
    });
    this.pc.setEncoderOverrides(this._encoderOverrides);
    this._syncCompositor();

    this._lastInitKey = initKey;

    if (this.autoStartCamera && this.pc.s.state === "idle") {
      this.pc.startCamera().catch(console.error);
    }
  }

  private _syncCompositor() {
    const controller = this.pc.getController();
    if (!controller) return;

    if (!this.enableCompositor) {
      if (controller.getSceneManager()) {
        controller.disableCompositor();
        this.requestUpdate();
      }
      return;
    }

    if (controller.getSceneManager() || this._compositorEnabling) {
      return;
    }

    const config =
      this.compositorConfig && Object.keys(this.compositorConfig).length > 0
        ? this.compositorConfig
        : undefined;

    this._compositorEnabling = controller
      .enableCompositor(config)
      .then(() => {
        if (!this.enableCompositor && controller.getSceneManager()) {
          controller.disableCompositor();
        }
        this.requestUpdate();
      })
      .catch((error) => {
        console.error("Failed to enable compositor:", error);
      })
      .finally(() => {
        this._compositorEnabling = null;
      });
  }

  private _syncVideoPreview() {
    const video = this._videoEl;
    const stream = this.pc.s.mediaStream;
    if (video && stream && video.srcObject !== stream) {
      video.srcObject = stream;
      video.play().catch(() => {});
    } else if (video && !stream) {
      video.srcObject = null;
    }
  }

  private _eventHitsElement(event: Event, element: HTMLElement | null): boolean {
    if (!element) return false;
    const path = event.composedPath();
    if (path.includes(element)) return true;
    const target = event.target;
    return target instanceof Node ? element.contains(target) : false;
  }

  private _handleDocumentMouseDown = (event: MouseEvent) => {
    if (this._showSettings) {
      const clickedSettingsPopup = this._eventHitsElement(event, this._settingsPopupEl);
      const clickedSettingsButton = this._eventHitsElement(event, this._settingsButtonEl);
      if (!clickedSettingsPopup && !clickedSettingsButton) {
        this._showSettings = false;
      }
    }

    if (this._contextMenu) {
      const clickedContextMenu = this._eventHitsElement(event, this._contextMenuEl);
      if (!clickedContextMenu) {
        this._contextMenu = null;
      }
    }
  };

  private _handleDocumentKeyDown = (event: KeyboardEvent) => {
    if (event.key !== "Escape") return;
    if (this._showSettings) this._showSettings = false;
    if (this._contextMenu) this._contextMenu = null;
  };

  private _syncDismissListeners() {
    const shouldAttach = this._showSettings || !!this._contextMenu;
    if (shouldAttach && !this._dismissListenersAttached) {
      document.addEventListener("mousedown", this._handleDocumentMouseDown);
      document.addEventListener("keydown", this._handleDocumentKeyDown);
      this._dismissListenersAttached = true;
    } else if (!shouldAttach && this._dismissListenersAttached) {
      this._teardownDismissListeners();
    }
  }

  private _teardownDismissListeners() {
    if (!this._dismissListenersAttached) return;
    document.removeEventListener("mousedown", this._handleDocumentMouseDown);
    document.removeEventListener("keydown", this._handleDocumentKeyDown);
    this._dismissListenersAttached = false;
  }

  private _handleContextMenu(event: MouseEvent) {
    event.preventDefault();
    event.stopPropagation();
    this._contextMenu = { x: event.clientX, y: event.clientY };
  }

  private _copyWhipUrl() {
    if (this._resolvedWhipUrl) {
      navigator.clipboard.writeText(this._resolvedWhipUrl).catch(console.error);
    }
    this._contextMenu = null;
  }

  private _copyStreamInfo() {
    const s = this.pc.s;
    const profileLabel = this._t(s.qualityProfile as any);
    const info = [
      `Status: ${s.state}`,
      `Quality: ${profileLabel}`,
      `Sources: ${s.sources.length}`,
      this._resolvedWhipUrl ? `WHIP: ${this._resolvedWhipUrl}` : null,
    ]
      .filter(Boolean)
      .join("\n");
    navigator.clipboard.writeText(info).catch(console.error);
    this._contextMenu = null;
  }

  async startCamera(options?: Parameters<IngestControllerHost["startCamera"]>[0]) {
    return this.pc.startCamera(options);
  }
  async startScreenShare(options?: Parameters<IngestControllerHost["startScreenShare"]>[0]) {
    return this.pc.startScreenShare(options);
  }
  async startStreaming() {
    return this.pc.startStreaming();
  }
  async stopStreaming() {
    return this.pc.stopStreaming();
  }
  async stopCapture() {
    return this.pc.stopCapture();
  }
  removeSource(id: string) {
    this.pc.removeSource(id);
  }
  addCustomSource(stream: MediaStream, label: string) {
    return this.pc.addCustomSource(stream, label);
  }
  setSourceVolume(id: string, vol: number) {
    this.pc.setSourceVolume(id, vol);
  }
  setSourceMuted(id: string, muted: boolean) {
    this.pc.setSourceMuted(id, muted);
  }
  setSourceActive(id: string, active: boolean) {
    this.pc.setSourceActive(id, active);
  }
  setPrimaryVideoSource(id: string) {
    this.pc.setPrimaryVideoSource(id);
  }
  setMasterVolume(vol: number) {
    this.pc.setMasterVolume(vol);
  }
  getMasterVolume() {
    return this.pc.getMasterVolume();
  }
  async setQualityProfile(profile: QualityProfile) {
    return this.pc.setQualityProfile(profile);
  }
  async getDevices() {
    return this.pc.getDevices();
  }
  async switchVideoDevice(deviceId: string) {
    return this.pc.switchVideoDevice(deviceId);
  }
  async switchAudioDevice(deviceId: string) {
    return this.pc.switchAudioDevice(deviceId);
  }
  async getStats() {
    return this.pc.getStats();
  }
  setUseWebCodecs(enabled: boolean) {
    this.pc.setUseWebCodecs(enabled);
  }
  setEncoderOverrides(overrides: EncoderOverrides) {
    this._encoderOverrides = overrides;
    this.pc.setEncoderOverrides(overrides);
  }
  getController() {
    return this.pc.getController();
  }

  /** Expose the controller host for external access. */
  get controller(): IngestControllerHost {
    return this.pc;
  }

  destroy() {
    this._destroyIngestClient();
    this.pc.getController()?.destroy();
  }

  private _getStatusText(state: IngestState, reconnectionState?: ReconnectionState | null): string {
    if (reconnectionState?.isReconnecting) {
      return this._t("reconnectingAttempt", { attempt: reconnectionState.attemptNumber, max: 5 });
    }
    switch (state) {
      case "idle":
        return this._t("idle");
      case "requesting_permissions":
        return this._t("requestingPermissions");
      case "capturing":
        return this._t("ready");
      case "connecting":
        return this._t("connecting");
      case "streaming":
        return this._t("live");
      case "reconnecting":
        return this._t("reconnecting");
      case "error":
        return this._t("error");
      case "destroyed":
        return this._t("destroyed");
      default:
        return state;
    }
  }

  protected render() {
    const s = this.pc.s;
    const hideControls = this.controls === "false";

    // Headless mode: only a wrapper div, user provides all UI via slot
    if (hideControls) {
      return html`
        <div class="root fw-sc-root ${this.className}">
          <slot></slot>
        </div>
      `;
    }

    const statusText = this._getStatusText(s.state, s.reconnectionState);
    const statusBadgeClass = getStatusBadgeClass(s.state, s.isReconnecting);
    const canAddSource = s.state !== "destroyed" && s.state !== "error";
    const canStream = s.isCapturing && !s.isStreaming && !!this._resolvedWhipUrl;
    const hasCamera = s.sources.some((src: MediaSource) => src.type === "camera");
    const isResolvingEndpoint =
      !this.whipUrl && !!this.gatewayUrl && !!this.streamKey && this._endpointStatus === "loading";
    const activeScene = this._getActiveScene();
    const rootClassName = [
      "root",
      "fw-sc-root",
      this.devMode ? "fw-sc-root--devmode" : "",
      this.className,
    ]
      .filter(Boolean)
      .join(" ");

    return html`
      <div
        class=${rootClassName}
        @contextmenu=${(event: MouseEvent) => this._handleContextMenu(event)}
      >
        <div class="main fw-sc-main">
          <div class="fw-sc-header">
            <span class="fw-sc-header-title">${this._t("streamCrafter")}</span>
            <div class="fw-sc-header-status">
              <span class=${statusBadgeClass}>${statusText}</span>
            </div>
          </div>

          <div class="fw-sc-content">
            <div class="fw-sc-preview-wrapper">
              <div class="fw-sc-preview">
                <video playsinline muted autoplay aria-label=${this._t("streamPreview")}></video>

                ${!s.mediaStream
                  ? html`
                      <div class="fw-sc-preview-placeholder">
                        ${cameraIcon(48)}
                        <span>${this._t("addSourcePrompt")}</span>
                      </div>
                    `
                  : nothing}
                ${s.state === "connecting" || s.state === "reconnecting"
                  ? html`
                      <div class="fw-sc-status-overlay">
                        <div class="fw-sc-status-spinner"></div>
                        <span class="fw-sc-status-text">${statusText}</span>
                      </div>
                    `
                  : nothing}
                ${s.isStreaming
                  ? html`<div class="fw-sc-live-badge">${this._t("live")}</div>`
                  : nothing}
                ${this.enableCompositor
                  ? html`
                      <fw-sc-compositor
                        .isEnabled=${this.enableCompositor}
                        .isInitialized=${!!activeScene}
                        .rendererType=${this._getCompositorRendererType()}
                        .stats=${this._getCompositorStats()}
                        .sources=${s.sources}
                        .layers=${activeScene?.layers ?? []}
                        .currentLayout=${this._getCurrentLayout()}
                        .t=${this._t}
                        @fw-sc-layout-apply=${(event: CustomEvent<{ layout: LayoutConfig }>) =>
                          this._handleCompositorLayoutApply(event)}
                        @fw-sc-cycle-source-order=${(
                          event: CustomEvent<{ direction?: "forward" | "backward" }>
                        ) => this._handleCompositorCycleSourceOrder(event)}
                      ></fw-sc-compositor>
                    `
                  : nothing}
              </div>
            </div>

            ${s.sources.length > 0
              ? html`
                  <div
                    class=${classMap({
                      "fw-sc-section": true,
                      "fw-sc-mixer": true,
                      "fw-sc-section--collapsed": !this._showSources,
                    })}
                  >
                    <div
                      class="fw-sc-section-header"
                      @click=${() => {
                        this._showSources = !this._showSources;
                      }}
                    >
                      <span>${this._t("mixer")} (${s.sources.length})</span>
                      ${this._showSources ? chevronsRightIcon(14) : chevronsLeftIcon(14)}
                    </div>
                    ${this._showSources
                      ? html`
                          <div class="fw-sc-section-body--flush">
                            <div class="fw-sc-sources">
                              ${s.sources.map((source: MediaSource) =>
                                this._renderSourceRow(source)
                              )}
                            </div>
                          </div>
                        `
                      : nothing}
                  </div>
                `
              : nothing}
          </div>

          ${s.isCapturing
            ? html`
                <div class="fw-sc-vu-meter">
                  <div
                    class="fw-sc-vu-meter-fill"
                    style="width:${Math.min(s.audioLevel * 100, 100)}%"
                  ></div>
                  <div
                    class="fw-sc-vu-meter-peak"
                    style="left:${Math.min(s.peakAudioLevel * 100, 100)}%"
                  ></div>
                </div>
              `
            : nothing}
          ${s.error || this._endpointError
            ? html`
                <div class="fw-sc-error">
                  <div class="fw-sc-error-title">${this._t("error")}</div>
                  <div class="fw-sc-error-message">${s.error || this._endpointError}</div>
                </div>
              `
            : nothing}
          ${!this._resolvedWhipUrl && !s.error && !this._endpointError && !isResolvingEndpoint
            ? html`
                <div class="fw-sc-error" style="border-left-color: hsl(40 80% 65%)">
                  <div class="fw-sc-error-title" style="color: hsl(40 80% 65%)">
                    ${this._t("warning")}
                  </div>
                  <div class="fw-sc-error-message">${this._t("configureWhipEndpoint")}</div>
                </div>
              `
            : nothing}
          ${isResolvingEndpoint
            ? html`
                <div class="fw-sc-error" style="border-left-color: hsl(210 80% 65%)">
                  <div class="fw-sc-error-title" style="color: hsl(210 80% 65%)">
                    ${this._t("resolvingEndpoint")}
                  </div>
                  <div class="fw-sc-error-message">${this._t("resolvingEndpoint")}</div>
                </div>
              `
            : nothing}

          <div class="fw-sc-actions">
            <button
              type="button"
              class="fw-sc-action-secondary"
              @click=${() => this.pc.startCamera().catch(console.error)}
              ?disabled=${!canAddSource || hasCamera}
              title=${hasCamera ? this._t("cameraActive") : this._t("addCamera")}
            >
              ${cameraIcon(18)}
            </button>
            <button
              type="button"
              class="fw-sc-action-secondary"
              @click=${() => this.pc.startScreenShare({ audio: true }).catch(console.error)}
              ?disabled=${!canAddSource}
              title=${this._t("shareScreen")}
            >
              ${monitorIcon(18)}
            </button>

            <div style="position:relative">
              <button
                type="button"
                class=${classMap({
                  "fw-sc-action-secondary": true,
                  "fw-sc-action-secondary--active": this._showSettings,
                  "fw-sc-action-secondary--settings": true,
                })}
                @click=${() => {
                  this._showSettings = !this._showSettings;
                }}
                title=${this._t("settings")}
              >
                ${settingsIcon(16)}
              </button>
              ${this._showSettings ? this._renderSettingsPopup() : nothing}
            </div>
            ${!s.isStreaming
              ? html`
                  <button
                    type="button"
                    class="fw-sc-action-primary"
                    @click=${() => this.pc.startStreaming().catch(console.error)}
                    ?disabled=${!canStream}
                  >
                    ${s.state === "connecting" ? this._t("connecting") : this._t("goLive")}
                  </button>
                `
              : html`
                  <button
                    type="button"
                    class="fw-sc-action-primary fw-sc-action-stop"
                    @click=${() => this.pc.stopStreaming().catch(console.error)}
                  >
                    ${this._t("stopStreaming")}
                  </button>
                `}
          </div>
        </div>

        ${this._contextMenu
          ? html`
              <div
                class="fw-sc-context-menu"
                style="position:fixed;top:${this._contextMenu.y}px;left:${this._contextMenu
                  .x}px;z-index:1000;background:#1a1b26;border:1px solid rgba(90,96,127,0.3);border-radius:6px;padding:4px;box-shadow:0 4px 12px rgba(0,0,0,0.5);min-width:160px"
              >
                ${this._resolvedWhipUrl
                  ? html`
                      <button
                        type="button"
                        class="fw-sc-context-menu-item"
                        @mousedown=${(event: MouseEvent) => {
                          event.preventDefault();
                          this._copyWhipUrl();
                        }}
                      >
                        ${this._t("copyWhipUrl")}
                      </button>
                    `
                  : nothing}
                <button
                  type="button"
                  class="fw-sc-context-menu-item"
                  @mousedown=${(event: MouseEvent) => {
                    event.preventDefault();
                    this._copyStreamInfo();
                  }}
                >
                  ${this._t("copyStreamInfo")}
                </button>
                ${this.devMode
                  ? html`
                      <div class="fw-sc-context-menu-separator"></div>
                      <button
                        type="button"
                        class="fw-sc-context-menu-item"
                        @mousedown=${(event: MouseEvent) => {
                          event.preventDefault();
                          this._isAdvancedPanelOpen = !this._isAdvancedPanelOpen;
                          this._contextMenu = null;
                        }}
                      >
                        ${settingsIcon(14)}
                        <span
                          >${this._isAdvancedPanelOpen
                            ? this._t("hideAdvanced")
                            : this._t("advanced")}</span
                        >
                      </button>
                    `
                  : nothing}
              </div>
            `
          : nothing}
        ${this.devMode && this._isAdvancedPanelOpen
          ? html`
              <fw-sc-advanced
                .ic=${this.pc}
                .whipUrl=${this._resolvedWhipUrl}
                .audioProcessing=${this._audioProcessing}
                .encoderOverrides=${this._encoderOverrides}
                .compositorEnabled=${this.enableCompositor}
                .compositorRendererType=${this._getCompositorRendererType()}
                .compositorStats=${this._getCompositorStats()}
                .sceneCount=${this._getSceneCount()}
                .layerCount=${this._getLayerCount()}
                .t=${this._t}
                @fw-audio-processing-change=${(
                  event: CustomEvent<{ settings: Partial<AudioProcessingSettings> }>
                ) => this._handleAudioProcessingChange(event)}
                @fw-encoder-overrides-change=${(
                  event: CustomEvent<{ overrides: EncoderOverrides }>
                ) => this._handleEncoderOverridesChange(event)}
                @fw-close=${() => {
                  this._isAdvancedPanelOpen = false;
                }}
              ></fw-sc-advanced>
            `
          : nothing}
      </div>
    `;
  }

  private _getActiveScene() {
    if (!this.enableCompositor) return null;
    return this.pc.getController()?.getSceneManager()?.getActiveScene() ?? null;
  }

  private _getCurrentLayout(): LayoutConfig | null {
    if (!this.enableCompositor) return null;
    return this.pc.getController()?.getSceneManager()?.getCurrentLayout() ?? null;
  }

  private _handleCompositorLayoutApply(event: CustomEvent<{ layout: LayoutConfig }>) {
    const sceneManager = this.pc.getController()?.getSceneManager();
    if (!sceneManager) return;

    const layout = event.detail.layout;
    const currentScene = sceneManager.getActiveScene();
    const preservedOrder = currentScene?.layers.map((layer) => layer.sourceId) ?? [];

    sceneManager.applyLayout(layout);
    if (currentScene && preservedOrder.length > 0) {
      sceneManager.reorderLayers(currentScene.id, preservedOrder);
    }

    this.requestUpdate();
  }

  private _handleCompositorCycleSourceOrder(
    event: CustomEvent<{ direction?: "forward" | "backward" }>
  ) {
    const sceneManager = this.pc.getController()?.getSceneManager();
    if (!sceneManager) return;
    sceneManager.cycleSourceOrder(event.detail.direction ?? "forward");
    this.requestUpdate();
  }

  private _getSourceLayerVisibility(sourceId: string): boolean {
    const scene = this._getActiveScene();
    if (!scene) return true;
    const layer = scene.layers.find((candidate) => candidate.sourceId === sourceId);
    return layer?.visible ?? true;
  }

  private _toggleSourceLayerVisibility(sourceId: string) {
    const sceneManager = this.pc.getController()?.getSceneManager();
    const scene = sceneManager?.getActiveScene();
    if (!sceneManager || !scene) return;

    const layer = scene.layers.find((candidate) => candidate.sourceId === sourceId);
    if (!layer) return;

    sceneManager.setLayerVisibility(scene.id, layer.id, !layer.visible);
    this.requestUpdate();
  }

  private _handleAudioProcessingChange(
    event: CustomEvent<{ settings: Partial<AudioProcessingSettings> }>
  ) {
    const next = {
      ...this._audioProcessing,
      ...event.detail.settings,
    };
    this._audioProcessing = next;

    for (const source of this.pc.s.sources) {
      source.stream.getAudioTracks().forEach((track) => {
        track
          .applyConstraints({
            echoCancellation: next.echoCancellation,
            noiseSuppression: next.noiseSuppression,
            autoGainControl: next.autoGainControl,
          })
          .catch((error) => {
            console.warn("Failed to apply audio constraints:", error);
          });
      });
    }
  }

  private _handleEncoderOverridesChange(event: CustomEvent<{ overrides: EncoderOverrides }>) {
    this.setEncoderOverrides(event.detail.overrides);
  }

  private _getCompositorRendererType() {
    if (!this.enableCompositor) return null;
    return this.pc.getController()?.getSceneManager()?.getRendererType() ?? null;
  }

  private _getCompositorStats() {
    if (!this.enableCompositor) return null;
    return this.pc.getController()?.getSceneManager()?.getStats() ?? null;
  }

  private _getSceneCount(): number {
    if (!this.enableCompositor) return 0;
    return this.pc.getController()?.getSceneManager()?.getAllScenes()?.length ?? 0;
  }

  private _getLayerCount(): number {
    if (!this.enableCompositor) return 0;
    const scene = this.pc.getController()?.getSceneManager()?.getActiveScene();
    return scene?.layers?.length ?? 0;
  }

  private _renderSourceRow(source: MediaSource) {
    const s = this.pc.s;
    const hasVideo = source.stream.getVideoTracks().length > 0;
    const isVisible = this._getSourceLayerVisibility(source.id);
    return html`
      <div class=${classMap({ "fw-sc-source": true, "fw-sc-source--hidden": !isVisible })}>
        ${this.enableCompositor
          ? html`
              <button
                type="button"
                class=${classMap({
                  "fw-sc-icon-btn": true,
                  "fw-sc-icon-btn--muted": !isVisible,
                })}
                @click=${() => this._toggleSourceLayerVisibility(source.id)}
                title=${isVisible ? this._t("hideFromComposition") : this._t("showInComposition")}
              >
                ${isVisible ? eyeIcon(14) : eyeOffIcon(14)}
              </button>
            `
          : nothing}
        <div class="fw-sc-source-icon">
          ${source.type === "camera" ? cameraIcon(16) : monitorIcon(16)}
        </div>
        <div class="fw-sc-source-info">
          <div class="fw-sc-source-label">
            ${source.label}
            ${source.primaryVideo && !this.enableCompositor
              ? html`<span class="fw-sc-primary-badge">${this._t("primary")}</span>`
              : nothing}
          </div>
          <div class="fw-sc-source-type">${source.type}</div>
        </div>
        <div class="fw-sc-source-controls">
          ${hasVideo && !this.enableCompositor
            ? html`
                <button
                  type="button"
                  class=${classMap({
                    "fw-sc-icon-btn": true,
                    "fw-sc-icon-btn--primary": !!source.primaryVideo,
                  })}
                  @click=${() => this.pc.setPrimaryVideoSource(source.id)}
                  ?disabled=${source.primaryVideo}
                  title=${source.primaryVideo
                    ? this._t("primaryVideoSource")
                    : this._t("setAsPrimary")}
                >
                  ${videoIcon(14)}
                </button>
              `
            : nothing}
          <span class="fw-sc-volume-label">${Math.round(source.volume * 100)}%</span>
          <fw-sc-volume
            .value=${source.volume}
            @fw-sc-volume-change=${(event: CustomEvent<{ value: number }>) =>
              this.pc.setSourceVolume(source.id, event.detail.value)}
            compact
          ></fw-sc-volume>
          <button
            type="button"
            class=${classMap({ "fw-sc-icon-btn": true, "fw-sc-icon-btn--active": source.muted })}
            @click=${() => this.pc.setSourceMuted(source.id, !source.muted)}
            title=${source.muted ? this._t("unmute") : this._t("mute")}
          >
            ${source.muted ? micMutedIcon(14) : micIcon(14)}
          </button>
          <button
            type="button"
            class="fw-sc-icon-btn fw-sc-icon-btn--destructive"
            @click=${() => this.pc.removeSource(source.id)}
            ?disabled=${s.isStreaming}
            title=${s.isStreaming ? this._t("cannotRemoveWhileStreaming") : this._t("removeSource")}
          >
            ${xIcon(14)}
          </button>
        </div>
      </div>
    `;
  }

  private _renderSettingsPopup() {
    const s = this.pc.s;
    return html`
      <div
        class="fw-sc-settings-popup"
        style="position:absolute;bottom:100%;left:0;margin-bottom:8px;width:192px;background:#1a1b26;border:1px solid rgba(90,96,127,0.3);box-shadow:0 4px 12px rgba(0,0,0,0.4);border-radius:4px;overflow:hidden;z-index:50"
      >
        <div style="padding:8px;border-bottom:1px solid rgba(90,96,127,0.3)">
          <div
            style="font-size:10px;color:#565f89;text-transform:uppercase;font-weight:600;margin-bottom:4px;padding-left:4px"
          >
            ${this._t("quality")}
          </div>
          <div style="display:flex;flex-direction:column;gap:2px">
            ${[
              {
                id: "professional" as QualityProfile,
                labelKey: "professional" as const,
                descKey: "professionalDesc" as const,
              },
              {
                id: "broadcast" as QualityProfile,
                labelKey: "broadcast" as const,
                descKey: "broadcastDesc" as const,
              },
              {
                id: "conference" as QualityProfile,
                labelKey: "conference" as const,
                descKey: "conferenceDesc" as const,
              },
            ].map(
              (profile) => html`
                <button
                  type="button"
                  @click=${() => {
                    if (!s.isStreaming) {
                      this.pc.setQualityProfile(profile.id).catch(console.error);
                      if (!this.devMode) this._showSettings = false;
                    }
                  }}
                  ?disabled=${s.isStreaming}
                  style="width:100%;padding:6px 8px;text-align:left;font-size:12px;border-radius:4px;border:none;cursor:${s.isStreaming
                    ? "not-allowed"
                    : "pointer"};opacity:${s.isStreaming
                    ? "0.5"
                    : "1"};background:${s.qualityProfile === profile.id
                    ? "rgba(122,162,247,0.2)"
                    : "transparent"};color:${s.qualityProfile === profile.id
                    ? "#7aa2f7"
                    : "#a9b1d6"}"
                >
                  <div style="font-weight:500">${this._t(profile.labelKey)}</div>
                  <div style="font-size:10px;color:#565f89">${this._t(profile.descKey)}</div>
                </button>
              `
            )}
          </div>
        </div>
        ${this.devMode
          ? html`
              <div style="padding:8px">
                <div
                  style="font-size:10px;color:#565f89;text-transform:uppercase;font-weight:600;margin-bottom:4px;padding-left:4px"
                >
                  ${this._t("debug")}
                </div>
                <div
                  style="display:flex;flex-direction:column;gap:4px;padding-left:4px;font-size:12px;font-family:ui-monospace,monospace"
                >
                  <div style="display:flex;justify-content:space-between">
                    <span style="color:#565f89">${this._t("state")}</span
                    ><span style="color:#c0caf5">${s.state}</span>
                  </div>
                  <div style="display:flex;justify-content:space-between">
                    <span style="color:#565f89">${this._t("audio")}</span>
                    <span style="color:${s.isCapturing ? "#9ece6a" : "#565f89"}">
                      ${s.isCapturing ? this._t("active") : this._t("inactive")}
                    </span>
                  </div>
                  <div style="display:flex;justify-content:space-between">
                    <span style="color:#565f89">${this._t("whip")}</span
                    ><span style="color:${this._resolvedWhipUrl ? "#9ece6a" : "#f7768e"}"
                      >${this._resolvedWhipUrl ? this._t("ok") : this._t("notSet")}</span
                    >
                  </div>
                </div>
              </div>
            `
          : nothing}
      </div>
    `;
  }
}

declare global {
  interface HTMLElementTagNameMap {
    "fw-streamcrafter": FwStreamCrafter;
  }
}
