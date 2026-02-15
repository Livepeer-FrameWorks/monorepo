import { LitElement, html, css, nothing } from "lit";
import { customElement, property } from "lit/decorators.js";
import { closeIcon } from "../icons/index.js";

@customElement("fw-toast")
export class FwToast extends LitElement {
  @property({ type: String }) message = "";

  static styles = css`
    :host {
      display: contents;
    }
    .toast {
      display: flex;
      align-items: center;
      gap: 0.5rem;
      border-radius: 0.5rem;
      border: 1px solid rgb(255 255 255 / 0.1);
      background: rgb(0 0 0 / 0.8);
      padding: 0.5rem 1rem;
      font-size: 0.875rem;
      color: white;
      box-shadow: 0 10px 15px -3px rgb(0 0 0 / 0.1);
      backdrop-filter: blur(4px);
      animation: _fw-slide-up 200ms ease-out;
    }
    @keyframes _fw-slide-up {
      from {
        opacity: 0;
        transform: translateY(8px);
      }
      to {
        opacity: 1;
        transform: translateY(0);
      }
    }
    button {
      margin-left: 0.125rem;
      color: rgb(255 255 255 / 0.6);
      background: none;
      border: none;
      cursor: pointer;
      padding: 0;
      display: flex;
    }
    button:hover {
      color: white;
    }
  `;

  protected render() {
    if (!this.message) return nothing;
    return html`
      <div class="toast">
        <span>${this.message}</span>
        <button type="button" @click=${this._dismiss} aria-label="Dismiss">${closeIcon()}</button>
      </div>
    `;
  }

  private _dismiss() {
    this.dispatchEvent(new CustomEvent("fw-dismiss", { bubbles: true, composed: true }));
  }
}

declare global {
  interface HTMLElementTagNameMap {
    "fw-toast": FwToast;
  }
}
