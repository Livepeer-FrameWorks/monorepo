/**
 * <fw-stats-panel> — Stats for nerds overlay.
 * Port of StatsPanel.tsx from player-react.
 */
import { LitElement, html, css, nothing } from "lit";
import { customElement, property } from "lit/decorators.js";
import { sharedStyles } from "../styles/shared-styles.js";
import { utilityStyles } from "../styles/utility-styles.js";
import { closeIcon } from "../icons/index.js";
import type { PlayerControllerHost } from "../controllers/player-controller-host.js";

@customElement("fw-stats-panel")
export class FwStatsPanel extends LitElement {
  @property({ attribute: false }) pc!: PlayerControllerHost;

  static styles = [
    sharedStyles,
    utilityStyles,
    css`
      :host {
        display: contents;
      }
      .panel {
        position: absolute;
        top: 0.75rem;
        left: 0.75rem;
        z-index: 30;
        min-width: 240px;
        max-width: 320px;
        max-height: 80%;
        overflow: auto;
        border-radius: 0.5rem;
        border: 1px solid rgb(255 255 255 / 0.1);
        background: rgb(0 0 0 / 0.85);
        backdrop-filter: blur(8px);
        padding: 0.5rem 0.75rem;
        font-size: 0.6875rem;
        color: rgb(255 255 255 / 0.7);
      }
      .header {
        display: flex;
        align-items: center;
        justify-content: space-between;
        margin-bottom: 0.5rem;
      }
      .title {
        font-size: 0.75rem;
        font-weight: 600;
        color: white;
      }
      .close {
        display: flex;
        background: none;
        border: none;
        color: rgb(255 255 255 / 0.5);
        cursor: pointer;
        padding: 0;
      }
      .close:hover {
        color: white;
      }
      .row {
        display: flex;
        justify-content: space-between;
        padding: 0.125rem 0;
      }
      .label {
        color: rgb(255 255 255 / 0.5);
      }
      .value {
        color: rgb(255 255 255 / 0.9);
        font-variant-numeric: tabular-nums;
        font-family: ui-monospace, monospace;
      }
      .sep {
        height: 1px;
        background: rgb(255 255 255 / 0.08);
        margin: 0.375rem 0;
      }
    `,
  ];

  private _resolution(): string | null {
    const video = this.pc.s.videoElement;
    if (!video || !video.videoWidth || !video.videoHeight) return null;
    return `${video.videoWidth}x${video.videoHeight}`;
  }

  private _stat(label: string, value: string | number | null | undefined) {
    if (value == null || value === "") return nothing;
    return html`<div class="row">
      <span class="label">${label}</span><span class="value">${value}</span>
    </div>`;
  }

  protected render() {
    const s = this.pc.s;
    const q = s.playbackQuality;
    const meta = s.metadata;
    const ss = s.streamState;

    return html`
      <div class="panel fw-stats-panel">
        <div class="header">
          <span class="title">Stats</span>
          <button
            class="close"
            @click=${() =>
              this.dispatchEvent(new CustomEvent("fw-close", { bubbles: true, composed: true }))}
            aria-label="Close stats"
          >
            ${closeIcon()}
          </button>
        </div>

        ${this._stat("State", s.state)} ${this._stat("Player", s.currentPlayerInfo?.name)}
        ${this._stat("Source", s.currentSourceInfo?.type)}

        <div class="sep"></div>

        ${q
          ? html`
              ${this._stat("Resolution", this._resolution())}
              ${this._stat("Bitrate", q.bitrate ? `${Math.round(q.bitrate / 1000)} kbps` : null)}
              ${this._stat("Latency", q.latency != null ? `${q.latency.toFixed(1)}s` : null)}
              ${this._stat(
                "Buffer",
                q.bufferedAhead != null ? `${q.bufferedAhead.toFixed(1)}s` : null
              )}
              ${this._stat("Quality", q.score != null ? `${q.score.toFixed(0)}` : null)}
              ${this._stat(
                "Frame drops",
                q.frameDropRate != null ? `${q.frameDropRate.toFixed(1)}%` : null
              )}
              ${this._stat("Stalls", q.stallCount ?? null)}
            `
          : nothing}
        ${meta || ss
          ? html`
              <div class="sep"></div>
              ${this._stat("Viewers", meta?.viewers ?? null)}
              ${this._stat("Stream status", ss?.status ?? null)}
            `
          : nothing}
      </div>
    `;
  }
}

declare global {
  interface HTMLElementTagNameMap {
    "fw-stats-panel": FwStatsPanel;
  }
}
