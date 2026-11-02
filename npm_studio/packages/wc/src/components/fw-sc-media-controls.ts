/**
 * <fw-sc-media-controls> — Local media files in the mix, with their playback
 * controls. Volume, mute and removal live with every other source in the mixer.
 * Port of StudioMediaControls from streamcrafter-react.
 */
import { html, css, nothing } from "lit";
import { customElement, state } from "lit/decorators.js";
import { sharedStyles } from "../styles/shared-styles.js";
import { utilityStyles } from "../styles/utility-styles.js";
import { StudioBoundElement } from "./studio-bound-element.js";
import { takeFile } from "./fw-sc-overlay-controls.js";

@customElement("fw-sc-media-controls")
export class FwScMediaControls extends StudioBoundElement {
  @state() private _fileFailed = false;

  static styles = [
    sharedStyles,
    utilityStyles,
    css`
      :host {
        display: block;
      }
    `,
  ];

  /** Adds a video or audio file to the mix; a file the browser cannot play is reported. */
  async addFile(file: File) {
    const studio = this.boundStudio;
    if (!studio) return;
    this._fileFailed = false;
    try {
      await studio.pc.addMediaFile(file);
    } catch (error) {
      console.error("Failed to add media file:", error);
      this._fileFailed = true;
    }
  }

  protected render() {
    const studio = this.boundStudio;
    if (!studio) return nothing;
    const support = studio.support;
    const files = studio.pc.s.sources.filter((source) => source.media);

    return html`
      <div class="fw-sc-producer-section">
        <div class="fw-sc-producer-section-header">
          <span class="fw-sc-producer-section-title">${this.t("media")}</span>
          ${support.mediaFile
            ? html`
                <div class="fw-sc-producer-section-actions">
                  <label class="fw-sc-producer-btn">
                    ${this.t("addMediaFile")}
                    <input
                      class="fw-sc-producer-file"
                      type="file"
                      accept="video/*,audio/*"
                      @change=${(event: Event) => {
                        const file = takeFile(event);
                        if (file) this.addFile(file);
                      }}
                    />
                  </label>
                </div>
              `
            : nothing}
        </div>
        ${!support.mediaFile
          ? html`<p class="fw-sc-producer-notice">${this.t("mediaFileUnsupported")}</p>`
          : nothing}
        ${this._fileFailed
          ? html`<p class="fw-sc-producer-error" role="alert">${this.t("mediaFileError")}</p>`
          : nothing}
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
