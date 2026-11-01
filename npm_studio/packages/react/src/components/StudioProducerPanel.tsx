/**
 * StudioProducerPanel Component
 *
 * Producer controls for the compositor: scenes with their transition, and the
 * layers of the active scene. Renders nothing until the compositor is running.
 */

import React from "react";
import { nextStudioSceneName, type MediaSource } from "@livepeer-frameworks/streamcrafter-core";
import type { UseCompositorReturn } from "../hooks/useCompositor";
import { useStudioTranslate } from "../context/StudioI18nContext";
import { SceneSwitcher } from "./SceneSwitcher";
import { LayerList } from "./LayerList";

export interface StudioProducerPanelProps {
  compositor: UseCompositorReturn;
  sources: MediaSource[];
  className?: string;
}

export function StudioProducerPanel({
  compositor,
  sources,
  className = "",
}: StudioProducerPanelProps) {
  const t = useStudioTranslate();
  const { activeSceneId, activeScene } = compositor;

  if (!compositor.isEnabled || !compositor.isInitialized) return null;

  return (
    <section className={`fw-sc-producer ${className}`} aria-label={t("composition")}>
      <SceneSwitcher
        scenes={compositor.scenes}
        activeSceneId={activeSceneId}
        onSceneSelect={compositor.setActiveScene}
        onSceneCreate={() => compositor.createScene(nextStudioSceneName(compositor.scenes, t))}
        onSceneDelete={compositor.deleteScene}
        onTransitionTo={compositor.transitionTo}
      />
      <LayerList
        layers={activeScene?.layers ?? []}
        sources={sources}
        onVisibilityToggle={(layerId, visible) => {
          if (activeSceneId) compositor.setLayerVisibility(activeSceneId, layerId, visible);
        }}
        onReorder={(layerIds) => {
          if (activeSceneId) compositor.reorderLayers(activeSceneId, layerIds);
        }}
      />
    </section>
  );
}
