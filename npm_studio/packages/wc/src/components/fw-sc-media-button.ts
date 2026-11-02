/**
 * <fw-sc-media-button> — Add Media File button: picks a local video or audio
 * file and plays it into the mix. A browser that cannot capture a media element
 * gets the button disabled with the reason as its title.
 * Port of MediaFileButton.tsx from streamcrafter-react.
 */
import { html, css, nothing } from "lit";
import { customElement, property, state, query } from "lit/decorators.js";
import { sharedStyles } from "../styles/shared-styles.js";
import { utilityStyles } from "../styles/utility-styles.js";
import { filmIcon } from "../icons/index.js";
import { StudioBoundElement } from "./studio-bound-element.js";

@customElement("fw-sc-media-button")
export class FwScMediaButton extends StudioBoundElement {
  @property({ type: Boolean }) disabled = false;

  @state() private _failed = false;

  @query("input[type=file]") private _input!: HTMLInputElement | null;
  @query(".fw-sc-camera-picker") private _rootEl!: HTMLDivElement | null;

  static styles = [
    sharedStyles,
    utilityStyles,
    css`
      :host {
        display: inline-flex;
      }
    `,
  ];

  disconnectedCallback() {
    this._listenForDismissal(false);
    super.disconnectedCallback();
  }

  // The error note closes like the camera menu: on a click elsewhere or Escape.
  private _handleDocumentMouseDown = (event: MouseEvent) => {
    if (this._rootEl && event.composedPath().includes(this._rootEl)) return;
    this._setFailed(false);
  };

  private _handleDocumentKeyDown = (event: KeyboardEvent) => {
    if (event.key === "Escape") this._setFailed(false);
  };

  private _listenForDismissal(listen: boolean) {
    if (listen) {
      document.addEventListener("mousedown", this._handleDocumentMouseDown);
      document.addEventListener("keydown", this._handleDocumentKeyDown);
    } else {
      document.removeEventListener("mousedown", this._handleDocumentMouseDown);
      document.removeEventListener("keydown", this._handleDocumentKeyDown);
    }
  }

  private _setFailed(failed: boolean) {
    this._failed = failed;
    this._listenForDismissal(failed);
  }

  /** Adds a video or audio file to the mix; a file the browser cannot play is reported. */
  async addFile(file: File) {
    const studio = this.boundStudio;
    if (!studio) return;
    this._setFailed(false);
    try {
      await studio.pc.addMediaFile(file);
    } catch (error) {
      console.error("Failed to add media file:", error);
      this._setFailed(true);
    }
  }

  protected render() {
    const studio = this.boundStudio;
    const supported = studio?.support.mediaFile ?? false;
    const canAdd = !!studio && supported && studio.pc.s.state !== "destroyed" && !this.disabled;
    const label = supported ? this.t("addMediaFile") : this.t("mediaFileUnsupported");
    return html`
      <div class="fw-sc-camera-picker">
        <button
          type="button"
          class="fw-sc-action-secondary"
          ?disabled=${!canAdd}
          title=${label}
          aria-label=${label}
          @click=${() => {
            this._setFailed(false);
            this._input?.click();
          }}
        >
          ${filmIcon(18)}
        </button>
        <input
          class="fw-sc-hidden"
          type="file"
          accept="video/*,audio/*"
          tabindex="-1"
          aria-hidden="true"
          @change=${(event: Event) => {
            const input = event.target as HTMLInputElement;
            const file = input.files?.[0];
            input.value = "";
            if (file) this.addFile(file);
          }}
        />
        ${this._failed
          ? html`
              <div class="fw-sc-camera-menu" role="alert">
                <div class="fw-sc-camera-menu-note">${this.t("mediaFileError")}</div>
              </div>
            `
          : nothing}
      </div>
    `;
  }
}

declare global {
  interface HTMLElementTagNameMap {
    "fw-sc-media-button": FwScMediaButton;
  }
}
