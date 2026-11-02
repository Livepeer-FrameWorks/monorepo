/**
 * <fw-sc-media-controls> — Local media files in the mix, with their playback
 * controls. Files are added with <fw-sc-media-button>; volume, mute and removal
 * live with every other source in the mixer.
 * Port of StudioMediaControls from streamcrafter-react.
 */
import { html, css, nothing } from "lit";
import { customElement } from "lit/decorators.js";
import { sharedStyles } from "../styles/shared-styles.js";
import { utilityStyles } from "../styles/utility-styles.js";
import { StudioBoundElement } from "./studio-bound-element.js";

@customElement("fw-sc-media-controls")
export class FwScMediaControls extends StudioBoundElement {
  static styles = [
    sharedStyles,
    utilityStyles,
    css`
      :host {
        display: block;
      }
    `,
  ];

  protected render() {
    const studio = this.boundStudio;
    if (!studio) return nothing;
    const files = studio.pc.s.sources.filter((source) => source.media);

    return html`
      <div class="fw-sc-producer-section">
        <div class="fw-sc-producer-section-header">
          <span class="fw-sc-producer-section-title">${this.t("media")}</span>
        </div>
        ${files.length === 0
          ? html`<p class="fw-sc-producer-empty">${this.t("noMedia")}</p>`
          : html`
              <div class="fw-sc-media-list">
                ${files.map(
                  (source) => html`
                    <div class="fw-sc-media-item">
                      <span class="fw-sc-overlay-name">${source.label}</span>
                      <button
                        type="button"
                        class="fw-sc-producer-btn"
                        @click=${() =>
                          source.media?.playing
                            ? studio.pc.pauseMediaSource(source.id)
                            : studio.pc.playMediaSource(source.id)}
                      >
                        ${source.media?.playing ? this.t("pauseMedia") : this.t("playMedia")}
                      </button>
                      <label class="fw-sc-media-loop">
                        <input
                          type="checkbox"
                          .checked=${source.media?.loop ?? false}
                          @change=${(event: Event) =>
                            studio.pc.setMediaSourceLoop(
                              source.id,
                              (event.target as HTMLInputElement).checked
                            )}
                        />
                        ${this.t("loopMedia")}
                      </label>
                    </div>
                  `
                )}
              </div>
            `}
        <p class="fw-sc-producer-notice">${this.t("mediaTabNotice")}</p>
      </div>
    `;
  }
}

declare global {
  interface HTMLElementTagNameMap {
    "fw-sc-media-controls": FwScMediaControls;
  }
}
