/**
 * <fw-seek-bar> — Video seek bar with buffer visualization, drag, tooltips.
 * Port of SeekBar.tsx from player-react.
 */
import { LitElement, html, css, nothing } from "lit";
import { customElement, property, state, query } from "lit/decorators.js";
import { classMap } from "lit/directives/class-map.js";
import { styleMap } from "lit/directives/style-map.js";
import { sharedStyles } from "../styles/shared-styles.js";

@customElement("fw-seek-bar")
export class FwSeekBar extends LitElement {
  @property({ type: Number }) currentTime = 0;
  @property({ type: Number }) duration = 0;
  @property({ type: Boolean }) disabled = false;
  @property({ type: Boolean, attribute: "is-live" }) isLive = false;

  @state() private _hovering = false;
  @state() private _dragging = false;
  @state() private _hoverPct = 0;
  @state() private _dragPct: number | null = null;

  @query(".track") private _trackEl!: HTMLDivElement;

  static styles = [
    sharedStyles,
    css`
      :host {
        display: block;
        width: 100%;
      }
      .wrap {
        position: relative;
        padding: 0.25rem 0;
        cursor: pointer;
      }
      .wrap--disabled {
        opacity: 0.4;
        pointer-events: none;
      }
      .track {
        position: relative;
        height: 4px;
        background: rgb(255 255 255 / 0.15);
        border-radius: 2px;
        overflow: hidden;
        transition: height 150ms ease;
      }
      .wrap:hover .track,
      .track--active {
        height: 6px;
      }
      .progress {
        position: absolute;
        top: 0;
        left: 0;
        height: 100%;
        background: hsl(var(--tn-blue, 217 89% 61%));
        border-radius: 2px;
        pointer-events: none;
      }
      .thumb {
        position: absolute;
        top: 50%;
        width: 12px;
        height: 12px;
        border-radius: 50%;
        background: white;
        transform: translate(-50%, -50%) scale(0);
        transition: transform 150ms ease;
        z-index: 5;
        pointer-events: none;
      }
      .wrap:hover .thumb,
      .thumb--active {
        transform: translate(-50%, -50%) scale(1);
      }
      .tooltip {
        position: absolute;
        bottom: 100%;
        margin-bottom: 8px;
        transform: translateX(-50%);
        padding: 0.25rem 0.5rem;
        border-radius: 4px;
        background: rgb(0 0 0 / 0.85);
        color: white;
        font-size: 0.6875rem;
        font-variant-numeric: tabular-nums;
        white-space: nowrap;
        pointer-events: none;
        z-index: 10;
      }
    `,
  ];

  private _pctFromEvent(e: MouseEvent | PointerEvent): number {
    if (!this._trackEl) return 0;
    const rect = this._trackEl.getBoundingClientRect();
    return Math.max(0, Math.min(1, (e.clientX - rect.left) / rect.width));
  }

  private _timeFromPct(pct: number): number {
    const d = this.isLive ? this.duration : this.duration;
    return isFinite(d) && d > 0 ? pct * d : 0;
  }

  private _formatTime(seconds: number): string {
    if (!isFinite(seconds)) return "0:00";
    const abs = Math.abs(Math.floor(seconds));
    const h = Math.floor(abs / 3600);
    const m = Math.floor((abs % 3600) / 60);
    const s = abs % 60;
    const sign = seconds < 0 ? "-" : "";
    if (h > 0) return `${sign}${h}:${String(m).padStart(2, "0")}:${String(s).padStart(2, "0")}`;
    return `${sign}${m}:${String(s).padStart(2, "0")}`;
  }

  private _handlePointerDown = (e: PointerEvent) => {
    if (this.disabled) return;
    e.preventDefault();
    this._dragging = true;
    this._dragPct = this._pctFromEvent(e);
    (e.target as HTMLElement).setPointerCapture?.(e.pointerId);
  };

  private _handlePointerMove = (e: PointerEvent) => {
    const pct = this._pctFromEvent(e);
    if (this._dragging) {
      this._dragPct = pct;
    }
    this._hoverPct = pct;
  };

  private _handlePointerUp = (e: PointerEvent) => {
    if (this._dragging && this._dragPct != null) {
      const time = this._timeFromPct(this._dragPct);
      this.dispatchEvent(
        new CustomEvent("fw-seek", { detail: { time }, bubbles: true, composed: true })
      );
    }
    this._dragging = false;
    this._dragPct = null;
  };

  private _handleClick = (e: MouseEvent) => {
    if (this.disabled || this._dragging) return;
    const pct = this._pctFromEvent(e);
    const time = this._timeFromPct(pct);
    this.dispatchEvent(
      new CustomEvent("fw-seek", { detail: { time }, bubbles: true, composed: true })
    );
  };

  private _handleMouseEnter = () => {
    this._hovering = true;
  };
  private _handleMouseLeave = () => {
    this._hovering = false;
  };

  private get _progressPct(): number {
    if (this._dragPct != null) return this._dragPct * 100;
    const d = this.duration;
    if (!isFinite(d) || d <= 0) return 0;
    return Math.min(100, (this.currentTime / d) * 100);
  }

  protected render() {
    const progressPct = this._progressPct;
    const thumbPct = this._dragPct != null ? this._dragPct * 100 : progressPct;
    const showTooltip = this._hovering || this._dragging;
    const tooltipPct = this._dragging ? (this._dragPct ?? 0) * 100 : this._hoverPct * 100;
    const tooltipTime = this._timeFromPct(tooltipPct / 100);

    let tooltipText = this._formatTime(tooltipTime);
    if (this.isLive && isFinite(this.duration) && this.duration > 0) {
      const behindLive = tooltipTime - this.duration;
      if (behindLive < -1) tooltipText = this._formatTime(behindLive);
    }

    return html`
      <div
        class=${classMap({ wrap: true, "wrap--disabled": this.disabled })}
        @pointerdown=${this._handlePointerDown}
        @pointermove=${this._handlePointerMove}
        @pointerup=${this._handlePointerUp}
        @click=${this._handleClick}
        @mouseenter=${this._handleMouseEnter}
        @mouseleave=${this._handleMouseLeave}
      >
        <div
          class=${classMap({ track: true, "track--active": this._dragging, "fw-seek-track": true })}
        >
          <div
            class="progress fw-seek-progress"
            style=${styleMap({ width: `${progressPct}%` })}
          ></div>
        </div>
        <div
          class=${classMap({ thumb: true, "thumb--active": this._dragging, "fw-seek-thumb": true })}
          style=${styleMap({ left: `${thumbPct}%` })}
        ></div>
        ${showTooltip
          ? html`
              <div class="tooltip fw-seek-tooltip" style=${styleMap({ left: `${tooltipPct}%` })}>
                ${tooltipText}
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
