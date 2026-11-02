/**
 * <fw-sc-overlay-controls> — Logo and text overlays of the active scene, with
 * their position, size and text. Renders nothing until the compositor runs.
 * Port of StudioOverlayControls from streamcrafter-react.
 */
import { html, css, nothing } from "lit";
import { customElement, state } from "lit/decorators.js";
import { TEXT_OVERLAY_DEFAULTS, type StudioOverlay } from "@livepeer-frameworks/streamcrafter-core";
import { sharedStyles } from "../styles/shared-styles.js";
import { utilityStyles } from "../styles/utility-styles.js";
import { xIcon } from "../icons/index.js";
import { StudioBoundElement } from "./studio-bound-element.js";

export function takeFile(event: Event): File | undefined {
  const input = event.target as HTMLInputElement;
  const file = input.files?.[0];
  input.value = "";
  return file;
}

@customElement("fw-sc-overlay-controls")
export class FwScOverlayControls extends StudioBoundElement {
  @state() private _imageFailed = false;

  static styles = [
    sharedStyles,
    utilityStyles,
    css`
      :host {
        display: block;
      }
    `,
  ];

  private get _sceneManager() {
    return this.boundStudio?.sceneManager ?? null;
  }

  /** Adds a logo to the active scene; a file that is not an image is reported. */
  async addImage(file: File) {
    const sceneManager = this._sceneManager;
    const scene = sceneManager?.getActiveScene();
    if (!sceneManager || !scene) return;

    this._imageFailed = false;
    try {
      await sceneManager.addImageOverlay(scene.id, file);
    } catch (error) {
      console.error("Failed to add image overlay:", error);
      this._imageFailed = true;
    }
    this.requestUpdate();
  }

  private _changed() {
    this.requestUpdate();
    this.boundStudio?.requestUpdate();
  }

  private _field(label: string, control: unknown) {
    return html`<label class="fw-sc-overlay-field">${label} ${control}</label>`;
  }

  private _percent(overlay: StudioOverlay, label: string, field: "x" | "y" | "size", min: number) {
    return this._field(
      label,
      html`<input
        type="range"
        min=${min}
        max="100"
        .value=${String(Math.round(overlay[field] * 100))}
        @input=${(event: Event) => {
          const value = Number((event.target as HTMLInputElement).value) / 100;
          this._sceneManager?.setOverlayPlacement(overlay.sourceId, { [field]: value });
          this._changed();
        }}
      />`
    );
  }

  private _renderOverlay(overlay: StudioOverlay) {
    const sceneManager = this._sceneManager;
    const text = overlay.text;
    const updateText = (
      config: Parameters<NonNullable<typeof sceneManager>["updateTextOverlay"]>[1]
    ) => {
      sceneManager?.updateTextOverlay(overlay.sourceId, config);
      this._changed();
    };

    return html`
      <div class="fw-sc-overlay-item">
        <div class="fw-sc-overlay-header">
          <span class="fw-sc-overlay-name">${overlay.label}</span>
          <button
            type="button"
            class="fw-sc-icon-btn fw-sc-icon-btn--destructive"
            title=${this.t("removeOverlay")}
            @click=${() => {
              sceneManager?.removeOverlay(overlay.sourceId);
              this._changed();
            }}
          >
            ${xIcon(14)}
          </button>
        </div>
        ${text
          ? html`
              ${this._field(
                this.t("overlayText"),
                html`<textarea
                  rows="2"
                  .value=${text.text}
                  @input=${(event: Event) =>
                    updateText({ text: (event.target as HTMLTextAreaElement).value })}
                ></textarea>`
              )}
              ${this._field(
                this.t("overlayFontSize"),
                html`<input
                  type="range"
                  min=${TEXT_OVERLAY_DEFAULTS.minFontSize}
                  max=${TEXT_OVERLAY_DEFAULTS.maxFontSize}
                  .value=${String(text.fontSize ?? TEXT_OVERLAY_DEFAULTS.fontSize)}
                  @input=${(event: Event) =>
                    updateText({ fontSize: Number((event.target as HTMLInputElement).value) })}
                />`
              )}
              ${this._field(
                this.t("overlayColor"),
                html`<input
                  type="color"
                  .value=${text.color ?? TEXT_OVERLAY_DEFAULTS.color}
                  @input=${(event: Event) =>
                    updateText({ color: (event.target as HTMLInputElement).value })}
                />`
              )}
            `
          : this._percent(overlay, this.t("overlaySize"), "size", 5)}
        ${this._percent(overlay, this.t("overlayPositionX"), "x", 0)}
        ${this._percent(overlay, this.t("overlayPositionY"), "y", 0)}
      </div>
    `;
  }

  protected render() {
    const sceneManager = this._sceneManager;
    const scene = sceneManager?.getActiveScene();
    if (!sceneManager || !scene) return nothing;
    const overlays = sceneManager.getOverlays(scene.id);

    return html`
      <div class="fw-sc-producer-section">
        <div class="fw-sc-producer-section-header">
          <span class="fw-sc-producer-section-title">${this.t("overlays")}</span>
          <div class="fw-sc-producer-section-actions">
            <label class="fw-sc-producer-btn">
              ${this.t("addLogo")}
              <input
                class="fw-sc-producer-file"
                type="file"
                accept="image/*"
                @change=${(event: Event) => {
                  const file = takeFile(event);
                  if (file) this.addImage(file);
                }}
              />
            </label>
            <button
              type="button"
              class="fw-sc-producer-btn"
              @click=${() => {
                sceneManager.addTextOverlay(scene.id, { text: this.t("overlayDefaultText") });
                this._changed();
              }}
            >
              ${this.t("addText")}
            </button>
          </div>
        </div>
        ${this._imageFailed
          ? html`<p class="fw-sc-producer-error" role="alert">${this.t("overlayImageError")}</p>`
          : nothing}
        ${overlays.length === 0
          ? html`<p class="fw-sc-producer-empty">${this.t("noOverlays")}</p>`
          : html`
              <div class="fw-sc-overlay-list">
                ${overlays.map((overlay) => this._renderOverlay(overlay))}
              </div>
            `}
        ${overlays.some((overlay) => overlay.kind === "image")
          ? html`<p class="fw-sc-producer-notice">${this.t("overlayLocalNotice")}</p>`
          : nothing}
      </div>
    `;
  }
}

declare global {
  interface HTMLElementTagNameMap {
    "fw-sc-overlay-controls": FwScOverlayControls;
  }
}
