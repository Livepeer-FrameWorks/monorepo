/**
 * <fw-sc-connection-health> — A pill that grades the publish connection while
 * live, with the send rate, packet loss and round-trip time as its title.
 * Port of StudioConnectionHealth from streamcrafter-react.
 */
import { html, css, nothing } from "lit";
import { customElement } from "lit/decorators.js";
import { assessConnectionHealth } from "@livepeer-frameworks/streamcrafter-core";
import { sharedStyles } from "../styles/shared-styles.js";
import { StudioBoundElement } from "./studio-bound-element.js";

const LABEL_KEYS = {
  good: "connectionGood",
  fair: "connectionFair",
  poor: "connectionPoor",
} as const;

@customElement("fw-sc-connection-health")
export class FwScConnectionHealth extends StudioBoundElement {
  static styles = [
    sharedStyles,
    css`
      :host {
        display: inline-flex;
      }
    `,
  ];

  protected render() {
    const s = this.boundStudio?.pc.s;
    const health = assessConnectionHealth(s?.stats ?? null);
    if (!s?.isStreaming || !health) return nothing;

    return html`
      <span
        class="fw-sc-health fw-sc-health--${health.level}"
        role="status"
        title=${this.t("connectionDetails", {
          bitrate: health.bitrateKbps,
          loss: health.packetLossPct.toFixed(1),
          rtt: Math.round(health.rttMs),
        })}
      >
        ${this.t(LABEL_KEYS[health.level])}
      </span>
    `;
  }
}

declare global {
  interface HTMLElementTagNameMap {
    "fw-sc-connection-health": FwScConnectionHealth;
  }
}
