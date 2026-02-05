/**
 * Compositor Worker
 *
 * Web Worker that handles real-time video composition.
 * Runs off the main thread to ensure smooth compositing even when
 * the main thread is busy or the tab is in the background.
 *
 * Features:
 * - Multi-layer rendering with z-order
 * - Scene-based composition
 * - Smooth transitions between scenes
 * - Frame rate control (30/60 FPS)
 * - Stats reporting
 *
 * Data Flow:
 * Main Thread → (VideoFrames via postMessage) → Compositor Worker
 *     → (Render to OffscreenCanvas) → MediaStreamTrackGenerator → WebRTC
 */

import type {
  Scene,
  Layer,
  LayerTransform,
  CompositorConfig,
  CompositorMainToWorker,
  CompositorWorkerToMain,
  TransitionConfig,
  LayoutConfig,
  LayoutTransitionConfig,
  FilterConfig,
  RendererType,
  EasingType,
} from "../types";

// Import renderers (importing registers them with the factory)
import { Canvas2DRenderer } from "../core/renderers/Canvas2DRenderer";
import { WebGLRenderer } from "../core/renderers/WebGLRenderer";
import { WebGPURenderer } from "../core/renderers/WebGPURenderer";
import { createRenderer, type CompositorRenderer } from "../core/renderers";
import { TransitionEngine } from "../core/TransitionEngine";

// Reference renderers to ensure they're bundled and registered
void Canvas2DRenderer;
void WebGLRenderer;
void WebGPURenderer;

// ============================================================================
// Worker State
// ============================================================================

let canvas: OffscreenCanvas | null = null;
let renderer: CompositorRenderer | null = null;
let config: CompositorConfig | null = null;
const transitionEngine: TransitionEngine = new TransitionEngine();

// Current scene state
let currentScene: Scene | null = null;
let nextScene: Scene | null = null;

// Frame storage (latest frame from each source)
const frames: Map<string, VideoFrame | ImageBitmap> = new Map();

// Transition snapshots
let fromSceneSnapshot: ImageBitmap | null = null;
let toSceneSnapshot: ImageBitmap | null = null;

// Layout animation state
interface LayoutAnimationState {
  active: boolean;
  startTime: number;
  durationMs: number;
  easing: EasingType;
  fromTransforms: Map<string, LayerTransform>;
  toTransforms: Map<string, LayerTransform>;
  targetScene: Scene;
}
let layoutAnimation: LayoutAnimationState | null = null;

// Composition loop state
let compositionLoopId: number | null = null;
let isRunning = false;

// Stats tracking
let lastStatsTime = 0;
const STATS_INTERVAL = 1000; // Report stats every second

// ============================================================================
// Message Handlers
// ============================================================================

self.onmessage = async (e: MessageEvent<CompositorMainToWorker>) => {
  const message = e.data;

  try {
    switch (message.type) {
      case "init":
        await handleInit(message.config, message.canvas);
        break;

      case "updateScene":
        handleUpdateScene(message.scene);
        break;

      case "sourceFrame":
        handleSourceFrame(message.sourceId, message.frame);
        break;

      case "sourceImage":
        handleSourceImage(message.sourceId, message.bitmap);
        break;

      case "startTransition":
        handleStartTransition(message.transition, message.toSceneId);
        break;

      case "updateLayout":
        handleUpdateLayout(message.layout);
        break;

      case "animateLayout":
        handleAnimateLayout(message.targetScene, message.transition);
        break;

      case "resize":
        await handleResize(message.width, message.height, message.frameRate);
        break;

      case "setRenderer":
        handleSetRenderer(message.renderer);
        break;

      case "applyFilter":
        handleApplyFilter(message.layerId, message.filter);
        break;

      case "destroy":
        handleDestroy();
        break;

      default:
        console.warn(
          "[CompositorWorker] Unknown message type:",
          (message as unknown as { type: string }).type
        );
    }
  } catch (error) {
    sendMessage({
      type: "error",
      message: error instanceof Error ? error.message : String(error),
    });
  }
};

// ============================================================================
// Initialization
// ============================================================================

async function handleInit(cfg: CompositorConfig, offscreenCanvas: OffscreenCanvas): Promise<void> {
  config = cfg;
  canvas = offscreenCanvas;

  // Create the renderer using the factory with fallback chain
  // Try the requested renderer, then fall back through the chain
  const fallbackChain: RendererType[] =
    config.renderer === "auto"
      ? ["webgpu", "webgl", "canvas2d"]
      : [config.renderer, "webgl", "canvas2d"];

  let lastError: Error | null = null;

  for (const rendererType of fallbackChain) {
    try {
      console.log("[CompositorWorker] Trying", rendererType, "renderer...");
      renderer = createRenderer(rendererType);
      await renderer.init(canvas, config);
      console.log("[CompositorWorker]", rendererType, "renderer initialized successfully");
      break;
    } catch (error) {
      lastError = error instanceof Error ? error : new Error(String(error));
      console.warn("[CompositorWorker]", rendererType, "renderer failed:", lastError.message);
      renderer = null;

      // If this wasn't canvas2d, try the next one in the chain
      if (rendererType !== "canvas2d") {
        continue;
      }
    }
  }

  if (!renderer) {
    throw new Error(`All renderers failed. Last error: ${lastError?.message ?? "unknown"}`);
  }

  // Create default empty scene
  currentScene = {
    id: "default",
    name: "Default Scene",
    layers: [],
    backgroundColor: "#000000",
  };

  // Start the composition loop
  isRunning = true;
  startCompositionLoop();

  sendMessage({ type: "ready" });
  sendMessage({ type: "rendererChanged", renderer: renderer.type });
}

// ============================================================================
// Scene Management
// ============================================================================

function handleUpdateScene(scene: Scene): void {
  // If no transition is active, update the current scene directly
  if (!transitionEngine.isActive()) {
    currentScene = scene;
  } else {
    // During transition, update the "next" scene
    nextScene = scene;
  }
}

function handleStartTransition(transition: TransitionConfig, toSceneId: string): void {
  if (!currentScene || !renderer) return;

  // Capture current scene as "from" snapshot
  captureSceneSnapshot("from");

  // The "to" scene should have been set via updateScene
  if (nextScene && nextScene.id === toSceneId) {
    // Render the next scene and capture it
    const tempScene = currentScene;
    currentScene = nextScene;
    renderCurrentScene();
    captureSceneSnapshot("to");
    currentScene = tempScene;

    // Start the transition
    transitionEngine.start(currentScene.id, toSceneId, transition);
  } else {
    console.warn("[CompositorWorker] Next scene not found for transition:", toSceneId);
  }
}

function captureSceneSnapshot(target: "from" | "to"): void {
  if (!canvas) return;

  // Use createImageBitmap to capture the current canvas state
  createImageBitmap(canvas).then((bitmap) => {
    if (target === "from") {
      if (fromSceneSnapshot) fromSceneSnapshot.close();
      fromSceneSnapshot = bitmap;
    } else {
      if (toSceneSnapshot) toSceneSnapshot.close();
      toSceneSnapshot = bitmap;
    }
  });
}

function handleUpdateLayout(layout: LayoutConfig): void {
  // Layout is now handled entirely by SceneManager on the main thread.
  // SceneManager applies the layout, updates scene.layers, and sends updateScene.
  // This handler is kept for potential future use (e.g., storing layout metadata)
  // but should NOT recalculate layers here as that would overwrite the correct
  // layer data from SceneManager.
  console.log("[CompositorWorker] Layout updated:", layout.mode);
}

// ============================================================================
// Layout Animation
// ============================================================================

/**
 * Apply easing function to progress value
 */
function applyEasing(progress: number, easing: EasingType): number {
  const t = Math.max(0, Math.min(1, progress));

  switch (easing) {
    case "linear":
      return t;
    case "ease-in":
      return t * t;
    case "ease-out":
      return 1 - (1 - t) * (1 - t);
    case "ease-in-out":
      return t < 0.5 ? 2 * t * t : 1 - Math.pow(-2 * t + 2, 2) / 2;
    default:
      return t;
  }
}

/**
 * Interpolate between two transforms
 */
function interpolateTransform(
  from: LayerTransform,
  to: LayerTransform,
  progress: number
): LayerTransform {
  const lerp = (a: number, b: number) => a + (b - a) * progress;

  return {
    x: lerp(from.x, to.x),
    y: lerp(from.y, to.y),
    width: lerp(from.width, to.width),
    height: lerp(from.height, to.height),
    opacity: lerp(from.opacity, to.opacity),
    rotation: lerp(from.rotation, to.rotation),
    borderRadius: lerp(from.borderRadius, to.borderRadius),
    crop: {
      top: lerp(from.crop.top, to.crop.top),
      right: lerp(from.crop.right, to.crop.right),
      bottom: lerp(from.crop.bottom, to.crop.bottom),
      left: lerp(from.crop.left, to.crop.left),
    },
  };
}

function handleAnimateLayout(targetScene: Scene, transition: LayoutTransitionConfig): void {
  if (!currentScene) {
    // No current scene, just set the target directly
    currentScene = targetScene;
    return;
  }

  // Build maps of old and new transforms by sourceId
  const fromTransforms = new Map<string, LayerTransform>();
  const toTransforms = new Map<string, LayerTransform>();

  // Get transforms from current scene
  for (const layer of currentScene.layers) {
    fromTransforms.set(layer.sourceId, { ...layer.transform });
  }

  // Get transforms from target scene
  for (const layer of targetScene.layers) {
    toTransforms.set(layer.sourceId, { ...layer.transform });

    // If layer didn't exist before, use target as starting point
    if (!fromTransforms.has(layer.sourceId)) {
      fromTransforms.set(layer.sourceId, { ...layer.transform });
    }
  }

  // Start the animation
  layoutAnimation = {
    active: true,
    startTime: performance.now(),
    durationMs: transition.durationMs,
    easing: transition.easing,
    fromTransforms,
    toTransforms,
    targetScene,
  };

  console.log("[CompositorWorker] Layout animation started", {
    durationMs: transition.durationMs,
    easing: transition.easing,
    layerCount: targetScene.layers.length,
  });
}

/**
 * Update layout animation and return interpolated scene
 * Returns null if no animation is active
 */
function updateLayoutAnimation(): Scene | null {
  if (!layoutAnimation || !layoutAnimation.active || !currentScene) {
    return null;
  }

  const elapsed = performance.now() - layoutAnimation.startTime;
  const rawProgress = elapsed / layoutAnimation.durationMs;
  const progress = applyEasing(Math.min(rawProgress, 1), layoutAnimation.easing);

  // Create interpolated scene
  const interpolatedLayers: Layer[] = layoutAnimation.targetScene.layers.map((targetLayer) => {
    const fromTransform = layoutAnimation!.fromTransforms.get(targetLayer.sourceId);
    const toTransform = layoutAnimation!.toTransforms.get(targetLayer.sourceId);

    if (!fromTransform || !toTransform) {
      return targetLayer;
    }

    return {
      ...targetLayer,
      transform: interpolateTransform(fromTransform, toTransform, progress),
    };
  });

  const interpolatedScene: Scene = {
    ...layoutAnimation.targetScene,
    layers: interpolatedLayers,
  };

  // Check if animation is complete
  if (rawProgress >= 1) {
    // Animation complete - set final scene
    currentScene = layoutAnimation.targetScene;
    layoutAnimation = null;
    sendMessage({ type: "layoutAnimationComplete" });
    return null; // Return null to indicate we should use currentScene now
  }

  return interpolatedScene;
}

// ============================================================================
// Frame Handling
// ============================================================================

function handleSourceFrame(sourceId: string, frame: VideoFrame): void {
  // Close the old frame to prevent memory leaks
  const oldFrame = frames.get(sourceId);
  if (oldFrame && "close" in oldFrame) {
    (oldFrame as VideoFrame).close();
  }

  frames.set(sourceId, frame);
}

function handleSourceImage(sourceId: string, bitmap: ImageBitmap): void {
  // Close the old bitmap if it exists
  const oldBitmap = frames.get(sourceId);
  if (oldBitmap && "close" in oldBitmap) {
    oldBitmap.close();
  }

  frames.set(sourceId, bitmap);
}

// ============================================================================
// Renderer Control
// ============================================================================

async function handleSetRenderer(rendererType: RendererType): Promise<void> {
  if (!canvas || !config) {
    console.warn("[CompositorWorker] Cannot switch renderer - not initialized");
    return;
  }

  // If already using this renderer, do nothing
  if (renderer && renderer.type === rendererType) {
    return;
  }

  try {
    // Destroy old renderer
    if (renderer) {
      renderer.destroy();
    }

    // Create new renderer
    renderer = createRenderer(rendererType);
    await renderer.init(canvas, config);

    sendMessage({ type: "rendererChanged", renderer: renderer.type });
  } catch {
    // If requested renderer fails, fall back to Canvas2D
    console.warn(
      `[CompositorWorker] Failed to create ${rendererType} renderer, falling back to canvas2d`
    );
    renderer = createRenderer("canvas2d");
    await renderer.init(canvas, config);
    sendMessage({ type: "rendererChanged", renderer: "canvas2d" });
  }
}

async function handleResize(width: number, height: number, frameRate?: number): Promise<void> {
  if (!canvas || !config) {
    console.warn("[CompositorWorker] Cannot resize - not initialized");
    return;
  }

  config.width = width;
  config.height = height;
  if (frameRate !== undefined) {
    config.frameRate = frameRate;
  }

  canvas.width = width;
  canvas.height = height;

  const targetRenderer = renderer?.type ?? config.renderer;
  try {
    if (renderer) {
      await renderer.resize(config);
      return;
    }

    // If renderer wasn't initialized yet, create it now
    renderer = createRenderer(targetRenderer);
    await renderer.init(canvas, config);
    sendMessage({ type: "rendererChanged", renderer: renderer.type });
  } catch (error) {
    const message = error instanceof Error ? error.message : String(error);
    console.warn(`[CompositorWorker] Failed to resize with ${targetRenderer}: ${message}`);
    sendMessage({
      type: "error",
      message: `Failed to resize renderer (${targetRenderer}): ${message}`,
    });
  }
}

function handleApplyFilter(layerId: string, filter: FilterConfig): void {
  if (!renderer) return;
  renderer.applyFilter(layerId, filter);
}

// ============================================================================
// Composition Loop
// ============================================================================

function startCompositionLoop(): void {
  if (!config) return;

  const frameTime = 1000 / config.frameRate;
  let lastFrameTime = performance.now();

  function loop(): void {
    if (!isRunning) return;

    const now = performance.now();
    const elapsed = now - lastFrameTime;

    // Only render if enough time has passed for the target frame rate
    if (elapsed >= frameTime) {
      lastFrameTime = now - (elapsed % frameTime);

      if (transitionEngine.isActive()) {
        renderTransition();
      } else {
        renderCurrentScene();
      }

      // Send stats periodically
      if (now - lastStatsTime >= STATS_INTERVAL) {
        sendStats();
        lastStatsTime = now;
      }
    }

    // Use setTimeout for timing (more reliable in workers than requestAnimationFrame)
    compositionLoopId = self.setTimeout(
      loop,
      Math.max(1, frameTime - (performance.now() - lastFrameTime))
    );
  }

  loop();
}

function stopCompositionLoop(): void {
  if (compositionLoopId !== null) {
    clearTimeout(compositionLoopId);
    compositionLoopId = null;
  }
}

function renderCurrentScene(): void {
  if (!renderer || !currentScene) return;

  // Check for layout animation
  const interpolatedScene = updateLayoutAnimation();
  const sceneToRender = interpolatedScene || currentScene;

  renderer.renderScene(sceneToRender, frames);
}

function renderTransition(): void {
  if (!renderer || !fromSceneSnapshot || !toSceneSnapshot) return;

  // Update transition progress
  const state = transitionEngine.update();

  if (state) {
    renderer.renderTransition(fromSceneSnapshot, toSceneSnapshot, state.progress, state.type);

    // Check if transition completed
    if (!state.active) {
      completeTransition();
    }
  }
}

function completeTransition(): void {
  if (nextScene) {
    currentScene = nextScene;
    nextScene = null;
  }

  // Clean up snapshots
  if (fromSceneSnapshot) {
    fromSceneSnapshot.close();
    fromSceneSnapshot = null;
  }
  if (toSceneSnapshot) {
    toSceneSnapshot.close();
    toSceneSnapshot = null;
  }

  // Notify main thread
  sendMessage({
    type: "transitionComplete",
    sceneId: currentScene?.id ?? "",
  });
}

// ============================================================================
// Stats Reporting
// ============================================================================

function sendStats(): void {
  if (!renderer) return;

  const stats = renderer.getStats();
  sendMessage({ type: "stats", stats });
}

// ============================================================================
// Cleanup
// ============================================================================

function handleDestroy(): void {
  isRunning = false;
  stopCompositionLoop();

  // Close all frames
  for (const frame of frames.values()) {
    if ("close" in frame) {
      frame.close();
    }
  }
  frames.clear();

  // Clean up snapshots
  if (fromSceneSnapshot) {
    fromSceneSnapshot.close();
    fromSceneSnapshot = null;
  }
  if (toSceneSnapshot) {
    toSceneSnapshot.close();
    toSceneSnapshot = null;
  }

  // Destroy renderer
  if (renderer) {
    renderer.destroy();
    renderer = null;
  }

  canvas = null;
  config = null;
  currentScene = null;
  nextScene = null;
}

// ============================================================================
// Communication Helpers
// ============================================================================

function sendMessage(message: CompositorWorkerToMain): void {
  self.postMessage(message);
}
