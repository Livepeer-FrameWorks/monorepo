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
import { useStudioTranslate } from "../context/StudioI18nContext";

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
  const t = useStudioTranslate();
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
        <span className="fw-sc-scene-switcher-title">{t("scenes")}</span>
        {showTransitionControls && (
          <div className="fw-sc-transition-controls">
            <select
              className="fw-sc-transition-select"
              value={selectedTransition}
              onChange={(e) => setSelectedTransition(e.target.value as TransitionType)}
            >
              <option value="cut">{t("cut")}</option>
              <option value="fade">{t("fade")}</option>
              <option value="slide-left">{t("slideLeft")}</option>
              <option value="slide-right">{t("slideRight")}</option>
              <option value="slide-up">{t("slideUp")}</option>
              <option value="slide-down">{t("slideDown")}</option>
            </select>
            <input
              type="number"
              className="fw-sc-transition-duration"
              value={transitionDuration}
              onChange={(e) => setTransitionDuration(Number(e.target.value))}
              min={0}
              max={3000}
              step={100}
              title={t("transitionDuration")}
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
                title={t("deleteScene")}
              >
                ×
              </button>
            )}
          </div>
        ))}

        {onSceneCreate && (
          <button className="fw-sc-scene-add" onClick={onSceneCreate} title={t("createNewScene")}>
            +
          </button>
        )}
      </div>
    </div>
  );
}

export default SceneSwitcher;
