/**
 * <fw-sc-advanced> — Advanced/dev mode side panel.
 * Port of AdvancedPanel.tsx from streamcrafter-react.
 */
import { LitElement, html, css, nothing } from "lit";
import { customElement, property, state } from "lit/decorators.js";
import { classMap } from "lit/directives/class-map.js";
import { sharedStyles } from "../styles/shared-styles.js";
import { utilityStyles } from "../styles/utility-styles.js";
import { xIcon } from "../icons/index.js";
import type { IngestControllerHost } from "../controllers/ingest-controller-host.js";
import {
  createEncoderConfig,
  getAudioConstraints,
  getEncoderSettings,
  type EncoderOverrides,
  type RendererType,
  type RendererStats,
  type StudioTranslateFn,
  createStudioTranslator,
} from "@livepeer-frameworks/streamcrafter-core";

type TabId = "audio" | "stats" | "info" | "compositor";

export interface AudioProcessingSettings {
  echoCancellation: boolean;
  noiseSuppression: boolean;
  autoGainControl: boolean;
}

interface SettingOption<T> {
  value: T;
  label: string;
}

const RESOLUTION_OPTIONS: SettingOption<string>[] = [
  { value: "3840x2160", label: "3840x2160 (4K)" },
  { value: "2560x1440", label: "2560x1440 (1440p)" },
  { value: "1920x1080", label: "1920x1080 (1080p)" },
  { value: "1280x720", label: "1280x720 (720p)" },
  { value: "854x480", label: "854x480 (480p)" },
  { value: "640x360", label: "640x360 (360p)" },
];

const VIDEO_BITRATE_OPTIONS: SettingOption<number>[] = [
  { value: 50_000_000, label: "50 Mbps" },
  { value: 35_000_000, label: "35 Mbps" },
  { value: 25_000_000, label: "25 Mbps" },
  { value: 15_000_000, label: "15 Mbps" },
  { value: 10_000_000, label: "10 Mbps" },
  { value: 8_000_000, label: "8 Mbps" },
  { value: 6_000_000, label: "6 Mbps" },
  { value: 4_000_000, label: "4 Mbps" },
  { value: 2_500_000, label: "2.5 Mbps" },
  { value: 2_000_000, label: "2 Mbps" },
  { value: 1_500_000, label: "1.5 Mbps" },
  { value: 1_000_000, label: "1 Mbps" },
  { value: 500_000, label: "500 kbps" },
];

const FRAMERATE_OPTIONS: SettingOption<number>[] = [
  { value: 120, label: "120 fps" },
  { value: 60, label: "60 fps" },
  { value: 30, label: "30 fps" },
  { value: 24, label: "24 fps" },
  { value: 15, label: "15 fps" },
];

const AUDIO_BITRATE_OPTIONS: SettingOption<number>[] = [
  { value: 320_000, label: "320 kbps" },
  { value: 256_000, label: "256 kbps" },
  { value: 192_000, label: "192 kbps" },
  { value: 128_000, label: "128 kbps" },
  { value: 96_000, label: "96 kbps" },
  { value: 64_000, label: "64 kbps" },
];

function formatBitrate(bps: number): string {
  if (bps >= 1_000_000) return `${(bps / 1_000_000).toFixed(1)} Mbps`;
  return `${(bps / 1000).toFixed(0)} kbps`;
}

function hasAnyDefinedValue(object?: Record<string, unknown>): boolean {
  if (!object) return false;
  return Object.values(object).some((value) => value !== undefined);
}

@customElement("fw-sc-advanced")
export class FwScAdvanced extends LitElement {
  /** ID of a `<fw-streamcrafter>` to bind to (for standalone usage). */
  @property({ type: String, attribute: "for" }) for: string = "";
  @property({ attribute: false }) ic!: IngestControllerHost;
  @property({ type: String, attribute: "whip-url" }) whipUrl = "";
  @property({ attribute: false }) audioProcessing: AudioProcessingSettings = {
    echoCancellation: true,
    noiseSuppression: true,
    autoGainControl: true,
  };
  @property({ attribute: false }) encoderOverrides: EncoderOverrides = {};
  @property({ type: Boolean, attribute: "compositor-enabled" }) compositorEnabled = false;
  @property({ type: String, attribute: "compositor-renderer" })
  compositorRendererType: RendererType | null = null;
  @property({ attribute: false }) compositorStats: RendererStats | null = null;
  @property({ type: Number, attribute: "scene-count" }) sceneCount = 0;
  @property({ type: Number, attribute: "layer-count" }) layerCount = 0;
  @property({ attribute: false }) t: StudioTranslateFn = createStudioTranslator({ locale: "en" });

  @state() private _activeTab: TabId = "audio";

  static styles = [
    sharedStyles,
    utilityStyles,
    css`
      :host {
        display: block;
      }
      .panel {
        width: 280px;
        height: 100%;
        border-left: 1px solid hsl(var(--fw-sc-border) / 0.5);
        background: hsl(var(--fw-sc-surface-deep));
        display: flex;
        flex-direction: column;
        font-size: 12px;
        font-family:
          ui-monospace,
          SFMono-Regular,
          SF Mono,
          Menlo,
          Consolas,
          monospace;
        color: hsl(var(--fw-sc-text-muted));
        flex-shrink: 0;
        z-index: 40;
      }
      .header {
        display: flex;
        align-items: center;
        border-bottom: 1px solid hsl(var(--fw-sc-border) / 0.3);
        background: hsl(var(--fw-sc-surface));
      }
      .tab {
        padding: 8px 12px;
        font-size: 10px;
        text-transform: uppercase;
        letter-spacing: 0.05em;
        font-weight: 600;
        border: none;
        background: transparent;
        color: hsl(var(--fw-sc-text-faint));
        cursor: pointer;
        transition: all 0.15s;
      }
      .tab--active {
        background: hsl(var(--fw-sc-surface-deep));
        color: hsl(var(--fw-sc-text));
      }
      .close {
        display: flex;
        background: transparent;
        border: none;
        color: hsl(var(--fw-sc-text-faint));
        cursor: pointer;
        padding: 8px;
        transition: color 0.15s;
      }
      .close:hover {
        color: hsl(var(--fw-sc-text));
      }
      .body {
        flex: 1;
        overflow-y: auto;
      }
      .section-header {
        font-size: 10px;
        color: hsl(var(--fw-sc-text-faint));
        text-transform: uppercase;
        letter-spacing: 0.05em;
        font-weight: 600;
        margin-bottom: 8px;
      }
      .section {
        padding: 12px;
        border-bottom: 1px solid hsl(var(--fw-sc-border) / 0.3);
      }
      .section-dark {
        padding: 8px 12px;
        background: hsl(var(--fw-sc-surface));
        display: flex;
        justify-content: space-between;
        align-items: center;
      }
      .row {
        display: flex;
        justify-content: space-between;
        align-items: center;
        padding: 8px 12px;
        border-top: 1px solid hsl(var(--fw-sc-border) / 0.2);
      }
      .row-label {
        color: hsl(var(--fw-sc-text-faint));
      }
      .row-value {
        color: hsl(var(--fw-sc-text));
        font-family: ui-monospace, monospace;
        font-variant-numeric: tabular-nums;
      }
      .level-bar {
        height: 8px;
        background: hsl(var(--fw-sc-border) / 0.3);
        border-radius: 4px;
        overflow: hidden;
      }
      .level-fill {
        height: 100%;
        transition: all 75ms;
      }
      .level-labels {
        display: flex;
        justify-content: space-between;
        font-size: 10px;
        color: hsl(var(--fw-sc-text-faint));
        margin-top: 4px;
      }
      .badge {
        font-size: 12px;
        font-family: monospace;
        padding: 2px 6px;
      }
      .toggle {
        position: relative;
        display: inline-flex;
        height: 20px;
        width: 36px;
        flex-shrink: 0;
        cursor: pointer;
        border-radius: 10px;
        border: 2px solid transparent;
        transition: background 0.2s;
        padding: 0;
      }
      .toggle:disabled {
        opacity: 0.5;
        cursor: not-allowed;
      }
      .toggle-knob {
        position: absolute;
        top: 2px;
        width: 12px;
        height: 12px;
        border-radius: 50%;
        background: white;
        transition: left 0.2s;
      }
      .toggle--on {
        background: hsl(var(--fw-sc-accent));
      }
      .toggle--off {
        background: hsl(var(--fw-sc-border) / 0.5);
      }
      .toggle--on .toggle-knob {
        left: 18px;
      }
      .toggle--off .toggle-knob {
        left: 4px;
      }
      .processing-row {
        display: flex;
        justify-content: space-between;
        align-items: center;
        padding: 10px 12px;
        border-top: 1px solid hsl(var(--fw-sc-border) / 0.2);
      }
      .processing-label {
        font-size: 12px;
        color: hsl(var(--fw-sc-text));
      }
      .processing-desc {
        font-size: 10px;
        color: hsl(var(--fw-sc-text-faint));
        margin-top: 2px;
      }
      .source-type {
        font-size: 10px;
        font-family: monospace;
        padding: 2px 6px;
        text-transform: uppercase;
      }
      .select {
        background: hsl(var(--fw-sc-border) / 0.3);
        border: 1px solid hsl(var(--fw-sc-border) / 0.5);
        border-radius: 4px;
        color: hsl(var(--fw-sc-text));
        padding: 4px 8px;
        font-size: 12px;
        font-family: inherit;
        min-width: 100px;
      }
      .select--overridden {
        background: hsl(var(--fw-sc-accent-secondary) / 0.15);
        border-color: hsl(var(--fw-sc-accent-secondary) / 0.4);
        color: hsl(var(--fw-sc-accent-secondary));
      }
      .mini-button {
        margin-top: 8px;
        font-size: 10px;
        color: hsl(var(--fw-sc-text-faint));
        background: transparent;
        border: none;
        cursor: pointer;
        padding: 0;
      }
      .info-copy {
        font-size: 10px;
        color: hsl(var(--fw-sc-text-faint));
        line-height: 1.5;
      }
      .modified-badge {
        font-size: 8px;
        font-weight: 600;
        text-transform: uppercase;
        letter-spacing: 0.05em;
        color: hsl(var(--fw-sc-warning));
        background: hsl(var(--fw-sc-warning) / 0.2);
        padding: 2px 4px;
      }
    `,
  ];

  protected render() {
    if (!this.ic) return nothing;

    return html`
      <div class="panel">
        <div class="header">
          <button
            class=${classMap({ tab: true, "tab--active": this._activeTab === "audio" })}
            @click=${() => {
              this._activeTab = "audio";
            }}
          >
            ${this.t("audio")}
          </button>
          <button
            class=${classMap({ tab: true, "tab--active": this._activeTab === "stats" })}
            @click=${() => {
              this._activeTab = "stats";
            }}
          >
            ${this.t("stats")}
          </button>
          <button
            class=${classMap({ tab: true, "tab--active": this._activeTab === "info" })}
            @click=${() => {
              this._activeTab = "info";
            }}
          >
            ${this.t("info")}
          </button>
          ${this.compositorEnabled
            ? html`
                <button
                  class=${classMap({
                    tab: true,
                    "tab--active": this._activeTab === "compositor",
                  })}
                  @click=${() => {
                    this._activeTab = "compositor";
                  }}
                >
                  ${this.t("comp")}
                </button>
              `
            : nothing}
          <div style="flex:1"></div>
          <button
            class="close"
            @click=${() =>
              this.dispatchEvent(new CustomEvent("fw-close", { bubbles: true, composed: true }))}
            aria-label=${this.t("closeAdvancedPanel")}
          >
            ${xIcon(12)}
          </button>
        </div>
        <div class="body">
          ${this._activeTab === "audio"
            ? this._renderAudio()
            : this._activeTab === "stats"
              ? this._renderStats()
              : this._activeTab === "info"
                ? this._renderInfo()
                : this._renderCompositor()}
        </div>
      </div>
    `;
  }

  private _renderAudio() {
    const s = this.ic.s;
    const masterVolume = this.ic.getMasterVolume();
    const audioLevel = s.audioLevel;
    const levelColor =
      audioLevel > 0.9
        ? "hsl(var(--fw-sc-danger))"
        : audioLevel > 0.7
          ? "hsl(var(--fw-sc-warning))"
          : "hsl(var(--fw-sc-success))";
    const volColor =
      masterVolume > 1
        ? "hsl(var(--fw-sc-warning))"
        : masterVolume === 1
          ? "hsl(var(--fw-sc-success))"
          : "hsl(var(--fw-sc-text))";

    const profileDefaults = getAudioConstraints(s.qualityProfile);
    const processing = {
      echoCancellation: this.audioProcessing.echoCancellation,
      noiseSuppression: this.audioProcessing.noiseSuppression,
      autoGainControl: this.audioProcessing.autoGainControl,
    };
    const toggles = [
      {
        key: "echoCancellation" as const,
        label: this.t("echoCancellation"),
        description: this.t("echoCancellationDesc"),
      },
      {
        key: "noiseSuppression" as const,
        label: this.t("noiseSuppression"),
        description: this.t("noiseSuppressionDesc"),
      },
      {
        key: "autoGainControl" as const,
        label: this.t("autoGainControl"),
        description: this.t("autoGainControlDesc"),
      },
    ];

    return html`
      <div class="section">
        <div class="section-header">${this.t("masterVolume")}</div>
        <div style="display:flex;align-items:center;gap:12px">
          <fw-sc-volume
            .value=${masterVolume}
            .min=${0}
            .max=${2}
            @fw-sc-volume-change=${(e: CustomEvent<{ value: number }>) =>
              this.ic.setMasterVolume(e.detail.value)}
          ></fw-sc-volume>
          <span style="font-size:14px;min-width:48px;text-align:right;color:${volColor}">
            ${Math.round(masterVolume * 100)}%
          </span>
        </div>
        ${masterVolume > 1
          ? html`<div style="font-size:10px;color:hsl(var(--fw-sc-warning));margin-top:4px">
              +${((masterVolume - 1) * 100).toFixed(0)}% boost
            </div>`
          : nothing}
      </div>

      <div class="section">
        <div class="section-header">${this.t("outputLevel")}</div>
        <div class="level-bar">
          <div class="level-fill" style="width:${audioLevel * 100}%;background:${levelColor}"></div>
        </div>
        <div class="level-labels"><span>-60dB</span><span>0dB</span></div>
      </div>

      <div class="section">
        <div style="display:flex;justify-content:space-between;align-items:center">
          <span class="section-header" style="margin-bottom:0">${this.t("audioMixing")}</span>
          <span
            class="badge"
            style="background:hsl(var(--fw-sc-success) / 0.2);color:hsl(var(--fw-sc-success))"
          >
            ${this.t("on")}
          </span>
        </div>
        <div style="font-size:10px;color:hsl(var(--fw-sc-text-faint));margin-top:4px">
          ${this.t("compressorLimiterActive")}
        </div>
      </div>

      <div style="border-bottom:1px solid hsl(var(--fw-sc-border) / 0.3)">
        <div class="section-dark">
          <span class="section-header" style="margin-bottom:0">${this.t("processing")}</span>
          <span style="font-size:9px;color:hsl(var(--fw-sc-text-faint))">
            profile: ${s.qualityProfile}
          </span>
        </div>
        ${toggles.map(({ key, label, description }) => {
          const isModified = processing[key] !== profileDefaults[key];
          return html`
            <div class="processing-row">
              <div style="display:flex;flex-direction:column;gap:0;min-width:0;flex:1">
                <div style="display:flex;align-items:center;gap:8px">
                  <span class="processing-label">${label}</span>
                  ${isModified
                    ? html`<span class="modified-badge">${this.t("modified")}</span>`
                    : nothing}
                </div>
                <div class="processing-desc">${description}</div>
              </div>
              <button
                type="button"
                role="switch"
                aria-checked=${processing[key]}
                class="toggle ${processing[key] ? "toggle--on" : "toggle--off"}"
                @click=${() =>
                  this._emitAudioProcessingChange({
                    [key]: !processing[key],
                  })}
              >
                <div class="toggle-knob"></div>
              </button>
            </div>
          `;
        })}
        <div class="row">
          <span class="row-label">${this.t("sampleRate")}</span>
          <span class="row-value">${profileDefaults.sampleRate} Hz</span>
        </div>
        <div class="row">
          <span class="row-label">${this.t("channels")}</span>
          <span class="row-value">${profileDefaults.channelCount}</span>
        </div>
      </div>
    `;
  }

  private _renderStats() {
    const s = this.ic.s;
    const stats = s.stats;
    const stateColor =
      s.state === "streaming"
        ? "hsl(var(--fw-sc-success))"
        : s.state === "connecting"
          ? "hsl(var(--fw-sc-accent))"
          : s.state === "error"
            ? "hsl(var(--fw-sc-danger))"
            : "hsl(var(--fw-sc-text))";

    return html`
      <div class="section">
        <div class="section-header" style="margin-bottom:4px">${this.t("connection")}</div>
        <div style="font-size:14px;font-weight:600;color:${stateColor}">
          ${s.state.charAt(0).toUpperCase() + s.state.slice(1)}
        </div>
      </div>
      ${stats
        ? html`
            <div class="row">
              <span class="row-label">${this.t("bitrate")}</span>
              <span class="row-value">
                ${formatBitrate(stats.video.bitrate + stats.audio.bitrate)}
              </span>
            </div>
            <div class="row">
              <span class="row-label">${this.t("video")}</span>
              <span class="row-value" style="color:hsl(var(--fw-sc-accent))">
                ${formatBitrate(stats.video.bitrate)}
              </span>
            </div>
            <div class="row">
              <span class="row-label">${this.t("audio")}</span>
              <span class="row-value" style="color:hsl(var(--fw-sc-accent))">
                ${formatBitrate(stats.audio.bitrate)}
              </span>
            </div>
            <div class="row">
              <span class="row-label">${this.t("frameRate")}</span>
              <span class="row-value"> ${stats.video.framesPerSecond.toFixed(0)} fps </span>
            </div>
            <div class="row">
              <span class="row-label">${this.t("framesEncoded")}</span>
              <span class="row-value">${stats.video.framesEncoded}</span>
            </div>
            ${stats.video.packetsLost > 0 || stats.audio.packetsLost > 0
              ? html`
                  <div class="row">
                    <span class="row-label">${this.t("packetsLost")}</span>
                    <span class="row-value" style="color:hsl(var(--fw-sc-danger))">
                      ${stats.video.packetsLost + stats.audio.packetsLost}
                    </span>
                  </div>
                `
              : nothing}
            <div class="row">
              <span class="row-label">${this.t("rtt")}</span>
              <span
                class="row-value"
                style="color:${stats.connection.rtt > 200
                  ? "hsl(var(--fw-sc-warning))"
                  : "hsl(var(--fw-sc-text))"}"
              >
                ${stats.connection.rtt.toFixed(0)} ms
              </span>
            </div>
            <div class="row">
              <span class="row-label">${this.t("iceState")}</span>
              <span class="row-value" style="text-transform:capitalize">
                ${stats.connection.iceState}
              </span>
            </div>
          `
        : html`
            <div style="color:hsl(var(--fw-sc-text-faint));text-align:center;padding:24px">
              ${s.state === "streaming"
                ? this.t("waitingForStats")
                : this.t("startStreamingForStats")}
            </div>
          `}
      ${s.error
        ? html`
            <div
              style="padding:12px;border-top:1px solid hsl(var(--fw-sc-danger) / 0.3);background:hsl(var(--fw-sc-danger) / 0.1)"
            >
              <div class="section-header" style="color:hsl(var(--fw-sc-danger));margin-bottom:4px">
                ${this.t("error")}
              </div>
              <div style="font-size:12px;color:hsl(var(--fw-sc-danger))">${s.error}</div>
            </div>
          `
        : nothing}
    `;
  }

  private _renderInfo() {
    const s = this.ic.s;
    const profileEncoderSettings = getEncoderSettings(s.qualityProfile);
    const effectiveEncoderConfig = createEncoderConfig(
      s.qualityProfile === "auto" ? "broadcast" : s.qualityProfile,
      this.encoderOverrides
    );
    const videoTrackSettings = s.mediaStream?.getVideoTracks?.()[0]?.getSettings?.();
    const hasEncoderOverrides =
      hasAnyDefinedValue(this.encoderOverrides.video as Record<string, unknown>) ||
      hasAnyDefinedValue(this.encoderOverrides.audio as Record<string, unknown>);
    const currentResolution = `${this.encoderOverrides.video?.width ?? profileEncoderSettings.video.width}x${this.encoderOverrides.video?.height ?? profileEncoderSettings.video.height}`;

    return html`
      <div class="section">
        <div class="section-header" style="margin-bottom:4px">${this.t("qualityProfile")}</div>
        <div style="font-size:14px;color:hsl(var(--fw-sc-text));text-transform:capitalize">
          ${s.qualityProfile}
        </div>
        <div style="font-size:10px;color:hsl(var(--fw-sc-text-faint));margin-top:4px">
          ${profileEncoderSettings.video.width}x${profileEncoderSettings.video.height} @
          ${formatBitrate(profileEncoderSettings.video.bitrate)}
        </div>
      </div>

      <div class="section">
        <div class="section-header" style="margin-bottom:4px">${this.t("whipEndpoint")}</div>
        <div style="font-size:12px;color:hsl(var(--fw-sc-accent));word-break:break-all">
          ${this.whipUrl || this.t("notConfigured")}
        </div>
        ${this.whipUrl
          ? html`
              <button type="button" class="mini-button" @click=${() => this._copyWhipUrl()}>
                ${this.t("copyUrl")}
              </button>
            `
          : nothing}
      </div>

      <div style="border-bottom:1px solid hsl(var(--fw-sc-border) / 0.3)">
        <div class="section-dark">
          <span class="section-header" style="margin-bottom:0">${this.t("encoder")}</span>
          ${hasEncoderOverrides
            ? html`
                <button
                  type="button"
                  style="font-size:10px;color:hsl(var(--fw-sc-accent-secondary));background:transparent;border:none;cursor:pointer;padding:2px 6px"
                  @click=${() => this._emitEncoderOverridesChange({})}
                >
                  ${this.t("resetToProfile")}
                </button>
              `
            : nothing}
        </div>
        <div class="row">
          <span class="row-label">${this.t("videoCodec")}</span>
          <span class="row-value">${effectiveEncoderConfig.video.codec}</span>
        </div>
        <div class="row">
          <span class="row-label">${this.t("resolution")}</span>
          <select
            class=${classMap({
              select: true,
              "select--overridden":
                this.encoderOverrides.video?.width !== undefined ||
                this.encoderOverrides.video?.height !== undefined,
            })}
            .value=${currentResolution}
            ?disabled=${s.state === "streaming"}
            @change=${(e: Event) => {
              const value = (e.target as HTMLSelectElement).value;
              const [w, h] = value.split("x").map((part) => Number(part));
              const isProfileDefault =
                w === profileEncoderSettings.video.width &&
                h === profileEncoderSettings.video.height;
              const next: EncoderOverrides = {
                ...this.encoderOverrides,
                video: {
                  ...this.encoderOverrides.video,
                  width: isProfileDefault ? undefined : w,
                  height: isProfileDefault ? undefined : h,
                },
              };
              this._emitEncoderOverridesChange(next);
            }}
          >
            ${RESOLUTION_OPTIONS.map(
              (option) => html`<option .value=${option.value}>${option.label}</option>`
            )}
          </select>
        </div>
        ${videoTrackSettings?.width && videoTrackSettings?.height
          ? html`
              <div class="row">
                <span class="row-label">${this.t("actualResolution")}</span>
                <span class="row-value">
                  ${Math.round(videoTrackSettings.width)}x${Math.round(videoTrackSettings.height)}
                </span>
              </div>
            `
          : nothing}
        <div class="row">
          <span class="row-label">${this.t("framerate")}</span>
          <select
            class=${classMap({
              select: true,
              "select--overridden": this.encoderOverrides.video?.framerate !== undefined,
            })}
            .value=${String(
              this.encoderOverrides.video?.framerate ?? profileEncoderSettings.video.framerate
            )}
            ?disabled=${s.state === "streaming"}
            @change=${(e: Event) => {
              const value = Number((e.target as HTMLSelectElement).value);
              const isProfileDefault = value === profileEncoderSettings.video.framerate;
              const next: EncoderOverrides = {
                ...this.encoderOverrides,
                video: {
                  ...this.encoderOverrides.video,
                  framerate: isProfileDefault ? undefined : value,
                },
              };
              this._emitEncoderOverridesChange(next);
            }}
          >
            ${FRAMERATE_OPTIONS.map(
              (option) => html`<option .value=${String(option.value)}>${option.label}</option>`
            )}
          </select>
        </div>
        ${videoTrackSettings?.frameRate
          ? html`
              <div class="row">
                <span class="row-label">${this.t("actualFramerate")}</span>
                <span class="row-value">${Math.round(videoTrackSettings.frameRate)} fps</span>
              </div>
            `
          : nothing}
        <div class="row">
          <span class="row-label">${this.t("videoBitrate")}</span>
          <select
            class=${classMap({
              select: true,
              "select--overridden": this.encoderOverrides.video?.bitrate !== undefined,
            })}
            .value=${String(
              this.encoderOverrides.video?.bitrate ?? profileEncoderSettings.video.bitrate
            )}
            ?disabled=${s.state === "streaming"}
            @change=${(e: Event) => {
              const value = Number((e.target as HTMLSelectElement).value);
              const isProfileDefault = value === profileEncoderSettings.video.bitrate;
              const next: EncoderOverrides = {
                ...this.encoderOverrides,
                video: {
                  ...this.encoderOverrides.video,
                  bitrate: isProfileDefault ? undefined : value,
                },
              };
              this._emitEncoderOverridesChange(next);
            }}
          >
            ${VIDEO_BITRATE_OPTIONS.map(
              (option) => html`<option .value=${String(option.value)}>${option.label}</option>`
            )}
          </select>
        </div>
        <div class="row">
          <span class="row-label">${this.t("audioCodec")}</span>
          <span class="row-value">${effectiveEncoderConfig.audio.codec}</span>
        </div>
        <div class="row">
          <span class="row-label">${this.t("audioBitrate")}</span>
          <select
            class=${classMap({
              select: true,
              "select--overridden": this.encoderOverrides.audio?.bitrate !== undefined,
            })}
            .value=${String(
              this.encoderOverrides.audio?.bitrate ?? profileEncoderSettings.audio.bitrate
            )}
            ?disabled=${s.state === "streaming"}
            @change=${(e: Event) => {
              const value = Number((e.target as HTMLSelectElement).value);
              const isProfileDefault = value === profileEncoderSettings.audio.bitrate;
              const next: EncoderOverrides = {
                ...this.encoderOverrides,
                audio: {
                  ...this.encoderOverrides.audio,
                  bitrate: isProfileDefault ? undefined : value,
                },
              };
              this._emitEncoderOverridesChange(next);
            }}
          >
            ${AUDIO_BITRATE_OPTIONS.map(
              (option) => html`<option .value=${String(option.value)}>${option.label}</option>`
            )}
          </select>
        </div>
        ${s.state === "streaming"
          ? html`
              <div style="padding:8px 12px;font-size:10px;color:hsl(var(--fw-sc-warning))">
                ${this.t("settingsLockedWhileStreaming")}
              </div>
            `
          : nothing}
      </div>

      <div style="border-bottom:1px solid hsl(var(--fw-sc-border) / 0.3)">
        <div class="section-dark">
          <span class="section-header" style="margin-bottom:0"
            >${this.t("sources")} (${s.sources.length})</span
          >
        </div>
        ${s.sources.length > 0
          ? s.sources.map(
              (source, index) => html`
                <div
                  style="padding:8px 12px;${index > 0
                    ? "border-top:1px solid hsl(var(--fw-sc-border) / 0.2)"
                    : ""}"
                >
                  <div style="display:flex;align-items:center;gap:8px">
                    <span
                      class="source-type"
                      style="background:${source.type === "camera"
                        ? "hsl(var(--fw-sc-accent) / 0.2)"
                        : source.type === "screen"
                          ? "hsl(var(--fw-sc-success) / 0.2)"
                          : "hsl(var(--fw-sc-warning) / 0.2)"};color:${source.type === "camera"
                        ? "hsl(var(--fw-sc-accent))"
                        : source.type === "screen"
                          ? "hsl(var(--fw-sc-success))"
                          : "hsl(var(--fw-sc-warning))"}"
                    >
                      ${source.type}
                    </span>
                    <span
                      style="color:hsl(var(--fw-sc-text));font-size:12px;overflow:hidden;text-overflow:ellipsis;white-space:nowrap"
                    >
                      ${source.label}
                    </span>
                  </div>
                  <div
                    style="display:flex;gap:12px;margin-top:4px;font-size:10px;color:hsl(var(--fw-sc-text-faint))"
                  >
                    <span>Vol: ${Math.round(source.volume * 100)}%</span>
                    ${source.muted
                      ? html`<span style="color:hsl(var(--fw-sc-danger))">${this.t("mute")}</span>`
                      : nothing}
                    ${!source.active
                      ? html`<span style="color:hsl(var(--fw-sc-warning))"
                          >${this.t("inactive")}</span
                        >`
                      : nothing}
                  </div>
                </div>
              `
            )
          : html`<div
              style="padding:16px 12px;color:hsl(var(--fw-sc-text-faint));text-align:center"
            >
              ${this.t("noSourcesAdded")}
            </div>`}
      </div>
    `;
  }

  private _renderCompositor() {
    const s = this.ic.s;
    const rt = this.compositorRendererType;
    const stats = this.compositorStats;
    const rendererColor =
      rt === "webgpu"
        ? "hsl(var(--fw-sc-accent-secondary))"
        : rt === "webgl"
          ? "hsl(var(--fw-sc-accent))"
          : "hsl(var(--fw-sc-success))";
    const rendererLabel =
      rt === "webgpu"
        ? "WebGPU"
        : rt === "webgl"
          ? "WebGL"
          : rt === "canvas2d"
            ? "Canvas2D"
            : this.t("notInitialized");

    return html`
      <div class="section">
        <div class="section-header">${this.t("renderer")}</div>
        <div style="font-size:14px;font-weight:600;color:${rendererColor}">${rendererLabel}</div>
        <div style="font-size:10px;color:hsl(var(--fw-sc-text-faint));margin-top:4px">
          ${this.t("setRendererHint")}
        </div>
      </div>

      ${stats
        ? html`
            <div style="border-bottom:1px solid hsl(var(--fw-sc-border) / 0.3)">
              <div class="section-dark">
                <span class="section-header" style="margin-bottom:0">${this.t("performance")}</span>
              </div>
              <div class="row">
                <span class="row-label">${this.t("frameRate")}</span>
                <span class="row-value">${stats.fps} fps</span>
              </div>
              <div class="row">
                <span class="row-label">${this.t("frameTime")}</span>
                <span
                  class="row-value"
                  style="color:${stats.frameTimeMs > 16
                    ? "hsl(var(--fw-sc-warning))"
                    : "hsl(var(--fw-sc-text))"}"
                >
                  ${stats.frameTimeMs.toFixed(2)} ms
                </span>
              </div>
              ${stats.gpuMemoryMB !== undefined
                ? html`
                    <div class="row">
                      <span class="row-label">${this.t("gpuMemory")}</span>
                      <span class="row-value"> ${stats.gpuMemoryMB.toFixed(1)} MB </span>
                    </div>
                  `
                : nothing}
            </div>
          `
        : nothing}

      <div style="border-bottom:1px solid hsl(var(--fw-sc-border) / 0.3)">
        <div class="section-dark">
          <span class="section-header" style="margin-bottom:0">${this.t("composition")}</span>
        </div>
        <div class="row">
          <span class="row-label">${this.t("scenes")}</span>
          <span class="row-value">${this.sceneCount}</span>
        </div>
        <div class="row">
          <span class="row-label">${this.t("layers")}</span>
          <span class="row-value">${this.layerCount}</span>
        </div>
      </div>

      <div style="border-bottom:1px solid hsl(var(--fw-sc-border) / 0.3)">
        <div class="section-dark">
          <span class="section-header" style="margin-bottom:0">${this.t("encoder")}</span>
        </div>
        <div class="row">
          <span class="row-label">${this.t("type")}</span>
          <span
            class="badge"
            style="background:${s.useWebCodecs && s.isWebCodecsAvailable
              ? "hsl(var(--fw-sc-accent-secondary) / 0.2)"
              : "hsl(var(--fw-sc-accent) / 0.2)"};color:${s.useWebCodecs && s.isWebCodecsAvailable
              ? "hsl(var(--fw-sc-accent-secondary))"
              : "hsl(var(--fw-sc-accent))"}"
          >
            ${s.useWebCodecs && s.isWebCodecsAvailable ? this.t("webCodecs") : this.t("browser")}
            ${s.state === "streaming"
              ? html`<span style="opacity:0.7;margin-left:4px">
                  ${s.isWebCodecsActive ? "(active)" : "(pending)"}
                </span>`
              : nothing}
          </span>
        </div>
        <div class="processing-row">
          <div style="display:flex;flex-direction:column;gap:2px;min-width:0;flex:1">
            <span class="processing-label">${this.t("useWebCodecs")}</span>
            <span class="processing-desc">${this.t("enableWebCodecsDesc")}</span>
          </div>
          <button
            type="button"
            role="switch"
            aria-checked=${s.useWebCodecs}
            class="toggle ${s.useWebCodecs ? "toggle--on" : "toggle--off"}"
            ?disabled=${s.state === "streaming" || !s.isWebCodecsAvailable}
            @click=${() => this.ic.setUseWebCodecs(!s.useWebCodecs)}
          >
            <div class="toggle-knob"></div>
          </button>
        </div>
        ${!s.isWebCodecsAvailable
          ? html`<div style="padding:8px 12px;font-size:10px;color:hsl(var(--fw-sc-danger))">
              ${this.t("webCodecsUnsupported")}
            </div>`
          : nothing}
        ${s.isWebCodecsAvailable &&
        s.state === "streaming" &&
        s.useWebCodecs !== s.isWebCodecsActive
          ? html`<div style="padding:8px 12px;font-size:10px;color:hsl(var(--fw-sc-warning))">
              ${this.t("changeTakesEffect")}
            </div>`
          : nothing}
      </div>

      ${s.isWebCodecsActive && s.encoderStats
        ? html`
            <div style="border-bottom:1px solid hsl(var(--fw-sc-border) / 0.3)">
              <div class="section-dark">
                <span class="section-header" style="margin-bottom:0"
                  >${this.t("encoderStats")}</span
                >
              </div>
              <div class="row">
                <span class="row-label">${this.t("videoFrames")}</span>
                <span class="row-value">${s.encoderStats.video.framesEncoded}</span>
              </div>
              <div class="row">
                <span class="row-label">${this.t("videoPending")}</span>
                <span
                  class="row-value"
                  style="color:${s.encoderStats.video.framesPending > 5
                    ? "hsl(var(--fw-sc-warning))"
                    : "hsl(var(--fw-sc-text))"}"
                >
                  ${s.encoderStats.video.framesPending}
                </span>
              </div>
              <div class="row">
                <span class="row-label">${this.t("videoBytes")}</span>
                <span class="row-value">
                  ${(s.encoderStats.video.bytesEncoded / 1024 / 1024).toFixed(2)} MB
                </span>
              </div>
              <div class="row">
                <span class="row-label">${this.t("audioSamples")}</span>
                <span class="row-value">${s.encoderStats.audio.samplesEncoded}</span>
              </div>
              <div class="row">
                <span class="row-label">${this.t("audioBytes")}</span>
                <span class="row-value">
                  ${(s.encoderStats.audio.bytesEncoded / 1024).toFixed(1)} KB
                </span>
              </div>
            </div>
          `
        : nothing}

      <div class="section">
        <div class="info-copy">
          ${s.useWebCodecs && s.isWebCodecsAvailable
            ? "WebCodecs encoder via RTCRtpScriptTransform provides lower latency and better encoding control."
            : "Browser's built-in MediaStream encoder. Enable WebCodecs toggle for advanced encoding."}
        </div>
      </div>
    `;
  }

  private _copyWhipUrl() {
    if (!this.whipUrl) return;
    navigator.clipboard.writeText(this.whipUrl).catch(console.error);
  }

  private _emitAudioProcessingChange(settings: Partial<AudioProcessingSettings>) {
    this.dispatchEvent(
      new CustomEvent("fw-audio-processing-change", {
        detail: { settings },
        bubbles: true,
        composed: true,
      })
    );
  }

  private _emitEncoderOverridesChange(overrides: EncoderOverrides) {
    const normalized: EncoderOverrides = { ...overrides };
    if (!hasAnyDefinedValue(normalized.video as Record<string, unknown>)) {
      delete normalized.video;
    }
    if (!hasAnyDefinedValue(normalized.audio as Record<string, unknown>)) {
      delete normalized.audio;
    }

    this.dispatchEvent(
      new CustomEvent("fw-encoder-overrides-change", {
        detail: { overrides: normalized },
        bubbles: true,
        composed: true,
      })
    );
  }
}

declare global {
  interface HTMLElementTagNameMap {
    "fw-sc-advanced": FwScAdvanced;
  }
}
