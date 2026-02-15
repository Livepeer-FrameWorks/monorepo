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
} from "../icons/index.js";
import { IngestControllerHost } from "../controllers/ingest-controller-host.js";
import type {
  QualityProfile,
  MediaSource,
  IngestState,
  ReconnectionState,
} from "@livepeer-frameworks/streamcrafter-core";

const QUALITY_PROFILES: { id: QualityProfile; label: string; description: string }[] = [
  { id: "professional", label: "Professional", description: "1080p @ 8 Mbps" },
  { id: "broadcast", label: "Broadcast", description: "1080p @ 4.5 Mbps" },
  { id: "conference", label: "Conference", description: "720p @ 2.5 Mbps" },
];

function getStatusText(state: IngestState, reconnectionState?: ReconnectionState | null): string {
  if (reconnectionState?.isReconnecting) {
    return `Reconnecting (${reconnectionState.attemptNumber}/5)...`;
  }
  switch (state) {
    case "idle":
      return "Idle";
    case "requesting_permissions":
      return "Permissions...";
    case "capturing":
      return "Ready";
    case "connecting":
      return "Connecting...";
    case "streaming":
      return "Live";
    case "reconnecting":
      return "Reconnecting...";
    case "error":
      return "Error";
    case "destroyed":
      return "Destroyed";
    default:
      return state;
  }
}

function getStatusBadgeClass(state: IngestState, isReconnecting: boolean): string {
  if (state === "streaming") return "fw-sc-badge fw-sc-badge--live";
  if (isReconnecting) return "fw-sc-badge fw-sc-badge--connecting";
  if (state === "error") return "fw-sc-badge fw-sc-badge--error";
  if (state === "capturing") return "fw-sc-badge fw-sc-badge--ready";
  return "fw-sc-badge fw-sc-badge--idle";
}

@customElement("fw-streamcrafter")
export class FwStreamCrafter extends LitElement {
  @property({ type: String, attribute: "whip-url" }) whipUrl = "";
  @property({ type: String, attribute: "gateway-url" }) gatewayUrl = "";
  @property({ type: String, attribute: "stream-key" }) streamKey = "";
  @property({ type: String, attribute: "initial-profile" }) initialProfile: QualityProfile =
    "broadcast";
  @property({ type: Boolean, attribute: "auto-start-camera" }) autoStartCamera = false;
  @property({ type: Boolean, attribute: "dev-mode" }) devMode = false;
  @property({ type: Boolean }) debug = false;
  @property({ type: Boolean, attribute: "enable-compositor" }) enableCompositor = false;

  @state() private _showSettings = false;
  @state() private _showSources = true;
  @state() private _isAdvancedPanelOpen = false;

  @query(".fw-sc-preview video") private _videoEl!: HTMLVideoElement | null;

  pc: IngestControllerHost;

  static styles = [
    sharedStyles,
    utilityStyles,
    css`
      :host {
        display: block;
      }
      .root {
        display: flex;
        height: 100%;
      }
      .main {
        display: flex;
        flex-direction: column;
        flex: 1;
        min-width: 0;
      }
    `,
  ];

  constructor() {
    super();
    this.pc = new IngestControllerHost(this, this.initialProfile);
  }

  connectedCallback() {
    super.connectedCallback();
    this._initController();
  }

  willUpdate(changed: Map<string, unknown>) {
    if (changed.has("whipUrl") || changed.has("initialProfile") || changed.has("debug")) {
      this._initController();
    }
  }

  updated(changed: Map<string, unknown>) {
    if (changed.has("_showSources") || changed.has("_showSettings")) {
      // no-op, reactive update handles UI
    }
    this._syncVideoPreview();
  }

  private _initController() {
    if (!this.whipUrl) return;
    this.pc.initialize({
      whipUrl: this.whipUrl,
      profile: this.initialProfile,
      debug: this.debug,
      reconnection: { enabled: true, maxAttempts: 5 },
      audioMixing: true,
    });

    if (this.autoStartCamera && this.pc.s.state === "idle") {
      this.pc.startCamera().catch(console.error);
    }
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

  // ---- Public API ----

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
  setSourceVolume(id: string, vol: number) {
    this.pc.setSourceVolume(id, vol);
  }
  setSourceMuted(id: string, m: boolean) {
    this.pc.setSourceMuted(id, m);
  }
  setPrimaryVideoSource(id: string) {
    this.pc.setPrimaryVideoSource(id);
  }
  setMasterVolume(vol: number) {
    this.pc.setMasterVolume(vol);
  }
  async setQualityProfile(p: QualityProfile) {
    return this.pc.setQualityProfile(p);
  }
  destroy() {
    this.pc.getController()?.destroy();
  }

  protected render() {
    const s = this.pc.s;
    const statusText = getStatusText(s.state, s.reconnectionState);
    const statusBadgeClass = getStatusBadgeClass(s.state, s.isReconnecting);
    const canAddSource = s.state !== "destroyed" && s.state !== "error";
    const canStream = s.isCapturing && !s.isStreaming && !!this.whipUrl;
    const hasCamera = s.sources.some((src: MediaSource) => src.type === "camera");

    return html`
      <div
        class=${classMap({ root: true, "fw-sc-root": true, "fw-sc-root--devmode": this.devMode })}
      >
        <div class="main fw-sc-main">
          <!-- Header -->
          <div class="fw-sc-header">
            <span class="fw-sc-header-title">StreamCrafter</span>
            <div class="fw-sc-header-status">
              <span class=${statusBadgeClass}>${statusText}</span>
            </div>
          </div>

          <!-- Content -->
          <div class="fw-sc-content">
            <div class="fw-sc-preview-wrapper">
              <div class="fw-sc-preview">
                <video playsinline muted autoplay aria-label="Stream preview"></video>

                ${!s.mediaStream
                  ? html`
                      <div class="fw-sc-preview-placeholder">
                        ${cameraIcon(48)}
                        <span>Add a camera or screen to preview</span>
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
                ${s.isStreaming ? html`<div class="fw-sc-live-badge">Live</div>` : nothing}
                ${this.enableCompositor
                  ? html` <fw-sc-compositor .ic=${this.pc}></fw-sc-compositor> `
                  : nothing}
              </div>
            </div>

            <!-- Sources Mixer -->
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
                      <span>Mixer (${s.sources.length})</span>
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

          <!-- Error -->
          ${s.error
            ? html`
                <div class="fw-sc-error">
                  <div class="fw-sc-error-title">Error</div>
                  <div class="fw-sc-error-message">${s.error}</div>
                </div>
              `
            : nothing}
          ${!this.whipUrl && !s.error
            ? html`
                <div class="fw-sc-error" style="border-left-color: hsl(40 80% 65%)">
                  <div class="fw-sc-error-title" style="color: hsl(40 80% 65%)">Warning</div>
                  <div class="fw-sc-error-message">Configure WHIP endpoint to stream</div>
                </div>
              `
            : nothing}

          <!-- Action Bar -->
          <div class="fw-sc-actions">
            <button
              type="button"
              class="fw-sc-action-secondary"
              @click=${() => this.pc.startCamera().catch(console.error)}
              ?disabled=${!canAddSource || hasCamera}
              title=${hasCamera ? "Camera active" : "Add Camera"}
            >
              ${cameraIcon(18)}
            </button>
            <button
              type="button"
              class="fw-sc-action-secondary"
              @click=${() => this.pc.startScreenShare({ audio: true }).catch(console.error)}
              ?disabled=${!canAddSource}
              title="Share Screen"
            >
              ${monitorIcon(18)}
            </button>

            <!-- Settings -->
            <div style="position:relative">
              <button
                type="button"
                class=${classMap({
                  "fw-sc-action-secondary": true,
                  "fw-sc-action-secondary--active": this._showSettings,
                })}
                @click=${() => {
                  this._showSettings = !this._showSettings;
                }}
                title="Settings"
              >
                ${settingsIcon(16)}
              </button>
              ${this._showSettings ? this._renderSettingsPopup() : nothing}
            </div>

            <!-- Go Live / Stop -->
            ${!s.isStreaming
              ? html`
                  <button
                    type="button"
                    class="fw-sc-action-primary"
                    @click=${() => this.pc.startStreaming().catch(console.error)}
                    ?disabled=${!canStream}
                  >
                    ${s.state === "connecting" ? "Connecting..." : "Go Live"}
                  </button>
                `
              : html`
                  <button
                    type="button"
                    class="fw-sc-action-primary fw-sc-action-stop"
                    @click=${() => this.pc.stopStreaming().catch(console.error)}
                  >
                    Stop Streaming
                  </button>
                `}
          </div>
        </div>

        <!-- Advanced Panel -->
        ${this.devMode && this._isAdvancedPanelOpen
          ? html`
              <fw-sc-advanced
                .ic=${this.pc}
                @fw-close=${() => {
                  this._isAdvancedPanelOpen = false;
                }}
              ></fw-sc-advanced>
            `
          : nothing}
      </div>
    `;
  }

  private _renderSourceRow(source: MediaSource) {
    const s = this.pc.s;
    const hasVideo = source.stream.getVideoTracks().length > 0;
    return html`
      <div class=${classMap({ "fw-sc-source": true })}>
        <div class="fw-sc-source-icon">
          ${source.type === "camera" ? cameraIcon(16) : monitorIcon(16)}
        </div>
        <div class="fw-sc-source-info">
          <div class="fw-sc-source-label">
            ${source.label}
            ${source.primaryVideo && !this.enableCompositor
              ? html`<span class="fw-sc-primary-badge">PRIMARY</span>`
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
                  title=${source.primaryVideo ? "Primary video source" : "Set as primary video"}
                >
                  ${videoIcon(14)}
                </button>
              `
            : nothing}
          <span class="fw-sc-volume-label">${Math.round(source.volume * 100)}%</span>
          <fw-sc-volume
            .value=${source.volume}
            @fw-sc-volume-change=${(e: CustomEvent) =>
              this.pc.setSourceVolume(source.id, e.detail.value)}
            compact
          ></fw-sc-volume>
          <button
            type="button"
            class=${classMap({ "fw-sc-icon-btn": true, "fw-sc-icon-btn--active": source.muted })}
            @click=${() => this.pc.setSourceMuted(source.id, !source.muted)}
            title=${source.muted ? "Unmute" : "Mute"}
          >
            ${source.muted ? micMutedIcon(14) : micIcon(14)}
          </button>
          <button
            type="button"
            class="fw-sc-icon-btn fw-sc-icon-btn--destructive"
            @click=${() => this.pc.removeSource(source.id)}
            ?disabled=${s.isStreaming}
            title=${s.isStreaming ? "Cannot remove source while streaming" : "Remove source"}
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
            Quality
          </div>
          <div style="display:flex;flex-direction:column;gap:2px">
            ${QUALITY_PROFILES.map(
              (p) => html`
                <button
                  type="button"
                  @click=${() => {
                    if (!s.isStreaming) {
                      this.pc.setQualityProfile(p.id);
                      if (!this.devMode) this._showSettings = false;
                    }
                  }}
                  ?disabled=${s.isStreaming}
                  style="width:100%;padding:6px 8px;text-align:left;font-size:12px;border-radius:4px;border:none;cursor:${s.isStreaming
                    ? "not-allowed"
                    : "pointer"};opacity:${s.isStreaming
                    ? "0.5"
                    : "1"};background:${s.qualityProfile === p.id
                    ? "rgba(122,162,247,0.2)"
                    : "transparent"};color:${s.qualityProfile === p.id ? "#7aa2f7" : "#a9b1d6"}"
                >
                  <div style="font-weight:500">${p.label}</div>
                  <div style="font-size:10px;color:#565f89">${p.description}</div>
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
                  Debug
                </div>
                <div
                  style="display:flex;flex-direction:column;gap:4px;padding-left:4px;font-size:12px;font-family:ui-monospace,monospace"
                >
                  <div style="display:flex;justify-content:space-between">
                    <span style="color:#565f89">State</span
                    ><span style="color:#c0caf5">${s.state}</span>
                  </div>
                  <div style="display:flex;justify-content:space-between">
                    <span style="color:#565f89">WHIP</span
                    ><span style="color:${this.whipUrl ? "#9ece6a" : "#f7768e"}"
                      >${this.whipUrl ? "OK" : "Not set"}</span
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
