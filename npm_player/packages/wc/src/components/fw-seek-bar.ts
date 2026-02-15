/**
 * <fw-seek-bar> — Video seek bar with buffer visualization, drag, and live tooltips.
 * Behavioral parity with react/svelte SeekBar implementations.
 */
import { LitElement, html, css, nothing } from "lit";
import { customElement, property, state } from "lit/decorators.js";
import { classMap } from "lit/directives/class-map.js";
import { styleMap } from "lit/directives/style-map.js";
import { sharedStyles } from "../styles/shared-styles.js";

interface BufferedSegment {
  startPercent: number;
  endPercent: number;
}

@customElement("fw-seek-bar")
export class FwSeekBar extends LitElement {
  @property({ type: Number }) currentTime = 0;
  @property({ type: Number }) duration = 0;
  @property({ attribute: false }) buffered: TimeRanges | null = null;
  @property({ type: Boolean }) disabled = false;
  @property({ type: Boolean, attribute: "is-live" }) isLive = false;
  @property({ type: Number, attribute: "seekable-start" }) seekableStart = 0;
  @property({ type: Number, attribute: "live-edge" }) liveEdge?: number;
  @property({ type: Boolean, attribute: "commit-on-release" }) commitOnRelease = false;

  @state() private _hovering = false;
  @state() private _dragging = false;
  @state() private _dragTime: number | null = null;
  @state() private _hoverPosition = 0;
  @state() private _hoverTime = 0;

  private _trackRect: DOMRect | null = null;
  private _activePointerId: number | null = null;

  static styles = [
    sharedStyles,
    css`
      :host {
        display: block;
        width: 100%;
      }

      .seek-root {
        position: relative;
        width: 100%;
        height: 1.5rem;
        display: flex;
        align-items: center;
        cursor: pointer;
      }

      .seek-root--disabled {
        opacity: 0.5;
        cursor: not-allowed;
      }
    `,
  ];

  disconnectedCallback(): void {
    super.disconnectedCallback();
    this._detachDragListeners();
  }

  private get _effectiveLiveEdge(): number {
    if (typeof this.liveEdge === "number" && Number.isFinite(this.liveEdge)) {
      return this.liveEdge;
    }
    return this.duration;
  }

  private get _seekableWindow(): number {
    return this._effectiveLiveEdge - this.seekableStart;
  }

  private get _displayTime(): number {
    return this._dragTime ?? this.currentTime;
  }

  private get _progressPercent(): number {
    const displayTime = this._displayTime;

    if (this.isLive && this._seekableWindow > 0) {
      const positionInWindow = displayTime - this.seekableStart;
      return Math.min(100, Math.max(0, (positionInWindow / this._seekableWindow) * 100));
    }

    if (!Number.isFinite(this.duration) || this.duration <= 0) {
      return 0;
    }

    return Math.min(100, Math.max(0, (displayTime / this.duration) * 100));
  }

  private get _bufferedSegments(): BufferedSegment[] {
    const buffered = this.buffered;
    if (!buffered || buffered.length === 0) {
      return [];
    }

    const rangeEnd = this.isLive ? this._effectiveLiveEdge : this.duration;
    const rangeStart = this.isLive ? this.seekableStart : 0;
    const rangeSize = rangeEnd - rangeStart;

    if (!Number.isFinite(rangeSize) || rangeSize <= 0) {
      return [];
    }

    const segments: BufferedSegment[] = [];
    for (let i = 0; i < buffered.length; i += 1) {
      const start = buffered.start(i);
      const end = buffered.end(i);
      const relativeStart = start - rangeStart;
      const relativeEnd = end - rangeStart;

      segments.push({
        startPercent: Math.min(100, Math.max(0, (relativeStart / rangeSize) * 100)),
        endPercent: Math.min(100, Math.max(0, (relativeEnd / rangeSize) * 100)),
      });
    }

    return segments;
  }

  private _formatTime(seconds: number): string {
    if (!Number.isFinite(seconds) || seconds < 0) {
      return "0:00";
    }

    const total = Math.floor(seconds);
    const hours = Math.floor(total / 3600);
    const minutes = Math.floor((total % 3600) / 60);
    const secs = total % 60;

    if (hours > 0) {
      return `${hours}:${String(minutes).padStart(2, "0")}:${String(secs).padStart(2, "0")}`;
    }

    return `${minutes}:${String(secs).padStart(2, "0")}`;
  }

  private _formatLiveTime(seconds: number, edge: number): string {
    const behindSeconds = edge - seconds;
    if (behindSeconds < 1) {
      return "LIVE";
    }

    const total = Math.floor(behindSeconds);
    const hours = Math.floor(total / 3600);
    const minutes = Math.floor((total % 3600) / 60);
    const secs = total % 60;

    if (hours > 0) {
      return `-${hours}:${String(minutes).padStart(2, "0")}:${String(secs).padStart(2, "0")}`;
    }

    return `-${minutes}:${String(secs).padStart(2, "0")}`;
  }

  private _getTrackRect(): DOMRect | null {
    const track = this.renderRoot.querySelector(".seek-root") as HTMLDivElement | null;
    if (!track) {
      return null;
    }

    this._trackRect = track.getBoundingClientRect();
    return this._trackRect;
  }

  private _getTimeFromClientX(clientX: number): number {
    const rect = this._getTrackRect();
    if (!rect || rect.width <= 0) {
      return 0;
    }

    const x = clientX - rect.left;
    const percent = Math.min(1, Math.max(0, x / rect.width));

    if (this.isLive && Number.isFinite(this._seekableWindow) && this._seekableWindow > 0) {
      return this.seekableStart + percent * this._seekableWindow;
    }

    if (Number.isFinite(this.duration) && this.duration > 0) {
      return percent * this.duration;
    }

    if (Number.isFinite(this._effectiveLiveEdge) && this._effectiveLiveEdge > 0) {
      const start = Number.isFinite(this.seekableStart) ? this.seekableStart : 0;
      const window = this._effectiveLiveEdge - start;
      if (window > 0) {
        return start + percent * window;
      }
    }

    return percent * (this.currentTime || 1);
  }

  private _updateHover(clientX: number): void {
    const rect = this._getTrackRect();
    if (!rect || rect.width <= 0) {
      return;
    }

    const x = clientX - rect.left;
    const percent = Math.min(1, Math.max(0, x / rect.width));
    this._hoverPosition = percent * 100;
    this._hoverTime = this._getTimeFromClientX(clientX);
  }

  private _emitSeek(time: number): void {
    this.dispatchEvent(
      new CustomEvent("fw-seek", { detail: { time }, bubbles: true, composed: true })
    );
  }

  private _onKeyDown = (event: KeyboardEvent) => {
    if (this.disabled) {
      return;
    }

    const step = event.shiftKey ? 10 : 5;
    const rawRangeEnd = this.isLive ? this._effectiveLiveEdge : this.duration;
    const rangeEnd = Number.isFinite(rawRangeEnd) ? rawRangeEnd : this.currentTime + step;
    const rangeStart = this.isLive ? this.seekableStart : 0;

    let newTime: number | null = null;
    switch (event.key) {
      case "ArrowLeft":
      case "ArrowDown":
        newTime = Math.max(rangeStart, this.currentTime - step);
        break;
      case "ArrowRight":
      case "ArrowUp":
        newTime = Math.min(rangeEnd, this.currentTime + step);
        break;
      case "Home":
        newTime = rangeStart;
        break;
      case "End":
        newTime = rangeEnd;
        break;
      default:
        return;
    }

    if (newTime != null) {
      event.preventDefault();
      this._emitSeek(newTime);
    }
  };

  private _onPointerEnter = () => {
    if (this.disabled) {
      return;
    }
    this._hovering = true;
  };

  private _onPointerLeave = () => {
    this._hovering = false;
    this._trackRect = null;
  };

  private _onPointerMove = (event: PointerEvent) => {
    if (this.disabled || (!this._hovering && !this._dragging)) {
      return;
    }
    this._updateHover(event.clientX);
  };

  private _onPointerDown = (event: PointerEvent) => {
    if (this.disabled) {
      return;
    }

    if (!this.isLive && !Number.isFinite(this.duration)) {
      return;
    }

    event.preventDefault();
    this._activePointerId = event.pointerId;
    this._dragging = true;
    this._hovering = true;

    const initialTime = this._getTimeFromClientX(event.clientX);
    this._updateHover(event.clientX);

    if (this.commitOnRelease) {
      this._dragTime = initialTime;
    } else {
      this._emitSeek(initialTime);
    }

    this._attachDragListeners();
  };

  private _onGlobalPointerMove = (event: PointerEvent) => {
    if (!this._dragging || this._activePointerId !== event.pointerId) {
      return;
    }

    const time = this._getTimeFromClientX(event.clientX);
    this._updateHover(event.clientX);

    if (this.commitOnRelease) {
      this._dragTime = time;
    } else {
      this._emitSeek(time);
    }
  };

  private _onGlobalPointerUp = (event: PointerEvent) => {
    if (!this._dragging || this._activePointerId !== event.pointerId) {
      return;
    }

    if (this.commitOnRelease && this._dragTime != null) {
      this._emitSeek(this._dragTime);
    }

    this._dragging = false;
    this._dragTime = null;
    this._activePointerId = null;
    this._detachDragListeners();
  };

  private _attachDragListeners(): void {
    window.addEventListener("pointermove", this._onGlobalPointerMove);
    window.addEventListener("pointerup", this._onGlobalPointerUp);
    window.addEventListener("pointercancel", this._onGlobalPointerUp);
  }

  private _detachDragListeners(): void {
    window.removeEventListener("pointermove", this._onGlobalPointerMove);
    window.removeEventListener("pointerup", this._onGlobalPointerUp);
    window.removeEventListener("pointercancel", this._onGlobalPointerUp);
  }

  protected render() {
    const progressPercent = this._progressPercent;
    const showThumb = this._hovering || this._dragging;
    const canShowTooltip = this.isLive ? this._seekableWindow > 0 : Number.isFinite(this.duration);

    return html`
      <div
        class=${classMap({
          "seek-root": true,
          "seek-root--disabled": this.disabled,
          "fw-seek-root": true,
        })}
        @pointerenter=${this._onPointerEnter}
        @pointerleave=${this._onPointerLeave}
        @pointermove=${this._onPointerMove}
        @pointerdown=${this._onPointerDown}
        role="slider"
        aria-label="Seek"
        aria-valuemin=${this.isLive ? this.seekableStart : 0}
        aria-valuemax=${this.isLive
          ? this._effectiveLiveEdge
          : Number.isFinite(this.duration)
            ? this.duration
            : 100}
        aria-valuenow=${this._displayTime}
        aria-valuetext=${this.isLive
          ? this._formatLiveTime(this._displayTime, this._effectiveLiveEdge)
          : this._formatTime(this._displayTime)}
        tabindex=${this.disabled ? -1 : 0}
        @keydown=${this._onKeyDown}
      >
        <div class=${classMap({ "fw-seek-track": true, "fw-seek-track--active": this._dragging })}>
          ${this._bufferedSegments.map(
            (segment) => html`
              <div
                class="fw-seek-buffered"
                style=${styleMap({
                  left: `${segment.startPercent}%`,
                  width: `${segment.endPercent - segment.startPercent}%`,
                })}
              ></div>
            `
          )}
          <div class="fw-seek-progress" style=${styleMap({ width: `${progressPercent}%` })}></div>
        </div>

        <div
          class=${classMap({
            "fw-seek-thumb": true,
            "fw-seek-thumb--active": showThumb,
            "fw-seek-thumb--hidden": !showThumb,
          })}
          style=${styleMap({ left: `${progressPercent}%` })}
        ></div>

        ${this._hovering && !this._dragging && canShowTooltip
          ? html`
              <div class="fw-seek-tooltip" style=${styleMap({ left: `${this._hoverPosition}%` })}>
                ${this.isLive
                  ? this._formatLiveTime(this._hoverTime, this._effectiveLiveEdge)
                  : this._formatTime(this._hoverTime)}
              </div>
            `
          : nothing}
      </div>
    `;
  }
}

declare global {
  interface HTMLElementTagNameMap {
    "fw-seek-bar": FwSeekBar;
  }
}
