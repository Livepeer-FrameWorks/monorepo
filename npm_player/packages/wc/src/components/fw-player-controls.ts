/**
 * <fw-player-controls> — Control bar with play/pause, seek, volume, quality, fullscreen.
 * Port of PlayerControls.tsx from player-react.
 */
import { LitElement, html, css, nothing } from "lit";
import { customElement, property, state } from "lit/decorators.js";
import { classMap } from "lit/directives/class-map.js";
import { sharedStyles } from "../styles/shared-styles.js";
import { utilityStyles } from "../styles/utility-styles.js";
import {
  playIcon,
  pauseIcon,
  fullscreenIcon,
  fullscreenExitIcon,
  settingsIcon,
  seekToLiveIcon,
} from "../icons/index.js";
import type { PlayerControllerHost } from "../controllers/player-controller-host.js";
import type { PlaybackMode } from "@livepeer-frameworks/player-core";

@customElement("fw-player-controls")
export class FwPlayerControls extends LitElement {
  @property({ attribute: false }) pc!: PlayerControllerHost;
  @property({ type: String }) playbackMode: PlaybackMode = "auto";
  @property({ type: Boolean, attribute: "is-content-live" }) isContentLive = false;
  @property({ type: Boolean, attribute: "is-stats-open" }) isStatsOpen = false;

  @state() private _settingsOpen = false;

  static styles = [
    sharedStyles,
    utilityStyles,
    css`
      :host {
        display: contents;
      }
      .controls-wrapper {
        position: absolute;
        bottom: 0;
        left: 0;
        right: 0;
        z-index: 20;
        background: linear-gradient(to top, rgb(0 0 0 / 0.7), transparent);
        padding: 2rem 0.75rem 0.5rem;
        opacity: 0;
        transition: opacity 200ms ease;
        pointer-events: none;
      }
      .controls-wrapper--visible {
        opacity: 1;
        pointer-events: auto;
      }
      .bar {
        display: flex;
        flex-direction: column;
        gap: 0.25rem;
      }
      .row {
        display: flex;
        align-items: center;
        justify-content: space-between;
        gap: 0.25rem;
      }
      .group {
        display: flex;
        align-items: center;
        gap: 0.125rem;
      }
      .btn {
        display: flex;
        align-items: center;
        justify-content: center;
        width: 2rem;
        height: 2rem;
        background: none;
        border: none;
        color: rgb(255 255 255 / 0.8);
        cursor: pointer;
        padding: 0;
        border-radius: 0.25rem;
        transition: color 150ms;
      }
      .btn:hover {
        color: white;
      }
      .btn:disabled {
        opacity: 0.4;
        cursor: not-allowed;
      }
      .time {
        font-size: 0.6875rem;
        color: rgb(255 255 255 / 0.7);
        font-variant-numeric: tabular-nums;
        padding: 0 0.375rem;
        white-space: nowrap;
      }
      .live-badge {
        display: inline-flex;
        align-items: center;
        gap: 0.375rem;
        padding: 0.125rem 0.5rem;
        border-radius: 0.25rem;
        font-size: 0.6875rem;
        font-weight: 600;
        text-transform: uppercase;
        letter-spacing: 0.025em;
        cursor: pointer;
        border: none;
        background: none;
        transition: color 150ms;
      }
      .live-badge--active {
        color: hsl(var(--tn-red, 348 74% 64%));
      }
      .live-badge--behind {
        color: rgb(255 255 255 / 0.5);
      }
      .live-dot {
        width: 6px;
        height: 6px;
        border-radius: 50%;
        background: currentColor;
      }
      .settings-anchor {
        position: relative;
      }
    `,
  ];

  private _formatTime(seconds: number): string {
    if (!isFinite(seconds) || isNaN(seconds)) return "0:00";
    const abs = Math.abs(Math.floor(seconds));
    const h = Math.floor(abs / 3600);
    const m = Math.floor((abs % 3600) / 60);
    const s = abs % 60;
    if (h > 0) return `${h}:${String(m).padStart(2, "0")}:${String(s).padStart(2, "0")}`;
    return `${m}:${String(s).padStart(2, "0")}`;
  }

  private get _isNearLive(): boolean {
    if (!this.isContentLive) return false;
    const { currentTime, duration } = this.pc.s;
    if (!isFinite(duration) || duration <= 0) return true;
    return duration - currentTime < 10;
  }

  protected render() {
    const s = this.pc.s;
    const disabled = !s.videoElement;

    return html`
      <div
        class=${classMap({
          "controls-wrapper": true,
          "controls-wrapper--visible": s.shouldShowControls,
          "fw-controls-wrapper": true,
        })}
      >
        <div class="bar fw-control-bar">
          <!-- Seek bar -->
          <fw-seek-bar
            .currentTime=${s.currentTime}
            .duration=${s.duration}
            .disabled=${disabled}
            .isLive=${this.isContentLive}
            @fw-seek=${(e: CustomEvent) => this.pc.seek(e.detail.time)}
          ></fw-seek-bar>

          <!-- Button row -->
          <div class="row">
            <div class="group fw-control-group">
              <!-- Play/Pause -->
              <button
                class="btn fw-btn-flush"
                type="button"
                ?disabled=${disabled}
                @click=${() => this.pc.togglePlay()}
                aria-label="${s.isPlaying ? "Pause" : "Play"}"
              >
                ${s.isPlaying ? pauseIcon(18) : playIcon(18)}
              </button>

              <!-- Volume -->
              <fw-volume-control .pc=${this.pc}></fw-volume-control>

              <!-- Time display -->
              ${!this.isContentLive
                ? html`
                    <span class="time fw-time-display">
                      ${this._formatTime(s.currentTime)} / ${this._formatTime(s.duration)}
                    </span>
                  `
                : nothing}

              <!-- Live badge -->
              ${this.isContentLive
                ? html`
                    <button
                      class=${classMap({
                        "live-badge": true,
                        "fw-live-badge": true,
                        "live-badge--active": this._isNearLive,
                        "fw-live-badge--active": this._isNearLive,
                        "live-badge--behind": !this._isNearLive,
                        "fw-live-badge--behind": !this._isNearLive,
                      })}
                      type="button"
                      @click=${() => this.pc.jumpToLive()}
                      aria-label="Jump to live"
                    >
                      <span class="live-dot"></span>
                      LIVE
                    </button>
                  `
                : nothing}
            </div>

            <div class="group fw-control-group">
              <!-- Settings -->
              <div class="settings-anchor">
                <button
                  class="btn fw-btn-flush"
                  type="button"
                  @click=${() => {
                    this._settingsOpen = !this._settingsOpen;
                  }}
                  aria-label="Settings"
                >
                  ${settingsIcon(16)}
                </button>
                <fw-settings-menu .pc=${this.pc} .open=${this._settingsOpen}></fw-settings-menu>
              </div>

              <!-- Fullscreen -->
              <button
                class="btn fw-btn-flush"
                type="button"
                ?disabled=${disabled}
                @click=${() => this.pc.toggleFullscreen()}
                aria-label="${s.isFullscreen ? "Exit fullscreen" : "Fullscreen"}"
              >
                ${s.isFullscreen ? fullscreenExitIcon(16) : fullscreenIcon(16)}
              </button>
            </div>
          </div>
        </div>
      </div>
    `;
  }
}

declare global {
  interface HTMLElementTagNameMap {
    "fw-player-controls": FwPlayerControls;
  }
}
