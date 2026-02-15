/**
 * <fw-context-menu> — Placeholder for right-click context menu.
 * The actual context menu is rendered inline by <fw-player> since it needs
 * to be positioned relative to mouse coordinates. This component exists
 * for the module export and define.ts registration.
 */
import { LitElement, css } from "lit";
import { customElement } from "lit/decorators.js";

@customElement("fw-context-menu")
export class FwContextMenu extends LitElement {
  static styles = css`
    :host {
      display: none;
    }
  `;
}

declare global {
  interface HTMLElementTagNameMap {
    "fw-context-menu": FwContextMenu;
  }
}
