<script lang="ts">
  import { getContext } from "svelte";
  import type { Readable } from "svelte/store";
  import CameraIcon from "../icons/CameraIcon.svelte";
  import EyeIcon from "../icons/EyeIcon.svelte";
  import MonitorIcon from "../icons/MonitorIcon.svelte";
  import SettingsIcon from "../icons/SettingsIcon.svelte";
  import VideoIcon from "../icons/VideoIcon.svelte";
  import type { Layer, LayerTransform, MediaSource } from "@livepeer-frameworks/streamcrafter-core";
  import {
    createStudioTranslator,
    type StudioTranslateFn,
  } from "@livepeer-frameworks/streamcrafter-core";

  interface Props {
    layers: Layer[];
    sources: MediaSource[];
    onVisibilityToggle: (layerId: string, visible: boolean) => void;
    onReorder: (layerIds: string[]) => void;
    onTransformEdit?: (layerId: string, transform: Partial<LayerTransform>) => void;
    onRemove?: (layerId: string) => void;
    onSelect?: (layerId: string | null) => void;
    selectedLayerId?: string | null;
    class?: string;
  }

  let {
    layers = [],
    sources = [],
    onVisibilityToggle,
    onReorder,
    onTransformEdit,
    onRemove,
    onSelect,
    selectedLayerId = null,
    class: className = "",
  }: Props = $props();

  const translatorCtx = getContext<Readable<StudioTranslateFn> | undefined>("fw-sc-translator");
  const fallbackT = createStudioTranslator({ locale: "en" });
  let t: StudioTranslateFn = $derived(translatorCtx ? $translatorCtx : fallbackT);

  let draggedId = $state<string | null>(null);
  let dragOverId = $state<string | null>(null);
  let editingLayerId = $state<string | null>(null);

  let sortedLayers = $derived([...layers].sort((a, b) => b.zIndex - a.zIndex));

  function getSourceLabel(sourceId: string): string {
    const source = sources.find((candidate) => candidate.id === sourceId);
    return source?.label || sourceId;
  }

  function getSourceType(sourceId: string): MediaSource["type"] | undefined {
    return sources.find((candidate) => candidate.id === sourceId)?.type;
  }

  function handleDragStart(e: DragEvent, layerId: string) {
    draggedId = layerId;
    if (!e.dataTransfer) return;
    e.dataTransfer.effectAllowed = "move";
    e.dataTransfer.setData("text/plain", layerId);
  }

  function handleDragOver(e: DragEvent, layerId: string) {
    e.preventDefault();
    if (e.dataTransfer) {
      e.dataTransfer.dropEffect = "move";
    }
    dragOverId = layerId;
  }

  function handleDrop(e: DragEvent, targetLayerId: string) {
    e.preventDefault();
    dragOverId = null;

    if (!draggedId || draggedId === targetLayerId) {
      draggedId = null;
      return;
    }

    const currentIds = sortedLayers.map((layer) => layer.id);
    const fromIndex = currentIds.indexOf(draggedId);
    const toIndex = currentIds.indexOf(targetLayerId);
    if (fromIndex === -1 || toIndex === -1) {
      draggedId = null;
      return;
    }

    const newOrder = [...currentIds];
    newOrder.splice(fromIndex, 1);
    newOrder.splice(toIndex, 0, draggedId);
    onReorder(newOrder);
    draggedId = null;
  }

  function handleDragEnd() {
    draggedId = null;
    dragOverId = null;
  }

  function handleMoveUp(layerId: string) {
    const currentIds = sortedLayers.map((layer) => layer.id);
    const index = currentIds.indexOf(layerId);
    if (index <= 0) return;

    const newOrder = [...currentIds];
    [newOrder[index - 1], newOrder[index]] = [newOrder[index], newOrder[index - 1]];
    onReorder(newOrder);
  }

  function handleMoveDown(layerId: string) {
    const currentIds = sortedLayers.map((layer) => layer.id);
    const index = currentIds.indexOf(layerId);
    if (index >= currentIds.length - 1) return;

    const newOrder = [...currentIds];
    [newOrder[index], newOrder[index + 1]] = [newOrder[index + 1], newOrder[index]];
    onReorder(newOrder);
  }

  function handleOpacityChange(layerId: string, opacity: number) {
    onTransformEdit?.(layerId, { opacity });
  }
</script>

<div class="fw-sc-layer-list {className}">
  <div class="fw-sc-layer-list-header">
    <span class="fw-sc-layer-list-title">{t("layers")}</span>
    <span class="fw-sc-layer-count">{layers.length}</span>
  </div>

  <div class="fw-sc-layer-items">
    {#if sortedLayers.length === 0}
      <div class="fw-sc-layer-empty">{t("noLayers")}</div>
    {:else}
      {#each sortedLayers as layer, index (layer.id)}
        {@const sourceType = getSourceType(layer.sourceId)}
        {@const opacity = layer.transform?.opacity ?? 1}
        <div
          class="fw-sc-layer-item {layer.id === selectedLayerId
            ? 'fw-sc-layer-item--selected'
            : ''} {layer.id === draggedId ? 'fw-sc-layer-item--dragging' : ''} {layer.id ===
          dragOverId
            ? 'fw-sc-layer-item--drag-over'
            : ''} {!layer.visible ? 'fw-sc-layer-item--hidden' : ''}"
          role="button"
          tabindex="0"
          draggable={true}
          ondragstart={(e) => handleDragStart(e, layer.id)}
          ondragover={(e) => handleDragOver(e, layer.id)}
          ondragleave={() => {
            dragOverId = null;
          }}
          ondrop={(e) => handleDrop(e, layer.id)}
          ondragend={handleDragEnd}
          onclick={() => onSelect?.(layer.id === selectedLayerId ? null : layer.id)}
          onkeydown={(e) => {
            if (e.key === "Enter" || e.key === " ") {
              e.preventDefault();
              onSelect?.(layer.id === selectedLayerId ? null : layer.id);
            }
          }}
        >
          <button
            class="fw-sc-layer-visibility {layer.visible ? 'fw-sc-layer-visibility--visible' : ''}"
            onclick={(e) => {
              e.stopPropagation();
              onVisibilityToggle(layer.id, !layer.visible);
            }}
            title={layer.visible ? t("hideLayer") : t("showLayer")}
          >
            <EyeIcon size={14} visible={layer.visible} />
          </button>

          <span class="fw-sc-layer-icon">
            {#if sourceType === "camera"}
              <CameraIcon size={14} />
            {:else if sourceType === "screen"}
              <MonitorIcon size={14} />
            {:else}
              <VideoIcon size={14} />
            {/if}
          </span>
          <span class="fw-sc-layer-name">{getSourceLabel(layer.sourceId)}</span>

          {#if editingLayerId === layer.id && onTransformEdit}
            <div class="fw-sc-layer-opacity">
              <input
                type="range"
                min="0"
                max="1"
                step="0.1"
                value={opacity}
                oninput={(e) =>
                  handleOpacityChange(layer.id, Number((e.target as HTMLInputElement).value))}
                onclick={(e) => e.stopPropagation()}
              />
              <span>{Math.round(opacity * 100)}%</span>
            </div>
          {/if}

          <div class="fw-sc-layer-controls">
            <button
              class="fw-sc-layer-btn"
              onclick={(e) => {
                e.stopPropagation();
                handleMoveUp(layer.id);
              }}
              disabled={index === 0}
              title={t("moveUp")}
            >
              ↑
            </button>

            <button
              class="fw-sc-layer-btn"
              onclick={(e) => {
                e.stopPropagation();
                handleMoveDown(layer.id);
              }}
              disabled={index === sortedLayers.length - 1}
              title={t("moveDown")}
            >
              ↓
            </button>

            {#if onTransformEdit}
              <button
                class="fw-sc-layer-btn {editingLayerId === layer.id
                  ? 'fw-sc-layer-btn--active'
                  : ''}"
                onclick={(e) => {
                  e.stopPropagation();
                  editingLayerId = editingLayerId === layer.id ? null : layer.id;
                }}
                title={t("editOpacity")}
              >
                <SettingsIcon size={12} />
              </button>
            {/if}

            {#if onRemove}
              <button
                class="fw-sc-layer-btn fw-sc-layer-btn--danger"
                onclick={(e) => {
                  e.stopPropagation();
                  onRemove(layer.id);
                }}
                title={t("removeLayer")}
              >
                ×
              </button>
            {/if}
          </div>
        </div>
      {/each}
    {/if}
  </div>
</div>
