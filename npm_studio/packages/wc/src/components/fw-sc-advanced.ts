/**
 * <fw-sc-advanced> — Advanced/dev mode side panel.
 * Simplified port of AdvancedPanel.tsx from streamcrafter-react.
 */
import { LitElement, html, css, nothing } from "lit";
import { customElement, property, state } from "lit/decorators.js";
import { classMap } from "lit/directives/class-map.js";
import { sharedStyles } from "../styles/shared-styles.js";
import { utilityStyles } from "../styles/utility-styles.js";
import { xIcon } from "../icons/index.js";
import type { IngestControllerHost } from "../controllers/ingest-controller-host.js";

type TabId = "stats" | "info";

@customElement("fw-sc-advanced")
export class FwScAdvanced extends LitElement {
  @property({ attribute: false }) ic!: IngestControllerHost;

  @state() private _activeTab: TabId = "stats";

  static styles = [
    sharedStyles,
    utilityStyles,
    css`
      :host {
        display: block;
      }
      .panel {
        width: 320px;
        height: 100%;
        border-left: 1px solid rgba(90, 96, 127, 0.3);
        background: #1a1b26;
        overflow: auto;
        font-size: 0.75rem;
        color: #a9b1d6;
      }
      .header {
        display: flex;
        align-items: center;
        justify-content: space-between;
        padding: 0.5rem 0.75rem;
        border-bottom: 1px solid rgba(90, 96, 127, 0.3);
      }
      .tabs {
        display: flex;
        gap: 0.5rem;
      }
      .tab {
        padding: 0.25rem 0.5rem;
        border: none;
        background: none;
        color: #565f89;
        font-size: 0.6875rem;
        font-weight: 600;
        cursor: pointer;
        border-radius: 0.25rem;
      }
      .tab--active {
        color: #c0caf5;
        background: rgba(90, 96, 127, 0.2);
      }
      .close {
        display: flex;
        background: none;
        border: none;
        color: #565f89;
        cursor: pointer;
        padding: 0;
      }
      .close:hover {
        color: #c0caf5;
      }
      .body {
        padding: 0.75rem;
      }
      .section {
        margin-bottom: 0.75rem;
      }
      .label {
        font-size: 0.625rem;
        font-weight: 600;
        text-transform: uppercase;
        letter-spacing: 0.05em;
        color: #565f89;
        margin-bottom: 0.375rem;
      }
      .row {
        display: flex;
        justify-content: space-between;
        padding: 0.125rem 0;
      }
      .row-label {
        color: #565f89;
      }
      .row-value {
        color: #c0caf5;
        font-family: ui-monospace, monospace;
        font-variant-numeric: tabular-nums;
      }
    `,
  ];

  protected render() {
    return html`
      <div class="panel">
        <div class="header">
          <div class="tabs">
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
          </div>
          <button
            class="close"
            @click=${() =>
              this.dispatchEvent(new CustomEvent("fw-close", { bubbles: true, composed: true }))}
            aria-label="Close panel"
          >
            ${xIcon(14)}
          </button>
        </div>
        <div class="body">
          ${this._activeTab === "stats" ? this._renderStats() : this._renderInfo()}
        </div>
      </div>
    `;
  }

  private _renderStats() {
    const s = this.ic.s;
    const stats = s.stats;
    return html`
      <div class="section">
        <div class="label">Connection</div>
        ${this._row("State", s.state)}
        ${this._row(
          "WebCodecs",
          s.isWebCodecsActive ? "Active" : s.useWebCodecs ? "Pending" : "Off"
        )}
      </div>
      ${stats
        ? html`
            <div class="section">
              <div class="label">WebRTC Stats</div>
              ${this._row(
                "Video bitrate",
                stats.video.bitrate ? `${Math.round(stats.video.bitrate / 1000)} kbps` : "—"
              )}
              ${this._row(
                "Audio bitrate",
                stats.audio.bitrate ? `${Math.round(stats.audio.bitrate / 1000)} kbps` : "—"
              )}
              ${this._row(
                "RTT",
                stats.connection.rtt ? `${(stats.connection.rtt * 1000).toFixed(0)} ms` : "—"
              )}
              ${this._row(
                "FPS",
                stats.video.framesPerSecond ? String(stats.video.framesPerSecond) : "—"
              )}
              ${this._row("Packets sent", String(stats.video.packetsSent))}
              ${this._row("Packets lost", String(stats.video.packetsLost))}
            </div>
          `
        : nothing}
      ${s.encoderStats
        ? html`
            <div class="section">
              <div class="label">Encoder</div>
              ${this._row("Video frames", String(s.encoderStats.video.framesEncoded))}
              ${this._row("Video pending", String(s.encoderStats.video.framesPending))}
              ${this._row("Audio samples", String(s.encoderStats.audio.samplesEncoded))}
            </div>
          `
        : nothing}
    `;
  }

  private _renderInfo() {
    const s = this.ic.s;
    return html`
      <div class="section">
        <div class="label">Configuration</div>
        ${this._row("Profile", s.qualityProfile)} ${this._row("Sources", String(s.sources.length))}
        ${this._row("WebCodecs Available", s.isWebCodecsAvailable ? "Yes" : "No")}
      </div>
      <div class="section">
        <div class="label">Sources</div>
        ${s.sources.map(
          (source) => html`
            ${this._row(source.label, `${source.type} ${source.muted ? "(muted)" : ""}`)}
          `
        )}
      </div>
    `;
  }

  private _row(label: string, value: string) {
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
