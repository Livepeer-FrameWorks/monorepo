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
  @state() private _dragging = false;
  @state() private _hasAudio = true;

  private _activePointerId: number | null = null;
  private _activeSliderTarget: HTMLElement | null = null;
  private _boundStream: MediaStream | null = null;
  private _onStreamTrackChange: (() => void) | null = null;

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
        touch-action: none;
        user-select: none;
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
    return this._hovered || this._focused || this._dragging;
  }

  disconnectedCallback(): void {
    super.disconnectedCallback();
    this._endDragInteraction();
    this._unbindStreamListeners();
  }

  protected updated(): void {
    this._updateHasAudio();
  }

  private _unbindStreamListeners(): void {
    if (this._boundStream && this._onStreamTrackChange) {
      this._boundStream.removeEventListener("addtrack", this._onStreamTrackChange);
      this._boundStream.removeEventListener("removetrack", this._onStreamTrackChange);
    }
    this._boundStream = null;
    this._onStreamTrackChange = null;
  }

  private _updateHasAudio(): void {
    const video = this.pc?.s.videoElement;
    if (!video) {
      this._unbindStreamListeners();
      this._hasAudio = true;
      return;
    }

    // MediaStream: bind track change listeners (WebRTC tracks arrive async)
    if (video.srcObject instanceof MediaStream) {
      const stream = video.srcObject;
      if (stream !== this._boundStream) {
        this._unbindStreamListeners();
        this._boundStream = stream;
        this._onStreamTrackChange = () => {
          this._hasAudio = stream.getAudioTracks().length > 0;
        };
        stream.addEventListener("addtrack", this._onStreamTrackChange);
        stream.addEventListener("removetrack", this._onStreamTrackChange);
      }
      this._hasAudio = stream.getAudioTracks().length > 0;
      return;
    }

    this._unbindStreamListeners();

    // Fallback: metadata for non-MediaStream sources.
    const mistHasAudio = this.pc?.s.streamState?.streamInfo?.hasAudio;
    if (mistHasAudio !== undefined) {
      this._hasAudio = mistHasAudio;
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

  private _beginDragInteraction(target: HTMLElement, pointerId: number): void {
    this._activePointerId = pointerId;
    this._activeSliderTarget = target;
    this._dragging = true;
    this._hovered = true;
    this._focused = true;

    try {
      target.setPointerCapture(pointerId);
    } catch {
      // Non-fatal: we still listen on window as a fallback.
    }

    window.addEventListener("pointermove", this._onGlobalPointerMove);
    window.addEventListener("pointerup", this._onGlobalPointerUp);
    window.addEventListener("pointercancel", this._onGlobalPointerUp);
  }

  private _endDragInteraction(): void {
    const pointerId = this._activePointerId;
    const target = this._activeSliderTarget;
    if (pointerId != null && target) {
      try {
        if (target.hasPointerCapture(pointerId)) {
          target.releasePointerCapture(pointerId);
        }
      } catch {
        // Ignore pointer-capture release errors.
      }
    }

    this._activePointerId = null;
    this._activeSliderTarget = null;
    this._dragging = false;
    window.removeEventListener("pointermove", this._onGlobalPointerMove);
    window.removeEventListener("pointerup", this._onGlobalPointerUp);
    window.removeEventListener("pointercancel", this._onGlobalPointerUp);
  }

  private _onGlobalPointerMove = (event: PointerEvent): void => {
    if (!this._dragging || this._activePointerId !== event.pointerId) {
      return;
    }
    const target = this._activeSliderTarget;
    if (!target) {
      return;
    }
    this._setVolumeFromClientX(event.clientX, target);
  };

  private _onGlobalPointerUp = (event: PointerEvent): void => {
    if (!this._dragging || this._activePointerId !== event.pointerId) {
      return;
    }
    this._endDragInteraction();
  };

  private _handleMouseEnter = (): void => {
    this._hovered = true;
  };

  private _handleMouseLeave = (): void => {
    if (this._dragging) {
      return;
    }
    this._hovered = false;
    this._focused = false;
  };

  private _handleFocusIn = (): void => {
    this._focused = true;
  };

  private _handleFocusOut = (event: FocusEvent): void => {
    if (this._dragging) {
      return;
    }
    const related = event.relatedTarget as Node | null;
    if (!related || !this.renderRoot.contains(related)) {
      this._focused = false;
    }
  };

  private _onSliderPointerDown = (event: PointerEvent) => {
    if (!this._hasAudio) {
      return;
    }

    event.preventDefault();
    const target = event.currentTarget as HTMLElement;
    this._beginDragInteraction(target, event.pointerId);
    this._setVolumeFromClientX(event.clientX, target);
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
        @mouseenter=${this._handleMouseEnter}
        @mouseleave=${this._handleMouseLeave}
        @focusin=${this._handleFocusIn}
        @focusout=${this._handleFocusOut}
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
          aria-label=${!this._hasAudio
            ? "No audio"
            : isMuted
              ? this.pc.t("unmute")
              : this.pc.t("mute")}
          title=${!this._hasAudio ? "No audio" : isMuted ? this.pc.t("unmute") : this.pc.t("mute")}
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
            aria-label=${this.pc.t("volume")}
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
