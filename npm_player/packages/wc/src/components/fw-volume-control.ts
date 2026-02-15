/**
 * <fw-volume-control> — Mute toggle + expandable volume slider.
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

  @state() private _hovered = false;
  @state() private _focused = false;
  @state() private _hasAudio = true;

  private _activePointerId: number | null = null;

  static styles = [
    sharedStyles,
    css`
      :host {
        display: flex;
        align-items: center;
      }

      .slider {
        position: relative;
        width: 100%;
        height: 0.25rem;
        background: rgb(255 255 255 / 0.2);
        border-radius: 9999px;
        cursor: pointer;
      }

      .slider-fill {
        position: absolute;
        top: 0;
        left: 0;
        height: 100%;
        border-radius: 9999px;
        background: hsl(var(--tn-fg));
      }

      .slider-thumb {
        position: absolute;
        top: 50%;
        width: 0.625rem;
        height: 0.625rem;
        border-radius: 9999px;
        background: hsl(var(--tn-fg));
        transform: translate(-50%, -50%);
        pointer-events: none;
      }
    `,
  ];

  private get _expanded(): boolean {
    return this._hovered || this._focused;
  }

  protected updated(): void {
    this._updateHasAudio();
  }

  private _updateHasAudio(): void {
    const video = this.pc?.s.videoElement;
    if (!video) {
      this._hasAudio = true;
      return;
    }

    if (video.srcObject instanceof MediaStream) {
      this._hasAudio = video.srcObject.getAudioTracks().length > 0;
      return;
    }

    const maybeWithTracks = video as HTMLVideoElement & {
      audioTracks?: {
        length: number;
      };
    };

    if (maybeWithTracks.audioTracks && typeof maybeWithTracks.audioTracks.length === "number") {
      this._hasAudio = maybeWithTracks.audioTracks.length > 0;
      return;
    }

    this._hasAudio = true;
  }

  private _setVolumeFromClientX(clientX: number, target: HTMLElement): void {
    const rect = target.getBoundingClientRect();
    if (rect.width <= 0) {
      return;
    }

    const pct = Math.max(0, Math.min(1, (clientX - rect.left) / rect.width));
    this.pc.setVolume(pct);

    if (this.pc.s.isMuted && pct > 0) {
      this.pc.toggleMute();
    }
  }

  private _onSliderPointerDown = (event: PointerEvent) => {
    if (!this._hasAudio) {
      return;
    }

    event.preventDefault();
    const target = event.currentTarget as HTMLElement;
    this._activePointerId = event.pointerId;
    this._setVolumeFromClientX(event.clientX, target);

    const onMove = (moveEvent: PointerEvent) => {
      if (this._activePointerId !== moveEvent.pointerId) {
        return;
      }
      this._setVolumeFromClientX(moveEvent.clientX, target);
    };

    const onUp = (upEvent: PointerEvent) => {
      if (this._activePointerId !== upEvent.pointerId) {
        return;
      }

      this._activePointerId = null;
      window.removeEventListener("pointermove", onMove);
      window.removeEventListener("pointerup", onUp);
      window.removeEventListener("pointercancel", onUp);
    };

    window.addEventListener("pointermove", onMove);
    window.addEventListener("pointerup", onUp);
    window.addEventListener("pointercancel", onUp);
  };

  private _onWheel = (event: WheelEvent) => {
    if (!this._hasAudio) {
      return;
    }

    event.preventDefault();

    const current = this.pc.s.isMuted ? 0 : Math.round(this.pc.s.volume * 100);
    const delta = event.deltaY < 0 ? 5 : -5;
    const next = Math.max(0, Math.min(100, current + delta));
    this.pc.setVolume(next / 100);

    if (this.pc.s.isMuted && next > 0) {
      this.pc.toggleMute();
    }
  };

  protected render() {
    const isMuted = this.pc.s.isMuted;
    const volume = this.pc.s.volume;
    const displayVolume = isMuted ? 0 : Math.max(0, Math.min(1, volume));

    return html`
      <div
        class=${classMap({
          "fw-volume-group": true,
          "fw-volume-group--expanded": this._expanded,
          "fw-volume-group--disabled": !this._hasAudio,
        })}
        role="group"
        aria-label="Volume controls"
        @mouseenter=${() => {
          this._hovered = true;
        }}
        @mouseleave=${() => {
          this._hovered = false;
          this._focused = false;
        }}
        @focusin=${() => {
          this._focused = true;
        }}
        @focusout=${(event: FocusEvent) => {
          const related = event.relatedTarget as Node | null;
          if (!related || !this.renderRoot.contains(related)) {
            this._focused = false;
          }
        }}
        @click=${(event: MouseEvent) => {
          if (this._hasAudio && event.target === event.currentTarget) {
            this.pc.toggleMute();
          }
        }}
        @wheel=${this._onWheel}
      >
        <button
          class="fw-volume-btn"
          type="button"
          @click=${() => {
            if (this._hasAudio) {
              this.pc.toggleMute();
            }
          }}
          ?disabled=${!this._hasAudio}
          aria-label=${!this._hasAudio ? "No audio" : isMuted ? "Unmute" : "Mute"}
          title=${!this._hasAudio ? "No audio" : isMuted ? "Unmute" : "Mute"}
        >
          ${isMuted || !this._hasAudio ? volumeOffIcon(16) : volumeUpIcon(16)}
        </button>

        <div
          class=${classMap({
            "fw-volume-slider-wrapper": true,
            "fw-volume-slider-wrapper--expanded": this._expanded,
            "fw-volume-slider-wrapper--collapsed": !this._expanded,
          })}
        >
          <div
            class="slider"
            role="slider"
            aria-label="Volume"
            aria-valuemin="0"
            aria-valuemax="100"
            aria-valuenow=${Math.round(displayVolume * 100)}
            @pointerdown=${this._onSliderPointerDown}
          >
            <div class="slider-fill" style=${styleMap({ width: `${displayVolume * 100}%` })}></div>
            <div class="slider-thumb" style=${styleMap({ left: `${displayVolume * 100}%` })}></div>
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
