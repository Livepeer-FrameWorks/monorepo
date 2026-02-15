import { LitElement, html, css, nothing } from "lit";
import { customElement, property, state } from "lit/decorators.js";
import { classMap } from "lit/directives/class-map.js";

export type SkipDirection = "back" | "forward" | null;

@customElement("fw-skip-indicator")
export class FwSkipIndicator extends LitElement {
  @property({ attribute: false }) direction: SkipDirection = null;
  @property({ type: Number }) seconds = 10;

  @state() private _visible = false;
  private _timer?: ReturnType<typeof setTimeout>;

  static styles = css`
    :host {
      display: contents;
    }
    .overlay {
      position: absolute;
      inset: 0;
      pointer-events: none;
      z-index: 25;
    }
    .ripple {
      position: absolute;
      top: 0;
      bottom: 0;
      width: 33%;
      background: rgb(255 255 255 / 0.1);
      border-radius: 50%;
      animation: _fw-ripple 600ms ease-out forwards;
    }
    .ripple--back {
      left: 0;
      border-radius: 0 50% 50% 0;
    }
    .ripple--forward {
      right: 0;
      border-radius: 50% 0 0 50%;
    }
    @keyframes _fw-ripple {
      from {
        opacity: 1;
      }
      to {
        opacity: 0;
      }
    }
    .content {
      position: absolute;
      top: 50%;
      transform: translateY(-50%);
      display: flex;
      flex-direction: column;
      align-items: center;
      gap: 0.25rem;
      color: white;
      animation: _fw-skip-pop 400ms ease-out;
    }
    .content--back {
      left: 12%;
    }
    .content--forward {
      right: 12%;
    }
    @keyframes _fw-skip-pop {
      from {
        opacity: 0;
        transform: translateY(-50%) scale(0.8);
      }
      to {
        opacity: 1;
        transform: translateY(-50%) scale(1);
      }
    }
    .icons {
      display: flex;
    }
    .icons svg:nth-child(2) {
      margin-left: -1rem;
    }
    .time {
      font-size: 0.75rem;
      font-weight: 600;
    }
  `;

  protected updated() {
    if (this.direction && !this._visible) {
      this._visible = true;
      clearTimeout(this._timer);
      this._timer = setTimeout(() => {
        this._visible = false;
        this.dispatchEvent(new CustomEvent("fw-hide", { bubbles: true, composed: true }));
      }, 600);
    }
  }

  private _renderRewind() {
    return html`<svg width="24" height="24" viewBox="0 0 24 24" fill="currentColor">
      <path d="M11 18V6l-8.5 6 8.5 6zm.5-6l8.5 6V6l-8.5 6z" />
    </svg>`;
  }

  private _renderForward() {
    return html`<svg width="24" height="24" viewBox="0 0 24 24" fill="currentColor">
      <path d="M4 18l8.5-6L4 6v12zm9-12v12l8.5-6L13 6z" />
    </svg>`;
  }

  protected render() {
    if (!this._visible || !this.direction) return nothing;
    const isBack = this.direction === "back";
    return html`
      <div class="overlay">
        <div
          class=${classMap({ ripple: true, "ripple--back": isBack, "ripple--forward": !isBack })}
        ></div>
        <div
          class=${classMap({ content: true, "content--back": isBack, "content--forward": !isBack })}
        >
          <div class="icons">
            ${isBack
              ? html`${this._renderRewind()}${this._renderRewind()}`
              : html`${this._renderForward()}${this._renderForward()}`}
          </div>
          <span class="time">${isBack ? `-${this.seconds}s` : `+${this.seconds}s`}</span>
        </div>
      </div>
    `;
  }
}

declare global {
  interface HTMLElementTagNameMap {
    "fw-skip-indicator": FwSkipIndicator;
  }
}
