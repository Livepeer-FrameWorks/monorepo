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
import type { IngestControllerHost } from "../controllers/ingest-controller-host.js";
import type {
  LayoutMode,
  LayoutConfig,
  ScalingMode,
} from "@livepeer-frameworks/streamcrafter-core";
import { isLayoutAvailable } from "@livepeer-frameworks/streamcrafter-core";

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
  @property({ attribute: false }) ic!: IngestControllerHost;

  @state() private _tooltipText = "";
  @state() private _tooltipTarget: Element | null = null;

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
    // Compositor uses controller compositor API — for now render layout bar from CSS classes
    const sources = this.ic.s.sources;
    const visibleSourceCount = sources.length;

    const availableLayouts = LAYOUT_PRESETS_UI.filter((preset) =>
      isLayoutAvailable(preset.mode, visibleSourceCount)
    );

    return html`
      <div class="fw-sc-layout-overlay">
        <div class="fw-sc-layout-bar">
          <div class="fw-sc-layout-section">
            <span class="fw-sc-layout-label">Layout</span>
            <div class="fw-sc-layout-icons">
              ${availableLayouts.map(
                (preset) => html`
                  <button
                    type="button"
                    class="fw-sc-layout-icon"
                    @click=${(e: MouseEvent) => {
                      e.stopPropagation();
                      this._handleLayoutSelect(preset.mode);
                    }}
                    title=${preset.label}
                  >
                    ${preset.icon()}
                  </button>
                `
              )}
            </div>
          </div>
          <div class="fw-sc-layout-separator"></div>
          <div class="fw-sc-layout-section">
            <span class="fw-sc-layout-label">Display</span>
            <div class="fw-sc-scaling-icons">
              ${SCALING_MODES.map(
                (sm) => html`
                  <button
                    type="button"
                    class="fw-sc-layout-icon"
                    @click=${(e: MouseEvent) => {
                      e.stopPropagation();
                    }}
                    title=${sm.label}
                  >
                    ${sm.icon()}
                  </button>
                `
              )}
            </div>
          </div>
        </div>
      </div>
    `;
  }

  private _handleLayoutSelect(mode: LayoutMode) {
    this.dispatchEvent(
      new CustomEvent("fw-sc-layout-select", {
        detail: { mode },
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
