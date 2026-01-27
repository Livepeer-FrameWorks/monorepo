/**
 * SceneSwitcher Component
 *
 * Horizontal row of scene buttons with active indicator.
 * Click to switch scenes, + button to create new scene.
 */

import React, { useState, useCallback } from "react";
import type {
  Scene,
  TransitionConfig,
  TransitionType,
} from "@livepeer-frameworks/streamcrafter-core";

export interface SceneSwitcherProps {
  scenes: Scene[];
  activeSceneId: string | null;
  onSceneSelect: (sceneId: string) => void;
  onSceneCreate?: () => void;
  onSceneDelete?: (sceneId: string) => void;
  onTransitionTo?: (sceneId: string, transition: TransitionConfig) => Promise<void>;
  transitionConfig?: TransitionConfig;
  showTransitionControls?: boolean;
  className?: string;
}

const DEFAULT_TRANSITION: TransitionConfig = {
  type: "fade",
  durationMs: 500,
  easing: "ease-in-out",
};

export function SceneSwitcher({
  scenes,
  activeSceneId,
  onSceneSelect,
  onSceneCreate,
  onSceneDelete,
  onTransitionTo,
  transitionConfig = DEFAULT_TRANSITION,
  showTransitionControls = true,
  className = "",
}: SceneSwitcherProps) {
  const [selectedTransition, setSelectedTransition] = useState<TransitionType>(
    transitionConfig.type
  );
  const [transitionDuration, setTransitionDuration] = useState(transitionConfig.durationMs);
  const [isTransitioning, setIsTransitioning] = useState(false);

  const handleSceneClick = useCallback(
    async (sceneId: string) => {
      if (sceneId === activeSceneId || isTransitioning) return;

      if (onTransitionTo) {
        setIsTransitioning(true);
        try {
          await onTransitionTo(sceneId, {
            type: selectedTransition,
            durationMs: transitionDuration,
            easing: transitionConfig.easing,
          });
        } finally {
          setIsTransitioning(false);
        }
      } else {
        onSceneSelect(sceneId);
      }
    },
    [
      activeSceneId,
      isTransitioning,
      onTransitionTo,
      onSceneSelect,
      selectedTransition,
      transitionDuration,
      transitionConfig.easing,
    ]
  );

  const handleDeleteClick = useCallback(
    (e: React.MouseEvent, sceneId: string) => {
      e.stopPropagation();
      if (scenes.length <= 1) return; // Don't delete last scene
      onSceneDelete?.(sceneId);
    },
    [scenes.length, onSceneDelete]
  );

  return (
    <div className={`fw-sc-scene-switcher ${className}`}>
      <div className="fw-sc-scene-switcher-header">
        <span className="fw-sc-scene-switcher-title">Scenes</span>
        {showTransitionControls && (
          <div className="fw-sc-transition-controls">
            <select
              className="fw-sc-transition-select"
              value={selectedTransition}
              onChange={(e) => setSelectedTransition(e.target.value as TransitionType)}
            >
              <option value="cut">Cut</option>
              <option value="fade">Fade</option>
              <option value="slide-left">Slide Left</option>
              <option value="slide-right">Slide Right</option>
              <option value="slide-up">Slide Up</option>
              <option value="slide-down">Slide Down</option>
            </select>
            <input
              type="number"
              className="fw-sc-transition-duration"
              value={transitionDuration}
              onChange={(e) => setTransitionDuration(Number(e.target.value))}
              min={0}
              max={3000}
              step={100}
              title="Transition duration (ms)"
            />
            <span className="fw-sc-transition-unit">ms</span>
          </div>
        )}
      </div>

      <div className="fw-sc-scene-list">
        {scenes.map((scene) => (
          <div
            key={scene.id}
            className={`fw-sc-scene-item ${scene.id === activeSceneId ? "fw-sc-scene-item--active" : ""} ${isTransitioning ? "fw-sc-scene-item--transitioning" : ""}`}
            onClick={() => handleSceneClick(scene.id)}
            style={{ backgroundColor: scene.backgroundColor }}
          >
            <span className="fw-sc-scene-name">{scene.name}</span>
            <span className="fw-sc-scene-layer-count">{scene.layers.length} layers</span>
            {onSceneDelete && scenes.length > 1 && scene.id !== activeSceneId && (
              <button
                className="fw-sc-scene-delete"
                onClick={(e) => handleDeleteClick(e, scene.id)}
                title="Delete scene"
              >
                ×
              </button>
            )}
          </div>
        ))}

        {onSceneCreate && (
          <button className="fw-sc-scene-add" onClick={onSceneCreate} title="Create new scene">
            +
          </button>
        )}
      </div>
    </div>
  );
}

export default SceneSwitcher;
