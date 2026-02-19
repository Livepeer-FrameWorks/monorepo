/**
 * Compositor Store for Svelte 5
 * Manages compositor state with scenes, layers, and transitions
 */

import { writable, derived, type Readable, type Writable } from "svelte/store";
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

export interface CompositorState {
  isEnabled: boolean;
  isInitialized: boolean;
  rendererType: RendererType | null;
  stats: RendererStats | null;
  scenes: Scene[];
  activeSceneId: string | null;
  currentLayout: LayoutConfig | null;
}

export interface CompositorStore extends Readable<CompositorState> {
  // State
  readonly activeScene: Readable<Scene | null>;

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

  // Lifecycle
  destroy: () => void;
}

export interface CreateCompositorStoreOptions {
  controller: IngestControllerV2 | null;
  config?: Partial<CompositorConfig>;
  autoEnable?: boolean;
}

export function createCompositorStore(options: CreateCompositorStoreOptions): CompositorStore {
  const { controller, config, autoEnable = true } = options;

  const store: Writable<CompositorState> = writable({
    isEnabled: false,
    isInitialized: false,
    rendererType: null,
    stats: null,
    scenes: [],
    activeSceneId: null,
    currentLayout: null,
  });

  let unsubscribers: (() => void)[] = [];

  // Derived store for active scene
  const activeScene = derived(store, ($state) => {
    return $state.scenes.find((s) => s.id === $state.activeSceneId) || null;
  });

  // Sync state from SceneManager
  function syncState() {
    if (!controller) return;
    const sceneManager = controller.getSceneManager();
    if (!sceneManager) return;

    store.update((state) => ({
      ...state,
      scenes: sceneManager.getAllScenes(),
      activeSceneId: sceneManager.getActiveScene()?.id || null,
      rendererType: sceneManager.getRendererType(),
      stats: sceneManager.getStats(),
      currentLayout: sceneManager.getCurrentLayout(),
      isInitialized: sceneManager.isInitialized(),
    }));
  }

  // Set up event listeners when controller is available
  function setupListeners() {
    if (!controller) return;
    const sceneManager = controller.getSceneManager();
    if (!sceneManager) return;

    unsubscribers = [
      sceneManager.on("sceneCreated", syncState),
      sceneManager.on("sceneDeleted", syncState),
      sceneManager.on("sceneActivated", syncState),
      sceneManager.on("layerAdded", syncState),
      sceneManager.on("layerRemoved", syncState),
      sceneManager.on("layerUpdated", syncState),
      sceneManager.on("transitionStarted", syncState),
      sceneManager.on("transitionCompleted", syncState),
      sceneManager.on("statsUpdate", ({ stats }) => {
        store.update((state) => ({ ...state, stats }));
      }),
      sceneManager.on("rendererChanged", ({ renderer }) => {
        store.update((state) => ({ ...state, rendererType: renderer }));
      }),
    ];

    syncState();
  }

  // Auto-enable if requested
  if (autoEnable && controller) {
    console.log("[CompositorStore] Auto-enabling compositor...");
    controller
      .enableCompositor(config)
      .then(() => {
        console.log("[CompositorStore] Compositor enabled successfully");
        store.update((state) => ({ ...state, isEnabled: true }));
        setupListeners();
      })
      .catch((error) => {
        console.error("[CompositorStore] Failed to enable compositor:", error);
        store.update((state) => ({
          ...state,
          isEnabled: false,
          isInitialized: false,
        }));
      });
  }

  // Actions
  async function enable(enableConfig?: Partial<CompositorConfig>): Promise<void> {
    if (!controller) {
      console.warn("[CompositorStore] Cannot enable: no controller");
      return;
    }
    try {
      console.log("[CompositorStore] Enabling compositor...");
      await controller.enableCompositor(enableConfig || config);
      console.log("[CompositorStore] Compositor enabled successfully");
      store.update((state) => ({ ...state, isEnabled: true }));
      setupListeners();
    } catch (error) {
      console.error("[CompositorStore] Failed to enable compositor:", error);
      store.update((state) => ({
        ...state,
        isEnabled: false,
        isInitialized: false,
      }));
      throw error;
    }
  }

  function disable(): void {
    if (!controller) return;
    controller.disableCompositor();
    unsubscribers.forEach((unsub) => unsub());
    unsubscribers = [];
    store.set({
      isEnabled: false,
      isInitialized: false,
      rendererType: null,
      stats: null,
      scenes: [],
      activeSceneId: null,
      currentLayout: null,
    });
  }

  // Scene management
  function createScene(name: string, backgroundColor = "#000000"): Scene | null {
    const sceneManager = controller?.getSceneManager();
    if (!sceneManager) return null;
    const scene = sceneManager.createScene(name, backgroundColor);
    syncState();
    return scene;
  }

  function deleteScene(sceneId: string): void {
    const sceneManager = controller?.getSceneManager();
    if (!sceneManager) return;
    sceneManager.deleteScene(sceneId);
    syncState();
  }

  function setActiveScene(sceneId: string): void {
    const sceneManager = controller?.getSceneManager();
    if (!sceneManager) return;
    sceneManager.setActiveScene(sceneId);
    syncState();
  }

  async function transitionTo(sceneId: string, transition?: TransitionConfig): Promise<void> {
    const sceneManager = controller?.getSceneManager();
    if (!sceneManager) return;
    await sceneManager.transitionTo(sceneId, transition);
    syncState();
  }

  // Layer management
  function addLayer(
    sceneId: string,
    sourceId: string,
    transform?: Partial<LayerTransform>
  ): Layer | null {
    const sceneManager = controller?.getSceneManager();
    if (!sceneManager) return null;
    const layer = sceneManager.addLayer(sceneId, sourceId, transform);
    syncState();
    return layer;
  }

  function removeLayer(sceneId: string, layerId: string): void {
    const sceneManager = controller?.getSceneManager();
    if (!sceneManager) return;
    sceneManager.removeLayer(sceneId, layerId);
    syncState();
  }

  function updateLayerTransform(
    sceneId: string,
    layerId: string,
    transform: Partial<LayerTransform>
  ): void {
    const sceneManager = controller?.getSceneManager();
    if (!sceneManager) return;
    sceneManager.updateLayerTransform(sceneId, layerId, transform);
    syncState();
  }

  function setLayerVisibility(sceneId: string, layerId: string, visible: boolean): void {
    const sceneManager = controller?.getSceneManager();
    if (!sceneManager) return;
    sceneManager.setLayerVisibility(sceneId, layerId, visible);
    syncState();
  }

  function reorderLayers(sceneId: string, layerIds: string[]): void {
    const sceneManager = controller?.getSceneManager();
    if (!sceneManager) return;
    sceneManager.reorderLayers(sceneId, layerIds);
    syncState();
  }

  // Layout presets
  function applyLayout(layout: LayoutConfig): void {
    const sceneManager = controller?.getSceneManager();
    if (!sceneManager) return;

    // Fix: Capture current layer order (source IDs) to persist across layout changes
    // This prevents the layout switch from resetting your carefully cycled order.
    const currentScene = sceneManager.getActiveScene();
    let preservedOrder: string[] = [];
    if (currentScene) {
      preservedOrder = currentScene.layers.map((l) => l.sourceId);
    }

    // Apply the new layout (which calculates transforms)
    sceneManager.applyLayout(layout);

    // Fix: Re-apply the preserved order immediately
    if (currentScene && preservedOrder.length > 0) {
      sceneManager.reorderLayers(currentScene.id, preservedOrder);
    }

    store.update((state) => ({ ...state, currentLayout: layout }));
    syncState();
  }

  function cycleSourceOrder(direction: "forward" | "backward" = "forward"): void {
    const sceneManager = controller?.getSceneManager();
    if (!sceneManager) return;

    sceneManager.cycleSourceOrder(direction);
    syncState();
  }

  // Destroy
  function destroy(): void {
    unsubscribers.forEach((unsub) => unsub());
    unsubscribers = [];
  }

  return {
    subscribe: store.subscribe,
    activeScene,

    enable,
    disable,

    createScene,
    deleteScene,
    setActiveScene,
    transitionTo,

    addLayer,
    removeLayer,
    updateLayerTransform,
    setLayerVisibility,
    reorderLayers,

    applyLayout,
    cycleSourceOrder,
    destroy,
  };
}
