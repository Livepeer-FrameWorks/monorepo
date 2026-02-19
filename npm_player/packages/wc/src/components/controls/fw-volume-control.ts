import { LitElement, html, css } from "lit";
import { customElement, state } from "lit/decorators.js";
import { createTranslator, type TranslateFn } from "@livepeer-frameworks/player-core";

@customElement("fw-volume-button")
export class FwVolumeControl extends LitElement {
  @state() private isMuted = false;
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

    this.isMuted = pc.s.isMuted;
    const handler = () => {
      this.isMuted = this._player.pc.s.isMuted;
    };
    this._player.addEventListener("fw-volume-change", handler);
    this._player.addEventListener("fw-ready", handler);
    this._cleanup = () => {
      this._player?.removeEventListener("fw-volume-change", handler);
      this._player?.removeEventListener("fw-ready", handler);
    };
  }

  disconnectedCallback() {
    super.disconnectedCallback();
    this._cleanup?.();
    this._cleanup = null;
  }

  private handleClick() {
    this._player?.pc?.toggleMute();
  }

  render() {
    return html`<button
      type="button"
      class="fw-btn-flush"
      aria-label=${this.isMuted ? this._t("unmute") : this._t("mute")}
      aria-pressed=${this.isMuted}
      title=${this.isMuted ? this._t("unmute") : this._t("mute")}
      @click=${this.handleClick}
    >
      ${this.isMuted ? "\uD83D\uDD07" : "\uD83D\uDD0A"}
    </button>`;
  }
}

declare global {
  interface HTMLElementTagNameMap {
    "fw-volume-button": FwVolumeControl;
  }
}
