import { LitElement, css, html, nothing } from "lit";
import { customElement, property } from "lit/decorators.js";
import { classMap } from "lit/directives/class-map.js";
import { styleMap } from "lit/directives/style-map.js";
import { sharedStyles } from "../styles/shared-styles.js";
import { utilityStyles } from "../styles/utility-styles.js";

@customElement("fw-thumbnail-overlay")
export class FwThumbnailOverlay extends LitElement {
  @property({ type: String, attribute: "thumbnail-url" }) thumbnailUrl: string | null = null;
  @property({ type: String }) message: string | null = null;
  @property({ type: Boolean, attribute: "show-unmute-message" }) showUnmuteMessage = false;
  @property({ attribute: false }) onPlay?: () => void;
  @property({ type: String, attribute: "class-name" }) className = "";

  static styles = [
    sharedStyles,
    utilityStyles,
    css`
      :host {
        display: block;
        width: 100%;
        height: 100%;
      }
    `,
  ];

  private _handlePlay = (event: Event) => {
    event.stopPropagation();
    if (this.onPlay) {
      this.onPlay();
      return;
    }
    this.dispatchEvent(new CustomEvent("fw-play", { bubbles: true, composed: true }));
  };

  private _handleKeyDown = (event: KeyboardEvent) => {
    if (event.key !== "Enter" && event.key !== " ") {
      return;
    }
    event.preventDefault();
    this._handlePlay(event);
  };

  protected render() {
    return html`
      <div
        role="button"
        tabindex="0"
        @click=${this._handlePlay}
        @keydown=${this._handleKeyDown}
        class=${classMap({
          "fw-player-thumbnail": true,
          relative: true,
          flex: true,
          "h-full": true,
          "w-full": true,
          "min-h-\\[280px\\]": true,
          "items-center": true,
          "justify-center": true,
          "overflow-hidden": true,
          "rounded-xl": true,
          "bg-slate-950": true,
          "text-foreground": true,
          "outline-none": true,
          transition: true,
          "focus-visible\\:ring-2": true,
          "focus-visible\\:ring-primary": true,
          "focus-visible\\:ring-offset-2": true,
          "focus-visible\\:ring-offset-background": true,
          [this.className]: this.className.length > 0,
        })}
      >
        ${this.thumbnailUrl
          ? html`<div
              class="absolute inset-0 bg-cover bg-center"
              style=${styleMap({ backgroundImage: `url(${this.thumbnailUrl})` })}
            ></div>`
          : nothing}

        <div
          class=${classMap({
            absolute: true,
            "inset-0": true,
            "bg-slate-950\\/70": !!this.thumbnailUrl,
            "bg-gradient-to-br": !this.thumbnailUrl,
            "from-slate-900": !this.thumbnailUrl,
            "via-slate-950": !this.thumbnailUrl,
            "to-slate-900": !this.thumbnailUrl,
          })}
        ></div>

        <div
          class="relative z-10 flex max-w-[320px] flex-col items-center gap-4 px-6 text-center text-sm sm:gap-6"
        >
          ${this.showUnmuteMessage
            ? html`
                <div
                  class="w-full rounded-lg border border-white/15 bg-black/80 p-4 text-sm text-white shadow-lg backdrop-blur"
                >
                  <div
                    class="mb-1 flex items-center justify-center gap-2 text-base font-semibold text-primary"
                  >
                    <span aria-hidden="true">MUTED</span>
                    <span>Click to unmute</span>
                  </div>
                  <p class="text-xs text-white/80">
                    Stream is playing muted - tap to enable sound.
                  </p>
                </div>
              `
            : html`
                <button
                  type="button"
                  class="h-20 w-20 rounded-full bg-primary/90 text-primary-foreground shadow-lg shadow-primary/40 transition hover:bg-primary focus-visible:bg-primary flex items-center justify-center"
                  aria-label="Play stream"
                >
                  <svg
                    viewBox="0 0 24 24"
                    fill="currentColor"
                    class="ml-0.5 h-8 w-8"
                    aria-hidden="true"
                  >
                    <path d="M8 5v14l11-7z"></path>
                  </svg>
                </button>
                <div
                  class="w-full rounded-lg border border-white/10 bg-black/70 p-5 text-white shadow-inner backdrop-blur"
                >
                  <p class="text-base font-semibold text-primary">
                    ${this.message ?? "Click to play"}
                  </p>
                  <p class="mt-1 text-xs text-white/70">
                    ${this.message ? "Start streaming instantly" : "Jump into the live feed"}
                  </p>
                </div>
              `}
        </div>
      </div>
    `;
  }
}

declare global {
  interface HTMLElementTagNameMap {
    "fw-thumbnail-overlay": FwThumbnailOverlay;
  }
}
