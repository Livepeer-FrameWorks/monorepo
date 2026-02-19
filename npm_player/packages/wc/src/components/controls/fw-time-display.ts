import { LitElement, html, css } from "lit";
import { customElement, state } from "lit/decorators.js";
import { formatTimeDisplay } from "@livepeer-frameworks/player-core";

@customElement("fw-time-display")
export class FwTimeDisplay extends LitElement {
  @state() private display = "";
  private _player: any = null;
  private _cleanup: (() => void) | null = null;

  static styles = css`
    :host {
      display: inline-flex;
      align-items: center;
      font-variant-numeric: tabular-nums;
    }
    span {
      white-space: nowrap;
      font-size: 0.75rem;
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

    this.updateDisplay();
    const handler = () => this.updateDisplay();
    this._player.addEventListener("fw-time-update", handler);
    this._player.addEventListener("fw-ready", handler);
    this._cleanup = () => {
      this._player?.removeEventListener("fw-time-update", handler);
      this._player?.removeEventListener("fw-ready", handler);
    };
  }

  disconnectedCallback() {
    super.disconnectedCallback();
    this._cleanup?.();
    this._cleanup = null;
  }

  private updateDisplay() {
    const pc = this._player?.pc;
    if (!pc) return;
    const s = pc.s;
    this.display = formatTimeDisplay({
      isLive: s.isEffectivelyLive,
      currentTime: s.currentTime,
      duration: s.duration,
      liveEdge: pc.getLiveEdge?.() ?? s.duration,
      seekableStart: pc.getSeekableStart?.() ?? 0,
    });
  }

  render() {
    return html`<span>${this.display}</span>`;
  }
}

declare global {
  interface HTMLElementTagNameMap {
    "fw-time-display": FwTimeDisplay;
  }
}
