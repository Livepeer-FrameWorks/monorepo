/**
 * SceneManager
 *
 * Main thread coordinator for the compositor system.
 * Manages scenes, layers, and coordinates with the compositor worker.
 *
 * Responsibilities:
 * - Scene CRUD (create, read, update, delete)
 * - Layer management within scenes
 * - Source → Layer binding
 * - Transition triggering
 * - Frame extraction and forwarding to worker
 * - Output track management
 */

import { TextOverlaySource, type TextOverlayConfig } from "./TextOverlaySource";
import { TypedEventEmitter } from "./EventEmitter";
import type {
  Scene,
  Layer,
  LayerKind,
  LayerTransform,
  OverlayPlacement,
  StudioOverlay,
  CompositorConfig,
  TransitionConfig,
  LayoutConfig,
  LayoutTransitionConfig,
  FilterConfig,
  RendererType,
  RendererStats,
  SceneManagerEvents,
  CompositorMainToWorker,
  CompositorWorkerToMain,
} from "../types";
import { DEFAULT_LAYER_TRANSFORM, DEFAULT_COMPOSITOR_CONFIG } from "../types";
import { applyLayout, createDefaultLayoutConfig } from "./layouts";
import { createDefaultTransitionConfig, validateTransitionConfig } from "./TransitionEngine";

// Capture the IIFE script src at module-load time (only available during synchronous execution).
// In ESM/bundled contexts this is undefined — the import.meta.url paths handle those.
const _iifeScriptSrc: string | undefined =
  typeof document !== "undefined"
    ? (document.currentScript?.getAttribute("src") ?? undefined)
    : undefined;

/**
 * Default layout transition configuration
 */
const DEFAULT_LAYOUT_TRANSITION: LayoutTransitionConfig = {
  durationMs: 300,
  easing: "ease-out",
};

/** Lower left, clear of the frame edge. */
const DEFAULT_TEXT_PLACEMENT: Partial<OverlayPlacement> = { x: 0.04, y: 0.92 };
/** Top right, at a size that suits a logo. */
const DEFAULT_IMAGE_PLACEMENT: OverlayPlacement = { x: 0.97, y: 0.05, size: 0.15 };

interface OverlayRecord {
  kind: StudioOverlay["kind"];
  label: string;
  /** Width over height of the overlay's bitmap, in pixels. */
  aspect: number;
  placement: OverlayPlacement;
  text?: { source: TextOverlaySource; config: TextOverlayConfig };
}

export class SceneManager extends TypedEventEmitter<SceneManagerEvents> {
  private scenes: Map<string, Scene> = new Map();
  private activeSceneId: string | null = null;
  private config: CompositorConfig;
  private defaultTransition: TransitionConfig;
  private defaultLayoutTransition: LayoutTransitionConfig = DEFAULT_LAYOUT_TRANSITION;
  private currentLayout: LayoutConfig;
  private isAnimating = false;
  private pendingTransitions: (() => void)[] = [];

  // Worker communication
  private worker: Worker | null = null;
  private workerUrl = "";
  private workerReady = false;
  private pendingMessages: CompositorMainToWorker[] = [];

  // Frame extraction
  // eslint-disable-next-line @typescript-eslint/no-explicit-any
  private frameProcessors: Map<string, any> = new Map();
  // eslint-disable-next-line @typescript-eslint/no-explicit-any
  private frameReaders: Map<string, ReadableStreamDefaultReader<any>> = new Map();
  private frameFallbacks: Map<
    string,
    {
      video: HTMLVideoElement;
      stopped: boolean;
      timer: number | null;
      frameCallback: number | null;
      busy: boolean;
      canvas: HTMLCanvasElement | null;
    }
  > = new Map();

  // Output
  private outputCanvas: HTMLCanvasElement | null = null;
  private outputStream: MediaStream | null = null;

  // Stats
  private lastStats: RendererStats = { fps: 0, frameTimeMs: 0 };

  // Direct frame output callback (bypasses captureStream for WebCodecs path)
  private frameCallback: ((frame: VideoFrame) => void) | null = null;

  // Overlay tracking
  private overlays: Map<string, OverlayRecord> = new Map();
  private persistentOverlays: Map<
    string,
    { sourceId: string; transform?: Partial<LayerTransform> }
  > = new Map();

  constructor(config?: Partial<CompositorConfig>, options?: { workerUrl?: string }) {
    super();
    this.config = { ...DEFAULT_COMPOSITOR_CONFIG, ...config };
    this.workerUrl = options?.workerUrl ?? "";
    this.defaultTransition = this.config.defaultTransition || createDefaultTransitionConfig();
    this.currentLayout = createDefaultLayoutConfig();
  }

  // ============================================================================
  // Worker Loading
  // ============================================================================

  /**
   * Initialize the worker with fallback loading strategies
   */
  private async initializeWorker(): Promise<void> {
    // Fallback: try loading from paths (for development or custom deployments)
    let lastError: Error | null = null;

    const tryWorkerFactory = async (
      label: string,
      createWorker: () => Worker
    ): Promise<boolean> => {
      try {
        const testWorker = createWorker();

        const success = await new Promise<boolean>((resolve) => {
          const timeout = setTimeout(() => {
            testWorker.terminate();
            resolve(false);
          }, 2000);

          testWorker.onerror = () => {
            clearTimeout(timeout);
            testWorker.terminate();
            resolve(false);
          };

          testWorker.onmessage = () => {
            clearTimeout(timeout);
            resolve(true);
          };

          // Some workers don't post an immediate message on startup.
          setTimeout(() => {
            clearTimeout(timeout);
            resolve(true);
          }, 500);
        });

        if (!success) {
          return false;
        }

        testWorker.terminate();
        this.worker = createWorker();
        console.log(`[SceneManager] Worker loaded from: ${label}`);
        return true;
      } catch (e) {
        lastError = e instanceof Error ? e : new Error(String(e));
        console.warn(`[SceneManager] Failed to create worker from ${label}:`, lastError.message);
        return false;
      }
    };

    // Consumer override for environments where package-relative worker URLs are not reachable.
    if (this.workerUrl) {
      const loaded =
        (await tryWorkerFactory(
          this.workerUrl,
          () => new Worker(this.workerUrl, { type: "module" })
        )) || (await tryWorkerFactory(this.workerUrl, () => new Worker(this.workerUrl)));
      if (loaded) {
        return;
      }
    }

    // Bundler-managed worker URL patterns. Keep new URL(...) inline in new Worker(...)
    // so host app bundlers can emit/rehydrate worker assets.
    if (
      await tryWorkerFactory(
        "bundler ../../workers (.js)",
        () =>
          new Worker(new URL("../../workers/compositor.worker.js", import.meta.url), {
            type: "module",
          })
      )
    ) {
      return;
    }

    // First-pass candidates relative to the current module URL.
    // Keep these as plain href strings so downstream app bundlers don't require static
    // worker-module graph resolution for the streamcrafter-core package internals.
    const moduleRelativeCandidates = [
      new URL("../workers/compositor.worker.mod.js", import.meta.url).href,
      new URL("../workers/compositor.worker.js", import.meta.url).href,
      new URL("../workers/compositor.worker.ts", import.meta.url).href,
      new URL("../../workers/compositor.worker.js", import.meta.url).href,
    ];

    for (const path of moduleRelativeCandidates) {
      const loaded =
        (await tryWorkerFactory(path, () => new Worker(path, { type: "module" }))) ||
        (await tryWorkerFactory(path, () => new Worker(path)));
      if (loaded) {
        return;
      }
    }

    // IIFE scripts: derive worker path relative to the script's own URL.
    // document.currentScript is only available during synchronous <script> execution,
    // so SceneManager captures it at module-load time via the module-scope constant below.
    const workerPaths: string[] = [];
    if (typeof _iifeScriptSrc === "string" && _iifeScriptSrc) {
      try {
        const base = new URL("./workers/compositor.worker.js", _iifeScriptSrc).href;
        workerPaths.push(base);
      } catch {
        // invalid URL, skip
      }
    }

    workerPaths.push(
      // Web Component package bundles workers in its own dist/workers folder.
      "/node_modules/@livepeer-frameworks/streamcrafter-wc/dist/workers/compositor.worker.js",
      // Vite dev server with pnpm workspace symlinks
      "/node_modules/@livepeer-frameworks/streamcrafter-core/dist/workers/compositor.worker.js",
      "/node_modules/@livepeer-frameworks/streamcrafter-core/src/workers/compositor.worker.mod.js",
      // Workspace source fallback (Vite can transpile .ts workers in dev)
      "/node_modules/@livepeer-frameworks/streamcrafter-core/src/workers/compositor.worker.ts",
      // Public folder paths
      "/workers/compositor.worker.js",
      "./workers/compositor.worker.js"
    );

    console.log("[SceneManager] Trying fallback worker paths:", workerPaths);

    for (const path of workerPaths) {
      if (this.worker) break;

      try {
        console.log(`[SceneManager] Trying worker path: ${path}`);
        const loaded =
          (await tryWorkerFactory(path, () => new Worker(path, { type: "module" }))) ||
          (await tryWorkerFactory(path, () => new Worker(path)));
        if (!loaded) {
          console.warn(`[SceneManager] Worker failed to load from: ${path}`);
        }
      } catch (e) {
        lastError = e instanceof Error ? e : new Error(String(e));
        console.warn(`[SceneManager] Failed to load worker from ${path}:`, lastError.message);
        this.worker = null;
      }
    }

    if (!this.worker) {
      throw new Error(
        `Failed to initialize compositor worker. ` +
          `Make sure the worker is bundled correctly. ` +
          `Last error: ${lastError?.message ?? "unknown"}`
      );
    }
  }

  // ============================================================================
  // Initialization
  // ============================================================================

  /**
   * Initialize the compositor system
   * Creates the worker and sets up the output canvas
   * Returns when the worker is ready
   */
  async initialize(): Promise<void> {
    console.log("[SceneManager] initialize() called");

    if (this.worker) {
      throw new Error("SceneManager already initialized");
    }

    // Create output canvas
    console.log("[SceneManager] Creating output canvas", {
      width: this.config.width,
      height: this.config.height,
    });
    this.outputCanvas = document.createElement("canvas");
    this.outputCanvas.width = this.config.width;
    this.outputCanvas.height = this.config.height;

    // Get OffscreenCanvas for worker
    const offscreen = this.outputCanvas.transferControlToOffscreen();
    console.log("[SceneManager] Created OffscreenCanvas");

    // Create the worker using the same strategy as the player
    console.log("[SceneManager] Initializing worker...");
    await this.initializeWorker();
    console.log("[SceneManager] Worker initialized, waiting for ready...");

    // Create a promise that resolves when worker sends 'ready'
    const readyPromise = new Promise<void>((resolve, reject) => {
      const timeout = setTimeout(() => {
        console.error("[SceneManager] Worker initialization timeout");
        reject(new Error("Compositor worker initialization timeout"));
      }, 10000); // 10 second timeout

      // Set up message handler (worker is guaranteed non-null after initializeWorker)
      this.worker!.onmessage = (e: MessageEvent<CompositorWorkerToMain>) => {
        console.log("[SceneManager] Worker message:", e.data.type);
        if (e.data.type === "ready") {
          clearTimeout(timeout);
          resolve();
        }
        this.handleWorkerMessage(e.data);
      };

      this.worker!.onerror = (e: ErrorEvent) => {
        console.error("[SceneManager] Worker error:", e.message);
        clearTimeout(timeout);
        reject(new Error(e.message));
      };
    });

    // Initialize the worker with the offscreen canvas
    // Note: Send directly (not via sendToWorker) because workerReady is still false
    console.log("[SceneManager] Sending init message to worker");
    this.worker!.postMessage({ type: "init", config: this.config, canvas: offscreen }, [offscreen]);

    // Wait for worker to be ready
    await readyPromise;
    console.log("[SceneManager] Worker is ready");

    // Create default scene
    console.log("[SceneManager] Creating default scene");
    const defaultScene = this.createScene("Default");
    console.log("[SceneManager] Setting active scene:", defaultScene.id);
    this.setActiveScene(defaultScene.id);
    console.log("[SceneManager] Initialize complete");
  }

  /**
   * Handle messages from the compositor worker
   */
  private handleWorkerMessage(message: CompositorWorkerToMain): void {
    switch (message.type) {
      case "ready":
        this.workerReady = true;
        // Send any pending messages
        for (const msg of this.pendingMessages) {
          this.worker?.postMessage(msg);
        }
        this.pendingMessages = [];
        break;

      case "stats":
        this.lastStats = message.stats;
        this.emit("statsUpdate", { stats: message.stats });
        break;

      case "transitionComplete":
        this.emit("transitionCompleted", { sceneId: message.sceneId });
        this.settlePendingTransitions();
        break;

      case "layoutAnimationComplete": {
        this.isAnimating = false;
        const activeScene = this.getActiveScene();
        if (activeScene) {
          activeScene.layers = this.buildLayoutLayers(activeScene, this.currentLayout);
        }
        this.emit("layoutAnimationCompleted", { layout: this.currentLayout });
        break;
      }

      case "rendererChanged":
        // Update the config with the actual renderer type (important for 'auto' mode)
        this.config.renderer = message.renderer;
        this.emit("rendererChanged", { renderer: message.renderer });
        break;

      case "compositorFrame":
        if (this.frameCallback) {
          this.frameCallback(message.frame);
        } else {
          // No consumer — close frame to avoid memory leak
          message.frame.close();
        }
        break;

      case "error":
        this.emit("error", { message: message.message });
        break;
    }
  }

  /**
   * Send a message to the worker
   */
  private sendToWorker(message: CompositorMainToWorker, transfer?: Transferable[]): void {
    if (this.workerReady && this.worker) {
      this.worker.postMessage(message, transfer || []);
    } else {
      // Queue the message until worker is ready
      this.pendingMessages.push(message);
    }
  }

  // ============================================================================
  // Scene Management
  // ============================================================================

  /**
   * Create a new scene
   */
  createScene(name: string, backgroundColor = "#000000"): Scene {
    const id = `scene-${Date.now()}-${Math.random().toString(36).slice(2, 9)}`;

    const scene: Scene = {
      id,
      name,
      layers: [],
      backgroundColor,
    };

    this.scenes.set(id, scene);
    this.emit("sceneCreated", { scene });

    // Auto-add persistent overlays to new scenes
    for (const [sourceId, overlay] of this.persistentOverlays) {
      const defaultTransform: Partial<LayerTransform> = {
        x: 0,
        y: 0,
        width: 1,
        height: 1,
        opacity: 1,
        ...overlay.transform,
      };
      this.addLayer(id, sourceId, defaultTransform, { kind: "overlay" });
    }

    return scene;
  }

  /**
   * Delete a scene
   */
  deleteScene(sceneId: string): void {
    if (!this.scenes.has(sceneId)) {
      throw new Error(`Scene not found: ${sceneId}`);
    }

    if (this.activeSceneId === sceneId) {
      throw new Error("Cannot delete the active scene");
    }

    this.scenes.delete(sceneId);
    this.emit("sceneDeleted", { sceneId });
  }

  /**
   * Get a scene by ID
   */
  getScene(sceneId: string): Scene | undefined {
    return this.scenes.get(sceneId);
  }

  /**
   * Get all scenes
   */
  getAllScenes(): Scene[] {
    return Array.from(this.scenes.values());
  }

  /**
   * Get the active scene
   */
  getActiveScene(): Scene | undefined {
    return this.activeSceneId ? this.scenes.get(this.activeSceneId) : undefined;
  }

  /**
   * Set the active scene (instant switch, no transition)
   */
  setActiveScene(sceneId: string): void {
    const scene = this.scenes.get(sceneId);
    if (!scene) {
      throw new Error(`Scene not found: ${sceneId}`);
    }

    const previousSceneId = this.activeSceneId;
    this.activeSceneId = sceneId;

    // Update the worker
    this.sendToWorker({ type: "updateScene", scene });

    this.emit("sceneActivated", { scene, previousSceneId });
  }

  /**
   * Transition to a new scene. The target is the active scene from the start;
   * the promise resolves once the worker has finished the transition, which is
   * also when a transition interrupted by a later one settles.
   */
  async transitionTo(sceneId: string, transition?: TransitionConfig): Promise<void> {
    const scene = this.scenes.get(sceneId);
    if (!scene) {
      throw new Error(`Scene not found: ${sceneId}`);
    }

    const transitionConfig = validateTransitionConfig(transition || this.defaultTransition);
    const finished = new Promise<void>((resolve) => this.pendingTransitions.push(resolve));

    // The target travels with the transition: an updateScene first would
    // replace the worker's current scene and leave nothing to transition from.
    this.sendToWorker({
      type: "startTransition",
      transition: transitionConfig,
      toScene: scene,
    });

    const previousSceneId = this.activeSceneId;
    this.activeSceneId = sceneId;

    this.emit("transitionStarted", {
      fromSceneId: previousSceneId || "",
      toSceneId: sceneId,
      transition: transitionConfig,
    });

    return finished;
  }

  private settlePendingTransitions(): void {
    const pending = this.pendingTransitions;
    this.pendingTransitions = [];
    pending.forEach((resolve) => resolve());
  }

  // ============================================================================
  // Layer Management
  // ============================================================================

  /**
   * Add a layer to a scene
   */
  addLayer(
    sceneId: string,
    sourceId: string,
    transform?: Partial<LayerTransform>,
    options?: { kind?: LayerKind; label?: string }
  ): Layer {
    const scene = this.scenes.get(sceneId);
    if (!scene) {
      throw new Error(`Scene not found: ${sceneId}`);
    }

    const layerId = `layer-${Date.now()}-${Math.random().toString(36).slice(2, 9)}`;
    const kind = options?.kind ?? "source";
    const isOverlay = kind === "overlay";
    const stack = isOverlay ? scene.layers : scene.layers.filter((l) => l.kind !== "overlay");
    const zIndex = stack.reduce((max, l) => Math.max(max, l.zIndex), -1) + 1;

    // A new source goes under the overlays, which move up to make room.
    if (!isOverlay) {
      for (const existing of scene.layers) {
        if (existing.kind === "overlay" && existing.zIndex >= zIndex) existing.zIndex += 1;
      }
    }

    const layer: Layer = {
      id: layerId,
      sourceId,
      kind,
      ...(options?.label !== undefined && { label: options.label }),
      visible: true,
      locked: false,
      zIndex,
      transform: { ...DEFAULT_LAYER_TRANSFORM, ...transform },
      // A layout's crop or stretch is for its slots; an overlay keeps its picture whole.
      scalingMode: isOverlay ? "letterbox" : (this.currentLayout.scalingMode ?? "letterbox"),
    };

    scene.layers.push(layer);
    this.updateSceneInWorker(scene);

    this.emit("layerAdded", { sceneId, layer });
    return layer;
  }

  /**
   * Remove a layer from a scene
   */
  removeLayer(sceneId: string, layerId: string): void {
    const scene = this.scenes.get(sceneId);
    if (!scene) {
      throw new Error(`Scene not found: ${sceneId}`);
    }

    const index = scene.layers.findIndex((l) => l.id === layerId);
    if (index === -1) {
      throw new Error(`Layer not found: ${layerId}`);
    }

    scene.layers.splice(index, 1);
    this.updateSceneInWorker(scene);

    this.emit("layerRemoved", { sceneId, layerId });
  }

  /**
   * Remove every layer showing a source, in all scenes.
   */
  removeSourceLayers(sourceId: string): void {
    for (const scene of this.scenes.values()) {
      for (const layer of scene.layers.filter((l) => l.sourceId === sourceId)) {
        this.removeLayer(scene.id, layer.id);
      }
    }
  }

  /**
   * Update a layer's transform
   */
  updateLayerTransform(sceneId: string, layerId: string, transform: Partial<LayerTransform>): void {
    const scene = this.scenes.get(sceneId);
    if (!scene) {
      throw new Error(`Scene not found: ${sceneId}`);
    }

    const layer = scene.layers.find((l) => l.id === layerId);
    if (!layer) {
      throw new Error(`Layer not found: ${layerId}`);
    }

    layer.transform = { ...layer.transform, ...transform };
    this.updateSceneInWorker(scene);

    this.emit("layerUpdated", { sceneId, layer });
  }

  /**
   * Toggle layer visibility
   */
  setLayerVisibility(sceneId: string, layerId: string, visible: boolean): void {
    const scene = this.scenes.get(sceneId);
    if (!scene) {
      throw new Error(`Scene not found: ${sceneId}`);
    }

    const layer = scene.layers.find((l) => l.id === layerId);
    if (!layer) {
      throw new Error(`Layer not found: ${layerId}`);
    }

    layer.visible = visible;
    scene.layers = this.buildLayoutLayers(scene, this.currentLayout);
    this.updateSceneInWorker(scene);

    this.emit("layerUpdated", { sceneId, layer });
  }

  /**
   * Reorder layers in a scene
   */
  reorderLayers(sceneId: string, layerIds: string[]): void {
    const scene = this.scenes.get(sceneId);
    if (!scene) {
      throw new Error(`Scene not found: ${sceneId}`);
    }

    // Reorder based on the provided IDs
    layerIds.forEach((id, index) => {
      const layer = scene.layers.find((l) => l.id === id);
      if (layer) {
        layer.zIndex = index;
      }
    });

    // A layout assigns its slots by stacking order, so the active scene is
    // laid out again; an overlay would otherwise end up under the main source.
    if (this.activeSceneId === sceneId) {
      this.applyLayout(this.currentLayout, true, { durationMs: 200, easing: "ease-out" });
    }
  }

  /**
   * Cycle the source order in the active scene (rotate layers)
   * @param direction - 'forward' moves first to last, 'backward' moves last to first
   */
  cycleSourceOrder(direction: "forward" | "backward" = "forward"): void {
    const scene = this.getActiveScene();
    if (!scene || scene.layers.length < 2) {
      console.warn("[SceneManager] cycleSourceOrder: Need at least 2 layers");
      return;
    }

    // Get layer IDs sorted by current zIndex
    const sortedLayers = [...scene.layers]
      .filter((l) => l.visible && l.kind !== "overlay")
      .sort((a, b) => a.zIndex - b.zIndex);
    if (sortedLayers.length < 2) {
      console.warn("[SceneManager] cycleSourceOrder: Need at least 2 visible layers");
      return;
    }
    const layerIds = sortedLayers.map((l) => l.id);

    // Rotate the array
    if (direction === "forward") {
      const first = layerIds.shift();
      if (first) layerIds.push(first);
    } else {
      const last = layerIds.pop();
      if (last) layerIds.unshift(last);
    }

    this.reorderLayers(scene.id, layerIds);

    this.emit("layerUpdated", { sceneId: scene.id, layer: scene.layers[0] });
  }

  /**
   * Update scene in the worker
   */
  private updateSceneInWorker(scene: Scene): void {
    if (this.activeSceneId === scene.id) {
      this.sendToWorker({ type: "updateScene", scene });
    }
  }

  // ============================================================================
  // Layout Presets
  // ============================================================================

  /**
   * Apply a layout preset to the active scene
   * @param layout - Layout configuration to apply
   * @param animate - Whether to animate the transition (default: true)
   * @param transition - Custom transition config (uses default if not provided)
   */
  applyLayout(
    layout: LayoutConfig,
    animate = true,
    transition?: Partial<LayoutTransitionConfig>
  ): void {
    const scene = this.getActiveScene();
    if (!scene) {
      console.warn("[SceneManager] applyLayout: No active scene");
      return;
    }

    this.currentLayout = layout;

    const newLayers = this.buildLayoutLayers(scene, layout);
    const hasVisibleLayers = newLayers.some((layer) => layer.visible);

    // Create target scene with new layers
    const targetScene: Scene = {
      ...scene,
      layers: newLayers,
    };

    // Always update scene.layers to keep state in sync
    scene.layers = newLayers;

    if (animate && hasVisibleLayers) {
      // Animate to the new layout
      const transitionConfig: LayoutTransitionConfig = {
        ...this.defaultLayoutTransition,
        ...transition,
      };

      this.isAnimating = true;
      this.emit("layoutAnimationStarted", { layout });
      this.sendToWorker({
        type: "animateLayout",
        targetScene,
        transition: transitionConfig,
      });
    } else {
      // Instant update (no animation)
      this.updateSceneInWorker(scene);
    }

    // Also send layout update to worker (for metadata tracking)
    this.sendToWorker({ type: "updateLayout", layout });
  }

  private buildLayoutLayers(scene: Scene, layout: LayoutConfig): Layer[] {
    const sourceStack = scene.layers
      .filter((layer) => layer.kind !== "overlay")
      .sort((a, b) => a.zIndex - b.zIndex);
    const existingBySource = new Map(sourceStack.map((layer) => [layer.sourceId, layer]));
    const visibleSourceIds = sourceStack
      .filter((layer) => layer.visible)
      .map((layer) => layer.sourceId);
    const layoutLayers = applyLayout(layout, visibleSourceIds);

    const placed = [...layoutLayers]
      .sort((a, b) => a.zIndex - b.zIndex)
      .map((layoutLayer) => {
        const existing = existingBySource.get(layoutLayer.sourceId);
        if (!existing) return layoutLayer;
        return {
          ...existing,
          transform: layoutLayer.transform,
          scalingMode: layoutLayer.scalingMode,
        };
      });

    // Layers the layout has no slot for, hidden ones included, stay in the scene
    // at their place in the stack; the placed layers fill the remaining places.
    const placedSources = new Set(layoutLayers.map((layer) => layer.sourceId));
    const stackedLayers = sourceStack.map((layer, zIndex) => {
      const next = placedSources.has(layer.sourceId) ? placed.shift() : undefined;
      return { ...(next ?? layer), zIndex };
    });

    const overlays = scene.layers
      .filter((layer) => layer.kind === "overlay")
      .sort((a, b) => a.zIndex - b.zIndex)
      .map((layer, index) => ({ ...layer, zIndex: stackedLayers.length + index }));

    return [...stackedLayers, ...overlays];
  }

  /**
   * Set the default layout transition configuration
   */
  setDefaultLayoutTransition(transition: Partial<LayoutTransitionConfig>): void {
    this.defaultLayoutTransition = {
      ...this.defaultLayoutTransition,
      ...transition,
    };
  }

  /**
   * Check if a layout animation is currently in progress
   */
  isLayoutAnimating(): boolean {
    return this.isAnimating;
  }

  /**
   * Get the current layout
   */
  getCurrentLayout(): LayoutConfig {
    return this.currentLayout;
  }

  // ============================================================================
  // Source Binding
  // ============================================================================

  /**
   * Bind a MediaStream source to the compositor
   * This extracts frames and sends them to the worker
   */
  bindSource(sourceId: string, stream: MediaStream): void {
    // Get the video track
    const videoTrack = stream.getVideoTracks()[0];
    if (!videoTrack) {
      throw new Error("No video track in stream");
    }

    // Create a MediaStreamTrackProcessor to extract frames
    // MediaStreamTrackProcessor is experimental and not in all TypeScript defs
    // eslint-disable-next-line @typescript-eslint/no-explicit-any
    const MediaStreamTrackProcessorCtor = (globalThis as any).MediaStreamTrackProcessor;
    if (!MediaStreamTrackProcessorCtor) {
      this.bindSourceViaVideoElement(sourceId, stream);
      return;
    }

    const processor = new MediaStreamTrackProcessorCtor({ track: videoTrack });
    this.frameProcessors.set(sourceId, processor);

    // Read frames and send to worker
    const reader = processor.readable.getReader();
    this.frameReaders.set(sourceId, reader);

    const readFrame = async (): Promise<void> => {
      try {
        const { done, value } = await reader.read();
        if (done || !value) return;

        // Cast to VideoFrame since we know it's a video track processor
        const frame = value as VideoFrame;

        // A read still pending at unbind can deliver one last frame.
        if (this.frameReaders.get(sourceId) !== reader) {
          frame.close();
          return;
        }

        // Send frame to worker (transfer ownership)
        this.sendToWorker({ type: "sourceFrame", sourceId, frame }, [frame]);

        // Continue reading
        readFrame();
      } catch (error) {
        // Ignore errors from cancelled readers (expected during unbind)
        if ((error as Error)?.name !== "AbortError") {
          console.error("[SceneManager] Frame read error:", error);
        }
      }
    };

    readFrame();
  }

  private bindSourceViaVideoElement(sourceId: string, stream: MediaStream): void {
    this.unbindSource(sourceId);

    const video = document.createElement("video");
    video.muted = true;
    video.playsInline = true;
    video.srcObject = stream;

    const fallback = {
      video,
      stopped: false,
      timer: null as number | null,
      frameCallback: null as number | null,
      busy: false,
      canvas: null as HTMLCanvasElement | null,
    };
    this.frameFallbacks.set(sourceId, fallback);

    const intervalMs = Math.max(16, Math.floor(1000 / Math.max(1, this.config.frameRate)));

    const schedule = () => {
      if (fallback.stopped) return;
      const rvfc = (video as any).requestVideoFrameCallback;
      if (typeof rvfc === "function") {
        fallback.frameCallback = rvfc.call(video, () => {
          fallback.frameCallback = null;
          void pushFrame();
        });
      } else {
        fallback.timer = window.setTimeout(() => {
          fallback.timer = null;
          void pushFrame();
        }, intervalMs);
      }
    };

    const makeBitmap = async (): Promise<ImageBitmap | null> => {
      if (video.readyState < HTMLMediaElement.HAVE_CURRENT_DATA) return null;
      if (video.videoWidth <= 0 || video.videoHeight <= 0) return null;
      try {
        return await createImageBitmap(video);
      } catch {
        const canvas = fallback.canvas ?? document.createElement("canvas");
        fallback.canvas = canvas;
        canvas.width = video.videoWidth;
        canvas.height = video.videoHeight;
        const ctx = canvas.getContext("2d");
        if (!ctx) return null;
        ctx.drawImage(video, 0, 0, canvas.width, canvas.height);
        return await createImageBitmap(canvas);
      }
    };

    // schedule() and pushFrame() call each other; schedule first runs below, after both exist.
    const pushFrame = async (): Promise<void> => {
      if (fallback.stopped) return;
      if (!fallback.busy) {
        fallback.busy = true;
        try {
          const bitmap = await makeBitmap();
          if (bitmap) {
            this.sendToWorker({ type: "sourceImage", sourceId, bitmap }, [bitmap]);
          }
        } catch (error) {
          console.warn("[SceneManager] Video-element frame extraction failed:", error);
        } finally {
          fallback.busy = false;
        }
      }
      schedule();
    };

    void video.play().catch((error) => {
      console.warn("[SceneManager] Video-element source playback failed:", error);
    });
    schedule();
  }

  /**
   * Unbind a source from the compositor
   */
  unbindSource(sourceId: string): void {
    // Cancel the reader first to stop the read loop
    const reader = this.frameReaders.get(sourceId);
    if (reader) {
      reader.cancel().catch(() => {});
      this.frameReaders.delete(sourceId);
    }

    const processor = this.frameProcessors.get(sourceId);
    if (processor) {
      this.frameProcessors.delete(sourceId);
    }

    const fallback = this.frameFallbacks.get(sourceId);
    if (fallback) {
      fallback.stopped = true;
      if (fallback.timer !== null) {
        clearTimeout(fallback.timer);
      }
      const cancelRVFC = (fallback.video as any).cancelVideoFrameCallback;
      if (fallback.frameCallback !== null && typeof cancelRVFC === "function") {
        cancelRVFC.call(fallback.video, fallback.frameCallback);
      }
      fallback.video.pause();
      fallback.video.srcObject = null;
      this.frameFallbacks.delete(sourceId);
    }
  }

  /**
   * Drop a source's last frame in the worker. Unbinding alone leaves it there,
   * which is what keeps a paused or rebound source on screen.
   */
  releaseSource(sourceId: string): void {
    this.sendToWorker({ type: "removeSource", sourceId });
  }

  /**
   * Bind an image source to the compositor
   */
  async bindImageSource(sourceId: string, imageUrl: string): Promise<void> {
    const bitmap = await this.decodeImage(imageUrl);
    this.sendToWorker({ type: "sourceImage", sourceId, bitmap }, [bitmap]);
  }

  /** Decode an image in this browser; a Blob is never sent anywhere. */
  private async decodeImage(image: Blob | string): Promise<ImageBitmap> {
    const blob = typeof image === "string" ? await (await fetch(image)).blob() : image;
    const bitmap = await createImageBitmap(blob).catch(() => this.decodeThroughElement(blob));

    const scale = Math.min(this.config.width / bitmap.width, this.config.height / bitmap.height);
    if (scale >= 1) return bitmap;

    const resized = await createImageBitmap(bitmap, {
      resizeWidth: Math.round(bitmap.width * scale),
      resizeHeight: Math.round(bitmap.height * scale),
      resizeQuality: "high",
    });
    bitmap.close();
    return resized;
  }

  /** SVG has no decoder behind createImageBitmap(blob), only behind an image element. */
  private async decodeThroughElement(blob: Blob): Promise<ImageBitmap> {
    const url = URL.createObjectURL(blob);
    try {
      const element = new Image();
      element.src = url;
      await element.decode();
      return await createImageBitmap(element);
    } finally {
      URL.revokeObjectURL(url);
    }
  }

  // ============================================================================
  // Overlays
  // ============================================================================

  /** The image and text overlays in a scene, bottom to top. */
  getOverlays(sceneId: string): StudioOverlay[] {
    const layers = this.scenes.get(sceneId)?.layers ?? [];
    return [...layers]
      .sort((a, b) => a.zIndex - b.zIndex)
      .flatMap((layer) => {
        const overlay = this.overlays.get(layer.sourceId);
        if (!overlay) return [];
        return [
          {
            ...overlay.placement,
            sourceId: layer.sourceId,
            layerId: layer.id,
            kind: overlay.kind,
            label: overlay.label,
            ...(overlay.text && { text: overlay.text.config }),
          },
        ];
      });
  }

  /** Move an overlay, or resize an image overlay, in every scene it is in. */
  setOverlayPlacement(sourceId: string, placement: Partial<OverlayPlacement>): void {
    const overlay = this.overlays.get(sourceId);
    if (!overlay) throw new Error(`Overlay not found: ${sourceId}`);

    overlay.placement = { ...overlay.placement, ...placement };
    this.applyOverlayRecord(sourceId, overlay);
  }

  /** Remove an overlay from every scene and let go of its bitmap. */
  removeOverlay(sourceId: string): void {
    if (!this.overlays.delete(sourceId)) return;
    this.removeSourceLayers(sourceId);
    this.releaseSource(sourceId);
  }

  private overlayTransform(overlay: OverlayRecord): Partial<LayerTransform> {
    const { width: frameWidth, height: frameHeight } = this.config;
    const frameAspect = frameWidth / frameHeight;

    let width = overlay.placement.size;
    let height = (width * frameAspect) / overlay.aspect;
    if (height > 1) {
      width /= height;
      height = 1;
    }
    return {
      x: overlay.placement.x * (1 - width),
      y: overlay.placement.y * (1 - height),
      width,
      height,
    };
  }

  private applyOverlayRecord(sourceId: string, overlay: OverlayRecord): void {
    const transform = this.overlayTransform(overlay);
    for (const scene of this.scenes.values()) {
      for (const layer of scene.layers.filter((l) => l.sourceId === sourceId)) {
        layer.label = overlay.label;
        this.updateLayerTransform(scene.id, layer.id, transform);
      }
    }
  }

  private addOverlayLayer(
    sceneId: string,
    sourceId: string,
    overlay: OverlayRecord
  ): { layerId: string; sourceId: string } {
    this.overlays.set(sourceId, overlay);
    const layer = this.addLayer(sceneId, sourceId, this.overlayTransform(overlay), {
      kind: "overlay",
      label: overlay.label,
    });
    return { layerId: layer.id, sourceId };
  }

  // ============================================================================
  // Text Overlays
  // ============================================================================

  /**
   * Add a text overlay to a scene. Its bitmap is drawn here, on the main thread
   * where fonts are available, and its layer is as large as the text.
   */
  addTextOverlay(
    sceneId: string,
    config: TextOverlayConfig,
    placement?: Partial<OverlayPlacement>
  ): { layerId: string; sourceId: string } {
    const scene = this.scenes.get(sceneId);
    if (!scene) throw new Error(`Scene not found: ${sceneId}`);

    const sourceId = `text-${Date.now()}-${Math.random().toString(36).slice(2, 9)}`;
    const source = new TextOverlaySource(sourceId, this.config.width, this.config.height);
    const textConfig: TextOverlayConfig = { style: "lower-third", ...config };
    const bitmap = source.render(textConfig);
    const size = bitmap.width / this.config.width;
    const aspect = bitmap.width / bitmap.height;

    this.sendToWorker({ type: "sourceImage", sourceId, bitmap }, [bitmap]);

    return this.addOverlayLayer(sceneId, sourceId, {
      kind: "text",
      label: config.text,
      aspect,
      placement: { x: 0, y: 0, ...DEFAULT_TEXT_PLACEMENT, ...placement, size },
      text: { source, config: textConfig },
    });
  }

  /**
   * Update the text content/style of an existing text overlay.
   */
  updateTextOverlay(sourceId: string, config: Partial<TextOverlayConfig>): void {
    const overlay = this.overlays.get(sourceId);
    if (!overlay?.text) throw new Error(`Text overlay not found: ${sourceId}`);

    const textConfig: TextOverlayConfig = { ...overlay.text.config, ...config };
    const bitmap = overlay.text.source.render(textConfig);
    overlay.text.config = textConfig;
    overlay.label = textConfig.text;
    overlay.aspect = bitmap.width / bitmap.height;
    overlay.placement = { ...overlay.placement, size: bitmap.width / this.config.width };

    this.sendToWorker({ type: "sourceImage", sourceId, bitmap }, [bitmap]);
    this.applyOverlayRecord(sourceId, overlay);
  }

  /**
   * Remove a text overlay from all scenes it belongs to.
   */
  removeTextOverlay(sourceId: string): void {
    this.removeOverlay(sourceId);
  }

  // ============================================================================
  // Image Overlays
  // ============================================================================

  /**
   * Add an image overlay to a scene. A File or Blob is decoded in the browser
   * and stays there; a URL is fetched by the browser.
   */
  async addImageOverlay(
    sceneId: string,
    image: Blob | string,
    placement?: Partial<OverlayPlacement>
  ): Promise<{ layerId: string; sourceId: string }> {
    if (!this.scenes.has(sceneId)) throw new Error(`Scene not found: ${sceneId}`);

    const sourceId = `img-${Date.now()}-${Math.random().toString(36).slice(2, 9)}`;
    const bitmap = await this.decodeImage(image);
    const aspect = bitmap.width / bitmap.height;

    this.sendToWorker({ type: "sourceImage", sourceId, bitmap }, [bitmap]);

    return this.addOverlayLayer(sceneId, sourceId, {
      kind: "image",
      label: typeof image === "string" ? image : image instanceof File ? image.name : "Image",
      aspect,
      placement: { ...DEFAULT_IMAGE_PLACEMENT, ...placement },
    });
  }

  /**
   * Add a persistent overlay that is automatically added to every scene
   * (existing and future). Useful for watermarks/logos.
   */
  async addPersistentOverlay(
    imageUrl: string,
    transform?: Partial<LayerTransform>
  ): Promise<string> {
    const sourceId = `persist-${Date.now()}-${Math.random().toString(36).slice(2, 9)}`;
    await this.bindImageSource(sourceId, imageUrl);

    this.persistentOverlays.set(sourceId, { sourceId, transform });

    // Add to all existing scenes
    for (const [sceneId, _scene] of this.scenes) {
      const defaultTransform: Partial<LayerTransform> = {
        x: 0,
        y: 0,
        width: 1,
        height: 1,
        opacity: 1,
        ...transform,
      };
      this.addLayer(sceneId, sourceId, defaultTransform, { kind: "overlay" });
    }

    return sourceId;
  }

  /**
   * Remove a persistent overlay from all scenes.
   */
  removePersistentOverlay(sourceId: string): void {
    this.persistentOverlays.delete(sourceId);

    for (const [sceneId, scene] of this.scenes) {
      const idx = scene.layers.findIndex((l) => l.sourceId === sourceId);
      if (idx !== -1) {
        const layerId = scene.layers[idx].id;
        scene.layers.splice(idx, 1);
        this.updateSceneInWorker(scene);
        this.emit("layerRemoved", { sceneId, layerId });
      }
    }
    this.releaseSource(sourceId);
  }

  // ============================================================================
  // Filters
  // ============================================================================

  /**
   * Apply a filter to a layer
   * Note: Only supported with WebGL or WebGPU renderer
   */
  applyFilter(layerId: string, filter: FilterConfig): void {
    this.sendToWorker({ type: "applyFilter", layerId, filter });
  }

  // ============================================================================
  // Output
  // ============================================================================

  /**
   * Get the output MediaStreamTrack for the composited video
   */
  getOutputTrack(): MediaStreamTrack | null {
    if (!this.outputCanvas) return null;

    // Capture from the canvas at the configured frame rate
    if (!this.outputStream) {
      this.outputStream = this.outputCanvas.captureStream(this.config.frameRate);
    }

    return this.outputStream.getVideoTracks()[0] || null;
  }

  /**
   * Enable direct frame output — compositor posts VideoFrames via callback
   * instead of relying on captureStream(). This eliminates one encode-decode
   * cycle when used with the WebCodecs encoding path.
   *
   * @param callback Receives each composited VideoFrame. Caller must close() frames.
   */
  setFrameCallback(callback: ((frame: VideoFrame) => void) | null): void {
    this.frameCallback = callback;
    this.sendToWorker({ type: "setDirectFrameOutput", enabled: callback !== null });
  }

  /**
   * Get the output MediaStream
   */
  getOutputStream(): MediaStream | null {
    if (!this.outputCanvas) return null;

    if (!this.outputStream) {
      this.outputStream = this.outputCanvas.captureStream(this.config.frameRate);
    }

    return this.outputStream;
  }

  // ============================================================================
  // Stats & State
  // ============================================================================

  /**
   * Get the current renderer type
   */
  getRendererType(): RendererType {
    return this.config.renderer;
  }

  /**
   * Switch to a different renderer at runtime
   * The worker will destroy the old renderer and create a new one
   */
  setRenderer(rendererType: RendererType): void {
    this.config.renderer = rendererType;
    this.sendToWorker({ type: "setRenderer", renderer: rendererType });
  }

  /**
   * Get the latest stats from the compositor
   */
  getStats(): RendererStats {
    return this.lastStats;
  }

  /**
   * Get the compositor configuration
   */
  getConfig(): CompositorConfig {
    return { ...this.config };
  }

  /**
   * Update output configuration (resolution / frame rate).
   * Resizes the output canvas and reinitializes the renderer.
   */
  updateOutputConfig(
    config: Partial<Pick<CompositorConfig, "width" | "height" | "frameRate">>
  ): boolean {
    const nextWidth = config.width ?? this.config.width;
    const nextHeight = config.height ?? this.config.height;
    const nextFrameRate = config.frameRate ?? this.config.frameRate;

    const changed =
      nextWidth !== this.config.width ||
      nextHeight !== this.config.height ||
      nextFrameRate !== this.config.frameRate;

    if (!changed) return false;

    this.config.width = nextWidth;
    this.config.height = nextHeight;
    this.config.frameRate = nextFrameRate;

    // Canvas size must be updated in the worker (OffscreenCanvas after transfer).
    // Updating the HTMLCanvasElement here throws once control is transferred.
    this.sendToWorker({
      type: "resize",
      width: nextWidth,
      height: nextHeight,
      frameRate: nextFrameRate,
    });

    return true;
  }

  /**
   * Check if the compositor is initialized
   */
  isInitialized(): boolean {
    return this.workerReady;
  }

  // ============================================================================
  // Cleanup
  // ============================================================================

  /**
   * Destroy the SceneManager and release all resources
   */
  destroy(): void {
    this.settlePendingTransitions();

    // Stop all frame processors and cancel readers
    for (const [sourceId] of this.frameProcessors) {
      this.unbindSource(sourceId);
    }
    this.frameProcessors.clear();
    this.frameReaders.clear();

    // Terminate the worker
    if (this.worker) {
      this.sendToWorker({ type: "destroy" });
      this.worker.terminate();
      this.worker = null;
    }

    // Clear scenes
    this.scenes.clear();
    this.activeSceneId = null;

    // Clear overlays
    this.overlays.clear();
    this.persistentOverlays.clear();

    // Clear output
    this.outputStream = null;
    this.outputCanvas = null;
    this.frameCallback = null;

    this.workerReady = false;
    this.removeAllListeners();
  }
}
