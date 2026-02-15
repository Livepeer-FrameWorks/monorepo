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

const DEFAULT_TRANSITION: TransitionConfig = {
  type: "fade",
  durationMs: 500,
  easing: "ease-in-out",
};

@customElement("fw-sc-scene-switcher")
export class FwScSceneSwitcher extends LitElement {
  @property({ attribute: false }) scenes: Scene[] = [];
  @property({ type: String, attribute: "active-scene-id" }) activeSceneId: string | null = null;
  @property({ type: Boolean, attribute: "show-transition-controls" }) showTransitionControls = true;
  @property({ attribute: false }) transitionConfig: TransitionConfig = DEFAULT_TRANSITION;
  @property({ attribute: false }) onSceneSelect?: (sceneId: string) => void;
  @property({ attribute: false }) onSceneCreate?: () => void;
  @property({ attribute: false }) onSceneDelete?: (sceneId: string) => void;
  @property({ attribute: false }) onTransitionTo?: (
    sceneId: string,
    transition: TransitionConfig
  ) => Promise<void>;

  @state() private _selectedTransition: TransitionType = DEFAULT_TRANSITION.type;
  @state() private _transitionDuration = DEFAULT_TRANSITION.durationMs;
  @state() private _isTransitioning = false;
  @state() private _transitionInitialized = false;

  static styles = [
    sharedStyles,
    utilityStyles,
    css`
      :host {
        display: block;
      }
    `,
  ];

  connectedCallback() {
    super.connectedCallback();
    if (!this._transitionInitialized) {
      this._selectedTransition = this.transitionConfig.type;
      this._transitionDuration = this.transitionConfig.durationMs;
      this._transitionInitialized = true;
    }
  }

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
                ${this.onSceneDelete && this.scenes.length > 1 && scene.id !== this.activeSceneId
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
          ${this.onSceneCreate
            ? html`
                <button
                  class="fw-sc-scene-add"
                  @click=${() => this._handleCreate()}
                  title="Create new scene"
                >
                  +
                </button>
              `
            : nothing}
        </div>
      </div>
    `;
  }

  private async _handleSceneClick(sceneId: string) {
    if (sceneId === this.activeSceneId || this._isTransitioning) return;

    const transition: TransitionConfig = {
      type: this._selectedTransition,
      durationMs: this._transitionDuration,
      easing: this.transitionConfig.easing,
    };

    if (this.onTransitionTo) {
      this._isTransitioning = true;
      try {
        await this.onTransitionTo(sceneId, transition);
      } finally {
        this._isTransitioning = false;
      }
      return;
    }

    this.onSceneSelect?.(sceneId);
  }

  private _handleCreate() {
    this.onSceneCreate?.();
  }

  private _handleDelete(sceneId: string) {
    if (this.scenes.length <= 1) return;
    this.onSceneDelete?.(sceneId);
  }
}

declare global {
  interface HTMLElementTagNameMap {
    "fw-sc-scene-switcher": FwScSceneSwitcher;
  }
}
