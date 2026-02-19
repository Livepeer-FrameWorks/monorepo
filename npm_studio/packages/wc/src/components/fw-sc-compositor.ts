/**
 * <fw-sc-compositor> — Compact floating compositor controls overlay.
 * Port of CompositorControls.tsx from streamcrafter-react.
 */
import { LitElement, html, css, nothing } from "lit";
import { customElement, property, state } from "lit/decorators.js";
import { classMap } from "lit/directives/class-map.js";
import { sharedStyles } from "../styles/shared-styles.js";
import { utilityStyles } from "../styles/utility-styles.js";
import {
  soloIcon,
  pipBRIcon,
  pipBLIcon,
  pipTRIcon,
  pipTLIcon,
  splitHIcon,
  splitVIcon,
  focusLIcon,
  focusRIcon,
  gridIcon,
  stackIcon,
  dualPipIcon,
  splitPipIcon,
  featuredIcon,
  featuredRIcon,
  letterboxIcon,
  cropIcon,
  stretchIcon,
} from "../icons/index.js";
import type {
  Layer,
  LayoutMode,
  LayoutConfig,
  ScalingMode,
  MediaSource,
  RendererType,
  RendererStats,
} from "@livepeer-frameworks/streamcrafter-core";
import {
  isLayoutAvailable,
  type StudioTranslateFn,
  createStudioTranslator,
} from "@livepeer-frameworks/streamcrafter-core";

interface LayoutPresetUI {
  mode: LayoutMode;
  label: string;
  icon: () => ReturnType<typeof soloIcon>;
  minSources: number;
}

const LAYOUT_PRESETS_UI: LayoutPresetUI[] = [
  { mode: "solo", label: "Solo", icon: soloIcon, minSources: 1 },
  { mode: "pip-br", label: "PiP ↘", icon: pipBRIcon, minSources: 2 },
  { mode: "pip-bl", label: "PiP ↙", icon: pipBLIcon, minSources: 2 },
  { mode: "pip-tr", label: "PiP ↗", icon: pipTRIcon, minSources: 2 },
  { mode: "pip-tl", label: "PiP ↖", icon: pipTLIcon, minSources: 2 },
  { mode: "split-h", label: "Split ⬌", icon: splitHIcon, minSources: 2 },
  { mode: "split-v", label: "Split ⬍", icon: splitVIcon, minSources: 2 },
  { mode: "focus-l", label: "Focus ◀", icon: focusLIcon, minSources: 2 },
  { mode: "focus-r", label: "Focus ▶", icon: focusRIcon, minSources: 2 },
  { mode: "pip-dual-br", label: "Main+2 PiP", icon: dualPipIcon, minSources: 3 },
  { mode: "split-pip-r", label: "Split+PiP", icon: splitPipIcon, minSources: 3 },
  { mode: "featured", label: "Featured", icon: featuredIcon, minSources: 3 },
  { mode: "featured-r", label: "Featured ▶", icon: featuredRIcon, minSources: 3 },
  { mode: "grid", label: "Grid", icon: gridIcon, minSources: 2 },
  { mode: "stack", label: "Stack", icon: stackIcon, minSources: 2 },
];

const SCALING_MODES: {
  mode: ScalingMode;
  icon: () => ReturnType<typeof letterboxIcon>;
  label: string;
}[] = [
  { mode: "letterbox", icon: letterboxIcon, label: "Letterbox (fit)" },
  { mode: "crop", icon: cropIcon, label: "Crop (fill)" },
  { mode: "stretch", icon: stretchIcon, label: "Stretch" },
];

@customElement("fw-sc-compositor")
export class FwScCompositor extends LitElement {
  /** ID of a `<fw-streamcrafter>` to bind to (for standalone usage). */
  @property({ type: String, attribute: "for" }) for: string = "";
  @property({ type: Boolean, attribute: "is-enabled" }) isEnabled = false;
  @property({ type: Boolean, attribute: "is-initialized" }) isInitialized = false;
  @property({ type: String, attribute: "renderer-type" }) rendererType: RendererType | null = null;
  @property({ attribute: false }) stats: RendererStats | null = null;
  @property({ attribute: false }) sources: MediaSource[] = [];
  @property({ attribute: false }) layers: Layer[] = [];
  @property({ attribute: false }) currentLayout: LayoutConfig | null = null;
  @property({ type: Boolean, attribute: "show-stats" }) showStats = true;
  @property({ attribute: false }) t: StudioTranslateFn = createStudioTranslator({ locale: "en" });

  @state() private _tooltipKey: string | null = null;
  @state() private _tooltipText = "";

  static styles = [
    sharedStyles,
    utilityStyles,
    css`
      :host {
        display: contents;
      }
    `,
  ];

  protected render() {
    if (!this.isEnabled || !this.isInitialized) {
      return nothing;
    }

    const visibleSourceCount = this.sources.filter((source) => {
      const layer = this.layers.find((candidate) => candidate.sourceId === source.id);
      return layer?.visible ?? true;
    }).length;
    const currentScalingMode = this.currentLayout?.scalingMode ?? "letterbox";

    const availableLayouts = LAYOUT_PRESETS_UI.filter((preset) =>
      isLayoutAvailable(preset.mode, visibleSourceCount)
    );

    return html`
      <div class="fw-sc-layout-overlay">
        <div class="fw-sc-layout-bar">
          <div class="fw-sc-layout-section">
            <span class="fw-sc-layout-label">${this.t("layout")}</span>
            <div class="fw-sc-layout-icons">
              ${availableLayouts.map(
                (preset) => html`
                  <div
                    class="fw-sc-tooltip-wrapper"
                    @mouseenter=${() => {
                      const isActive = this.currentLayout?.mode === preset.mode;
                      this._tooltipKey = `layout-${preset.mode}`;
                      this._tooltipText = isActive
                        ? `${preset.label} (click to swap)`
                        : preset.label;
                    }}
                    @mouseleave=${() => {
                      this._tooltipKey = null;
                    }}
                  >
                    <button
                      type="button"
                      class=${classMap({
                        "fw-sc-layout-icon": true,
                        "fw-sc-layout-icon--active": this.currentLayout?.mode === preset.mode,
                      })}
                      @click=${(e: MouseEvent) => {
                        e.stopPropagation();
                        this._handleLayoutSelect(preset.mode, e);
                      }}
                    >
                      ${preset.icon()}
                    </button>
                    ${this._tooltipKey === `layout-${preset.mode}`
                      ? html`<div class="fw-sc-tooltip">${this._tooltipText}</div>`
                      : nothing}
                  </div>
                `
              )}
            </div>
          </div>
          <div class="fw-sc-layout-separator"></div>
          <div class="fw-sc-layout-section">
            <span class="fw-sc-layout-label">${this.t("display")}</span>
            <div class="fw-sc-scaling-icons">
              ${SCALING_MODES.map(
                (sm) => html`
                  <div
                    class="fw-sc-tooltip-wrapper"
                    @mouseenter=${() => {
                      this._tooltipKey = `scaling-${sm.mode}`;
                      this._tooltipText = sm.label;
                    }}
                    @mouseleave=${() => {
                      this._tooltipKey = null;
                    }}
                  >
                    <button
                      type="button"
                      class=${classMap({
                        "fw-sc-layout-icon": true,
                        "fw-sc-layout-icon--active": currentScalingMode === sm.mode,
                      })}
                      @click=${(e: MouseEvent) => {
                        e.stopPropagation();
                        this._handleScalingModeChange(sm.mode);
                      }}
                    >
                      ${sm.icon()}
                    </button>
                    ${this._tooltipKey === `scaling-${sm.mode}`
                      ? html`<div class="fw-sc-tooltip">${this._tooltipText}</div>`
                      : nothing}
                  </div>
                `
              )}
            </div>
          </div>
          ${this.showStats && this.stats
            ? html`
                <div class="fw-sc-layout-separator"></div>
                <span class="fw-sc-layout-stats">
                  ${this.rendererType === "webgpu"
                    ? "GPU"
                    : this.rendererType === "webgl"
                      ? "GL"
                      : this.rendererType === "canvas2d"
                        ? "2D"
                        : ""}
                  ${this.stats.fps}fps
                </span>
              `
            : nothing}
        </div>
      </div>
    `;
  }

  private _handleLayoutSelect(mode: LayoutMode, e?: MouseEvent) {
    if (this.currentLayout?.mode === mode) {
      const direction = e?.shiftKey ? "backward" : "forward";
      this.dispatchEvent(
        new CustomEvent("fw-sc-cycle-source-order", {
          detail: { direction },
          bubbles: true,
          composed: true,
        })
      );
      return;
    }

    const layout: LayoutConfig = {
      mode,
      scalingMode: this.currentLayout?.scalingMode ?? "letterbox",
      pipScale: 0.25,
    };

    this.dispatchEvent(
      new CustomEvent("fw-sc-layout-apply", {
        detail: { layout },
        bubbles: true,
        composed: true,
      })
    );
  }

  private _handleScalingModeChange(scalingMode: ScalingMode) {
    if (!this.currentLayout) {
      return;
    }

    this.dispatchEvent(
      new CustomEvent("fw-sc-layout-apply", {
        detail: {
          layout: {
            ...this.currentLayout,
            scalingMode,
          },
        },
        bubbles: true,
        composed: true,
      })
    );
  }
}

declare global {
  interface HTMLElementTagNameMap {
    "fw-sc-compositor": FwScCompositor;
  }
}
