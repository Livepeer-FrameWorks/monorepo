/**
 * <fw-sc-advanced> — Advanced/dev mode side panel.
 * Port of AdvancedPanel.tsx from streamcrafter-react.
 */
import { LitElement, html, css, nothing } from "lit";
import { customElement, property, state } from "lit/decorators.js";
import { classMap } from "lit/directives/class-map.js";
import { sharedStyles } from "../styles/shared-styles.js";
import { utilityStyles } from "../styles/utility-styles.js";
import { closeIcon } from "../icons/index.js";
import type { IngestControllerHost } from "../controllers/ingest-controller-host.js";
import {
  encoderSummaryRows,
  getAudioConstraints,
  getEncoderSettings,
  type EncoderOverrides,
  type RendererType,
  type RendererStats,
  type StudioTranslateFn,
  createStudioTranslator,
  maskWhipUrl,
} from "@livepeer-frameworks/streamcrafter-core";

type TabId = "audio" | "stats" | "info" | "compositor";

function formatBitrate(bps: number): string {
  if (bps >= 1_000_000) return `${(bps / 1_000_000).toFixed(1)} Mbps`;
  return `${(bps / 1000).toFixed(0)} kbps`;
}

@customElement("fw-sc-advanced")
export class FwScAdvanced extends LitElement {
  @property({ attribute: false }) ic!: IngestControllerHost;
  @property({ type: String, attribute: "whip-url" }) whipUrl = "";
  @property({ attribute: false }) encoderOverrides: EncoderOverrides = {};
  @property({ type: Boolean, attribute: "compositor-enabled" }) compositorEnabled = false;
  @property({ type: String, attribute: "compositor-renderer" })
  compositorRendererType: RendererType | null = null;
  @property({ attribute: false }) compositorStats: RendererStats | null = null;
  @property({ type: Number, attribute: "scene-count" }) sceneCount = 0;
  @property({ type: Number, attribute: "layer-count" }) layerCount = 0;
  @property({ attribute: false }) t: StudioTranslateFn = createStudioTranslator({ locale: "en" });

  @state() private _activeTab: TabId = "audio";

  static styles = [
    sharedStyles,
    utilityStyles,
    css`
      :host {
        display: block;
      }
      .panel {
        width: 280px;
        height: 100%;
        border-left: 1px solid hsl(var(--fw-sc-border) / 0.5);
        background: hsl(var(--fw-sc-surface-deep));
        display: flex;
        flex-direction: column;
        font-size: 12px;
        font-family:
          ui-monospace,
          SFMono-Regular,
          SF Mono,
          Menlo,
          Consolas,
          monospace;
        color: hsl(var(--fw-sc-text-muted));
        flex-shrink: 0;
        z-index: 40;
      }
      .header {
        display: flex;
        align-items: center;
        border-bottom: 1px solid hsl(var(--fw-sc-border) / 0.3);
        background: hsl(var(--fw-sc-surface));
      }
      .tab {
        padding: 8px 12px;
        font-size: 10px;
        text-transform: uppercase;
        letter-spacing: 0.05em;
        font-weight: 600;
        border: none;
        background: transparent;
        color: hsl(var(--fw-sc-text-faint));
        cursor: pointer;
        transition: all 0.15s;
      }
      .tab--active {
        background: hsl(var(--fw-sc-surface-deep));
        color: hsl(var(--fw-sc-text));
      }
      .close {
        display: flex;
        background: transparent;
        border: none;
        color: hsl(var(--fw-sc-text-faint));
        cursor: pointer;
        padding: 8px;
        transition: color 0.15s;
      }
      .close:hover {
        color: hsl(var(--fw-sc-text));
      }
      .body {
        flex: 1;
        overflow-y: auto;
      }
      .section-header {
        font-size: 10px;
        color: hsl(var(--fw-sc-text-faint));
        text-transform: uppercase;
        letter-spacing: 0.05em;
        font-weight: 600;
        margin-bottom: 8px;
      }
      .section {
        padding: 12px;
        border-bottom: 1px solid hsl(var(--fw-sc-border) / 0.3);
      }
      .section-dark {
        padding: 8px 12px;
        background: hsl(var(--fw-sc-surface));
        display: flex;
        justify-content: space-between;
        align-items: center;
      }
      .row {
        display: flex;
        justify-content: space-between;
        align-items: center;
        padding: 8px 12px;
        border-top: 1px solid hsl(var(--fw-sc-border) / 0.2);
      }
      .row-label {
        color: hsl(var(--fw-sc-text-faint));
      }
      .row-value {
        color: hsl(var(--fw-sc-text));
        font-family: ui-monospace, monospace;
        font-variant-numeric: tabular-nums;
      }
      .level-bar {
        height: 8px;
        background: hsl(var(--fw-sc-border) / 0.3);
        border-radius: 4px;
        overflow: hidden;
      }
      .level-fill {
        height: 100%;
        transition: all 75ms;
      }
      .level-labels {
        display: flex;
        justify-content: space-between;
        font-size: 10px;
        color: hsl(var(--fw-sc-text-faint));
        margin-top: 4px;
      }
      .badge {
        font-size: 12px;
        font-family: monospace;
        padding: 2px 6px;
      }
      .toggle {
        position: relative;
        display: inline-flex;
        height: 20px;
        width: 36px;
        flex-shrink: 0;
        cursor: pointer;
        border-radius: 10px;
        border: 2px solid transparent;
        transition: background 0.2s;
        padding: 0;
      }
      .toggle:disabled {
        opacity: 0.5;
        cursor: not-allowed;
      }
      .toggle-knob {
        position: absolute;
        top: 2px;
        width: 12px;
        height: 12px;
        border-radius: 50%;
        /* Foreground tokens of the track under the knob, so it shows in light and dark themes. */
        background: hsl(var(--fw-sc-text-muted));
        transition: left 0.2s;
      }
      .toggle--on .toggle-knob {
        background: hsl(var(--fw-sc-on-accent));
      }
      .toggle--on {
        background: hsl(var(--fw-sc-accent));
      }
      .toggle--off {
        background: hsl(var(--fw-sc-border) / 0.5);
      }
      .toggle--on .toggle-knob {
        left: 18px;
      }
      .toggle--off .toggle-knob {
        left: 4px;
      }
      .processing-row {
        display: flex;
        justify-content: space-between;
        align-items: center;
        padding: 10px 12px;
        border-top: 1px solid hsl(var(--fw-sc-border) / 0.2);
      }
      .processing-label {
        font-size: 12px;
        color: hsl(var(--fw-sc-text));
      }
      .processing-desc {
        font-size: 10px;
        color: hsl(var(--fw-sc-text-faint));
        margin-top: 2px;
      }
      .source-type {
        font-size: 10px;
        font-family: monospace;
        padding: 2px 6px;
        text-transform: uppercase;
      }
      .row-value--overridden {
        color: hsl(var(--fw-sc-accent-secondary));
      }
      .overridden-tag {
        margin-left: 6px;
        font-size: 10px;
      }
      .encoder-pointer {
        padding: 8px 12px;
        font-size: 10px;
        color: hsl(var(--fw-sc-text-faint));
      }
      .mini-button {
        margin-top: 8px;
        font-size: 10px;
        color: hsl(var(--fw-sc-text-faint));
        background: transparent;
        border: none;
        cursor: pointer;
        padding: 0;
      }
      .info-copy {
        font-size: 10px;
        color: hsl(var(--fw-sc-text-faint));
        line-height: 1.5;
      }
    `,
  ];

  protected render() {
    if (!this.ic) return nothing;

    return html`
      <div class="panel">
        <div class="header">
          <button
            class=${classMap({ tab: true, "tab--active": this._activeTab === "audio" })}
            @click=${() => {
              this._activeTab = "audio";
            }}
          >
            ${this.t("audio")}
          </button>
          <button
            class=${classMap({ tab: true, "tab--active": this._activeTab === "stats" })}
            @click=${() => {
              this._activeTab = "stats";
            }}
          >
            ${this.t("stats")}
          </button>
          <button
            class=${classMap({ tab: true, "tab--active": this._activeTab === "info" })}
            @click=${() => {
              this._activeTab = "info";
            }}
          >
            ${this.t("info")}
          </button>
          ${this.compositorEnabled
            ? html`
                <button
                  class=${classMap({
                    tab: true,
                    "tab--active": this._activeTab === "compositor",
                  })}
                  @click=${() => {
                    this._activeTab = "compositor";
                  }}
                >
                  ${this.t("comp")}
                </button>
              `
            : nothing}
          <div style="flex:1"></div>
          <button
            class="close"
            @click=${() =>
              this.dispatchEvent(new CustomEvent("fw-close", { bubbles: true, composed: true }))}
            aria-label=${this.t("closeAdvancedPanel")}
          >
            ${closeIcon(12)}
          </button>
        </div>
        <div class="body">
          ${this._activeTab === "audio"
            ? this._renderAudio()
            : this._activeTab === "stats"
              ? this._renderStats()
              : this._activeTab === "info"
                ? this._renderInfo()
                : this._renderCompositor()}
        </div>
      </div>
    `;
  }

  private _renderAudio() {
    const s = this.ic.s;
    const masterVolume = this.ic.getMasterVolume();
    const audioLevel = s.audioLevel;
    const levelColor =
      audioLevel > 0.9
        ? "hsl(var(--fw-sc-danger))"
        : audioLevel > 0.7
          ? "hsl(var(--fw-sc-warning))"
          : "hsl(var(--fw-sc-success))";
    const volColor =
      masterVolume > 1
        ? "hsl(var(--fw-sc-warning))"
        : masterVolume === 1
          ? "hsl(var(--fw-sc-success))"
          : "hsl(var(--fw-sc-text))";

    const profileDefaults = getAudioConstraints(s.qualityProfile);
    return html`
      <div class="section">
        <div class="section-header">${this.t("masterVolume")}</div>
        <div style="display:flex;align-items:center;gap:12px">
          <fw-sc-volume
            .value=${masterVolume}
            .min=${0}
            .max=${2}
            @fw-sc-volume-change=${(e: CustomEvent<{ value: number }>) =>
              this.ic.setMasterVolume(e.detail.value)}
          ></fw-sc-volume>
          <span style="font-size:14px;min-width:48px;text-align:right;color:${volColor}">
            ${Math.round(masterVolume * 100)}%
          </span>
        </div>
        ${masterVolume > 1
          ? html`<div style="font-size:10px;color:hsl(var(--fw-sc-warning));margin-top:4px">
              +${((masterVolume - 1) * 100).toFixed(0)}% boost
            </div>`
          : nothing}
      </div>

      <div class="section">
        <div class="section-header">${this.t("outputLevel")}</div>
        <div class="level-bar">
          <div class="level-fill" style="width:${audioLevel * 100}%;background:${levelColor}"></div>
        </div>
        <div class="level-labels"><span>-60dB</span><span>0dB</span></div>
      </div>

      <div class="section">
        <div style="display:flex;justify-content:space-between;align-items:center">
          <span class="section-header" style="margin-bottom:0">${this.t("audioMixing")}</span>
          <span
            class="badge"
            style="background:hsl(var(--fw-sc-success) / 0.2);color:hsl(var(--fw-sc-success))"
          >
            ${this.t("on")}
          </span>
        </div>
        <div style="font-size:10px;color:hsl(var(--fw-sc-text-faint));margin-top:4px">
          ${this.t("compressorLimiterActive")}
        </div>
      </div>

      <div style="border-bottom:1px solid hsl(var(--fw-sc-border) / 0.3)">
        <div class="section-dark">
          <span class="section-header" style="margin-bottom:0">${this.t("processing")}</span>
          <span style="font-size:9px;color:hsl(var(--fw-sc-text-faint))">
            profile: ${s.qualityProfile}
          </span>
        </div>
        <div class="row">
          <span class="row-label">${this.t("sampleRate")}</span>
          <span class="row-value">${profileDefaults.sampleRate} Hz</span>
        </div>
        <div class="row">
          <span class="row-label">${this.t("channels")}</span>
          <span class="row-value">${profileDefaults.channelCount}</span>
        </div>
      </div>
    `;
  }

  private _renderStats() {
    const s = this.ic.s;
    const stats = s.stats;
    const stateColor =
      s.state === "streaming"
        ? "hsl(var(--fw-sc-success))"
        : s.state === "connecting"
          ? "hsl(var(--fw-sc-accent))"
          : s.state === "error"
            ? "hsl(var(--fw-sc-danger))"
            : "hsl(var(--fw-sc-text))";

    return html`
      <div class="section">
        <div class="section-header" style="margin-bottom:4px">${this.t("connection")}</div>
        <div style="font-size:14px;font-weight:600;color:${stateColor}">
          ${s.state.charAt(0).toUpperCase() + s.state.slice(1)}
        </div>
      </div>
      ${stats
        ? html`
            <div class="row">
              <span class="row-label">${this.t("bitrate")}</span>
              <span class="row-value">
                ${formatBitrate(stats.video.bitrate + stats.audio.bitrate)}
              </span>
            </div>
            <div class="row">
              <span class="row-label">${this.t("video")}</span>
              <span class="row-value" style="color:hsl(var(--fw-sc-accent))">
                ${formatBitrate(stats.video.bitrate)}
              </span>
            </div>
            <div class="row">
              <span class="row-label">${this.t("audio")}</span>
              <span class="row-value" style="color:hsl(var(--fw-sc-accent))">
                ${formatBitrate(stats.audio.bitrate)}
              </span>
            </div>
            <div class="row">
              <span class="row-label">${this.t("frameRate")}</span>
              <span class="row-value"> ${stats.video.framesPerSecond.toFixed(0)} fps </span>
            </div>
            <div class="row">
              <span class="row-label">${this.t("framesEncoded")}</span>
              <span class="row-value">${stats.video.framesEncoded}</span>
            </div>
            ${stats.video.packetsLost > 0 || stats.audio.packetsLost > 0
              ? html`
                  <div class="row">
                    <span class="row-label">${this.t("packetsLost")}</span>
                    <span class="row-value" style="color:hsl(var(--fw-sc-danger))">
                      ${stats.video.packetsLost + stats.audio.packetsLost}
                    </span>
                  </div>
                `
              : nothing}
            <div class="row">
              <span class="row-label">${this.t("rtt")}</span>
              <span
                class="row-value"
                style="color:${stats.connection.rtt > 200
                  ? "hsl(var(--fw-sc-warning))"
                  : "hsl(var(--fw-sc-text))"}"
              >
                ${stats.connection.rtt.toFixed(0)} ms
              </span>
            </div>
            <div class="row">
              <span class="row-label">${this.t("iceState")}</span>
              <span class="row-value" style="text-transform:capitalize">
                ${stats.connection.iceState}
              </span>
            </div>
          `
        : html`
            <div style="color:hsl(var(--fw-sc-text-faint));text-align:center;padding:24px">
              ${s.state === "streaming"
                ? this.t("waitingForStats")
                : this.t("startStreamingForStats")}
            </div>
          `}
      ${s.error
        ? html`
            <div
              style="padding:12px;border-top:1px solid hsl(var(--fw-sc-danger) / 0.3);background:hsl(var(--fw-sc-danger) / 0.1)"
            >
              <div class="section-header" style="color:hsl(var(--fw-sc-danger));margin-bottom:4px">
                ${this.t("error")}
              </div>
              <div style="font-size:12px;color:hsl(var(--fw-sc-danger))">${s.error}</div>
            </div>
          `
        : nothing}
    `;
  }

  private _renderInfo() {
    const s = this.ic.s;
    const profileEncoderSettings = getEncoderSettings(s.qualityProfile);
    const encoderRows = encoderSummaryRows(
      s.qualityProfile,
      this.encoderOverrides,
      this.t("codecAuto")
    );
    const videoTrackSettings = s.mediaStream?.getVideoTracks?.()[0]?.getSettings?.();

    return html`
      <div class="section">
        <div class="section-header" style="margin-bottom:4px">${this.t("qualityProfile")}</div>
        <div style="font-size:14px;color:hsl(var(--fw-sc-text));text-transform:capitalize">
          ${s.qualityProfile}
        </div>
        <div style="font-size:10px;color:hsl(var(--fw-sc-text-faint));margin-top:4px">
          ${profileEncoderSettings.video.width}x${profileEncoderSettings.video.height} @
          ${formatBitrate(profileEncoderSettings.video.bitrate)}
        </div>
      </div>

      <div class="section">
        <div class="section-header" style="margin-bottom:4px">${this.t("whipEndpoint")}</div>
        <div style="font-size:12px;color:hsl(var(--fw-sc-accent));word-break:break-all">
          ${maskWhipUrl(this.whipUrl) || this.t("notConfigured")}
        </div>
        ${this.whipUrl
          ? html`
              <button type="button" class="mini-button" @click=${() => this._copyWhipUrl()}>
                ${this.t("copyUrl")}
              </button>
            `
          : nothing}
      </div>

      <div style="border-bottom:1px solid hsl(var(--fw-sc-border) / 0.3)">
        <div class="section-dark">
          <span class="section-header" style="margin-bottom:0">${this.t("encoder")}</span>
        </div>
        ${encoderRows.map(
          (row) => html`
            <div class="row" ?data-overridden=${row.overridden}>
              <span class="row-label">${this.t(row.labelKey)}</span>
              <span
                class=${classMap({ "row-value": true, "row-value--overridden": row.overridden })}
                >${row.value}${row.overridden
                  ? html`<span class="overridden-tag">${this.t("encoderOverridden")}</span>`
                  : nothing}</span
              >
            </div>
          `
        )}
        ${videoTrackSettings?.width && videoTrackSettings?.height
          ? html`
              <div class="row">
                <span class="row-label">${this.t("actualResolution")}</span>
                <span class="row-value">
                  ${Math.round(videoTrackSettings.width)}x${Math.round(videoTrackSettings.height)}
                </span>
              </div>
            `
          : nothing}
        ${videoTrackSettings?.frameRate
          ? html`
              <div class="row">
                <span class="row-label">${this.t("actualFramerate")}</span>
                <span class="row-value">${Math.round(videoTrackSettings.frameRate)} fps</span>
              </div>
            `
          : nothing}
        <div class="encoder-pointer">${this.t("encoderEditInSettings")}</div>
      </div>

      <div style="border-bottom:1px solid hsl(var(--fw-sc-border) / 0.3)">
        <div class="section-dark">
          <span class="section-header" style="margin-bottom:0"
            >${this.t("sources")} (${s.sources.length})</span
          >
        </div>
        ${s.sources.length > 0
          ? s.sources.map(
              (source, index) => html`
                <div
                  style="padding:8px 12px;${index > 0
                    ? "border-top:1px solid hsl(var(--fw-sc-border) / 0.2)"
                    : ""}"
                >
                  <div style="display:flex;align-items:center;gap:8px">
                    <span
                      class="source-type"
                      style="background:${source.type === "camera"
                        ? "hsl(var(--fw-sc-accent) / 0.2)"
                        : source.type === "screen"
                          ? "hsl(var(--fw-sc-success) / 0.2)"
                          : "hsl(var(--fw-sc-warning) / 0.2)"};color:${source.type === "camera"
                        ? "hsl(var(--fw-sc-accent))"
                        : source.type === "screen"
                          ? "hsl(var(--fw-sc-success))"
                          : "hsl(var(--fw-sc-warning))"}"
                    >
                      ${source.type}
                    </span>
                    <span
                      style="color:hsl(var(--fw-sc-text));font-size:12px;overflow:hidden;text-overflow:ellipsis;white-space:nowrap"
                    >
                      ${source.label}
                    </span>
                  </div>
                  <div
                    style="display:flex;gap:12px;margin-top:4px;font-size:10px;color:hsl(var(--fw-sc-text-faint))"
                  >
                    <span>Vol: ${Math.round(source.volume * 100)}%</span>
                    ${source.muted
                      ? html`<span style="color:hsl(var(--fw-sc-danger))">${this.t("mute")}</span>`
                      : nothing}
                    ${!source.active
                      ? html`<span style="color:hsl(var(--fw-sc-warning))"
                          >${this.t("inactive")}</span
                        >`
                      : nothing}
                  </div>
                </div>
              `
            )
          : html`<div
              style="padding:16px 12px;color:hsl(var(--fw-sc-text-faint));text-align:center"
            >
              ${this.t("noSourcesAdded")}
            </div>`}
      </div>
    `;
  }

  private _renderCompositor() {
    const s = this.ic.s;
    const rt = this.compositorRendererType;
    const stats = this.compositorStats;
    const rendererColor =
      rt === "webgpu"
        ? "hsl(var(--fw-sc-accent-secondary))"
        : rt === "webgl"
          ? "hsl(var(--fw-sc-accent))"
          : "hsl(var(--fw-sc-success))";
    const rendererLabel =
      rt === "webgpu"
        ? "WebGPU"
        : rt === "webgl"
          ? "WebGL"
          : rt === "canvas2d"
            ? "Canvas2D"
            : this.t("notInitialized");

    return html`
      <div class="section">
        <div class="section-header">${this.t("renderer")}</div>
        <div style="font-size:14px;font-weight:600;color:${rendererColor}">${rendererLabel}</div>
        <div style="font-size:10px;color:hsl(var(--fw-sc-text-faint));margin-top:4px">
          ${this.t("setRendererHint")}
        </div>
      </div>

      ${stats
        ? html`
            <div style="border-bottom:1px solid hsl(var(--fw-sc-border) / 0.3)">
              <div class="section-dark">
                <span class="section-header" style="margin-bottom:0">${this.t("performance")}</span>
              </div>
              <div class="row">
                <span class="row-label">${this.t("frameRate")}</span>
                <span class="row-value">${stats.fps} fps</span>
              </div>
              <div class="row">
                <span class="row-label">${this.t("frameTime")}</span>
                <span
                  class="row-value"
                  style="color:${stats.frameTimeMs > 16
                    ? "hsl(var(--fw-sc-warning))"
                    : "hsl(var(--fw-sc-text))"}"
                >
                  ${stats.frameTimeMs.toFixed(2)} ms
                </span>
              </div>
              ${stats.gpuMemoryMB !== undefined
                ? html`
                    <div class="row">
                      <span class="row-label">${this.t("gpuMemory")}</span>
                      <span class="row-value"> ${stats.gpuMemoryMB.toFixed(1)} MB </span>
                    </div>
                  `
                : nothing}
            </div>
          `
        : nothing}

      <div style="border-bottom:1px solid hsl(var(--fw-sc-border) / 0.3)">
        <div class="section-dark">
          <span class="section-header" style="margin-bottom:0">${this.t("composition")}</span>
        </div>
        <div class="row">
          <span class="row-label">${this.t("scenes")}</span>
          <span class="row-value">${this.sceneCount}</span>
        </div>
        <div class="row">
          <span class="row-label">${this.t("layers")}</span>
          <span class="row-value">${this.layerCount}</span>
        </div>
      </div>

      <div style="border-bottom:1px solid hsl(var(--fw-sc-border) / 0.3)">
        <div class="section-dark">
          <span class="section-header" style="margin-bottom:0">${this.t("encoder")}</span>
        </div>
        <div class="row">
          <span class="row-label">${this.t("type")}</span>
          <span
            class="badge"
            style="background:${s.useWebCodecs && s.isWebCodecsAvailable
              ? "hsl(var(--fw-sc-accent-secondary) / 0.2)"
              : "hsl(var(--fw-sc-accent) / 0.2)"};color:${s.useWebCodecs && s.isWebCodecsAvailable
              ? "hsl(var(--fw-sc-accent-secondary))"
              : "hsl(var(--fw-sc-accent))"}"
          >
            ${s.useWebCodecs && s.isWebCodecsAvailable ? this.t("webCodecs") : this.t("browser")}
            ${s.state === "streaming"
              ? html`<span style="opacity:0.7;margin-left:4px">
                  ${s.isWebCodecsActive ? "(active)" : "(pending)"}
                </span>`
              : nothing}
          </span>
        </div>
        <div class="processing-row">
          <div style="display:flex;flex-direction:column;gap:2px;min-width:0;flex:1">
            <span class="processing-label">${this.t("useWebCodecs")}</span>
            <span class="processing-desc">${this.t("enableWebCodecsDesc")}</span>
          </div>
          <button
            type="button"
            role="switch"
            aria-checked=${s.useWebCodecs}
            class="toggle ${s.useWebCodecs ? "toggle--on" : "toggle--off"}"
            ?disabled=${s.state === "streaming" || !s.isWebCodecsAvailable}
            @click=${() => this.ic.setUseWebCodecs(!s.useWebCodecs)}
          >
            <div class="toggle-knob"></div>
          </button>
        </div>
        ${!s.isWebCodecsAvailable
          ? html`<div style="padding:8px 12px;font-size:10px;color:hsl(var(--fw-sc-danger))">
              ${this.t("webCodecsUnsupported")}
            </div>`
          : nothing}
        ${s.isWebCodecsAvailable &&
        s.state === "streaming" &&
        s.useWebCodecs !== s.isWebCodecsActive
          ? html`<div style="padding:8px 12px;font-size:10px;color:hsl(var(--fw-sc-warning))">
              ${this.t("changeTakesEffect")}
            </div>`
          : nothing}
      </div>

      ${s.isWebCodecsActive && s.encoderStats
        ? html`
            <div style="border-bottom:1px solid hsl(var(--fw-sc-border) / 0.3)">
              <div class="section-dark">
                <span class="section-header" style="margin-bottom:0"
                  >${this.t("encoderStats")}</span
                >
              </div>
              <div class="row">
                <span class="row-label">${this.t("videoFrames")}</span>
                <span class="row-value">${s.encoderStats.video.framesEncoded}</span>
              </div>
              <div class="row">
                <span class="row-label">${this.t("videoPending")}</span>
                <span
                  class="row-value"
                  style="color:${s.encoderStats.video.framesPending > 5
                    ? "hsl(var(--fw-sc-warning))"
                    : "hsl(var(--fw-sc-text))"}"
                >
                  ${s.encoderStats.video.framesPending}
                </span>
              </div>
              <div class="row">
                <span class="row-label">${this.t("videoBytes")}</span>
                <span class="row-value">
                  ${(s.encoderStats.video.bytesEncoded / 1024 / 1024).toFixed(2)} MB
                </span>
              </div>
              <div class="row">
                <span class="row-label">${this.t("audioSamples")}</span>
                <span class="row-value">${s.encoderStats.audio.samplesEncoded}</span>
              </div>
              <div class="row">
                <span class="row-label">${this.t("audioBytes")}</span>
                <span class="row-value">
                  ${(s.encoderStats.audio.bytesEncoded / 1024).toFixed(1)} KB
                </span>
              </div>
            </div>
          `
        : nothing}

      <div class="section">
        <div class="info-copy">
          ${s.useWebCodecs && s.isWebCodecsAvailable
            ? "WebCodecs encoder via RTCRtpScriptTransform provides lower latency and better encoding control."
            : "Browser's built-in MediaStream encoder. Enable WebCodecs toggle for advanced encoding."}
        </div>
      </div>
    `;
  }

  private _copyWhipUrl() {
    if (!this.whipUrl) return;
    navigator.clipboard.writeText(this.whipUrl).catch(console.error);
  }
}

declare global {
  interface HTMLElementTagNameMap {
    "fw-sc-advanced": FwScAdvanced;
  }
}
