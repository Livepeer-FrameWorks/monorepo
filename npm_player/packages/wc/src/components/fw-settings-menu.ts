/**
 * <fw-settings-menu> — Quality, speed, and captions settings popup.
 */
import { LitElement, html, css, nothing } from "lit";
import { customElement, property, state } from "lit/decorators.js";
import { classMap } from "lit/directives/class-map.js";
import { sharedStyles } from "../styles/shared-styles.js";
import { utilityStyles } from "../styles/utility-styles.js";
import type { PlayerControllerHost } from "../controllers/player-controller-host.js";

@customElement("fw-settings-menu")
export class FwSettingsMenu extends LitElement {
  @property({ attribute: false }) pc!: PlayerControllerHost;
  @property({ type: Boolean }) open = false;

  static styles = [
    sharedStyles,
    utilityStyles,
    css`
      :host {
        display: contents;
      }
      .menu {
        position: absolute;
        bottom: 100%;
        right: 0;
        margin-bottom: 0.5rem;
        min-width: 200px;
        border-radius: 0.5rem;
        border: 1px solid rgb(255 255 255 / 0.1);
        background: rgb(0 0 0 / 0.9);
        backdrop-filter: blur(8px);
        padding: 0.5rem;
        z-index: 50;
        box-shadow: 0 10px 15px -3px rgb(0 0 0 / 0.3);
      }
      .section {
        padding: 0.25rem 0;
      }
      .section + .section {
        border-top: 1px solid rgb(255 255 255 / 0.1);
      }
      .label {
        padding: 0.25rem 0.5rem;
        font-size: 0.6875rem;
        font-weight: 600;
        text-transform: uppercase;
        letter-spacing: 0.05em;
        color: rgb(255 255 255 / 0.4);
      }
      .option {
        display: flex;
        align-items: center;
        width: 100%;
        padding: 0.375rem 0.5rem;
        border: none;
        background: none;
        color: rgb(255 255 255 / 0.7);
        font-size: 0.8125rem;
        cursor: pointer;
        border-radius: 0.25rem;
        text-align: left;
      }
      .option:hover {
        background: rgb(255 255 255 / 0.1);
        color: white;
      }
      .option--active {
        color: hsl(var(--tn-blue, 217 89% 61%));
      }
      .dot {
        width: 6px;
        height: 6px;
        border-radius: 50%;
        background: hsl(var(--tn-blue, 217 89% 61%));
        margin-right: 0.5rem;
      }
      .dot--hidden {
        visibility: hidden;
      }
    `,
  ];

  protected render() {
    if (!this.open) return nothing;
    const { qualities } = this.pc.s;
    const speeds = [0.25, 0.5, 0.75, 1, 1.25, 1.5, 2];

    return html`
      <div class="menu fw-settings-menu">
        ${qualities.length > 0
          ? html`
              <div class="section">
                <div class="label">Quality</div>
                ${qualities.map(
                  (q) => html`
                    <button
                      class=${classMap({ option: true, "option--active": !!q.active })}
                      @click=${() => this.pc.selectQuality(q.id)}
                    >
                      <div class=${classMap({ dot: true, "dot--hidden": !q.active })}></div>
                      ${q.label}
                    </button>
                  `
                )}
              </div>
            `
          : nothing}
        <div class="section">
          <div class="label">Speed</div>
          ${speeds.map(
            (s) => html`
              <button class="option" @click=${() => this.pc.getController()?.setPlaybackRate(s)}>
                ${s === 1 ? "Normal" : `${s}x`}
              </button>
            `
          )}
        </div>
      </div>
    `;
  }
}

declare global {
  interface HTMLElementTagNameMap {
    "fw-settings-menu": FwSettingsMenu;
  }
}
