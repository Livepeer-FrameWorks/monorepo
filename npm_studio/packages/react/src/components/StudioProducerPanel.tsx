/**
 * StudioProducerPanel Component
 *
 * Producer controls for the compositor: scenes with their transition, the layers
 * of the active scene, overlays and media files. Reads the compositor, sources
 * and media actions from the StreamCrafter context unless they are given, and
 * renders nothing until the compositor is running.
 */

import React from "react";
import { nextStudioSceneName, type MediaSource } from "@livepeer-frameworks/streamcrafter-core";
import type { UseCompositorReturn } from "../hooks/useCompositor";
import { useStudioTranslate } from "../context/StudioI18nContext";
import { useStreamCrafterContextOptional } from "../context/StreamCrafterContext";
import { SceneSwitcher } from "./SceneSwitcher";
import { LayerList } from "./LayerList";
import { StudioOverlayControls } from "./StudioOverlayControls";
import { StudioMediaControls, type StudioMediaActions } from "./StudioMediaControls";

export type { StudioMediaActions } from "./StudioMediaControls";

export interface StudioProducerPanelProps {
  /** Defaults to the compositor of the parent StreamCrafter. */
  compositor?: UseCompositorReturn | null;
  /** Defaults to the sources of the parent StreamCrafter. */
  sources?: MediaSource[];
  /** Media file actions; defaults to those of the parent StreamCrafter. */
  media?: StudioMediaActions;
  className?: string;
}

export function StudioProducerPanel({
  compositor: compositorProp,
  sources: sourcesProp,
  media,
  className = "",
}: StudioProducerPanelProps) {
  const t = useStudioTranslate();
  const ctx = useStreamCrafterContextOptional();
  const compositor = compositorProp ?? ctx?.compositor ?? null;
  const sources = sourcesProp ?? ctx?.sources ?? [];

  if (!compositor || !compositor.isEnabled || !compositor.isInitialized) return null;
  const { activeSceneId, activeScene } = compositor;

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
        onTransformEdit={(layerId, transform) => {
          if (activeSceneId) compositor.updateLayerTransform(activeSceneId, layerId, transform);
        }}
        onRemove={(layerId) => {
          if (activeSceneId) compositor.removeLayer(activeSceneId, layerId);
        }}
      />
      <StudioOverlayControls compositor={compositor} />
      <StudioMediaControls sources={sources} media={media} />
    </section>
  );
}
