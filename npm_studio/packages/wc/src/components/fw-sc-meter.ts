/**
 * <fw-sc-meter> — Level meter of the mixed audio. It follows the mixer's level
 * updates by itself, so a studio does not render again at the meter's rate.
 */
import { html, css } from "lit";
import { customElement, state } from "lit/decorators.js";
import type { AudioMixer } from "@livepeer-frameworks/streamcrafter-core";
import { sharedStyles } from "../styles/shared-styles.js";
import { StudioBoundElement } from "./studio-bound-element.js";

@customElement("fw-sc-meter")
export class FwScMeter extends StudioBoundElement {
  @state() private _level = 0;
  @state() private _peak = 0;

  private _mixer: AudioMixer | null = null;
  private _unsubscribe: (() => void) | null = null;

  static styles = [
    sharedStyles,
    css`
      :host {
        display: block;
      }
    `,
  ];

  disconnectedCallback() {
    this._follow(null);
    super.disconnectedCallback();
  }

  protected willUpdate() {
    this._follow(this.boundStudio?.pc.getController()?.getAudioMixer() ?? null);
  }

  private _follow(mixer: AudioMixer | null) {
    if (mixer === this._mixer) return;
    this._unsubscribe?.();
    this._unsubscribe = null;
    this._mixer = mixer;
    this._level = 0;
    this._peak = 0;
    if (!mixer) return;
    this._unsubscribe = mixer.on("levelUpdate", ({ level, peakLevel }) => {
      this._level = level;
      this._peak = peakLevel;
    });
  }

  protected render() {
    return html`
      <div
        class="fw-sc-vu-meter"
        role="meter"
        aria-valuemin="0"
        aria-valuemax="1"
        aria-valuenow=${this._level.toFixed(2)}
        aria-label=${this.t("audio")}
      >
        <div class="fw-sc-vu-meter-fill" style="width:${Math.min(this._level * 100, 100)}%"></div>
        <div class="fw-sc-vu-meter-peak" style="left:${Math.min(this._peak * 100, 100)}%"></div>
      </div>
    `;
  }
}

declare global {
  interface HTMLElementTagNameMap {
    "fw-sc-meter": FwScMeter;
  }
}
