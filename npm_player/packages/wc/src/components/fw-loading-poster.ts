import { LitElement, css, html, nothing } from "lit";
import { customElement, property, state } from "lit/decorators.js";
import type { LoadingPosterInfo } from "@livepeer-frameworks/player-core";

export type LoadingPosterMode = "animate" | "latest";

/**
 * Loading-state poster overlay element.
 *
 * Source priority (per mode):
 *   - "animate": sprite cycle → Chandler poster.jpg → Mist preview JPEG → fallbackPosterUrl
 *   - "latest":  Chandler poster.jpg → Mist preview JPEG → fallbackPosterUrl (never uses sprite)
 *
 * The sprite is thumbnail-resolution and is only suitable for animation; the static fallback
 * tier always uses the full-resolution poster.jpg / Mist lang:"pre" frame.
 */
@customElement("fw-loading-poster")
export class FwLoadingPoster extends LitElement {
  @property({ attribute: false }) loadingPoster: LoadingPosterInfo | null = null;
  @property({ type: String }) mode: LoadingPosterMode = "animate";
  @property({ type: String, attribute: "fallback-poster-url" }) fallbackPosterUrl?: string;
  @property({ type: Number, attribute: "cycle-ms" }) cycleMs = 2000;

  @state() private _tickIdx = 0;
  private _intervalId: ReturnType<typeof setInterval> | null = null;

  static styles = css`
    :host {
      display: block;
      position: absolute;
      inset: 0;
      pointer-events: none;
    }
    .sprite {
      position: absolute;
      inset: 0;
      pointer-events: none;
    }
    img {
      position: absolute;
      inset: 0;
      width: 100%;
      height: 100%;
      object-fit: cover;
    }
  `;

  updated(changed: Map<string, unknown>) {
    if (changed.has("loadingPoster") || changed.has("mode") || changed.has("cycleMs")) {
      this._restartCycle();
    }
  }

  disconnectedCallback() {
    super.disconnectedCallback();
    this._stopCycle();
  }

  private _canAnimate(): boolean {
    const lp = this.loadingPoster;
    if (this.mode !== "animate" || !lp) return false;
    return !!lp.spriteJpgUrl && lp.cues.length >= 2 && lp.columns > 0 && lp.rows > 0;
  }

  private _restartCycle() {
    this._stopCycle();
    if (!this._canAnimate()) {
      this._tickIdx = 0;
      return;
    }
    const cues = this.loadingPoster!.cues;
    const stepMs = Math.max(20, Math.floor(this.cycleMs / cues.length));
    this._intervalId = setInterval(() => {
      this._tickIdx = (this._tickIdx + 1) % cues.length;
    }, stepMs);
  }

  private _stopCycle() {
    if (this._intervalId !== null) {
      clearInterval(this._intervalId);
      this._intervalId = null;
    }
  }

  render() {
    const lp = this.loadingPoster;
    if (this._canAnimate() && lp) {
      const cue = lp.cues[this._tickIdx % lp.cues.length];
      const spriteWidth = lp.spriteWidth || cue.width * lp.columns;
      const spriteHeight = lp.spriteHeight || cue.height * lp.rows;
      return html`<svg
        class="sprite"
        viewBox=${`${cue.x} ${cue.y} ${cue.width} ${cue.height}`}
        preserveAspectRatio="xMidYMid slice"
        aria-hidden="true"
      >
        <image href=${lp.spriteJpgUrl!} x="0" y="0" width=${spriteWidth} height=${spriteHeight} />
      </svg>`;
    }
    const rawUrl = lp?.posterUrl || lp?.mistPreviewUrl || this.fallbackPosterUrl;
    if (!rawUrl) return nothing;
    // Cache-bust on every cue regen so the browser refetches poster.jpg. Skip for
    // data:/blob: URLs and for the user-provided fallback (no refresh contract).
    const isRefreshable =
      rawUrl !== this.fallbackPosterUrl &&
      !rawUrl.startsWith("data:") &&
      !rawUrl.startsWith("blob:");
    const url =
      isRefreshable && lp
        ? `${rawUrl}${rawUrl.includes("?") ? "&" : "?"}_g=${lp.generation}`
        : rawUrl;
    return html`<img src=${url} alt="" aria-hidden="true" />`;
  }
}

declare global {
  interface HTMLElementTagNameMap {
    "fw-loading-poster": FwLoadingPoster;
  }
}
