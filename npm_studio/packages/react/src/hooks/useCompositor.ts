/**
 * useCompositor Hook
 * React hook for controlling the compositor (Phase 3)
 *
 * Features:
 * - Scene management (create, delete, switch)
 * - Layer management (add, remove, reorder, transform)
 * - Layout presets (fullscreen, PiP, side-by-side)
 * - Transitions between scenes
 */

import { useState, useEffect, useCallback, useRef } from "react";
import type {
  Scene,
  Layer,
  LayerTransform,
  LayoutConfig,
  TransitionConfig,
  RendererType,
  RendererStats,
  CompositorConfig,
} from "@livepeer-frameworks/streamcrafter-core";
import type { IngestControllerV2 } from "@livepeer-frameworks/streamcrafter-core";

export interface UseCompositorOptions {
  controller: IngestControllerV2 | null;
  config?: Partial<CompositorConfig>;
  autoEnable?: boolean;
}

export interface UseCompositorReturn {
  // State
  isEnabled: boolean;
  isInitialized: boolean;
  rendererType: RendererType | null;
  stats: RendererStats | null;

  // Scenes
  scenes: Scene[];
  activeScene: Scene | null;
  activeSceneId: string | null;

  // Actions
  enable: (config?: Partial<CompositorConfig>) => Promise<void>;
  disable: () => void;

  // Scene management
  createScene: (name: string, backgroundColor?: string) => Scene | null;
  deleteScene: (sceneId: string) => void;
  setActiveScene: (sceneId: string) => void;
  transitionTo: (sceneId: string, transition?: TransitionConfig) => Promise<void>;

  // Layer management
  addLayer: (
    sceneId: string,
    sourceId: string,
    transform?: Partial<LayerTransform>
  ) => Layer | null;
  removeLayer: (sceneId: string, layerId: string) => void;
  updateLayerTransform: (
    sceneId: string,
    layerId: string,
    transform: Partial<LayerTransform>
  ) => void;
  setLayerVisibility: (sceneId: string, layerId: string, visible: boolean) => void;
  reorderLayers: (sceneId: string, layerIds: string[]) => void;

  // Layout presets
  applyLayout: (layout: LayoutConfig) => void;
  cycleSourceOrder: (direction?: "forward" | "backward") => void;
  currentLayout: LayoutConfig | null;

  // Renderer control
  setRenderer: (renderer: RendererType) => void;
}

export function useCompositor({
  controller,
  config,
  autoEnable = true,
}: UseCompositorOptions): UseCompositorReturn {
  const [isEnabled, setIsEnabled] = useState(false);
  const [isInitialized, setIsInitialized] = useState(false);
  const [rendererType, setRendererType] = useState<RendererType | null>(null);
  const [stats, setStats] = useState<RendererStats | null>(null);
  const [scenes, setScenes] = useState<Scene[]>([]);
  const [activeSceneId, setActiveSceneId] = useState<string | null>(null);
  const [currentLayout, setCurrentLayout] = useState<LayoutConfig | null>(null);

  const configRef = useRef(config);
  configRef.current = config;

  // Get the active scene
  const activeScene = scenes.find((s) => s.id === activeSceneId) || null;

  // Sync state from SceneManager
  const syncState = useCallback(() => {
    if (!controller) return;
    const sceneManager = controller.getSceneManager();
    if (!sceneManager) return;

    setScenes(sceneManager.getAllScenes());
    const active = sceneManager.getActiveScene();
    setActiveSceneId(active?.id || null);
    setRendererType(sceneManager.getRendererType());
    setStats(sceneManager.getStats());
    setCurrentLayout(sceneManager.getCurrentLayout());
    setIsInitialized(sceneManager.isInitialized());
  }, [controller]);

  // Auto-enable if requested
  useEffect(() => {
    let mounted = true;
    if (autoEnable && controller && !isEnabled) {
      controller.enableCompositor(configRef.current).then(() => {
        if (!mounted) return;
        setIsEnabled(true);
        syncState();
      });
    }
    return () => {
      mounted = false;
    };
  }, [autoEnable, controller, isEnabled, syncState]);

  // Set up event listeners
  useEffect(() => {
    if (!controller) return;

    const sceneManager = controller.getSceneManager();
    if (!sceneManager) return;

    const unsubSceneCreated = sceneManager.on("sceneCreated", () => syncState());
    const unsubSceneDeleted = sceneManager.on("sceneDeleted", () => syncState());
    const unsubSceneActivated = sceneManager.on("sceneActivated", () => syncState());
    const unsubLayerAdded = sceneManager.on("layerAdded", () => syncState());
    const unsubLayerRemoved = sceneManager.on("layerRemoved", () => syncState());
    const unsubLayerUpdated = sceneManager.on("layerUpdated", () => syncState());
    const unsubTransitionStarted = sceneManager.on("transitionStarted", () => syncState());
    const unsubTransitionCompleted = sceneManager.on("transitionCompleted", () => syncState());
    const unsubStatsUpdate = sceneManager.on("statsUpdate", ({ stats: newStats }) => {
      setStats(newStats);
    });
    const unsubRendererChanged = sceneManager.on("rendererChanged", ({ renderer }) => {
      setRendererType(renderer);
    });

    // Initial sync
    syncState();

    return () => {
      unsubSceneCreated();
      unsubSceneDeleted();
      unsubSceneActivated();
      unsubLayerAdded();
      unsubLayerRemoved();
      unsubLayerUpdated();
      unsubTransitionStarted();
      unsubTransitionCompleted();
      unsubStatsUpdate();
      unsubRendererChanged();
    };
  }, [controller, syncState]);

  // Enable compositor
  const enable = useCallback(
    async (enableConfig?: Partial<CompositorConfig>) => {
      if (!controller) return;
      await controller.enableCompositor(enableConfig || configRef.current);
      setIsEnabled(true);
      syncState();
    },
    [controller, syncState]
  );

  // Disable compositor
  const disable = useCallback(() => {
    if (!controller) return;
    controller.disableCompositor();
    setIsEnabled(false);
    setIsInitialized(false);
    setScenes([]);
    setActiveSceneId(null);
    setRendererType(null);
    setStats(null);
  }, [controller]);

  // Scene management
  const createScene = useCallback(
    (name: string, backgroundColor = "#000000"): Scene | null => {
      const sceneManager = controller?.getSceneManager();
      if (!sceneManager) return null;
      const scene = sceneManager.createScene(name, backgroundColor);
      syncState();
      return scene;
    },
    [controller, syncState]
  );

  const deleteScene = useCallback(
    (sceneId: string) => {
      const sceneManager = controller?.getSceneManager();
      if (!sceneManager) return;
      sceneManager.deleteScene(sceneId);
      syncState();
    },
    [controller, syncState]
  );

  const setActiveScene = useCallback(
    (sceneId: string) => {
      const sceneManager = controller?.getSceneManager();
      if (!sceneManager) return;
      sceneManager.setActiveScene(sceneId);
      syncState();
    },
    [controller, syncState]
  );

  const transitionTo = useCallback(
    async (sceneId: string, transition?: TransitionConfig) => {
      const sceneManager = controller?.getSceneManager();
      if (!sceneManager) return;
      await sceneManager.transitionTo(sceneId, transition);
      syncState();
    },
    [controller, syncState]
  );

  // Layer management
  const addLayer = useCallback(
    (sceneId: string, sourceId: string, transform?: Partial<LayerTransform>): Layer | null => {
      const sceneManager = controller?.getSceneManager();
      if (!sceneManager) return null;
      const layer = sceneManager.addLayer(sceneId, sourceId, transform);
      syncState();
      return layer;
    },
    [controller, syncState]
  );

  const removeLayer = useCallback(
    (sceneId: string, layerId: string) => {
      const sceneManager = controller?.getSceneManager();
      if (!sceneManager) return;
      sceneManager.removeLayer(sceneId, layerId);
      syncState();
    },
    [controller, syncState]
  );

  const updateLayerTransform = useCallback(
    (sceneId: string, layerId: string, transform: Partial<LayerTransform>) => {
      const sceneManager = controller?.getSceneManager();
      if (!sceneManager) return;
      sceneManager.updateLayerTransform(sceneId, layerId, transform);
      syncState();
    },
    [controller, syncState]
  );

  const setLayerVisibility = useCallback(
    (sceneId: string, layerId: string, visible: boolean) => {
      const sceneManager = controller?.getSceneManager();
      if (!sceneManager) return;
      sceneManager.setLayerVisibility(sceneId, layerId, visible);
      syncState();
    },
    [controller, syncState]
  );

  const reorderLayers = useCallback(
    (sceneId: string, layerIds: string[]) => {
      const sceneManager = controller?.getSceneManager();
      if (!sceneManager) return;
      sceneManager.reorderLayers(sceneId, layerIds);
      syncState();
    },
    [controller, syncState]
  );

  // Layout presets
  const applyLayout = useCallback(
    (layout: LayoutConfig) => {
      const sceneManager = controller?.getSceneManager();
      if (!sceneManager) return;

      // Fix: Capture current layer order to persist across layout changes
      const currentScene = sceneManager.getActiveScene();
      let preservedOrder: string[] = [];
      if (currentScene) {
        preservedOrder = currentScene.layers.map((l) => l.sourceId);
      }

      sceneManager.applyLayout(layout);

      // Fix: Re-apply preserved order
      if (currentScene && preservedOrder.length > 0) {
        sceneManager.reorderLayers(currentScene.id, preservedOrder);
      }

      setCurrentLayout(layout);
      syncState();
    },
    [controller, syncState]
  );

  const cycleSourceOrder = useCallback(
    (direction: "forward" | "backward" = "forward") => {
      const sceneManager = controller?.getSceneManager();
      if (!sceneManager) return;

      sceneManager.cycleSourceOrder(direction);
      syncState();
    },
    [controller, syncState]
  );

  // Renderer control
  const setRendererCallback = useCallback(
    (renderer: RendererType) => {
      const sceneManager = controller?.getSceneManager();
      if (!sceneManager) return;
      sceneManager.setRenderer(renderer);
    },
    [controller]
  );

  return {
    // State
    isEnabled,
    isInitialized,
    rendererType,
    stats,

    // Scenes
    scenes,
    activeScene,
    activeSceneId,

    // Actions
    enable,
    disable,

    // Scene management
    createScene,
    deleteScene,
    setActiveScene,
    transitionTo,

    // Layer management
    addLayer,
    removeLayer,
    updateLayerTransform,
    setLayerVisibility,
    reorderLayers,

    // Layout presets
    applyLayout,
    cycleSourceOrder,
    currentLayout,

    // Renderer control
    setRenderer: setRendererCallback,
  };
}
