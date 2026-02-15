/**
 * <fw-dev-mode-panel> — Developer mode panel for forcing player/source selection.
 * Port of DevModePanel.tsx from player-react.
 */
import { LitElement, html, css, nothing } from "lit";
import { customElement, property, state } from "lit/decorators.js";
import { classMap } from "lit/directives/class-map.js";
import { sharedStyles } from "../styles/shared-styles.js";
import { utilityStyles } from "../styles/utility-styles.js";
import { closeIcon } from "../icons/index.js";
import type { PlayerControllerHost } from "../controllers/player-controller-host.js";
import type { PlaybackMode } from "@livepeer-frameworks/player-core";

@customElement("fw-dev-mode-panel")
export class FwDevModePanel extends LitElement {
  @property({ attribute: false }) pc!: PlayerControllerHost;
  @property({ type: String }) playbackMode: PlaybackMode = "auto";

  @state() private _activeTab: "config" | "stats" = "config";

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
        border-left: 1px solid rgb(255 255 255 / 0.1);
        background: rgb(15 23 42);
        overflow: auto;
        font-size: 0.75rem;
        color: rgb(255 255 255 / 0.7);
      }
      .header {
        display: flex;
        align-items: center;
        justify-content: space-between;
        padding: 0.5rem 0.75rem;
        border-bottom: 1px solid rgb(255 255 255 / 0.1);
      }
      .tabs {
        display: flex;
        gap: 0.5rem;
      }
      .tab {
        padding: 0.25rem 0.5rem;
        border: none;
        background: none;
        color: rgb(255 255 255 / 0.5);
        font-size: 0.6875rem;
        font-weight: 600;
        cursor: pointer;
        border-radius: 0.25rem;
      }
      .tab--active {
        color: white;
        background: rgb(255 255 255 / 0.1);
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
        color: rgb(255 255 255 / 0.4);
        margin-bottom: 0.375rem;
      }
      .value {
        color: rgb(255 255 255 / 0.9);
        font-family: ui-monospace, monospace;
      }
      .mode-group {
        display: flex;
        gap: 0.25rem;
        flex-wrap: wrap;
      }
      .mode-btn {
        padding: 0.25rem 0.5rem;
        border: 1px solid rgb(255 255 255 / 0.15);
        background: none;
        color: rgb(255 255 255 / 0.6);
        font-size: 0.6875rem;
        cursor: pointer;
        border-radius: 0.25rem;
        transition: all 150ms;
      }
      .mode-btn:hover {
        border-color: rgb(255 255 255 / 0.3);
        color: white;
      }
      .mode-btn--active {
        border-color: hsl(var(--tn-blue, 217 89% 61%));
        color: hsl(var(--tn-blue, 217 89% 61%));
        background: hsl(var(--tn-blue, 217 89% 61%) / 0.1);
      }
      .actions {
        display: flex;
        gap: 0.5rem;
        margin-top: 0.5rem;
      }
      .action-btn {
        padding: 0.375rem 0.75rem;
        border: 1px solid rgb(255 255 255 / 0.15);
        background: none;
        color: rgb(255 255 255 / 0.7);
        font-size: 0.6875rem;
        cursor: pointer;
        border-radius: 0.25rem;
      }
      .action-btn:hover {
        border-color: rgb(255 255 255 / 0.3);
        color: white;
      }
      .stat-row {
        display: flex;
        justify-content: space-between;
        padding: 0.125rem 0;
      }
      .stat-label {
        color: rgb(255 255 255 / 0.4);
      }
      .stat-value {
        color: rgb(255 255 255 / 0.8);
        font-family: ui-monospace, monospace;
        font-variant-numeric: tabular-nums;
      }
    `,
  ];

  private _modes: PlaybackMode[] = ["auto", "low-latency", "quality"];

  protected render() {
    const s = this.pc.s;

    return html`
      <div class="panel fw-dev-panel">
        <div class="header fw-dev-header">
          <div class="tabs">
            <button
              class=${classMap({ tab: true, "tab--active": this._activeTab === "config" })}
              @click=${() => {
                this._activeTab = "config";
              }}
            >
              Config
            </button>
            <button
              class=${classMap({ tab: true, "tab--active": this._activeTab === "stats" })}
              @click=${() => {
                this._activeTab = "stats";
              }}
            >
              Stats
            </button>
          </div>
          <button
            class="close"
            @click=${() =>
              this.dispatchEvent(new CustomEvent("fw-close", { bubbles: true, composed: true }))}
            aria-label="Close panel"
          >
            ${closeIcon()}
          </button>
        </div>

        <div class="body fw-dev-body">
          ${this._activeTab === "config" ? this._renderConfig(s) : this._renderStats(s)}
        </div>
      </div>
    `;
  }

  private _renderConfig(s: typeof this.pc.s) {
    return html`
      <div class="section">
        <div class="label">Current Player</div>
        <div class="value">${s.currentPlayerInfo?.name ?? "—"}</div>
      </div>
      <div class="section">
        <div class="label">Current Source</div>
        <div class="value">${s.currentSourceInfo?.type ?? "—"}</div>
      </div>
      <div class="section">
        <div class="label">Playback Mode</div>
        <div class="mode-group fw-dev-mode-group">
          ${this._modes.map(
            (mode) => html`
              <button
                class=${classMap({
                  "mode-btn": true,
                  "fw-dev-mode-btn": true,
                  "mode-btn--active": this.playbackMode === mode,
                  "fw-dev-mode-btn--active": this.playbackMode === mode,
                })}
                @click=${() => this.pc.setDevModeOptions({ playbackMode: mode })}
              >
                ${mode}
              </button>
            `
          )}
        </div>
      </div>
      <div class="actions fw-dev-actions">
        <button
          class="action-btn fw-dev-action-btn"
          @click=${() => {
            this.pc.clearError();
            this.pc.reload();
          }}
        >
          Reload
        </button>
      </div>
    `;
  }

  private _renderStats(s: typeof this.pc.s) {
    const q = s.playbackQuality;
    return html`
      <div class="section">
        <div class="label">Playback</div>
        ${this._row("State", s.state)}
        ${this._row(
          "Time",
          `${s.currentTime.toFixed(1)}s / ${isFinite(s.duration) ? s.duration.toFixed(1) + "s" : "∞"}`
        )}
        ${this._row("Volume", `${Math.round(s.volume * 100)}%${s.isMuted ? " (muted)" : ""}`)}
      </div>
      ${q
        ? html`
            <div class="section">
              <div class="label">Quality</div>
              ${this._row("Resolution", this._resolution())}
              ${this._row("Bitrate", q.bitrate ? `${Math.round(q.bitrate / 1000)} kbps` : "—")}
              ${this._row("Latency", q.latency != null ? `${q.latency.toFixed(2)}s` : "—")}
              ${this._row(
                "Buffer",
                q.bufferedAhead != null ? `${q.bufferedAhead.toFixed(1)}s` : "—"
              )}
              ${this._row("Score", q.score != null ? `${q.score.toFixed(0)}` : "—")}
              ${this._row("Drops", `${q.frameDropRate?.toFixed(1) ?? "0"}%`)}
              ${this._row("Stalls", `${q.stallCount ?? 0}`)}
            </div>
          `
        : nothing}
    `;
  }

  private _resolution(): string {
    const video = this.pc.s.videoElement;
    if (!video || !video.videoWidth || !video.videoHeight) return "—";
    return `${video.videoWidth}×${video.videoHeight}`;
  }

  private _row(label: string, value: string) {
    return html`<div class="stat-row">
      <span class="stat-label">${label}</span><span class="stat-value">${value}</span>
    </div>`;
  }
}

declare global {
  interface HTMLElementTagNameMap {
    "fw-dev-mode-panel": FwDevModePanel;
  }
}
