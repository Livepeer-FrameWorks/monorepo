import { LitElement, html, css } from "lit";
import { customElement, property } from "lit/decorators.js";

@customElement("fw-speed-indicator")
export class FwSpeedIndicator extends LitElement {
  @property({ type: Number }) speed = 2;

  static styles = css`
    :host {
      display: contents;
    }
    .pill {
      position: absolute;
      top: 0.75rem;
      right: 0.75rem;
      display: inline-flex;
      align-items: center;
      gap: 0.375rem;
      padding: 0.25rem 0.625rem;
      border-radius: 9999px;
      background: rgb(0 0 0 / 0.6);
      color: white;
      font-size: 0.75rem;
      font-variant-numeric: tabular-nums;
      z-index: 30;
      transform: scale(1);
      animation: _fw-pop 150ms ease-out;
    }
    @keyframes _fw-pop {
      from {
        transform: scale(0.9);
        opacity: 0;
      }
      to {
        transform: scale(1);
        opacity: 1;
      }
    }
  `;

  protected render() {
    return html`
      <div class="pill fw-speed-indicator">
        <svg width="14" height="14" viewBox="0 0 24 24" fill="currentColor" aria-hidden="true">
          <path d="M4 18l8.5-6L4 6v12zm9-12v12l8.5-6L13 6z" />
        </svg>
        <span>${this.speed}x</span>
      </div>
    `;
  }
}

declare global {
  interface HTMLElementTagNameMap {
    "fw-speed-indicator": FwSpeedIndicator;
  }
}
