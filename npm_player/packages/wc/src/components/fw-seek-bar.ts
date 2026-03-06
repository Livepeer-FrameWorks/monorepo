/**
 * <fw-seek-bar> — Video seek bar with buffer visualization, drag, and live tooltips.
 * Behavioral parity with react/svelte SeekBar implementations.
 */
import { LitElement, html, css, nothing } from "lit";
import { customElement, property, state } from "lit/decorators.js";
import { classMap } from "lit/directives/class-map.js";
import { styleMap } from "lit/directives/style-map.js";
import { sharedStyles } from "../styles/shared-styles.js";
import { findCueAtTime, type ThumbnailCue } from "@livepeer-frameworks/player-core";

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
  @property({ type: Boolean, attribute: "is-playing" }) isPlaying = false;
  @property({ attribute: false }) thumbnailCues: ThumbnailCue[] = [];

  @state() private _hovering = false;
  @state() private _dragging = false;
  @state() private _dragTime: number | null = null;
  @state() private _hoverPosition = 0;
  @state() private _hoverTime = 0;

  private _trackRect: DOMRect | null = null;
  private _activePointerId: number | null = null;
  private _rafId = 0;
  private _rafBase = { time: 0, stamp: 0 };

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
    cancelAnimationFrame(this._rafId);
  }

  updated(changed: Map<string, unknown>): void {
    if (changed.has("currentTime")) {
      this._rafBase = { time: this.currentTime, stamp: performance.now() };
    }
    if (
      changed.has("isPlaying") ||
      changed.has("disabled") ||
      changed.has("currentTime") ||
      changed.has("duration")
    ) {
      this._syncRaf();
    }
  }

  private _syncRaf(): void {
    const shouldAnimate = this.isPlaying && !this._dragging && !this.disabled;

    if (!shouldAnimate) {
      cancelAnimationFrame(this._rafId);
      const el = this.renderRoot.querySelector(".fw-seek-progress") as HTMLElement | null;
      if (el) {
        el.style.transform = `scaleX(${this._progressPercent / 100})`;
      }
      return;
    }

    cancelAnimationFrame(this._rafId);
    const rangeStart = this.isLive ? this.seekableStart : 0;
    const rangeSize = this.isLive ? this._seekableWindow : this.duration;

    const animate = () => {
      if (!this.isPlaying || this._dragging || this.disabled) return;
      const interpolated = this._rafBase.time + (performance.now() - this._rafBase.stamp);
      const relative = interpolated - rangeStart;
      const pct =
        Number.isFinite(rangeSize) && rangeSize > 0
          ? Math.min(100, Math.max(0, (relative / rangeSize) * 100))
          : 0;

      const el = this.renderRoot.querySelector(".fw-seek-progress") as HTMLElement | null;
      if (el) {
        el.style.transform = `scaleX(${pct / 100})`;
      }
      this._rafId = requestAnimationFrame(animate);
    };

    this._rafId = requestAnimationFrame(animate);
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
      // buffered TimeRanges are in seconds (browser API), convert to ms
      const start = buffered.start(i) * 1000;
      const end = buffered.end(i) * 1000;
      const relativeStart = start - rangeStart;
      const relativeEnd = end - rangeStart;

      segments.push({
        startPercent: Math.min(100, Math.max(0, (relativeStart / rangeSize) * 100)),
        endPercent: Math.min(100, Math.max(0, (relativeEnd / rangeSize) * 100)),
      });
    }

    return segments;
  }

  private _formatTime(ms: number): string {
    if (!Number.isFinite(ms) || ms < 0) {
      return "0:00";
    }

    const total = Math.floor(ms / 1000);
    const hours = Math.floor(total / 3600);
    const minutes = Math.floor((total % 3600) / 60);
    const secs = total % 60;

    if (hours > 0) {
      return `${hours}:${String(minutes).padStart(2, "0")}:${String(secs).padStart(2, "0")}`;
    }

    return `${minutes}:${String(secs).padStart(2, "0")}`;
  }

  private _formatLiveTime(ms: number, edgeMs: number): string {
    const behindMs = edgeMs - ms;
    if (behindMs < 1000) {
      return "LIVE";
    }

    const total = Math.floor(behindMs / 1000);
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

    const step = event.shiftKey ? 10000 : 5000;
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
    this._didDragMove = false;

    const initialTime = this._getTimeFromClientX(event.clientX);
    this._updateHover(event.clientX);

    if (this.commitOnRelease) {
      this._dragTime = initialTime;
    } else {
      this._emitSeek(initialTime);
    }

    this._attachDragListeners();
  };

  private _didDragMove = false;

  private _onGlobalPointerMove = (event: PointerEvent) => {
    if (!this._dragging || this._activePointerId !== event.pointerId) {
      return;
    }

    this._didDragMove = true;
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
    // Non-commitOnRelease: drag moves already emitted seeks, no double-seek on release

    this._dragging = false;
    this._dragTime = null;
    this._activePointerId = null;
    this._didDragMove = false;
    this._detachDragListeners();
    this._syncRaf();
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

  private get _thumbnailStyle(): Record<string, string> | null {
    if (!this.thumbnailCues?.length || !this._hovering || this._dragging) return null;
    const cue = findCueAtTime(this.thumbnailCues, this._hoverTime / 1000);
    if (!cue || cue.width === undefined || cue.height === undefined) return null;
    return {
      backgroundImage: `url(${cue.url})`,
      backgroundPosition: `-${cue.x ?? 0}px -${cue.y ?? 0}px`,
      backgroundSize: "auto",
      width: `${cue.width}px`,
      height: `${cue.height}px`,
      left: `${this._hoverPosition}%`,
    };
  }

  protected render() {
    const progressPercent = this._progressPercent;
    const showThumb = this._hovering || this._dragging;
    const canShowTooltip = this.isLive ? this._seekableWindow > 0 : Number.isFinite(this.duration);
    const thumbStyle = this._thumbnailStyle;

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
          <div
            class="fw-seek-progress"
            style=${styleMap({ transform: `scaleX(${progressPercent / 100})` })}
          ></div>
          ${this._hovering && !this._dragging
            ? html`<div
                class="fw-seek-hover-line"
                style=${styleMap({ left: `${this._hoverPosition}%` })}
              ></div>`
            : nothing}
          ${this.isLive ? html`<div class="fw-seek-live-edge"></div>` : nothing}
        </div>

        <div
          class=${classMap({
            "fw-seek-thumb": true,
            "fw-seek-thumb--active": showThumb,
            "fw-seek-thumb--hidden": !showThumb,
          })}
          style=${styleMap({ left: `${progressPercent}%` })}
        ></div>

        ${thumbStyle
          ? html`<div class="fw-seek-thumbnail" style=${styleMap(thumbStyle)}></div>`
          : nothing}
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
