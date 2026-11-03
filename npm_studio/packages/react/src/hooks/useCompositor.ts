/**
 * useCompositor Hook
 * React hook for controlling the compositor.
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
  OverlayPlacement,
  StudioLayout,
  StudioOverlay,
  TextOverlayConfig,
} from "@livepeer-frameworks/streamcrafter-core";
import type { IngestControllerV2 } from "@livepeer-frameworks/streamcrafter-core";

export interface UseCompositorOptions {
  controller: IngestControllerV2 | null;
  /** A change of `renderer` switches the running compositor to it. */
  config?: Partial<CompositorConfig>;
  autoEnable?: boolean;
  /** Applied each time the compositor starts, before it reports itself enabled. */
  initialLayout?: StudioLayout | null;
}

export interface UseCompositorReturn {
  // State
  isEnabled: boolean;
  isInitialized: boolean;
  rendererType: RendererType | null;
  stats: RendererStats | null;
  /** Why the compositor could not start by itself, if it could not. */
  error: string | null;

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

  // Overlays of the active scene, bottom to top; new ones join the active scene
  overlays: StudioOverlay[];
  addImageOverlay: (image: Blob) => Promise<void>;
  addTextOverlay: (config: TextOverlayConfig) => void;
  updateTextOverlay: (sourceId: string, config: Partial<TextOverlayConfig>) => void;
  setOverlayPlacement: (sourceId: string, placement: Partial<OverlayPlacement>) => void;
  removeOverlay: (sourceId: string) => void;

  // Layout presets
  applyLayout: (layout: LayoutConfig) => void;
  cycleSourceOrder: (direction?: "forward" | "backward") => void;
  currentLayout: LayoutConfig | null;

  // Renderer control
  setRenderer: (renderer: RendererType) => void;

  // Layout as plain data, for storing per stream
  exportLayout: () => Promise<StudioLayout | null>;
  importLayout: (layout: StudioLayout) => Promise<void>;
}

export function useCompositor({
  controller,
  config,
  autoEnable = true,
  initialLayout,
}: UseCompositorOptions): UseCompositorReturn {
  const [isEnabled, setIsEnabled] = useState(false);
  const [isInitialized, setIsInitialized] = useState(false);
  const [rendererType, setRendererType] = useState<RendererType | null>(null);
  const [stats, setStats] = useState<RendererStats | null>(null);
  const [scenes, setScenes] = useState<Scene[]>([]);
  const [activeSceneId, setActiveSceneId] = useState<string | null>(null);
  const [currentLayout, setCurrentLayout] = useState<LayoutConfig | null>(null);
  const [overlays, setOverlays] = useState<StudioOverlay[]>([]);
  const [error, setError] = useState<string | null>(null);

  const configRef = useRef(config);
  configRef.current = config;
  const initialLayoutRef = useRef(initialLayout);
  initialLayoutRef.current = initialLayout;

  // A stored layout that no longer applies must not keep the compositor off.
  const startCompositor = useCallback(
    async (target: IngestControllerV2, enableConfig?: Partial<CompositorConfig>) => {
      await target.enableCompositor(enableConfig);
      const layout = initialLayoutRef.current;
      if (layout) {
        await target.importLayout(layout).catch((err: unknown) => {
          console.warn("Could not apply the studio layout:", err);
        });
      }
    },
    []
  );

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
    setOverlays(active ? sceneManager.getOverlays(active.id) : []);
    setRendererType(sceneManager.getRendererType());
    setStats(sceneManager.getStats());
    setCurrentLayout(sceneManager.getCurrentLayout());
    setIsInitialized(sceneManager.isInitialized());
  }, [controller]);

  // A new controller (a new endpoint) starts without a compositor.
  const enabledControllerRef = useRef<IngestControllerV2 | null>(null);
  useEffect(() => {
    if (enabledControllerRef.current && enabledControllerRef.current !== controller) {
      enabledControllerRef.current = null;
      setIsEnabled(false);
      setIsInitialized(false);
    }
  }, [controller]);

  // Auto-enable if requested
  useEffect(() => {
    let mounted = true;
    if (autoEnable && controller && !isEnabled) {
      setError(null);
      startCompositor(controller, configRef.current)
        .then(() => {
          if (!mounted) return;
          enabledControllerRef.current = controller;
          setIsEnabled(true);
          syncState();
        })
        .catch((err: unknown) => {
          console.error("Failed to enable compositor:", err);
          if (!mounted) return;
          setError(err instanceof Error ? err.message : String(err));
        });
    }
    return () => {
      mounted = false;
    };
  }, [autoEnable, controller, isEnabled, syncState, startCompositor]);

  // The scene manager exists only once the compositor is enabled, so the
  // listeners are attached again whenever that changes.
  useEffect(() => {
    if (!controller || !isEnabled) return;

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
  }, [controller, isEnabled, syncState]);

  // Switch the running compositor when the requested renderer changes.
  const requestedRenderer = config?.renderer;
  useEffect(() => {
    if (!isEnabled || !requestedRenderer) return;
    const sceneManager = controller?.getSceneManager();
    if (sceneManager && sceneManager.getRendererType() !== requestedRenderer) {
      sceneManager.setRenderer(requestedRenderer);
    }
  }, [controller, isEnabled, requestedRenderer]);

  // Enable compositor
  const enable = useCallback(
    async (enableConfig?: Partial<CompositorConfig>) => {
      if (!controller) return;
      await startCompositor(controller, enableConfig || configRef.current);
      enabledControllerRef.current = controller;
      setIsEnabled(true);
      syncState();
    },
    [controller, syncState, startCompositor]
  );

  // Disable compositor
  const disable = useCallback(() => {
    if (!controller) return;
    controller.disableCompositor();
    enabledControllerRef.current = null;
    setIsEnabled(false);
    setIsInitialized(false);
    setScenes([]);
    setActiveSceneId(null);
    setOverlays([]);
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

  // Overlays
  const addImageOverlay = useCallback(
    async (image: Blob) => {
      const sceneManager = controller?.getSceneManager();
      const scene = sceneManager?.getActiveScene();
      if (!sceneManager || !scene) return;
      await sceneManager.addImageOverlay(scene.id, image);
      syncState();
    },
    [controller, syncState]
  );

  const addTextOverlay = useCallback(
    (config: TextOverlayConfig) => {
      const sceneManager = controller?.getSceneManager();
      const scene = sceneManager?.getActiveScene();
      if (!sceneManager || !scene) return;
      sceneManager.addTextOverlay(scene.id, config);
      syncState();
    },
    [controller, syncState]
  );

  const updateTextOverlay = useCallback(
    (sourceId: string, config: Partial<TextOverlayConfig>) => {
      const sceneManager = controller?.getSceneManager();
      if (!sceneManager) return;
      sceneManager.updateTextOverlay(sourceId, config);
      syncState();
    },
    [controller, syncState]
  );

  const setOverlayPlacement = useCallback(
    (sourceId: string, placement: Partial<OverlayPlacement>) => {
      const sceneManager = controller?.getSceneManager();
      if (!sceneManager) return;
      sceneManager.setOverlayPlacement(sourceId, placement);
      syncState();
    },
    [controller, syncState]
  );

  const removeOverlay = useCallback(
    (sourceId: string) => {
      const sceneManager = controller?.getSceneManager();
      if (!sceneManager) return;
      sceneManager.removeOverlay(sourceId);
      syncState();
    },
    [controller, syncState]
  );

  // Layout presets
  const applyLayout = useCallback(
    (layout: LayoutConfig) => {
      const sceneManager = controller?.getSceneManager();
      if (!sceneManager) return;

      sceneManager.applyLayout(layout);

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

  const exportLayout = useCallback(
    async () => (controller ? controller.exportLayout() : null),
    [controller]
  );

  const importLayout = useCallback(
    async (layout: StudioLayout) => {
      if (!controller) return;
      await controller.importLayout(layout);
      syncState();
    },
    [controller, syncState]
  );

  return {
    // State
    isEnabled,
    isInitialized,
    rendererType,
    stats,
    error,

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

    // Overlays
    overlays,
    addImageOverlay,
    addTextOverlay,
    updateTextOverlay,
    setOverlayPlacement,
    removeOverlay,

    // Layout presets
    applyLayout,
    cycleSourceOrder,
    currentLayout,

    // Renderer control
    setRenderer: setRendererCallback,

    exportLayout,
    importLayout,
  };
}
