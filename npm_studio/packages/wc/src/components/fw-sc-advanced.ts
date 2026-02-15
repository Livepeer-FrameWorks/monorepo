/**
 * <fw-sc-advanced> — Advanced/dev mode side panel.
 * Full port of AdvancedPanel.tsx from streamcrafter-react.
 * Tabs: Audio, Stats, Info, Compositor (when enabled).
 */
import { LitElement, html, css, nothing } from "lit";
import { customElement, property, state } from "lit/decorators.js";
import { classMap } from "lit/directives/class-map.js";
import { sharedStyles } from "../styles/shared-styles.js";
import { utilityStyles } from "../styles/utility-styles.js";
import { xIcon } from "../icons/index.js";
import type { IngestControllerHost } from "../controllers/ingest-controller-host.js";
import type { RendererType, RendererStats } from "@livepeer-frameworks/streamcrafter-core";

type TabId = "audio" | "stats" | "info" | "compositor";

function formatBitrate(bps: number): string {
  if (bps >= 1_000_000) return `${(bps / 1_000_000).toFixed(1)} Mbps`;
  return `${(bps / 1000).toFixed(0)} kbps`;
}

@customElement("fw-sc-advanced")
export class FwScAdvanced extends LitElement {
  @property({ attribute: false }) ic!: IngestControllerHost;
  @property({ type: Boolean, attribute: "compositor-enabled" }) compositorEnabled = false;
  @property({ type: String, attribute: "compositor-renderer" })
  compositorRendererType: RendererType | null = null;
  @property({ attribute: false }) compositorStats: RendererStats | null = null;
  @property({ type: Number, attribute: "scene-count" }) sceneCount = 0;
  @property({ type: Number, attribute: "layer-count" }) layerCount = 0;

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
        border-left: 1px solid rgba(65, 72, 104, 0.5);
        background: #1a1b26;
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
        color: #a9b1d6;
        flex-shrink: 0;
        z-index: 40;
      }
      .header {
        display: flex;
        align-items: center;
        border-bottom: 1px solid rgba(65, 72, 104, 0.3);
        background: #16161e;
      }
      .tab {
        padding: 8px 12px;
        font-size: 10px;
        text-transform: uppercase;
        letter-spacing: 0.05em;
        font-weight: 600;
        border: none;
        background: transparent;
        color: #565f89;
        cursor: pointer;
        transition: all 0.15s;
      }
      .tab--active {
        background: #1a1b26;
        color: #c0caf5;
      }
      .close {
        display: flex;
        background: transparent;
        border: none;
        color: #565f89;
        cursor: pointer;
        padding: 8px;
        transition: color 0.15s;
      }
      .close:hover {
        color: #c0caf5;
      }
      .body {
        flex: 1;
        overflow-y: auto;
      }
      .section-header {
        font-size: 10px;
        color: #565f89;
        text-transform: uppercase;
        letter-spacing: 0.05em;
        font-weight: 600;
        margin-bottom: 8px;
      }
      .section {
        padding: 12px;
        border-bottom: 1px solid rgba(65, 72, 104, 0.3);
      }
      .section-dark {
        padding: 8px 12px;
        background: #16161e;
        display: flex;
        justify-content: space-between;
        align-items: center;
      }
      .row {
        display: flex;
        justify-content: space-between;
        padding: 8px 12px;
        border-top: 1px solid rgba(65, 72, 104, 0.2);
      }
      .row-label {
        color: #565f89;
      }
      .row-value {
        color: #c0caf5;
        font-family: ui-monospace, monospace;
        font-variant-numeric: tabular-nums;
      }
      .level-bar {
        height: 8px;
        background: rgba(65, 72, 104, 0.3);
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
        color: #565f89;
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
        background: white;
        transition: left 0.2s;
      }
      .toggle--on {
        background: #7aa2f7;
      }
      .toggle--off {
        background: rgba(65, 72, 104, 0.5);
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
        padding: 8px 12px;
        border-top: 1px solid rgba(65, 72, 104, 0.2);
      }
      .processing-label {
        font-size: 12px;
        color: #a9b1d6;
      }
      .source-type {
        font-size: 10px;
        font-family: monospace;
        padding: 2px 6px;
        text-transform: uppercase;
      }
    `,
  ];

  protected render() {
    return html`
      <div class="panel">
        <div class="header">
          <button
            class=${classMap({ tab: true, "tab--active": this._activeTab === "audio" })}
            @click=${() => {
              this._activeTab = "audio";
            }}
          >
            Audio
          </button>
          <button
            class=${classMap({ tab: true, "tab--active": this._activeTab === "stats" })}
            @click=${() => {
              this._activeTab = "stats";
            }}
          >
            Stats
          </button>
          <button
            class=${classMap({ tab: true, "tab--active": this._activeTab === "info" })}
            @click=${() => {
              this._activeTab = "info";
            }}
          >
            Info
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
                  Comp
                </button>
              `
            : nothing}
          <div style="flex:1"></div>
          <button
            class="close"
            @click=${() =>
              this.dispatchEvent(new CustomEvent("fw-close", { bubbles: true, composed: true }))}
            aria-label="Close advanced panel"
          >
            ${xIcon(12)}
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

  // ---- Audio Tab ----

  private _renderAudio() {
    const s = this.ic.s;
    const masterVolume = this.ic.getMasterVolume();
    const audioLevel = s.audioLevel;
    const levelColor = audioLevel > 0.9 ? "#f7768e" : audioLevel > 0.7 ? "#e0af68" : "#9ece6a";
    const volColor = masterVolume > 1 ? "#e0af68" : masterVolume === 1 ? "#9ece6a" : "#c0caf5";

    return html`
      <!-- Master Volume -->
      <div class="section">
        <div class="section-header">Master Volume</div>
        <div style="display:flex;align-items:center;gap:12px">
          <fw-sc-volume
            .value=${masterVolume}
            .min=${0}
            .max=${2}
            @fw-sc-volume-change=${(e: CustomEvent) => this.ic.setMasterVolume(e.detail.value)}
          ></fw-sc-volume>
          <span style="font-size:14px;min-width:48px;text-align:right;color:${volColor}">
            ${Math.round(masterVolume * 100)}%
          </span>
        </div>
        ${masterVolume > 1
          ? html`<div style="font-size:10px;color:#e0af68;margin-top:4px">
              +${((masterVolume - 1) * 100).toFixed(0)}% boost
            </div>`
          : nothing}
      </div>

      <!-- Audio Level -->
      <div class="section">
        <div class="section-header">Output Level</div>
        <div class="level-bar">
          <div class="level-fill" style="width:${audioLevel * 100}%;background:${levelColor}"></div>
        </div>
        <div class="level-labels"><span>-60dB</span><span>0dB</span></div>
      </div>

      <!-- Audio Mixing Status -->
      <div class="section">
        <div style="display:flex;justify-content:space-between;align-items:center">
          <span class="section-header" style="margin-bottom:0">Audio Mixing</span>
          <span class="badge" style="background:rgba(158,206,106,0.2);color:#9ece6a"> ON </span>
        </div>
        <div style="font-size:10px;color:#565f89;margin-top:4px">Compressor + Limiter active</div>
      </div>

      <!-- Audio Processing -->
      <div style="border-bottom:1px solid rgba(65,72,104,0.3)">
        <div class="section-dark">
          <span class="section-header" style="margin-bottom:0">Processing</span>
          <span style="font-size:9px;color:#565f89"> profile: ${s.qualityProfile} </span>
        </div>
        ${this._renderToggle("Echo Cancellation", true)}
        ${this._renderToggle("Noise Suppression", true)}
        ${this._renderToggle("Auto Gain Control", true)}
      </div>
    `;
  }

  private _renderToggle(label: string, checked: boolean) {
    return html`
      <div class="processing-row">
        <span class="processing-label">${label}</span>
        <button
          type="button"
          role="switch"
          aria-checked=${checked}
          class="toggle ${checked ? "toggle--on" : "toggle--off"}"
        >
          <div class="toggle-knob"></div>
        </button>
      </div>
    `;
  }

  // ---- Stats Tab ----

  private _renderStats() {
    const s = this.ic.s;
    const stats = s.stats;
    const stateColor =
      s.state === "streaming"
        ? "#9ece6a"
        : s.state === "connecting"
          ? "#7aa2f7"
          : s.state === "error"
            ? "#f7768e"
            : "#c0caf5";

    return html`
      <div class="section">
        <div class="section-header" style="margin-bottom:4px">Connection</div>
        <div style="font-size:14px;font-weight:600;color:${stateColor}">
          ${s.state.charAt(0).toUpperCase() + s.state.slice(1)}
        </div>
      </div>
      ${stats
        ? html`
            <div class="row">
              <span class="row-label">Bitrate</span>
              <span class="row-value">
                ${formatBitrate(stats.video.bitrate + stats.audio.bitrate)}
              </span>
            </div>
            <div class="row">
              <span class="row-label">Video</span>
              <span class="row-value" style="color:#7aa2f7">
                ${formatBitrate(stats.video.bitrate)}
              </span>
            </div>
            <div class="row">
              <span class="row-label">Audio</span>
              <span class="row-value" style="color:#7aa2f7">
                ${formatBitrate(stats.audio.bitrate)}
              </span>
            </div>
            <div class="row">
              <span class="row-label">Frame Rate</span>
              <span class="row-value"> ${stats.video.framesPerSecond.toFixed(0)} fps </span>
            </div>
            <div class="row">
              <span class="row-label">Frames Encoded</span>
              <span class="row-value">${stats.video.framesEncoded}</span>
            </div>
            ${stats.video.packetsLost > 0 || stats.audio.packetsLost > 0
              ? html`
                  <div class="row">
                    <span class="row-label">Packets Lost</span>
                    <span class="row-value" style="color:#f7768e">
                      ${stats.video.packetsLost + stats.audio.packetsLost}
                    </span>
                  </div>
                `
              : nothing}
            <div class="row">
              <span class="row-label">RTT</span>
              <span
                class="row-value"
                style="color:${stats.connection.rtt > 200 ? "#e0af68" : "#c0caf5"}"
              >
                ${stats.connection.rtt.toFixed(0)} ms
              </span>
            </div>
            <div class="row">
              <span class="row-label">ICE State</span>
              <span class="row-value" style="text-transform:capitalize">
                ${stats.connection.iceState}
              </span>
            </div>
          `
        : html`
            <div style="color:#565f89;text-align:center;padding:24px">
              ${s.state === "streaming" ? "Waiting for stats..." : "Start streaming to see stats"}
            </div>
          `}
      ${s.error
        ? html`
            <div
              style="padding:12px;border-top:1px solid rgba(247,118,142,0.3);background:rgba(247,118,142,0.1)"
            >
              <div class="section-header" style="color:#f7768e;margin-bottom:4px">Error</div>
              <div style="font-size:12px;color:#f7768e">${s.error}</div>
            </div>
          `
        : nothing}
      ${s.encoderStats
        ? html`
            <div style="border-top:1px solid rgba(65,72,104,0.3)">
              <div class="section-dark">
                <span class="section-header" style="margin-bottom:0">Encoder</span>
              </div>
              <div class="row">
                <span class="row-label">Video frames</span>
                <span class="row-value">${s.encoderStats.video.framesEncoded}</span>
              </div>
              <div class="row">
                <span class="row-label">Video pending</span>
                <span
                  class="row-value"
                  style="color:${s.encoderStats.video.framesPending > 5 ? "#e0af68" : "#c0caf5"}"
                >
                  ${s.encoderStats.video.framesPending}
                </span>
              </div>
              <div class="row">
                <span class="row-label">Audio samples</span>
                <span class="row-value">${s.encoderStats.audio.samplesEncoded}</span>
              </div>
            </div>
          `
        : nothing}
    `;
  }

  // ---- Info Tab ----

  private _renderInfo() {
    const s = this.ic.s;
    return html`
      <div class="section">
        <div class="section-header" style="margin-bottom:4px">Quality Profile</div>
        <div style="font-size:14px;color:#c0caf5;text-transform:capitalize">
          ${s.qualityProfile}
        </div>
      </div>

      <div class="section">
        <div class="section-header" style="margin-bottom:4px">Configuration</div>
        ${this._simpleRow("WebCodecs", s.isWebCodecsAvailable ? "Available" : "Unavailable")}
        ${this._simpleRow(
          "WebCodecs Active",
          s.isWebCodecsActive ? "Yes" : s.useWebCodecs ? "Pending" : "No"
        )}
        ${this._simpleRow("Sources", String(s.sources.length))}
      </div>

      ${s.sources.length > 0
        ? html`
            <div style="border-bottom:1px solid rgba(65,72,104,0.3)">
              <div class="section-dark">
                <span class="section-header" style="margin-bottom:0">
                  Sources (${s.sources.length})
                </span>
              </div>
              ${s.sources.map(
                (source, idx) => html`
                  <div
                    style="padding:8px 12px;${idx > 0
                      ? "border-top:1px solid rgba(65,72,104,0.2)"
                      : ""}"
                  >
                    <div style="display:flex;align-items:center;gap:8px">
                      <span
                        class="source-type"
                        style="background:${source.type === "camera"
                          ? "rgba(122,162,247,0.2)"
                          : source.type === "screen"
                            ? "rgba(158,206,106,0.2)"
                            : "rgba(224,175,104,0.2)"};color:${source.type === "camera"
                          ? "#7aa2f7"
                          : source.type === "screen"
                            ? "#9ece6a"
                            : "#e0af68"}"
                      >
                        ${source.type}
                      </span>
                      <span
                        style="color:#c0caf5;font-size:12px;overflow:hidden;text-overflow:ellipsis;white-space:nowrap"
                      >
                        ${source.label}
                      </span>
                    </div>
                    <div style="display:flex;gap:12px;margin-top:4px;font-size:10px;color:#565f89">
                      <span>Vol: ${Math.round(source.volume * 100)}%</span>
                      ${source.muted ? html`<span style="color:#f7768e">Muted</span>` : nothing}
                    </div>
                  </div>
                `
              )}
            </div>
          `
        : nothing}
    `;
  }

  // ---- Compositor Tab ----

  private _renderCompositor() {
    const s = this.ic.s;
    const rt = this.compositorRendererType;
    const stats = this.compositorStats;
    const rendererColor = rt === "webgpu" ? "#bb9af7" : rt === "webgl" ? "#7aa2f7" : "#9ece6a";
    const rendererLabel =
      rt === "webgpu"
        ? "WebGPU"
        : rt === "webgl"
          ? "WebGL"
          : rt === "canvas2d"
            ? "Canvas2D"
            : "Not initialized";

    return html`
      <div class="section">
        <div class="section-header">Renderer</div>
        <div style="font-size:14px;font-weight:600;color:${rendererColor}">${rendererLabel}</div>
      </div>

      ${stats
        ? html`
            <div style="border-bottom:1px solid rgba(65,72,104,0.3)">
              <div class="section-dark">
                <span class="section-header" style="margin-bottom:0">Performance</span>
              </div>
              <div class="row">
                <span class="row-label">Frame Rate</span>
                <span class="row-value">${stats.fps} fps</span>
              </div>
              <div class="row">
                <span class="row-label">Frame Time</span>
                <span
                  class="row-value"
                  style="color:${stats.frameTimeMs > 16 ? "#e0af68" : "#c0caf5"}"
                >
                  ${stats.frameTimeMs.toFixed(2)} ms
                </span>
              </div>
              ${stats.gpuMemoryMB !== undefined
                ? html`
                    <div class="row">
                      <span class="row-label">GPU Memory</span>
                      <span class="row-value"> ${stats.gpuMemoryMB.toFixed(1)} MB </span>
                    </div>
                  `
                : nothing}
            </div>
          `
        : nothing}

      <div style="border-bottom:1px solid rgba(65,72,104,0.3)">
        <div class="section-dark">
          <span class="section-header" style="margin-bottom:0">Composition</span>
        </div>
        <div class="row">
          <span class="row-label">Scenes</span>
          <span class="row-value">${this.sceneCount}</span>
        </div>
        <div class="row">
          <span class="row-label">Layers</span>
          <span class="row-value">${this.layerCount}</span>
        </div>
      </div>

      <!-- Encoder -->
      <div style="border-bottom:1px solid rgba(65,72,104,0.3)">
        <div class="section-dark">
          <span class="section-header" style="margin-bottom:0">Encoder</span>
        </div>
        <div class="row">
          <span class="row-label">Type</span>
          <span
            class="badge"
            style="background:${s.useWebCodecs && s.isWebCodecsAvailable
              ? "rgba(187,154,247,0.2)"
              : "rgba(122,162,247,0.2)"};color:${s.useWebCodecs && s.isWebCodecsAvailable
              ? "#bb9af7"
              : "#7aa2f7"}"
          >
            ${s.useWebCodecs && s.isWebCodecsAvailable ? "WebCodecs" : "Browser"}
            ${s.state === "streaming"
              ? html`<span style="opacity:0.7;margin-left:4px">
                  ${s.isWebCodecsActive ? "(active)" : "(pending)"}
                </span>`
              : nothing}
          </span>
        </div>
        <div class="processing-row">
          <span class="processing-label">Use WebCodecs</span>
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
          ? html`<div style="padding:8px 12px;font-size:10px;color:#f7768e">
              Not available - RTCRtpScriptTransform unsupported
            </div>`
          : nothing}
      </div>
    `;
  }

  private _simpleRow(label: string, value: string) {
    return html`<div class="row">
      <span class="row-label">${label}</span><span class="row-value">${value}</span>
    </div>`;
  }
}

declare global {
  interface HTMLElementTagNameMap {
    "fw-sc-advanced": FwScAdvanced;
  }
}
