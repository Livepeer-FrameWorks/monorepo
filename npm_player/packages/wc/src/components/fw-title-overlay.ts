import { LitElement, html, css, nothing } from "lit";
import { customElement, property } from "lit/decorators.js";
import { sharedStyles } from "../styles/shared-styles.js";
import { utilityStyles } from "../styles/utility-styles.js";

@customElement("fw-title-overlay")
export class FwTitleOverlay extends LitElement {
  @property({ type: String }) override title: string = "";
  @property({ type: String }) description: string | null = null;

  static styles = [
    sharedStyles,
    utilityStyles,
    css`
      :host {
        display: contents;
      }
      .overlay {
        position: absolute;
        inset: 0 0 auto 0;
        padding: 1rem 1.25rem;
        background: linear-gradient(to bottom, rgb(0 0 0 / 0.7), rgb(0 0 0 / 0.4), transparent);
        pointer-events: none;
        transition: opacity 300ms ease;
        z-index: 10;
      }
      .title {
        font-size: 0.875rem;
        font-weight: 500;
        color: white;
        max-width: 80%;
        overflow: hidden;
        text-overflow: ellipsis;
        white-space: nowrap;
      }
      .desc {
        margin-top: 0.25rem;
        font-size: 0.75rem;
        color: rgb(255 255 255 / 0.7);
        max-width: 70%;
        display: -webkit-box;
        -webkit-line-clamp: 2;
        -webkit-box-orient: vertical;
        overflow: hidden;
      }
    `,
  ];

  protected render() {
    if (this.title === "" && !this.description) return nothing;
    return html`
      <div class="overlay fw-title-overlay">
        ${this.title ? html`<div class="title">${this.title}</div>` : nothing}
        ${this.description ? html`<div class="desc">${this.description}</div>` : nothing}
      </div>
    `;
  }
}

declare global {
  interface HTMLElementTagNameMap {
    "fw-title-overlay": FwTitleOverlay;
  }
}
