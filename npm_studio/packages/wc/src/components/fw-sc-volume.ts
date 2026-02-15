/**
 * <fw-sc-volume> — Volume slider with snap-to-100% and popup tooltip.
 * Port of VolumeSlider.tsx from streamcrafter-react.
 */
import { LitElement, html, css } from "lit";
import { customElement, property, state, query } from "lit/decorators.js";
import { sharedStyles } from "../styles/shared-styles.js";

@customElement("fw-sc-volume")
export class FwScVolume extends LitElement {
  @property({ type: Number }) value = 1;
  @property({ type: Number }) min = 0;
  @property({ type: Number }) max = 2;
  @property({ type: Number, attribute: "snap-threshold" }) snapThreshold = 0.05;
  @property({ type: Boolean }) compact = false;

  @state() private _isDragging = false;
  @state() private _popupPosition = 0;

  @query("input[type=range]") private _slider!: HTMLInputElement;

  static styles = [
    sharedStyles,
    css`
      :host {
        display: inline-flex;
        position: relative;
        flex: 1;
      }
      :host([compact]) {
        min-width: 60px;
      }
      :host(:not([compact])) {
        min-width: 100px;
      }
      .track {
        position: relative;
        width: 100%;
      }
      .marker {
        position: absolute;
        top: 0;
        bottom: 0;
        width: 2px;
        border-radius: 1px;
        z-index: 1;
        pointer-events: none;
        transform: translateX(-50%);
      }
      input[type="range"] {
        width: 100%;
        height: 6px;
        border-radius: 3px;
        cursor: pointer;
      }
      .popup {
        position: absolute;
        bottom: 100%;
        transform: translateX(-50%);
        margin-bottom: 8px;
        padding: 4px 8px;
        color: #1a1b26;
        border-radius: 4px;
        font-size: 12px;
        font-weight: 600;
        font-family: monospace;
        white-space: nowrap;
        pointer-events: none;
        z-index: 100;
        box-shadow: 0 2px 8px rgba(0, 0, 0, 0.3);
      }
      .popup-arrow {
        position: absolute;
        top: 100%;
        left: 50%;
        transform: translateX(-50%);
        width: 0;
        height: 0;
        border-left: 6px solid transparent;
        border-right: 6px solid transparent;
      }
    `,
  ];

  private get _displayValue(): number {
    return Math.round(this.value * 100);
  }
  private get _isBoost(): boolean {
    return this.value > 1;
  }
  private get _isDefault(): boolean {
    return this.value === 1;
  }

  private get _accentColor(): string {
    if (this._isBoost) return "#e0af68";
    if (this._isDefault) return "#9ece6a";
    return "#7aa2f7";
  }

  private _handleChange(e: Event) {
    let newValue = parseInt((e.target as HTMLInputElement).value, 10) / 100;
    if (Math.abs(newValue - 1) <= this.snapThreshold) newValue = 1;
    this.dispatchEvent(
      new CustomEvent("fw-sc-volume-change", {
        detail: { value: newValue },
        bubbles: true,
        composed: true,
      })
    );
    this._updatePopupPosition(newValue);
  }

  private _handleMouseDown() {
    this._isDragging = true;
    this._updatePopupPosition(this.value);
  }

  private _handleMouseUp() {
    this._isDragging = false;
  }

  private _updatePopupPosition(value: number) {
    if (this._slider) {
      const rect = this._slider.getBoundingClientRect();
      const percent = (value - this.min) / (this.max - this.min);
      this._popupPosition = percent * rect.width;
    }
  }

  protected render() {
    const markerLeft = `${(1 / this.max) * 100}%`;
    const markerBg = this._isDefault ? "#9ece6a" : "rgba(158, 206, 106, 0.3)";

    return html`
      ${this._isDragging
        ? html`
            <div
              class="popup"
              style="left:${this._popupPosition}px;background:${this._accentColor}"
            >
              ${this._displayValue}%${this._isDefault ? " (default)" : ""}
              <div class="popup-arrow" style="border-top:6px solid ${this._accentColor}"></div>
            </div>
          `
        : ""}
      <div class="track">
        <div class="marker" style="left:${markerLeft};background:${markerBg}"></div>
        <input
          type="range"
          .min=${String(this.min * 100)}
          .max=${String(this.max * 100)}
          .value=${String(Math.round(this.value * 100))}
          @input=${this._handleChange}
          @mousedown=${this._handleMouseDown}
          @mouseup=${this._handleMouseUp}
          @mouseleave=${this._handleMouseUp}
          @touchstart=${this._handleMouseDown}
          @touchend=${this._handleMouseUp}
          style="accent-color:${this._accentColor}"
        />
      </div>
    `;
  }
}

declare global {
  interface HTMLElementTagNameMap {
    "fw-sc-volume": FwScVolume;
  }
}
