import { LitElement, css, html, nothing } from "lit";
import { customElement, property } from "lit/decorators.js";
import type { StreamStatus } from "@livepeer-frameworks/player-core";
import { createTranslator, type TranslateFn } from "@livepeer-frameworks/player-core";
import { sharedStyles } from "../styles/shared-styles.js";

@customElement("fw-stream-state-overlay")
export class FwStreamStateOverlay extends LitElement {
  @property({ type: String }) status: StreamStatus = "OFFLINE";
  @property({ type: String }) message = "";
  @property({ type: Number }) percentage?: number;
  @property({ type: Boolean }) visible = true;
  @property({ type: Boolean, attribute: "retry-enabled" }) retryEnabled = false;
  @property({ attribute: false }) onRetry?: () => void;
  @property({ attribute: false }) translator?: TranslateFn;

  private _defaultTranslator: TranslateFn = createTranslator({ locale: "en" });

  private get _t(): TranslateFn {
    return this.translator ?? this._defaultTranslator;
  }

  static styles = [
    sharedStyles,
    css`
      :host {
        display: contents;
      }

      .overlay-backdrop {
        position: absolute;
        inset: 0;
        z-index: 20;
        display: flex;
        align-items: center;
        justify-content: center;
        background-color: hsl(var(--tn-bg-dark, 235 21% 11%) / 0.8);
        backdrop-filter: blur(4px);
      }

      .slab {
        width: 280px;
        max-width: 90%;
        background-color: hsl(var(--tn-bg, 233 23% 17%) / 0.95);
        border: 1px solid hsl(var(--tn-fg-gutter, 233 23% 25%) / 0.3);
      }

      .slab-header {
        display: flex;
        align-items: center;
        gap: 0.5rem;
        padding: 0.75rem 1rem;
        border-bottom: 1px solid hsl(var(--tn-fg-gutter, 233 23% 25%) / 0.3);
        font-size: 0.75rem;
        font-weight: 600;
        text-transform: uppercase;
        letter-spacing: 0.05em;
        color: hsl(var(--tn-fg-dark, 233 23% 60%));
      }

      .slab-body {
        padding: 1rem;
      }

      .slab-message {
        font-size: 0.875rem;
        color: hsl(var(--tn-fg, 233 23% 75%));
      }

      .progress-wrap {
        margin-top: 0.75rem;
      }

      .progress-bar {
        height: 0.375rem;
        width: 100%;
        overflow: hidden;
        background-color: hsl(var(--tn-bg-visual, 233 23% 20%));
      }

      .progress-fill {
        height: 100%;
        transition: width 0.3s ease;
        background-color: hsl(var(--tn-yellow, 40 70% 64%));
      }

      .progress-text {
        margin-top: 0.375rem;
        font-size: 0.75rem;
        font-family: monospace;
        color: hsl(var(--tn-fg-dark, 233 23% 60%));
      }

      .hint {
        margin-top: 0.5rem;
        font-size: 0.75rem;
        color: hsl(var(--tn-fg-dark, 233 23% 60%));
      }

      .polling-indicator {
        display: flex;
        align-items: center;
        gap: 0.5rem;
        margin-top: 0.75rem;
        font-size: 0.75rem;
        color: hsl(var(--tn-fg-dark, 233 23% 60%));
      }

      .polling-dot {
        width: 0.375rem;
        height: 0.375rem;
        background-color: hsl(var(--tn-cyan, 192 78% 73%));
        animation: pulse 2s cubic-bezier(0.4, 0, 0.6, 1) infinite;
      }

      .slab-actions {
        border-top: 1px solid hsl(var(--tn-fg-gutter, 233 23% 25%) / 0.3);
      }

      .btn-flush {
        width: 100%;
        padding: 0.625rem 1rem;
        background: none;
        border: none;
        cursor: pointer;
        font-size: 0.75rem;
        font-weight: 500;
        text-transform: uppercase;
        letter-spacing: 0.05em;
        color: hsl(var(--tn-blue, 217 89% 71%));
        transition: background-color 0.15s;
      }

      .btn-flush:hover {
        background-color: hsl(var(--tn-bg-visual, 233 23% 20%) / 0.5);
      }

      .icon {
        width: 1.25rem;
        height: 1.25rem;
      }

      .icon-online {
        color: hsl(var(--tn-green, 115 54% 57%));
      }

      .icon-offline {
        color: hsl(var(--tn-red, 355 68% 65%));
      }

      .icon-warning {
        color: hsl(var(--tn-yellow, 40 70% 64%));
      }

      .animate-spin {
        animation: spin 1s linear infinite;
      }

      @keyframes spin {
        to {
          transform: rotate(360deg);
        }
      }

      @keyframes pulse {
        0%,
        100% {
          opacity: 1;
        }
        50% {
          opacity: 0.5;
        }
      }
    `,
  ];

  private _getStatusLabel(status: StreamStatus): string {
    switch (status) {
      case "ONLINE":
        return "ONLINE";
      case "OFFLINE":
        return "OFFLINE";
      case "INITIALIZING":
        return "INITIALIZING";
      case "BOOTING":
        return "STARTING";
      case "WAITING_FOR_DATA":
        return "WAITING";
      case "SHUTTING_DOWN":
        return "ENDING";
      case "ERROR":
        return "ERROR";
      case "INVALID":
        return "INVALID";
      default:
        return "STATUS";
    }
  }

  private _renderStatusIcon(status: StreamStatus) {
    if (status === "OFFLINE") {
      return html`<svg
        class="icon icon-offline"
        fill="none"
        viewBox="0 0 24 24"
        stroke="currentColor"
      >
        <path
          stroke-linecap="round"
          stroke-linejoin="round"
          stroke-width="2"
          d="M18.364 5.636a9 9 0 010 12.728m0 0l-2.829-2.829m2.829 2.829L21 21M15.536 8.464a5 5 0 010 7.072m0 0l-2.829-2.829m-4.243 2.829a4.978 4.978 0 01-1.414-2.83m-1.414 5.658a9 9 0 01-2.167-9.238m7.824 2.167a1 1 0 111.414 1.414m-1.414-1.414L3 3m8.293 8.293l1.414 1.414"
        ></path>
      </svg>`;
    }

    if (status === "INITIALIZING" || status === "BOOTING" || status === "WAITING_FOR_DATA") {
      return html`<svg class="icon icon-warning animate-spin" fill="none" viewBox="0 0 24 24">
        <circle
          class="opacity-25"
          cx="12"
          cy="12"
          r="10"
          stroke="currentColor"
          stroke-width="4"
        ></circle>
        <path
          class="opacity-75"
          fill="currentColor"
          d="M4 12a8 8 0 018-8V0C5.373 0 0 5.373 0 12h4zm2 5.291A7.962 7.962 0 014 12H0c0 3.042 1.135 5.824 3 7.938l3-2.647z"
        ></path>
      </svg>`;
    }

    if (status === "SHUTTING_DOWN") {
      return html`<svg
        class="icon icon-warning"
        fill="none"
        viewBox="0 0 24 24"
        stroke="currentColor"
      >
        <path
          stroke-linecap="round"
          stroke-linejoin="round"
          stroke-width="2"
          d="M13 10V3L4 14h7v7l9-11h-7z"
        ></path>
      </svg>`;
    }

    return html`<svg
      class="icon icon-offline"
      fill="none"
      viewBox="0 0 24 24"
      stroke="currentColor"
    >
      <path
        stroke-linecap="round"
        stroke-linejoin="round"
        stroke-width="2"
        d="M12 9v2m0 4h.01m-6.938 4h13.856c1.54 0 2.502-1.667 1.732-3L13.732 4c-.77-1.333-2.694-1.333-3.464 0L3.34 16c-.77 1.333.192 3 1.732 3z"
      ></path>
    </svg>`;
  }

  private _handleRetry = () => {
    if (this.onRetry) {
      this.onRetry();
      return;
    }
    this.dispatchEvent(new CustomEvent("fw-retry", { bubbles: true, composed: true }));
  };

  protected render() {
    if (!this.visible || this.status === "ONLINE") {
      return nothing;
    }

    const showRetry =
      (this.status === "ERROR" || this.status === "INVALID" || this.status === "OFFLINE") &&
      (this.retryEnabled || typeof this.onRetry === "function");
    const showProgress = this.status === "INITIALIZING" && this.percentage !== undefined;
    const progressWidth = `${Math.min(100, Math.max(0, this.percentage ?? 0))}%`;

    return html`
      <div class="overlay-backdrop" role="status" aria-live="polite">
        <div class="slab">
          <div class="slab-header">
            ${this._renderStatusIcon(this.status)}
            <span>${this._getStatusLabel(this.status)}</span>
          </div>
          <div class="slab-body">
            <p class="slab-message">${this.message}</p>
            ${showProgress
              ? html`
                  <div class="progress-wrap">
                    <div class="progress-bar">
                      <div class="progress-fill" style="width:${progressWidth};"></div>
                    </div>
                    <p class="progress-text">${Math.round(this.percentage ?? 0)}%</p>
                  </div>
                `
              : nothing}
            ${this.status === "OFFLINE"
              ? html`<p class="hint">${this._t("broadcasterGoLive")}</p>`
              : nothing}
            ${this.status === "BOOTING" || this.status === "WAITING_FOR_DATA"
              ? html`<p class="hint">${this._t("streamPreparing")}</p>`
              : nothing}
            ${!showRetry
              ? html`<div class="polling-indicator">
                  <span class="polling-dot"></span>
                  <span>${this._t("checkingStatus")}</span>
                </div>`
              : nothing}
          </div>
          ${showRetry
            ? html`<div class="slab-actions">
                <button
                  type="button"
                  class="btn-flush"
                  @click=${this._handleRetry}
                  aria-label=${this._t("retryConnection")}
                >
                  ${this._t("retryConnection")}
                </button>
              </div>`
            : nothing}
        </div>
      </div>
    `;
  }
}

declare global {
  interface HTMLElementTagNameMap {
    "fw-stream-state-overlay": FwStreamStateOverlay;
  }
}
