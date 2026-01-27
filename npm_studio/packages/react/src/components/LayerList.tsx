/**
 * LayerList Component
 *
 * Vertical list of layers with drag-to-reorder support.
 * Each row shows visibility toggle, lock, source name, and controls.
 */

import React, { useState, useCallback } from "react";
import type { Layer, LayerTransform, MediaSource } from "@livepeer-frameworks/streamcrafter-core";

// SVG Icons
function EyeIcon() {
  return (
    <svg
      width="14"
      height="14"
      viewBox="0 0 24 24"
      fill="none"
      stroke="currentColor"
      strokeWidth="2"
      strokeLinecap="round"
      strokeLinejoin="round"
    >
      <path d="M1 12s4-8 11-8 11 8 11 8-4 8-11 8-11-8-11-8z" />
      <circle cx="12" cy="12" r="3" />
    </svg>
  );
}

function EyeOffIcon() {
  return (
    <svg
      width="14"
      height="14"
      viewBox="0 0 24 24"
      fill="none"
      stroke="currentColor"
      strokeWidth="2"
      strokeLinecap="round"
      strokeLinejoin="round"
    >
      <path d="M17.94 17.94A10.07 10.07 0 0 1 12 20c-7 0-11-8-11-8a18.45 18.45 0 0 1 5.06-5.94M9.9 4.24A9.12 9.12 0 0 1 12 4c7 0 11 8 11 8a18.5 18.5 0 0 1-2.16 3.19m-6.72-1.07a3 3 0 1 1-4.24-4.24" />
      <line x1="1" y1="1" x2="23" y2="23" />
    </svg>
  );
}

function CameraIcon() {
  return (
    <svg
      width="14"
      height="14"
      viewBox="0 0 24 24"
      fill="none"
      stroke="currentColor"
      strokeWidth="2"
      strokeLinecap="round"
      strokeLinejoin="round"
    >
      <path d="M23 19a2 2 0 0 1-2 2H3a2 2 0 0 1-2-2V8a2 2 0 0 1 2-2h4l2-3h6l2 3h4a2 2 0 0 1 2 2z" />
      <circle cx="12" cy="13" r="4" />
    </svg>
  );
}

function ScreenIcon() {
  return (
    <svg
      width="14"
      height="14"
      viewBox="0 0 24 24"
      fill="none"
      stroke="currentColor"
      strokeWidth="2"
      strokeLinecap="round"
      strokeLinejoin="round"
    >
      <rect x="2" y="3" width="20" height="14" rx="2" ry="2" />
      <line x1="8" y1="21" x2="16" y2="21" />
      <line x1="12" y1="17" x2="12" y2="21" />
    </svg>
  );
}

function VideoIcon() {
  return (
    <svg
      width="14"
      height="14"
      viewBox="0 0 24 24"
      fill="none"
      stroke="currentColor"
      strokeWidth="2"
      strokeLinecap="round"
      strokeLinejoin="round"
    >
      <polygon points="23 7 16 12 23 17 23 7" />
      <rect x="1" y="5" width="15" height="14" rx="2" ry="2" />
    </svg>
  );
}

function GearIcon() {
  return (
    <svg
      width="12"
      height="12"
      viewBox="0 0 24 24"
      fill="none"
      stroke="currentColor"
      strokeWidth="2"
      strokeLinecap="round"
      strokeLinejoin="round"
    >
      <circle cx="12" cy="12" r="3" />
      <path d="M19.4 15a1.65 1.65 0 0 0 .33 1.82l.06.06a2 2 0 0 1 0 2.83 2 2 0 0 1-2.83 0l-.06-.06a1.65 1.65 0 0 0-1.82-.33 1.65 1.65 0 0 0-1 1.51V21a2 2 0 0 1-2 2 2 2 0 0 1-2-2v-.09A1.65 1.65 0 0 0 9 19.4a1.65 1.65 0 0 0-1.82.33l-.06.06a2 2 0 0 1-2.83 0 2 2 0 0 1 0-2.83l.06-.06a1.65 1.65 0 0 0 .33-1.82 1.65 1.65 0 0 0-1.51-1H3a2 2 0 0 1-2-2 2 2 0 0 1 2-2h.09A1.65 1.65 0 0 0 4.6 9a1.65 1.65 0 0 0-.33-1.82l-.06-.06a2 2 0 0 1 0-2.83 2 2 0 0 1 2.83 0l.06.06a1.65 1.65 0 0 0 1.82.33H9a1.65 1.65 0 0 0 1-1.51V3a2 2 0 0 1 2-2 2 2 0 0 1 2 2v.09a1.65 1.65 0 0 0 1 1.51 1.65 1.65 0 0 0 1.82-.33l.06-.06a2 2 0 0 1 2.83 0 2 2 0 0 1 0 2.83l-.06.06a1.65 1.65 0 0 0-.33 1.82V9a1.65 1.65 0 0 0 1.51 1H21a2 2 0 0 1 2 2 2 2 0 0 1-2 2h-.09a1.65 1.65 0 0 0-1.51 1z" />
    </svg>
  );
}

export interface LayerListProps {
  layers: Layer[];
  sources: MediaSource[];
  onVisibilityToggle: (layerId: string, visible: boolean) => void;
  onReorder: (layerIds: string[]) => void;
  onTransformEdit?: (layerId: string, transform: Partial<LayerTransform>) => void;
  onRemove?: (layerId: string) => void;
  onSelect?: (layerId: string | null) => void;
  selectedLayerId?: string | null;
  className?: string;
}

export function LayerList({
  layers,
  sources,
  onVisibilityToggle,
  onReorder,
  onTransformEdit,
  onRemove,
  onSelect,
  selectedLayerId,
  className = "",
}: LayerListProps) {
  const [draggedId, setDraggedId] = useState<string | null>(null);
  const [dragOverId, setDragOverId] = useState<string | null>(null);
  const [editingLayerId, setEditingLayerId] = useState<string | null>(null);

  // Sort layers by z-index (bottom to top, so higher z-index at top of list)
  const sortedLayers = [...layers].sort((a, b) => b.zIndex - a.zIndex);

  const getSourceLabel = useCallback(
    (sourceId: string): string => {
      const source = sources.find((s) => s.id === sourceId);
      return source?.label || sourceId;
    },
    [sources]
  );

  const getSourceIcon = useCallback(
    (sourceId: string): React.ReactNode => {
      const source = sources.find((s) => s.id === sourceId);
      switch (source?.type) {
        case "camera":
          return <CameraIcon />;
        case "screen":
          return <ScreenIcon />;
        case "custom":
          return <VideoIcon />;
        default:
          return <VideoIcon />;
      }
    },
    [sources]
  );

  // Drag and drop handlers
  const handleDragStart = useCallback((e: React.DragEvent, layerId: string) => {
    setDraggedId(layerId);
    e.dataTransfer.effectAllowed = "move";
    e.dataTransfer.setData("text/plain", layerId);
  }, []);

  const handleDragOver = useCallback((e: React.DragEvent, layerId: string) => {
    e.preventDefault();
    e.dataTransfer.dropEffect = "move";
    setDragOverId(layerId);
  }, []);

  const handleDragLeave = useCallback(() => {
    setDragOverId(null);
  }, []);

  const handleDrop = useCallback(
    (e: React.DragEvent, targetLayerId: string) => {
      e.preventDefault();
      setDragOverId(null);

      if (!draggedId || draggedId === targetLayerId) {
        setDraggedId(null);
        return;
      }

      // Reorder: move dragged layer to target position
      const currentIds = sortedLayers.map((l) => l.id);
      const fromIndex = currentIds.indexOf(draggedId);
      const toIndex = currentIds.indexOf(targetLayerId);

      if (fromIndex === -1 || toIndex === -1) {
        setDraggedId(null);
        return;
      }

      // Create new order
      const newOrder = [...currentIds];
      newOrder.splice(fromIndex, 1);
      newOrder.splice(toIndex, 0, draggedId);

      onReorder(newOrder);
      setDraggedId(null);
    },
    [draggedId, sortedLayers, onReorder]
  );

  const handleDragEnd = useCallback(() => {
    setDraggedId(null);
    setDragOverId(null);
  }, []);

  // Move layer up/down
  const handleMoveUp = useCallback(
    (layerId: string) => {
      const currentIds = sortedLayers.map((l) => l.id);
      const index = currentIds.indexOf(layerId);
      if (index <= 0) return;

      const newOrder = [...currentIds];
      [newOrder[index - 1], newOrder[index]] = [newOrder[index], newOrder[index - 1]];
      onReorder(newOrder);
    },
    [sortedLayers, onReorder]
  );

  const handleMoveDown = useCallback(
    (layerId: string) => {
      const currentIds = sortedLayers.map((l) => l.id);
      const index = currentIds.indexOf(layerId);
      if (index >= currentIds.length - 1) return;

      const newOrder = [...currentIds];
      [newOrder[index], newOrder[index + 1]] = [newOrder[index + 1], newOrder[index]];
      onReorder(newOrder);
    },
    [sortedLayers, onReorder]
  );

  // Opacity slider
  const handleOpacityChange = useCallback(
    (layerId: string, opacity: number) => {
      onTransformEdit?.(layerId, { opacity });
    },
    [onTransformEdit]
  );

  return (
    <div className={`fw-sc-layer-list ${className}`}>
      <div className="fw-sc-layer-list-header">
        <span className="fw-sc-layer-list-title">Layers</span>
        <span className="fw-sc-layer-count">{layers.length}</span>
      </div>

      <div className="fw-sc-layer-items">
        {sortedLayers.length === 0 ? (
          <div className="fw-sc-layer-empty">No layers. Add a source to get started.</div>
        ) : (
          sortedLayers.map((layer, index) => (
            <div
              key={layer.id}
              className={`fw-sc-layer-item ${layer.id === selectedLayerId ? "fw-sc-layer-item--selected" : ""} ${layer.id === draggedId ? "fw-sc-layer-item--dragging" : ""} ${layer.id === dragOverId ? "fw-sc-layer-item--drag-over" : ""} ${!layer.visible ? "fw-sc-layer-item--hidden" : ""}`}
              draggable
              onDragStart={(e) => handleDragStart(e, layer.id)}
              onDragOver={(e) => handleDragOver(e, layer.id)}
              onDragLeave={handleDragLeave}
              onDrop={(e) => handleDrop(e, layer.id)}
              onDragEnd={handleDragEnd}
              onClick={() => onSelect?.(layer.id === selectedLayerId ? null : layer.id)}
            >
              {/* Visibility Toggle */}
              <button
                className={`fw-sc-layer-visibility ${layer.visible ? "fw-sc-layer-visibility--visible" : ""}`}
                onClick={(e) => {
                  e.stopPropagation();
                  onVisibilityToggle(layer.id, !layer.visible);
                }}
                title={layer.visible ? "Hide layer" : "Show layer"}
              >
                {layer.visible ? <EyeIcon /> : <EyeOffIcon />}
              </button>

              {/* Source Info */}
              <span className="fw-sc-layer-icon">{getSourceIcon(layer.sourceId)}</span>
              <span className="fw-sc-layer-name">{getSourceLabel(layer.sourceId)}</span>

              {/* Opacity (shown when editing) */}
              {editingLayerId === layer.id && onTransformEdit && (
                <div className="fw-sc-layer-opacity">
                  <input
                    type="range"
                    min="0"
                    max="1"
                    step="0.1"
                    value={layer.transform.opacity}
                    onChange={(e) => handleOpacityChange(layer.id, Number(e.target.value))}
                    onClick={(e) => e.stopPropagation()}
                  />
                  <span>{Math.round(layer.transform.opacity * 100)}%</span>
                </div>
              )}

              {/* Controls */}
              <div className="fw-sc-layer-controls">
                {/* Move Up */}
                <button
                  className="fw-sc-layer-btn"
                  onClick={(e) => {
                    e.stopPropagation();
                    handleMoveUp(layer.id);
                  }}
                  disabled={index === 0}
                  title="Move up"
                >
                  ↑
                </button>

                {/* Move Down */}
                <button
                  className="fw-sc-layer-btn"
                  onClick={(e) => {
                    e.stopPropagation();
                    handleMoveDown(layer.id);
                  }}
                  disabled={index === sortedLayers.length - 1}
                  title="Move down"
                >
                  ↓
                </button>

                {/* Edit Transform */}
                {onTransformEdit && (
                  <button
                    className={`fw-sc-layer-btn ${editingLayerId === layer.id ? "fw-sc-layer-btn--active" : ""}`}
                    onClick={(e) => {
                      e.stopPropagation();
                      setEditingLayerId(editingLayerId === layer.id ? null : layer.id);
                    }}
                    title="Edit opacity"
                  >
                    <GearIcon />
                  </button>
                )}

                {/* Remove */}
                {onRemove && (
                  <button
                    className="fw-sc-layer-btn fw-sc-layer-btn--danger"
                    onClick={(e) => {
                      e.stopPropagation();
                      onRemove(layer.id);
                    }}
                    title="Remove layer"
                  >
                    ×
                  </button>
                )}
              </div>
            </div>
          ))
        )}
      </div>
    </div>
  );
}

export default LayerList;
