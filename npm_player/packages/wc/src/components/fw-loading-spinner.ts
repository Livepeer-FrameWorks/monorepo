import { LitElement, html, css } from "lit";
import { customElement } from "lit/decorators.js";

@customElement("fw-loading-spinner")
export class FwLoadingSpinner extends LitElement {
  static styles = css`
    :host {
      display: contents;
    }
    .overlay {
      position: absolute;
      inset: 0;
      display: flex;
      align-items: center;
      justify-content: center;
      background: rgb(0 0 0 / 0.4);
      backdrop-filter: blur(4px);
      z-index: 20;
    }
    .pill {
      display: flex;
      align-items: center;
      gap: 0.75rem;
      border-radius: 0.5rem;
      border: 1px solid rgb(255 255 255 / 0.1);
      background: rgb(0 0 0 / 0.7);
      padding: 0.75rem 1rem;
      font-size: 0.875rem;
      color: white;
      box-shadow: 0 10px 15px -3px rgb(0 0 0 / 0.1);
    }
    .spinner {
      width: 1rem;
      height: 1rem;
      border: 2px solid rgb(255 255 255 / 0.3);
      border-top-color: white;
      border-radius: 50%;
      animation: _fw-spin 1s linear infinite;
    }
    @keyframes _fw-spin {
      to {
        transform: rotate(360deg);
      }
    }
  `;

  protected render() {
    return html`
      <div class="overlay" role="status" aria-live="polite">
        <div class="pill">
          <div class="spinner"></div>
          <span>Buffering...</span>
        </div>
      </div>
    `;
  }
}

declare global {
  interface HTMLElementTagNameMap {
    "fw-loading-spinner": FwLoadingSpinner;
  }
}
