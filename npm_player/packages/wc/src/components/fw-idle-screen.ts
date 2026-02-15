import { LitElement, html, css, nothing } from "lit";
import { customElement, property } from "lit/decorators.js";
import { classMap } from "lit/directives/class-map.js";
import { sharedStyles } from "../styles/shared-styles.js";
import { utilityStyles } from "../styles/utility-styles.js";

@customElement("fw-idle-screen")
export class FwIdleScreen extends LitElement {
  @property({ type: String }) status?: string;
  @property({ type: String }) message?: string;
  @property({ type: Number }) percentage?: number;

  static styles = [
    sharedStyles,
    utilityStyles,
    css`
      :host {
        display: contents;
      }
      .idle {
        position: absolute;
        inset: 0;
        display: flex;
        align-items: center;
        justify-content: center;
        z-index: 15;
        background: linear-gradient(135deg, rgb(15 23 42), rgb(2 6 23), rgb(15 23 42));
        overflow: hidden;
      }
      .card {
        position: relative;
        z-index: 10;
        max-width: 280px;
        width: 100%;
        text-align: center;
      }
      .status-icon {
        margin: 0 auto 0.75rem;
        width: 2.5rem;
        height: 2.5rem;
        display: flex;
        align-items: center;
        justify-content: center;
      }
      .spinner {
        width: 1.5rem;
        height: 1.5rem;
        border: 2px solid rgb(255 255 255 / 0.2);
        border-top-color: hsl(var(--tn-blue, 217 89% 61%));
        border-radius: 50%;
        animation: _fw-spin 1s linear infinite;
      }
      @keyframes _fw-spin {
        to {
          transform: rotate(360deg);
        }
      }
      .offline-dot {
        width: 0.75rem;
        height: 0.75rem;
        border-radius: 50%;
        background: hsl(var(--tn-red, 348 74% 64%));
        animation: _fw-blink 2s ease-in-out infinite;
      }
      @keyframes _fw-blink {
        0%,
        100% {
          opacity: 1;
        }
        50% {
          opacity: 0.3;
        }
      }
      .label {
        font-size: 0.6875rem;
        font-weight: 600;
        text-transform: uppercase;
        letter-spacing: 0.05em;
        color: rgb(255 255 255 / 0.5);
        margin-bottom: 0.5rem;
      }
      .msg {
        font-size: 0.8125rem;
        color: rgb(255 255 255 / 0.7);
        margin-bottom: 0.75rem;
      }
      .progress-wrap {
        width: 100%;
        height: 0.25rem;
        background: rgb(255 255 255 / 0.1);
        border-radius: 2px;
        overflow: hidden;
        margin-bottom: 0.5rem;
      }
      .progress-bar {
        height: 100%;
        background: hsl(var(--tn-blue, 217 89% 61%));
        border-radius: 2px;
        transition: width 300ms ease;
      }
      .pct {
        font-size: 0.6875rem;
        color: rgb(255 255 255 / 0.4);
        font-variant-numeric: tabular-nums;
      }
      .particles {
        position: absolute;
        inset: 0;
        overflow: hidden;
        pointer-events: none;
      }
      .particle {
        position: absolute;
        width: 4px;
        height: 4px;
        border-radius: 50%;
        opacity: 0.3;
        animation: _fw-float var(--dur, 8s) ease-in-out infinite;
      }
      @keyframes _fw-float {
        0%,
        100% {
          transform: translateY(0) translateX(0);
        }
        25% {
          transform: translateY(-20px) translateX(10px);
        }
        50% {
          transform: translateY(-10px) translateX(-5px);
        }
        75% {
          transform: translateY(-30px) translateX(15px);
        }
      }
    `,
  ];

  protected render() {
    const isOffline = this.status === "OFFLINE";
    const isInitializing = this.status === "INITIALIZING" || this.status === "STARTING";

    return html`
      <div class="idle">
        <div class="particles">
          ${[0, 1, 2, 3, 4, 5, 6, 7].map(
            (i) => html`
              <div
                class="particle"
                style="left: ${10 + i * 11}%; top: ${20 + (i % 3) * 25}%; --dur: ${6 +
                i * 0.8}s; background: hsl(${217 + i * 15} 70% 60%); animation-delay: ${i * -1}s;"
              ></div>
            `
          )}
        </div>
        <div class="card">
          <div class="status-icon">
            ${isOffline ? html`<div class="offline-dot"></div>` : html`<div class="spinner"></div>`}
          </div>
          ${this.status ? html`<div class="label">${this.status}</div>` : nothing}
          ${this.message ? html`<div class="msg">${this.message}</div>` : nothing}
          ${isInitializing && this.percentage != null
            ? html`
                <div class="progress-wrap">
                  <div
                    class="progress-bar"
                    style="width: ${Math.min(100, Math.max(0, this.percentage))}%"
                  ></div>
                </div>
                <div class="pct">${Math.round(this.percentage)}%</div>
              `
            : nothing}
        </div>
      </div>
    `;
  }
}

declare global {
  interface HTMLElementTagNameMap {
    "fw-idle-screen": FwIdleScreen;
  }
}
