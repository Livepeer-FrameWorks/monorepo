/**
 * <fw-sc-camera-picker> — Add Camera button. Starts the only camera straight
 * away and lists the cameras when there is more than one, or when one is live.
 * Port of CameraPicker.tsx from streamcrafter-react.
 */
import { html, css, nothing } from "lit";
import { customElement, property, state, query } from "lit/decorators.js";
import type { CameraChoice, MediaSource } from "@livepeer-frameworks/streamcrafter-core";
import { sharedStyles } from "../styles/shared-styles.js";
import { utilityStyles } from "../styles/utility-styles.js";
import { cameraIcon } from "../icons/index.js";
import { StudioBoundElement } from "./studio-bound-element.js";

@customElement("fw-sc-camera-picker")
export class FwScCameraPicker extends StudioBoundElement {
  @property({ type: Boolean }) disabled = false;

  @state() private _choices: CameraChoice[] | null = null;
  @state() private _listFailed = false;

  @query(".fw-sc-camera-picker") private _rootEl!: HTMLElement | null;

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
    this._close();
    super.disconnectedCallback();
  }

  private _handleDocumentMouseDown = (event: MouseEvent) => {
    if (this._rootEl && event.composedPath().includes(this._rootEl)) return;
    this._close();
  };

  private _handleDocumentKeyDown = (event: KeyboardEvent) => {
    if (event.key === "Escape") this._close();
  };

  private _setOpen(open: boolean) {
    if (open) {
      document.addEventListener("mousedown", this._handleDocumentMouseDown);
      document.addEventListener("keydown", this._handleDocumentKeyDown);
    } else {
      document.removeEventListener("mousedown", this._handleDocumentMouseDown);
      document.removeEventListener("keydown", this._handleDocumentKeyDown);
    }
  }

  private _close() {
    this._choices = null;
    this._listFailed = false;
    this._setOpen(false);
  }

  /** Opens the list, or starts the camera when there is nothing to choose. */
  async toggle() {
    const studio = this.boundStudio;
    if (!studio) return;
    this._listFailed = false;
    if (this._choices) {
      this._close();
      return;
    }
    let choices: CameraChoice[];
    try {
      choices = await studio.pc.getCameraChoices();
    } catch (error) {
      console.error("Failed to list cameras:", error);
      this._listFailed = true;
      this._setOpen(true);
      return;
    }
    const hasCamera = studio.pc.s.sources.some((source: MediaSource) => source.type === "camera");
    if (!hasCamera && choices.length <= 1) {
      this.pick(choices[0]?.deviceId);
      return;
    }
    this._choices = choices;
    this._setOpen(true);
  }

  pick(videoDeviceId?: string) {
    this._close();
    this.boundStudio?.pc
      .startCamera(videoDeviceId ? { videoDeviceId } : undefined)
      .catch(console.error);
  }

  /** The cameras on offer while the list is open. */
  get choices(): CameraChoice[] | null {
    return this._choices;
  }

  protected render() {
    const studio = this.boundStudio;
    const canAdd =
      !!studio && studio.support.camera && studio.pc.s.state !== "destroyed" && !this.disabled;
    const choices = this._choices;
    return html`
      <div class="fw-sc-camera-picker">
        <button
          type="button"
          class="fw-sc-action-secondary"
          @click=${() => this.toggle()}
          ?disabled=${!canAdd}
          title=${this.t("addCamera")}
          aria-haspopup="menu"
          aria-expanded=${choices !== null}
        >
          ${cameraIcon(18)}
        </button>
        ${this._listFailed
          ? html`
              <div class="fw-sc-camera-menu" role="alert">
                <div class="fw-sc-camera-menu-note">${this.t("cameraListError")}</div>
              </div>
            `
          : nothing}
        ${choices
          ? html`
              <div class="fw-sc-camera-menu" role="menu" aria-label=${this.t("chooseCamera")}>
                <div class="fw-sc-camera-menu-title">${this.t("chooseCamera")}</div>
                ${choices.map(
                  (choice) => html`
                    <button
                      type="button"
                      role="menuitem"
                      class="fw-sc-context-menu-item"
                      ?disabled=${choice.inUse}
                      @click=${() => this.pick(choice.deviceId)}
                    >
                      <span>${choice.label}</span>
                      ${choice.inUse
                        ? html`<span class="fw-sc-camera-menu-note"
                            >${this.t("cameraActive")}</span
                          >`
                        : nothing}
                    </button>
                  `
                )}
              </div>
            `
          : nothing}
      </div>
    `;
  }
}

declare global {
  interface HTMLElementTagNameMap {
    "fw-sc-camera-picker": FwScCameraPicker;
  }
}
