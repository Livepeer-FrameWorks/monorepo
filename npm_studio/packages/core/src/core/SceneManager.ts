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

import { TypedEventEmitter } from './EventEmitter';
import type {
  Scene,
  Layer,
  LayerTransform,
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
} from '../types';
import { DEFAULT_LAYER_TRANSFORM, DEFAULT_COMPOSITOR_CONFIG } from '../types';
import { applyLayout, createDefaultLayoutConfig } from './layouts';
import { createDefaultTransitionConfig } from './TransitionEngine';

// ============================================================================
// SceneManager Class
// ============================================================================

/**
 * Default layout transition configuration
 */
const DEFAULT_LAYOUT_TRANSITION: LayoutTransitionConfig = {
  durationMs: 300,
  easing: 'ease-out',
};

export class SceneManager extends TypedEventEmitter<SceneManagerEvents> {
  private scenes: Map<string, Scene> = new Map();
  private activeSceneId: string | null = null;
  private config: CompositorConfig;
  private defaultTransition: TransitionConfig;
  private defaultLayoutTransition: LayoutTransitionConfig = DEFAULT_LAYOUT_TRANSITION;
  private currentLayout: LayoutConfig;
  private isAnimating = false;

  // Worker communication
  private worker: Worker | null = null;
  private workerReady = false;
  private pendingMessages: CompositorMainToWorker[] = [];

  // Frame extraction
  // eslint-disable-next-line @typescript-eslint/no-explicit-any
  private frameProcessors: Map<string, any> = new Map();

  // Output
  private outputCanvas: HTMLCanvasElement | null = null;
  private outputStream: MediaStream | null = null;

  // Stats
  private lastStats: RendererStats = { fps: 0, frameTimeMs: 0 };

  constructor(config?: Partial<CompositorConfig>) {
    super();
    this.config = { ...DEFAULT_COMPOSITOR_CONFIG, ...config };
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

    const workerPaths = [
      // Preferred: packaged worker relative to built module URL
      new URL('../workers/compositor.worker.js', import.meta.url).href,
      // Vite dev server with pnpm workspace symlinks
      '/node_modules/@livepeer-frameworks/streamcrafter-core/dist/workers/compositor.worker.js',
      // Public folder paths
      '/workers/compositor.worker.js',
      './workers/compositor.worker.js',
    ];

    console.log('[SceneManager] Trying fallback worker paths:', workerPaths);

    for (const path of workerPaths) {
      if (this.worker) break;

      try {
        console.log(`[SceneManager] Trying worker path: ${path}`);

        // Worker constructor doesn't throw on 404, need to use a test approach
        const testWorker =
          (() => {
            try {
              return new Worker(path, { type: 'module' });
            } catch {
              return new Worker(path);
            }
          })();

        // Wait for either successful message or error
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

          // If worker starts sending messages, it loaded successfully
          testWorker.onmessage = () => {
            clearTimeout(timeout);
            resolve(true);
          };

          // Also check if we get a ready response quickly
          // Some workers might not send immediate messages, give a short grace period
          setTimeout(() => {
            // If we haven't errored by now, assume it loaded
            clearTimeout(timeout);
            resolve(true);
          }, 500);
        });

        if (success) {
          // Terminate test worker and create the real one
          testWorker.terminate();
          try {
            this.worker = new Worker(path, { type: 'module' });
          } catch {
            this.worker = new Worker(path);
          }
          console.log(`[SceneManager] Worker loaded from: ${path}`);
          break;
        } else {
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
        `Last error: ${lastError?.message ?? 'unknown'}`
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
    console.log('[SceneManager] initialize() called');

    if (this.worker) {
      throw new Error('SceneManager already initialized');
    }

    // Create output canvas
    console.log('[SceneManager] Creating output canvas', { width: this.config.width, height: this.config.height });
    this.outputCanvas = document.createElement('canvas');
    this.outputCanvas.width = this.config.width;
    this.outputCanvas.height = this.config.height;

    // Get OffscreenCanvas for worker
    const offscreen = this.outputCanvas.transferControlToOffscreen();
    console.log('[SceneManager] Created OffscreenCanvas');

    // Create the worker using the same strategy as the player
    console.log('[SceneManager] Initializing worker...');
    await this.initializeWorker();
    console.log('[SceneManager] Worker initialized, waiting for ready...');

    // Create a promise that resolves when worker sends 'ready'
    const readyPromise = new Promise<void>((resolve, reject) => {
      const timeout = setTimeout(() => {
        console.error('[SceneManager] Worker initialization timeout');
        reject(new Error('Compositor worker initialization timeout'));
      }, 10000); // 10 second timeout

      // Set up message handler (worker is guaranteed non-null after initializeWorker)
      this.worker!.onmessage = (e: MessageEvent<CompositorWorkerToMain>) => {
        console.log('[SceneManager] Worker message:', e.data.type);
        if (e.data.type === 'ready') {
          clearTimeout(timeout);
          resolve();
        }
        this.handleWorkerMessage(e.data);
      };

      this.worker!.onerror = (e: ErrorEvent) => {
        console.error('[SceneManager] Worker error:', e.message);
        clearTimeout(timeout);
        reject(new Error(e.message));
      };
    });

    // Initialize the worker with the offscreen canvas
    // Note: Send directly (not via sendToWorker) because workerReady is still false
    console.log('[SceneManager] Sending init message to worker');
    this.worker!.postMessage(
      { type: 'init', config: this.config, canvas: offscreen },
      [offscreen]
    );

    // Wait for worker to be ready
    await readyPromise;
    console.log('[SceneManager] Worker is ready');

    // Create default scene
    console.log('[SceneManager] Creating default scene');
    const defaultScene = this.createScene('Default');
    console.log('[SceneManager] Setting active scene:', defaultScene.id);
    this.setActiveScene(defaultScene.id);
    console.log('[SceneManager] Initialize complete');
  }

  /**
   * Handle messages from the compositor worker
   */
  private handleWorkerMessage(message: CompositorWorkerToMain): void {
    switch (message.type) {
      case 'ready':
        this.workerReady = true;
        // Send any pending messages
        for (const msg of this.pendingMessages) {
          this.worker?.postMessage(msg);
        }
        this.pendingMessages = [];
        break;

      case 'stats':
        this.lastStats = message.stats;
        this.emit('statsUpdate', { stats: message.stats });
        break;

      case 'transitionComplete':
        this.emit('transitionCompleted', { sceneId: message.sceneId });
        break;

      case 'layoutAnimationComplete': {
        this.isAnimating = false;
        // Update local scene with the final layout
        const activeScene = this.getActiveScene();
        if (activeScene) {
          const sourceIds = activeScene.layers
            .filter(l => l.visible)
            .map((l) => l.sourceId);
          activeScene.layers = applyLayout(this.currentLayout, sourceIds);
        }
        this.emit('layoutAnimationCompleted', { layout: this.currentLayout });
        break;
      }

      case 'rendererChanged':
        // Update the config with the actual renderer type (important for 'auto' mode)
        this.config.renderer = message.renderer;
        this.emit('rendererChanged', { renderer: message.renderer });
        break;

      case 'error':
        this.emit('error', { message: message.message });
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
  createScene(name: string, backgroundColor = '#000000'): Scene {
    const id = `scene-${Date.now()}-${Math.random().toString(36).slice(2, 9)}`;

    const scene: Scene = {
      id,
      name,
      layers: [],
      backgroundColor,
    };

    this.scenes.set(id, scene);
    this.emit('sceneCreated', { scene });

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
      throw new Error('Cannot delete the active scene');
    }

    this.scenes.delete(sceneId);
    this.emit('sceneDeleted', { sceneId });
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
    this.sendToWorker({ type: 'updateScene', scene });

    this.emit('sceneActivated', { scene, previousSceneId });
  }

  /**
   * Transition to a new scene
   */
  async transitionTo(sceneId: string, transition?: TransitionConfig): Promise<void> {
    const scene = this.scenes.get(sceneId);
    if (!scene) {
      throw new Error(`Scene not found: ${sceneId}`);
    }

    const transitionConfig = transition || this.defaultTransition;

    // First, send the target scene to the worker
    this.sendToWorker({ type: 'updateScene', scene });

    // Then start the transition
    this.sendToWorker({
      type: 'startTransition',
      transition: transitionConfig,
      toSceneId: sceneId,
    });

    const previousSceneId = this.activeSceneId;
    this.emit('transitionStarted', {
      fromSceneId: previousSceneId || '',
      toSceneId: sceneId,
      transition: transitionConfig,
    });

    // Update active scene ID
    this.activeSceneId = sceneId;
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
    transform?: Partial<LayerTransform>
  ): Layer {
    const scene = this.scenes.get(sceneId);
    if (!scene) {
      throw new Error(`Scene not found: ${sceneId}`);
    }

    const layerId = `layer-${Date.now()}-${Math.random().toString(36).slice(2, 9)}`;
    const maxZIndex = scene.layers.reduce((max, l) => Math.max(max, l.zIndex), -1);

    const layer: Layer = {
      id: layerId,
      sourceId,
      visible: true,
      locked: false,
      zIndex: maxZIndex + 1,
      transform: { ...DEFAULT_LAYER_TRANSFORM, ...transform },
      scalingMode: this.currentLayout.scalingMode ?? 'letterbox',
    };

    scene.layers.push(layer);
    this.updateSceneInWorker(scene);

    this.emit('layerAdded', { sceneId, layer });
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

    this.emit('layerRemoved', { sceneId, layerId });
  }

  /**
   * Update a layer's transform
   */
  updateLayerTransform(
    sceneId: string,
    layerId: string,
    transform: Partial<LayerTransform>
  ): void {
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

    this.emit('layerUpdated', { sceneId, layer });
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
    this.updateSceneInWorker(scene);

    this.emit('layerUpdated', { sceneId, layer });
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

    this.updateSceneInWorker(scene);
  }

  /**
   * Cycle the source order in the active scene (rotate layers)
   * @param direction - 'forward' moves first to last, 'backward' moves last to first
   */
  cycleSourceOrder(direction: 'forward' | 'backward' = 'forward'): void {
    const scene = this.getActiveScene();
    if (!scene || scene.layers.length < 2) {
      console.warn('[SceneManager] cycleSourceOrder: Need at least 2 layers');
      return;
    }

    // Debug: log state BEFORE cycle
    console.log('[SceneManager] cycleSourceOrder BEFORE:', {
      direction,
      layers: scene.layers.map(l => ({ id: l.id, sourceId: l.sourceId, zIndex: l.zIndex })),
    });

    // Get layer IDs sorted by current zIndex
    const sortedLayers = [...scene.layers].sort((a, b) => a.zIndex - b.zIndex);
    const layerIds = sortedLayers.map((l) => l.id);

    console.log('[SceneManager] sorted layerIds before rotate:', [...layerIds]);

    // Rotate the array
    if (direction === 'forward') {
      const first = layerIds.shift();
      if (first) layerIds.push(first);
    } else {
      const last = layerIds.pop();
      if (last) layerIds.unshift(last);
    }

    console.log('[SceneManager] layerIds after rotate:', [...layerIds]);

    // Apply the new Z-order
    this.reorderLayers(scene.id, layerIds);

    // Debug: log state AFTER reorder, BEFORE applyLayout
    console.log('[SceneManager] after reorderLayers:', {
      layers: scene.layers.map(l => ({ id: l.id, sourceId: l.sourceId, zIndex: l.zIndex })),
    });

    // Re-apply layout to update visual positions based on new Z-order
    if (this.currentLayout) {
      this.applyLayout(this.currentLayout, true, { durationMs: 200, easing: 'ease-out' });
    }

    // Debug: log state AFTER applyLayout
    console.log('[SceneManager] cycleSourceOrder AFTER applyLayout:', {
      layers: scene.layers.map(l => ({ id: l.id, sourceId: l.sourceId, zIndex: l.zIndex })),
    });

    this.emit('layerUpdated', { sceneId: scene.id, layer: scene.layers[0] });
  }

  /**
   * Update scene in the worker
   */
  private updateSceneInWorker(scene: Scene): void {
    if (this.activeSceneId === scene.id) {
      this.sendToWorker({ type: 'updateScene', scene });
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
      console.warn('[SceneManager] applyLayout: No active scene');
      return;
    }

    this.currentLayout = layout;

    // Get source IDs from current layers sorted by zIndex (preserves cycle order)
    const sourceIds = [...scene.layers]
      .filter(l => l.visible)
      .sort((a, b) => a.zIndex - b.zIndex)
      .map((l) => l.sourceId);
    console.log('[SceneManager] applyLayout', {
      mode: layout.mode,
      sourceIds,
      currentLayerCount: scene.layers.length,
      animate,
    });

    // Generate new layers from layout
    const newLayers = applyLayout(layout, sourceIds);

    // Create target scene with new layers
    const targetScene: Scene = {
      ...scene,
      layers: newLayers,
    };

    // Always update scene.layers to keep state in sync
    scene.layers = newLayers;

    if (animate && newLayers.length > 0) {
      // Animate to the new layout
      const transitionConfig: LayoutTransitionConfig = {
        ...this.defaultLayoutTransition,
        ...transition,
      };

      this.isAnimating = true;
      this.emit('layoutAnimationStarted', { layout });
      this.sendToWorker({
        type: 'animateLayout',
        targetScene,
        transition: transitionConfig,
      });
    } else {
      // Instant update (no animation)
      this.updateSceneInWorker(scene);
    }

    // Also send layout update to worker (for metadata tracking)
    this.sendToWorker({ type: 'updateLayout', layout });
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
      throw new Error('No video track in stream');
    }

    // Create a MediaStreamTrackProcessor to extract frames
    // MediaStreamTrackProcessor is experimental and not in all TypeScript defs
    // eslint-disable-next-line @typescript-eslint/no-explicit-any
    const MediaStreamTrackProcessorCtor = (globalThis as any).MediaStreamTrackProcessor;
    if (!MediaStreamTrackProcessorCtor) {
      console.warn('[SceneManager] MediaStreamTrackProcessor not available, compositor will not work');
      return;
    }

    const processor = new MediaStreamTrackProcessorCtor({ track: videoTrack });
    this.frameProcessors.set(sourceId, processor);

    // Read frames and send to worker
    const reader = processor.readable.getReader();

    const readFrame = async (): Promise<void> => {
      try {
        const { done, value } = await reader.read();
        if (done || !value) return;

        // Cast to VideoFrame since we know it's a video track processor
        const frame = value as VideoFrame;

        // Send frame to worker (transfer ownership)
        this.sendToWorker(
          { type: 'sourceFrame', sourceId, frame },
          [frame]
        );

        // Continue reading
        readFrame();
      } catch (error) {
        console.error('[SceneManager] Frame read error:', error);
      }
    };

    readFrame();
  }

  /**
   * Unbind a source from the compositor
   */
  unbindSource(sourceId: string): void {
    const processor = this.frameProcessors.get(sourceId);
    if (processor) {
      // Processor will be garbage collected
      this.frameProcessors.delete(sourceId);
    }
  }

  /**
   * Bind an image source to the compositor
   */
  async bindImageSource(sourceId: string, imageUrl: string): Promise<void> {
    const response = await fetch(imageUrl);
    const blob = await response.blob();
    const bitmap = await createImageBitmap(blob);

    this.sendToWorker(
      { type: 'sourceImage', sourceId, bitmap },
      [bitmap]
    );
  }

  // ============================================================================
  // Filters
  // ============================================================================

  /**
   * Apply a filter to a layer
   * Note: Only supported with WebGL or WebGPU renderer
   */
  applyFilter(layerId: string, filter: FilterConfig): void {
    this.sendToWorker({ type: 'applyFilter', layerId, filter });
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
    this.sendToWorker({ type: 'setRenderer', renderer: rendererType });
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
  updateOutputConfig(config: Partial<Pick<CompositorConfig, 'width' | 'height' | 'frameRate'>>): boolean {
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
    this.sendToWorker({ type: 'resize', width: nextWidth, height: nextHeight, frameRate: nextFrameRate });

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
    // Stop all frame processors
    for (const [sourceId] of this.frameProcessors) {
      this.unbindSource(sourceId);
    }
    this.frameProcessors.clear();

    // Terminate the worker
    if (this.worker) {
      this.sendToWorker({ type: 'destroy' });
      this.worker.terminate();
      this.worker = null;
    }

    // Clear scenes
    this.scenes.clear();
    this.activeSceneId = null;

    // Clear output
    this.outputStream = null;
    this.outputCanvas = null;

    this.workerReady = false;
    this.removeAllListeners();
  }
}
