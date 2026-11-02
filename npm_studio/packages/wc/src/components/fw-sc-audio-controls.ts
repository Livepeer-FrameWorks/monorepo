/**
 * <fw-sc-audio-controls> — The audio section of the studio: the browser's own
 * processing of each microphone, and the noise gate, de-esser, EQ and ducking
 * on the mixed audio. Port of StudioAudioControls from streamcrafter-react.
 */
import { html, css, nothing } from "lit";
import { customElement, property } from "lit/decorators.js";
import type { AudioProcessingConfig } from "@livepeer-frameworks/streamcrafter-core";
import { sharedStyles } from "../styles/shared-styles.js";
import { utilityStyles } from "../styles/utility-styles.js";
import { StudioBoundElement } from "./studio-bound-element.js";

export interface BrowserAudioProcessing {
  echoCancellation: boolean;
  noiseSuppression: boolean;
  autoGainControl: boolean;
}

const GATE_THRESHOLD_DEFAULT = -40;
const BROWSER_KEYS = ["echoCancellation", "noiseSuppression", "autoGainControl"] as const;

type Processing = Omit<AudioProcessingConfig, "sidechain">;

@customElement("fw-sc-audio-controls")
export class FwScAudioControls extends StudioBoundElement {
  /** Browser processing of the microphones; set by the studio, which applies changes. */
  @property({ attribute: false }) browserProcessing: BrowserAudioProcessing | null = null;

  static styles = [
    sharedStyles,
    utilityStyles,
    css`
      :host {
        display: block;
      }
    `,
  ];

  private get _controller() {
    return this.boundStudio?.pc.getController() ?? null;
  }

  private _change(config: Processing) {
    this._controller?.setAudioProcessing(config);
    this.requestUpdate();
  }

  private _changeBrowser(settings: Partial<BrowserAudioProcessing>) {
    this.dispatchEvent(
      new CustomEvent("fw-audio-processing-change", {
        detail: { settings },
        bubbles: true,
        composed: true,
      })
    );
  }

  private _range(
    label: string,
    value: number,
    min: number,
    max: number,
    disabled: boolean,
    onInput: (value: number) => void
  ) {
    return html`
      <label class="fw-sc-audio-field">
        <span>${label}</span>
        <input
          type="range"
          min=${min}
          max=${max}
          step="1"
          .value=${String(value)}
          ?disabled=${disabled}
          @input=${(e: Event) => onInput(Number((e.target as HTMLInputElement).value))}
        />
        <span class="fw-sc-audio-value">${value} dB</span>
      </label>
    `;
  }

  private _toggle(label: string, checked: boolean, onChange: (checked: boolean) => void) {
    return html`
      <label class="fw-sc-audio-toggle">
        <input
          type="checkbox"
          .checked=${checked}
          @change=${(e: Event) => onChange((e.target as HTMLInputElement).checked)}
        />
        ${label}
      </label>
    `;
  }

  protected render() {
    const controller = this._controller;
    if (!controller) return nothing;
    const processing = controller.getAudioProcessing();
    const gate = processing.noiseGate;
    const eq = processing.eq;
    const browser = this.browserProcessing;

    return html`
      <details class="fw-sc-audio">
        <summary class="fw-sc-audio-summary">${this.t("audio")}</summary>
        <div class="fw-sc-audio-body">
          ${browser
            ? html`
                <fieldset class="fw-sc-audio-group">
                  <legend>${this.t("browserProcessing")}</legend>
                  ${BROWSER_KEYS.map((key) =>
                    this._toggle(this.t(key), browser[key], (checked) =>
                      this._changeBrowser({ [key]: checked })
                    )
                  )}
                </fieldset>
              `
            : nothing}
          <fieldset class="fw-sc-audio-group">
            <legend>${this.t("processing")}</legend>
            ${this._toggle(this.t("noiseGate"), gate?.enabled ?? false, (checked) =>
              this._change({
                noiseGate: {
                  enabled: checked,
                  threshold: gate?.threshold ?? GATE_THRESHOLD_DEFAULT,
                },
              })
            )}
            ${this._range(
              this.t("noiseGateThreshold"),
              gate?.threshold ?? GATE_THRESHOLD_DEFAULT,
              -80,
              0,
              !gate?.enabled,
              (threshold) => this._change({ noiseGate: { enabled: true, threshold } })
            )}
            ${this._toggle(this.t("deEsser"), processing.deEsser?.enabled ?? false, (checked) =>
              this._change({ deEsser: { enabled: checked } })
            )}
            ${this._toggle(this.t("equalizer"), eq?.enabled ?? false, (checked) =>
              this._change({
                eq: { enabled: checked, low: eq?.low ?? 0, mid: eq?.mid ?? 0, high: eq?.high ?? 0 },
              })
            )}
            ${(
              [
                ["low", this.t("eqLow")],
                ["mid", this.t("eqMid")],
                ["high", this.t("eqHigh")],
              ] as const
            ).map(([band, label]) =>
              this._range(label, eq?.[band] ?? 0, -12, 12, !eq?.enabled, (gain) =>
                this._change({ eq: { enabled: true, [band]: gain } })
              )
            )}
            ${this._toggle(this.t("ducking"), controller.isAudioDuckingEnabled(), (checked) => {
              controller.setAudioDucking(checked);
              this.requestUpdate();
            })}
          </fieldset>
        </div>
      </details>
    `;
  }
}

declare global {
  interface HTMLElementTagNameMap {
    "fw-sc-audio-controls": FwScAudioControls;
  }
}
