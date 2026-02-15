/**
 * <fw-sc-scene-switcher> — Horizontal scene selector with transition controls.
 * Port of SceneSwitcher.tsx from streamcrafter-react.
 */
import { LitElement, html, css, nothing } from "lit";
import { customElement, property, state } from "lit/decorators.js";
import { classMap } from "lit/directives/class-map.js";
import { sharedStyles } from "../styles/shared-styles.js";
import { utilityStyles } from "../styles/utility-styles.js";
import type {
  Scene,
  TransitionConfig,
  TransitionType,
} from "@livepeer-frameworks/streamcrafter-core";

@customElement("fw-sc-scene-switcher")
export class FwScSceneSwitcher extends LitElement {
  @property({ attribute: false }) scenes: Scene[] = [];
  @property({ type: String, attribute: "active-scene-id" }) activeSceneId: string | null = null;
  @property({ type: Boolean, attribute: "show-transition-controls" }) showTransitionControls = true;

  @state() private _selectedTransition: TransitionType = "fade";
  @state() private _transitionDuration = 500;
  @state() private _isTransitioning = false;

  static styles = [
    sharedStyles,
    utilityStyles,
    css`
      :host {
        display: block;
      }
    `,
  ];

  protected render() {
    return html`
      <div class="fw-sc-scene-switcher">
        <div class="fw-sc-scene-switcher-header">
          <span class="fw-sc-scene-switcher-title">Scenes</span>
          ${this.showTransitionControls
            ? html`
                <div class="fw-sc-transition-controls">
                  <select
                    class="fw-sc-transition-select"
                    .value=${this._selectedTransition}
                    @change=${(e: Event) => {
                      this._selectedTransition = (e.target as HTMLSelectElement)
                        .value as TransitionType;
                    }}
                  >
                    <option value="cut">Cut</option>
                    <option value="fade">Fade</option>
                    <option value="slide-left">Slide Left</option>
                    <option value="slide-right">Slide Right</option>
                    <option value="slide-up">Slide Up</option>
                    <option value="slide-down">Slide Down</option>
                  </select>
                  <input
                    type="number"
                    class="fw-sc-transition-duration"
                    .value=${String(this._transitionDuration)}
                    @change=${(e: Event) => {
                      this._transitionDuration = Number((e.target as HTMLInputElement).value);
                    }}
                    min="0"
                    max="3000"
                    step="100"
                    title="Transition duration (ms)"
                  />
                  <span class="fw-sc-transition-unit">ms</span>
                </div>
              `
            : nothing}
        </div>

        <div class="fw-sc-scene-list">
          ${this.scenes.map(
            (scene) => html`
              <div
                class=${classMap({
                  "fw-sc-scene-item": true,
                  "fw-sc-scene-item--active": scene.id === this.activeSceneId,
                  "fw-sc-scene-item--transitioning": this._isTransitioning,
                })}
                @click=${() => this._handleSceneClick(scene.id)}
                style="background-color:${scene.backgroundColor}"
              >
                <span class="fw-sc-scene-name">${scene.name}</span>
                <span class="fw-sc-scene-layer-count">${scene.layers.length} layers</span>
                ${this.scenes.length > 1 && scene.id !== this.activeSceneId
                  ? html`
                      <button
                        class="fw-sc-scene-delete"
                        @click=${(e: Event) => {
                          e.stopPropagation();
                          this._handleDelete(scene.id);
                        }}
                        title="Delete scene"
                      >
                        ×
                      </button>
                    `
                  : nothing}
              </div>
            `
          )}

          <button
            class="fw-sc-scene-add"
            @click=${() =>
              this.dispatchEvent(
                new CustomEvent("fw-sc-scene-create", { bubbles: true, composed: true })
              )}
            title="Create new scene"
          >
            +
          </button>
        </div>
      </div>
    `;
  }

  private async _handleSceneClick(sceneId: string) {
    if (sceneId === this.activeSceneId || this._isTransitioning) return;
    this._isTransitioning = true;
    try {
      this.dispatchEvent(
        new CustomEvent("fw-sc-scene-select", {
          detail: {
            sceneId,
            transition: {
              type: this._selectedTransition,
              durationMs: this._transitionDuration,
              easing: "ease-in-out" as const,
            } satisfies TransitionConfig,
          },
          bubbles: true,
          composed: true,
        })
      );
    } finally {
      this._isTransitioning = false;
    }
  }

  private _handleDelete(sceneId: string) {
    if (this.scenes.length <= 1) return;
    this.dispatchEvent(
      new CustomEvent("fw-sc-scene-delete", {
        detail: { sceneId },
        bubbles: true,
        composed: true,
      })
    );
  }
}

declare global {
  interface HTMLElementTagNameMap {
    "fw-sc-scene-switcher": FwScSceneSwitcher;
  }
}
