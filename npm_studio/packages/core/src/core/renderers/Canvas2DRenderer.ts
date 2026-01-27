/**
 * Canvas2D Renderer
 *
 * Universal fallback renderer using the Canvas 2D API.
 * Works in all browsers that support OffscreenCanvas.
 *
 * Features:
 * - Layer compositing with z-order
 * - Transform: position, size, rotation, opacity
 * - Border radius (rounded corners)
 * - Cropping
 * - Transitions: fade, slide (left/right/up/down), cut
 *
 * Limitations:
 * - No real-time filters (blur, color correction)
 * - No GPU-accelerated effects
 *
 * Performance:
 * - GPU-accelerated via compositor (drawImage is fast)
 * - `desynchronized: true` reduces input latency
 */

import type {
  Scene,
  Layer,
  CompositorConfig,
  TransitionType,
  FilterConfig,
  RendererType,
  RendererStats,
} from "../../types";
import { registerRenderer, type CompositorRenderer } from "./index";

export class Canvas2DRenderer implements CompositorRenderer {
  readonly type: RendererType = "canvas2d";
  readonly isSupported = true; // Always available

  private canvas!: OffscreenCanvas;
  private ctx!: OffscreenCanvasRenderingContext2D;
  private config!: CompositorConfig;

  // Stats tracking
  private frameCount = 0;
  private lastFrameTime = 0;
  private fps = 0;
  private lastRenderTime = 0;

  async init(canvas: OffscreenCanvas, config: CompositorConfig): Promise<void> {
    this.canvas = canvas;
    this.config = config;

    const ctx = canvas.getContext("2d", {
      desynchronized: true, // Lower latency (no vsync wait)
      alpha: false, // Opaque canvas (faster)
      willReadFrequently: false, // Optimize for write-only
    });

    if (!ctx) {
      throw new Error("Failed to get 2D context from OffscreenCanvas");
    }

    this.ctx = ctx;
    this.lastFrameTime = performance.now();

    // Set up image smoothing for quality
    this.ctx.imageSmoothingEnabled = true;
    this.ctx.imageSmoothingQuality = "high";
  }

  renderScene(scene: Scene, frames: Map<string, VideoFrame | ImageBitmap>): void {
    const startTime = performance.now();

    // Clear with background color
    this.ctx.fillStyle = scene.backgroundColor || "#000000";
    this.ctx.fillRect(0, 0, this.config.width, this.config.height);

    // Sort layers by z-index and render
    const visibleLayers = scene.layers
      .filter((layer) => layer.visible)
      .sort((a, b) => a.zIndex - b.zIndex);

    for (const layer of visibleLayers) {
      const frame = frames.get(layer.sourceId);
      if (frame) {
        this.renderLayer(layer, frame);
      }
    }

    // Track stats
    this.lastRenderTime = performance.now() - startTime;
    this.updateStats();
  }

  resize(config: CompositorConfig): void {
    this.config = config;

    const ctx = this.canvas.getContext("2d", {
      desynchronized: true,
      alpha: false,
      willReadFrequently: false,
    });

    if (!ctx) {
      throw new Error("Failed to get 2D context from OffscreenCanvas");
    }

    this.ctx = ctx;
    this.ctx.imageSmoothingEnabled = true;
    this.ctx.imageSmoothingQuality = "high";
  }

  private renderLayer(layer: Layer, frame: VideoFrame | ImageBitmap): void {
    const { x, y, width, height, opacity, rotation, borderRadius, crop } = layer.transform;
    const scalingMode = layer.scalingMode || "letterbox";

    // Convert relative coordinates to pixels
    const px = x * this.config.width;
    const py = y * this.config.height;
    const pw = width * this.config.width;
    const ph = height * this.config.height;

    // Save context state
    this.ctx.save();

    // Apply opacity
    this.ctx.globalAlpha = Math.max(0, Math.min(1, opacity));

    // Apply rotation around center
    if (rotation !== 0) {
      const centerX = px + pw / 2;
      const centerY = py + ph / 2;
      this.ctx.translate(centerX, centerY);
      this.ctx.rotate((rotation * Math.PI) / 180);
      this.ctx.translate(-centerX, -centerY);
    }

    // Apply border radius clipping
    if (borderRadius > 0) {
      this.roundedRect(px, py, pw, ph, borderRadius);
      this.ctx.clip();
    }

    // Calculate source crop rectangle (user-defined crop)
    const frameWidth = this.getFrameWidth(frame);
    const frameHeight = this.getFrameHeight(frame);

    const sx = crop.left * frameWidth;
    const sy = crop.top * frameHeight;
    const sw = frameWidth * (1 - crop.left - crop.right);
    const sh = frameHeight * (1 - crop.top - crop.bottom);

    // Calculate destination based on scaling mode
    const { dx, dy, dw, dh, sxFinal, syFinal, swFinal, shFinal } = this.calculateScaling(
      scalingMode,
      sx,
      sy,
      sw,
      sh,
      px,
      py,
      pw,
      ph
    );

    // Draw the frame
    this.ctx.drawImage(frame, sxFinal, syFinal, swFinal, shFinal, dx, dy, dw, dh);

    // Restore context state
    this.ctx.restore();
  }

  /**
   * Calculate source and destination rectangles based on scaling mode
   */
  private calculateScaling(
    mode: "stretch" | "letterbox" | "crop",
    sx: number,
    sy: number,
    sw: number,
    sh: number,
    dx: number,
    dy: number,
    dw: number,
    dh: number
  ): {
    sxFinal: number;
    syFinal: number;
    swFinal: number;
    shFinal: number;
    dx: number;
    dy: number;
    dw: number;
    dh: number;
  } {
    const sourceAspect = sw / sh;
    const destAspect = dw / dh;

    switch (mode) {
      case "stretch":
        // Stretch source to fill destination (may distort)
        return {
          sxFinal: sx,
          syFinal: sy,
          swFinal: sw,
          shFinal: sh,
          dx,
          dy,
          dw,
          dh,
        };

      case "letterbox": {
        // Fit source within destination, preserving aspect ratio
        // Add black bars if needed (handled by layer background)
        let newDw: number, newDh: number;

        if (sourceAspect > destAspect) {
          // Source is wider - fit to width, add top/bottom bars
          newDw = dw;
          newDh = dw / sourceAspect;
        } else {
          // Source is taller - fit to height, add left/right bars
          newDh = dh;
          newDw = dh * sourceAspect;
        }

        const newDx = dx + (dw - newDw) / 2;
        const newDy = dy + (dh - newDh) / 2;

        return {
          sxFinal: sx,
          syFinal: sy,
          swFinal: sw,
          shFinal: sh,
          dx: newDx,
          dy: newDy,
          dw: newDw,
          dh: newDh,
        };
      }

      case "crop": {
        // Fill destination, preserving aspect ratio, crop overflow
        let cropSx = sx,
          cropSy = sy,
          cropSw = sw,
          cropSh = sh;

        if (sourceAspect > destAspect) {
          // Source is wider - crop sides
          const targetSw = sh * destAspect;
          const cropAmount = (sw - targetSw) / 2;
          cropSx = sx + cropAmount;
          cropSw = targetSw;
        } else {
          // Source is taller - crop top/bottom
          const targetSh = sw / destAspect;
          const cropAmount = (sh - targetSh) / 2;
          cropSy = sy + cropAmount;
          cropSh = targetSh;
        }

        return {
          sxFinal: cropSx,
          syFinal: cropSy,
          swFinal: cropSw,
          shFinal: cropSh,
          dx,
          dy,
          dw,
          dh,
        };
      }

      default:
        return {
          sxFinal: sx,
          syFinal: sy,
          swFinal: sw,
          shFinal: sh,
          dx,
          dy,
          dw,
          dh,
        };
    }
  }

  private getFrameWidth(frame: VideoFrame | ImageBitmap): number {
    return "displayWidth" in frame ? frame.displayWidth : frame.width;
  }

  private getFrameHeight(frame: VideoFrame | ImageBitmap): number {
    return "displayHeight" in frame ? frame.displayHeight : frame.height;
  }

  /**
   * Draw a rounded rectangle path
   */
  private roundedRect(x: number, y: number, w: number, h: number, r: number): void {
    // Clamp radius to half of shortest side
    const radius = Math.min(r, w / 2, h / 2);

    this.ctx.beginPath();
    this.ctx.moveTo(x + radius, y);
    this.ctx.lineTo(x + w - radius, y);
    this.ctx.quadraticCurveTo(x + w, y, x + w, y + radius);
    this.ctx.lineTo(x + w, y + h - radius);
    this.ctx.quadraticCurveTo(x + w, y + h, x + w - radius, y + h);
    this.ctx.lineTo(x + radius, y + h);
    this.ctx.quadraticCurveTo(x, y + h, x, y + h - radius);
    this.ctx.lineTo(x, y + radius);
    this.ctx.quadraticCurveTo(x, y, x + radius, y);
    this.ctx.closePath();
  }

  renderTransition(
    from: ImageBitmap,
    to: ImageBitmap,
    progress: number,
    type: TransitionType
  ): void {
    // Clamp progress
    const p = Math.max(0, Math.min(1, progress));

    switch (type) {
      case "fade":
        this.renderFadeTransition(from, to, p);
        break;

      case "slide-left":
        this.renderSlideTransition(from, to, p, "left");
        break;

      case "slide-right":
        this.renderSlideTransition(from, to, p, "right");
        break;

      case "slide-up":
        this.renderSlideTransition(from, to, p, "up");
        break;

      case "slide-down":
        this.renderSlideTransition(from, to, p, "down");
        break;

      case "cut":
      default:
        // Instant cut - just show the target
        this.ctx.drawImage(to, 0, 0, this.config.width, this.config.height);
        break;
    }
  }

  private renderFadeTransition(from: ImageBitmap, to: ImageBitmap, progress: number): void {
    // Draw "from" scene at full opacity
    this.ctx.globalAlpha = 1;
    this.ctx.drawImage(from, 0, 0, this.config.width, this.config.height);

    // Overlay "to" scene with increasing opacity
    this.ctx.globalAlpha = progress;
    this.ctx.drawImage(to, 0, 0, this.config.width, this.config.height);

    // Reset alpha
    this.ctx.globalAlpha = 1;
  }

  private renderSlideTransition(
    from: ImageBitmap,
    to: ImageBitmap,
    progress: number,
    direction: "left" | "right" | "up" | "down"
  ): void {
    const w = this.config.width;
    const h = this.config.height;

    switch (direction) {
      case "left": {
        // "from" slides out to the left, "to" slides in from the right
        const offset = progress * w;
        this.ctx.drawImage(from, -offset, 0, w, h);
        this.ctx.drawImage(to, w - offset, 0, w, h);
        break;
      }

      case "right": {
        // "from" slides out to the right, "to" slides in from the left
        const offset = progress * w;
        this.ctx.drawImage(from, offset, 0, w, h);
        this.ctx.drawImage(to, -w + offset, 0, w, h);
        break;
      }

      case "up": {
        // "from" slides out to the top, "to" slides in from the bottom
        const offset = progress * h;
        this.ctx.drawImage(from, 0, -offset, w, h);
        this.ctx.drawImage(to, 0, h - offset, w, h);
        break;
      }

      case "down": {
        // "from" slides out to the bottom, "to" slides in from the top
        const offset = progress * h;
        this.ctx.drawImage(from, 0, offset, w, h);
        this.ctx.drawImage(to, 0, -h + offset, w, h);
        break;
      }
    }
  }

  applyFilter(_layerId: string, _filter: FilterConfig): void {
    // Canvas2D doesn't support real-time filters efficiently
    // Filters would require reading pixels back, processing, and redrawing
    // This is too slow for real-time video compositing
    console.warn(
      "[Canvas2DRenderer] Filters not supported. Use WebGL or WebGPU renderer for filter effects."
    );
  }

  captureFrame(): VideoFrame {
    // Create a VideoFrame from the canvas content
    return new VideoFrame(this.canvas, {
      timestamp: performance.now() * 1000, // microseconds
    });
  }

  private updateStats(): void {
    this.frameCount++;
    const now = performance.now();
    const elapsed = now - this.lastFrameTime;

    // Update FPS every second
    if (elapsed >= 1000) {
      this.fps = (this.frameCount * 1000) / elapsed;
      this.frameCount = 0;
      this.lastFrameTime = now;
    }
  }

  getStats(): RendererStats {
    return {
      fps: Math.round(this.fps),
      frameTimeMs: this.lastRenderTime,
      // Canvas2D doesn't have GPU memory tracking
    };
  }

  destroy(): void {
    // Canvas2D context doesn't need explicit cleanup
    // Just clear any references
    this.canvas = null as unknown as OffscreenCanvas;
    this.ctx = null as unknown as OffscreenCanvasRenderingContext2D;
  }
}

// Register this renderer with the factory
registerRenderer("canvas2d", Canvas2DRenderer);
