import { LitElement, html, css } from "lit";
import { customElement, property } from "lit/decorators.js";
import { createTranslator, type TranslateFn } from "@livepeer-frameworks/player-core";

@customElement("fw-skip-button")
export class FwSkipButton extends LitElement {
  @property({ type: String }) direction: "back" | "forward" = "forward";
  @property({ type: Number }) seconds = 10;
  private _player: any = null;

  private get _t(): TranslateFn {
    return this._player?.pc?.t ?? createTranslator({ locale: "en" });
  }

  static styles = css`
    :host {
      display: inline-flex;
      align-items: center;
    }
  `;

  private _resolvePlayer(): HTMLElement | null {
    const forId = this.getAttribute("for");
    if (forId) return document.getElementById(forId);
    return this.closest("fw-player");
  }

  connectedCallback() {
    super.connectedCallback();
    this._player = this._resolvePlayer();
  }

  private handleClick() {
    const delta = (this.direction === "back" ? -this.seconds : this.seconds) * 1000;
    this._player?.pc?.seekBy(delta);
  }

  render() {
    const label = this.direction === "back" ? this._t("skipBackward") : this._t("skipForward");
    const icon = this.direction === "back" ? "\u23EA" : "\u23E9";
    const shortcut = this.direction === "back" ? "j" : "l";

    return html`<button
      type="button"
      class="fw-btn-flush"
      aria-label=${label}
      title="${label} (${shortcut})"
      @click=${this.handleClick}
    >
      ${icon} ${this.seconds}s
    </button>`;
  }
}

declare global {
  interface HTMLElementTagNameMap {
    "fw-skip-button": FwSkipButton;
  }
}
