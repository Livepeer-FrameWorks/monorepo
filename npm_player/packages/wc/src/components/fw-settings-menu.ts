/**
 * <fw-settings-menu> — Mode, speed, quality, and captions settings popup.
 */
import { LitElement, html, css, nothing } from "lit";
import { customElement, property, state } from "lit/decorators.js";
import { classMap } from "lit/directives/class-map.js";
import { sharedStyles } from "../styles/shared-styles.js";
import { utilityStyles } from "../styles/utility-styles.js";
import {
  SPEED_PRESETS,
  supportsPlaybackRate as coreSupportsPlaybackRate,
  getAvailableLocales,
  getLocaleDisplayName,
} from "@livepeer-frameworks/player-core";
import type { PlaybackMode, FwLocale } from "@livepeer-frameworks/player-core";
import type { PlayerControllerHost } from "../controllers/player-controller-host.js";

@customElement("fw-settings-menu")
export class FwSettingsMenu extends LitElement {
  @property({ attribute: false }) pc!: PlayerControllerHost;
  @property({ type: Boolean }) open = false;
  @property({ type: String }) playbackMode: PlaybackMode = "auto";
  @property({ type: Boolean, attribute: "is-content-live" }) isContentLive = true;
  @property({ type: Number, attribute: "playback-rate" }) playbackRate?: number;
  @property({ type: String, attribute: "quality-value" }) qualityValue?: string;
  @property({ type: String, attribute: "caption-value" }) captionValue?: string;
  @property({ type: Boolean, attribute: "supports-playback-rate" }) supportsPlaybackRate?: boolean;
  @property({ attribute: "active-locale" }) activeLocale?: FwLocale;

  @state() private _playbackRate = 1;

  static styles = [
    sharedStyles,
    utilityStyles,
    css`
      :host {
        display: contents;
      }
    `,
  ];

  protected updated(): void {
    if (!this.open) {
      return;
    }

    if (Number.isFinite(this.playbackRate)) {
      this._playbackRate = this.playbackRate as number;
      return;
    }

    const video = this.pc?.s.videoElement;
    if (video && Number.isFinite(video.playbackRate)) {
      this._playbackRate = video.playbackRate;
    }
  }

  private _close(): void {
    this.dispatchEvent(new CustomEvent("fw-close", { bubbles: true, composed: true }));
  }

  private _handleModeChange(mode: "auto" | "low-latency" | "quality"): void {
    this.pc.setDevModeOptions({ playbackMode: mode });
    this.dispatchEvent(
      new CustomEvent("fw-mode-change", {
        detail: { mode },
        bubbles: true,
        composed: true,
      })
    );
    this._close();
  }

  private _handleSpeedChange(rate: number): void {
    this._playbackRate = rate;
    this.pc.setPlaybackRate(rate);
    this.dispatchEvent(
      new CustomEvent("fw-speed-change", {
        detail: { rate },
        bubbles: true,
        composed: true,
      })
    );
    this._close();
  }

  private _handleQualityChange(id: string): void {
    this.pc.selectQuality(id);
    this.dispatchEvent(
      new CustomEvent("fw-quality-change", {
        detail: { quality: id },
        bubbles: true,
        composed: true,
      })
    );
    this._close();
  }

  private _handleCaptionChange(id: string): void {
    if (id === "none") {
      this.pc.selectTextTrack(null);
    } else {
      this.pc.selectTextTrack(id);
    }
    this.dispatchEvent(
      new CustomEvent("fw-caption-change", {
        detail: { caption: id },
        bubbles: true,
        composed: true,
      })
    );
    this._close();
  }

  private _handleLocaleChange(locale: FwLocale): void {
    this.dispatchEvent(
      new CustomEvent("fw-locale-change", {
        detail: { locale },
        bubbles: true,
        composed: true,
      })
    );
  }

  private _deriveFallbackQualities(): Array<{
    id: string;
    label: string;
    bitrate?: number;
    width?: number;
    height?: number;
    isAuto?: boolean;
    active?: boolean;
  }> {
    const tracks = (
      this.pc?.s.streamState?.streamInfo as
        | {
            meta?: {
              tracks?: Record<
                string,
                { type?: string; codec?: string; width?: number; height?: number; bps?: number }
              >;
            };
          }
        | undefined
    )?.meta?.tracks;

    if (!tracks) {
      return [];
    }

    return Object.entries(tracks)
      .filter(([, track]) => track?.type === "video")
      .map(([id, track]) => ({
        id,
        label: track.height ? `${track.height}p` : (track.codec ?? id),
        width: track.width,
        height: track.height,
        bitrate: track.bps,
      }))
      .sort((a, b) => (b.height ?? 0) - (a.height ?? 0));
  }

  protected render() {
    if (!this.open) {
      return nothing;
    }

    const state = this.pc.s;
    const controllerQualities = state.qualities ?? [];
    const qualities =
      controllerQualities.length > 0 ? controllerQualities : this._deriveFallbackQualities();
    const textTracks = state.textTracks ?? [];
    const activeQuality =
      this.qualityValue ?? qualities.find((quality) => quality.active)?.id ?? "auto";
    const activeCaption =
      this.captionValue ?? textTracks.find((track) => track.active)?.id ?? "none";

    const supportsPlaybackRate =
      this.supportsPlaybackRate ?? coreSupportsPlaybackRate(state.videoElement);

    return html`
      <div class="fw-settings-menu" role="menu" aria-label=${this.pc.t("settings")}>
        ${this.isContentLive
          ? html`
              <div class="fw-settings-section">
                <div class="fw-settings-label">${this.pc.t("mode")}</div>
                <div class="fw-settings-options">
                  ${(["auto", "low-latency", "quality"] as const).map(
                    (mode) => html`
                      <button
                        type="button"
                        class=${classMap({
                          "fw-settings-btn": true,
                          "fw-settings-btn--active": this.playbackMode === mode,
                        })}
                        @click=${() => this._handleModeChange(mode)}
                      >
                        ${mode === "low-latency"
                          ? this.pc.t("fast")
                          : mode === "quality"
                            ? this.pc.t("stable")
                            : this.pc.t("auto")}
                      </button>
                    `
                  )}
                </div>
              </div>
            `
          : nothing}
        ${supportsPlaybackRate
          ? html`
              <div class="fw-settings-section">
                <div class="fw-settings-label">${this.pc.t("speed")}</div>
                <div class="fw-settings-options fw-settings-options--wrap">
                  ${SPEED_PRESETS.map(
                    (rate) => html`
                      <button
                        type="button"
                        class=${classMap({
                          "fw-settings-btn": true,
                          "fw-settings-btn--active": this._playbackRate === rate,
                        })}
                        @click=${() => this._handleSpeedChange(rate)}
                      >
                        ${rate}x
                      </button>
                    `
                  )}
                </div>
              </div>
            `
          : nothing}
        ${qualities.length > 0
          ? html`
              <div class="fw-settings-section">
                <div class="fw-settings-label">${this.pc.t("quality")}</div>
                <div class="fw-settings-list">
                  <button
                    type="button"
                    class=${classMap({
                      "fw-settings-list-item": true,
                      "fw-settings-list-item--active": activeQuality === "auto",
                    })}
                    @click=${() => this._handleQualityChange("auto")}
                  >
                    ${this.pc.t("auto")}
                  </button>
                  ${qualities.map(
                    (quality) => html`
                      <button
                        type="button"
                        class=${classMap({
                          "fw-settings-list-item": true,
                          "fw-settings-list-item--active": activeQuality === quality.id,
                        })}
                        @click=${() => this._handleQualityChange(quality.id)}
                      >
                        ${quality.label}
                      </button>
                    `
                  )}
                </div>
              </div>
            `
          : nothing}
        ${textTracks.length > 0
          ? html`
              <div class="fw-settings-section">
                <div class="fw-settings-label">${this.pc.t("captions")}</div>
                <div class="fw-settings-list">
                  <button
                    type="button"
                    class=${classMap({
                      "fw-settings-list-item": true,
                      "fw-settings-list-item--active": activeCaption === "none",
                    })}
                    @click=${() => this._handleCaptionChange("none")}
                  >
                    ${this.pc.t("captionsOff")}
                  </button>
                  ${textTracks.map(
                    (track) => html`
                      <button
                        type="button"
                        class=${classMap({
                          "fw-settings-list-item": true,
                          "fw-settings-list-item--active": activeCaption === track.id,
                        })}
                        @click=${() => this._handleCaptionChange(track.id)}
                      >
                        ${track.label || track.id}
                      </button>
                    `
                  )}
                </div>
              </div>
            `
          : nothing}
        ${this.activeLocale !== undefined
          ? html`
              <div class="fw-settings-section">
                <div class="fw-settings-label">${this.pc.t("language")}</div>
                <div class="fw-settings-list">
                  ${getAvailableLocales().map(
                    (loc) => html`
                      <button
                        type="button"
                        class=${classMap({
                          "fw-settings-list-item": true,
                          "fw-settings-list-item--active": this.activeLocale === loc,
                        })}
                        @click=${() => this._handleLocaleChange(loc)}
                      >
                        ${getLocaleDisplayName(loc)}
                      </button>
                    `
                  )}
                </div>
              </div>
            `
          : nothing}
      </div>
    `;
  }
}

declare global {
  interface HTMLElementTagNameMap {
    "fw-settings-menu": FwSettingsMenu;
  }
}
