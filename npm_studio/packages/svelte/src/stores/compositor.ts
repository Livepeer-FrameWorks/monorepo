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
  MediaSource,
  OverlayPlacement,
  StudioLayout,
  StudioOverlay,
  TextOverlayConfig,
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
  /** Overlays of the active scene, bottom to top. */
  overlays: StudioOverlay[];
  /** Why the compositor could not start, if it could not. */
  error: string | null;
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

  // Overlays, added to the active scene
  addImageOverlay: (image: Blob) => Promise<void>;
  addTextOverlay: (config: TextOverlayConfig) => void;
  updateTextOverlay: (sourceId: string, config: Partial<TextOverlayConfig>) => void;
  setOverlayPlacement: (sourceId: string, placement: Partial<OverlayPlacement>) => void;
  removeOverlay: (sourceId: string) => void;

  // Layout presets
  applyLayout: (layout: LayoutConfig) => void;
  cycleSourceOrder: (direction?: "forward" | "backward") => void;

  // Renderer
  setRenderer: (renderer: RendererType) => void;

  // Layout as plain data, for storing per stream
  exportLayout: () => Promise<StudioLayout | null>;
  importLayout: (layout: StudioLayout) => Promise<void>;

  // Lifecycle
  destroy: () => void;
}

/**
 * What the "fw-sc-controller" context gives composable children for compositing.
 * `compositor` is null until the controller exists, so read it through the
 * context each time rather than keeping a copy.
 */
export interface CompositorContext {
  readonly compositor: CompositorStore | null;
  readonly compositorState: CompositorState;
  readonly sources: MediaSource[];
  addMediaFile: (file: File) => Promise<unknown>;
  playMediaSource: (sourceId: string) => void;
  pauseMediaSource: (sourceId: string) => void;
  setMediaSourceLoop: (sourceId: string, loop: boolean) => void;
}

export interface CreateCompositorStoreOptions {
  controller: IngestControllerV2 | null;
  config?: Partial<CompositorConfig>;
  autoEnable?: boolean;
  /**
   * Read each time the compositor starts, before it is turned on, and applied
   * before it reports itself enabled.
   */
  initialLayout?: () => StudioLayout | Promise<StudioLayout | null | undefined> | null | undefined;
}

export function createCompositorStore(options: CreateCompositorStoreOptions): CompositorStore {
  const { controller, config, autoEnable = true, initialLayout } = options;

  // A stored layout that no longer applies must not keep the compositor off.
  async function startCompositor(
    target: IngestControllerV2,
    enableConfig?: Partial<CompositorConfig>
  ): Promise<void> {
    const requested = initialLayout?.();
    await target.enableCompositor(enableConfig);
    const layout = await requested;
    if (layout) {
      await target.importLayout(layout).catch((error: unknown) => {
        console.warn("[CompositorStore] Could not apply the studio layout:", error);
      });
    }
  }

  const store: Writable<CompositorState> = writable({
    isEnabled: false,
    isInitialized: false,
    rendererType: null,
    stats: null,
    scenes: [],
    activeSceneId: null,
    currentLayout: null,
    overlays: [],
    error: null,
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

    const activeSceneId = sceneManager.getActiveScene()?.id || null;
    store.update((state) => ({
      ...state,
      scenes: sceneManager.getAllScenes(),
      activeSceneId,
      overlays: activeSceneId ? sceneManager.getOverlays(activeSceneId) : [],
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
    startCompositor(controller, config)
      .then(() => {
        console.log("[CompositorStore] Compositor enabled successfully");
        store.update((state) => ({ ...state, isEnabled: true, error: null }));
        setupListeners();
      })
      .catch((error) => {
        console.error("[CompositorStore] Failed to enable compositor:", error);
        store.update((state) => ({
          ...state,
          isEnabled: false,
          isInitialized: false,
          error: error instanceof Error ? error.message : String(error),
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
      await startCompositor(controller, enableConfig || config);
      console.log("[CompositorStore] Compositor enabled successfully");
      store.update((state) => ({ ...state, isEnabled: true, error: null }));
      setupListeners();
    } catch (error) {
      console.error("[CompositorStore] Failed to enable compositor:", error);
      store.update((state) => ({
        ...state,
        isEnabled: false,
        isInitialized: false,
        error: error instanceof Error ? error.message : String(error),
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
      overlays: [],
      error: null,
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

  // Overlays
  async function addImageOverlay(image: Blob): Promise<void> {
    const sceneManager = controller?.getSceneManager();
    const scene = sceneManager?.getActiveScene();
    if (!sceneManager || !scene) return;
    await sceneManager.addImageOverlay(scene.id, image);
    syncState();
  }

  function addTextOverlay(config: TextOverlayConfig): void {
    const sceneManager = controller?.getSceneManager();
    const scene = sceneManager?.getActiveScene();
    if (!sceneManager || !scene) return;
    sceneManager.addTextOverlay(scene.id, config);
    syncState();
  }

  function updateTextOverlay(sourceId: string, config: Partial<TextOverlayConfig>): void {
    const sceneManager = controller?.getSceneManager();
    if (!sceneManager) return;
    sceneManager.updateTextOverlay(sourceId, config);
    syncState();
  }

  function setOverlayPlacement(sourceId: string, placement: Partial<OverlayPlacement>): void {
    const sceneManager = controller?.getSceneManager();
    if (!sceneManager) return;
    sceneManager.setOverlayPlacement(sourceId, placement);
    syncState();
  }

  function removeOverlay(sourceId: string): void {
    const sceneManager = controller?.getSceneManager();
    if (!sceneManager) return;
    sceneManager.removeOverlay(sourceId);
    syncState();
  }

  // Layout presets
  function applyLayout(layout: LayoutConfig): void {
    const sceneManager = controller?.getSceneManager();
    if (!sceneManager) return;

    sceneManager.applyLayout(layout);
    store.update((state) => ({ ...state, currentLayout: layout }));
    syncState();
  }

  function cycleSourceOrder(direction: "forward" | "backward" = "forward"): void {
    const sceneManager = controller?.getSceneManager();
    if (!sceneManager) return;

    sceneManager.cycleSourceOrder(direction);
    syncState();
  }

  // The new renderer type arrives through the scene manager's rendererChanged event.
  function setRenderer(renderer: RendererType): void {
    controller?.getSceneManager()?.setRenderer(renderer);
  }

  async function exportLayout(): Promise<StudioLayout | null> {
    return controller ? controller.exportLayout() : null;
  }

  async function importLayout(layout: StudioLayout): Promise<void> {
    if (!controller) return;
    await controller.importLayout(layout);
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

    addImageOverlay,
    addTextOverlay,
    updateTextOverlay,
    setOverlayPlacement,
    removeOverlay,

    applyLayout,
    cycleSourceOrder,
    setRenderer,
    exportLayout,
    importLayout,
    destroy,
  };
}
