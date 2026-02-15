/**
 * <fw-sc-layer-list> — Drag-to-reorder layer list.
 * Port of LayerList.tsx from streamcrafter-react.
 */
import { LitElement, html, css, nothing } from "lit";
import { customElement, property, state } from "lit/decorators.js";
import { classMap } from "lit/directives/class-map.js";
import { sharedStyles } from "../styles/shared-styles.js";
import { utilityStyles } from "../styles/utility-styles.js";
import { eyeIcon, eyeOffIcon, cameraIcon, monitorIcon, videoIcon } from "../icons/index.js";
import type { Layer, MediaSource, LayerTransform } from "@livepeer-frameworks/streamcrafter-core";

@customElement("fw-sc-layer-list")
export class FwScLayerList extends LitElement {
  @property({ attribute: false }) layers: Layer[] = [];
  @property({ attribute: false }) sources: MediaSource[] = [];
  @property({ type: String, attribute: "selected-layer-id" }) selectedLayerId: string | null = null;
  @property({ attribute: false }) onVisibilityToggle?: (layerId: string, visible: boolean) => void;
  @property({ attribute: false }) onReorder?: (layerIds: string[]) => void;
  @property({ attribute: false }) onTransformEdit?: (
    layerId: string,
    transform: Partial<LayerTransform>
  ) => void;
  @property({ attribute: false }) onRemove?: (layerId: string) => void;
  @property({ attribute: false }) onSelect?: (layerId: string | null) => void;

  @state() private _draggedId: string | null = null;
  @state() private _dragOverId: string | null = null;
  @state() private _editingLayerId: string | null = null;

  static styles = [
    sharedStyles,
    utilityStyles,
    css`
      :host {
        display: block;
      }
    `,
  ];

  private get _sortedLayers(): Layer[] {
    return [...this.layers].sort((a, b) => b.zIndex - a.zIndex);
  }

  private _getSourceLabel(sourceId: string): string {
    const source = this.sources.find((s) => s.id === sourceId);
    return source?.label || sourceId;
  }

  private _getSourceIcon(sourceId: string) {
    const source = this.sources.find((s) => s.id === sourceId);
    switch (source?.type) {
      case "camera":
        return cameraIcon(14);
      case "screen":
        return monitorIcon(14);
      default:
        return videoIcon(14);
    }
  }

  protected render() {
    const sorted = this._sortedLayers;
    return html`
      <div class="fw-sc-layer-list">
        <div class="fw-sc-layer-list-header">
          <span class="fw-sc-layer-list-title">Layers</span>
          <span class="fw-sc-layer-count">${this.layers.length}</span>
        </div>

        <div class="fw-sc-layer-items">
          ${sorted.length === 0
            ? html` <div class="fw-sc-layer-empty">No layers. Add a source to get started.</div> `
            : sorted.map(
                (layer, index) => html`
                  <div
                    class=${classMap({
                      "fw-sc-layer-item": true,
                      "fw-sc-layer-item--selected": layer.id === this.selectedLayerId,
                      "fw-sc-layer-item--dragging": layer.id === this._draggedId,
                      "fw-sc-layer-item--drag-over": layer.id === this._dragOverId,
                      "fw-sc-layer-item--hidden": !layer.visible,
                    })}
                    draggable="true"
                    @dragstart=${(e: DragEvent) => this._handleDragStart(e, layer.id)}
                    @dragover=${(e: DragEvent) => this._handleDragOver(e, layer.id)}
                    @dragleave=${() => {
                      this._dragOverId = null;
                    }}
                    @drop=${(e: DragEvent) => this._handleDrop(e, layer.id)}
                    @dragend=${() => {
                      this._draggedId = null;
                      this._dragOverId = null;
                    }}
                    @click=${() =>
                      this.onSelect?.(layer.id === this.selectedLayerId ? null : layer.id)}
                  >
                    <button
                      class=${classMap({
                        "fw-sc-layer-visibility": true,
                        "fw-sc-layer-visibility--visible": layer.visible,
                      })}
                      @click=${(e: Event) => {
                        e.stopPropagation();
                        this.onVisibilityToggle?.(layer.id, !layer.visible);
                      }}
                      title=${layer.visible ? "Hide layer" : "Show layer"}
                    >
                      ${layer.visible ? eyeIcon(14) : eyeOffIcon(14)}
                    </button>
                    <span class="fw-sc-layer-icon">${this._getSourceIcon(layer.sourceId)}</span>
                    <span class="fw-sc-layer-name">${this._getSourceLabel(layer.sourceId)}</span>

                    ${this._editingLayerId === layer.id && this.onTransformEdit
                      ? html`
                          <div class="fw-sc-layer-opacity">
                            <input
                              type="range"
                              min="0"
                              max="1"
                              step="0.1"
                              .value=${String(layer.transform.opacity)}
                              @input=${(e: Event) =>
                                this.onTransformEdit?.(layer.id, {
                                  opacity: Number((e.target as HTMLInputElement).value),
                                })}
                              @click=${(e: Event) => e.stopPropagation()}
                            />
                            <span>${Math.round(layer.transform.opacity * 100)}%</span>
                          </div>
                        `
                      : nothing}

                    <div class="fw-sc-layer-controls">
                      <button
                        class="fw-sc-layer-btn"
                        @click=${(e: Event) => {
                          e.stopPropagation();
                          this._moveUp(layer.id);
                        }}
                        ?disabled=${index === 0}
                        title="Move up"
                      >
                        ↑
                      </button>
                      <button
                        class="fw-sc-layer-btn"
                        @click=${(e: Event) => {
                          e.stopPropagation();
                          this._moveDown(layer.id);
                        }}
                        ?disabled=${index === sorted.length - 1}
                        title="Move down"
                      >
                        ↓
                      </button>
                      ${this.onTransformEdit
                        ? html`
                            <button
                              class=${classMap({
                                "fw-sc-layer-btn": true,
                                "fw-sc-layer-btn--active": this._editingLayerId === layer.id,
                              })}
                              @click=${(e: Event) => {
                                e.stopPropagation();
                                this._editingLayerId =
                                  this._editingLayerId === layer.id ? null : layer.id;
                              }}
                              title="Edit opacity"
                            >
                              ⚙
                            </button>
                          `
                        : nothing}
                      ${this.onRemove
                        ? html`
                            <button
                              class="fw-sc-layer-btn fw-sc-layer-btn--danger"
                              @click=${(e: Event) => {
                                e.stopPropagation();
                                this.onRemove?.(layer.id);
                              }}
                              title="Remove layer"
                            >
                              ×
                            </button>
                          `
                        : nothing}
                    </div>
                  </div>
                `
              )}
        </div>
      </div>
    `;
  }

  private _handleDragStart(e: DragEvent, layerId: string) {
    this._draggedId = layerId;
    e.dataTransfer!.effectAllowed = "move";
    e.dataTransfer!.setData("text/plain", layerId);
  }

  private _handleDragOver(e: DragEvent, layerId: string) {
    e.preventDefault();
    e.dataTransfer!.dropEffect = "move";
    this._dragOverId = layerId;
  }

  private _handleDrop(e: DragEvent, targetLayerId: string) {
    e.preventDefault();
    this._dragOverId = null;
    if (!this._draggedId || this._draggedId === targetLayerId) {
      this._draggedId = null;
      return;
    }
    const sorted = this._sortedLayers;
    const currentIds = sorted.map((l) => l.id);
    const fromIndex = currentIds.indexOf(this._draggedId);
    const toIndex = currentIds.indexOf(targetLayerId);
    if (fromIndex === -1 || toIndex === -1) {
      this._draggedId = null;
      return;
    }
    const newOrder = [...currentIds];
    newOrder.splice(fromIndex, 1);
    newOrder.splice(toIndex, 0, this._draggedId);
    this.onReorder?.(newOrder);
    this._draggedId = null;
  }

  private _moveUp(layerId: string) {
    const sorted = this._sortedLayers;
    const ids = sorted.map((l) => l.id);
    const idx = ids.indexOf(layerId);
    if (idx <= 0) return;
    [ids[idx - 1], ids[idx]] = [ids[idx], ids[idx - 1]];
    this.onReorder?.(ids);
  }

  private _moveDown(layerId: string) {
    const sorted = this._sortedLayers;
    const ids = sorted.map((l) => l.id);
    const idx = ids.indexOf(layerId);
    if (idx >= ids.length - 1) return;
    [ids[idx], ids[idx + 1]] = [ids[idx + 1], ids[idx]];
    this.onReorder?.(ids);
  }
}

declare global {
  interface HTMLElementTagNameMap {
    "fw-sc-layer-list": FwScLayerList;
  }
}
