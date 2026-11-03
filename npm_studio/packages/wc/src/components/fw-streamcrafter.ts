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
import { defaultTrueBoolean } from "../attribute-converters.js";
import { STUDIO_UPDATE_EVENT } from "./studio-bound-element.js";
import "./fw-sc-camera-picker.js";
import "./fw-sc-media-button.js";
import "./fw-sc-meter.js";
import "./fw-sc-producer-panel.js";
import "./fw-sc-audio-controls.js";
import "./fw-sc-connection-health.js";
import {
  IngestClient,
  StudioLayoutKeeper,
  type CompositorConfig,
  detectStudioSupport,
  encoderSettingRows,
  formatStreamInfo,
  sendableVideoCodecs,
  withEncoderSetting,
  getAudioConstraints,
  handleStudioSceneHotkey,
  isSourceVisibleInScene,
  type EncoderOverrides,
  type IngestControllerV2,
  type IngestClientStatus,
  type IngestState,
  type IngestStateContextV2,
  type LayoutConfig,
  type MediaSource,
  type QualityProfile,
  type ReconnectionState,
  type FwThemePreset,
  type StudioThemeOverrides,
  type SceneManager,
  type StudioSupport,
  applyStudioTheme,
  applyStudioThemeOverrides,
  clearStudioTheme,
  createStudioTranslator,
  type StudioTranslateFn,
  type StudioTranslationStrings,
  type StudioLocale,
  type StudioLayout,
  type AudioProcessingSettings,
} from "@livepeer-frameworks/streamcrafter-core";

/** Drags and typing settle before the layout is reported. */
const LAYOUT_CHANGE_DELAY_MS = 300;

function getStatusBadgeClass(state: IngestState, isReconnecting: boolean): string {
  if (state === "streaming") return "fw-sc-badge fw-sc-badge--live";
  if (isReconnecting) return "fw-sc-badge fw-sc-badge--connecting";
  if (state === "error") return "fw-sc-badge fw-sc-badge--error";
  if (state === "capturing") return "fw-sc-badge fw-sc-badge--ready";
  return "fw-sc-badge fw-sc-badge--idle";
}

const QUALITY_PROFILES = [
  { id: "professional", labelKey: "professional", descKey: "professionalDesc" },
  { id: "broadcast", labelKey: "broadcast", descKey: "broadcastDesc" },
  { id: "conference", labelKey: "conference", descKey: "conferenceDesc" },
] as const satisfies readonly {
  id: QualityProfile;
  labelKey: keyof StudioTranslationStrings;
  descKey: keyof StudioTranslationStrings;
}[];

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
  /** Refresh authorization/placement immediately before each connection without rebuilding capture. */
  @property({ attribute: false }) resolveWhipUrl?: (signal: AbortSignal) => Promise<string>;
  @property({ type: String, attribute: "gateway-url" }) gatewayUrl = "";
  @property({ type: String, attribute: "stream-key" }) streamKey = "";
  @property({ type: String, attribute: "initial-profile" }) initialProfile: QualityProfile =
    "broadcast";
  @property({ type: Boolean, attribute: "auto-start-camera" }) autoStartCamera = false;
  @property({ type: Boolean, attribute: "dev-mode" }) devMode = false;
  @property({ attribute: "show-producer-panel", converter: defaultTrueBoolean })
  showProducerPanel = true;
  @property({ type: Boolean, attribute: "show-settings" }) showSettings = false;
  @property({ type: Boolean }) debug = false;
  @property({ attribute: "enable-compositor", converter: defaultTrueBoolean })
  enableCompositor = true;
  /** A preset name (also as the `theme` attribute), or custom overrides set as a property. */
  @property({ type: String }) theme: FwThemePreset | StudioThemeOverrides | "" = "";
  @property({ type: String, attribute: "class-name" }) className = "";
  @property({ attribute: false }) onStateChange?:
    | ((state: IngestState, context?: IngestStateContextV2) => void)
    | undefined;
  @property({ attribute: false }) onError?: ((error: string) => void) | undefined;
  /** A new `renderer` switches the running compositor without a restart. */
  @property({ attribute: false }) compositorConfig: Partial<CompositorConfig> = {};
  /**
   * What the layout belongs to, such as a stream ID. A new key shows that
   * key's initialLayout, or the default layout, without restarting sources.
   */
  @property({ type: String, attribute: "layout-key" }) layoutKey?: string;
  /** Scenes, overlays and layout preset for layoutKey, as reported by fw-sc-layout-change. */
  @property({ attribute: false }) initialLayout: StudioLayout | null = null;
  /** Called shortly after the scenes, overlays or layout preset change, with the key they belong to. */
  @property({ attribute: false }) onLayoutChange?:
    | ((layout: StudioLayout, layoutKey: string | undefined) => void)
    | undefined;
  @property({ type: String, attribute: "compositor-worker-url" }) compositorWorkerUrl = "";
  @property({ type: String, attribute: "encoder-worker-url" }) encoderWorkerUrl = "";
  @property({ type: String, attribute: "rtc-transform-worker-url" }) rtcTransformWorkerUrl = "";
  @property({ type: String }) locale: StudioLocale = "en";
  /** Set to "false" to render only a slot for your own UI built from the fw-sc-* elements. */
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
  @state() private _compositorFailed = false;

  @query(".fw-sc-preview video") private _videoEl!: HTMLVideoElement | null;
  @query(".fw-sc-settings-popup") private _settingsPopupEl!: HTMLElement | null;
  @query(".fw-sc-action-secondary--settings") private _settingsButtonEl!: HTMLElement | null;
  @query(".fw-sc-context-menu") private _contextMenuEl!: HTMLElement | null;

  pc: IngestControllerHost;
  private _t: StudioTranslateFn = createStudioTranslator({ locale: "en" });
  private _support = detectStudioSupport();
  private _boundSceneManager: SceneManager | null = null;
  private _sceneManagerUnsubs: Array<() => void> = [];
  private _ingestClient: IngestClient | null = null;
  private _lastInitKey = "";
  private _dismissListenersAttached = false;
  private _showSettingsInitialized = false;
  private _compositorEnabling: Promise<void> | null = null;
  // One keeper for the element's lifetime, so the layout follows a new
  // controller (a new profile, debug setting or worker URL) for the same key.
  private _layoutKeeper = new StudioLayoutKeeper<IngestControllerV2>(LAYOUT_CHANGE_DELAY_MS);
  private _reportedPreset: unknown = null;
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
    this._bindSceneManager(null);
    this._layoutKeeper.cancelReport();
    this._destroyIngestClient();
    super.disconnectedCallback();
  }

  /** Translator for the current locale, shared with the fw-sc-* sub-elements. */
  get t(): StudioTranslateFn {
    return this._t;
  }

  /** What this browser can capture and publish. */
  get support(): StudioSupport {
    return this._support;
  }

  /** The compositor's scene manager while the compositor runs. */
  get sceneManager(): SceneManager | null {
    if (!this.enableCompositor) return null;
    return this.pc.getController()?.getSceneManager() ?? null;
  }

  willUpdate(changed: Map<string, unknown>) {
    if (changed.has("locale")) {
      this._t = createStudioTranslator({ locale: this.locale });
    }

    // Before the controller and compositor below, so a compositor that starts
    // in this update starts with the new key.
    if (
      (changed.has("layoutKey") || changed.has("initialLayout")) &&
      this._layoutKeeper.setLayout(this.layoutKey, this.initialLayout)
    ) {
      this._layoutKeeper
        .apply(this.pc.getController())
        .then(() => this.requestUpdate())
        .catch(() => {});
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
    this._bindSceneManager(this.sceneManager);
    // A layout preset applied without animation fires no scene manager event.
    const preset = this.sceneManager?.getCurrentLayout() ?? null;
    if (preset && preset !== this._reportedPreset) {
      this._reportedPreset = preset;
      this._scheduleLayoutReport();
    }
    this.dispatchEvent(new Event(STUDIO_UPDATE_EVENT, { bubbles: true, composed: true }));
  }

  /**
   * The compositor's scenes, overlays and layout preset. While the compositor
   * is off it is the layout from when it was last on; null when it never ran.
   */
  async exportLayout(): Promise<StudioLayout | null> {
    return (await this.pc.getController()?.exportLayout()) ?? null;
  }

  /** Apply a layout from exportLayout; the compositor has to be on. */
  async importLayout(layout: StudioLayout): Promise<void> {
    const controller = this.pc.getController();
    if (!controller) throw new Error("The studio has no WHIP endpoint yet");
    await controller.importLayout(layout);
    this.requestUpdate();
  }

  private _scheduleLayoutReport() {
    const controller = this.pc.getController();
    if (!controller) return;
    this._layoutKeeper.scheduleReport(controller, (layout, layoutKey) => {
      try {
        this.onLayoutChange?.(layout, layoutKey);
      } catch (error) {
        console.warn("onLayoutChange failed:", error);
      }
      this.dispatchEvent(
        new CustomEvent("fw-sc-layout-change", {
          detail: { layout, layoutKey },
          bubbles: true,
          composed: true,
        })
      );
    });
  }

  /** Renders again on scene and layer changes made through the scene manager by anyone. */
  private _bindSceneManager(sceneManager: SceneManager | null) {
    if (sceneManager === this._boundSceneManager) return;
    this._sceneManagerUnsubs.forEach((unsubscribe) => unsubscribe());
    this._sceneManagerUnsubs = [];
    this._boundSceneManager = sceneManager;
    if (!sceneManager) return;
    const refresh = () => {
      this.requestUpdate();
      this._scheduleLayoutReport();
    };
    this._sceneManagerUnsubs = [
      sceneManager.on("sceneCreated", refresh),
      sceneManager.on("sceneDeleted", refresh),
      sceneManager.on("sceneActivated", refresh),
      sceneManager.on("layerAdded", refresh),
      sceneManager.on("layerRemoved", refresh),
      sceneManager.on("layerUpdated", refresh),
      sceneManager.on("transitionStarted", refresh),
      sceneManager.on("transitionCompleted", refresh),
      sceneManager.on("layoutAnimationCompleted", refresh),
      sceneManager.on("rendererChanged", refresh),
    ];
  }

  // The controller keeps the resolver it was built with; this reaches the current property.
  private _callResolveWhipUrl(signal: AbortSignal): Promise<string> {
    return (
      this.resolveWhipUrl?.(signal) ?? Promise.reject(new Error("resolveWhipUrl is no longer set"))
    );
  }

  private _applyTheme() {
    const root = this.renderRoot.querySelector<HTMLElement>(".fw-sc-root");
    if (!root) return;
    if (!this.theme || this.theme === "default") {
      clearStudioTheme(root);
    } else if (typeof this.theme === "object") {
      clearStudioTheme(root);
      applyStudioThemeOverrides(root, this.theme);
    } else {
      applyStudioTheme(root, this.theme);
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

  // A new profile, debug setting or worker URL builds a new controller; a new
  // endpoint goes to the running one, which keeps its sources.
  private _initController() {
    if (!this._resolvedWhipUrl) return;

    const initKey = [
      this.initialProfile,
      String(this.debug),
      this.compositorWorkerUrl,
      this.encoderWorkerUrl,
      this.rtcTransformWorkerUrl,
    ].join("|");
    const running = this.pc.getController();
    if (this._lastInitKey === initKey && running) {
      running
        .setWhipUrl(this._resolvedWhipUrl)
        .catch((error) => console.warn("Could not change the WHIP endpoint:", error));
      return;
    }

    this.pc.initialize({
      whipUrl: this._resolvedWhipUrl,
      resolveWhipUrl: this.resolveWhipUrl
        ? (signal) => this._callResolveWhipUrl(signal)
        : undefined,
      profile: this.initialProfile,
      debug: this.debug,
      reconnection: { enabled: true, maxAttempts: 5 },
      audioMixing: true,
      workers: this._resolveWorkerConfig(),
    });
    this.pc.setEncoderOverrides(this._encoderOverrides);
    this._syncCompositor();

    this._lastInitKey = initKey;

    if (this.autoStartCamera && this._support.camera && this.pc.s.state === "idle") {
      this.pc.startCamera().catch(console.error);
    }
  }

  private _syncCompositor() {
    const controller = this.pc.getController();
    if (!controller) return;

    if (!this.enableCompositor) {
      // A compositor still starting is turned off once it has started.
      if (controller.getSceneManager() && !this._compositorEnabling) {
        controller.disableCompositor();
        this.requestUpdate();
      }
      return;
    }

    if (this._compositorEnabling) {
      return;
    }

    const sceneManager = controller.getSceneManager();
    if (sceneManager) {
      const renderer = this.compositorConfig?.renderer;
      if (renderer && sceneManager.getRendererType() !== renderer) {
        sceneManager.setRenderer(renderer);
      }
      return;
    }

    const config =
      this.compositorConfig && Object.keys(this.compositorConfig).length > 0
        ? this.compositorConfig
        : undefined;

    this._compositorFailed = false;
    this._compositorEnabling = this._layoutKeeper
      .start(controller, () => controller.enableCompositor(config))
      .then(() => {
        this.requestUpdate();
      })
      .catch((error) => {
        console.error("Failed to enable compositor:", error);
        this._compositorFailed = true;
      })
      .finally(() => {
        this._compositorEnabling = null;
        // Catch up with what changed while the compositor was starting: it
        // was turned off, or a new controller was built. A start that failed
        // on this controller is not tried again.
        if (!this.enableCompositor || this.pc.getController() !== controller) {
          this._syncCompositor();
        }
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
    const profile = QUALITY_PROFILES.find((p) => p.id === s.qualityProfile);
    const info = formatStreamInfo({
      state: s.state,
      quality: profile
        ? `${this._t(profile.labelKey)} (${this._t(profile.descKey)})`
        : s.qualityProfile,
      sourceCount: s.sources.length,
      whipUrl: this._resolvedWhipUrl,
    });
    navigator.clipboard.writeText(info).catch(console.error);
    this._contextMenu = null;
  }

  async startCamera(options?: Parameters<IngestControllerHost["startCamera"]>[0]) {
    return this.pc.startCamera(options);
  }
  async startScreenShare(options?: Parameters<IngestControllerHost["startScreenShare"]>[0]) {
    return this.pc.startScreenShare(options);
  }
  async addMediaFile(file: File, options?: { loop?: boolean }) {
    return this.pc.addMediaFile(file, options);
  }
  playMediaSource(id: string) {
    this.pc.playMediaSource(id);
  }
  pauseMediaSource(id: string) {
    this.pc.pauseMediaSource(id);
  }
  setMediaSourceLoop(id: string, loop: boolean) {
    this.pc.setMediaSourceLoop(id, loop);
  }
  async getCameraChoices() {
    return this.pc.getCameraChoices();
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
  async switchVideoDevice(deviceId: string, sourceId?: string) {
    return this.pc.switchVideoDevice(deviceId, sourceId);
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
    const canAddSource = s.state !== "destroyed";
    const support = this._support;
    const canStream = s.isCapturing && !s.isStreaming && !!this._resolvedWhipUrl && support.publish;
    const browserNotices = [
      !support.camera && this._t("browserCannotCapture"),
      !support.publish && this._t("browserCannotPublish"),
      this.enableCompositor && this._compositorFailed && this._t("compositorUnavailable"),
    ].filter((notice): notice is string => !!notice);
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
        tabindex="-1"
        @contextmenu=${(event: MouseEvent) => this._handleContextMenu(event)}
        @keydown=${(event: KeyboardEvent) => handleStudioSceneHotkey(event, this.sceneManager)}
      >
        <div class="main fw-sc-main">
          <div class="fw-sc-header">
            <span class="fw-sc-header-title">${this._t("streamCrafter")}</span>
            <div class="fw-sc-header-status">
              <fw-sc-connection-health .studio=${this}></fw-sc-connection-health>
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

          ${s.isCapturing ? html`<fw-sc-meter .studio=${this}></fw-sc-meter>` : nothing}
          ${this.showProducerPanel && activeScene
            ? html`<fw-sc-producer-panel .studio=${this}></fw-sc-producer-panel>`
            : nothing}
          ${s.isCapturing
            ? html`<fw-sc-audio-controls
                .studio=${this}
                .browserProcessing=${this._audioProcessing}
                @fw-audio-processing-change=${(
                  event: CustomEvent<{ settings: Partial<AudioProcessingSettings> }>
                ) => this._handleAudioProcessingChange(event)}
              ></fw-sc-audio-controls>`
            : nothing}
          ${browserNotices.map(
            (notice) => html`
              <div class="fw-sc-error fw-sc-error--warning" role="alert">
                <div class="fw-sc-error-title">${this._t("warning")}</div>
                <div class="fw-sc-error-message">${notice}</div>
              </div>
            `
          )}
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
                <div class="fw-sc-error fw-sc-error--warning">
                  <div class="fw-sc-error-title">${this._t("warning")}</div>
                  <div class="fw-sc-error-message">${this._t("configureWhipEndpoint")}</div>
                </div>
              `
            : nothing}
          ${isResolvingEndpoint
            ? html`
                <div class="fw-sc-error fw-sc-error--info">
                  <div class="fw-sc-error-title">${this._t("connecting")}</div>
                  <div class="fw-sc-error-message">${this._t("resolvingEndpoint")}</div>
                </div>
              `
            : nothing}

          <div class="fw-sc-actions">
            <fw-sc-camera-picker .studio=${this}></fw-sc-camera-picker>
            <button
              type="button"
              class="fw-sc-action-secondary"
              @click=${() => this.pc.startScreenShare({ audio: true }).catch(console.error)}
              ?disabled=${!canAddSource || !support.screen}
              title=${support.screen ? this._t("shareScreen") : this._t("screenShareUnsupported")}
            >
              ${monitorIcon(18)}
            </button>
            <fw-sc-media-button .studio=${this}></fw-sc-media-button>

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
            ${!s.isStreaming && !s.isReconnecting
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
                style="top:${this._contextMenu.y}px;left:${this._contextMenu.x}px"
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
                .encoderOverrides=${this._encoderOverrides}
                .compositorEnabled=${this.enableCompositor}
                .compositorRendererType=${this._getCompositorRendererType()}
                .compositorStats=${this._getCompositorStats()}
                .sceneCount=${this._getSceneCount()}
                .layerCount=${this._getLayerCount()}
                .t=${this._t}
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

  private _toggleSourceLayerVisibility(sourceId: string) {
    const sceneManager = this.pc.getController()?.getSceneManager();
    const scene = sceneManager?.getActiveScene();
    if (!sceneManager || !scene) return;

    const layer = scene.layers.find((candidate) => candidate.sourceId === sourceId);
    if (layer) {
      sceneManager.setLayerVisibility(scene.id, layer.id, !layer.visible);
    } else {
      sceneManager.addLayer(scene.id, sourceId);
    }
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
    const isVisible = isSourceVisibleInScene(source, this._getActiveScene());
    return html`
      <div class=${classMap({ "fw-sc-source": true, "fw-sc-source--hidden": !isVisible })}>
        ${this.enableCompositor && hasVideo
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
          ${source.type === "camera"
            ? cameraIcon(16)
            : source.type === "screen"
              ? monitorIcon(16)
              : videoIcon(16)}
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

  private _sendableCodecs = sendableVideoCodecs();

  /** Resolution, frame rate, bitrates and video codec; they apply when streaming starts. */
  private _renderEncoderSection() {
    const s = this.pc.s;
    const profile = s.qualityProfile;
    const overrides = this._encoderOverrides;
    const rows = encoderSettingRows(profile, overrides, this._sendableCodecs, this._t("codecAuto"));
    const hasOverrides = rows.some((row) => row.overridden);
    return html`
      <div class="fw-sc-dropdown-section">
        <div class="fw-sc-encoder-header">
          <div class="fw-sc-dropdown-label">${this._t("encoder")}</div>
          ${hasOverrides && !s.isStreaming
            ? html`<button
                type="button"
                class="fw-sc-encoder-reset"
                @click=${() => this.setEncoderOverrides({})}
              >
                ${this._t("resetToProfile")}
              </button>`
            : nothing}
        </div>
        ${rows.map(
          (row) => html`
            <label class="fw-sc-encoder-row">
              <span>${this._t(row.labelKey)}</span>
              <select
                class=${classMap({
                  "fw-sc-select": true,
                  "fw-sc-select--overridden": row.overridden,
                })}
                .value=${row.value}
                ?disabled=${s.isStreaming}
                @change=${(e: Event) =>
                  this.setEncoderOverrides(
                    withEncoderSetting(
                      profile,
                      overrides,
                      row.setting,
                      (e.target as HTMLSelectElement).value
                    )
                  )}
              >
                ${row.options.map(
                  (option) =>
                    html`<option value=${option.value} ?selected=${option.value === row.value}>
                      ${option.label}
                    </option>`
                )}
              </select>
            </label>
          `
        )}
        ${s.isStreaming
          ? html`<div class="fw-sc-encoder-note">${this._t("settingsLockedWhileStreaming")}</div>`
          : nothing}
      </div>
    `;
  }

  private _renderSettingsPopup() {
    const s = this.pc.s;
    return html`
      <div class="fw-sc-settings-popup">
        <div class="fw-sc-dropdown-section">
          <div class="fw-sc-dropdown-label">${this._t("quality")}</div>
          <div class="fw-sc-dropdown-options">
            ${QUALITY_PROFILES.map(
              (profile) => html`
                <button
                  type="button"
                  class=${classMap({
                    "fw-sc-dropdown-option": true,
                    "fw-sc-dropdown-option--active": s.qualityProfile === profile.id,
                  })}
                  @click=${() => {
                    if (!s.isStreaming) {
                      this.pc.setQualityProfile(profile.id).catch(console.error);
                      if (!this.devMode) this._showSettings = false;
                    }
                  }}
                  ?disabled=${s.isStreaming}
                >
                  <span class="fw-sc-dropdown-option-label">${this._t(profile.labelKey)}</span>
                  <span class="fw-sc-dropdown-option-desc">${this._t(profile.descKey)}</span>
                </button>
              `
            )}
          </div>
        </div>
        ${this._renderEncoderSection()}
        ${this.devMode
          ? html`
              <div class="fw-sc-dropdown-section">
                <div class="fw-sc-dropdown-label">${this._t("debug")}</div>
                <div class="fw-sc-dropdown-info">
                  <div class="fw-sc-dropdown-info-row">
                    <span>${this._t("state")}</span>
                    <span class="fw-sc-dropdown-info-value">${s.state}</span>
                  </div>
                  <div class="fw-sc-dropdown-info-row">
                    <span>${this._t("audio")}</span>
                    <span
                      class="fw-sc-dropdown-info-value ${s.isCapturing
                        ? "fw-sc-dropdown-info-value--ok"
                        : "fw-sc-dropdown-info-value--off"}"
                    >
                      ${s.isCapturing ? this._t("active") : this._t("inactive")}
                    </span>
                  </div>
                  <div class="fw-sc-dropdown-info-row">
                    <span>${this._t("whip")}</span>
                    <span
                      class="fw-sc-dropdown-info-value ${this._resolvedWhipUrl
                        ? "fw-sc-dropdown-info-value--ok"
                        : "fw-sc-dropdown-info-value--bad"}"
                    >
                      ${this._resolvedWhipUrl ? this._t("ok") : this._t("notSet")}
                    </span>
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
