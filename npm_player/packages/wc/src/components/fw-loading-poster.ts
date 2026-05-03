import { LitElement, css, html, nothing } from "lit";
import { customElement, property, state } from "lit/decorators.js";
import type { LoadingPosterInfo } from "@livepeer-frameworks/player-core";

/**
 * Loading-state poster overlay element. Dumb spec consumer — dispatches on
 * `loadingPoster.mode` and reads spec fields. The controller
 * (PlayerController.buildLoadingPosterInfo) owns source priority and the
 * synthetic-vs-measured decision.
 */
@customElement("fw-loading-poster")
export class FwLoadingPoster extends LitElement {
  @property({ attribute: false }) loadingPoster: LoadingPosterInfo | null = null;

  @state() private _tickIdx = 0;
  @state() private _measuredW = 0;
  @state() private _measuredH = 0;
  @state() private _spriteFailed = false;
  private _measuredUrl: string | null = null;
  private _intervalId: ReturnType<typeof setInterval> | null = null;
  private static readonly CYCLE_MS = 2000;
  private readonly _clipId = `fw-loading-poster-clip-${Math.random().toString(36).slice(2)}`;

  static styles = css`
    :host {
      display: block;
      position: absolute;
      inset: 0;
      pointer-events: none;
    }
    .root {
      position: absolute;
      inset: 0;
      width: 100%;
      height: 100%;
      background: #000;
      overflow: hidden;
      pointer-events: none;
    }
    .sprite {
      position: absolute;
      inset: 0;
      width: 100%;
      height: 100%;
      overflow: hidden;
    }
    img {
      position: absolute;
      inset: 0;
      width: 100%;
      height: 100%;
      background: #000;
      object-fit: contain;
    }
  `;

  updated(changed: Map<string, unknown>) {
    if (changed.has("loadingPoster")) {
      this._restartCycle();
      this._maybeMeasureSprite();
    }
  }

  disconnectedCallback() {
    super.disconnectedCallback();
    this._stopCycle();
  }

  private _tileCount(): number {
    const lp = this.loadingPoster;
    if (!lp || lp.mode !== "animate") return 0;
    return lp.geometry === "measured" ? lp.cues.length : 0;
  }

  private _restartCycle() {
    this._stopCycle();
    const tiles = this._tileCount();
    if (tiles < 2) {
      this._tickIdx = 0;
      return;
    }
    let current = 0;
    this._tickIdx = 0;
    const stepMs = Math.max(20, Math.floor(FwLoadingPoster.CYCLE_MS / tiles));
    this._intervalId = setInterval(() => {
      current = Math.min(current + 1, tiles - 1);
      this._tickIdx = current;
      if (current >= tiles - 1) this._stopCycle();
    }, stepMs);
  }

  private _stopCycle() {
    if (this._intervalId !== null) {
      clearInterval(this._intervalId);
      this._intervalId = null;
    }
  }

  private _maybeMeasureSprite() {
    const lp = this.loadingPoster;
    if (!lp || lp.mode !== "animate" || lp.geometry !== "measured" || !lp.spriteJpgUrl) return;
    if (this._measuredUrl === lp.spriteJpgUrl && (this._measuredW > 0 || this._spriteFailed)) {
      return;
    }
    this._measuredUrl = lp.spriteJpgUrl;
    this._measuredW = 0;
    this._measuredH = 0;
    this._spriteFailed = false;
    const img = new Image();
    img.onload = () => {
      if (this._measuredUrl !== lp.spriteJpgUrl) return;
      this._measuredW = img.naturalWidth;
      this._measuredH = img.naturalHeight;
    };
    img.onerror = () => {
      if (this._measuredUrl !== lp.spriteJpgUrl) return;
      this._spriteFailed = true;
    };
    img.src = lp.spriteJpgUrl;
  }

  private _shouldCacheBust(p: LoadingPosterInfo): boolean {
    if (!p.staticUrl) return false;
    if (p.staticUrl.startsWith("data:") || p.staticUrl.startsWith("blob:")) return false;
    if (p.staticSource === "thumbnail-prop") return false;
    return true;
  }

  private _withCacheBust(p: LoadingPosterInfo): string | undefined {
    if (!p.staticUrl) return undefined;
    if (!this._shouldCacheBust(p)) return p.staticUrl;
    const sep = p.staticUrl.includes("?") ? "&" : "?";
    return `${p.staticUrl}${sep}_g=${p.generation}`;
  }

  render() {
    const lp = this.loadingPoster;
    if (!lp) return nothing;

    if (lp.mode === "animate" && lp.spriteJpgUrl) {
      let cueRect: {
        x: number;
        y: number;
        w: number;
        h: number;
        imgW: number;
        imgH: number;
      } | null = null;
      if (lp.geometry === "measured") {
        const cue = lp.cues[this._tickIdx % Math.max(lp.cues.length, 1)];
        if (
          cue &&
          this._measuredUrl === lp.spriteJpgUrl &&
          this._measuredW > 0 &&
          this._measuredH > 0
        ) {
          cueRect = {
            x: cue.x,
            y: cue.y,
            w: cue.width,
            h: cue.height,
            imgW: this._measuredW,
            imgH: this._measuredH,
          };
        }
      }

      if (cueRect) {
        return html`<div class="root" aria-hidden="true">
          <svg
            class="sprite"
            viewBox=${`0 0 ${cueRect.w} ${cueRect.h}`}
            preserveAspectRatio="xMidYMid meet"
          >
            <defs>
              <clipPath id=${this._clipId} clipPathUnits="userSpaceOnUse">
                <rect x="0" y="0" width=${cueRect.w} height=${cueRect.h}></rect>
              </clipPath>
            </defs>
            <g clip-path=${`url(#${this._clipId})`}>
              <image
                href=${lp.spriteJpgUrl}
                x=${-cueRect.x}
                y=${-cueRect.y}
                width=${cueRect.imgW}
                height=${cueRect.imgH}
                preserveAspectRatio="none"
              />
            </g>
          </svg>
        </div>`;
      }
    }

    const url = this._withCacheBust(lp);
    if (!url) return nothing;
    return html`<div class="root" aria-hidden="true"><img src=${url} alt="" /></div>`;
  }
}

declare global {
  interface HTMLElementTagNameMap {
    "fw-loading-poster": FwLoadingPoster;
  }
}
