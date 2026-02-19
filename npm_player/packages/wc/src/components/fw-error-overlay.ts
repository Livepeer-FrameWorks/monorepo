import { LitElement, html, css } from "lit";
import { customElement, property } from "lit/decorators.js";
import { classMap } from "lit/directives/class-map.js";
import { sharedStyles } from "../styles/shared-styles.js";
import { utilityStyles } from "../styles/utility-styles.js";
import { closeIcon } from "../icons/index.js";
import { createTranslator, type TranslateFn } from "@livepeer-frameworks/player-core";

@customElement("fw-error-overlay")
export class FwErrorOverlay extends LitElement {
  @property({ type: String }) error: string | null = null;
  @property({ type: Boolean, attribute: "is-passive" }) isPassive = false;
  @property({ attribute: false }) translator?: TranslateFn;

  private _defaultTranslator: TranslateFn = createTranslator({ locale: "en" });

  private get _t(): TranslateFn {
    return this.translator ?? this._defaultTranslator;
  }

  static styles = [
    sharedStyles,
    utilityStyles,
    css`
      :host {
        display: contents;
      }
    `,
  ];

  protected render() {
    return html`
      <div
        role="alert"
        aria-live="assertive"
        class=${classMap({
          "fw-error-overlay": true,
          "fw-error-overlay--passive": this.isPassive,
          "fw-error-overlay--fullscreen": !this.isPassive,
        })}
      >
        <div
          class=${classMap({
            "fw-error-popup": true,
            "fw-error-popup--passive": this.isPassive,
            "fw-error-popup--fullscreen": !this.isPassive,
          })}
        >
          <div
            class=${classMap({
              "fw-error-header": true,
              "fw-error-header--warning": this.isPassive,
              "fw-error-header--error": !this.isPassive,
            })}
          >
            <span
              class=${classMap({
                "fw-error-title": true,
                "fw-error-title--warning": this.isPassive,
                "fw-error-title--error": !this.isPassive,
              })}
              >${this.isPassive ? this._t("warning") : this._t("error")}</span
            >
            <button
              type="button"
              class="fw-error-close"
              @click=${this._clearError}
              aria-label=${this._t("dismiss")}
            >
              ${closeIcon()}
            </button>
          </div>
          <div class="fw-error-body">
            <p class="fw-error-message">${this.error || this._t("playbackIssue")}</p>
          </div>
          <div class="fw-error-actions">
            <button
              type="button"
              class="fw-error-btn"
              aria-label=${this._t("retry")}
              @click=${this._retry}
            >
              ${this._t("retry")}
            </button>
          </div>
        </div>
      </div>
    `;
  }

  private _clearError() {
    this.dispatchEvent(new CustomEvent("fw-clear-error", { bubbles: true, composed: true }));
  }

  private _retry() {
    this.dispatchEvent(new CustomEvent("fw-retry", { bubbles: true, composed: true }));
  }
}

declare global {
  interface HTMLElementTagNameMap {
    "fw-error-overlay": FwErrorOverlay;
  }
}
