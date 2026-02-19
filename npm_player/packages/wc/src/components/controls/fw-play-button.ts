import { LitElement, html, css } from "lit";
import { customElement, state } from "lit/decorators.js";
import { createTranslator, type TranslateFn } from "@livepeer-frameworks/player-core";

@customElement("fw-play-button")
export class FwPlayButton extends LitElement {
  @state() private isPlaying = false;
  private _player: any = null;
  private _cleanup: (() => void) | null = null;

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
    if (!this._player) return;
    const pc = this._player.pc;
    if (!pc) return;

    this.isPlaying = pc.s.isPlaying;
    const handler = () => {
      this.isPlaying = pc.s.isPlaying;
    };
    this._player.addEventListener("fw-state-change", handler);
    this._player.addEventListener("fw-ready", handler);
    this._cleanup = () => {
      this._player?.removeEventListener("fw-state-change", handler);
      this._player?.removeEventListener("fw-ready", handler);
    };
  }

  disconnectedCallback() {
    super.disconnectedCallback();
    this._cleanup?.();
    this._cleanup = null;
  }

  private handleClick() {
    this._player?.pc?.togglePlay();
  }

  render() {
    return html`<button
      type="button"
      class="fw-btn-flush"
      aria-label=${this.isPlaying ? this._t("pause") : this._t("play")}
      aria-pressed=${this.isPlaying}
      title=${this.isPlaying ? this._t("pause") : this._t("play")}
      @click=${this.handleClick}
    >
      ${this.isPlaying ? "\u23F8" : "\u25B6"}
    </button>`;
  }
}

declare global {
  interface HTMLElementTagNameMap {
    "fw-play-button": FwPlayButton;
  }
}
