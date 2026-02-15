/**
 * <fw-volume-control> — Mute toggle + volume slider.
 */
import { LitElement, html, css } from "lit";
import { customElement, property, state } from "lit/decorators.js";
import { classMap } from "lit/directives/class-map.js";
import { styleMap } from "lit/directives/style-map.js";
import { sharedStyles } from "../styles/shared-styles.js";
import { volumeUpIcon, volumeOffIcon } from "../icons/index.js";
import type { PlayerControllerHost } from "../controllers/player-controller-host.js";

@customElement("fw-volume-control")
export class FwVolumeControl extends LitElement {
  @property({ attribute: false }) pc!: PlayerControllerHost;
  @state() private _expanded = false;

  static styles = [
    sharedStyles,
    css`
      :host {
        display: flex;
        align-items: center;
      }
      .group {
        display: flex;
        align-items: center;
        gap: 0;
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
      .slider-wrap {
        width: 0;
        overflow: hidden;
        transition: width 200ms ease;
      }
      .slider-wrap--expanded {
        width: 72px;
      }
      .slider {
        position: relative;
        width: 72px;
        height: 4px;
        background: rgb(255 255 255 / 0.15);
        border-radius: 2px;
        cursor: pointer;
      }
      .slider-fill {
        position: absolute;
        top: 0;
        left: 0;
        height: 100%;
        background: white;
        border-radius: 2px;
        pointer-events: none;
      }
      .slider-thumb {
        position: absolute;
        top: 50%;
        width: 10px;
        height: 10px;
        border-radius: 50%;
        background: white;
        transform: translate(-50%, -50%);
        pointer-events: none;
      }
    `,
  ];

  private _handleSliderClick = (e: MouseEvent) => {
    const target = e.currentTarget as HTMLElement;
    const rect = target.getBoundingClientRect();
    const pct = Math.max(0, Math.min(1, (e.clientX - rect.left) / rect.width));
    this.pc.setVolume(pct);
    if (this.pc.s.isMuted && pct > 0) this.pc.toggleMute();
  };

  private _handleSliderDrag = (e: PointerEvent) => {
    const target = e.currentTarget as HTMLElement;
    const rect = target.getBoundingClientRect();
    const pct = Math.max(0, Math.min(1, (e.clientX - rect.left) / rect.width));
    this.pc.setVolume(pct);
  };

  protected render() {
    const { isMuted, volume } = this.pc.s;
    const displayVol = isMuted ? 0 : volume;

    return html`
      <div
        class="group fw-volume-group"
        @mouseenter=${() => {
          this._expanded = true;
        }}
        @mouseleave=${() => {
          this._expanded = false;
        }}
      >
        <button
          class="btn fw-btn-flush fw-volume-btn"
          type="button"
          @click=${() => this.pc.toggleMute()}
          aria-label="${isMuted ? "Unmute" : "Mute"}"
        >
          ${isMuted ? volumeOffIcon(16) : volumeUpIcon(16)}
        </button>
        <div class=${classMap({ "slider-wrap": true, "slider-wrap--expanded": this._expanded })}>
          <div
            class="slider"
            @click=${this._handleSliderClick}
            @pointermove=${this._handleSliderDrag}
          >
            <div class="slider-fill" style=${styleMap({ width: `${displayVol * 100}%` })}></div>
            <div class="slider-thumb" style=${styleMap({ left: `${displayVol * 100}%` })}></div>
          </div>
        </div>
      </div>
    `;
  }
}

declare global {
  interface HTMLElementTagNameMap {
    "fw-volume-control": FwVolumeControl;
  }
}
