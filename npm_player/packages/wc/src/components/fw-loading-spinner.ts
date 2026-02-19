import { LitElement, html, css } from "lit";
import { customElement, property } from "lit/decorators.js";
import { createTranslator, type TranslateFn } from "@livepeer-frameworks/player-core";

@customElement("fw-loading-spinner")
export class FwLoadingSpinner extends LitElement {
  @property({ attribute: false }) translator?: TranslateFn;

  private _defaultTranslator: TranslateFn = createTranslator({ locale: "en" });

  private get _t(): TranslateFn {
    return this.translator ?? this._defaultTranslator;
  }

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
      background: hsl(var(--fw-surface-deep, 235 21% 11%) / 0.85);
      backdrop-filter: blur(4px);
      z-index: 20;
    }
    .pill {
      display: flex;
      align-items: center;
      gap: 0.75rem;
      border-radius: 0.5rem;
      border: 1px solid hsl(var(--fw-text, 229 73% 86%) / 0.1);
      background: hsl(var(--fw-surface-deep, 235 21% 11%) / 0.9);
      padding: 0.75rem 1rem;
      font-size: 0.875rem;
      color: hsl(var(--fw-text, 229 73% 86%));
      box-shadow: 0 10px 15px -3px hsl(var(--fw-shadow-color, 0 0% 0%) / 0.1);
    }
    .spinner {
      width: 1rem;
      height: 1rem;
      border: 2px solid hsl(var(--fw-text-faint, 228 15% 45%) / 0.3);
      border-top-color: hsl(var(--fw-accent, 218 79% 73%));
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
          <span>${this._t("buffering")}</span>
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
