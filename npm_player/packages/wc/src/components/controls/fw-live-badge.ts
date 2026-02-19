import { LitElement, html, css } from "lit";
import { customElement, state } from "lit/decorators.js";
import { createTranslator, type TranslateFn } from "@livepeer-frameworks/player-core";

@customElement("fw-live-badge")
export class FwLiveBadge extends LitElement {
  @state() private isLive = false;
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
    button {
      display: inline-flex;
      align-items: center;
      gap: 0.25rem;
      border: none;
      background: none;
      cursor: pointer;
      padding: 0.125rem 0.375rem;
      border-radius: 0.25rem;
      font-size: 0.625rem;
      font-weight: 700;
      text-transform: uppercase;
      letter-spacing: 0.05em;
      color: inherit;
    }
    .dot {
      width: 0.375rem;
      height: 0.375rem;
      border-radius: 50%;
      background: #ef4444;
    }
    :host([live]) .dot {
      animation: pulse 1.5s ease-in-out infinite;
    }
    @keyframes pulse {
      0%,
      100% {
        opacity: 1;
      }
      50% {
        opacity: 0.4;
      }
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

    this.syncLive();
    const handler = () => this.syncLive();
    this._player.addEventListener("fw-state-change", handler);
    this._player.addEventListener("fw-stream-state", handler);
    this._player.addEventListener("fw-ready", handler);
    this._cleanup = () => {
      this._player?.removeEventListener("fw-state-change", handler);
      this._player?.removeEventListener("fw-stream-state", handler);
      this._player?.removeEventListener("fw-ready", handler);
    };
  }

  disconnectedCallback() {
    super.disconnectedCallback();
    this._cleanup?.();
    this._cleanup = null;
  }

  private syncLive() {
    const live = this._player?.pc?.s?.isEffectivelyLive ?? false;
    this.isLive = live;
    this.toggleAttribute("live", live);
  }

  private handleClick() {
    this._player?.pc?.jumpToLive();
  }

  render() {
    if (!this.isLive) return html``;

    return html`<button type="button" aria-label=${this._t("live")} @click=${this.handleClick}>
      <span class="dot"></span> ${this._t("live").toUpperCase()}
    </button>`;
  }
}

declare global {
  interface HTMLElementTagNameMap {
    "fw-live-badge": FwLiveBadge;
  }
}
