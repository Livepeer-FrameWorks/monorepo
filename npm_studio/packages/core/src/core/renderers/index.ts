/**
 * Compositor Renderer System
 *
 * Multi-renderer architecture with auto-fallback chain:
 * WebGPU (if supported) → WebGL/PixiJS → Canvas2D (always works)
 *
 * Usage:
 * ```typescript
 * const renderer = createRenderer('auto');
 * await renderer.init(canvas, config);
 * renderer.renderScene(scene, frames);
 * const frame = renderer.captureFrame();
 * ```
 */

import type {
  Scene,
  CompositorConfig,
  TransitionType,
  FilterConfig,
  RendererType,
  RendererStats,
} from '../../types';

// ============================================================================
// Renderer Interface
// ============================================================================

/**
 * Common interface for all compositor renderers.
 * Implementations: Canvas2DRenderer, WebGLRenderer, WebGPURenderer
 */
export interface CompositorRenderer {
  /** Renderer type identifier */
  readonly type: RendererType;

  /** Whether this renderer is supported in the current environment */
  readonly isSupported: boolean;

  /**
   * Initialize the renderer with an OffscreenCanvas
   * @param canvas - OffscreenCanvas transferred to worker
   * @param config - Compositor configuration
   */
  init(canvas: OffscreenCanvas, config: CompositorConfig): Promise<void>;

  /**
   * Render a complete scene with all visible layers
   * @param scene - Scene to render
   * @param frames - Map of sourceId to VideoFrame or ImageBitmap
   */
  renderScene(scene: Scene, frames: Map<string, VideoFrame | ImageBitmap>): void;

  /**
   * Render a transition between two scene snapshots
   * @param from - ImageBitmap of the previous scene
   * @param to - ImageBitmap of the target scene
   * @param progress - Transition progress (0-1)
   * @param type - Type of transition effect
   */
  renderTransition(
    from: ImageBitmap,
    to: ImageBitmap,
    progress: number,
    type: TransitionType
  ): void;

  /**
   * Apply a filter effect to a specific layer
   * Note: Only WebGL and WebGPU renderers support filters
   * @param layerId - Target layer ID
   * @param filter - Filter configuration
   */
  applyFilter(layerId: string, filter: FilterConfig): void;

  /**
   * Capture the current canvas content as a VideoFrame
   * @returns VideoFrame of the rendered content
   */
  captureFrame(): VideoFrame;

  /**
   * Resize renderer after the canvas size changes.
   * @param config - Updated compositor config with new dimensions
   */
  resize(config: CompositorConfig): Promise<void> | void;

  /**
   * Get current renderer statistics
   * @returns Current FPS, frame time, and optional GPU memory usage
   */
  getStats(): RendererStats;

  /**
   * Clean up renderer resources
   */
  destroy(): void;
}

// ============================================================================
// Renderer Factory
// ============================================================================

// Forward declarations - actual implementations in separate files
let Canvas2DRenderer: new () => CompositorRenderer;
let WebGLRenderer: new () => CompositorRenderer;
let WebGPURenderer: new () => CompositorRenderer;

/**
 * Check if renderer implementations are registered
 */
let renderersRegistered = false;

/**
 * Register renderer implementations
 * Called automatically when renderer files are imported
 */
export function registerRenderer(
  type: 'canvas2d' | 'webgl' | 'webgpu',
  RendererClass: new () => CompositorRenderer
): void {
  switch (type) {
    case 'canvas2d':
      Canvas2DRenderer = RendererClass;
      break;
    case 'webgl':
      WebGLRenderer = RendererClass;
      break;
    case 'webgpu':
      WebGPURenderer = RendererClass;
      break;
  }
  renderersRegistered = true;
}

/**
 * Create a renderer instance based on preference and browser support
 *
 * @param preferred - Preferred renderer type or 'auto' for best available
 * @returns Appropriate renderer instance based on support and preference
 *
 * Auto-fallback chain: WebGPU → WebGL → Canvas2D
 */
export function createRenderer(preferred: RendererType = 'auto'): CompositorRenderer {
  if (!renderersRegistered) {
    // If no renderers registered yet, try to import them dynamically
    throw new Error(
      'No renderers registered. Import Canvas2DRenderer before calling createRenderer.'
    );
  }

  const fallbackChain: RendererType[] =
    preferred === 'auto'
      ? ['webgpu', 'webgl', 'canvas2d']
      : [preferred, 'webgl', 'canvas2d'];

  for (const type of fallbackChain) {
    const renderer = instantiateRenderer(type);
    if (renderer && renderer.isSupported) {
      return renderer;
    }
  }

  // Canvas2D should always work, but if it somehow doesn't...
  throw new Error('No supported renderer available');
}

/**
 * Instantiate a specific renderer type
 */
function instantiateRenderer(type: RendererType): CompositorRenderer | null {
  switch (type) {
    case 'webgpu':
      return WebGPURenderer ? new WebGPURenderer() : null;
    case 'webgl':
      return WebGLRenderer ? new WebGLRenderer() : null;
    case 'canvas2d':
      return Canvas2DRenderer ? new Canvas2DRenderer() : null;
    default:
      return null;
  }
}

/**
 * Check which renderers are supported in the current environment
 */
export function getSupportedRenderers(): RendererType[] {
  const supported: RendererType[] = [];

  // Canvas2D is always supported
  supported.push('canvas2d');

  // Check WebGL2 support
  if (typeof WebGL2RenderingContext !== 'undefined') {
    supported.push('webgl');
  }

  // Check WebGPU support (not available in workers before Chrome 115)
  if (typeof navigator !== 'undefined' && 'gpu' in navigator) {
    supported.push('webgpu');
  }

  return supported;
}

/**
 * Get the recommended renderer for the current environment
 */
export function getRecommendedRenderer(): RendererType {
  const supported = getSupportedRenderers();

  // Prefer WebGL for now (WebGPU not ready for production)
  if (supported.includes('webgl')) {
    return 'webgl';
  }

  return 'canvas2d';
}

// ============================================================================
// Re-exports
// ============================================================================

export type { CompositorConfig, RendererStats, RendererType } from '../../types';
