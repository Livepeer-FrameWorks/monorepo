import { LitElement, html, css } from "lit";
import { customElement, state } from "lit/decorators.js";
import { createTranslator, type TranslateFn } from "@livepeer-frameworks/player-core";

@customElement("fw-fullscreen-button")
export class FwFullscreenButton extends LitElement {
  @state() private isFullscreen = false;
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

    this.isFullscreen = pc.s.isFullscreen;
    const handler = () => {
      this.isFullscreen = this._player.pc.s.isFullscreen;
    };
    this._player.addEventListener("fw-fullscreen-change", handler);
    this._player.addEventListener("fw-ready", handler);
    this._cleanup = () => {
      this._player?.removeEventListener("fw-fullscreen-change", handler);
      this._player?.removeEventListener("fw-ready", handler);
    };
  }

  disconnectedCallback() {
    super.disconnectedCallback();
    this._cleanup?.();
    this._cleanup = null;
  }

  private handleClick() {
    this._player?.pc?.toggleFullscreen();
  }

  render() {
    return html`<button
      type="button"
      class="fw-btn-flush"
      aria-label=${this.isFullscreen ? this._t("exitFullscreen") : this._t("fullscreen")}
      aria-pressed=${this.isFullscreen}
      title=${this.isFullscreen ? this._t("exitFullscreen") : this._t("fullscreen")}
      @click=${this.handleClick}
    >
      ${this.isFullscreen ? "\u2716" : "\u26F6"}
    </button>`;
  }
}

declare global {
  interface HTMLElementTagNameMap {
    "fw-fullscreen-button": FwFullscreenButton;
  }
}
